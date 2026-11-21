package station

import (
	"net/http"
	"strings"
	"testing"
)

func TestTitleOnlyScrapeAddsMultipleCoversAndLyrics(t *testing.T) {
	service := newTransferTestService(t)
	for _, provider := range []string{"musicbrainz", "coverartarchive"} {
		if err := service.SaveScrapeProvider(provider, false, nil); err != nil {
			t.Fatal(err)
		}
	}
	file := ServerFile{ServerFileID: "title-only", Title: "Same Song"}
	if err := service.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.appendScrapeResource(file.ServerFileID, "metadata", "json", `{"title":"Same Song","artist":"Old Guess","album":"Old Guess"}`, "musicbrainz", ""); err != nil {
		t.Fatal(err)
	}
	service.SetScrapeHTTPClient(&http.Client{Transport: scrapeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Host == "lrclib.net" && r.URL.Path == "/api/search":
			if r.URL.Query().Get("track_name") != file.Title || r.URL.Query().Get("artist_name") != "" {
				t.Fatalf("unexpected lyrics search: %s", r.URL)
			}
			return scrapeResponse(http.StatusOK, `[
				{"trackName":"Another Song","artistName":"Discard","albumName":"Discard","plainLyrics":"discard"},
				{"trackName":"Same Song","artistName":"Artist One","albumName":"Album One","syncedLyrics":"[00:01.00]First","plainLyrics":"First"},
				{"trackName":"same song","artistName":"Artist Two","albumName":"Album Two","plainLyrics":"Second"},
				{"trackName":"Same Song","artistName":"Artist Three","albumName":"Album Three","plainLyrics":"Third"},
				{"trackName":"Same Song","artistName":"Artist Four","albumName":"Album Four","plainLyrics":"discard"}
			]`), nil
		case r.URL.Host == "www.theaudiodb.com" && strings.HasSuffix(r.URL.Path, "/searchalbum.php"):
			switch r.URL.Query().Get("a") {
			case "Album One":
				return scrapeResponse(http.StatusOK, `{"album":[{"strAlbum":"Album One","strArtist":"Artist One","strAlbumThumb":"https://covers.test/1"}]}`), nil
			case "Album Two":
				return scrapeResponse(http.StatusOK, `{"album":[{"strAlbum":"Album Two","strArtist":"Artist Two","strAlbumThumb":"https://covers.test/2"}]}`), nil
			case "Album Three":
				return scrapeResponse(http.StatusOK, `{"album":[{"strAlbum":"Album Three","strArtist":"Artist Three","strAlbumThumb":"https://covers.test/3"}]}`), nil
			}
		case r.URL.Host == "covers.test":
			res := scrapeResponse(http.StatusOK, "\xff\xd8\xff"+r.URL.Path)
			res.Header.Set("Content-Type", "image/jpeg")
			return res, nil
		}
		t.Fatalf("unexpected upstream request: %s", r.URL)
		return nil, nil
	})})
	for _, provider := range []string{"lrclib", "theaudiodb"} {
		if _, added, err := service.StartScrapeProvider(provider); err != nil || added != 1 {
			t.Fatalf("start %s with just a title: queued=%d, err=%v", provider, added, err)
		}
	}
	for _, provider := range []string{"lrclib", "theaudiodb"} {
		item, err := service.claimScrapeItem()
		if err != nil || item == nil || item.Provider != provider {
			t.Fatalf("claim %s: item=%+v, err=%v", provider, item, err)
		}
		for range 2 {
			if err := service.processScrapeItem(*item); err != nil {
				t.Fatal(err)
			}
		}
	}
	view, err := service.GetServerFile(file.ServerFileID)
	if err != nil || view.CoverCount != 3 || view.LyricsCount != 3 || view.Artist != "" || view.Album != "" {
		t.Fatalf("wanted three covers and three lyrics with original tags: view=%+v, err=%v", view, err)
	}
}
