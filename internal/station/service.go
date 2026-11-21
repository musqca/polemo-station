package station

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wonli/aqi/ws"
	"gorm.io/gorm"
)

type Device struct {
	ID         uint      `gorm:"primaryKey" json:"-"`
	DeviceID   string    `gorm:"uniqueIndex;size:128" json:"deviceId"`
	Name       string    `gorm:"size:255" json:"name"`
	Platform   string    `gorm:"size:32" json:"platform"`
	AppVersion string    `gorm:"size:64" json:"appVersion"`
	IP         string    `gorm:"size:64" json:"ip"`
	Online     bool      `json:"online"`
	Pinned     bool      `gorm:"default:false" json:"pinned"`
	Hidden     bool      `gorm:"default:false;index" json:"-"`
	LastSeen   time.Time `json:"lastSeen"`
	TrackCount int64     `json:"trackCount" gorm:"-"`
	Pending    int64     `json:"pending" gorm:"-"`
}

type Track struct {
	ID             uint      `gorm:"primaryKey" json:"-"`
	DeviceID       string    `gorm:"uniqueIndex:ux_device_path;size:128" json:"deviceId"`
	FileID         string    `gorm:"size:255;index" json:"fileId"`
	RelativePath   string    `gorm:"uniqueIndex:ux_device_path;size:1024" json:"relativePath"`
	Folder         string    `gorm:"size:1024" json:"folder"`
	FileName       string    `gorm:"size:255" json:"fileName"`
	Size           int64     `json:"size"`
	ModifiedAt     time.Time `json:"modifiedAt"`
	Title          string    `gorm:"size:255" json:"title"`
	Artist         string    `gorm:"size:255" json:"artist"`
	Album          string    `gorm:"size:255" json:"album"`
	AlbumArtist    string    `gorm:"size:255" json:"albumArtist"`
	DurationMs     int64     `json:"durationMs"`
	TrackNumber    int       `json:"track"`
	DiscNumber     int       `json:"disc"`
	Format         string    `gorm:"size:32" json:"format"`
	Bitrate        int       `json:"bitrate"`
	SyncState      string    `gorm:"size:32;index" json:"syncState"`
	ServerFileID   string    `gorm:"size:64;index" json:"serverFileId"`
	AppliedVersion int64     `json:"appliedVersion"`
	Hash           string    `gorm:"size:64" json:"hash"`
	SnapshotID     string    `gorm:"size:64;index" json:"-"`
	LastSeenAt     time.Time `json:"lastSeenAt"`
	ServerVersion  int64     `gorm:"-" json:"serverVersion"`
	ServerHash     string    `gorm:"-" json:"serverHash"`
}

type ServerFile struct {
	ID             uint       `gorm:"primaryKey" json:"-"`
	ServerFileID   string     `gorm:"uniqueIndex;size:64" json:"serverFileId"`
	StoragePath    string     `gorm:"size:2048" json:"-"`
	RelativePath   string     `gorm:"size:1024;index" json:"relativePath"`
	SourceDeviceID string     `gorm:"size:128;index" json:"sourceDeviceId"`
	FileName       string     `gorm:"size:255" json:"fileName"`
	Size           int64      `json:"size"`
	Hash           string     `gorm:"size:64;index" json:"hash"`
	Version        int64      `json:"version"`
	Title          string     `gorm:"size:255" json:"title"`
	Artist         string     `gorm:"size:255" json:"artist"`
	Album          string     `gorm:"size:255" json:"album"`
	AlbumArtist    string     `gorm:"size:255" json:"albumArtist"`
	Lyrics         string     `gorm:"type:text" json:"lyrics"`
	TrackNumber    int        `json:"track"`
	DiscNumber     int        `json:"disc"`
	CoverPath      string     `gorm:"size:2048" json:"-"`
	CoverMIME      string     `gorm:"size:64" json:"coverMime"`
	Format         string     `gorm:"size:32" json:"format"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	DeletedAt      *time.Time `gorm:"index" json:"deletedAt,omitempty"`
}

type MetadataResource struct {
	ID           uint       `gorm:"primaryKey" json:"-"`
	ResourceID   string     `gorm:"uniqueIndex;size:64" json:"resourceId"`
	ServerFileID string     `gorm:"uniqueIndex:ux_file_resource_hash;size:72;index" json:"serverFileId"`
	Type         string     `gorm:"size:16;index" json:"type"`
	ResourceHash string     `gorm:"uniqueIndex:ux_file_resource_hash;size:64" json:"resourceHash"`
	Data         string     `gorm:"type:text" json:"data,omitempty"`
	StoragePath  string     `gorm:"size:2048" json:"-"`
	MIME         string     `gorm:"size:128" json:"mime,omitempty"`
	Source       string     `gorm:"size:32" json:"source"`
	Format       string     `gorm:"size:32" json:"format,omitempty"`
	Language     string     `gorm:"size:32" json:"language,omitempty"`
	Deleted      bool       `json:"deleted"`
	LastUseAt    *time.Time `gorm:"index" json:"lastUseAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}

type TransferTask struct {
	ID               uint      `gorm:"primaryKey" json:"-"`
	TaskID           string    `gorm:"uniqueIndex;size:64" json:"taskId"`
	BatchID          string    `gorm:"size:64;index" json:"batchId"`
	ServerFileID     string    `gorm:"size:64;index" json:"serverFileId"`
	DeviceID         string    `gorm:"size:128;index" json:"deviceId"`
	Direction        string    `gorm:"size:16" json:"direction"`
	FileID           string    `gorm:"size:255" json:"fileId"`
	RelativePath     string    `gorm:"size:1024" json:"relativePath"`
	FileName         string    `gorm:"size:255" json:"fileName"`
	ExpectedHash     string    `gorm:"size:64" json:"expectedHash"`
	TotalBytes       int64     `json:"totalBytes"`
	TransferredBytes int64     `json:"transferredBytes"`
	BaseVersion      int64     `json:"baseVersion"`
	TargetVersion    int64     `json:"targetVersion"`
	TempPath         string    `gorm:"size:2048" json:"-"`
	State            string    `gorm:"size:32;index" json:"state"`
	LastError        string    `gorm:"size:1024" json:"lastError"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type TransferBatch struct {
	ID               uint       `gorm:"primaryKey" json:"-"`
	BatchID          string     `gorm:"uniqueIndex;size:64" json:"batchId"`
	DeviceID         string     `gorm:"size:128;index" json:"deviceId"`
	Direction        string     `gorm:"size:16;index" json:"direction"`
	State            string     `gorm:"size:32;index" json:"state"`
	TotalFiles       int        `json:"totalFiles"`
	TotalBytes       int64      `json:"totalBytes"`
	CompletedFiles   int        `json:"completedFiles"`
	FailedFiles      int        `json:"failedFiles"`
	TransferredBytes int64      `json:"transferredBytes"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	CompletedAt      *time.Time `gorm:"index" json:"completedAt,omitempty"`
}

type SyncOffer struct {
	TargetLibraryID string    `gorm:"size:64" json:"targetLibraryId,omitempty"`
	ID              uint      `gorm:"primaryKey" json:"-"`
	OfferID         string    `gorm:"uniqueIndex;size:64" json:"offerId"`
	DeviceID        string    `gorm:"size:128;index" json:"deviceId"`
	State           string    `gorm:"size:32;index" json:"state"`
	Choice          string    `gorm:"size:32" json:"choice,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type SyncOfferItem struct {
	ID           uint   `gorm:"primaryKey" json:"-"`
	OfferID      string `gorm:"size:64;index" json:"offerId"`
	ServerFileID string `gorm:"size:72;index" json:"serverFileId"`
	RelativePath string `gorm:"size:1024" json:"relativePath"`
	FileName     string `gorm:"size:255" json:"fileName"`
	Size         int64  `json:"size"`
	Hash         string `gorm:"size:64" json:"hash"`
	Title        string `gorm:"size:255" json:"title"`
	Artist       string `gorm:"size:255" json:"artist"`
	Album        string `gorm:"size:255" json:"album"`
	Format       string `gorm:"size:32" json:"format"`
}

// SyncQueueItem is a Station-owned, durable draft. It is deliberately separate
// from SyncOffer: adding an item must never contact a device until the user
// sends the whole list.
type SyncQueueItem struct {
	ID           uint      `gorm:"primaryKey" json:"-"`
	QueueItemID  string    `gorm:"uniqueIndex;size:64" json:"queueItemId"`
	DeviceID     string    `gorm:"uniqueIndex:ux_sync_queue_device_file;size:128;index" json:"deviceId"`
	ServerFileID string    `gorm:"uniqueIndex:ux_sync_queue_device_file;size:72;index" json:"serverFileId"`
	CreatedAt    time.Time `json:"createdAt"`
}

type MediaSource struct {
	ID           uint       `gorm:"primaryKey" json:"-"`
	SourceID     string     `gorm:"uniqueIndex;size:64" json:"sourceId"`
	Type         string     `gorm:"size:32;index" json:"type"`
	Name         string     `gorm:"size:255" json:"name"`
	RootPath     string     `gorm:"size:2048" json:"rootPath"`
	State        string     `gorm:"size:32;index" json:"state"`
	FileCount    int64      `json:"fileCount"`
	ScannedCount int64      `json:"scannedCount"`
	LastError    string     `gorm:"size:1024" json:"lastError,omitempty"`
	LastScanned  *time.Time `json:"lastScanned,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

type MediaLocation struct {
	ID           uint      `gorm:"primaryKey" json:"-"`
	LocationID   string    `gorm:"uniqueIndex;size:64" json:"locationId"`
	ServerFileID string    `gorm:"size:72;index" json:"serverFileId"`
	SourceID     string    `gorm:"uniqueIndex:ux_source_relative;size:64;index" json:"sourceId"`
	RelativePath string    `gorm:"uniqueIndex:ux_source_relative;size:1024" json:"relativePath"`
	LocalPath    string    `gorm:"size:2048" json:"-"`
	Size         int64     `json:"size"`
	ModifiedAt   time.Time `json:"modifiedAt"`
	Available    bool      `gorm:"index" json:"available"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Service struct {
	db                      *gorm.DB
	pairing                 string
	publicBaseURL           string
	publicWebSocketURL      string
	authorizationClients    sync.Map
	webAuthorizationClients sync.Map
	storageRoot             string
	transferMu              sync.Mutex
	transferLocks           sync.Map
	activeTransfers         sync.Map
	cancelledTransfers      sync.Map
	onlineDevices           sync.Map
	libraryRequests         sync.Map
	nowPlaying              sync.Map
	webPlaybackMu           sync.RWMutex
	webPlayback             *webPlaybackRequest
	transferEventMu         sync.Mutex
	transferEventAt         map[string]time.Time
	mediaRoots              []string
	mediaScanMu             sync.Mutex
	scrapeOnce              sync.Once
	scrapeRateMu            sync.Mutex
	scrapeProviderNext      map[string]time.Time
	scrapeConfigMu          sync.RWMutex
	scrapeHTTPClient        *http.Client
	scrapeProviderBaseURL   map[string]string
	disconnectOnce          sync.Once
	transcodeSlots          chan struct{}
	transcodeProfilesMu     sync.RWMutex
	transcodeProfiles       map[string]TranscodeProfile
	transcodeJobsMu         sync.Mutex
	transcodeJobs           map[string]*transcodeJob
	ffmpegPath              string
}

type StationSecuritySettings struct {
	ID         uint   `gorm:"primaryKey"`
	AccessMode string `json:"accessMode" gorm:"size:16;default:public"`
	StationID  string `json:"stationId" gorm:"size:96"`
}

// SetFFmpegPath configures an external FFmpeg executable. An empty value uses
// the system PATH, allowing FFmpeg to remain an optional dependency.
func (s *Service) SetFFmpegPath(path string) {
	s.ffmpegPath = strings.TrimSpace(path)
}

func (s *Service) transferLock(taskID string) *sync.Mutex {
	lock, _ := s.transferLocks.LoadOrStore(taskID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func NewService(db *gorm.DB, pairingToken, storageRoot string) *Service {
	return &Service{
		db:              db,
		pairing:         strings.TrimSpace(pairingToken),
		storageRoot:     filepath.Clean(storageRoot),
		transferEventAt: make(map[string]time.Time),
		// Keep media conversion bounded. A single Station should remain usable
		// for transfers and control traffic while a browser is playing media.
		transcodeSlots:        make(chan struct{}, 2),
		transcodeProfiles:     defaultTranscodeProfiles(),
		transcodeJobs:         make(map[string]*transcodeJob),
		scrapeProviderNext:    make(map[string]time.Time),
		scrapeProviderBaseURL: make(map[string]string),
	}
}

// SetTranscodeProfiles replaces server-side profile values while retaining the
// built-in profiles for entries omitted from configuration.
func (s *Service) SetTranscodeProfiles(profiles map[string]TranscodeProfile) error {
	merged := defaultTranscodeProfiles()
	for id, profile := range profiles {
		profile.ID = strings.ToLower(strings.TrimSpace(id))
		if err := validateTranscodeProfile(profile); err != nil {
			return err
		}
		merged[profile.ID] = profile
	}
	s.transcodeProfilesMu.Lock()
	s.transcodeProfiles = merged
	s.transcodeProfilesMu.Unlock()
	return nil
}

func (s *Service) transcodeProfile(profileID string) (TranscodeProfile, error) {
	profileID = strings.ToLower(strings.TrimSpace(profileID))
	if profileID == "" {
		profileID = "mobile"
	}
	s.transcodeProfilesMu.RLock()
	profile, ok := s.transcodeProfiles[profileID]
	s.transcodeProfilesMu.RUnlock()
	if !ok {
		return TranscodeProfile{}, fmt.Errorf("unknown transcode profile: %s", profileID)
	}
	return profile, nil
}

// SetScrapeHTTPClient replaces the HTTP client used by scrape providers. It is
// intended for tests and local deployments; nil restores the default client.
func (s *Service) SetScrapeHTTPClient(client *http.Client) {
	s.scrapeConfigMu.Lock()
	s.scrapeHTTPClient = client
	s.scrapeConfigMu.Unlock()
}

// SetScrapeProviderBaseURL overrides one provider endpoint. An empty URL
// restores that provider's production endpoint.
func (s *Service) SetScrapeProviderBaseURL(provider, baseURL string) error {
	if !isScrapeProvider(provider) {
		return errors.New("unknown scrape provider")
	}
	s.scrapeConfigMu.Lock()
	if strings.TrimSpace(baseURL) == "" {
		delete(s.scrapeProviderBaseURL, provider)
	} else {
		s.scrapeProviderBaseURL[provider] = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	}
	s.scrapeConfigMu.Unlock()
	return nil
}

// SetPublicBaseURL configures the externally reachable HTTP(S) base URL used
// in Station authorization QR payloads. The /ws endpoint is derived from it.
func (s *Service) SetPublicBaseURL(value string) error {
	base, websocket, err := parsePublicBaseURL(value)
	if err != nil {
		return err
	}
	s.publicBaseURL = base
	s.publicWebSocketURL = websocket
	return nil
}

// WatchDisconnects bridges aqi/ws lifecycle events back to Station state.
func (s *Service) WatchDisconnects() {
	s.disconnectOnce.Do(func() {
		go func() {
			for ws.Hub == nil || ws.Hub.PubSub == nil {
				time.Sleep(10 * time.Millisecond)
			}
			ws.Hub.PubSub.SubFunc("disconnect", func(message *ws.TopicMsg) {
				client, ok := message.Ori.(*ws.Client)
				if !ok || client == nil {
					return
				}
				value := client.GetKey("stationDeviceId")
				if value == nil || s.db == nil {
					return
				}
				deviceID := value.String()
				if current, loaded := s.onlineDevices.Load(deviceID); loaded && current != client {
					// A replaced socket can finish closing after the new socket has
					// already completed hello. Do not take the new connection offline.
					return
				}
				s.onlineDevices.Delete(deviceID)
				s.nowPlaying.Delete(deviceID)
				s.pauseDeviceDownloads(deviceID)
				if s.db.Model(&Device{}).Where("device_id = ?", deviceID).Updates(map[string]any{"online": false, "last_seen": time.Now()}).Error == nil {
					s.emitDeviceChanged(deviceID)
				}
			})
		}()
	})
}
func (s *Service) Migrate() error {
	if s.db == nil {
		return errors.New("station database is not initialized")
	}
	if err := os.MkdirAll(filepath.Join(s.storageRoot, "objects"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.storageRoot, "temp"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.storageRoot, "artwork"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.storageRoot, "library"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.storageRoot, "transcodes"), 0755); err != nil {
		return err
	}
	// Authorization v2 intentionally starts clean. Legacy authorization rows are
	// not compatible with the separated account, credential, session and ticket models.
	if s.db.Migrator().HasTable("station_authorizations") {
		if err := s.db.Migrator().DropTable("station_authorizations"); err != nil {
			return err
		}
	}
	// Earlier v2 development builds accidentally made account names globally
	// unique, which prevented a reset superadmin from claiming the same name.
	if s.db.Migrator().HasTable(&StationAccount{}) && s.db.Migrator().HasIndex(&StationAccount{}, "idx_station_accounts_name") {
		if err := s.db.Migrator().DropIndex(&StationAccount{}, "idx_station_accounts_name"); err != nil {
			return err
		}
	}
	if err := s.db.AutoMigrate(&Device{}, &Track{}, &ServerFile{}, &MetadataResource{}, &TransferBatch{}, &TransferTask{}, &SyncOffer{}, &SyncOfferItem{}, &SyncQueueItem{}, &MediaSource{}, &MediaLocation{},
		&StationAccount{}, &StationAppCredential{}, &StationWebSession{}, &StationQrTicket{}, &StationSecuritySettings{},
		&ThirdPartyProviderConfig{}, &ScrapeItem{}, &ScrapeRun{}, &scrapeSettingsRow{}); err != nil {
		return err
	}
	// SQLite adds newly migrated columns as NULL to pre-existing rows. Normalize
	// the device-list flags so older devices remain visible and unpinned.
	if err := s.db.Model(&Device{}).Where("hidden IS NULL OR pinned IS NULL").
		Updates(map[string]any{"hidden": false, "pinned": false}).Error; err != nil {
		return err
	}
	if err := s.migrateMetadataCursor(); err != nil {
		return err
	}
	if err := s.migrateTracksToServerFiles(); err != nil {
		return err
	}
	if _, err := s.stationIdentity(); err != nil {
		return err
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	var expiredBatchIDs []string
	if s.db.Model(&TransferBatch{}).
		Where("completed_at IS NOT NULL AND completed_at < ?", cutoff).
		Pluck("batch_id", &expiredBatchIDs).Error == nil && len(expiredBatchIDs) > 0 {
		_ = s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("batch_id IN ?", expiredBatchIDs).Delete(&TransferTask{}).Error; err != nil {
				return err
			}
			return tx.Where("batch_id IN ?", expiredBatchIDs).Delete(&TransferBatch{}).Error
		})
	}
	// Early Station builds keyed device files by a path-derived fileId. The
	// canonical device identity is now deviceId + relativePath.
	if s.db.Migrator().HasIndex(&Track{}, "ux_device_file") {
		if err := s.db.Migrator().DropIndex(&Track{}, "ux_device_file"); err != nil {
			return err
		}
	}
	if err := s.initializeScrapeSettings(); err != nil {
		return err
	}
	// Versions before the dedicated image Accept header made TheAudioDB cover
	// downloads fail with IIS 406 responses. Those failures are safe to retry
	// once after upgrading because they are caused by our old request, not a
	// missing album or cover.
	if err := s.db.Model(&ScrapeItem{}).
		Where("provider = ? AND state = ? AND last_error LIKE ?", "theaudiodb", ScrapeFailed, "theaudiodb returned HTTP 406:%").
		Updates(map[string]any{"state": ScrapePending, "failure_count": 0, "next_retry_at": nil, "last_error": "", "last_error_code": "", "finished_at": nil, "lease_until": nil}).Error; err != nil {
		return err
	}
	return nil
}

func (s *Service) Authorized(r *http.Request) bool {
	// A public Station intentionally has no login boundary. It must apply to all
	// clients, not only LAN peers: Station Web uses the same APIs to edit
	// metadata and can be served through a reverse proxy.
	if !s.authorizationRequired() {
		return true
	}
	queryToken := r.URL.Query().Get("token")
	effectiveToken := stationTokenFromRequest(r)
	if _, err := s.identityForToken(effectiveToken); err == nil {
		return true
	}
	return s.pairing != "" && (r.Header.Get("Authorization") == "Bearer "+s.pairing || queryToken == s.pairing)
}

func (s *Service) upsertTracks(deviceID, snapshotID string, tracks []Track) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		for i := range tracks {
			tracks[i].DeviceID = deviceID
			tracks[i].SnapshotID = snapshotID
			// serverFileID is the stable identity of an audio file across all
			// devices. A remote-only track has no local path, so give old clients
			// a deterministic path as well; the path remains display metadata,
			// never the primary identity when serverFileID is available.
			if tracks[i].ServerFileID != "" && tracks[i].RelativePath == "" {
				tracks[i].RelativePath = ".polemo-station/" + tracks[i].ServerFileID
			}
			if tracks[i].SyncState == "" {
				tracks[i].SyncState = "local"
			}
			tracks[i].LastSeenAt = time.Now()
			var current Track
			var err error
			if tracks[i].ServerFileID != "" {
				err = tx.Where("device_id = ? AND server_file_id = ?", deviceID, tracks[i].ServerFileID).First(&current).Error
			}
			// Files not yet associated with a Station have no serverFileID. For
			// those, and when a new file replaces an old file at the same path,
			// retain the path fallback.
			if errors.Is(err, gorm.ErrRecordNotFound) || tracks[i].ServerFileID == "" {
				err = tx.Where("device_id = ? AND relative_path = ?", deviceID, tracks[i].RelativePath).First(&current).Error
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err = tx.Create(&tracks[i]).Error; err != nil {
					return err
				}
			} else if err == nil {
				// A freshly rebuilt local index does not know which Station file was
				// previously applied. The device/path identity is stable, so an empty
				// association in a snapshot must not erase the server-side history.
				if current.ServerFileID != "" && tracks[i].ServerFileID == "" {
					tracks[i].ServerFileID = current.ServerFileID
					tracks[i].AppliedVersion = current.AppliedVersion
					tracks[i].Hash = current.Hash
					tracks[i].SyncState = current.SyncState
				}
				tracks[i].ID = current.ID
				if err = tx.Save(&tracks[i]).Error; err != nil {
					return err
				}
			} else {
				return err
			}
			if tracks[i].ServerFileID != "" {
				var sf ServerFile
				if lookupErr := tx.Where("server_file_id = ?", tracks[i].ServerFileID).First(&sf).Error; errors.Is(lookupErr, gorm.ErrRecordNotFound) {
					fileName := filepath.Base(tracks[i].RelativePath)
					if fileName == "" || fileName == "." {
						fileName = tracks[i].Title
						if fileName == "" {
							fileName = tracks[i].ServerFileID
						}
					}
					placeholder := ServerFile{
						ServerFileID:   tracks[i].ServerFileID,
						SourceDeviceID: deviceID,
						StoragePath:    "",
						FileName:       fileName,
						RelativePath:   tracks[i].RelativePath,
						Title:          tracks[i].Title,
						Artist:         tracks[i].Artist,
						Album:          tracks[i].Album,
						AlbumArtist:    tracks[i].AlbumArtist,
						Format:         tracks[i].Format,
						Size:           tracks[i].Size,
						Hash:           tracks[i].Hash,
						TrackNumber:    tracks[i].TrackNumber,
						DiscNumber:     tracks[i].DiscNumber,
						CreatedAt:      time.Now(),
						UpdatedAt:      time.Now(),
					}
					if createErr := tx.Create(&placeholder).Error; createErr != nil {
						return createErr
					}
				}
			}
		}
		return nil
	})
}

func (s *Service) migrateTracksToServerFiles() error {
	var tracks []Track
	if err := s.db.Where("server_file_id <> '' AND server_file_id IS NOT NULL").
		Order("last_seen_at desc").Find(&tracks).Error; err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, track := range tracks {
		if seen[track.ServerFileID] {
			continue
		}
		seen[track.ServerFileID] = true
		var existing ServerFile
		err := s.db.Where("server_file_id = ?", track.ServerFileID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fileName := filepath.Base(track.RelativePath)
			if fileName == "" || fileName == "." {
				fileName = track.Title
				if fileName == "" {
					fileName = track.ServerFileID
				}
			}
			file := ServerFile{
				ServerFileID:   track.ServerFileID,
				SourceDeviceID: track.DeviceID,
				StoragePath:    "",
				FileName:       fileName,
				RelativePath:   track.RelativePath,
				Title:          track.Title,
				Artist:         track.Artist,
				Album:          track.Album,
				AlbumArtist:    track.AlbumArtist,
				Format:         track.Format,
				Size:           track.Size,
				Hash:           track.Hash,
				TrackNumber:    track.TrackNumber,
				DiscNumber:     track.DiscNumber,
				CreatedAt:      time.Now(),
				UpdatedAt:      time.Now(),
			}
			if err := s.db.Create(&file).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ListDevices() ([]Device, error) {
	var devices []Device
	// Existing Station databases receive this column through AutoMigrate. SQLite
	// keeps the new value NULL for pre-existing rows, which has the same meaning
	// as false: the device remains visible.
	if err := s.db.Where("hidden = ? OR hidden IS NULL", false).Order("pinned desc, last_seen desc").Find(&devices).Error; err != nil {
		return nil, err
	}
	for i := range devices {
		s.db.Model(&Track{}).Where("device_id = ?", devices[i].DeviceID).Count(&devices[i].TrackCount)
		s.db.Model(&Track{}).Where("device_id = ? AND sync_state NOT IN (?)", devices[i].DeviceID, []string{"synced", "local"}).Count(&devices[i].Pending)
	}
	return devices, nil
}
func (s *Service) ListTracks(id string) ([]Track, error) {
	var tracks []Track
	if err := s.db.Where("device_id = ?", id).Order("folder asc, file_name asc").Find(&tracks).Error; err != nil {
		return nil, err
	}
	for i := range tracks {
		if tracks[i].ServerFileID == "" {
			continue
		}
		var serverFile ServerFile
		if err := s.db.Select("hash").Where("server_file_id = ? AND deleted_at IS NULL", tracks[i].ServerFileID).First(&serverFile).Error; err == nil {
			tracks[i].ServerHash = serverFile.Hash
		}
	}
	return tracks, nil
}

func (s *Service) ListServerFiles() ([]ServerFileView, error) {
	var files []ServerFile
	if err := s.db.Where("deleted_at IS NULL").Order("created_at desc, file_name asc").Find(&files).Error; err != nil {
		return nil, err
	}
	views := make([]ServerFileView, 0, len(files))
	for _, file := range files {
		view, err := s.serverFileView(file)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *Service) OpenAudio(serverFileID string) (*os.File, error) {
	var locations []MediaLocation
	if err := s.db.Where("server_file_id = ? AND available = ?", serverFileID, true).
		Order("updated_at desc").Find(&locations).Error; err == nil {
		for _, location := range locations {
			info, statErr := os.Stat(location.LocalPath)
			if statErr != nil || info.Size() != location.Size || !info.ModTime().Equal(location.ModifiedAt) {
				continue
			}
			if file, openErr := os.Open(location.LocalPath); openErr == nil {
				return file, nil
			}
		}
		if len(locations) > 0 {
			return nil, os.ErrNotExist
		}
	}
	var file ServerFile
	if err := s.db.Select("storage_path").Where("server_file_id = ? AND deleted_at IS NULL", serverFileID).First(&file).Error; err != nil {
		return nil, err
	}
	return os.Open(file.StoragePath)
}

func (s *Service) ListTransfers() ([]TransferTask, error) {
	var tasks []TransferTask
	if err := s.db.Order("updated_at desc").Limit(100).Find(&tasks).Error; err != nil {
		return nil, err
	}
	// The dashboard normally only needs recent tasks, but a batch can be much
	// larger than that. When any task in a batch is still active, include the
	// rest of that batch so clients can report its real file count and size.
	activeStates := map[string]bool{
		"prepared": true, "uploading": true, "downloading": true,
		"transferring": true, "paused": true,
	}
	batchIDs := make([]string, 0)
	seenBatches := make(map[string]bool)
	for _, task := range tasks {
		if task.BatchID != "" && activeStates[task.State] && !seenBatches[task.BatchID] {
			seenBatches[task.BatchID] = true
			batchIDs = append(batchIDs, task.BatchID)
		}
	}
	if len(batchIDs) == 0 {
		return tasks, nil
	}
	var batchTasks []TransferTask
	if err := s.db.Where("batch_id IN ?", batchIDs).Find(&batchTasks).Error; err != nil {
		return nil, err
	}
	seenTasks := make(map[string]bool, len(tasks)+len(batchTasks))
	combined := make([]TransferTask, 0, len(tasks)+len(batchTasks))
	for _, task := range append(tasks, batchTasks...) {
		if !seenTasks[task.TaskID] {
			seenTasks[task.TaskID] = true
			combined = append(combined, task)
		}
	}
	return combined, nil
}
func (s *Service) Stats() (map[string]any, error) {
	var devices, tracks, pending int64
	if err := s.db.Model(&Device{}).Where("online = ?", true).Count(&devices).Error; err != nil {
		return nil, err
	}
	if err := s.db.Model(&Track{}).Count(&tracks).Error; err != nil {
		return nil, err
	}
	if err := s.db.Model(&Track{}).Where("sync_state NOT IN (?)", []string{"synced", "local"}).Count(&pending).Error; err != nil {
		return nil, err
	}
	return map[string]any{"onlineDevices": devices, "tracks": tracks, "pending": pending}, nil
}
