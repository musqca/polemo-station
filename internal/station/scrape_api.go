package station

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/musqca/polemo-station/internal/thirdparty/coverartarchive"
	"github.com/musqca/polemo-station/internal/thirdparty/fanarttv"
	"github.com/musqca/polemo-station/internal/thirdparty/lrclib"
	"github.com/musqca/polemo-station/internal/thirdparty/musicbrainz"
	"github.com/musqca/polemo-station/internal/thirdparty/theaudiodb"
)

func (s *Service) ScrapeSettingsView() (ScrapeSettingsView, error) {
	settings, err := s.scrapeSettings()
	if err != nil {
		return ScrapeSettingsView{}, err
	}
	var configs []ThirdPartyProviderConfig
	if err := s.db.Order("provider asc").Find(&configs).Error; err != nil {
		return ScrapeSettingsView{}, err
	}
	providers := make([]ScrapeProviderView, 0, len(configs))
	for _, config := range configs {
		oauthConnected := config.discogsOAuthConnected()
		view := ScrapeProviderView{Provider: config.Provider, Enabled: config.Enabled, Paused: config.Paused,
			Configured: oauthConnected || strings.TrimSpace(config.APIKey) != "", RequiresAPIKey: scrapeProviderRequiresAPIKey(config.Provider), OAuthConnected: oauthConnected}
		var latest ScrapeRun
		if err := s.db.Where("provider = ?", config.Provider).Order("created_at desc").First(&latest).Error; err == nil {
			view.RunID, view.RunTotal, view.SkippedSuccess = latest.RunID, latest.Total, latest.SkippedSuccess
			// A source can require fields that some library tracks do not have
			// (for example, LRCLIB requires both title and artist). Such tracks do
			// not get a queue item, but remain part of the full-library run total.
			// Expose that remainder explicitly so the progress partition always
			// adds up to the displayed total.
			var itemCount int64
			if err := s.db.Model(&ScrapeItem{}).Where("provider = ? AND run_id = ?", config.Provider, latest.RunID).Count(&itemCount).Error; err != nil {
				return ScrapeSettingsView{}, err
			}
			view.SkippedUnready = max(0, latest.Total-latest.SkippedSuccess-int(itemCount))
		}
		providers = append(providers, view)
	}
	return ScrapeSettingsView{Settings: settings, Providers: providers}, nil
}

type ScrapeDailyStat struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	Count    int    `json:"count"`
}

type ScrapeDailyStatSong struct {
	ServerFileID string `json:"serverFileId"`
	FileName     string `json:"fileName"`
	RelativePath string `json:"relativePath"`
	Title        string `json:"title"`
	Artist       string `json:"artist"`
	Album        string `json:"album"`
}

func scrapeTodayStart(now time.Time) time.Time {
	local := now.In(time.Local)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
}

// ScrapeDailyStats counts unique songs whose scraped lyrics or covers were
// created today, grouped by the provider that supplied the resource.
func (s *Service) ScrapeDailyStats() ([]ScrapeDailyStat, error) {
	stats := []ScrapeDailyStat{}
	err := s.db.Table("metadata_resources").
		Select("source AS provider, type AS kind, COUNT(DISTINCT server_file_id) AS count").
		Where("source IN ? AND type IN ? AND deleted = ? AND created_at >= ?", scrapeProviders, []string{"lyrics", "cover"}, false, scrapeTodayStart(time.Now())).
		Group("source, type").Order("source, type").Scan(&stats).Error
	return stats, err
}

func (s *Service) ScrapeDailyStatSongs(provider, kind string) ([]ScrapeDailyStatSong, error) {
	if !isScrapeProvider(provider) || (kind != "lyrics" && kind != "cover") {
		return nil, errors.New("invalid scrape statistic")
	}
	items := []ScrapeDailyStatSong{}
	err := s.db.Table("server_files AS files").
		Select("files.server_file_id, files.file_name, files.relative_path, files.title, files.artist, files.album").
		Joins("JOIN metadata_resources AS resources ON resources.server_file_id = files.server_file_id").
		Where("resources.source = ? AND resources.type = ? AND resources.deleted = ? AND resources.created_at >= ? AND files.deleted_at IS NULL", provider, kind, false, scrapeTodayStart(time.Now())).
		Group("files.server_file_id, files.file_name, files.relative_path, files.title, files.artist, files.album").
		Order("files.title, files.file_name").Scan(&items).Error
	return items, err
}

func (s *Service) SaveScrapeProvider(provider string, enabled bool, apiKey *string) error {
	provider = strings.TrimSpace(provider)
	if !isScrapeProvider(provider) {
		return errors.New("unknown scrape provider")
	}
	if provider == discogsProvider {
		s.discogsOAuth.Lock()
		defer s.discogsOAuth.Unlock()
	}
	var config ThirdPartyProviderConfig
	err := s.db.Where("provider = ?", provider).First(&config).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		config = ThirdPartyProviderConfig{Provider: provider}
	} else if err != nil {
		return err
	}
	config.Enabled = enabled
	if apiKey != nil {
		config.APIKey = strings.TrimSpace(*apiKey)
	}
	if config.Enabled && scrapeProviderRequiresAPIKey(provider) && strings.TrimSpace(config.APIKey) == "" && !config.discogsOAuthConnected() {
		if provider == discogsProvider {
			return errors.New("connect a Discogs account or configure a personal access token before enabling Discogs")
		}
		return errors.New("fanart.tv API key is required before enabling this provider")
	}
	config.UpdatedAt = time.Now()
	// The configured SQL logger may expand bound values on slow queries and errors.
	db := s.db.Session(&gorm.Session{Logger: logger.Discard})
	if config.ID == 0 {
		return db.Create(&config).Error
	}
	updates := map[string]any{"enabled": config.Enabled, "updated_at": config.UpdatedAt}
	if apiKey != nil {
		updates["api_key"] = config.APIKey
	}
	return db.Model(&config).Updates(updates).Error
}

func (s *Service) SaveScrapeSettings(value ScrapeSettings) error {
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
	return s.db.Model(&scrapeSettingsRow{}).Where("id = ?", 1).Updates(map[string]any{"paused": value.Paused, "max_failures": value.MaxFailures, "request_timeout_seconds": value.RequestTimeoutSeconds, "item_timeout_seconds": value.ItemTimeoutSeconds, "retry_delay_seconds": value.RetryDelaySeconds}).Error
}

func isScrapeProvider(value string) bool {
	for _, provider := range scrapeProviders {
		if provider == value {
			return true
		}
	}
	return false
}

func scrapeProviderRequiresAPIKey(value string) bool {
	return value == "fanarttv" || value == discogsProvider
}

func (s *Service) TestScrapeProvider(provider string) error {
	if !isScrapeProvider(provider) {
		return errors.New("unknown scrape provider")
	}
	settings, err := s.scrapeSettings()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(settings.RequestTimeoutSeconds)*time.Second)
	defer cancel()
	timeout := time.Duration(settings.RequestTimeoutSeconds) * time.Second
	httpClient, baseURL := s.scrapeProviderHTTP(provider)
	switch provider {
	case discogsProvider:
		config, err := s.providerConfig(provider)
		if err != nil {
			return err
		}
		if !config.Enabled {
			return errScrapeProviderDisabled
		}
		return s.handleDiscogsError(s.newDiscogsClient(config, timeout).Check(ctx))
	case "musicbrainz":
		options := []musicbrainz.Option{musicbrainz.WithTimeout(timeout)}
		if httpClient != nil {
			options = append(options, musicbrainz.WithHTTPClient(httpClient))
		}
		if baseURL != "" {
			options = append(options, musicbrainz.WithBaseURL(baseURL))
		}
		return musicbrainz.NewClient(options...).Check(ctx)
	case "coverartarchive":
		options := []coverartarchive.Option{coverartarchive.WithTimeout(timeout)}
		if httpClient != nil {
			options = append(options, coverartarchive.WithHTTPClient(httpClient))
		}
		if baseURL != "" {
			options = append(options, coverartarchive.WithBaseURL(baseURL))
		}
		return coverartarchive.NewClient(options...).Check(ctx)
	case "fanarttv":
		var config ThirdPartyProviderConfig
		if err := s.db.Where("provider = ?", provider).First(&config).Error; err != nil {
			return err
		}
		options := []fanarttv.Option{fanarttv.WithTimeout(timeout), fanarttv.WithAPIKey(config.APIKey)}
		if httpClient != nil {
			options = append(options, fanarttv.WithHTTPClient(httpClient))
		}
		if baseURL != "" {
			options = append(options, fanarttv.WithBaseURL(baseURL))
		}
		return fanarttv.NewClient(options...).Check(ctx)
	case "lrclib":
		options := []lrclib.Option{lrclib.WithTimeout(timeout)}
		if httpClient != nil {
			options = append(options, lrclib.WithHTTPClient(httpClient))
		}
		if baseURL != "" {
			options = append(options, lrclib.WithBaseURL(baseURL))
		}
		return lrclib.NewClient(options...).Check(ctx)
	case "theaudiodb":
		options := []theaudiodb.Option{theaudiodb.WithTimeout(timeout)}
		if httpClient != nil {
			options = append(options, theaudiodb.WithHTTPClient(httpClient))
		}
		if baseURL != "" {
			options = append(options, theaudiodb.WithBaseURL(baseURL))
		}
		var config ThirdPartyProviderConfig
		if err := s.db.Where("provider = ?", provider).First(&config).Error; err != nil {
			return err
		}
		options = append(options, theaudiodb.WithAPIKey(config.APIKey))
		return theaudiodb.NewClient(options...).Check(ctx)
	default:
		return errors.New("unknown scrape provider")
	}
}

// HasActiveScrapeItems reports whether a provider already has queue work in
// flight. Starting an active provider must not issue an extra probe request:
// the existing work is a more meaningful connectivity check and avoids a
// false negative while the upstream is rate-limiting us.
func (s *Service) HasActiveScrapeItems(provider string) (bool, error) {
	var count int64
	err := s.db.Model(&ScrapeItem{}).Where("provider = ? AND state IN ?", provider, []string{ScrapePending, ScrapeRetry, ScrapeRunning}).Count(&count).Error
	return count > 0, err
}

func (s *Service) EnqueueScrape(serverFileIDs []string) (int, error) {
	if s.db == nil {
		return 0, errors.New("station database is not initialized")
	}
	var configs []ThirdPartyProviderConfig
	if err := s.db.Where("enabled = ?", true).Find(&configs).Error; err != nil {
		return 0, err
	}
	if len(configs) == 0 {
		return 0, errors.New("no scrape provider enabled")
	}
	settings, err := s.scrapeSettings()
	if err != nil {
		return 0, err
	}
	var files []ServerFile
	query := s.db.Where("deleted_at IS NULL")
	if len(serverFileIDs) > 0 {
		query = query.Where("server_file_id IN ?", serverFileIDs)
	}
	if err := query.Find(&files).Error; err != nil {
		return 0, err
	}
	count := 0
	for _, file := range files {
		for _, config := range configs {
			created, err := s.enqueueScrapeProvider(file, config, settings)
			if err != nil {
				return count, err
			}
			if created {
				count++
			}
		}
	}
	return count, nil
}

func (s *Service) enqueueScrapeProvider(file ServerFile, config ThirdPartyProviderConfig, settings ScrapeSettings) (bool, error) {
	// LRCLIB needs both a title and an artist. Do not leave a terminal no-match
	// record for files that cannot yet form a query; MusicBrainz may later add it.
	if config.Provider == "lrclib" {
		return s.queueLyricsScrapeIfReady(file, settings)
	}
	if config.Provider == discogsProvider {
		query := discogsQuery(file)
		if query.Title == "" || (query.Artist == "" && query.Album == "") {
			return false, nil
		}
	}
	if config.Provider == "theaudiodb" {
		_, artist, album, err := s.lyricsQuery(file)
		if err != nil {
			return false, err
		}
		if artist == "" || album == "" {
			return false, nil
		}
	}
	item := ScrapeItem{ServerFileID: file.ServerFileID, Provider: config.Provider, State: ScrapePending, MaxFailures: settings.MaxFailures}
	if config.Provider == "coverartarchive" {
		var musicbrainzItem ScrapeItem
		if err := s.db.Where("server_file_id = ? AND provider = ?", file.ServerFileID, "musicbrainz").First(&musicbrainzItem).Error; err == nil {
			item.ExternalID = musicbrainzItem.ExternalID
		}
	}
	if config.Provider == "fanarttv" {
		var musicbrainzItem ScrapeItem
		if err := s.db.Where("server_file_id = ? AND provider = ?", file.ServerFileID, "musicbrainz").First(&musicbrainzItem).Error; err == nil {
			item.ExternalID = musicbrainzItem.RelatedExternalID
		}
	}
	err := s.db.Where("server_file_id = ? AND provider = ?", file.ServerFileID, config.Provider).First(&ScrapeItem{}).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, s.db.Create(&item).Error
	}
	return false, err
}

// ReprocessScrapeProvider starts one provider for the full library. It creates
// missing tasks and retries failed/no-match ones, while leaving successful work intact.
func (s *Service) ReprocessScrapeProvider(provider string) (int, error) {
	_, count, err := s.StartScrapeProvider(provider)
	return count, err
}

// StartScrapeProvider creates a distinct run so clients can report progress
// only for work scheduled by this invocation.
func (s *Service) StartScrapeProvider(provider string) (string, int, error) {
	provider = strings.TrimSpace(provider)
	if !isScrapeProvider(provider) {
		return "", 0, errors.New("unknown scrape provider")
	}
	var config ThirdPartyProviderConfig
	if err := s.db.Where("provider = ?", provider).First(&config).Error; err != nil {
		return "", 0, err
	}
	if !config.Enabled {
		return "", 0, errors.New("scrape provider is disabled")
	}
	if config.Paused {
		if err := s.db.Model(&config).Update("paused", false).Error; err != nil {
			return "", 0, err
		}
	}
	// Fanart.tv consumes a MusicBrainz release-group ID. Older successful
	// MusicBrainz rows predate that field, so schedule their enrichment under
	// the MusicBrainz provider before Fanart.tv enters the queue. Fanart.tv
	// itself never performs a hidden MusicBrainz request.
	if provider == "fanarttv" {
		if err := s.ensureFanartMusicBrainzReleaseGroups(); err != nil {
			return "", 0, err
		}
	}
	settings, err := s.scrapeSettings()
	if err != nil {
		return "", 0, err
	}
	runID := newID("scrape-run")
	var files []ServerFile
	if err := s.db.Where("deleted_at IS NULL").Find(&files).Error; err != nil {
		return "", 0, err
	}
	run := ScrapeRun{RunID: runID, Provider: provider, Total: len(files)}
	if err := s.db.Create(&run).Error; err != nil {
		return "", 0, err
	}
	count := 0
	skippedSuccess := 0
	for _, file := range files {
		var item ScrapeItem
		err := s.db.Where("server_file_id = ? AND provider = ?", file.ServerFileID, provider).First(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			created, err := s.enqueueScrapeProvider(file, config, settings)
			if err != nil {
				return "", count, err
			}
			if created {
				if err := s.db.Model(&ScrapeItem{}).Where("server_file_id = ? AND provider = ?", file.ServerFileID, provider).Update("run_id", runID).Error; err != nil {
					return "", count, err
				}
				count++
			}
			continue
		}
		if err != nil {
			return "", count, err
		}
		needsReleaseGroup := provider == "musicbrainz" && item.ExternalID != "" && item.RelatedExternalID == ""
		needsDiscogsPreview := provider == discogsProvider && s.discogsMatch(file.ServerFileID) == nil
		if item.State == ScrapeSuccess && !needsReleaseGroup && !needsDiscogsPreview {
			skippedSuccess++
			continue
		}
		if item.State == ScrapeSuccess {
			if err := s.db.Model(&item).Updates(map[string]any{
				"state": ScrapePending, "failure_count": 0, "next_retry_at": nil,
				"last_error": "", "last_error_code": "", "finished_at": nil, "lease_until": nil, "run_id": runID,
			}).Error; err != nil {
				return "", count, err
			}
			count++
			continue
		}
		if item.State == ScrapePending || item.State == ScrapeRetry || item.State == ScrapeRunning || item.State == ScrapePaused {
			// This work is already in flight (or waiting). Do not duplicate it,
			// but attach it to the newly requested full-library run so progress
			// remains a complete partition of the library.
			updates := map[string]any{"run_id": runID}
			if item.State == ScrapePaused {
				updates["state"] = ScrapePending
				updates["next_retry_at"] = nil
				count++
			}
			if err := s.db.Model(&item).Updates(updates).Error; err != nil {
				return "", count, err
			}
			continue
		}
		if item.State != ScrapeFailed && item.State != ScrapeNoMatch {
			continue
		}
		if err := s.db.Model(&item).Updates(map[string]any{
			"state": ScrapePending, "failure_count": 0, "max_failures": settings.MaxFailures,
			"dependency_wait_count": 0, "next_retry_at": nil, "last_error": "", "last_error_code": "",
			"started_at": nil, "finished_at": nil, "lease_until": nil, "added_count": 0, "run_id": runID,
		}).Error; err != nil {
			return "", count, err
		}
		count++
	}
	if err := s.db.Model(&run).Update("skipped_success", skippedSuccess).Error; err != nil {
		return "", count, err
	}
	return runID, count, nil
}

// ensureFanartMusicBrainzReleaseGroups creates a visible MusicBrainz run for
// legacy successful rows that have a release ID but predate release-group ID
// storage. It deliberately schedules the prerequisite provider rather than
// issuing MusicBrainz HTTP calls from Fanart.tv.
func (s *Service) ensureFanartMusicBrainzReleaseGroups() error {
	var missingReleaseGroups int64
	if err := s.db.Model(&ScrapeItem{}).Where("provider = ? AND state = ? AND external_id <> '' AND related_external_id = ''", "musicbrainz", ScrapeSuccess).Count(&missingReleaseGroups).Error; err != nil {
		return err
	}
	if missingReleaseGroups == 0 {
		return nil
	}
	if _, _, err := s.StartScrapeProvider("musicbrainz"); err != nil {
		return fmt.Errorf("start MusicBrainz release-group enrichment: %w", err)
	}
	return nil
}

// StopScrapeProvider pauses queued and retrying items for one provider. A
// request already in flight is allowed to finish so its result is not lost;
// the worker will not claim another item from this provider afterwards.
func (s *Service) StopScrapeProvider(provider string) (int64, error) {
	if !isScrapeProvider(strings.TrimSpace(provider)) {
		return 0, errors.New("unknown scrape provider")
	}
	if err := s.db.Model(&ThirdPartyProviderConfig{}).Where("provider = ?", provider).Update("paused", true).Error; err != nil {
		return 0, err
	}
	result := s.db.Model(&ScrapeItem{}).
		Where("provider = ? AND state IN ?", provider, []string{ScrapePending, ScrapeRetry}).
		Updates(map[string]any{"state": ScrapePaused, "next_retry_at": nil, "lease_until": nil, "last_error": "paused by user", "last_error_code": "paused"})
	return result.RowsAffected, result.Error
}

func (s *Service) ListScrapeItems() ([]ScrapeItem, error) {
	var items []ScrapeItem
	if err := s.db.Order("state asc, updated_at desc").Find(&items).Error; err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Provider == discogsProvider && items[i].State == ScrapeSuccess {
			items[i].DiscogsMatch = s.discogsMatch(items[i].ServerFileID)
		}
	}
	return items, nil
}

func (s *Service) RetryScrapeItem(id uint) error {
	var item ScrapeItem
	if err := s.db.First(&item, id).Error; err != nil {
		return err
	}
	settings, err := s.scrapeSettings()
	if err != nil {
		return err
	}
	return s.db.Model(&item).Updates(map[string]any{"state": ScrapePending, "failure_count": 0, "max_failures": settings.MaxFailures, "next_retry_at": nil, "last_error": "", "last_error_code": "", "finished_at": nil, "lease_until": nil}).Error
}
