package station

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDiscogsPreviewRunsWhileMusicBrainzIsCoolingDown(t *testing.T) {
	s := newTransferTestService(t)
	view, err := s.ScrapeSettingsView()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, provider := range view.Providers {
		if provider.Provider == "discogs" {
			found = true
			if provider.Enabled || !provider.RequiresAPIKey {
				t.Fatalf("unexpected Discogs defaults: %+v", provider)
			}
		}
	}
	if !found {
		t.Fatal("Discogs provider missing")
	}
	if err := s.SaveScrapeProvider("discogs", true, nil); err == nil {
		t.Fatal("Discogs was enabled without a token")
	}
	token := "fixture-token"
	if err := s.SaveScrapeProvider("discogs", true, &token); err != nil {
		t.Fatal(err)
	}
	file := ServerFile{ServerFileID: "discogs-fixture", FileName: "Example Artist - Example Song.flac"}
	if err := s.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	unknownFile := ServerFile{ServerFileID: "discogs-unknown", FileName: "track03.flac"}
	if err := s.db.Create(&unknownFile).Error; err != nil {
		t.Fatal(err)
	}
	musicbrainz := ScrapeItem{ServerFileID: file.ServerFileID, Provider: "musicbrainz", State: ScrapePending}
	if err := s.db.Create(&musicbrainz).Error; err != nil {
		t.Fatal(err)
	}
	s.coolScrapeProvider("musicbrainz", time.Minute)
	if _, count, err := s.StartScrapeProvider("discogs"); err != nil || count != 1 {
		t.Fatalf("Discogs run: count=%d, err=%v", count, err)
	}
	item, err := s.claimScrapeItem()
	if err != nil || item == nil || item.Provider != "discogs" {
		t.Fatalf("queued Discogs item did not run: item=%+v, err=%v", item, err)
	}
	s.SetScrapeHTTPClient(&http.Client{Transport: discogsTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Discogs token=fixture-token" {
			t.Fatal("Discogs API request missing token")
		}
		var body string
		switch r.URL.Path {
		case "/database/search":
			body = `{"results":[{"id":42,"type":"release","title":"Example Artist - Example Album","master_id":9}]}`
		case "/releases/42":
			body = `{"id":42,"title":"Example Album","artists":[{"name":"Example Artist"}],"tracklist":[{"type_":"track","position":"1","title":"Example Song","duration":"3:00"}]}`
		default:
			t.Fatalf("unexpected Discogs request path: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
	if err := s.processScrapeItem(*item); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListScrapeItems()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"state":"success"`, `"discogsMatch":`, `"title":"Example Song"`, `"artist":"Example Artist"`, `"releaseUrl":"https://www.discogs.com/release/42"`, `"expiresAt":`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("missing %s in scrape results: %s", want, encoded)
		}
	}
	stationFile, err := s.GetServerFile(file.ServerFileID)
	if err != nil || stationFile.Title != "" || len(stationFile.Resources) != 0 {
		t.Fatalf("Discogs preview changed library metadata: %+v, err=%v", stationFile, err)
	}
	if _, count, err := s.StartScrapeProvider("discogs"); err != nil || count != 0 {
		t.Fatalf("fresh Discogs preview was requeued: count=%d, err=%v", count, err)
	}
	s.discogsMu.Lock()
	s.discogsPreviews[file.ServerFileID].ExpiresAt = time.Now().Add(-time.Minute)
	s.discogsMu.Unlock()
	items, err = s.ListScrapeItems()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.DiscogsMatch != nil {
			t.Fatalf("expired Discogs preview is still visible: %+v", item)
		}
	}
	if _, count, err := s.StartScrapeProvider("discogs"); err != nil || count != 1 {
		t.Fatalf("missing Discogs preview was not requeued: count=%d, err=%v", count, err)
	}
}

func TestDiscogsRateLimitLetsOtherProvidersKeepRunning(t *testing.T) {
	s := newTransferTestService(t)
	token := "fixture-token"
	if err := s.SaveScrapeProvider("discogs", false, &token); err != nil {
		t.Fatal(err)
	}
	requests := 0
	s.SetScrapeHTTPClient(&http.Client{Transport: discogsTestTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusTooManyRequests, Body: http.NoBody, Header: http.Header{"Retry-After": {"120"}}}, nil
	})})
	if err := s.TestScrapeProvider("discogs"); !errors.Is(err, errScrapeProviderDisabled) || requests != 0 {
		t.Fatalf("disabled provider made a request: requests=%d, err=%v", requests, err)
	}
	if err := s.SaveScrapeProvider("discogs", true, nil); err != nil {
		t.Fatal(err)
	}
	err := s.TestScrapeProvider("discogs")
	if err == nil || scrapeRetryDelay(err, defaultScrapeSettings()) != 2*time.Minute {
		t.Fatalf("Discogs rate limit did not respect Retry-After: %v", err)
	}
	for _, item := range []ScrapeItem{{Provider: "discogs", ServerFileID: "d", State: ScrapePending}, {Provider: "musicbrainz", ServerFileID: "m", State: ScrapePending}} {
		if err := s.db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := s.claimScrapeItem()
	if err != nil || claimed == nil || claimed.Provider != "musicbrainz" {
		t.Fatalf("Discogs cooldown blocked the other provider: item=%+v, err=%v", claimed, err)
	}
}

func TestDiscogsBacklogLetsReadyStagedWorkRun(t *testing.T) {
	s := newTransferTestService(t)
	token := "fixture-token"
	if err := s.SaveScrapeProvider("discogs", true, &token); err != nil {
		t.Fatal(err)
	}
	for _, item := range []ScrapeItem{
		{Provider: "discogs", ServerFileID: "first", State: ScrapePending},
		{Provider: "discogs", ServerFileID: "second", State: ScrapePending},
		{Provider: "musicbrainz", ServerFileID: "staged", State: ScrapePending},
	} {
		if err := s.db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := s.claimScrapeItem()
	if err != nil || claimed == nil || claimed.Provider != "discogs" {
		t.Fatalf("first claim: item=%+v, err=%v", claimed, err)
	}
	if err := s.finishScrapeSuccess(claimed); err != nil {
		t.Fatal(err)
	}
	s.scrapeRateMu.Lock()
	s.scrapeProviderNext["discogs"] = time.Now().Add(-time.Second)
	s.scrapeRateMu.Unlock()
	claimed, err = s.claimScrapeItem()
	if err != nil || claimed == nil || claimed.Provider != "musicbrainz" {
		t.Fatalf("Discogs backlog starved the staged queue: item=%+v, err=%v", claimed, err)
	}
}

type discogsTestTransport func(*http.Request) (*http.Response, error)

func (transport discogsTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}
