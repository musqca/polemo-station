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

func TestFrontPrefers1200Thumbnail(t *testing.T) {
	requestedURLs := []string{}
	client := NewClient(WithBaseURL("http://coverart.test"), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requestedURLs = append(requestedURLs, r.URL.Path)
		if r.URL.Path == "/release/rel-1" {
			json := `{"images":[{"image":"http://coverart.test/original.jpg","thumbnails":{"1200":"http://coverart.test/thumb-1200.jpg","500":"http://coverart.test/thumb-500.jpg"},"front":true,"approved":true}]}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(json)), Header: make(http.Header)}, nil
		}
		if r.URL.Path == "/thumb-1200.jpg" {
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
	if len(requestedURLs) != 2 || requestedURLs[1] != "/thumb-1200.jpg" {
		t.Fatalf("expected /thumb-1200.jpg to be requested, got %v", requestedURLs)
	}
}

func TestFrontFallsBackWhenOriginalOversized(t *testing.T) {
	requestedURLs := []string{}
	client := NewClient(WithBaseURL("http://coverart.test"), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requestedURLs = append(requestedURLs, r.URL.Path)
		if r.URL.Path == "/release/rel-oversized" {
			// No 1200 thumbnail, but has large/500
			json := `{"images":[{"image":"http://coverart.test/oversized-original.jpg","thumbnails":{"500":"http://coverart.test/thumb-500.jpg"},"front":true,"approved":true}]}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(json)), Header: make(http.Header)}, nil
		}
		if r.URL.Path == "/oversized-original.jpg" {
			// Simulate content-length exceeding max
			return &http.Response{
				StatusCode:    200,
				ContentLength: maxCoverDataSize + 1024,
				Body:          io.NopCloser(strings.NewReader("too large")),
				Header:        make(http.Header),
			}, nil
		}
		if r.URL.Path == "/thumb-500.jpg" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("\xff\xd8\xff\xd9")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
	})}))
	data, mimeType, err := client.Front(context.Background(), "rel-oversized")
	if err != nil {
		t.Fatalf("expected fallback to succeed, got %v", err)
	}
	if mimeType != "image/jpeg" || len(data) != 4 {
		t.Fatalf("unexpected cover: %s %d", mimeType, len(data))
	}
	if len(requestedURLs) != 3 || requestedURLs[1] != "/oversized-original.jpg" || requestedURLs[2] != "/thumb-500.jpg" {
		t.Fatalf("unexpected request sequence: %v", requestedURLs)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
