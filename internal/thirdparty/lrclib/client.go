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
	e := &endpoint{client: c, query: query}
	id := apic.ApiId{Name: "lrclib.get", Client: e}
	apiClient := apic.NewApiClient().WithContext(ctx)
	if c.httpClient != nil {
		apiClient.WithHTTPClient(c.httpClient)
	}
	if err := apiClient.Call(&id, &apic.Options{Timeout: c.timeout}); err != nil {
		return Result{}, err
	}
	if id.Response == nil {
		return Result{}, errors.New("lrclib empty response")
	}
	if id.Response.HttpStatus == http.StatusNotFound {
		return Result{}, ErrNoMatch
	}
	if err := json.Unmarshal(id.Response.Data, &result); err != nil {
		return Result{}, fmt.Errorf("lrclib decode response: %w", err)
	}
	if result.PlainLyrics == "" && result.SyncedLyrics == "" {
		return Result{}, ErrNoMatch
	}
	return result, nil
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
	query  url.Values
}

func (e *endpoint) Url() string       { return e.client.baseURL + "/api/get" }
func (e *endpoint) Path() string      { return "" }
func (e *endpoint) Query() url.Values { return e.query }
func (e *endpoint) Headers() apic.Params {
	return apic.Params{"Accept": "application/json", "User-Agent": "Polemo Station/1.0"}
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
