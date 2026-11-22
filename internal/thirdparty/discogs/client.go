// Package discogs recognizes tracks by matching Discogs release tracklists.
package discogs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIHost = "api.discogs.com"
	defaultBaseURL = "https://" + defaultAPIHost
	userAgent      = "Polemo Station/1.0 (+https://github.com/musqca/polemo-station)"
	maxReleases    = 3
)

var ErrNoMatch = errors.New("discogs no match")

type Client struct {
	token      string
	oauth      *OAuthApplication
	oauthToken OAuthToken
	baseURL    *url.URL
	httpClient http.Client
	wait       func(context.Context) error
}

func NewClient(token, baseURL string, httpClient *http.Client, wait func(context.Context) error) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") ||
		base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		base = nil
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	safeClient := *httpClient
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{token: strings.TrimSpace(token), baseURL: base, httpClient: safeClient, wait: wait}
}

type Query struct {
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Duration    time.Duration
}

type Result struct {
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	ReleaseURL string `json:"releaseUrl"`
}

type HTTPError struct {
	StatusCode int
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("discogs HTTP %d", e.StatusCode)
}

type searchResult struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type searchResponse struct {
	Results []searchResult `json:"results"`
}

func (c *Client) Check(ctx context.Context) error {
	var search searchResponse
	return c.get(ctx, "database/search", url.Values{
		"type": {"release"}, "per_page": {"1"}, "q": {"__polemo_connection_test__"},
	}, &search)
}

func (c *Client) Search(ctx context.Context, input Query) (Result, error) {
	matches, err := c.search(ctx, input, false, 1)
	if err != nil {
		return Result{}, err
	}
	return normalizedQuery(input).bestMatch(matches)
}

func (c *Client) Candidates(ctx context.Context, input Query, limit int) ([]Result, error) {
	matches, err := c.search(ctx, input, true, limit)
	if err != nil {
		return nil, err
	}
	sortMatches(matches)
	var candidates []Result
	seen := make(map[string]bool)
	for _, match := range matches {
		if !seen[match.ReleaseURL] {
			candidates = append(candidates, match.Result)
			seen[match.ReleaseURL] = true
		}
		if len(candidates) == limit {
			break
		}
	}
	if len(candidates) == 0 {
		return nil, ErrNoMatch
	}
	return candidates, nil
}

func (c *Client) search(ctx context.Context, input Query, alternatives bool, candidateLimit int) ([]trackMatch, error) {
	query := normalizedQuery(input)
	if query.Title == "" || (!alternatives && query.Artist == "" && query.Album == "") {
		return nil, ErrNoMatch
	}
	var filters []url.Values
	if query.Album != "" {
		filters = append(filters, url.Values{"release_title": {strings.TrimSpace(input.Album)}})
	}
	if query.Artist != "" {
		filters = append(filters, url.Values{"artist": {strings.TrimSpace(input.Artist)}})
	}
	filters = append(filters, url.Values{})

	seen := make(map[int64]struct{}, maxReleases)
	var matches []trackMatch
	for i, filter := range filters {
		filter.Set("type", "release")
		filter.Set("per_page", "25")
		filter.Set("track", strings.TrimSpace(input.Title))
		var search searchResponse
		if err := c.get(ctx, "database/search", filter, &search); err != nil {
			if alternatives && len(matches) > 0 {
				return matches, nil
			}
			return nil, err
		}
		// Reserve one detail request for every remaining search.
		limit := maxReleases - (len(filters) - i - 1)
		found, err := c.inspectReleases(ctx, query, search.Results, seen, limit, alternatives)
		matches = append(matches, found...)
		if err != nil {
			if alternatives && len(matches) > 0 {
				return matches, nil
			}
			return nil, err
		}
		if alternatives {
			releases := make(map[string]bool)
			for _, match := range matches {
				releases[match.ReleaseURL] = true
			}
			if len(releases) >= candidateLimit {
				break
			}
		} else if best, err := query.bestMatch(matches); err == nil && (query.Album == "" || best.Album != "") {
			break
		}
	}
	return matches, nil
}

func (c *Client) inspectReleases(ctx context.Context, query matchQuery, results []searchResult, seen map[int64]struct{}, limit int, alternatives bool) ([]trackMatch, error) {
	slices.SortStableFunc(results, func(a, b searchResult) int {
		return query.releasePriority(b.Title) - query.releasePriority(a.Title)
	})
	var matches []trackMatch
	for _, found := range results {
		if len(seen) >= limit {
			break
		}
		if found.ID <= 0 || (found.Type != "" && found.Type != "release") {
			continue
		}
		if _, exists := seen[found.ID]; exists {
			continue
		}
		seen[found.ID] = struct{}{}
		id := strconv.FormatInt(found.ID, 10)
		var release release
		if err := c.get(ctx, "releases/"+id, nil, &release); err != nil {
			var status *HTTPError
			if errors.As(err, &status) && status.StatusCode == http.StatusNotFound {
				continue
			}
			return matches, err
		}
		if release.ID != 0 && release.ID != found.ID {
			continue
		}
		matches = append(matches, query.match(release, "https://www.discogs.com/release/"+id, alternatives)...)
	}

	return matches, nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values, result any) error {
	request, err := c.newRequest(ctx, http.MethodGet, path, query)
	if err != nil {
		return err
	}
	if c.oauth != nil && !c.oauthToken.valid() {
		return errors.New("discogs requires OAuth access credentials")
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.do(request, c.oauthToken, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(result); err != nil {
		return errors.New("invalid discogs response")
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values) (*http.Request, error) {
	if c.baseURL == nil {
		return nil, errors.New("invalid discogs base URL")
	}
	if c.oauth == nil && (c.token == "" || strings.ContainsAny(c.token, "\r\n")) {
		return nil, errors.New("discogs requires a valid API token")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoint := c.baseURL.JoinPath(path)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
	if err != nil {
		return nil, errors.New("invalid discogs request")
	}
	request.Header.Set("User-Agent", userAgent)
	return request, nil
}

func (c *Client) do(request *http.Request, token OAuthToken, oauthParams url.Values) (*http.Response, error) {
	if c.oauth != nil && (c.oauth.Key == "" || c.oauth.Secret == "") {
		return nil, errors.New("discogs requires an OAuth consumer key and secret")
	}
	if c.wait != nil {
		if err := c.wait(request.Context()); err != nil {
			if err == context.Canceled || err == context.DeadlineExceeded {
				return nil, err
			}
			credentials := []string{c.token}
			if c.oauth != nil {
				credentials = []string{c.oauth.Key, c.oauth.Secret, token.Token, token.Secret, oauthParams.Get("oauth_verifier")}
			}
			for _, credential := range credentials {
				if credential != "" && (strings.Contains(err.Error(), credential) || strings.Contains(err.Error(), oauthEscape(credential))) {
					return nil, errors.New("discogs request interrupted")
				}
			}
			return nil, err
		}
	}
	if c.oauth == nil {
		request.Header.Set("Authorization", "Discogs token="+c.token)
	} else {
		request.Header.Set("Authorization", oauthAuthorization(request, *c.oauth, token, oauthParams))
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return nil, context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			return nil, context.DeadlineExceeded
		default:
			// An HTTP client error can echo the request URL or a malformed header.
			return nil, errors.New("discogs request failed")
		}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		response.Body.Close()
		return nil, &HTTPError{StatusCode: response.StatusCode, RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
	}
	return response, nil
}

func retryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 && seconds <= math.MaxInt64/int64(time.Second) {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(date))
	}
	return 0
}
