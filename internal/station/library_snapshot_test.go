package station

import "testing"

func TestUpsertTracksPreservesExistingServerAssociation(t *testing.T) {
	service := newTransferTestService(t)
	existing := Track{
		DeviceID:       "device-a",
		FileID:         "old-file-id",
		RelativePath:   "Album/song.flac",
		FileName:       "song.flac",
		ServerFileID:   "sf-song",
		AppliedVersion: 3,
		Hash:           "existing-hash",
		SyncState:      "synced",
	}
	if err := service.db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}

	incoming := Track{
		FileID:       "rebuilt-file-id",
		RelativePath: "Album/song.flac",
		FileName:     "song.flac",
		Size:         1234,
		SyncState:    "local_only",
	}
	if err := service.upsertTracks("device-a", "snapshot-new", []Track{incoming}); err != nil {
		t.Fatal(err)
	}

	var updated Track
	if err := service.db.Where("device_id = ? AND relative_path = ?", "device-a", incoming.RelativePath).First(&updated).Error; err != nil {
		t.Fatal(err)
	}
	if updated.FileID != incoming.FileID || updated.Size != incoming.Size {
		t.Fatalf("local fields were not refreshed: %+v", updated)
	}
	if updated.ServerFileID != existing.ServerFileID || updated.AppliedVersion != existing.AppliedVersion || updated.Hash != existing.Hash || updated.SyncState != existing.SyncState {
		t.Fatalf("server association was erased: %+v", updated)
	}
}

func TestUpsertTracksUsesServerFileIDForRemoteTracks(t *testing.T) {
	service := newTransferTestService(t)
	tracks := []Track{
		{FileID: "remote-a", FileName: "A.mp3", ServerFileID: "sf-a", SyncState: "remote"},
		{FileID: "remote-b", FileName: "B.mp3", ServerFileID: "sf-b", SyncState: "remote"},
	}
	if err := service.upsertTracks("device-a", "snapshot-one", tracks); err != nil {
		t.Fatal(err)
	}

	var stored []Track
	if err := service.db.Where("device_id = ?", "device-a").Order("server_file_id").Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 {
		t.Fatalf("got %d tracks, want distinct rows for both remote files: %+v", len(stored), stored)
	}
	if stored[0].ServerFileID != "sf-a" || stored[0].RelativePath != ".polemo-station/sf-a" ||
		stored[1].ServerFileID != "sf-b" || stored[1].RelativePath != ".polemo-station/sf-b" {
		t.Fatalf("remote tracks were not keyed by server file ID: %+v", stored)
	}

	// A path change must update the same device-to-file association, not add
	// another row for the same globally identified song.
	if err := service.upsertTracks("device-a", "snapshot-two", []Track{{
		FileID: "remote-a-reindexed", RelativePath: "Moved/A.mp3", FileName: "A.mp3", ServerFileID: "sf-a", SyncState: "remote",
	}}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := service.db.Model(&Track{}).Where("device_id = ? AND server_file_id = ?", "device-a", "sf-a").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("got %d rows for sf-a, want one", count)
	}
}

func TestUpsertTracksCreatesServerFilePlaceholdersAndListsThem(t *testing.T) {
	service := newTransferTestService(t)
	tracks := []Track{
		{
			FileID:       "file-1",
			RelativePath: "Rock/Song1.flac",
			FileName:     "Song1.flac",
			Title:        "Track One",
			Artist:       "Artist A",
			Album:        "Album A",
			ServerFileID: "sf-song-1",
			Size:         2048,
			Hash:         "hash-1",
			Format:       "FLAC",
		},
	}
	if err := service.upsertTracks("device-x", "snap-1", tracks); err != nil {
		t.Fatal(err)
	}

	files, err := service.ListServerFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file in ListServerFiles, got %d", len(files))
	}
	if files[0].ServerFileID != "sf-song-1" || files[0].Title != "Track One" || files[0].Artist != "Artist A" {
		t.Fatalf("unexpected file content: %+v", files[0])
	}
	if files[0].ExistsOnServer || files[0].HasAudio {
		t.Fatalf("placeholder should have ExistsOnServer=false and HasAudio=false, got %v / %v", files[0].ExistsOnServer, files[0].HasAudio)
	}
}
