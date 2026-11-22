package theaudiodb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "https://www.theaudiodb.com/api/v1/json"
const defaultAPIKey = "123"

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
func WithAPIKey(value string) Option { return func(c *Client) { c.apiKey = strings.TrimSpace(value) } }
func WithHTTPClient(value *http.Client) Option {
	return func(c *Client) { c.httpClient = value }
}
func WithTimeout(value time.Duration) Option { return func(c *Client) { c.timeout = value } }

func NewClient(opts ...Option) *Client {
	c := &Client{baseURL: defaultBaseURL, apiKey: defaultAPIKey, timeout: 20 * time.Second}
	for _, opt := range opts {
		opt(c)
	}
	if c.apiKey == "" {
		c.apiKey = defaultAPIKey
	}
	return c
}

type Result struct {
	Title  string
	Artist string
	Album  string
	Cover  []byte
	MIME   string
}

var ErrNoMatch = errors.New("theaudiodb album not found")

func (c *Client) Album(ctx context.Context, artist, album string) (Result, error) {
	results, err := c.Albums(ctx, artist, album, 1)
	if err != nil {
		return Result{}, err
	}
	return results[0], nil
}

func (c *Client) Albums(ctx context.Context, artist, album string, limit int) ([]Result, error) {
	artist, album = strings.TrimSpace(artist), strings.TrimSpace(album)
	if album == "" {
		return nil, ErrNoMatch
	}
	query := url.Values{"a": {album}}
	if artist != "" {
		query.Set("s", artist)
	}
	var response struct {
		Albums []struct {
			Title  string `json:"strAlbum"`
			Artist string `json:"strArtist"`
			Cover  string `json:"strAlbumThumb"`
		} `json:"album"`
	}
	if err := c.getJSON(ctx, "/"+c.apiKey+"/searchalbum.php?"+query.Encode(), &response); err != nil {
		return nil, err
	}
	var results []Result
	var coverErr error
	coverCount := 0
	seen := make(map[string]bool)
	for index, candidate := range response.Albums {
		if index >= limit*3 || coverCount >= limit {
			break
		}
		title, credit, artwork := strings.TrimSpace(candidate.Title), strings.TrimSpace(candidate.Artist), strings.TrimSpace(candidate.Cover)
		if title == "" && credit == "" && artwork == "" {
			continue
		}
		result := Result{Title: album, Artist: credit, Album: title}
		if result.Artist == "" {
			result.Artist = artist
		}
		if result.Album == "" {
			result.Album = album
		}
		if artwork != "" && !seen[artwork] {
			seen[artwork] = true
			data, mime, err := c.download(ctx, artwork)
			if err != nil && !errors.Is(err, ErrNoMatch) {
				coverErr = err
			}
			result.Cover, result.MIME = data, mime
		}
		if len(result.Cover) > 0 {
			coverCount++
		}
		if index < limit || len(result.Cover) > 0 {
			results = append(results, result)
		}
	}
	if len(results) == 0 {
		return nil, ErrNoMatch
	}
	return results, coverErr
}

func (c *Client) Check(ctx context.Context) error {
	var response map[string]any
	return c.getJSON(ctx, "/"+c.apiKey+"/search.php?s=__polemo_connection_test__", &response)
}

func (c *Client) getJSON(ctx context.Context, path string, output any) error {
	data, _, err := c.get(ctx, c.baseURL+path, "application/json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("theaudiodb decode response: %w", err)
	}
	return nil
}

func (c *Client) download(ctx context.Context, value string) ([]byte, string, error) {
	data, mimeType, err := c.get(ctx, value, "image/avif,image/webp,image/png,image/jpeg,*/*;q=0.8")
	if err != nil {
		return nil, "", err
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	if mimeType != "image/jpeg" && mimeType != "image/png" {
		return nil, "", fmt.Errorf("theaudiodb cover has unsupported content type %s", mimeType)
	}
	return data, mimeType, nil
}

func (c *Client) get(ctx context.Context, value, accept string) ([]byte, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, value, nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", "Polemo Station/1.0")
	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: c.timeout}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 15<<20))
	if err != nil {
		return nil, "", err
	}
	if response.StatusCode == http.StatusNotFound {
		return nil, "", ErrNoMatch
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := strings.TrimSpace(string(data))
		if len(message) > 500 {
			message = message[:500] + "…"
		}
		return nil, "", fmt.Errorf("theaudiodb returned HTTP %d: %s", response.StatusCode, message)
	}
	return data, strings.Split(response.Header.Get("Content-Type"), ";")[0], nil
}
