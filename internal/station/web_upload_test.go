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
