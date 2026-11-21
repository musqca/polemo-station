package station

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/musqca/polemo-station/internal/thirdparty/coverartarchive"
	"github.com/musqca/polemo-station/internal/thirdparty/fanarttv"
	"github.com/musqca/polemo-station/internal/thirdparty/lrclib"
	"github.com/musqca/polemo-station/internal/thirdparty/musicbrainz"
	"github.com/musqca/polemo-station/internal/thirdparty/theaudiodb"
	"gorm.io/gorm"
)

var scrapeProviders = []string{"musicbrainz", "coverartarchive", "fanarttv", "lrclib", "theaudiodb"}
var errScrapeDependency = errors.New("scrape dependency is not ready")
var errScrapeProviderDisabled = errors.New("scrape provider is disabled")

const (
	scrapeDependencyRetryDelay = 2 * time.Second
	maxScrapeDependencyWaits   = 30
)

func (s *Service) initializeScrapeSettings() error {
	var settings scrapeSettingsRow
	if err := s.db.First(&settings, 1).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		defaults := defaultScrapeSettings()
		if err := s.db.Create(&scrapeSettingsRow{ID: 1, Paused: false, MaxFailures: defaults.MaxFailures, RequestTimeoutSeconds: defaults.RequestTimeoutSeconds, ItemTimeoutSeconds: defaults.ItemTimeoutSeconds, RetryDelaySeconds: defaults.RetryDelaySeconds}).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	for _, provider := range scrapeProviders {
		var config ThirdPartyProviderConfig
		if err := s.db.Where("provider = ?", provider).First(&config).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			if err := s.db.Create(&ThirdPartyProviderConfig{Provider: provider, Enabled: provider != "fanarttv", UpdatedAt: time.Now()}).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	if err := s.migrateLegacyCoverArtNoMatches(); err != nil {
		return err
	}
	return s.migratePausedScrapeProviders()
}

// migratePausedScrapeProviders closes the small race between a stop request
// and an in-flight item finishing. A paused provider must not retain queue
// states that make the UI look active even though the worker will not claim it.
func (s *Service) migratePausedScrapeProviders() error {
	var configs []ThirdPartyProviderConfig
	if err := s.db.Where("paused = ?", true).Find(&configs).Error; err != nil {
		return err
	}
	for _, config := range configs {
		if err := s.db.Model(&ScrapeItem{}).Where("provider = ? AND state IN ?", config.Provider, []string{ScrapePending, ScrapeRetry}).
			Updates(map[string]any{"state": ScrapePaused, "next_retry_at": nil, "lease_until": nil, "last_error": "paused by user", "last_error_code": "paused"}).Error; err != nil {
			return err
		}
	}
	return nil
}

// migrateLegacyCoverArtNoMatches corrects task rows created before Cover Art
// Archive 404 responses were classified as ErrNoCover. These rows describe a
// normal absence of artwork, not a transport failure, and no media data is
// changed by the migration.
func (s *Service) migrateLegacyCoverArtNoMatches() error {
	return s.db.Model(&ScrapeItem{}).
		Where("provider = ? AND state = ? AND last_error LIKE ?", "coverartarchive", ScrapeFailed, "cover art archive returned HTTP 404:%").
		Updates(map[string]any{
			"state": ScrapeNoMatch, "failure_count": 0,
			"last_error": coverartarchive.ErrNoCover.Error(), "last_error_code": "no_match",
			"next_retry_at": nil, "lease_until": nil,
		}).Error
}

func (s *Service) StartScrapeWorker() {
	if s.db == nil {
		return
	}
	s.scrapeOnce.Do(func() { go s.scrapeLoop() })
}

func (s *Service) scrapeLoop() {
	for {
		item, err := s.claimScrapeItem()
		if err != nil {
			log.Printf("station scrape queue claim failed: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if item == nil {
			time.Sleep(2 * time.Second)
			continue
		}
		if err := s.processScrapeItem(*item); err != nil {
			log.Printf("station scrape item %s/%s failed: %v", item.ServerFileID, item.Provider, err)
		}
	}
}

func (s *Service) claimScrapeItem() (*ScrapeItem, error) {
	now := time.Now()
	settings, err := s.scrapeSettings()
	if err != nil {
		return nil, err
	}
	if settings.Paused {
		return nil, nil
	}
	var item ScrapeItem
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&ScrapeItem{}).Where("state = ? AND lease_until IS NOT NULL AND lease_until < ?", ScrapeRunning, now).
			Updates(map[string]any{"state": ScrapeRetry, "next_retry_at": now, "lease_until": nil, "last_error": "worker lease expired after restart"}).Error; err != nil {
			return err
		}
		var configs []ThirdPartyProviderConfig
		if err := tx.Where("enabled = ?", true).Find(&configs).Error; err != nil {
			return err
		}
		enabled := make(map[string]bool, len(configs))
		for _, config := range configs {
			enabled[config.Provider] = !config.Paused
		}
		provider := ""
		for _, candidate := range scrapeProviders {
			if !enabled[candidate] {
				continue
			}
			var activeCount int64
			if err := tx.Model(&ScrapeItem{}).Where("provider = ? AND state IN ?", candidate, []string{ScrapePending, ScrapeRetry, ScrapeRunning}).Count(&activeCount).Error; err != nil {
				return err
			}
			if activeCount > 0 {
				provider = candidate
				break
			}
		}
		if provider == "" {
			return gorm.ErrRecordNotFound
		}
		const candidateBatchSize = 256
		for offset := 0; ; offset += candidateBatchSize {
			var candidates []ScrapeItem
			if err := tx.Where("provider = ?", provider).
				Where("(state = ? OR state = ?) AND (next_retry_at IS NULL OR next_retry_at <= ?) AND failure_count < ?", ScrapePending, ScrapeRetry, now, settings.MaxFailures).
				Where("(lease_until IS NULL OR lease_until < ?)", now).
				Order("created_at asc").Offset(offset).Limit(candidateBatchSize).Find(&candidates).Error; err != nil {
				return err
			}
			if len(candidates) == 0 {
				break
			}
			for _, candidate := range candidates {
				if !s.scrapeProviderReady(candidate.Provider, now) {
					continue
				}
				item = candidate
				lease := now.Add(2 * time.Minute)
				if err := tx.Model(&item).Updates(map[string]any{"state": ScrapeRunning, "started_at": now, "lease_until": lease, "last_error": ""}).Error; err != nil {
					return err
				}
				s.noteScrapeRequest(item.Provider, now)
				return nil
			}
		}
		return gorm.ErrRecordNotFound
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &item, err
}

func (s *Service) scrapeProviderReady(provider string, now time.Time) bool {
	s.scrapeRateMu.Lock()
	defer s.scrapeRateMu.Unlock()
	return !now.Before(s.scrapeProviderNext[provider])
}
func (s *Service) noteScrapeRequest(provider string, now time.Time) {
	interval := 1500 * time.Millisecond
	if provider == "lrclib" {
		interval = 100 * time.Millisecond
	}
	if provider == "theaudiodb" {
		interval = 2 * time.Second
	}
	s.scrapeRateMu.Lock()
	s.scrapeProviderNext[provider] = now.Add(interval)
	s.scrapeRateMu.Unlock()
}
func (s *Service) coolScrapeProvider(provider string, delay time.Duration) {
	s.scrapeRateMu.Lock()
	value := time.Now().Add(delay)
	if value.After(s.scrapeProviderNext[provider]) {
		s.scrapeProviderNext[provider] = value
	}
	s.scrapeRateMu.Unlock()
}

func (s *Service) processScrapeItem(item ScrapeItem) error {
	settings, err := s.scrapeSettings()
	if err != nil {
		s.finishScrapeFailure(&item, err)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(settings.ItemTimeoutSeconds)*time.Second)
	defer cancel()
	var processErr error
	switch item.Provider {
	case "musicbrainz":
		processErr = s.scrapeMusicBrainz(ctx, &item, settings)
	case "coverartarchive":
		processErr = s.scrapeCoverArt(ctx, &item, settings)
	case "fanarttv":
		processErr = s.scrapeFanartTV(ctx, &item, settings)
	case "lrclib":
		processErr = s.scrapeLyrics(ctx, &item, settings)
	case "theaudiodb":
		processErr = s.scrapeTheAudioDB(ctx, &item, settings)
	default:
		processErr = fmt.Errorf("unknown scrape provider %q", item.Provider)
	}
	if processErr == nil {
		return s.finishScrapeSuccess(&item)
	}
	if errors.Is(processErr, musicbrainz.ErrNoMatch) || errors.Is(processErr, coverartarchive.ErrNoCover) || errors.Is(processErr, fanarttv.ErrNoCover) || errors.Is(processErr, lrclib.ErrNoMatch) || errors.Is(processErr, lrclib.ErrInvalidQuery) || errors.Is(processErr, theaudiodb.ErrNoMatch) {
		return s.finishScrapeNoMatch(&item, processErr)
	}
	if errors.Is(processErr, errScrapeDependency) {
		return s.finishScrapeWaiting(&item)
	}
	if errors.Is(processErr, errScrapeProviderDisabled) {
		return s.finishScrapeDeferred(&item)
	}
	s.finishScrapeFailure(&item, processErr)
	if shouldCoolScrapeProvider(processErr) {
		cooldown := 30 * time.Second
		if strings.Contains(strings.ToLower(processErr.Error()), "http 429") {
			cooldown = time.Minute
		}
		s.coolScrapeProvider(item.Provider, cooldown)
	}
	return processErr
}

func shouldCoolScrapeProvider(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"http 429", "http 502", "http 503", "http 504", "timeout", "timed out", "deadline exceeded", "connection refused", "connection reset", "no such host"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (s *Service) scrapeSettings() (ScrapeSettings, error) {
	var row scrapeSettingsRow
	if err := s.db.First(&row, 1).Error; err != nil {
		return ScrapeSettings{}, err
	}
	value := ScrapeSettings{Paused: row.Paused, MaxFailures: row.MaxFailures, RequestTimeoutSeconds: row.RequestTimeoutSeconds, ItemTimeoutSeconds: row.ItemTimeoutSeconds, RetryDelaySeconds: row.RetryDelaySeconds}
	defaults := defaultScrapeSettings()
	if value.MaxFailures <= 0 {
		value.MaxFailures = defaults.MaxFailures
	}
	if value.RequestTimeoutSeconds <= 0 {
		value.RequestTimeoutSeconds = defaults.RequestTimeoutSeconds
	}
	if value.ItemTimeoutSeconds <= 0 {
		value.ItemTimeoutSeconds = defaults.ItemTimeoutSeconds
	}
	if value.RetryDelaySeconds <= 0 {
		value.RetryDelaySeconds = defaults.RetryDelaySeconds
	}
	return value, nil
}

func (s *Service) providerConfig(name string) (ThirdPartyProviderConfig, error) {
	var config ThirdPartyProviderConfig
	err := s.db.Where("provider = ?", name).First(&config).Error
	return config, err
}

func (s *Service) scrapeProviderHTTP(provider string) (*http.Client, string) {
	s.scrapeConfigMu.RLock()
	defer s.scrapeConfigMu.RUnlock()
	return s.scrapeHTTPClient, s.scrapeProviderBaseURL[provider]
}

func (s *Service) scrapeMusicBrainz(ctx context.Context, item *ScrapeItem, settings ScrapeSettings) error {
	config, err := s.providerConfig("musicbrainz")
	if err != nil {
		return err
	}
	if !config.Enabled {
		return errScrapeProviderDisabled
	}
	var file ServerFile
	if err := s.db.Where("server_file_id = ?", item.ServerFileID).First(&file).Error; err != nil {
		return err
	}
	httpClient, baseURL := s.scrapeProviderHTTP("musicbrainz")
	clientOptions := []musicbrainz.Option{musicbrainz.WithTimeout(time.Duration(settings.RequestTimeoutSeconds) * time.Second)}
	if httpClient != nil {
		clientOptions = append(clientOptions, musicbrainz.WithHTTPClient(httpClient))
	}
	if baseURL != "" {
		clientOptions = append(clientOptions, musicbrainz.WithBaseURL(baseURL))
	}
	result, err := musicbrainz.NewClient(clientOptions...).Search(ctx, file.Title, file.Artist, file.Album)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(metadataSnapshot{Title: result.Title, Artist: result.Artist, Album: result.Album})
	added, err := s.appendScrapeResource(item.ServerFileID, "metadata", "json", string(data), "musicbrainz", "")
	if err != nil {
		return err
	}
	item.AddedCount += added
	// MusicBrainz metadata remains a candidate resource: it is not written to
	// the source audio file or automatically made the active file metadata.
	// A MusicBrainz-only run must not enqueue any downstream provider; those
	// providers run only when the user explicitly starts them (or starts the
	// full-library scan).
	item.ExternalID = result.ReleaseID
	item.RelatedExternalID = result.ReleaseGroupID
	if result.ReleaseID != "" {
		_ = s.db.Model(&ScrapeItem{}).Where("server_file_id = ? AND provider = ?", item.ServerFileID, "coverartarchive").
			Update("external_id", result.ReleaseID).Error
	}
	if result.ReleaseGroupID != "" {
		_ = s.db.Model(&ScrapeItem{}).Where("server_file_id = ? AND provider = ?", item.ServerFileID, "fanarttv").
			Update("external_id", result.ReleaseGroupID).Error
	}
	return nil
}

func (s *Service) scrapeCoverArt(ctx context.Context, item *ScrapeItem, settings ScrapeSettings) error {
	config, err := s.providerConfig("coverartarchive")
	if err != nil {
		return err
	}
	if !config.Enabled {
		return errScrapeProviderDisabled
	}
	if strings.TrimSpace(item.ExternalID) == "" {
		var musicbrainzItem ScrapeItem
		if err := s.db.Where("server_file_id = ? AND provider = ?", item.ServerFileID, "musicbrainz").First(&musicbrainzItem).Error; err == nil {
			if strings.TrimSpace(musicbrainzItem.ExternalID) != "" {
				item.ExternalID = musicbrainzItem.ExternalID
				if item.ID != 0 {
					_ = s.db.Model(&ScrapeItem{}).Where("id = ?", item.ID).Update("external_id", item.ExternalID).Error
				}
			} else if musicbrainzItem.State == ScrapePending || musicbrainzItem.State == ScrapeRunning || musicbrainzItem.State == ScrapeRetry {
				return errScrapeDependency
			}
		}
		if strings.TrimSpace(item.ExternalID) == "" {
			return coverartarchive.ErrNoCover
		}
	}
	httpClient, baseURL := s.scrapeProviderHTTP("coverartarchive")
	clientOptions := []coverartarchive.Option{coverartarchive.WithTimeout(time.Duration(settings.RequestTimeoutSeconds) * time.Second)}
	if httpClient != nil {
		clientOptions = append(clientOptions, coverartarchive.WithHTTPClient(httpClient))
	}
	if baseURL != "" {
		clientOptions = append(clientOptions, coverartarchive.WithBaseURL(baseURL))
	}
	data, mimeType, err := coverartarchive.NewClient(clientOptions...).Front(ctx, item.ExternalID)
	if err != nil {
		return err
	}
	added, err := s.appendScrapeCover(item.ServerFileID, data, mimeType, "coverartarchive")
	if err != nil {
		return err
	}
	item.AddedCount += added
	return nil
}

func (s *Service) scrapeFanartTV(ctx context.Context, item *ScrapeItem, settings ScrapeSettings) error {
	config, err := s.providerConfig("fanarttv")
	if err != nil {
		return err
	}
	if !config.Enabled {
		return errScrapeProviderDisabled
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return errors.New("fanart.tv API key is required")
	}
	httpClient, baseURL := s.scrapeProviderHTTP("fanarttv")
	if strings.TrimSpace(item.ExternalID) == "" {
		if err := s.ensureFanartMusicBrainzReleaseGroups(); err != nil {
			return err
		}
		return errScrapeDependency
	}
	options := []fanarttv.Option{fanarttv.WithTimeout(time.Duration(settings.RequestTimeoutSeconds) * time.Second), fanarttv.WithAPIKey(config.APIKey)}
	if httpClient != nil {
		options = append(options, fanarttv.WithHTTPClient(httpClient))
	}
	if baseURL != "" {
		options = append(options, fanarttv.WithBaseURL(baseURL))
	}
	data, mimeType, err := fanarttv.NewClient(options...).AlbumCover(ctx, item.ExternalID)
	if err != nil {
		return err
	}
	added, err := s.appendScrapeCover(item.ServerFileID, data, mimeType, "fanarttv")
	if err != nil {
		return err
	}
	item.AddedCount += added
	return nil
}

func (s *Service) finishScrapeWaiting(item *ScrapeItem) error {
	if s.scrapeProviderPaused(item.Provider) {
		return s.pauseScrapeItem(item)
	}
	waitCount := item.DependencyWaitCount + 1
	now := time.Now()
	if waitCount >= maxScrapeDependencyWaits {
		return s.db.Model(&ScrapeItem{}).Where("id = ?", item.ID).Updates(map[string]any{
			"state": ScrapeNoMatch, "finished_at": now, "lease_until": nil,
			"next_retry_at": nil, "dependency_wait_count": waitCount,
			"last_error":      "musicbrainz release id was not available before dependency wait expired",
			"last_error_code": "dependency_timeout", "added_count": item.AddedCount,
		}).Error
	}
	return s.db.Model(&ScrapeItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"state": ScrapePending, "lease_until": nil,
		"next_retry_at":         now.Add(scrapeDependencyRetryDelay),
		"dependency_wait_count": waitCount,
	}).Error
}

func (s *Service) finishScrapeDeferred(item *ScrapeItem) error {
	return s.db.Model(&ScrapeItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"state": ScrapePending, "lease_until": nil, "next_retry_at": nil,
		"finished_at": nil, "last_error": "", "last_error_code": "",
	}).Error
}

func (s *Service) scrapeLyrics(ctx context.Context, item *ScrapeItem, settings ScrapeSettings) error {
	config, err := s.providerConfig("lrclib")
	if err != nil {
		return err
	}
	if !config.Enabled {
		return errScrapeProviderDisabled
	}
	var file ServerFile
	if err := s.db.Where("server_file_id = ?", item.ServerFileID).First(&file).Error; err != nil {
		return err
	}
	title, artist, album, err := s.lyricsQuery(file)
	if err != nil {
		return err
	}
	if title == "" || artist == "" {
		return lrclib.ErrInvalidQuery
	}
	httpClient, baseURL := s.scrapeProviderHTTP("lrclib")
	clientOptions := []lrclib.Option{lrclib.WithTimeout(time.Duration(settings.RequestTimeoutSeconds) * time.Second)}
	if httpClient != nil {
		clientOptions = append(clientOptions, lrclib.WithHTTPClient(httpClient))
	}
	if baseURL != "" {
		clientOptions = append(clientOptions, lrclib.WithBaseURL(baseURL))
	}
	result, err := lrclib.NewClient(clientOptions...).Get(ctx, title, artist, album)
	if err != nil {
		return err
	}
	if result.SyncedLyrics != "" {
		n, e := s.appendScrapeResource(item.ServerFileID, "lyrics", "lrc", result.SyncedLyrics, "lrclib", "")
		if e != nil {
			return e
		}
		item.AddedCount += n
	}
	if result.PlainLyrics != "" {
		n, e := s.appendScrapeResource(item.ServerFileID, "lyrics", "plain", result.PlainLyrics, "lrclib", "")
		if e != nil {
			return e
		}
		item.AddedCount += n
	}
	return nil
}

func (s *Service) scrapeTheAudioDB(ctx context.Context, item *ScrapeItem, settings ScrapeSettings) error {
	config, err := s.providerConfig("theaudiodb")
	if err != nil {
		return err
	}
	if !config.Enabled {
		return errScrapeProviderDisabled
	}
	var file ServerFile
	if err := s.db.Where("server_file_id = ?", item.ServerFileID).First(&file).Error; err != nil {
		return err
	}
	_, artist, album, err := s.lyricsQuery(file)
	if err != nil {
		return err
	}
	if artist == "" || album == "" {
		return theaudiodb.ErrNoMatch
	}
	httpClient, baseURL := s.scrapeProviderHTTP("theaudiodb")
	options := []theaudiodb.Option{theaudiodb.WithTimeout(time.Duration(settings.RequestTimeoutSeconds) * time.Second), theaudiodb.WithAPIKey(config.APIKey)}
	if httpClient != nil {
		options = append(options, theaudiodb.WithHTTPClient(httpClient))
	}
	if baseURL != "" {
		options = append(options, theaudiodb.WithBaseURL(baseURL))
	}
	result, err := theaudiodb.NewClient(options...).Album(ctx, artist, album)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(metadataSnapshot{Title: file.Title, Artist: result.Artist, Album: result.Album})
	n, err := s.appendScrapeResource(item.ServerFileID, "metadata", "json", string(data), "theaudiodb", "")
	if err != nil {
		return err
	}
	item.AddedCount += n
	if len(result.Cover) > 0 {
		n, err = s.appendScrapeCover(item.ServerFileID, result.Cover, result.MIME, "theaudiodb")
		if err != nil {
			return err
		}
		item.AddedCount += n
	}
	return nil
}

// lyricsQuery uses the file's selected metadata first. When artist data is
// missing, it may use MusicBrainz's scraped candidate only for the lookup.
// Neither path writes to the immutable audio file or changes selected metadata.
func (s *Service) lyricsQuery(file ServerFile) (title, artist, album string, err error) {
	title, artist, album = strings.TrimSpace(file.Title), strings.TrimSpace(file.Artist), strings.TrimSpace(file.Album)
	if title != "" && artist != "" {
		return title, artist, album, nil
	}
	var resource MetadataResource
	err = s.db.Where("server_file_id = ? AND type = ? AND source = ? AND deleted = ?", file.ServerFileID, "metadata", "musicbrainz", false).
		Order("created_at desc").First(&resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return title, artist, album, nil
	}
	if err != nil {
		return "", "", "", err
	}
	var snapshot metadataSnapshot
	if err := json.Unmarshal([]byte(resource.Data), &snapshot); err != nil {
		return "", "", "", fmt.Errorf("decode musicbrainz metadata resource: %w", err)
	}
	if title == "" {
		title = strings.TrimSpace(snapshot.Title)
	}
	if artist == "" {
		artist = strings.TrimSpace(snapshot.Artist)
	}
	if album == "" {
		album = strings.TrimSpace(snapshot.Album)
	}
	return title, artist, album, nil
}

// queueLyricsScrapeIfReady creates (or revives) an LRCLIB task only after a
// usable query is available. It returns whether a task was created or revived.
func (s *Service) queueLyricsScrapeIfReady(file ServerFile, settings ScrapeSettings) (bool, error) {
	var config ThirdPartyProviderConfig
	if err := s.db.Where("provider = ?", "lrclib").First(&config).Error; err != nil {
		return false, err
	}
	if !config.Enabled {
		return false, nil
	}
	title, artist, _, err := s.lyricsQuery(file)
	if err != nil {
		return false, err
	}
	if title == "" || artist == "" {
		return false, nil
	}
	var item ScrapeItem
	err = s.db.Where("server_file_id = ? AND provider = ?", file.ServerFileID, "lrclib").First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, s.db.Create(&ScrapeItem{ServerFileID: file.ServerFileID, Provider: "lrclib", State: ScrapePending, MaxFailures: settings.MaxFailures}).Error
	}
	if err != nil {
		return false, err
	}
	if item.State != ScrapeNoMatch || !strings.Contains(item.LastError, lrclib.ErrInvalidQuery.Error()) {
		return false, nil
	}
	return true, s.db.Model(&item).Updates(map[string]any{
		"state": ScrapePending, "failure_count": 0, "max_failures": settings.MaxFailures,
		"next_retry_at": nil, "last_error": "", "last_error_code": "", "finished_at": nil, "lease_until": nil,
	}).Error
}

func (s *Service) appendScrapeResource(fileID, kind, format, data, source, language string) (int, error) {
	if kind == "lyrics" {
		data = normalizeLyrics(data)
	}
	now := time.Now()
	resource := newTextResource(fileID, kind, format, language, data, false, now)
	resource.Source, resource.LastUseAt = source, nil
	var existing MetadataResource
	err := s.db.Where("server_file_id = ? AND resource_hash = ?", fileID, resource.ResourceHash).First(&existing).Error
	if err == nil {
		return 0, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	if err := s.db.Create(&resource).Error; err != nil {
		return 0, err
	}
	s.broadcastMetadataUpdated(fileID)
	return 1, nil
}

func (s *Service) appendScrapeCover(fileID string, data []byte, mimeType, source string) (int, error) {
	if int64(len(data)) > maxCoverSize {
		return 0, errors.New("scraped cover exceeds size limit")
	}
	now := time.Now()
	resource := newBinaryResource(fileID, "cover", mimeType, data, false, now)
	resource.Source, resource.LastUseAt = source, nil
	resource.StoragePath = filepath.Join(s.storageRoot, "artwork", resource.ResourceHash+map[string]string{"image/png": ".png", "image/jpeg": ".jpg"}[mimeType])
	var existing MetadataResource
	err := s.db.Where("server_file_id = ? AND resource_hash = ?", fileID, resource.ResourceHash).First(&existing).Error
	if err == nil {
		return 0, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	if err := os.WriteFile(resource.StoragePath, data, 0644); err != nil {
		return 0, err
	}
	if err := s.db.Create(&resource).Error; err != nil {
		return 0, err
	}
	s.broadcastMetadataUpdated(fileID)
	return 1, nil
}

func (s *Service) finishScrapeSuccess(item *ScrapeItem) error {
	now := time.Now()
	return s.db.Model(&ScrapeItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"state": ScrapeSuccess, "finished_at": now, "lease_until": nil,
		"external_id": item.ExternalID, "related_external_id": item.RelatedExternalID, "added_count": item.AddedCount,
		"dependency_wait_count": 0, "failure_count": 0, "next_retry_at": nil, "last_error": "", "last_error_code": "",
	}).Error
}
func (s *Service) finishScrapeNoMatch(item *ScrapeItem, err error) error {
	now := time.Now()
	return s.db.Model(&ScrapeItem{}).Where("id = ?", item.ID).Updates(map[string]any{"state": ScrapeNoMatch, "finished_at": now, "lease_until": nil, "failure_count": 0, "next_retry_at": nil, "last_error": err.Error(), "last_error_code": "no_match", "added_count": item.AddedCount}).Error
}
func (s *Service) finishScrapeFailure(item *ScrapeItem, err error) error {
	if s.scrapeProviderPaused(item.Provider) {
		return s.pauseScrapeItem(item)
	}
	item.FailureCount++
	settings, _ := s.scrapeSettings()
	state := ScrapeRetry
	if item.FailureCount >= settings.MaxFailures {
		state = ScrapeFailed
	}
	next := time.Now().Add(scrapeRetryDelay(err, settings))
	if state == ScrapeFailed {
		next = time.Time{}
	}
	log.Printf("station scrape failed file=%s provider=%s attempt=%d state=%s error=%v", item.ServerFileID, item.Provider, item.FailureCount, state, err)
	updates := map[string]any{"state": state, "failure_count": item.FailureCount, "finished_at": time.Now(), "lease_until": nil, "last_error": err.Error(), "last_error_code": "request_failed", "added_count": item.AddedCount}
	if state == ScrapeRetry {
		updates["next_retry_at"] = next
	}
	return s.db.Model(&ScrapeItem{}).Where("id = ?", item.ID).Updates(updates).Error
}

func (s *Service) scrapeProviderPaused(provider string) bool {
	var config ThirdPartyProviderConfig
	return s.db.Where("provider = ?", provider).First(&config).Error == nil && config.Paused
}

func (s *Service) pauseScrapeItem(item *ScrapeItem) error {
	return s.db.Model(&ScrapeItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"state": ScrapePaused, "lease_until": nil, "next_retry_at": nil,
		"last_error": "paused by user", "last_error_code": "paused",
	}).Error
}

// scrapeRetryDelay keeps rate-limit and upstream overload retries conservative,
// while allowing a dropped connection to recover promptly. The latter is common
// for Cover Art Archive's archive.org download nodes and does not indicate that
// the provider is rejecting our request.
func scrapeRetryDelay(err error, settings ScrapeSettings) time.Duration {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "http 429"):
		return time.Minute
	case strings.Contains(message, "http 502"), strings.Contains(message, "http 503"), strings.Contains(message, "http 504"):
		return 30 * time.Second
	case strings.Contains(message, "timeout"), strings.Contains(message, "timed out"), strings.Contains(message, "deadline exceeded"):
		return 30 * time.Second
	case strings.Contains(message, " eof"), strings.HasSuffix(message, "eof"), strings.Contains(message, "connection reset"), strings.Contains(message, "broken pipe"):
		return 5 * time.Second
	default:
		return time.Duration(settings.RetryDelaySeconds) * time.Second
	}
}
