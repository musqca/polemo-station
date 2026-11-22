package fanarttv

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type testTransport func(*http.Request) (*http.Response, error)

func (transport testTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestAlbumCoversSkipBrokenImagesAndKeepAlternatives(t *testing.T) {
	client := &http.Client{Transport: testTransport(func(request *http.Request) (*http.Response, error) {
		status := http.StatusOK
		var body string
		switch request.URL.Path {
		case "/music/albums/group-1":
			if got := request.Header.Get("client-key"); got != "personal-key" {
				t.Errorf("API client-key = %q, want personal-key", got)
			}
			body = `{"albums":[{"release_group_id":"group-1","albumcover":[
				{"url":"https://artwork.test/missing.jpg"},{"url":"https://artwork.test/first.jpg"},
				{"url":"https://artwork.test/second.jpg"}
			]}]}`
		case "/missing.jpg":
			status = http.StatusNotFound
		case "/first.jpg", "/second.jpg":
			body = "\xff\xd8\xff" + request.URL.Path
		default:
			t.Fatalf("unexpected request: %s", request.URL)
		}
		if request.URL.Host == "artwork.test" && request.Header.Get("client-key") != "" {
			t.Error("artwork request contained the API key")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	covers, err := NewClient(WithBaseURL("https://fanart.test"), WithAPIKey("personal-key"), WithHTTPClient(client)).
		AlbumCovers(context.Background(), "group-1", 3)
	if err != nil || len(covers) != 2 || covers[0].MIME != "image/jpeg" || string(covers[1].Data[3:]) != "/second.jpg" {
		t.Fatalf("wanted two usable alternate covers, got %+v, %v", covers, err)
	}
}
