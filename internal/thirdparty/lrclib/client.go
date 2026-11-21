package lrclib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wonli/apic/v2"
)

const defaultBaseURL = "https://lrclib.net"

type Client struct {
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
}
type Option func(*Client)

func WithBaseURL(value string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(value, "/") }
}
func WithHTTPClient(value *http.Client) Option { return func(c *Client) { c.httpClient = value } }
func WithTimeout(value time.Duration) Option   { return func(c *Client) { c.timeout = value } }
func NewClient(opts ...Option) *Client {
	c := &Client{baseURL: defaultBaseURL, timeout: 15 * time.Second}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type Result struct {
	ID           int     `json:"id"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

var ErrNoMatch = errors.New("lrclib lyrics not found")
var ErrInvalidQuery = errors.New("lrclib query requires track title and artist")

func (c *Client) Get(ctx context.Context, title, artist, album string) (Result, error) {
	title, artist, album = strings.TrimSpace(title), strings.TrimSpace(artist), strings.TrimSpace(album)
	if title == "" || artist == "" {
		return Result{}, ErrInvalidQuery
	}
	query := url.Values{"track_name": {title}, "artist_name": {artist}}
	if album != "" {
		query.Set("album_name", album)
	}
	var result Result
	if err := c.query(ctx, "/api/get", query, &result); err != nil {
		return Result{}, err
	}
	if result.PlainLyrics == "" && result.SyncedLyrics == "" {
		return Result{}, ErrNoMatch
	}
	return result, nil
}

// Search finds lyric and album information without requiring the artist.
func (c *Client) Search(ctx context.Context, title, artist, album string) ([]Result, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, ErrInvalidQuery
	}
	query := url.Values{"track_name": {title}}
	if artist = strings.TrimSpace(artist); artist != "" {
		query.Set("artist_name", artist)
	}
	if album = strings.TrimSpace(album); album != "" {
		query.Set("album_name", album)
	}
	var results []Result
	if err := c.query(ctx, "/api/search", query, &results); err != nil {
		return nil, err
	}
	return results, nil
}

func (c *Client) query(ctx context.Context, path string, query url.Values, result any) error {
	e := &endpoint{client: c, path: path, query: query}
	id := apic.ApiId{Name: "lrclib" + path, Client: e}
	apiClient := apic.NewApiClient().WithContext(ctx)
	if c.httpClient != nil {
		apiClient.WithHTTPClient(c.httpClient)
	}
	if err := apiClient.Call(&id, &apic.Options{Timeout: c.timeout}); err != nil {
		return err
	}
	if id.Response == nil {
		return errors.New("lrclib empty response")
	}
	if id.Response.HttpStatus == http.StatusNotFound {
		return ErrNoMatch
	}
	if err := json.Unmarshal(id.Response.Data, result); err != nil {
		return fmt.Errorf("lrclib decode response: %w", err)
	}
	return nil
}

// Check verifies that LRCLIB responds to a read-only request. A 404 is a
// healthy response for the deliberately nonexistent probe song.
func (c *Client) Check(ctx context.Context) error {
	_, err := c.Get(ctx, "__polemo_connection_test__", "__polemo_connection_test__", "")
	if errors.Is(err, ErrNoMatch) {
		return nil
	}
	return err
}

type endpoint struct {
	client *Client
	path   string
	query  url.Values
}

func (e *endpoint) Url() string       { return e.client.baseURL + e.path }
func (e *endpoint) Path() string      { return "" }
func (e *endpoint) Query() url.Values { return e.query }
func (e *endpoint) Headers() apic.Params {
	return apic.Params{"Accept": "application/json", "User-Agent": "Polemo Station/1.0 (+https://github.com/musqca/polemo-station)"}
}
func (e *endpoint) PostBody() any                      { return nil }
func (e *endpoint) FormData() any                      { return nil }
func (e *endpoint) WWWFormData() any                   { return nil }
func (e *endpoint) SetData(*apic.SetDataRequest) error { return nil }
func (e *endpoint) Setup() (apic.Api, error)           { return e, nil }
func (e *endpoint) HttpMethod() apic.HttpMethod        { return http.MethodGet }
func (e *endpoint) Debug() bool                        { return false }
func (e *endpoint) UseContext(context.Context) error   { return nil }
func (e *endpoint) OnRequest() error                   { return nil }
func (e *endpoint) OnResponse(data []byte) (*apic.ResponseData, error) {
	return &apic.ResponseData{Data: data}, nil
}
func (e *endpoint) OnEvent(func(string)) {}
func (e *endpoint) ReceiveEvent(string)  {}
func (e *endpoint) OnHttpStatusError(code int, data []byte) error {
	if code == http.StatusNotFound {
		return ErrNoMatch
	}
	return fmt.Errorf("lrclib returned HTTP %d: %s", code, strings.TrimSpace(string(data)))
}
