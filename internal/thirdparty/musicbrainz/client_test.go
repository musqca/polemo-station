package musicbrainz

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSearchUsesRecordingQuery(t *testing.T) {
	client := NewClient(WithBaseURL("http://musicbrainz.test"), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/recording" || r.URL.Query().Get("query") == "" || r.Header.Get("User-Agent") == "" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"recordings":[{"id":"rec-1","title":"Song","artist-credit":[{"name":"Artist"}],"releases":[{"id":"rel-1","title":"Album"}]}]}`)), Header: make(http.Header)}, nil
	})}))
	result, err := client.Search(context.Background(), "Song", "Artist", "Album")
	if err != nil {
		t.Fatal(err)
	}
	if result.RecordingID != "rec-1" || result.ReleaseID != "rel-1" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestSearchMatchesRequestedRelease(t *testing.T) {
	client := NewClient(WithBaseURL("http://musicbrainz.test"), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"recordings":[{"id":"rec-2","title":"Song","artist-credit":[{"name":"Artist"}],"releases":[{"id":"wrong","title":"Other Album"},{"id":"right","title":"Album"}]}]}`)), Header: make(http.Header)}, nil
	})}))
	result, err := client.Search(context.Background(), "Song", "Artist", "Album")
	if err != nil {
		t.Fatal(err)
	}
	if result.ReleaseID != "right" || result.Album != "Album" {
		t.Fatalf("requested album was not selected: %+v", result)
	}
}

func TestSearchChoosesMatchingRecordingWhenFirstResultLacksRelease(t *testing.T) {
	client := NewClient(WithBaseURL("http://musicbrainz.test"), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"recordings":[{"id":"wrong-recording","title":"Song","artist-credit":[{"name":"Artist"}],"releases":[{"id":"other","title":"Other Album"}]},{"id":"right-recording","title":"Song","artist-credit":[{"name":"Artist"}],"releases":[{"id":"right-release","title":"Album"}]}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}))
	result, err := client.Search(context.Background(), "Song", "Artist", "Album")
	if err != nil {
		t.Fatal(err)
	}
	if result.RecordingID != "right-recording" || result.ReleaseID != "right-release" {
		t.Fatalf("matching recording was not selected: %+v", result)
	}
}

func TestSearchMatchesSongWithoutMatchingAlbum(t *testing.T) {
	requests := 0
	client := NewClient(WithBaseURL("http://musicbrainz.test"), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		body := `{"recordings":[{"id":"rec","title":"Song","artist-credit":[{"name":"Artist"}],"releases":[{"id":"different-release","title":"Original Album"}]}]}`
		if strings.Contains(r.URL.Query().Get("query"), `AND release:"Local Compilation"`) {
			body = `{"recordings":[]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}))
	result, err := client.Search(context.Background(), "Song", "Artist", "Local Compilation")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || result.RecordingID != "rec" || result.Album != "Original Album" || result.ReleaseID != "different-release" {
		t.Fatalf("expected the available release in one request, got %+v in %d requests", result, requests)
	}
}

func TestSearchChecksNormalizedTitlesAndArtistCredits(t *testing.T) {
	const body = `{"recordings":[
		{"id":"wrong-artist","title":"Song: Tonight","artist-credit":[{"name":"Someone Else"}],"releases":[{"id":"other","title":"Album"}]},
		{"id":"wrong-version","title":"Song: Tonight (Live)","artist-credit":[{"name":"Lead","joinphrase":" feat. ","artist":{"name":"Lead Artist"}},{"name":"Guest"}]},
		{"id":"collaboration","title":"Song: Tonight","artist-credit":[{"name":"Lead","joinphrase":" feat. ","artist":{"name":"Lead Artist"}},{"name":"Guest"}],"releases":[{"id":"match","title":"Album"}]}
	]}`
	client := NewClient(WithBaseURL("http://musicbrainz.test"), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}))
	for _, artist := range []string{"Lead & Guest", "Lead Artist"} {
		t.Run(artist, func(t *testing.T) {
			result, err := client.Search(context.Background(), "Song：  Tonight", artist, "Album")
			if err != nil {
				t.Fatal(err)
			}
			if result.RecordingID != "collaboration" || result.Artist != "Lead feat. Guest" || result.ReleaseID != "match" {
				t.Fatalf("unexpected match: %+v", result)
			}
		})
	}
	if result, err := client.Search(context.Background(), "Song: Tonight", "Different Artist", "Album"); err != nil || result.Title != "Song: Tonight" {
		t.Fatalf("missing fallback for a different artist: %+v, %v", result, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
