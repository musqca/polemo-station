package station

import (
	"bytes"
	"os"
	"testing"
)

func TestImportWebUploadStoresAndDeduplicatesAudio(t *testing.T) {
	service := newTransferTestService(t)
	content := deterministicFixture(256 * 1024)
	first, err := service.ImportWebUpload("Browser Song.mp3", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceDeviceID != "web" || first.Format != "MP3" || first.Size != int64(len(content)) {
		t.Fatalf("unexpected imported file: %+v", first.ServerFile)
	}
	if _, err := os.Stat(first.StoragePath); err != nil {
		t.Fatalf("managed audio missing: %v", err)
	}
	second, err := service.ImportWebUpload("Another name.mp3", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if second.ServerFileID != first.ServerFileID {
		t.Fatalf("same bytes created two files: %s != %s", second.ServerFileID, first.ServerFileID)
	}
	var count int64
	if err := service.db.Model(&ServerFile{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("got %d server files, want 1", count)
	}
}

func TestImportWebUploadRejectsNonAudioExtension(t *testing.T) {
	service := newTransferTestService(t)
	if _, err := service.ImportWebUpload("not-a-song.txt", bytes.NewReader([]byte("no"))); err == nil {
		t.Fatal("expected unsupported extension to be rejected")
	}
}

func TestImportWebUploadAcceptsWave64(t *testing.T) {
	service := newTransferTestService(t)
	file, err := service.ImportWebUpload("Song.W64", bytes.NewReader(deterministicFixture(1024)))
	if err != nil {
		t.Fatalf("Wave64 upload rejected: %v", err)
	}
	if file.Format != "W64" {
		t.Fatalf("got format %q, want W64", file.Format)
	}
}

func TestImportWebUploadAcceptsVob(t *testing.T) {
	service := newTransferTestService(t)
	file, err := service.ImportWebUpload("Concert.VOB", bytes.NewReader(deterministicFixture(1024)))
	if err != nil {
		t.Fatalf("VOB upload rejected: %v", err)
	}
	if file.Format != "VOB" {
		t.Fatalf("got format %q, want VOB", file.Format)
	}
}

func TestImportWebUploadAcceptsMkaAndM4b(t *testing.T) {
	service := newTransferTestService(t)
	f1, err := service.ImportWebUpload("Album.MKA", bytes.NewReader(deterministicFixture(1024)))
	if err != nil {
		t.Fatalf("MKA upload rejected: %v", err)
	}
	if f1.Format != "MKA" {
		t.Fatalf("got format %q, want MKA", f1.Format)
	}

	f2, err := service.ImportWebUpload("Book.m4b", bytes.NewReader(deterministicFixture(2048)))
	if err != nil {
		t.Fatalf("M4B upload rejected: %v", err)
	}
	if f2.Format != "M4B" {
		t.Fatalf("got format %q, want M4B", f2.Format)
	}
}
