package station

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/musqca/polemo-station/internal/thirdparty/lrclib"
)

func TestScrapeQueueRecoversExpiredRunningTask(t *testing.T) {
	service := newTransferTestService(t)
	past := time.Now().Add(-time.Minute)
	item := ScrapeItem{ServerFileID: "recover", Provider: "lrclib", State: ScrapeRunning, LeaseUntil: &past}
	if err := service.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	claimed, err := service.claimScrapeItem()
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != item.ID {
		t.Fatalf("recovered task was not claimed: %+v", claimed)
	}
}

func TestScrapeDisabledProviderStaysPending(t *testing.T) {
	service := newTransferTestService(t)
	if err := service.SaveScrapeProvider("musicbrainz", false, nil); err != nil {
		t.Fatal(err)
	}
	item := ScrapeItem{ServerFileID: "disabled", Provider: "musicbrainz", State: ScrapeRunning}
	if err := service.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.processScrapeItem(item); err != nil {
		t.Fatal(err)
	}
	var got ScrapeItem
	if err := service.db.First(&got, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.State != ScrapePending || got.FailureCount != 0 || got.FinishedAt != nil {
		t.Fatalf("disabled task was completed or failed: %+v", got)
	}
}

func TestScrapeQueueWaitsForTheSongsOwnMusicBrainzID(t *testing.T) {
	service := newTransferTestService(t)
	key := "fixture-key"
	if err := service.SaveScrapeProvider("fanarttv", true, &key); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	items := []ScrapeItem{
		{ServerFileID: "waiting", Provider: "musicbrainz", State: ScrapePending},
		{ServerFileID: "waiting", Provider: "coverartarchive", State: ScrapePending, CreatedAt: old},
		{ServerFileID: "waiting", Provider: "fanarttv", State: ScrapePending, CreatedAt: old},
		{ServerFileID: "ready", Provider: "musicbrainz", State: ScrapeSuccess, ExternalID: "release", RelatedExternalID: "group"},
		{ServerFileID: "ready", Provider: "coverartarchive", State: ScrapePending},
		{ServerFileID: "ready", Provider: "fanarttv", State: ScrapePending},
	}
	if err := service.db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	service.SetScrapeHTTPClient(&http.Client{Transport: scrapeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/release/release":
			return scrapeResponse(http.StatusOK, `{"images":[{"image":"https://fixture.example/archive.jpg","front":true,"approved":true}]}`), nil
		case "/v3.2/music/albums/group":
			return scrapeResponse(http.StatusOK, `{"albums":[{"albumcover":[{"url":"https://fixture.example/fanart.jpg"}]}]}`), nil
		case "/archive.jpg", "/fanart.jpg":
			return scrapeResponse(http.StatusOK, "\xff\xd8\xff\xe0"+r.URL.Path), nil
		default:
			return nil, fmt.Errorf("unexpected request to %s", r.URL)
		}
	})})
	service.coolScrapeProvider("musicbrainz", time.Minute)
	for _, provider := range []string{"coverartarchive", "fanarttv"} {
		claimed, err := service.claimScrapeItem()
		if err != nil || claimed == nil || claimed.ServerFileID != "ready" || claimed.Provider != provider {
			t.Fatalf("wanted ready %s cover while MusicBrainz was cooling: item=%+v, err=%v", provider, claimed, err)
		}
		if err := service.processScrapeItem(*claimed); err != nil {
			t.Fatal(err)
		}
		var saved MetadataResource
		if err := service.db.Where("server_file_id = ? AND source = ? AND type = ?", "ready", provider, "cover").First(&saved).Error; err != nil {
			t.Fatalf("%s cover was not saved: %v", provider, err)
		}
	}
	claimed, err := service.claimScrapeItem()
	if err != nil || claimed != nil {
		t.Fatalf("a cover with a pending MusicBrainz ID was claimed: item=%+v, err=%v", claimed, err)
	}
	if err := service.db.Model(&items[0]).Updates(map[string]any{
		"state": ScrapeSuccess, "external_id": "second-release", "related_external_id": "second-group",
	}).Error; err != nil {
		t.Fatal(err)
	}
	service.scrapeRateMu.Lock()
	delete(service.scrapeProviderNext, "coverartarchive")
	delete(service.scrapeProviderNext, "fanarttv")
	service.scrapeRateMu.Unlock()
	for _, provider := range []string{"coverartarchive", "fanarttv"} {
		claimed, err := service.claimScrapeItem()
		if err != nil || claimed == nil || claimed.ServerFileID != "waiting" || claimed.Provider != provider {
			t.Fatalf("wanted %s cover after its ID became available: item=%+v, err=%v", provider, claimed, err)
		}
	}
}

func TestTitleScrapersRunWhileMusicBrainzStillHasWork(t *testing.T) {
	for _, cooling := range []bool{false, true} {
		t.Run(fmt.Sprintf("musicbrainz cooling=%t", cooling), func(t *testing.T) {
			service := newTransferTestService(t)
			files := []ScrapeItem{
				{ServerFileID: "first-mb", Provider: "musicbrainz", State: ScrapePending},
				{ServerFileID: "second-mb", Provider: "musicbrainz", State: ScrapePending},
				{ServerFileID: "second-mb", Provider: "coverartarchive", State: ScrapePending},
				{ServerFileID: "title-lyrics", Provider: "lrclib", State: ScrapePending},
				{ServerFileID: "title-cover", Provider: "theaudiodb", State: ScrapePending},
			}
			if err := service.db.Create(&files).Error; err != nil {
				t.Fatal(err)
			}
			order := []string{"musicbrainz", "lrclib", "theaudiodb"}
			if cooling {
				service.coolScrapeProvider("musicbrainz", time.Minute)
				order = order[1:]
			}
			for _, provider := range order {
				item, err := service.claimScrapeItem()
				if err != nil || item == nil || item.Provider != provider {
					t.Fatalf("wanted %s while MusicBrainz still had tasks: item=%+v, err=%v", provider, item, err)
				}
				if err := service.finishScrapeSuccess(item); err != nil {
					t.Fatal(err)
				}
				if provider == "musicbrainz" {
					service.scrapeRateMu.Lock()
					service.scrapeProviderNext[provider] = time.Now()
					service.scrapeRateMu.Unlock()
				}
			}
		})
	}
}

func TestScrapeDependencyWaitIsDelayedAndDoesNotFail(t *testing.T) {
	service := newTransferTestService(t)
	item := ScrapeItem{ServerFileID: "dependency", Provider: "coverartarchive", State: ScrapeRunning}
	if err := service.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	if err := service.finishScrapeWaiting(&item); err != nil {
		t.Fatal(err)
	}
	var got ScrapeItem
	if err := service.db.First(&got, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.State != ScrapePending || got.FailureCount != 0 || got.NextRetryAt == nil || !got.NextRetryAt.After(before) {
		t.Fatalf("dependency wait was not delayed cleanly: %+v", got)
	}
	claimed, err := service.claimScrapeItem()
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("dependency task was reclaimed before delay: %+v", claimed)
	}
}

func TestScrapeDependencyWithoutReleaseEndsNoMatch(t *testing.T) {
	service := newTransferTestService(t)
	mb := ScrapeItem{ServerFileID: "no-release", Provider: "musicbrainz", State: ScrapeSuccess}
	cover := ScrapeItem{ServerFileID: mb.ServerFileID, Provider: "coverartarchive", State: ScrapeRunning}
	if err := service.db.Create(&mb).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&cover).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.processScrapeItem(cover); err != nil {
		t.Fatal(err)
	}
	if err := service.db.First(&cover, cover.ID).Error; err != nil {
		t.Fatal(err)
	}
	if cover.State != ScrapeNoMatch || cover.FailureCount != 0 {
		t.Fatalf("missing release was treated as a request failure: %+v", cover)
	}
}

func TestScrapeExternalIDPersistsAndIsReused(t *testing.T) {
	service := newTransferTestService(t)
	mb := ScrapeItem{ServerFileID: "reuse-release", Provider: "musicbrainz", State: ScrapeSuccess, ExternalID: "release-1"}
	if err := service.db.Create(&mb).Error; err != nil {
		t.Fatal(err)
	}
	cover := ScrapeItem{ServerFileID: mb.ServerFileID, Provider: "coverartarchive", State: ScrapePending}
	if err := service.db.Create(&cover).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = service.scrapeCoverArt(ctx, &cover, defaultScrapeSettings())
	if cover.ExternalID != mb.ExternalID {
		t.Fatalf("saved release ID was not reused: %q", cover.ExternalID)
	}
}

func TestMusicBrainzDoesNotScheduleDownstreamProviders(t *testing.T) {
	service := newTransferTestService(t)
	file := ServerFile{ServerFileID: "musicbrainz-only", Title: "Song", Artist: "Artist"}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	cover := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "coverartarchive", State: ScrapeNoMatch}
	fanart := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "fanarttv", State: ScrapeFailed}
	if err := service.db.Create(&cover).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&fanart).Error; err != nil {
		t.Fatal(err)
	}
	service.SetScrapeHTTPClient(&http.Client{Transport: scrapeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return scrapeResponse(http.StatusOK, `{"recordings":[{"id":"recording-id","title":"Song","artist-credit":[{"name":"Artist"}],"releases":[{"id":"release-id","title":"Album","release-group":{"id":"release-group-id"}}]}]}`), nil
	})})
	item := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "musicbrainz", State: ScrapeRunning}
	if err := service.scrapeMusicBrainz(context.Background(), &item, defaultScrapeSettings()); err != nil {
		t.Fatal(err)
	}
	if err := service.db.First(&cover, cover.ID).Error; err != nil {
		t.Fatal(err)
	}
	if cover.State != ScrapeNoMatch || cover.ExternalID != "release-id" {
		t.Fatalf("cover task was scheduled instead of only hydrated: %+v", cover)
	}
	if err := service.db.First(&fanart, fanart.ID).Error; err != nil {
		t.Fatal(err)
	}
	if fanart.State != ScrapeFailed || fanart.ExternalID != "release-group-id" {
		t.Fatalf("fanart task was scheduled instead of only hydrated: %+v", fanart)
	}
	var lyricsCount int64
	if err := service.db.Model(&ScrapeItem{}).Where("server_file_id = ? AND provider = ?", file.ServerFileID, "lrclib").Count(&lyricsCount).Error; err != nil {
		t.Fatal(err)
	}
	if lyricsCount != 0 {
		t.Fatalf("MusicBrainz-only run created %d lyrics tasks", lyricsCount)
	}
}

func TestScrapeResourcesDoNotChangeSelectionOrDuplicateUse(t *testing.T) {
	service := newTransferTestService(t)
	file := ServerFile{ServerFileID: "resource-selection"}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	oldCover := []byte{0xff, 0xd8, 0xff, 0xd9}
	scrapedCover := []byte{0xff, 0xd8, 0xff, 0xe0, 0x01, 0x02, 0xff, 0xd9}
	if _, err := service.UpdateServerFileMetadata(file.ServerFileID, MetadataUpdate{Cover: oldCover, CoverMIME: "image/jpeg"}); err != nil {
		t.Fatal(err)
	}
	var originalCover MetadataResource
	if err := service.db.Where("server_file_id = ? AND type = ? AND source = ? AND deleted = ?", file.ServerFileID, "cover", "station", false).First(&originalCover).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.appendScrapeCover(file.ServerFileID, scrapedCover, "image/jpeg", "coverartarchive"); err != nil {
		t.Fatal(err)
	}
	view, err := service.GetServerFile(file.ServerFileID)
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasCover || view.CoverURL != "/api/station/resources/"+originalCover.ResourceID {
		t.Fatalf("scraped resource changed the active selection: %+v", view)
	}
	if _, err := service.UpdateServerFileMetadata(file.ServerFileID, MetadataUpdate{RemoveCover: true}); err != nil {
		t.Fatal(err)
	}
	view, err = service.GetServerFile(file.ServerFileID)
	if err != nil {
		t.Fatal(err)
	}
	if view.HasCover {
		t.Fatal("deleted cover restored an older resource")
	}

	first, err := service.appendScrapeResource(file.ServerFileID, "lyrics", "plain", "same lyrics", "lrclib", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.appendScrapeResource(file.ServerFileID, "lyrics", "plain", "same lyrics", "lrclib", "")
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("duplicate resource counts were wrong: %d, %d", first, second)
	}
	var resources []MetadataResource
	if err := service.db.Where("server_file_id = ? AND type = ? AND source = ?", file.ServerFileID, "lyrics", "lrclib").Find(&resources).Error; err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 || resources[0].LastUseAt != nil {
		t.Fatalf("duplicate resource changed use metadata: %+v", resources)
	}
}

func TestScrapeRequestTimeoutAndFailureIsolation(t *testing.T) {
	service := newTransferTestService(t)
	settings := defaultScrapeSettings()
	settings.RequestTimeoutSeconds = 1
	settings.ItemTimeoutSeconds = 1
	settings.MaxFailures = 3
	if err := service.SaveScrapeSettings(settings); err != nil {
		t.Fatal(err)
	}
	file := ServerFile{ServerFileID: "timeout-file", Title: "Song", Artist: "Artist"}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	mode := "timeout"
	service.SetScrapeHTTPClient(&http.Client{Transport: scrapeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch mode {
		case "timeout":
			<-r.Context().Done()
			return nil, r.Context().Err()
		case "429":
			return scrapeResponse(http.StatusTooManyRequests, ""), nil
		case "503":
			return scrapeResponse(http.StatusServiceUnavailable, ""), nil
		case "404":
			return scrapeResponse(http.StatusNotFound, ""), nil
		default:
			return scrapeResponse(http.StatusOK, `{"plainLyrics":"lyrics"}`), nil
		}
	})})
	item := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "lrclib", State: ScrapeRunning}
	if err := service.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := service.processScrapeItem(item); err == nil {
		t.Fatal("timeout request unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout exceeded item deadline: %s", elapsed)
	}
	if err := service.db.First(&item, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.FailureCount != 1 || item.State != ScrapeRetry {
		t.Fatalf("timeout failure was recorded incorrectly: %+v", item)
	}
	healthy := ScrapeItem{ServerFileID: "healthy-after-timeout", Provider: "musicbrainz", State: ScrapePending}
	if err := service.db.Create(&healthy).Error; err != nil {
		t.Fatal(err)
	}
	claimed, err := service.claimScrapeItem()
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != healthy.ID {
		t.Fatalf("healthy task was blocked by timed-out provider: %+v", claimed)
	}
	mode = "429"
	if err := service.processScrapeItem(item); err == nil {
		t.Fatal("429 unexpectedly succeeded")
	}
	if err := service.db.First(&item, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.FailureCount != 2 || item.State != ScrapeRetry {
		t.Fatalf("429 failure was recorded incorrectly: %+v", item)
	}
	mode = "503"
	if err := service.processScrapeItem(item); err == nil {
		t.Fatal("503 unexpectedly succeeded")
	}
	if err := service.db.First(&item, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.FailureCount != 3 || item.State != ScrapeFailed {
		t.Fatalf("503 did not reach failure limit: %+v", item)
	}
	if claimed, err := service.claimScrapeItem(); err != nil || claimed != nil {
		t.Fatalf("failed task was still claimable: item=%+v err=%v", claimed, err)
	}

	noMatch := ScrapeItem{ServerFileID: "no-match-file", Provider: "lrclib", State: ScrapeRunning}
	file2 := ServerFile{ServerFileID: noMatch.ServerFileID, Title: "No match", Artist: "Artist"}
	if err := service.db.Create(&file2).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&noMatch).Error; err != nil {
		t.Fatal(err)
	}
	mode = "404"
	if err := service.processScrapeItem(noMatch); err != nil {
		t.Fatal(err)
	}
	if err := service.db.First(&noMatch, noMatch.ID).Error; err != nil {
		t.Fatal(err)
	}
	if noMatch.State != ScrapeNoMatch || noMatch.FailureCount != 0 {
		t.Fatalf("no-match response consumed a failure: %+v", noMatch)
	}
}

func TestScrapeMissingTitleEndsNoMatchWithoutRequestFailure(t *testing.T) {
	service := newTransferTestService(t)
	file := ServerFile{ServerFileID: "missing-title", Artist: "Artist"}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	item := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "lrclib", State: ScrapeRunning}
	if err := service.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.processScrapeItem(item); err != nil {
		t.Fatal(err)
	}
	if err := service.db.First(&item, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.State != ScrapeNoMatch || item.FailureCount != 0 || !strings.Contains(item.LastError, "requires track title and artist") {
		t.Fatalf("missing title was retried as request failure: %+v", item)
	}
}

func TestScrapeLyricsUsesMusicBrainzCandidateWithoutChangingFileMetadata(t *testing.T) {
	service := newTransferTestService(t)
	file := ServerFile{ServerFileID: "lyrics-musicbrainz-candidate"}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.appendScrapeResource(file.ServerFileID, "metadata", "json", `{"title":"Song","artist":"Artist","album":"Album"}`, "musicbrainz", ""); err != nil {
		t.Fatal(err)
	}
	service.SetScrapeHTTPClient(&http.Client{Transport: scrapeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.URL.Query().Get("artist_name"); got != "Artist" {
			t.Fatalf("LRCLIB queried with artist %q, want MusicBrainz candidate", got)
		}
		return scrapeResponse(http.StatusOK, `{"plainLyrics":"lyrics"}`), nil
	})})
	item := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "lrclib", State: ScrapeRunning}
	if err := service.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.processScrapeItem(item); err != nil {
		t.Fatal(err)
	}
	if err := service.db.First(&file, "server_file_id = ?", file.ServerFileID).Error; err != nil {
		t.Fatal(err)
	}
	if file.Title != "" || file.Artist != "" {
		t.Fatalf("scrape changed selected file metadata: %+v", file)
	}
}

func TestScrapeNoMatchClearsPreviousFailureCount(t *testing.T) {
	service := newTransferTestService(t)
	item := ScrapeItem{ServerFileID: "no-match-count", Provider: "lrclib", State: ScrapeRunning, FailureCount: 2}
	if err := service.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.finishScrapeNoMatch(&item, lrclib.ErrNoMatch); err != nil {
		t.Fatal(err)
	}
	if err := service.db.First(&item, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.State != ScrapeNoMatch || item.FailureCount != 0 {
		t.Fatalf("no-match retained request failure count: %+v", item)
	}
}

func TestMigrateLegacyCoverArt404FailureToNoMatch(t *testing.T) {
	service := newTransferTestService(t)
	legacy := ScrapeItem{ServerFileID: "legacy-cover-404", Provider: "coverartarchive", State: ScrapeFailed, FailureCount: 3, LastError: "cover art archive returned HTTP 404: no cover"}
	other := ScrapeItem{ServerFileID: "cover-500", Provider: "coverartarchive", State: ScrapeFailed, FailureCount: 3, LastError: "cover art archive returned HTTP 500: server error"}
	if err := service.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.migrateLegacyCoverArtNoMatches(); err != nil {
		t.Fatal(err)
	}
	if err := service.db.First(&legacy, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if legacy.State != ScrapeNoMatch || legacy.FailureCount != 0 || legacy.LastErrorCode != "no_match" {
		t.Fatalf("legacy 404 was not normalized: %+v", legacy)
	}
	if err := service.db.First(&other, other.ID).Error; err != nil {
		t.Fatal(err)
	}
	if other.State != ScrapeFailed {
		t.Fatalf("non-404 failure was changed: %+v", other)
	}
}

func TestScrapeCoolingOnlyAppliesToTransientFailures(t *testing.T) {
	if shouldCoolScrapeProvider(errors.New(`lrclib returned HTTP 400: {"message":"artist_name: cannot be empty"}`)) {
		t.Fatal("validation failure incorrectly cooled the provider")
	}
	if !shouldCoolScrapeProvider(errors.New("lrclib returned HTTP 429: rate limited")) {
		t.Fatal("rate limiting did not cool the provider")
	}
	if !shouldCoolScrapeProvider(errors.New("musicbrainz returned HTTP 503: service unavailable")) {
		t.Fatal("service outage did not cool the provider")
	}
}

func TestScrapeOversizedCoverIsRejected(t *testing.T) {
	service := newTransferTestService(t)
	service.SetScrapeHTTPClient(&http.Client{Transport: scrapeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/release/release-1" {
			return scrapeResponse(http.StatusOK, fmt.Sprintf(`{"images":[{"image":"https://mock.local/cover.jpg","front":true,"approved":true}]}`)), nil
		}
		if r.URL.Path == "/cover.jpg" {
			data := append([]byte{0xff, 0xd8, 0xff, 0xd9}, bytes.Repeat([]byte{0}, int(maxCoverSize))...)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), Header: http.Header{"Content-Type": []string{"image/jpeg"}}}, nil
		}
		return scrapeResponse(http.StatusNotFound, ""), nil
	})})
	item := ScrapeItem{ServerFileID: "oversized", Provider: "coverartarchive", State: ScrapeRunning, ExternalID: "release-1"}
	if err := service.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.processScrapeItem(item); err == nil {
		t.Fatal("oversized cover unexpectedly succeeded")
	}
	if err := service.db.First(&item, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.FailureCount != 1 || item.State != ScrapeRetry {
		t.Fatalf("oversized cover failure was not isolated: %+v", item)
	}
}

func TestScrapeTimeoutTransportHonorsContext(t *testing.T) {
	client := lrclib.NewClient(lrclib.WithHTTPClient(&http.Client{Transport: scrapeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}), lrclib.WithTimeout(10*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.Get(ctx, "Song", "Artist", "Album")
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("client ignored context deadline: err=%v elapsed=%s", err, time.Since(started))
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context") {
		return
	}
	t.Fatalf("unexpected timeout error: %v", err)
}

func TestScrapeRetryDelayDistinguishesTransientConnectionFailure(t *testing.T) {
	settings := ScrapeSettings{RetryDelaySeconds: 60}
	if got := scrapeRetryDelay(errors.New(`HTTP request failed: Get "https://archive.example/index.json": EOF`), settings); got != 5*time.Second {
		t.Fatalf("EOF retry delay = %s, want 5s", got)
	}
	if got := scrapeRetryDelay(errors.New("coverartarchive returned HTTP 429"), settings); got != time.Minute {
		t.Fatalf("429 retry delay = %s, want 1m", got)
	}
	if got := scrapeRetryDelay(errors.New("context deadline exceeded while awaiting headers"), settings); got != 30*time.Second {
		t.Fatalf("timeout retry delay = %s, want 30s", got)
	}
	if got := scrapeRetryDelay(errors.New("ordinary request error"), settings); got != time.Minute {
		t.Fatalf("ordinary retry delay = %s, want configured 1m", got)
	}
}

type scrapeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f scrapeRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func scrapeResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
