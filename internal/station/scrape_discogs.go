package station

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/musqca/polemo-station/internal/thirdparty/discogs"
	"gorm.io/gorm"
)

const (
	discogsProvider        = "discogs"
	discogsRequestInterval = 1250 * time.Millisecond
	discogsPreviewTTL      = time.Hour
)

type discogsPreview struct {
	discogs.Result
	FileName  string    `json:"fileName"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func discogsQuery(file ServerFile) discogs.Query {
	query := discogs.Query{
		Title: strings.TrimSpace(file.Title), Artist: strings.TrimSpace(file.Artist),
		Album: strings.TrimSpace(file.Album), AlbumArtist: strings.TrimSpace(file.AlbumArtist),
	}
	stem := strings.TrimSpace(strings.TrimSuffix(file.FileName, filepath.Ext(file.FileName)))
	prefix, suffix, split := strings.Cut(stem, " - ")
	if _, err := strconv.Atoi(prefix); split && err == nil {
		stem = strings.TrimSpace(suffix)
		prefix, suffix, split = strings.Cut(stem, " - ")
	}
	if split && query.Artist == "" && (query.Title == "" || strings.EqualFold(query.Title, suffix)) {
		query.Artist = strings.TrimSpace(prefix)
	}
	if query.Title == "" {
		query.Title = stem
		if split && strings.EqualFold(query.Artist, prefix) {
			query.Title = strings.TrimSpace(suffix)
		}
	}
	return query
}

func (s *Service) newDiscogsClient(config ThirdPartyProviderConfig, timeout time.Duration) *discogs.Client {
	if config.discogsOAuthConnected() {
		return s.newDiscogsOAuthClient(discogs.OAuthApplication{Key: config.OAuthConsumerKey, Secret: config.OAuthConsumerSecret},
			discogs.OAuthToken{Token: config.OAuthAccessToken, Secret: config.OAuthAccessTokenSecret}, timeout)
	}
	httpClient, baseURL := s.discogsHTTP(timeout)
	return discogs.NewClient(config.APIKey, baseURL, httpClient, s.waitDiscogsRequest)
}

func (s *Service) newDiscogsOAuthClient(application discogs.OAuthApplication, token discogs.OAuthToken, timeout time.Duration) *discogs.Client {
	httpClient, baseURL := s.discogsHTTP(timeout)
	return discogs.NewOAuthClient(application, token, baseURL, httpClient, s.waitDiscogsRequest)
}

func (s *Service) discogsHTTP(timeout time.Duration) (*http.Client, string) {
	httpClient, baseURL := s.scrapeProviderHTTP(discogsProvider)
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return httpClient, baseURL
}

func (s *Service) scrapeDiscogs(ctx context.Context, item *ScrapeItem, settings ScrapeSettings) error {
	item.AddedCount = 0
	config, err := s.providerConfig(discogsProvider)
	if err != nil {
		return err
	}
	if !config.Enabled {
		return errScrapeProviderDisabled
	}
	var file ServerFile
	if err := s.db.Where("server_file_id = ? AND deleted_at IS NULL", item.ServerFileID).First(&file).Error; err != nil {
		return err
	}
	query := discogsQuery(file)
	var track Track
	err = s.db.Select("duration_ms").Where("server_file_id = ? AND duration_ms > 0", file.ServerFileID).
		Order("last_seen_at DESC").First(&track).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	query.Duration = time.Duration(track.DurationMs) * time.Millisecond
	result, err := s.newDiscogsClient(config, time.Duration(settings.RequestTimeoutSeconds)*time.Second).Search(ctx, query)
	if err != nil {
		return s.handleDiscogsError(err)
	}
	s.cacheDiscogsMatch(item.ServerFileID, file.FileName, result)
	return nil
}

func (s *Service) cacheDiscogsMatch(fileID, fileName string, result discogs.Result) {
	preview := &discogsPreview{Result: result, FileName: fileName, ExpiresAt: time.Now().Add(discogsPreviewTTL)}
	s.discogsMu.Lock()
	if s.discogsPreviews == nil {
		s.discogsPreviews = make(map[string]*discogsPreview)
	}
	s.discogsPreviews[fileID] = preview
	s.discogsMu.Unlock()
	time.AfterFunc(discogsPreviewTTL, func() {
		s.discogsMu.Lock()
		defer s.discogsMu.Unlock()
		if s.discogsPreviews[fileID] == preview {
			delete(s.discogsPreviews, fileID)
		}
	})
}

func (s *Service) discogsMatch(fileID string) *discogsPreview {
	s.discogsMu.Lock()
	defer s.discogsMu.Unlock()
	if preview := s.discogsPreviews[fileID]; preview != nil && time.Now().Before(preview.ExpiresAt) {
		return preview
	}
	return nil
}

func (s *Service) waitDiscogsRequest(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.discogsMu.Lock()
		now := time.Now()
		wait := s.discogsNextRequest.Sub(now)
		if wait <= 0 {
			s.discogsNextRequest = now.Add(discogsRequestInterval)
		}
		s.discogsMu.Unlock()
		if wait <= 0 {
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func discogsRetryDelay(err error) time.Duration {
	var response *discogs.HTTPError
	if errors.As(err, &response) && response.StatusCode == http.StatusTooManyRequests {
		return max(time.Minute, response.RetryAfter)
	}
	return 0
}

func (s *Service) handleDiscogsError(err error) error {
	if delay := discogsRetryDelay(err); delay > 0 {
		s.discogsMu.Lock()
		until := time.Now().Add(delay)
		if until.After(s.discogsNextRequest) {
			s.discogsNextRequest = until
		}
		s.discogsMu.Unlock()
		s.coolScrapeProvider(discogsProvider, delay)
	}
	return err
}
