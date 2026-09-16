package station

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateServerFileMetadataNeverChangesAudio(t *testing.T) {
	service := newTransferTestService(t)
	originalPath := filepath.Join(service.storageRoot, "library", "device-a", "song.wav")
	if err := os.MkdirAll(filepath.Dir(originalPath), 0755); err != nil {
		t.Fatal(err)
	}
	original := deterministicFixture(2 * 1024 * 1024)
	if err := os.WriteFile(originalPath, original, 0644); err != nil {
		t.Fatal(err)
	}
	serverFileID, err := serverFileIDForPath(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	serverFile := ServerFile{
		ServerFileID: serverFileID, StoragePath: originalPath, RelativePath: "song.wav",
		SourceDeviceID: "device-a", FileName: "song.wav", Size: int64(len(original)),
		Hash: "unchanged-content-hash", Version: 1, Format: "WAV",
	}
	if err := service.db.Create(&serverFile).Error; err != nil {
		t.Fatal(err)
	}
	cover, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2nQAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.UpdateServerFileMetadata(serverFileID, MetadataUpdate{
		Title: "New title", Artist: "Artist", Album: "Album", AlbumArtist: "Album artist",
		Lyrics: "[00:01.00]Hello", TrackNumber: 3, DiscNumber: 2,
		Cover: cover, CoverMIME: "image/png",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasCover || view.Title != "New title" || view.MetadataCount != 1 || view.LyricsCount != 1 || view.CoverCount != 1 {
		t.Fatalf("unexpected server file view: %+v", view)
	}
	if view.Hash != "unchanged-content-hash" || view.StoragePath != originalPath || view.Version != 1 {
		t.Fatalf("audio identity changed: %+v", view.ServerFile)
	}
	after, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatal("metadata update modified immutable audio bytes")
	}
}

func TestMetadataResourcesDeduplicateByHash(t *testing.T) {
	service := newTransferTestService(t)
	file := ServerFile{ServerFileID: "sf1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FileName: "song.flac"}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	update := MetadataUpdate{Title: "Title", Lyrics: "Hello"}
	if _, err := service.UpdateServerFileMetadata(file.ServerFileID, update); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateServerFileMetadata(file.ServerFileID, update); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := service.db.Model(&MetadataResource{}).Where("server_file_id = ?", file.ServerFileID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("got %d resources, want metadata + lyrics", count)
	}
}
