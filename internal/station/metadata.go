package station

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"
)

const maxCoverSize = int64(10 * 1024 * 1024)

type MetadataUpdate struct {
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Lyrics      string
	TrackNumber int
	DiscNumber  int
	Cover       []byte
	CoverMIME   string
	RemoveCover bool
}

type MetadataResourceView struct {
	MetadataResource
	URL string `json:"url,omitempty"`
}

type ServerFileView struct {
	ServerFile
	ExistsOnServer  bool                   `json:"existsOnServer"`
	HasAudio        bool                   `json:"hasAudio"`
	HasCover        bool                   `json:"hasCover"`
	CoverURL        string                 `json:"coverUrl,omitempty"`
	Resources       []MetadataResourceView `json:"resources"`
	MetadataCount   int                    `json:"metadataCount"`
	CoverCount      int                    `json:"coverCount"`
	LyricsCount     int                    `json:"lyricsCount"`
	MissingMetadata bool                   `json:"missingMetadata"`
}

type metadataSnapshot struct {
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	AlbumArtist string `json:"albumArtist"`
	Track       int    `json:"track"`
	Disc        int    `json:"disc"`
}

func (s *Service) GetServerFile(id string) (ServerFileView, error) {
	var file ServerFile
	if err := s.db.Where("server_file_id = ? AND deleted_at IS NULL", id).First(&file).Error; err != nil {
		return ServerFileView{}, err
	}
	return s.serverFileView(file)
}

func (s *Service) serverFileView(file ServerFile) (ServerFileView, error) {
	var resources []MetadataResource
	if err := s.db.Where("server_file_id = ?", file.ServerFileID).
		Order("last_use_at desc, created_at desc").Find(&resources).Error; err != nil {
		return ServerFileView{}, err
	}
	view := ServerFileView{ServerFile: file, Resources: make([]MetadataResourceView, 0, len(resources))}
	activeCoverID := ""
	for _, resource := range resources {
		if resource.Type == "cover" && resource.LastUseAt != nil {
			if resource.Deleted {
				activeCoverID = "__deleted__"
			} else {
				activeCoverID = resource.ResourceID
			}
			break
		}
	}
	if activeCoverID == "" {
		for _, resource := range resources {
			if resource.Type == "cover" && !resource.Deleted && !isScrapedMetadataSource(resource.Source) {
				activeCoverID = resource.ResourceID
				break
			}
		}
	}
	if activeCoverID == "" {
		for _, resource := range resources {
			if resource.Type == "cover" && !resource.Deleted {
				activeCoverID = resource.ResourceID
				break
			}
		}
	}
	for _, resource := range resources {
		resourceView := MetadataResourceView{MetadataResource: resource}
		switch resource.Type {
		case "cover":
			isActive := resource.ResourceID == activeCoverID
			if !resource.Deleted {
				resourceView.URL = "/api/station/resources/" + resource.ResourceID
				view.CoverCount++
				if isActive {
					view.HasCover = true
					view.CoverURL = resourceView.URL
				}
			}
		case "lyrics":
			if !resource.Deleted {
				view.LyricsCount++
			}
		case "metadata":
			if !resource.Deleted {
				view.MetadataCount++
			}
		}
		view.Resources = append(view.Resources, resourceView)
	}
	view.MissingMetadata = strings.TrimSpace(file.Title) == "" || strings.TrimSpace(file.Artist) == "" ||
		strings.TrimSpace(file.Album) == ""
	hasAudio := s.HasAudioFile(file)
	view.HasAudio = hasAudio
	view.ExistsOnServer = hasAudio
	return view, nil
}

func (s *Service) HasAudioFile(file ServerFile) bool {
	if file.StoragePath != "" {
		if _, err := os.Stat(file.StoragePath); err == nil {
			return true
		}
	}
	var locations []MediaLocation
	if err := s.db.Where("server_file_id = ? AND available = ?", file.ServerFileID, true).Find(&locations).Error; err == nil {
		for _, location := range locations {
			if location.LocalPath != "" {
				if info, statErr := os.Stat(location.LocalPath); statErr == nil && info.Size() == location.Size {
					return true
				}
			}
		}
	}
	return false
}

func isScrapedMetadataSource(source string) bool {
	return source == "musicbrainz" || source == "coverartarchive" || source == "lrclib"
}

func activeCoverResource(resources []MetadataResource) (MetadataResource, error) {
	for _, resource := range resources {
		if resource.Type == "cover" && resource.LastUseAt != nil {
			if resource.Deleted {
				return MetadataResource{}, gorm.ErrRecordNotFound
			}
			return resource, nil
		}
	}
	for _, resource := range resources {
		if resource.Type == "cover" && !resource.Deleted && !isScrapedMetadataSource(resource.Source) {
			return resource, nil
		}
	}
	for _, resource := range resources {
		if resource.Type == "cover" && !resource.Deleted {
			return resource, nil
		}
	}
	return MetadataResource{}, gorm.ErrRecordNotFound
}

func (s *Service) OpenCover(id string) (*os.File, string, error) {
	var resource MetadataResource
	query := s.db.Where("type = ? AND deleted = ?", "cover", false)
	if strings.HasPrefix(id, "mr_") {
		query = query.Where("resource_id = ?", id)
		if err := query.First(&resource).Error; err != nil {
			return nil, "", err
		}
	} else {
		var resources []MetadataResource
		// Include deleted resources here so an explicit cover-removal tombstone
		// can suppress older cover resources during fallback selection.
		if err := s.db.Where("type = ?", "cover").Where("server_file_id = ?", id).
			Order("last_use_at desc, created_at desc").Find(&resources).Error; err != nil {
			return nil, "", err
		}
		var selectErr error
		resource, selectErr = activeCoverResource(resources)
		if selectErr != nil {
			return nil, "", selectErr
		}
	}
	cover, err := os.Open(resource.StoragePath)
	return cover, resource.MIME, err
}

// UpdateServerFileMetadata appends resources and updates searchable projections.
// It never reads, copies, replaces, or writes the immutable audio file.
func (s *Service) UpdateServerFileMetadata(id string, update MetadataUpdate) (ServerFileView, error) {
	lock := s.transferLock("metadata:" + id)
	lock.Lock()
	defer lock.Unlock()

	var current ServerFile
	if err := s.db.Where("server_file_id = ? AND deleted_at IS NULL", id).First(&current).Error; err != nil {
		return ServerFileView{}, err
	}
	if int64(len(update.Cover)) > maxCoverSize {
		return ServerFileView{}, fmt.Errorf("cover exceeds %d bytes", maxCoverSize)
	}
	if len(update.Cover) > 0 && update.CoverMIME != "image/jpeg" && update.CoverMIME != "image/png" {
		return ServerFileView{}, errors.New("cover must be JPEG or PNG")
	}

	now := time.Now()
	snapshot := metadataSnapshot{
		Title: strings.TrimSpace(update.Title), Artist: strings.TrimSpace(update.Artist),
		Album: strings.TrimSpace(update.Album), AlbumArtist: strings.TrimSpace(update.AlbumArtist),
		Track: max(update.TrackNumber, 0), Disc: max(update.DiscNumber, 0),
	}
	metadataJSON, err := json.Marshal(snapshot)
	if err != nil {
		return ServerFileView{}, err
	}
	resources := []MetadataResource{newTextResource(id, "metadata", "json", "", string(metadataJSON), false, now)}

	lyrics := normalizeLyrics(update.Lyrics)
	if lyrics != "" {
		format := "plain"
		if strings.Contains(lyrics, "[") && strings.Contains(lyrics, ":") {
			format = "lrc"
		}
		resources = append(resources, newTextResource(id, "lyrics", format, "", lyrics, false, now))
	} else {
		resources = append(resources, newTextResource(id, "lyrics", "plain", "", "", true, now))
	}

	var coverResource *MetadataResource
	if len(update.Cover) > 0 {
		resource := newBinaryResource(id, "cover", update.CoverMIME, update.Cover, false, now)
		extension := ".jpg"
		if update.CoverMIME == "image/png" {
			extension = ".png"
		}
		resource.StoragePath = filepath.Join(s.storageRoot, "artwork", resource.ResourceHash+extension)
		coverResource = &resource
		resources = append(resources, resource)
	} else if update.RemoveCover {
		resources = append(resources, newBinaryResource(id, "cover", "", nil, true, now))
	}
	if coverResource != nil {
		if _, statErr := os.Stat(coverResource.StoragePath); errors.Is(statErr, os.ErrNotExist) {
			if err = os.WriteFile(coverResource.StoragePath, update.Cover, 0644); err != nil {
				return ServerFileView{}, err
			}
		}
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		for index := range resources {
			if err := appendResource(tx, &resources[index]); err != nil {
				return err
			}
		}
		return tx.Model(&ServerFile{}).Where("server_file_id = ?", id).Updates(map[string]any{
			"title": snapshot.Title, "artist": snapshot.Artist, "album": snapshot.Album,
			"album_artist": snapshot.AlbumArtist, "lyrics": lyrics,
			"track_number": snapshot.Track, "disc_number": snapshot.Disc,
		}).Error
	})
	if err != nil {
		return ServerFileView{}, err
	}
	if err := s.db.Where("server_file_id = ?", id).First(&current).Error; err != nil {
		return ServerFileView{}, err
	}
	s.emitServerFileChanged(current)
	s.broadcastMetadataUpdated(id)
	return s.serverFileView(current)
}

func appendResource(tx *gorm.DB, resource *MetadataResource) error {
	var existing MetadataResource
	err := tx.Where("server_file_id = ? AND resource_hash = ?", resource.ServerFileID, resource.ResourceHash).First(&existing).Error
	if err == nil {
		if resource.LastUseAt == nil {
			return nil
		}
		return tx.Model(&existing).Update("last_use_at", resource.LastUseAt).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(resource).Error
}

func newTextResource(fileID, kind, format, language, data string, deleted bool, now time.Time) MetadataResource {
	payload := []byte(kind + "\x00" + format + "\x00" + language + "\x00" + data)
	hash := sha256.Sum256(payload)
	return MetadataResource{
		ResourceID: newID("mr"), ServerFileID: fileID, Type: kind,
		ResourceHash: hex.EncodeToString(hash[:]), Data: data, Source: "station",
		Format: format, Language: language, Deleted: deleted, LastUseAt: &now,
	}
}

func newBinaryResource(fileID, kind, mimeType string, data []byte, deleted bool, now time.Time) MetadataResource {
	hash := sha256.New()
	_, _ = hash.Write([]byte(kind + "\x00" + mimeType + "\x00"))
	_, _ = hash.Write(data)
	return MetadataResource{
		ResourceID: newID("mr"), ServerFileID: fileID, Type: kind,
		ResourceHash: hex.EncodeToString(hash.Sum(nil)), MIME: mimeType,
		Source: "station", Format: mimeType, Deleted: deleted, LastUseAt: &now,
	}
}

func normalizeLyrics(value string) string {
	value = strings.TrimPrefix(value, "\ufeff")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}

func (s *Service) ListResources(serverFileID string) ([]MetadataResourceView, error) {
	var resources []MetadataResource
	query := s.db.Order("last_use_at desc, created_at desc")
	if serverFileID != "" {
		query = query.Where("server_file_id = ?", serverFileID)
	}
	if err := query.Find(&resources).Error; err != nil {
		return nil, err
	}
	views := make([]MetadataResourceView, 0, len(resources))
	for _, resource := range resources {
		view := MetadataResourceView{MetadataResource: resource}
		if resource.Type == "cover" && !resource.Deleted {
			view.URL = "/api/station/resources/" + resource.ResourceID
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *Service) ActivateResource(resourceID string) (ServerFileView, error) {
	var resource MetadataResource
	if err := s.db.Where("resource_id = ?", resourceID).First(&resource).Error; err != nil {
		return ServerFileView{}, err
	}
	now := time.Now()
	if err := s.db.Model(&resource).Update("last_use_at", now).Error; err != nil {
		return ServerFileView{}, err
	}
	var file ServerFile
	if err := s.db.Where("server_file_id = ?", resource.ServerFileID).First(&file).Error; err != nil {
		return ServerFileView{}, err
	}
	updates := map[string]any{}
	switch resource.Type {
	case "metadata":
		var snapshot metadataSnapshot
		if err := json.Unmarshal([]byte(resource.Data), &snapshot); err != nil {
			return ServerFileView{}, err
		}
		updates = map[string]any{
			"title": snapshot.Title, "artist": snapshot.Artist, "album": snapshot.Album,
			"album_artist": snapshot.AlbumArtist, "track_number": snapshot.Track, "disc_number": snapshot.Disc,
		}
	case "lyrics":
		updates["lyrics"] = resource.Data
	}
	if len(updates) > 0 {
		if err := s.db.Model(&file).Updates(updates).Error; err != nil {
			return ServerFileView{}, err
		}
	}
	if err := s.db.Where("server_file_id = ?", resource.ServerFileID).First(&file).Error; err != nil {
		return ServerFileView{}, err
	}
	s.emitServerFileChanged(file)
	s.broadcastMetadataUpdated(file.ServerFileID)
	return s.serverFileView(file)
}

func CoverMIME(header string) string {
	value, _, _ := mime.ParseMediaType(header)
	return strings.ToLower(value)
}
