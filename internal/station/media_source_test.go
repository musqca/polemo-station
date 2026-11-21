package station

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalMediaSourceIndexesInPlace(t *testing.T) {
	service := newTransferTestService(t)
	mediaRoot := filepath.Join(t.TempDir(), "Music")
	album := filepath.Join(mediaRoot, "Artist", "Album")
	if err := os.MkdirAll(album, 0755); err != nil {
		t.Fatal(err)
	}
	audioPath := filepath.Join(album, "Song.flac")
	if err := os.WriteFile(audioPath, deterministicFixture(128*1024), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(album, "notes.txt"), []byte("ignore"), 0644); err != nil {
		t.Fatal(err)
	}
	service.SetMediaRoots([]string{mediaRoot})
	source := MediaSource{SourceID: "ms_test", Type: "local", Name: "Music", RootPath: mediaRoot, State: "idle"}
	if err := service.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.ScanMediaSource(source.SourceID); err != nil {
		t.Fatal(err)
	}

	var file ServerFile
	if err := service.db.First(&file).Error; err != nil {
		t.Fatal(err)
	}
	canonicalAudioPath, err := filepath.EvalSymlinks(audioPath)
	if err != nil {
		t.Fatal(err)
	}
	if file.StoragePath != canonicalAudioPath || file.RelativePath != "Artist/Album/Song.flac" || file.Hash != "" {
		t.Fatalf("unexpected indexed file: %+v", file)
	}
	var location MediaLocation
	if err := service.db.Where("server_file_id = ?", file.ServerFileID).First(&location).Error; err != nil {
		t.Fatal(err)
	}
	if !location.Available || location.LocalPath != canonicalAudioPath {
		t.Fatalf("unexpected media location: %+v", location)
	}
	if err := service.db.First(&source, "source_id = ?", source.SourceID).Error; err != nil {
		t.Fatal(err)
	}
	if source.State != "ready" || source.FileCount != 1 {
		t.Fatalf("unexpected source result: %+v", source)
	}
	audio, err := service.OpenAudio(file.ServerFileID)
	if err != nil {
		t.Fatal(err)
	}
	_ = audio.Close()
	if err := os.Remove(audioPath); err != nil {
		t.Fatal(err)
	}
	if audio, err = service.OpenAudio(file.ServerFileID); err == nil {
		_ = audio.Close()
		t.Fatal("missing source file was served through the stale projection path")
	}
	if err := service.ScanMediaSource(source.SourceID); err != nil {
		t.Fatal(err)
	}
	if err := service.db.Where("server_file_id = ?", file.ServerFileID).First(&file).Error; err != nil {
		t.Fatal(err)
	}
	if file.DeletedAt == nil {
		t.Fatal("removed source file remained visible in the library")
	}
}

func TestMediaBrowserRequiresExistingDirectory(t *testing.T) {
	service := newTransferTestService(t)
	root := t.TempDir()
	if _, err := service.BrowseMediaDirectory(filepath.Join(root, "missing")); err == nil {
		t.Fatal("expected missing directory to be rejected")
	}
}

func TestLocalMediaSourceIndexesWave64(t *testing.T) {
	service := newTransferTestService(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Song.W64"), deterministicFixture(1024), 0644); err != nil {
		t.Fatal(err)
	}
	service.SetMediaRoots([]string{root})
	source := MediaSource{SourceID: "ms_wave64", Type: "local", Name: "Music", RootPath: root, State: "idle"}
	if err := service.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.ScanMediaSource(source.SourceID); err != nil {
		t.Fatal(err)
	}
	var file ServerFile
	if err := service.db.First(&file).Error; err != nil {
		t.Fatalf("Wave64 file was not indexed: %v", err)
	}
	if file.FileName != "Song.W64" {
		t.Fatalf("unexpected file: %+v", file)
	}
}

func TestLocalMediaSourceIndexesVob(t *testing.T) {
	service := newTransferTestService(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Concert.VOB"), deterministicFixture(1024), 0644); err != nil {
		t.Fatal(err)
	}
	service.SetMediaRoots([]string{root})
	source := MediaSource{SourceID: "ms_vob", Type: "local", Name: "Music", RootPath: root, State: "idle"}
	if err := service.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.ScanMediaSource(source.SourceID); err != nil {
		t.Fatal(err)
	}
	var file ServerFile
	if err := service.db.First(&file).Error; err != nil {
		t.Fatalf("VOB file was not indexed: %v", err)
	}
	if file.FileName != "Concert.VOB" {
		t.Fatalf("unexpected file: %+v", file)
	}
}

func TestLocalMediaSourceIndexesMkaAndM4b(t *testing.T) {
	service := newTransferTestService(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Album.MKA"), deterministicFixture(1024), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Audiobook.M4B"), deterministicFixture(2048), 0644); err != nil {
		t.Fatal(err)
	}
	service.SetMediaRoots([]string{root})
	source := MediaSource{SourceID: "ms_mka_m4b", Type: "local", Name: "Music", RootPath: root, State: "idle"}
	if err := service.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.ScanMediaSource(source.SourceID); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := service.db.Model(&ServerFile{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 files indexed, got %d", count)
	}
}

func TestMediaBrowserRejectsFilesystemRoot(t *testing.T) {
	service := newTransferTestService(t)
	root := string(filepath.Separator)
	if volume := filepath.VolumeName(t.TempDir()); volume != "" {
		root = volume + string(filepath.Separator)
	}
	if _, err := service.BrowseMediaDirectory(root); err == nil {
		t.Fatal("expected filesystem root to be rejected")
	}
}
