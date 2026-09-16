package theaudiodb

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func testResponse(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestAlbumReturnsAlbumMetadataAndCover(t *testing.T) {
	client := NewClient(WithBaseURL("https://theaudiodb.test/api/v1/json"), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/v1/json/123/searchalbum.php":
			if got := r.URL.Query().Get("s"); got != "Artist" {
				t.Fatalf("artist query = %q", got)
			}
			if got := r.URL.Query().Get("a"); got != "Album" {
				t.Fatalf("album query = %q", got)
			}
			return testResponse(http.StatusOK, "application/json", `{"album":[{"strAlbum":"Matched Album","strArtist":"Matched Artist","strAlbumThumb":"https://theaudiodb.test/cover.jpg"}]}`), nil
		case "/cover.jpg":
			if got := r.Header.Get("Accept"); !strings.Contains(got, "image/jpeg") {
				t.Fatalf("cover Accept header = %q", got)
			}
			return testResponse(http.StatusOK, "image/jpeg", "\xff\xd8\xff\xd9"), nil
		default:
			return testResponse(http.StatusNotFound, "text/plain", "not found"), nil
		}
	})}))
	result, err := client.Album(context.Background(), "Artist", "Album")
	if err != nil {
		t.Fatal(err)
	}
	if result.Artist != "Matched Artist" || result.Album != "Matched Album" || result.MIME != "image/jpeg" || len(result.Cover) != 4 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestAlbumWithoutResultIsNoMatch(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, "application/json", `{"album":null}`), nil
	})}
	_, err := NewClient(WithBaseURL("https://theaudiodb.test"), WithHTTPClient(client)).Album(context.Background(), "Artist", "Album")
	if err != ErrNoMatch {
		t.Fatalf("error = %v, want ErrNoMatch", err)
	}
}
