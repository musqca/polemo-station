package station

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

var supportedAudioExtensions = map[string]bool{
	".mp3": true, ".flac": true, ".wav": true, ".m4a": true,
	".aac": true, ".ogg": true, ".opus": true, ".wma": true,
	".ape": true, ".aiff": true, ".aif": true, ".dsf": true,
}

const (
	maxMediaScanEntries = 100000
	maxMediaScanFiles   = 10000
)

type MediaBrowseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type MediaBrowseResult struct {
	Path        string             `json:"path"`
	Parent      string             `json:"parent,omitempty"`
	Directories []MediaBrowseEntry `json:"directories"`
}

type CreateMediaSourceRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s *Service) UpdateMediaSource(sourceID string, request CreateMediaSourceRequest) (MediaSource, error) {
	root, err := s.allowedMediaDirectory(request.Path)
	if err != nil {
		return MediaSource{}, err
	}
	var source MediaSource
	if err = s.db.Where("source_id = ?", sourceID).First(&source).Error; err != nil {
		return MediaSource{}, err
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = filepath.Base(root)
	}
	if err = s.db.Model(&source).Updates(map[string]any{"name": name, "root_path": root}).Error; err != nil {
		return MediaSource{}, err
	}
	if err = s.StartMediaSourceScan(sourceID); err != nil {
		return MediaSource{}, err
	}
	source.Name, source.RootPath, source.State, source.ScannedCount = name, root, "scanning", 0
	return source, nil
}

func (s *Service) SetMediaRoots(roots []string) {
	s.mediaRoots = s.mediaRoots[:0]
	for _, root := range roots {
		absolute, err := filepath.Abs(strings.TrimSpace(root))
		if err != nil || strings.TrimSpace(root) == "" {
			continue
		}
		resolved := filepath.Clean(absolute)
		if canonical, canonicalErr := canonicalDirectory(resolved); canonicalErr == nil {
			resolved = canonical
		}
		if !containsString(s.mediaRoots, resolved) {
			s.mediaRoots = append(s.mediaRoots, resolved)
		}
	}
	sort.Strings(s.mediaRoots)
}

func (s *Service) MediaRoots() []MediaBrowseEntry {
	result := make([]MediaBrowseEntry, 0, len(s.mediaRoots)+1)
	// The selector must be able to reach any readable directory, not only
	// configured shortcuts. Unix-like Station hosts expose the filesystem root;
	// configured roots remain convenient named shortcuts.
	if string(filepath.Separator) == "/" {
		result = append(result, MediaBrowseEntry{Name: "/", Path: "/"})
	}
	for _, root := range s.mediaRoots {
		name := filepath.Base(root)
		if name == "." || name == string(filepath.Separator) {
			name = root
		}
		result = append(result, MediaBrowseEntry{Name: name, Path: root})
	}
	return result
}

func (s *Service) BrowseMediaDirectory(path string) (MediaBrowseResult, error) {
	if strings.TrimSpace(path) == "" {
		return MediaBrowseResult{Directories: s.MediaRoots()}, nil
	}
	canonical, err := s.allowedMediaDirectory(path)
	if err != nil {
		return MediaBrowseResult{}, err
	}
	entries, err := os.ReadDir(canonical)
	if err != nil {
		return MediaBrowseResult{}, err
	}
	result := MediaBrowseResult{Path: canonical, Directories: make([]MediaBrowseEntry, 0)}
	parent := filepath.Dir(canonical)
	if parent != canonical && s.mediaPathAllowed(parent) {
		result.Parent = parent
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		child, childErr := canonicalDirectory(filepath.Join(canonical, entry.Name()))
		if childErr == nil && s.mediaPathAllowed(child) {
			result.Directories = append(result.Directories, MediaBrowseEntry{Name: entry.Name(), Path: child})
		}
	}
	sort.Slice(result.Directories, func(i, j int) bool {
		return strings.ToLower(result.Directories[i].Name) < strings.ToLower(result.Directories[j].Name)
	})
	return result, nil
}

func (s *Service) ListMediaSources() ([]MediaSource, error) {
	var sources []MediaSource
	err := s.db.Order("created_at asc").Find(&sources).Error
	return sources, err
}

func (s *Service) RemoveMediaSource(sourceID string) error {
	s.mediaScanMu.Lock()
	defer s.mediaScanMu.Unlock()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var source MediaSource
		if err := tx.Where("source_id = ?", sourceID).First(&source).Error; err != nil {
			return err
		}
		var locations []MediaLocation
		if err := tx.Where("source_id = ?", sourceID).Find(&locations).Error; err != nil {
			return err
		}
		if err := tx.Model(&MediaLocation{}).Where("source_id = ?", sourceID).Update("available", false).Error; err != nil {
			return err
		}
		for _, location := range locations {
			if err := retireUnavailableServerFile(tx, location.ServerFileID); err != nil {
				return err
			}
		}
		if err := tx.Delete(&MediaLocation{}, "source_id = ?", sourceID).Error; err != nil {
			return err
		}
		return tx.Delete(&source).Error
	})
	if err == nil {
		s.broadcastEvent(StationEvent{Kind: "server_file"})
	}
	return err
}

func (s *Service) CreateMediaSource(request CreateMediaSourceRequest) (MediaSource, error) {
	root, err := s.allowedMediaDirectory(request.Path)
	if err != nil {
		return MediaSource{}, err
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = filepath.Base(root)
	}
	var existing MediaSource
	if err = s.db.Where("type = ? AND root_path = ?", "local", root).First(&existing).Error; err == nil {
		_ = s.StartMediaSourceScan(existing.SourceID)
		existing.State = "scanning"
		return existing, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return MediaSource{}, err
	}
	source := MediaSource{
		SourceID: newID("ms"), Type: "local", Name: name,
		RootPath: root, State: "scanning",
	}
	if err = s.db.Create(&source).Error; err != nil {
		return MediaSource{}, err
	}
	_ = s.StartMediaSourceScan(source.SourceID)
	return source, nil
}

func (s *Service) StartMediaSourceScan(sourceID string) error {
	var count int64
	if err := s.db.Model(&MediaSource{}).Where("source_id = ?", sourceID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	if err := s.db.Model(&MediaSource{}).Where("source_id = ?", sourceID).
		Updates(map[string]any{"state": "scanning", "scanned_count": 0, "last_error": ""}).Error; err != nil {
		return err
	}
	go func() { _ = s.ScanMediaSource(sourceID) }()
	return nil
}

func (s *Service) ScanMediaSource(sourceID string) error {
	s.mediaScanMu.Lock()
	defer s.mediaScanMu.Unlock()

	var source MediaSource
	if err := s.db.Where("source_id = ?", sourceID).First(&source).Error; err != nil {
		return err
	}
	root, err := s.allowedMediaDirectory(source.RootPath)
	if err != nil {
		s.finishMediaScan(&source, 0, err)
		return err
	}
	_ = s.db.Model(&source).Updates(map[string]any{"state": "scanning", "last_error": "", "scanned_count": 0}).Error

	var count int64
	var visited int
	seen := make([]string, 0)
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		visited++
		if visited > maxMediaScanEntries {
			return errors.New("media source contains too many filesystem entries")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !supportedAudioExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
			return nil
		}
		if count >= maxMediaScanFiles {
			return errors.New("media source contains too many audio files")
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			return infoErr
		}
		serverFileID, idErr := serverFileIDForPath(path)
		if idErr != nil {
			return idErr
		}
		relative, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil {
			return relativeErr
		}
		relative = filepath.ToSlash(relative)
		if err := s.indexLocalMediaFile(source, path, relative, info, serverFileID); err != nil {
			return err
		}
		s.importLocalEmbeddedMetadata(path, serverFileID)
		seen = append(seen, relative)
		count++
		if count == 1 || count%25 == 0 {
			_ = s.db.Model(&source).Updates(map[string]any{"scanned_count": count}).Error
		}
		return nil
	})
	if walkErr == nil {
		walkErr = s.finishMediaLocations(source.SourceID, seen)
	}
	s.finishMediaScan(&source, count, walkErr)
	// The web client uses this event to refresh both files and source state.
	// Broadcast failures too, otherwise a modal can remain stuck in scanning.
	s.broadcastEvent(StationEvent{Kind: "server_file"})
	return walkErr
}

func (s *Service) indexLocalMediaFile(source MediaSource, path, relative string, info fs.FileInfo, serverFileID string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var file ServerFile
		err := tx.Where("server_file_id = ?", serverFileID).First(&file).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			file = ServerFile{
				ServerFileID: serverFileID, StoragePath: path, RelativePath: relative,
				SourceDeviceID: "source:" + source.SourceID, FileName: filepath.Base(path),
				Size: info.Size(), Version: 1, Title: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
				Format: strings.TrimPrefix(strings.ToUpper(filepath.Ext(path)), "."),
			}
			if err = tx.Create(&file).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if file.DeletedAt != nil {
			if err = tx.Model(&file).Updates(map[string]any{
				"deleted_at": nil, "storage_path": path, "relative_path": relative,
				"size": info.Size(), "source_device_id": "source:" + source.SourceID,
			}).Error; err != nil {
				return err
			}
		}
		var location MediaLocation
		err = tx.Where("source_id = ? AND relative_path = ?", source.SourceID, relative).First(&location).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			location = MediaLocation{LocationID: newID("ml"), SourceID: source.SourceID, RelativePath: relative}
		} else if err != nil {
			return err
		}
		previousServerFileID := location.ServerFileID
		location.ServerFileID = serverFileID
		location.LocalPath = path
		location.Size = info.Size()
		location.ModifiedAt = info.ModTime()
		location.Available = true
		if err = tx.Save(&location).Error; err != nil {
			return err
		}
		if previousServerFileID != "" && previousServerFileID != serverFileID {
			return retireUnavailableServerFile(tx, previousServerFileID)
		}
		return nil
	})
}

func (s *Service) finishMediaLocations(sourceID string, seen []string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&MediaLocation{}).Where("source_id = ?", sourceID)
		if len(seen) == 0 {
			query = query.Where("available = ?", true)
		} else {
			query = query.Where("relative_path NOT IN ? AND available = ?", seen, true)
		}
		var removed []MediaLocation
		if err := query.Find(&removed).Error; err != nil {
			return err
		}
		if len(removed) == 0 {
			return nil
		}
		ids := make([]uint, 0, len(removed))
		for _, location := range removed {
			ids = append(ids, location.ID)
		}
		if err := tx.Model(&MediaLocation{}).Where("id IN ?", ids).Update("available", false).Error; err != nil {
			return err
		}
		for _, location := range removed {
			if err := retireUnavailableServerFile(tx, location.ServerFileID); err != nil {
				return err
			}
		}
		return nil
	})
}

func retireUnavailableServerFile(tx *gorm.DB, serverFileID string) error {
	var available int64
	if err := tx.Model(&MediaLocation{}).Where("server_file_id = ? AND available = ?", serverFileID, true).Count(&available).Error; err != nil {
		return err
	}
	if available > 0 {
		return nil
	}
	now := time.Now()
	return tx.Model(&ServerFile{}).
		Where("server_file_id = ? AND source_device_id LIKE ?", serverFileID, "source:%").
		Update("deleted_at", now).Error
}

func (s *Service) finishMediaScan(source *MediaSource, count int64, scanErr error) {
	now := time.Now()
	updates := map[string]any{"state": "ready", "file_count": count, "scanned_count": count, "last_scanned": now, "last_error": ""}
	if scanErr != nil {
		updates["state"] = "error"
		updates["last_error"] = scanErr.Error()
	}
	_ = s.db.Model(source).Updates(updates).Error
}

func (s *Service) ensureServerFileHash(file *ServerFile) error {
	if file.Hash != "" {
		return nil
	}
	audio, err := s.OpenAudio(file.ServerFileID)
	if err != nil {
		return err
	}
	defer audio.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, audio); err != nil {
		return err
	}
	file.Hash = hex.EncodeToString(hash.Sum(nil))
	return s.db.Model(&ServerFile{}).Where("server_file_id = ?", file.ServerFileID).Update("hash", file.Hash).Error
}

func (s *Service) allowedMediaDirectory(path string) (string, error) {
	canonical, err := canonicalDirectory(path)
	if err != nil {
		return "", err
	}
	if filepath.Dir(canonical) == canonical {
		return "", errors.New("scanning a filesystem root is not allowed")
	}
	return canonical, nil
}

func (s *Service) mediaPathAllowed(path string) bool {
	// The caller is authenticated by Station's pairing middleware. Media
	// sources intentionally accept any readable directory, including mounts.
	return path != ""
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("media source is not a directory")
	}
	return filepath.Clean(canonical), nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
