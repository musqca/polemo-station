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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
