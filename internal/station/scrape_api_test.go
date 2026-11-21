package station

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestEnqueueScrapeCreatesProviderItemsOnce(t *testing.T) {
	service := newTransferTestService(t)
	file := ServerFile{ServerFileID: "sf1_scrape-test", FileName: "song.flac", Title: "Song", Artist: "Artist"}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	count, err := service.EnqueueScrape([]string{file.ServerFileID})
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("created %d scrape items, want 4", count)
	}
	count, err = service.EnqueueScrape([]string{file.ServerFileID})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("duplicate enqueue created %d items", count)
	}
}

func TestEnqueueScrapeDefersLyricsUntilThereIsATitle(t *testing.T) {
	service := newTransferTestService(t)
	file := ServerFile{ServerFileID: "sf1_deferred-lyrics", FileName: "song.flac"}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	count, err := service.EnqueueScrape([]string{file.ServerFileID})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 { // MusicBrainz and Cover Art Archive; LRCLIB has no valid query yet.
		t.Fatalf("created %d scrape items, want 2", count)
	}
	var lyricsItem ScrapeItem
	if err := service.db.Where("server_file_id = ? AND provider = ?", file.ServerFileID, "lrclib").First(&lyricsItem).Error; err == nil {
		t.Fatalf("created LRCLIB task without a title: %+v", lyricsItem)
	}

	data, err := json.Marshal(metadataSnapshot{Title: "Song", Artist: "Artist", Album: "Album"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.appendScrapeResource(file.ServerFileID, "metadata", "json", string(data), "musicbrainz", ""); err != nil {
		t.Fatal(err)
	}
	count, err = service.EnqueueScrape([]string{file.ServerFileID})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("created %d scrape items after MusicBrainz metadata, want 2", count)
	}
	if err := service.db.Where("server_file_id = ? AND provider = ? AND state = ?", file.ServerFileID, "lrclib", ScrapePending).First(&lyricsItem).Error; err != nil {
		t.Fatalf("LRCLIB task was not queued after MusicBrainz metadata: %v", err)
	}
}

func TestReprocessScrapeProviderScansLibraryAndSkipsCompletedWork(t *testing.T) {
	service := newTransferTestService(t)
	file := ServerFile{ServerFileID: "sf1-reprocess-provider", Title: "Song", Artist: "Artist"}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	lyrics := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "lrclib", State: ScrapeFailed, FailureCount: 3, LastError: "old error", AddedCount: 2}
	musicbrainz := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "musicbrainz", State: ScrapeFailed}
	noMatch := ScrapeItem{ServerFileID: "sf1-reprocess-no-match", Provider: "lrclib", State: ScrapeNoMatch, LastError: "lyrics not found"}
	noMatchFile := ServerFile{ServerFileID: noMatch.ServerFileID, Title: "No match song", Artist: "Artist"}
	newFile := ServerFile{ServerFileID: "sf1-reprocess-new", Title: "New song", Artist: "Artist", Album: "Album"}
	pendingFile := ServerFile{ServerFileID: "sf1-reprocess-pending", Title: "Pending song", Artist: "Artist"}
	pending := ScrapeItem{ServerFileID: pendingFile.ServerFileID, Provider: "lrclib", State: ScrapePending}
	if err := service.db.Create(&lyrics).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&musicbrainz).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&noMatch).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&noMatchFile).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&newFile).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&pendingFile).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&pending).Error; err != nil {
		t.Fatal(err)
	}
	count, err := service.ReprocessScrapeProvider("lrclib")
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("started %d items, want 3", count)
	}
	if err := service.db.First(&lyrics, lyrics.ID).Error; err != nil {
		t.Fatal(err)
	}
	if lyrics.State != ScrapePending || lyrics.FailureCount != 0 || lyrics.LastError != "" || lyrics.AddedCount != 0 {
		t.Fatalf("lyrics task was not reset: %+v", lyrics)
	}
	if err := service.db.First(&musicbrainz, musicbrainz.ID).Error; err != nil {
		t.Fatal(err)
	}
	if musicbrainz.State != ScrapeFailed {
		t.Fatalf("unselected provider task was reset: %+v", musicbrainz)
	}
	if err := service.db.First(&noMatch, noMatch.ID).Error; err != nil {
		t.Fatal(err)
	}
	if noMatch.State != ScrapePending {
		t.Fatalf("no-match task was not requeued: %+v", noMatch)
	}
	var newItem ScrapeItem
	if err := service.db.Where("server_file_id = ? AND provider = ?", newFile.ServerFileID, "lrclib").First(&newItem).Error; err != nil {
		t.Fatalf("new library file was not queued: %v", err)
	}
	if err := service.db.First(&pending, pending.ID).Error; err != nil || pending.RunID == "" {
		t.Fatalf("existing pending item was not attached to the run: item=%+v err=%v", pending, err)
	}
}

func TestMusicBrainzReprocessBackfillsMissingReleaseGroup(t *testing.T) {
	service := newTransferTestService(t)
	file := ServerFile{ServerFileID: "sf1-musicbrainz-release-group", Title: "Song", Artist: "Artist"}
	item := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "musicbrainz", State: ScrapeSuccess, ExternalID: "release-id"}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	_, count, err := service.StartScrapeProvider("musicbrainz")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("scheduled %d items, want missing release-group item", count)
	}
	if err := service.db.First(&item, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.State != ScrapePending {
		t.Fatalf("MusicBrainz item state = %q, want pending", item.State)
	}
}

func TestStopScrapeProviderPausesQueuedItemsOnly(t *testing.T) {
	service := newTransferTestService(t)
	items := []ScrapeItem{
		{ServerFileID: "sf1-stop-pending", Provider: "lrclib", State: ScrapePending},
		{ServerFileID: "sf1-stop-retry", Provider: "lrclib", State: ScrapeRetry},
		{ServerFileID: "sf1-stop-running", Provider: "lrclib", State: ScrapeRunning},
	}
	for index := range items {
		if err := service.db.Create(&items[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	count, err := service.StopScrapeProvider("lrclib")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("paused %d items, want 2", count)
	}
	if claimed, err := service.claimScrapeItem(); err != nil || claimed != nil {
		t.Fatalf("paused provider was claimed: item=%+v err=%v", claimed, err)
	}
	for _, expected := range []struct {
		id    uint
		state string
	}{{items[0].ID, ScrapePaused}, {items[1].ID, ScrapePaused}, {items[2].ID, ScrapeRunning}} {
		var item ScrapeItem
		if err := service.db.First(&item, expected.id).Error; err != nil {
			t.Fatal(err)
		}
		if item.State != expected.state {
			t.Fatalf("item %d state = %q, want %q", item.ID, item.State, expected.state)
		}
	}
}

func TestScrapeDailyStatsAndSongs(t *testing.T) {
	service := newTransferTestService(t)
	file := ServerFile{ServerFileID: "sf1-daily-stats", FileName: "song.flac", RelativePath: "Music/song.flac", Title: "Song", Artist: "Artist", Album: "Album"}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.appendScrapeResource(file.ServerFileID, "lyrics", "plain", "lyrics", "lrclib", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.appendScrapeCover(file.ServerFileID, []byte{0xff, 0xd8, 0xff, 0xd9}, "image/jpeg", "theaudiodb"); err != nil {
		t.Fatal(err)
	}
	latestCover := "\xff\xd8\xff\xe0new cover"
	if _, err := service.appendScrapeCover(file.ServerFileID, []byte(latestCover), "image/jpeg", "theaudiodb"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.appendScrapeCover(file.ServerFileID, []byte("\xff\xd8\xff\xe0other source"), "image/jpeg", "coverartarchive"); err != nil {
		t.Fatal(err)
	}

	stats, err := service.ScrapeDailyStats()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, stat := range stats {
		counts[stat.Provider+":"+stat.Kind] = stat.Count
	}
	if counts["lrclib:lyrics"] != 1 || counts["theaudiodb:cover"] != 1 {
		t.Fatalf("unexpected daily stats: %+v", stats)
	}
	songs, err := service.ScrapeDailyStatSongs("theaudiodb", "cover")
	if err != nil {
		t.Fatal(err)
	}
	if len(songs) != 1 || songs[0].ServerFileID != file.ServerFileID || songs[0].Title != "Song" {
		t.Fatalf("unexpected daily-stat songs: %+v", songs)
	}
	payload, err := json.Marshal(songs[0])
	if err != nil {
		t.Fatal(err)
	}
	var row struct {
		CoverURL string `json:"coverUrl"`
	}
	if err := json.Unmarshal(payload, &row); err != nil {
		t.Fatal(err)
	}
	resourceID, ok := strings.CutPrefix(row.CoverURL, "/api/station/resources/")
	if !ok || resourceID == "" {
		t.Fatalf("expected a scraped cover URL, got %q", row.CoverURL)
	}
	cover, mime, err := service.OpenCover(resourceID)
	if err != nil {
		t.Fatal(err)
	}
	defer cover.Close()
	data, err := io.ReadAll(cover)
	if err != nil || mime != "image/jpeg" || string(data) != latestCover {
		t.Fatalf("unexpected cover %q, mime=%s, err=%v", data, mime, err)
	}
	if _, err := service.ScrapeDailyStatSongs("unknown", "cover"); err == nil {
		t.Fatal("invalid provider unexpectedly succeeded")
	}
}
