package musixmatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const baseURL = "https://api.musixmatch.com/ws/1.1"

var ErrNoMatch = errors.New("musixmatch synchronized lyrics not found")

type Client struct {
	apiKey string
	base   string
	http   http.Client
}

func NewClient(apiKey, endpoint string, httpClient *http.Client, timeout time.Duration) *Client {
	if endpoint == "" {
		endpoint = baseURL
	}
	client := http.Client{Timeout: timeout}
	if httpClient != nil {
		client = *httpClient
		client.Timeout = timeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{apiKey: strings.TrimSpace(apiKey), base: strings.TrimRight(endpoint, "/"), http: client}
}

type Track struct {
	ID           int64  `json:"track_id"`
	Title        string `json:"track_name"`
	Artist       string `json:"artist_name"`
	Album        string `json:"album_name"`
	Duration     int    `json:"track_length"`
	Restricted   int    `json:"restricted"`
	HasSubtitles *int   `json:"has_subtitles"`
}

type Subtitle struct {
	Text       string `json:"subtitle_body"`
	Language   string `json:"subtitle_language"`
	Restricted int    `json:"restricted"`
}

func (c *Client) Search(ctx context.Context, title, artist string) ([]Track, error) {
	var response struct {
		Tracks []struct {
			Track Track `json:"track"`
		} `json:"track_list"`
	}
	query := url.Values{"q_track": {title}, "page_size": {"15"}}
	if strings.TrimSpace(artist) != "" {
		query.Set("q_artist", artist)
	}
	if err := c.get(ctx, "track.search", query, &response); err != nil {
		return nil, err
	}
	var tracks []Track
	for _, entry := range response.Tracks {
		track := entry.Track
		if track.ID > 0 && track.Restricted == 0 && (track.HasSubtitles == nil || *track.HasSubtitles > 0) {
			tracks = append(tracks, track)
		}
	}
	return tracks, nil
}

func (c *Client) Subtitle(ctx context.Context, trackID int64) (Subtitle, error) {
	var response struct {
		Subtitle Subtitle `json:"subtitle"`
	}
	query := url.Values{"track_id": {strconv.FormatInt(trackID, 10)}, "subtitle_format": {"lrc"}}
	if err := c.get(ctx, "track.subtitle.get", query, &response); err != nil {
		return Subtitle{}, err
	}
	if strings.TrimSpace(response.Subtitle.Text) == "" || response.Subtitle.Restricted != 0 {
		return Subtitle{}, ErrNoMatch
	}
	return response.Subtitle, nil
}

func (c *Client) Check(ctx context.Context) error {
	_, err := c.Search(ctx, "__polemo_connection_test__", "")
	if errors.Is(err, ErrNoMatch) {
		return nil
	}
	return err
}

func (c *Client) get(ctx context.Context, method string, values url.Values, result any) error {
	if c.apiKey == "" {
		return errors.New("Musixmatch API key required")
	}
	values.Set("apikey", c.apiKey)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/"+method+"?"+values.Encode(), nil)
	if err != nil {
		return errors.New("musixmatch invalid API request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Polemo Station/1.0")
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The request URL contains the API key. Never return it in job errors.
		if failure, ok := err.(*url.Error); ok && failure.Timeout() {
			return context.DeadlineExceeded
		}
		return errors.New("musixmatch request failed")
	}
	defer response.Body.Close()
	if err := statusError(response.StatusCode); err != nil {
		return err
	}
	var envelope struct {
		Message struct {
			Header struct {
				StatusCode int `json:"status_code"`
			} `json:"header"`
			Body json.RawMessage `json:"body"`
		} `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return errors.New("musixmatch invalid API response")
	}
	if err := statusError(envelope.Message.Header.StatusCode); err != nil {
		return err
	}
	if err := json.Unmarshal(envelope.Message.Body, result); err != nil {
		return errors.New("musixmatch invalid response body")
	}
	return nil
}

func statusError(code int) error {
	switch code {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrNoMatch
	default:
		return fmt.Errorf("musixmatch returned HTTP %d", code)
	}
}
