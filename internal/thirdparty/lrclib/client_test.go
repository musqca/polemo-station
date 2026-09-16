package lrclib

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGetRejectsMissingArtistWithoutRequest(t *testing.T) {
	called := false
	client := NewClient(WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected request")
	})}))
	_, err := client.Get(context.Background(), "Song", "", "Album")
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("missing artist error = %v, want ErrInvalidQuery", err)
	}
	if called {
		t.Fatal("missing artist triggered an HTTP request")
	}
}

func TestGetLyrics(t *testing.T) {
	client := NewClient(WithBaseURL("http://lrclib.test"), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/get" || r.URL.Query().Get("track_name") != "Song" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"plainLyrics":"hello","syncedLyrics":"[00:01.00]hello"}`)), Header: make(http.Header)}, nil
	})}))
	result, err := client.Get(context.Background(), "Song", "Artist", "Album")
	if err != nil {
		t.Fatal(err)
	}
	if result.PlainLyrics == "" || result.SyncedLyrics == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
