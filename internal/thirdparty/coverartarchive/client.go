package coverartarchive

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/musqca/polemo-station/internal/thirdparty"
	"github.com/wonli/apic/v2"
)

const defaultBaseURL = "https://coverartarchive.org"
const maxCoverDataSize = int64(30 * 1024 * 1024)

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
	c := &Client{baseURL: defaultBaseURL, timeout: 20 * time.Second}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type response struct {
	Images []responseImage `json:"images"`
}

type responseImage struct {
	Image      string            `json:"image"`
	Thumbnails map[string]string `json:"thumbnails"`
	Types      []string          `json:"types"`
	Front      bool              `json:"front"`
	Approved   bool              `json:"approved"`
}

func (c *Client) buildCandidateURLs(img *responseImage) []string {
	var candidates []string
	add := func(raw string) {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return
		}
		for _, u := range candidates {
			if u == trimmed {
				return
			}
		}
		candidates = append(candidates, trimmed)
	}

	if img.Thumbnails != nil {
		add(img.Thumbnails["1200"])
	}
	add(img.Image)
	if img.Thumbnails != nil {
		add(img.Thumbnails["large"])
		add(img.Thumbnails["500"])
		add(img.Thumbnails["small"])
		add(img.Thumbnails["250"])
	}
	return candidates
}

func (c *Client) Front(ctx context.Context, releaseID string) ([]byte, string, error) {
	covers, err := c.Covers(ctx, releaseID, "", 1)
	if err != nil {
		return nil, "", err
	}
	return covers[0].Data, covers[0].MIME, nil
}

func (c *Client) Covers(ctx context.Context, releaseID, groupID string, limit int) ([]thirdparty.Artwork, error) {
	var covers []thirdparty.Artwork
	var lastErr error
	seen := make(map[string]bool)
	for _, target := range []struct{ kind, id string }{{"release", releaseID}, {"release-group", groupID}} {
		id := strings.TrimSpace(target.id)
		if id == "" || len(covers) >= limit {
			continue
		}
		var listing response
		if err := (&endpoint{client: c, path: "/" + target.kind + "/" + url.PathEscape(id)}).call(ctx, &listing); err != nil {
			if !errors.Is(err, ErrNoCover) {
				lastErr = err
			}
			continue
		}
		priority := func(image responseImage) int {
			if image.Front && image.Approved {
				return 2
			}
			if image.Front {
				return 1
			}
			return 0
		}
		slices.SortStableFunc(listing.Images, func(a, b responseImage) int { return cmp.Compare(priority(b), priority(a)) })
		for index, image := range listing.Images {
			// If an edition supplies no working front, keep one usable alternate image.
			if len(covers) >= limit || index >= 6 || (len(covers) > 0 && !image.Front) {
				break
			}
			if image.Image != "" && seen[image.Image] {
				continue
			}
			cover, err := c.downloadArtwork(ctx, &image)
			if err != nil {
				if !errors.Is(err, ErrNoCover) {
					lastErr = err
				}
				continue
			}
			seen[image.Image] = true
			if !slices.ContainsFunc(covers, func(other thirdparty.Artwork) bool { return bytes.Equal(other.Data, cover.Data) }) {
				covers = append(covers, cover)
			}
		}
	}
	if len(covers) > 0 {
		return covers, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrNoCover
}

func (c *Client) downloadArtwork(ctx context.Context, selected *responseImage) (thirdparty.Artwork, error) {
	candidates := c.buildCandidateURLs(selected)
	if len(candidates) == 0 {
		return thirdparty.Artwork{}, ErrNoCover
	}

	var lastErr error
	for _, imageURL := range candidates {
		var data []byte
		if err := (&endpoint{client: c, url: imageURL}).callBytes(ctx, &data); err != nil {
			lastErr = err
			continue
		}
		if int64(len(data)) > maxCoverDataSize {
			lastErr = fmt.Errorf("cover art response exceeds %d bytes", maxCoverDataSize)
			continue
		}
		mime := http.DetectContentType(data)
		if mime != "image/jpeg" && mime != "image/png" {
			lastErr = fmt.Errorf("cover art has unsupported content type %s", mime)
			continue
		}
		return thirdparty.Artwork{Data: data, MIME: mime}, nil
	}
	if lastErr != nil {
		return thirdparty.Artwork{}, lastErr
	}
	return thirdparty.Artwork{}, ErrNoCover
}

// Check performs a read-only request against the service root. It does not
// require a release ID and does not download artwork.
func (c *Client) Check(ctx context.Context) error {
	var data []byte
	return (&endpoint{client: c, url: c.baseURL + "/"}).callBytes(ctx, &data)
}

var ErrNoCover = errors.New("cover art not found")

type endpoint struct {
	client    *Client
	path, url string
}

type limitedResponseTransport struct {
	base http.RoundTripper
	max  int64
}

func (t limitedResponseTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(r)
	if err != nil || response == nil {
		return response, err
	}
	if response.ContentLength > t.max {
		response.Body.Close()
		return nil, fmt.Errorf("cover art response exceeds %d bytes", t.max)
	}
	response.Body = struct {
		io.Reader
		io.Closer
	}{Reader: io.LimitReader(response.Body, t.max+1), Closer: response.Body}
	return response, nil
}

func (e *endpoint) Url() string {
	if e.url != "" {
		return e.url
	}
	return e.client.baseURL + e.path
}
func (e *endpoint) Path() string      { return "" }
func (e *endpoint) Query() url.Values { return nil }
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
	if code == http.StatusNotFound || code == http.StatusNoContent {
		return ErrNoCover
	}
	return fmt.Errorf("cover art archive returned HTTP %d: %s", code, strings.TrimSpace(string(data)))
}
func (e *endpoint) call(ctx context.Context, out any) error {
	var data []byte
	if err := e.callBytes(ctx, &data); err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
func (e *endpoint) callBytes(ctx context.Context, out *[]byte) error {
	id := apic.ApiId{Name: "coverartarchive." + e.path, Client: e}
	client := apic.NewApiClient().WithContext(ctx)
	if e.client.httpClient != nil {
		clone := *e.client.httpClient
		clone.Transport = limitedResponseTransport{base: e.client.httpClient.Transport, max: maxCoverDataSize}
		client.WithHTTPClient(&clone)
	} else {
		client.WithHTTPClient(&http.Client{Transport: limitedResponseTransport{max: maxCoverDataSize}})
	}
	if err := client.Call(&id, &apic.Options{Timeout: e.client.timeout}); err != nil {
		return err
	}
	if id.Response == nil {
		return errors.New("cover art archive empty response")
	}
	if id.Response.HttpStatus == http.StatusNoContent {
		return ErrNoCover
	}
	if int64(len(id.Response.Data)) > maxCoverDataSize {
		return fmt.Errorf("cover art response exceeds %d bytes", maxCoverDataSize)
	}
	value, err := io.ReadAll(bytes.NewReader(id.Response.Data))
	if err != nil {
		return err
	}
	*out = value
	return nil
}
