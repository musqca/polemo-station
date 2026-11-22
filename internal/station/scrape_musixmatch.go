package station

import (
	"cmp"
	"context"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/musqca/polemo-station/internal/thirdparty/musixmatch"
)

const musixmatchProvider = "musixmatch"

var lrcTimestamp = regexp.MustCompile(`(?m)^\s*\[\d{1,3}:\d{2}(?:[.:]\d+)?\]`)

func (s *Service) newMusixmatchClient(config ThirdPartyProviderConfig, settings ScrapeSettings) *musixmatch.Client {
	httpClient, baseURL := s.scrapeProviderHTTP(musixmatchProvider)
	return musixmatch.NewClient(config.APIKey, baseURL, httpClient, time.Duration(settings.RequestTimeoutSeconds)*time.Second)
}

func (s *Service) scrapeMusixmatch(ctx context.Context, item *ScrapeItem, settings ScrapeSettings) error {
	config, err := s.providerConfig(musixmatchProvider)
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
	title, artist, album, err := s.lyricsQuery(file)
	if err != nil {
		return err
	}
	if title == "" {
		return musixmatch.ErrNoMatch
	}
	client := s.newMusixmatchClient(config, settings)
	tracks, err := client.Search(ctx, title, artist)
	if err != nil && !errors.Is(err, musixmatch.ErrNoMatch) {
		return err
	}
	if artist != "" && len(tracks) < scrapeTitleResultLimit {
		broader, broadErr := client.Search(ctx, title, "")
		if broadErr == nil {
			tracks = append(tracks, broader...)
		} else if len(tracks) == 0 {
			return broadErr
		}
	}
	duration, err := s.scrapeTrackDuration(file.ServerFileID)
	if err != nil {
		return err
	}
	rank := func(track musixmatch.Track) int {
		score := 0
		if scrapeTitleKey(track.Title) == scrapeTitleKey(title) {
			score += 8
		}
		if artist != "" && scrapeTitleKey(track.Artist) == scrapeTitleKey(artist) {
			score += 4
		}
		if album != "" && scrapeTitleKey(track.Album) == scrapeTitleKey(album) {
			score += 2
		}
		return score
	}
	distance := func(track musixmatch.Track) time.Duration {
		if track.Duration <= 0 || duration <= 0 {
			return 1<<63 - 1
		}
		return (time.Duration(track.Duration)*time.Second - duration).Abs()
	}
	slices.SortStableFunc(tracks, func(a, b musixmatch.Track) int {
		if order := cmp.Compare(rank(b), rank(a)); order != 0 {
			return order
		}
		if (a.HasSubtitles != nil) != (b.HasSubtitles != nil) {
			if a.HasSubtitles != nil {
				return -1
			}
			return 1
		}
		return cmp.Compare(distance(a), distance(b))
	})
	var versions []scrapeLyricsVersion
	seenTracks, seenLyrics := make(map[int64]bool), make(map[string]bool)
	var lookupErr error
	for _, track := range tracks {
		if len(versions) == scrapeTitleResultLimit || len(seenTracks) >= 2*scrapeTitleResultLimit {
			break
		}
		if seenTracks[track.ID] {
			continue
		}
		seenTracks[track.ID] = true
		subtitle, err := client.Subtitle(ctx, track.ID)
		if errors.Is(err, musixmatch.ErrNoMatch) {
			continue
		}
		if err != nil {
			lookupErr = err
			break
		}
		text := normalizeLyrics(subtitle.Text)
		if lrcTimestamp.MatchString(text) && !seenLyrics[text] {
			versions = append(versions, scrapeLyricsVersion{format: "lrc", text: text, language: subtitle.Language})
			seenLyrics[text] = true
		}
	}
	if len(versions) > 0 {
		return s.storeScrapeLyrics(item, versions)
	}
	if lookupErr != nil {
		return lookupErr
	}
	return musixmatch.ErrNoMatch
}
