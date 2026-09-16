package coverartarchive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wonli/apic/v2"
)

const defaultBaseURL = "https://coverartarchive.org"

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
	Images []struct {
		Image    string   `json:"image"`
		Types    []string `json:"types"`
		Front    bool     `json:"front"`
		Approved bool     `json:"approved"`
	} `json:"images"`
}

func (c *Client) Front(ctx context.Context, releaseID string) ([]byte, string, error) {
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" {
		return nil, "", errors.New("cover art release id is empty")
	}
	var listing response
	if err := (&endpoint{client: c, path: "/release/" + releaseID}).call(ctx, &listing); err != nil {
		return nil, "", err
	}
	imageURL := ""
	for _, image := range listing.Images {
		if image.Front && image.Approved {
			imageURL = image.Image
			break
		}
	}
	if imageURL == "" && len(listing.Images) > 0 {
		imageURL = listing.Images[0].Image
	}
	if imageURL == "" {
		return nil, "", ErrNoCover
	}
	var data []byte
	if err := (&endpoint{client: c, url: imageURL}).callBytes(ctx, &data); err != nil {
		return nil, "", err
	}
	mime := http.DetectContentType(data)
	if mime != "image/jpeg" && mime != "image/png" {
		return nil, "", fmt.Errorf("cover art has unsupported content type %s", mime)
	}
	return data, mime, nil
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
		clone.Transport = limitedResponseTransport{base: e.client.httpClient.Transport, max: 15 << 20}
		client.WithHTTPClient(&clone)
	} else {
		client.WithHTTPClient(&http.Client{Transport: limitedResponseTransport{max: 15 << 20}})
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
	value, err := io.ReadAll(io.LimitReader(bytes.NewReader(id.Response.Data), 15<<20))
	if err != nil {
		return err
	}
	*out = value
	return nil
}
