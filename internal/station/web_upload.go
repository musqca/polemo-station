package station

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
)

// ImportWebUpload stores a browser-selected audio file in Station's managed
// library. The caller owns the reader; this method never uses client paths.
func (s *Service) ImportWebUpload(fileName string, source io.Reader) (ServerFileView, error) {
	name := filepath.Base(strings.TrimSpace(fileName))
	extension := strings.ToLower(filepath.Ext(name))
	if name == "." || name == "" || !supportedAudioExtensions[extension] {
		return ServerFileView{}, fmt.Errorf("unsupported audio format %q", extension)
	}

	temporary, err := os.CreateTemp(filepath.Join(s.storageRoot, "temp"), "web-upload-*")
	if err != nil {
		return ServerFileView{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hasher := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(temporary, hasher), source)
	closeErr := temporary.Close()
	if copyErr != nil {
		return ServerFileView{}, copyErr
	}
	if closeErr != nil {
		return ServerFileView{}, closeErr
	}
	if size <= 0 {
		return ServerFileView{}, errors.New("uploaded audio is empty")
	}
	if err := os.Chmod(temporaryPath, 0644); err != nil {
		return ServerFileView{}, err
	}
	serverFileID, err := serverFileIDForPath(temporaryPath)
	if err != nil {
		return ServerFileView{}, err
	}
	lock := s.transferLock("web-upload:" + serverFileID)
	lock.Lock()
	defer lock.Unlock()

	var existing ServerFile
	err = s.db.Where("server_file_id = ?", serverFileID).First(&existing).Error
	if err == nil && existing.StoragePath != "" {
		return s.serverFileView(existing)
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return ServerFileView{}, err
	}

	objectDir := filepath.Join(s.storageRoot, "library", "web")
	if err := os.MkdirAll(objectDir, 0755); err != nil {
		return ServerFileView{}, err
	}
	objectPath := filepath.Join(objectDir, serverFileID+extension)
	if err := os.Rename(temporaryPath, objectPath); err != nil {
		return ServerFileView{}, err
	}
	hash := hex.EncodeToString(hasher.Sum(nil))
	file := ServerFile{
		ServerFileID: serverFileID, StoragePath: objectPath, RelativePath: name,
		SourceDeviceID: "web", FileName: name, Size: size, Hash: hash, Version: 1,
		Title: strings.TrimSuffix(name, filepath.Ext(name)), Format: strings.TrimPrefix(strings.ToUpper(extension), "."),
	}
	if err == nil && existing.StoragePath == "" {
		file.ID = existing.ID
		if existing.Title != "" {
			file.Title = existing.Title
		}
		if existing.Artist != "" {
			file.Artist = existing.Artist
		}
		if existing.Album != "" {
			file.Album = existing.Album
		}
		if existing.AlbumArtist != "" {
			file.AlbumArtist = existing.AlbumArtist
		}
		if err := s.db.Save(&file).Error; err != nil {
			_ = os.Remove(objectPath)
			return ServerFileView{}, err
		}
	} else {
		if err := s.db.Create(&file).Error; err != nil {
			_ = os.Remove(objectPath)
			return ServerFileView{}, err
		}
	}
	s.importLocalEmbeddedMetadata(objectPath, serverFileID)
	s.emitLibraryChanged("web")
	return s.GetServerFile(serverFileID)
}
