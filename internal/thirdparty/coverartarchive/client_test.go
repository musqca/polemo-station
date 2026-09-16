package coverartarchive

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFrontTreatsNotFoundAsNoCover(t *testing.T) {
	client := NewClient(WithBaseURL("http://coverart.test"), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("No cover art found")), Header: make(http.Header)}, nil
	})}))
	_, _, err := client.Front(context.Background(), "release-without-cover")
	if !errors.Is(err, ErrNoCover) {
		t.Fatalf("404 error = %v, want ErrNoCover", err)
	}
}

func TestFrontDownloadsApprovedImage(t *testing.T) {
	client := NewClient(WithBaseURL("http://coverart.test"), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/release/rel-1" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"images":[{"image":"http://coverart.test/cover.jpg","front":true,"approved":true}]}`)), Header: make(http.Header)}, nil
		}
		if r.URL.Path == "/cover.jpg" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("\xff\xd8\xff\xd9")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
	})}))
	data, mimeType, err := client.Front(context.Background(), "rel-1")
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "image/jpeg" || len(data) != 4 {
		t.Fatalf("unexpected cover: %s %d", mimeType, len(data))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
