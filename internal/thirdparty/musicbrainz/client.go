package musicbrainz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/wonli/apic/v2"
	"golang.org/x/text/unicode/norm"
)

const defaultBaseURL = "https://musicbrainz.org/ws/2"

type Client struct {
	baseURL    string
	userAgent  string
	httpClient *http.Client
	timeout    time.Duration
}

type Option func(*Client)

func WithBaseURL(value string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(value, "/") }
}
func WithHTTPClient(value *http.Client) Option { return func(c *Client) { c.httpClient = value } }
func WithUserAgent(value string) Option        { return func(c *Client) { c.userAgent = value } }
func WithTimeout(value time.Duration) Option   { return func(c *Client) { c.timeout = value } }

func NewClient(opts ...Option) *Client {
	c := &Client{baseURL: defaultBaseURL, userAgent: "Polemo Station/1.0 ( https://github.com/musqca/polemo-station )", timeout: 15 * time.Second}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type recordingSearch struct {
	Recordings []recording `json:"recordings"`
}
type recording struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Length       int    `json:"length"`
	ArtistCredit []struct {
		Name       string `json:"name"`
		JoinPhrase string `json:"joinphrase"`
		Artist     struct {
			Name string `json:"name"`
		} `json:"artist"`
	} `json:"artist-credit"`
	Releases []struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		Date         string `json:"date"`
		ReleaseGroup struct {
			ID string `json:"id"`
		} `json:"release-group"`
	} `json:"releases"`
}

type Result struct {
	RecordingID    string
	ReleaseID      string
	ReleaseGroupID string
	Title          string
	Artist         string
	Album          string
	TrackNumber    int
	DiscNumber     int
}

func (c *Client) Search(ctx context.Context, title, artist, album string) (Result, error) {
	title, artist, album = strings.TrimSpace(title), strings.TrimSpace(artist), strings.TrimSpace(album)
	if title == "" && artist == "" {
		return Result{}, errors.New("musicbrainz search requires title or artist")
	}
	if title == "" {
		return Result{}, ErrNoMatch
	}
	query := "recording:" + searchPhrase(title)
	if artist != "" {
		phrase := searchPhrase(artist)
		artistQuery := "artist:" + phrase + " OR artistname:" + phrase
		if lead, _, found := strings.Cut(normalizeArtistCredit(artist), " & "); found && lead != "" {
			artistQuery += " OR artist:" + searchPhrase(lead)
		}
		query += " AND (" + artistQuery + ")"
	}
	if album != "" {
		// A release match improves ranking without filtering out compilations
		// or recordings found under a different release title.
		query = "+(" + query + ") release:" + searchPhrase(album)
	}
	var response recordingSearch
	err := (&endpoint{client: c, path: "/recording", query: url.Values{"query": {query}, "limit": {"20"}, "fmt": {"json"}, "inc": {"releases+artist-credits+release-groups"}}}).call(ctx, &response)
	if err != nil {
		return Result{}, err
	}
	var best Result
	bestRank := -1
	for _, item := range response.Recordings {
		if !sameText(title, item.Title) {
			continue
		}
		creditedArtist := item.creditedArtist()
		fullCreditMatch := sameText(normalizeArtistCredit(artist), normalizeArtistCredit(creditedArtist))
		if artist != "" && !fullCreditMatch && !item.matchesLeadArtist(artist) {
			continue
		}
		result := Result{RecordingID: item.ID, Title: item.Title, Artist: creditedArtist, Album: album}
		// Prefer the requested release, then the full artist credit and exact title.
		rank := 0
		if fullCreditMatch {
			rank += 2
		}
		if strings.EqualFold(title, strings.TrimSpace(item.Title)) {
			rank++
		}
		if album == "" && len(item.Releases) > 0 {
			result.ReleaseID, result.Album = item.Releases[0].ID, item.Releases[0].Title
			result.ReleaseGroupID = item.Releases[0].ReleaseGroup.ID
		}
		for _, release := range item.Releases {
			if album != "" && sameText(album, release.Title) {
				result.ReleaseID, result.Album = release.ID, release.Title
				result.ReleaseGroupID = release.ReleaseGroup.ID
				rank += 4
				break
			}
		}
		if rank > bestRank {
			best, bestRank = result, rank
		}
	}
	if bestRank < 0 {
		return Result{}, ErrNoMatch
	}
	return best, nil
}

func (r recording) creditedArtist() string {
	var names strings.Builder
	for i, credit := range r.ArtistCredit {
		if i > 0 && r.ArtistCredit[i-1].JoinPhrase == "" {
			names.WriteString(", ")
		}
		name := credit.Name
		if name == "" {
			name = credit.Artist.Name
		}
		names.WriteString(name)
		names.WriteString(credit.JoinPhrase)
	}
	return strings.TrimSpace(names.String())
}

func (r recording) matchesLeadArtist(artist string) bool {
	if len(r.ArtistCredit) == 0 {
		return false
	}
	lead := r.ArtistCredit[0]
	return sameText(artist, lead.Name) || sameText(artist, lead.Artist.Name)
}

// Check verifies that the MusicBrainz endpoint is reachable without creating
// or changing any library data.
func (c *Client) Check(ctx context.Context) error {
	var response recordingSearch
	return (&endpoint{client: c, path: "/recording", query: url.Values{
		"query": {`recording:"__polemo_connection_test__"`}, "limit": {"1"}, "fmt": {"json"},
	}}).call(ctx, &response)
}

// ReleaseGroupForRelease resolves the release-group needed by services such as
// Fanart.tv. It is also used to backfill MusicBrainz work completed before we
// began storing release-group IDs locally.
func (c *Client) ReleaseGroupForRelease(ctx context.Context, releaseID string) (string, error) {
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" {
		return "", ErrNoMatch
	}
	var response struct {
		ReleaseGroup struct {
			ID string `json:"id"`
		} `json:"release-group"`
	}
	if err := (&endpoint{client: c, path: "/release/" + url.PathEscape(releaseID), query: url.Values{"fmt": {"json"}, "inc": {"release-groups"}}}).call(ctx, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.ReleaseGroup.ID) == "" {
		return "", ErrNoMatch
	}
	return response.ReleaseGroup.ID, nil
}

var ErrNoMatch = errors.New("musicbrainz no match")

func escapeQuery(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`)
}

func searchPhrase(value string) string {
	value = strings.Join(strings.Fields(norm.NFKC.String(value)), " ")
	return `"` + escapeQuery(value) + `"`
}

var creditJoinPhrases = strings.NewReplacer(" feat. ", " & ", " feat ", " & ", " ft. ", " & ", " ft ", " & ", " featuring ", " & ")

func normalizeArtistCredit(value string) string {
	value = strings.Join(strings.Fields(norm.NFKC.String(strings.ToLower(value))), " ")
	return creditJoinPhrases.Replace(value)
}

func sameText(left, right string) bool {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	if strings.EqualFold(left, right) {
		return true
	}
	separator := func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }
	left = strings.Join(strings.FieldsFunc(norm.NFKC.String(left), separator), " ")
	right = strings.Join(strings.FieldsFunc(norm.NFKC.String(right), separator), " ")
	return left != "" && strings.EqualFold(left, right)
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
	return apic.Params{"Accept": "application/json", "User-Agent": e.client.userAgent}
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
	return fmt.Errorf("musicbrainz returned HTTP %d: %s", code, strings.TrimSpace(string(data)))
}

func (e *endpoint) call(ctx context.Context, out any) error {
	id := apic.ApiId{Name: "musicbrainz." + strings.TrimPrefix(e.path, "/"), Client: e}
	client := apic.NewApiClient().WithContext(ctx)
	if e.client.httpClient != nil {
		client.WithHTTPClient(e.client.httpClient)
	}
	if err := client.Call(&id, &apic.Options{Timeout: e.client.timeout}); err != nil {
		return err
	}
	if id.Response == nil {
		return errors.New("musicbrainz empty response")
	}
	if err := json.Unmarshal(id.Response.Data, out); err != nil {
		return fmt.Errorf("musicbrainz decode response: %w", err)
	}
	return nil
}
