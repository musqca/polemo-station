package station

import (
	"context"
	"net/http"
	"testing"
)

func TestScrapeLyricsKeepsSearchCandidatesAfterStrictMiss(t *testing.T) {
	s := newTransferTestService(t)
	file := ServerFile{ServerFileID: "lyrics-alternatives", Title: "Same Song", Artist: "Local Artist", Album: "Local Album"}
	if err := s.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&Track{ServerFileID: file.ServerFileID, DurationMs: 183400}).Error; err != nil {
		t.Fatal(err)
	}
	var searched bool
	s.SetScrapeHTTPClient(&http.Client{Transport: scrapeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/get":
			if got := r.URL.Query().Get("duration"); got != "183" {
				t.Errorf("duration = %q, want 183", got)
			}
			return scrapeResponse(http.StatusNotFound, "{}"), nil
		case "/api/search":
			searched = true
			return scrapeResponse(http.StatusOK, `[
				{"trackName":"Same Song","artistName":"Another Artist","duration":200,"syncedLyrics":"[00:02.00]Live"},
				{"trackName":"Same Song","artistName":"Local Artist","duration":183,"syncedLyrics":"[00:01.00]Studio"}
			]`), nil
		default:
			t.Fatalf("unexpected request: %s", r.URL)
			return nil, nil
		}
	})})
	item := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "lrclib"}
	if err := s.scrapeLyrics(context.Background(), &item, defaultScrapeSettings()); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetServerFile(file.ServerFileID)
	if err != nil || !searched || view.LyricsCount != 2 || view.Resources[0].Data != "[00:01.00]Studio" || view.Lyrics != "[00:01.00]Studio" {
		t.Fatalf("wanted two ranked, selectable lyrics, got %+v, %v", view, err)
	}
}

func TestScrapeCoverUsesReleaseGroupWhenReleaseIsMissing(t *testing.T) {
	s := newTransferTestService(t)
	file := ServerFile{ServerFileID: "group-cover"}
	if err := s.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	mb := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "musicbrainz", State: ScrapeSuccess, ExternalID: "edition", RelatedExternalID: "group"}
	if err := s.db.Create(&mb).Error; err != nil {
		t.Fatal(err)
	}
	s.SetScrapeHTTPClient(&http.Client{Transport: scrapeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/release/edition":
			return scrapeResponse(http.StatusNotFound, "{}"), nil
		case "/release-group/group":
			return scrapeResponse(http.StatusOK, `{"images":[{"front":true,"approved":true,"image":"https://covers.test/group.jpg"}]}`), nil
		case "/group.jpg":
			return scrapeResponse(http.StatusOK, "\xff\xd8\xffgroup"), nil
		default:
			t.Fatalf("unexpected request: %s", r.URL)
			return nil, nil
		}
	})})
	item := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "coverartarchive"}
	if err := s.scrapeCoverArt(context.Background(), &item, defaultScrapeSettings()); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetServerFile(file.ServerFileID)
	if err != nil || view.CoverCount != 1 {
		t.Fatalf("wanted group cover, got %+v, %v", view, err)
	}
}

func TestScrapeMusixmatchUsesAuthorizedKeyAndKeepsMultipleSubtitles(t *testing.T) {
	s := newTransferTestService(t)
	file := ServerFile{ServerFileID: "mxm-candidates", Title: "Test Song", Artist: "Artist"}
	if err := s.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	key := "fixture-key"
	if err := s.SaveScrapeProvider("musixmatch", true, &key); err != nil {
		t.Fatal(err)
	}
	s.SetScrapeHTTPClient(&http.Client{Transport: scrapeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("apikey") != key {
			t.Fatal("missing API key")
		}
		switch r.URL.Path {
		case "/ws/1.1/track.search":
			return scrapeResponse(http.StatusOK, `{"message":{"header":{"status_code":200},"body":{"track_list":[
				{"track":{"track_id":4,"track_name":"Test Song","artist_name":"Artist","has_subtitles":0}},
				{"track":{"track_id":5,"track_name":"Test Song","artist_name":"Artist","has_subtitles":0}},
				{"track":{"track_id":6,"track_name":"Test Song","artist_name":"Artist","has_subtitles":0}},
				{"track":{"track_id":7,"track_name":"Test Song","artist_name":"Artist","has_subtitles":0}},
				{"track":{"track_id":8,"track_name":"Test Song","artist_name":"Artist","has_subtitles":0}},
				{"track":{"track_id":9,"track_name":"Test Song","artist_name":"Artist","has_subtitles":0}},
				{"track":{"track_id":10,"track_name":"Test Song","artist_name":"Artist","has_subtitles":0}},
				{"track":{"track_id":1,"track_name":"Test Song","artist_name":"Artist","has_subtitles":1}},
				{"track":{"track_id":2,"track_name":"Test Song","artist_name":"Other","has_subtitles":1}},
				{"track":{"track_id":3,"track_name":"Test Song","artist_name":"Restricted","has_subtitles":1}}
			]}}}`), nil
		case "/ws/1.1/track.subtitle.get":
			body := `{"message":{"header":{"status_code":200},"body":{"subtitle":{"subtitle_language":"en","subtitle_body":"[00:01.00]Studio"}}}}`
			switch r.URL.Query().Get("track_id") {
			case "2":
				body = `{"message":{"header":{"status_code":200},"body":{"subtitle":{"subtitle_language":"en","subtitle_body":"[00:02.00]Live"}}}}`
			case "1":
			case "3":
				body = `{"message":{"header":{"status_code":200},"body":{"subtitle":{"restricted":1,"subtitle_body":"[00:01.00]Restricted"}}}}`
			default:
				t.Errorf("requested subtitles for track without them: %s", r.URL.Query().Get("track_id"))
			}
			return scrapeResponse(http.StatusOK, body), nil
		default:
			t.Fatalf("unexpected request: %s", r.URL)
			return nil, nil
		}
	})})
	if _, count, err := s.StartScrapeProvider("musixmatch"); err != nil || count != 1 {
		t.Fatalf("start: count=%d, err=%v", count, err)
	}
	item, err := s.claimScrapeItem()
	if err != nil || item == nil || item.Provider != "musixmatch" {
		t.Fatalf("claim: item=%+v, err=%v", item, err)
	}
	if err := s.processScrapeItem(*item); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetServerFile(file.ServerFileID)
	if err != nil || view.LyricsCount != 2 || view.Resources[0].Source != "musixmatch" ||
		view.Resources[0].Data != "[00:01.00]Studio" || view.Lyrics != "[00:01.00]Studio" {
		t.Fatalf("wanted two Musixmatch candidates, got %+v, %v", view, err)
	}
}

func TestScrapeRefreshesLegacySuccessOnlyOnce(t *testing.T) {
	s := newTransferTestService(t)
	file := ServerFile{ServerFileID: "legacy-lyrics", Title: "Title", Lyrics: "Selected lyrics"}
	if err := s.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	item := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "lrclib", State: ScrapeSuccess}
	if err := s.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	s.SetScrapeHTTPClient(&http.Client{Transport: scrapeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return scrapeResponse(http.StatusOK, `[{"trackName":"Title","syncedLyrics":"[00:01.00]Other"}]`), nil
	})})
	if _, count, err := s.StartScrapeProvider("lrclib"); err != nil || count != 1 {
		t.Fatalf("legacy successful task should be rescanned, count=%d, err=%v", count, err)
	}
	itemToProcess, err := s.claimScrapeItem()
	if err != nil || itemToProcess == nil {
		t.Fatalf("claim: item=%+v, err=%v", itemToProcess, err)
	}
	if err := s.processScrapeItem(*itemToProcess); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetServerFile(file.ServerFileID)
	if err != nil || view.LyricsCount != 1 || view.Lyrics != "Selected lyrics" {
		t.Fatalf("refresh should preserve the selected lyrics and add a candidate, got %+v, %v", view, err)
	}
	view, err = s.ActivateResource(view.Resources[0].ResourceID)
	if err != nil || view.Lyrics != "[00:01.00]Other" {
		t.Fatalf("activation failed, got %+v, %v", view, err)
	}
	if _, count, err := s.StartScrapeProvider("lrclib"); err != nil || count != 0 {
		t.Fatalf("refreshed task should be skipped, count=%d, err=%v", count, err)
	}
}

func TestSavingMetadataPreservesUnselectedLyricsFallback(t *testing.T) {
	s := newTransferTestService(t)
	file := ServerFile{ServerFileID: "preserve-lyric-fallback", Title: "Original title"}
	if err := s.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	const lyrics = "[00:01.00]Suggested lyrics"
	if _, err := s.appendScrapeResource(file.ServerFileID, "lyrics", "lrc", lyrics, "musixmatch", "en"); err != nil {
		t.Fatal(err)
	}
	view, err := s.UpdateServerFileMetadata(file.ServerFileID, MetadataUpdate{
		Title: "Updated title", Lyrics: lyrics, KeepLyrics: true,
	})
	if err != nil || view.Title != "Updated title" || view.Lyrics != lyrics || view.LyricsCount != 1 {
		t.Fatalf("expected the original fallback after editing tags, got %+v, %v", view, err)
	}
	if err := s.db.Where("server_file_id = ?", file.ServerFileID).First(&file).Error; err != nil || file.Lyrics != "" {
		t.Fatalf("tag edit activated suggested lyrics, got %+v, %v", file, err)
	}
}
