package discogs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
}

func TestCheckAuthenticatesAndUsesOneSearchResult(t *testing.T) {
	waited := false
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !waited {
			t.Fatal("request did not wait")
		}
		if r.URL.Host != "api.discogs.com" || r.URL.Path != "/database/search" ||
			r.URL.Query().Get("type") != "release" || r.URL.Query().Get("per_page") != "1" {
			t.Fatalf("unexpected check request %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Discogs token=private-token" ||
			!strings.Contains(r.UserAgent(), "Polemo") || strings.Contains(r.URL.String(), "private-token") {
			t.Fatal("expected descriptive User-Agent and private authentication header")
		}
		return jsonResponse(`{"pagination":{"items":0},"results":[]}`), nil
	})}
	client := NewClient(" private-token ", "", hc, func(context.Context) error {
		waited = true
		return nil
	})
	if err := client.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSearchUsesTrackCreditAndReturnsCanonicalReleaseLink(t *testing.T) {
	waits, requests := 0, 0
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if waits != requests || r.Header.Get("Authorization") != "Discogs token=private-token" {
			t.Fatal("every request must wait and authenticate")
		}
		switch r.URL.Path {
		case "/database/search":
			if r.URL.Query().Get("track") != "Starman" || r.URL.Query().Get("type") != "release" {
				t.Fatalf("unexpected search query %s", r.URL)
			}
			return jsonResponse(`{"results":[{"type":"release","id":31,"title":"Various - Space Songs","resource_url":"https://other.example/releases/31","uri":"https://other.example/misleading"}]}`), nil
		case "/releases/31":
			return jsonResponse(`{"id":31,"title":"Space Songs","uri":"https://other.example/release","artists":[{"name":"Various"}],"tracklist":[{"type_":"heading","title":"Starman"},{"type_":"track","position":"A1","title":"Starman","duration":"4:14","artists":[{"name":"David Bowie (2)","anv":"David Bowie"}]}]}`), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})}
	client := NewClient("private-token", "https://discogs.test/", hc, func(context.Context) error {
		waits++
		return nil
	})
	got, err := client.Search(context.Background(), Query{Title: "Starman", Artist: "ＤＡＶＩＤ BOWIE", Album: "Space Songs"})
	want := Result{Title: "Starman", Artist: "David Bowie", Album: "Space Songs", ReleaseURL: "https://www.discogs.com/release/31"}
	if err != nil || got != want || requests != 2 {
		t.Fatalf("search = %+v, %v, %d requests, want %+v and two requests", got, err, requests, want)
	}
}

func TestSearchCorroboratesMatchAndChoosesDuration(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/database/search":
			return jsonResponse(`{"results":[{"id":1},{"id":2},{"id":3},{"id":4}]}`), nil
		case "/releases/1":
			return jsonResponse(`{"title":"Same Album","artists":[{"name":"Other Artist"}],"tracklist":[{"title":"Same Song","type_":"track","duration":"3:21"}]}`), nil
		case "/releases/2":
			return jsonResponse(`{"title":"Same Album","artists":[{"name":"The Band"}],"tracklist":[{"title":"Same Song","type_":"track","duration":"6:20"}]}`), nil
		case "/releases/3":
			return jsonResponse(`{"title":"The Single","artists":[{"name":"The Band (2)","anv":"The Band!"}],"tracklist":[{"title":"Same Song","type_":"track","duration":"3:20"}]}`), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})}
	client := NewClient("private-token", "https://discogs.test", hc, nil)
	got, err := client.Search(context.Background(), Query{
		Title: "Same Song", Artist: "The Band", Album: "Same Album", Duration: 201 * time.Second,
	})
	want := Result{Title: "Same Song", Artist: "The Band!", ReleaseURL: "https://www.discogs.com/release/3"}
	if err != nil || got != want {
		t.Fatalf("search = %+v, %v, want %+v", got, err, want)
	}
}

func TestSearchOmitsAlbumWhenMultipleReleaseTitlesFit(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/database/search":
			return jsonResponse(`{"results":[{"id":1},{"id":2}]}`), nil
		case "/releases/1":
			return jsonResponse(`{"title":"Single","artists":[{"name":"Someone"}],"tracklist":[{"title":"Song","type_":"track","duration":""}]}`), nil
		case "/releases/2":
			return jsonResponse(`{"title":"Album","artists":[{"name":"Someone"}],"tracklist":[{"title":"Song","type_":"track","duration":"3:49"}]}`), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})}
	client := NewClient("private-token", "https://discogs.test", hc, nil)
	got, err := client.Search(context.Background(), Query{Title: "Song", Artist: "Someone", Duration: 230 * time.Second})
	want := Result{Title: "Song", Artist: "Someone", ReleaseURL: "https://www.discogs.com/release/2"}
	if err != nil || got != want {
		t.Fatalf("search = %+v, %v, want %+v", got, err, want)
	}
}

func TestSearchUsesAlbumAndAlbumArtistWhenSongArtistMissing(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/database/search":
			return jsonResponse(`{"results":[{"id":1},{"id":2}]}`), nil
		case "/releases/1":
			return jsonResponse(`{"title":"Original","artists":[{"name":"Wrong Band"}],"tracklist":[{"title":"Song","type_":"track"}]}`), nil
		case "/releases/2":
			return jsonResponse(`{"title":"Original","artists":[{"name":"Right Band (17)"}],"tracklist":[{"title":"Song","type_":"track"}]}`), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})}
	client := NewClient("private-token", "https://discogs.test", hc, nil)
	got, err := client.Search(context.Background(), Query{Title: "Song", Album: "Original", AlbumArtist: "Right Band"})
	want := Result{Title: "Song", Artist: "Right Band", Album: "Original", ReleaseURL: "https://www.discogs.com/release/2"}
	if err != nil || got != want {
		t.Fatalf("search = %+v, %v, want %+v", got, err, want)
	}
}

func TestSearchReturnsNoMatchWithoutCorroboratingInput(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("insufficient query sent to Discogs: %s", r.URL)
		return nil, nil
	})}
	client := NewClient("private-token", "https://discogs.test", hc, nil)
	for _, query := range []Query{{Title: "Song", AlbumArtist: "Someone"}, {Artist: "Someone", Album: "Album"}} {
		if _, err := client.Search(context.Background(), query); !errors.Is(err, ErrNoMatch) {
			t.Fatalf("search error = %v, want ErrNoMatch", err)
		}
	}
}

func TestCheckReportsHTTP429WithRetryAfter(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"17"}},
			Body:       io.NopCloser(strings.NewReader(`{"message":"private-token"}`)),
		}, nil
	})}
	err := NewClient("private-token", "https://discogs.test", hc, nil).Check(context.Background())
	var status *HTTPError
	if !errors.As(err, &status) || status.StatusCode != 429 || status.RetryAfter != 17*time.Second {
		t.Fatalf("check error = %#v, want 429 with retry after 17s", err)
	}
	if !strings.Contains(err.Error(), "HTTP 429") || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("unsafe or unrecognizable error: %v", err)
	}
}

func TestCheckDoesNotFollowRedirect(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/database/search" {
			t.Fatalf("followed redirect: %s", r.URL)
		}
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://discogs.test/redirect?token=private-token"}},
			Body:       http.NoBody,
		}, nil
	})}
	err := NewClient("private-token", "https://discogs.test", hc, nil).Check(context.Background())
	var status *HTTPError
	if !errors.As(err, &status) || status.StatusCode != 302 || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("check error = %v, want sanitized redirect status", err)
	}
}

func TestCheckPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("interrupted wait must prevent the request")
		return nil, nil
	})}
	client := NewClient("private-token", "https://discogs.test", hc, func(ctx context.Context) error {
		return ctx.Err()
	})
	if err := client.Check(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("check error = %v, want context.Canceled", err)
	}
}

func TestSearchDeclinesAmbiguousArtistsWithTheSameName(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/database/search":
			return jsonResponse(`{"results":[{"id":11},{"id":22}]}`), nil
		case "/releases/11":
			return jsonResponse(`{"title":"One Album","artists":[{"id":10,"name":"John Doe"}],"tracklist":[{"title":"Common Song","type_":"track"}]}`), nil
		case "/releases/22":
			return jsonResponse(`{"title":"Another Album","artists":[{"id":20,"name":"John Doe (2)"}],"tracklist":[{"title":"Common Song","type_":"track"}]}`), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})}
	client := NewClient("private-token", "https://discogs.test", hc, nil)
	input := Query{Title: "Common Song", Artist: "John Doe"}
	if got, err := client.Search(context.Background(), input); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("search = %+v, %v, want ErrNoMatch for two different artists", got, err)
	}
	input.Album = "Another Album"
	got, err := client.Search(context.Background(), input)
	want := Result{Title: "Common Song", Artist: "John Doe", Album: "Another Album", ReleaseURL: "https://www.discogs.com/release/22"}
	if err != nil || got != want {
		t.Fatalf("search with corroborating album = %+v, %v, want %+v", got, err, want)
	}
}

func TestCheckKeepsTokenOutOfWaitErrors(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("a failed wait must prevent the request")
		return nil, nil
	})}
	client := NewClient("private-token", "https://discogs.test", hc, func(context.Context) error {
		return errors.New("wait failed for private-token")
	})
	if err := client.Check(context.Background()); err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("expected sanitized wait error, got %v", err)
	}
}

func TestSearchPrioritizesTaggedAlbumForCommonTrackTitle(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/database/search":
			query := r.URL.Query()
			if query.Get("track") != "Home" || query.Get("release_title") != "Rare Album" || query.Get("artist") != "" {
				t.Fatalf("expected a track and album search, got %s", r.URL)
			}
			return jsonResponse(`{"results":[{"id":42,"title":"Various - Rare Album"}]}`), nil
		case "/releases/42":
			return jsonResponse(`{"id":42,"title":"Rare Album","artists":[{"name":"Various"}],"tracklist":[{"title":"Home","type_":"track","artists":[{"name":"Noam"}]}]}`), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})}
	client := NewClient("private-token", "https://discogs.test", hc, nil)
	got, err := client.Search(context.Background(), Query{Title: "Home", Artist: "Noam", Album: "Rare Album"})
	want := Result{Title: "Home", Artist: "Noam", Album: "Rare Album", ReleaseURL: "https://www.discogs.com/release/42"}
	if err != nil || got != want {
		t.Fatalf("search = %+v, %v, want %+v", got, err, want)
	}
}

func TestSearchFallsBackToCompilationTrackCredit(t *testing.T) {
	var filters []string
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/database/search":
			query := r.URL.Query()
			if query.Get("track") != "Home" {
				t.Fatalf("expected track query, got %s", r.URL)
			}
			filters = append(filters, query.Get("release_title")+"|"+query.Get("artist"))
			switch filters[len(filters)-1] {
			case "Unlisted Album|":
				return jsonResponse(`{"results":[]}`), nil
			case "|Noam":
				return jsonResponse(`{"results":[{"id":41,"title":"Noam - The Mixes"}]}`), nil
			case "|":
				return jsonResponse(`{"results":[{"id":41},{"id":42,"title":"Various - Where We Live"}]}`), nil
			default:
				t.Fatalf("unexpected search filter %s", r.URL)
				return nil, nil
			}
		case "/releases/41":
			return jsonResponse(`{"id":41,"title":"The Mixes","artists":[{"name":"Noam"}],"tracklist":[{"title":"Home (Live)","type_":"track"}]}`), nil
		case "/releases/42":
			return jsonResponse(`{"id":42,"title":"Where We Live","artists":[{"name":"Various"}],"tracklist":[{"title":"Home","type_":"track","artists":[{"name":"Noam"}]}]}`), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})}
	client := NewClient("private-token", "https://discogs.test", hc, nil)
	got, err := client.Search(context.Background(), Query{Title: "Home", Artist: "Noam", Album: "Unlisted Album"})
	want := Result{Title: "Home", Artist: "Noam", ReleaseURL: "https://www.discogs.com/release/42"}
	if err != nil || got != want {
		t.Fatalf("search = %+v, %v, want %+v", got, err, want)
	}
	if got := strings.Join(filters, ","); got != "Unlisted Album|,|Noam,|" {
		t.Fatalf("search filters = %s", got)
	}
}
