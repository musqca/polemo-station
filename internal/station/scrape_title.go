package station

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/musqca/polemo-station/internal/thirdparty"
	"github.com/musqca/polemo-station/internal/thirdparty/lrclib"
	"github.com/musqca/polemo-station/internal/thirdparty/theaudiodb"
	"gorm.io/gorm"
)

const scrapeTitleResultLimit = 3

func (s *Service) newLyricsClient(settings ScrapeSettings) *lrclib.Client {
	httpClient, baseURL := s.scrapeProviderHTTP("lrclib")
	options := []lrclib.Option{lrclib.WithTimeout(time.Duration(settings.RequestTimeoutSeconds) * time.Second)}
	if httpClient != nil {
		options = append(options, lrclib.WithHTTPClient(httpClient))
	}
	if baseURL != "" {
		options = append(options, lrclib.WithBaseURL(baseURL))
	}
	return lrclib.NewClient(options...)
}

func scrapeTitleKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func tracksByTitle(ctx context.Context, client *lrclib.Client, title, artist, album string, duration time.Duration) ([]lrclib.Result, error) {
	// Search by title so different credits and release names can still surface.
	results, err := client.Search(ctx, title, "", "")
	if err != nil {
		return nil, err
	}
	return rankTracksByTitle(results, title, artist, album, duration), nil
}

func rankTracksByTitle(results []lrclib.Result, title, artist, album string, duration time.Duration) []lrclib.Result {
	titleKey, artistKey, albumKey := scrapeTitleKey(title), scrapeTitleKey(artist), scrapeTitleKey(album)
	rank := func(result lrclib.Result) int {
		score := 0
		if scrapeTitleKey(result.TrackName) == titleKey {
			score += 8
		}
		if artistKey != "" && scrapeTitleKey(result.ArtistName) == artistKey {
			score += 4
		}
		if albumKey != "" && scrapeTitleKey(result.AlbumName) == albumKey {
			score += 2
		}
		return score
	}
	matches := make([]lrclib.Result, 0, len(results))
	for _, result := range results {
		if scrapeTitleKey(result.TrackName) == titleKey || (rank(result) >= 4 &&
			strings.Contains(scrapeTitleKey(result.TrackName), titleKey)) {
			matches = append(matches, result)
		}
	}
	if len(matches) == 0 {
		matches = results
	}
	distance := func(result lrclib.Result) float64 {
		if duration <= 0 || result.Duration <= 0 {
			return math.Inf(1)
		}
		return math.Abs(result.Duration - duration.Seconds())
	}
	slices.SortStableFunc(matches, func(a, b lrclib.Result) int {
		if order := cmp.Compare(rank(b), rank(a)); order != 0 {
			return order
		}
		return cmp.Compare(distance(a), distance(b))
	})
	return matches
}

func (s *Service) scrapeTrackDuration(fileID string) (time.Duration, error) {
	var track Track
	err := s.db.Select("duration_ms").Where("server_file_id = ? AND duration_ms > 0", fileID).
		Order("last_seen_at DESC").First(&track).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return time.Duration(track.DurationMs) * time.Millisecond, err
}

type scrapeLyricsVersion struct{ format, text, language string }

func (s *Service) storeScrapeLyrics(item *ScrapeItem, versions []scrapeLyricsVersion) error {
	// Resources are displayed newest first, so save the strongest result last.
	for _, version := range slices.Backward(versions) {
		added, err := s.appendScrapeResource(item.ServerFileID, "lyrics", version.format, version.text, item.Provider, version.language)
		if err != nil {
			return err
		}
		item.AddedCount += added
	}
	return nil
}

func (s *Service) scrapeLRCLIBCandidates(ctx context.Context, item *ScrapeItem, client *lrclib.Client, title, artist, album string, duration time.Duration) error {
	var versions []scrapeLyricsVersion
	seen := make(map[string]bool)
	add := func(format, text string) {
		text = normalizeLyrics(text)
		if text != "" && !seen[text] {
			versions = append(versions, scrapeLyricsVersion{format: format, text: text})
			seen[text] = true
		}
	}
	if artist != "" {
		result, err := client.Get(ctx, title, artist, album, int(duration.Round(time.Second).Seconds()))
		if err != nil && !errors.Is(err, lrclib.ErrNoMatch) {
			return err
		}
		add("lrc", result.SyncedLyrics)
		add("plain", result.PlainLyrics)
	}
	appendTracks := func(tracks []lrclib.Result) {
		for _, track := range rankTracksByTitle(tracks, title, artist, album, duration) {
			if len(versions) >= scrapeTitleResultLimit {
				break
			}
			if normalizeLyrics(track.SyncedLyrics) != "" {
				add("lrc", track.SyncedLyrics)
			} else {
				add("plain", track.PlainLyrics)
			}
		}
	}
	// Try artist and album first, then relax the search when there are too
	// few distinct lyric versions.
	tracks, err := client.Search(ctx, title, artist, album)
	appendTracks(tracks)
	if (artist != "" || album != "") && len(versions) < scrapeTitleResultLimit &&
		(err == nil || errors.Is(err, lrclib.ErrNoMatch)) {
		broader, broadErr := client.Search(ctx, title, "", "")
		appendTracks(broader)
		if broadErr != nil || err != nil {
			err = broadErr
		}
	}
	if len(versions) > 0 {
		return s.storeScrapeLyrics(item, versions)
	}
	if err != nil {
		return err
	}
	return lrclib.ErrNoMatch
}

func (s *Service) scrapeAlbumsByTitle(ctx context.Context, item *ScrapeItem, settings ScrapeSettings, client *theaudiodb.Client, title, artist, album string) error {
	tracks, err := tracksByTitle(ctx, s.newLyricsClient(settings), title, artist, album, 0)
	if errors.Is(err, lrclib.ErrNoMatch) {
		return theaudiodb.ErrNoMatch
	}
	if err != nil {
		return err
	}
	var albums []theaudiodb.Result
	hasCover := false
	seen := make(map[[2]string]bool)
	for _, track := range tracks {
		key := [2]string{scrapeTitleKey(track.ArtistName), scrapeTitleKey(track.AlbumName)}
		if key[0] == "" || key[1] == "" || seen[key] {
			continue
		}
		seen[key] = true
		results, err := s.fetchScrapeAlbums(ctx, client, track.ArtistName, track.AlbumName)
		albums = append(albums, results...)
		hasCover = hasCover || slices.ContainsFunc(results, func(result theaudiodb.Result) bool { return len(result.Cover) > 0 })
		if err != nil && !errors.Is(err, theaudiodb.ErrNoMatch) {
			if len(albums) == 0 {
				return err
			}
			break
		}
		if len(seen) == scrapeTitleResultLimit || (hasCover && len(albums) >= scrapeTitleResultLimit) {
			break
		}
	}
	if len(albums) == 0 {
		return theaudiodb.ErrNoMatch
	}
	return s.storeScrapeAlbums(item, title, albums)
}

func (s *Service) storeScrapeAlbums(item *ScrapeItem, title string, albums []theaudiodb.Result) error {
	var covers []thirdparty.Artwork
	for _, album := range albums {
		if len(album.Cover) > 0 && len(covers) < scrapeTitleResultLimit {
			covers = append(covers, thirdparty.Artwork{Data: album.Cover, MIME: album.MIME})
		}
	}
	for _, result := range slices.Backward(albums[:min(len(albums), scrapeTitleResultLimit)]) {
		data, _ := json.Marshal(metadataSnapshot{Title: title, Artist: result.Artist, Album: result.Album})
		added, err := s.appendScrapeResource(item.ServerFileID, "metadata", "json", string(data), item.Provider, "")
		if err != nil {
			return err
		}
		item.AddedCount += added
	}
	return s.storeScrapeCovers(item, covers)
}

func (s *Service) fetchScrapeAlbums(ctx context.Context, client *theaudiodb.Client, artist, album string) ([]theaudiodb.Result, error) {
	const provider = "theaudiodb"
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.scrapeRateMu.Lock()
		now := time.Now()
		wait := s.scrapeProviderNext[provider].Sub(now)
		if wait <= 0 {
			s.scrapeProviderNext[provider] = now.Add(2 * time.Second)
		}
		s.scrapeRateMu.Unlock()
		if wait <= 0 {
			return client.Albums(ctx, artist, album, scrapeTitleResultLimit)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
