package station

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/musqca/polemo-station/internal/thirdparty/lrclib"
	"github.com/musqca/polemo-station/internal/thirdparty/theaudiodb"
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

func tracksByTitle(ctx context.Context, client *lrclib.Client, title, artist, album string) ([]lrclib.Result, error) {
	results, err := client.Search(ctx, title, artist, album)
	if err != nil {
		return nil, err
	}
	titleKey, artistKey, albumKey := scrapeTitleKey(title), scrapeTitleKey(artist), scrapeTitleKey(album)
	matches := results[:0]
	for _, result := range results {
		if scrapeTitleKey(result.TrackName) == titleKey &&
			(artistKey == "" || scrapeTitleKey(result.ArtistName) == artistKey) &&
			(albumKey == "" || scrapeTitleKey(result.AlbumName) == albumKey) {
			matches = append(matches, result)
		}
	}
	return matches, nil
}

func (s *Service) scrapeLyricsByTitle(ctx context.Context, item *ScrapeItem, client *lrclib.Client, title, album string) error {
	tracks, err := tracksByTitle(ctx, client, title, "", album)
	if err != nil {
		return err
	}
	type lyricsVersion struct{ format, text string }
	versions := make([]lyricsVersion, 0, scrapeTitleResultLimit)
	seen := make(map[string]bool)
	for _, track := range tracks {
		version := lyricsVersion{format: "lrc", text: normalizeLyrics(track.SyncedLyrics)}
		if version.text == "" {
			version = lyricsVersion{format: "plain", text: normalizeLyrics(track.PlainLyrics)}
		}
		if version.text == "" || seen[version.text] {
			continue
		}
		seen[version.text] = true
		versions = append(versions, version)
		if len(versions) == scrapeTitleResultLimit {
			break
		}
	}
	if len(versions) == 0 {
		return lrclib.ErrNoMatch
	}
	// Resources are displayed newest first, so the first result is saved last.
	for i := len(versions) - 1; i >= 0; i-- {
		version := versions[i]
		added, err := s.appendScrapeResource(item.ServerFileID, "lyrics", version.format, version.text, "lrclib", "")
		if err != nil {
			return err
		}
		item.AddedCount += added
	}
	return nil
}

func (s *Service) scrapeCoversByTitle(ctx context.Context, item *ScrapeItem, settings ScrapeSettings, client *theaudiodb.Client, title, artist, album string) error {
	tracks, err := tracksByTitle(ctx, s.newLyricsClient(settings), title, artist, album)
	if err != nil {
		return err
	}
	covers := make([]theaudiodb.Result, 0, scrapeTitleResultLimit)
	seen := make(map[[2]string]bool)
	var lookupErr error
	for _, track := range tracks {
		key := [2]string{scrapeTitleKey(track.ArtistName), scrapeTitleKey(track.AlbumName)}
		if key[0] == "" || key[1] == "" || seen[key] {
			continue
		}
		seen[key] = true
		result, err := s.fetchScrapeAlbum(ctx, client, track.ArtistName, track.AlbumName)
		if err != nil && !errors.Is(err, theaudiodb.ErrNoMatch) {
			lookupErr = err
			break
		}
		if err == nil && len(result.Cover) > 0 && scrapeTitleKey(result.Artist) == key[0] && scrapeTitleKey(result.Album) == key[1] {
			covers = append(covers, result)
		}
		if len(seen) == scrapeTitleResultLimit {
			break
		}
	}
	for i := len(covers) - 1; i >= 0; i-- {
		cover := covers[i]
		added, err := s.appendScrapeCover(item.ServerFileID, cover.Cover, cover.MIME, "theaudiodb")
		if err != nil {
			return err
		}
		item.AddedCount += added
	}
	if lookupErr != nil {
		return lookupErr
	}
	if len(covers) == 0 {
		return theaudiodb.ErrNoMatch
	}
	return nil
}

func (s *Service) fetchScrapeAlbum(ctx context.Context, client *theaudiodb.Client, artist, album string) (theaudiodb.Result, error) {
	const provider = "theaudiodb"
	for {
		if err := ctx.Err(); err != nil {
			return theaudiodb.Result{}, err
		}
		s.scrapeRateMu.Lock()
		now := time.Now()
		wait := s.scrapeProviderNext[provider].Sub(now)
		if wait <= 0 {
			s.scrapeProviderNext[provider] = now.Add(2 * time.Second)
		}
		s.scrapeRateMu.Unlock()
		if wait <= 0 {
			return client.Album(ctx, artist, album)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return theaudiodb.Result{}, ctx.Err()
		case <-timer.C:
		}
	}
}
