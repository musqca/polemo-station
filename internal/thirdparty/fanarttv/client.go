package fanarttv

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

const defaultBaseURL = "https://webservice.fanart.tv/v3.2"

var ErrNoCover = errors.New("fanart.tv album cover not found")

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	timeout    time.Duration
}

type Option func(*Client)

func WithBaseURL(value string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(value, "/") }
}
func WithAPIKey(value string) Option           { return func(c *Client) { c.apiKey = strings.TrimSpace(value) } }
func WithHTTPClient(value *http.Client) Option { return func(c *Client) { c.httpClient = value } }
func WithTimeout(value time.Duration) Option   { return func(c *Client) { c.timeout = value } }

func NewClient(opts ...Option) *Client {
	c := &Client{baseURL: defaultBaseURL, timeout: 20 * time.Second}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// AlbumCover retrieves Fanart.tv artwork for a MusicBrainz release-group ID.
func (c *Client) AlbumCover(ctx context.Context, releaseGroupID string) ([]byte, string, error) {
	if strings.TrimSpace(c.apiKey) == "" {
		return nil, "", errors.New("fanart.tv API key is required")
	}
	if strings.TrimSpace(releaseGroupID) == "" {
		return nil, "", ErrNoCover
	}
	var listing struct {
		Albums []struct {
			ReleaseGroupID string `json:"release_group_id"`
			AlbumCover     []struct {
				URL string `json:"url"`
			} `json:"albumcover"`
		} `json:"albums"`
	}
	if err := (&endpoint{client: c, path: "/music/albums/" + url.PathEscape(releaseGroupID), accept: "application/json"}).call(ctx, &listing); err != nil {
		return nil, "", err
	}
	for _, album := range listing.Albums {
		for _, cover := range album.AlbumCover {
			if strings.TrimSpace(cover.URL) == "" {
				continue
			}
			var data []byte
			if err := (&endpoint{client: c, rawURL: cover.URL, accept: "image/avif,image/webp,image/png,image/jpeg,*/*;q=0.8"}).call(ctx, &data); err != nil {
				return nil, "", err
			}
			mime := http.DetectContentType(data)
			if mime != "image/jpeg" && mime != "image/png" {
				return nil, "", fmt.Errorf("fanart.tv cover has unsupported content type %s", mime)
			}
			return data, mime, nil
		}
	}
	return nil, "", ErrNoCover
}

// Check validates the configured key without downloading artwork.
func (c *Client) Check(ctx context.Context) error {
	_, _, err := c.AlbumCover(ctx, "00000000-0000-0000-0000-000000000000")
	if errors.Is(err, ErrNoCover) {
		return nil
	}
	return err
}

type endpoint struct {
	client       *Client
	path, rawURL string
	accept       string
}

func (e *endpoint) Url() string {
	if e.rawURL != "" {
		return e.rawURL
	}
	return e.client.baseURL + e.path
}
func (e *endpoint) Path() string      { return "" }
func (e *endpoint) Query() url.Values { return nil }
func (e *endpoint) Headers() apic.Params {
	return apic.Params{"Accept": e.accept, "User-Agent": "Polemo Station/1.0", "client-key": e.client.apiKey}
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
	if code == http.StatusNotFound || code == http.StatusNoContent {
		return ErrNoCover
	}
	message := strings.TrimSpace(string(data))
	if len(message) > 500 {
		message = message[:500] + "…"
	}
	return fmt.Errorf("fanart.tv returned HTTP %d: %s", code, message)
}
func (e *endpoint) call(ctx context.Context, out any) error {
	id := apic.ApiId{Name: "fanarttv." + strings.TrimPrefix(e.path, "/"), Client: e}
	client := apic.NewApiClient().WithContext(ctx)
	if e.client.httpClient != nil {
		client.WithHTTPClient(e.client.httpClient)
	}
	if err := client.Call(&id, &apic.Options{Timeout: e.client.timeout}); err != nil {
		return err
	}
	if id.Response == nil {
		return errors.New("fanart.tv empty response")
	}
	if bytes, ok := out.(*[]byte); ok {
		*bytes = id.Response.Data
		return nil
	}
	if err := json.Unmarshal(id.Response.Data, out); err != nil {
		return fmt.Errorf("fanart.tv decode response: %w", err)
	}
	return nil
}
