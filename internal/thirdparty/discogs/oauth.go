package discogs

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// OAuthApplication contains a Discogs application's consumer credentials.
type OAuthApplication struct {
	Key    string
	Secret string
}

// OAuthToken contains the credentials for the temporary or authorized token.
type OAuthToken struct {
	Token  string
	Secret string
}

func (token OAuthToken) valid() bool {
	return token.Token != "" && token.Secret != ""
}

// NewOAuthClient creates a client for OAuth authorization and catalogue requests.
// Leave token empty to begin authorization.
func NewOAuthClient(application OAuthApplication, token OAuthToken, baseURL string, httpClient *http.Client, wait func(context.Context) error) *Client {
	client := NewClient("", baseURL, httpClient, wait)
	if base := client.baseURL; base != nil && base.Scheme == "http" && strings.EqualFold(strings.TrimSuffix(base.Hostname(), "."), defaultAPIHost) {
		client.baseURL = nil
	}
	client.oauth = &application
	client.oauthToken = token
	return client
}

// RequestToken obtains temporary credentials. An empty callbackURL uses manual
// verifier entry (out-of-band authorization).
func (c *Client) RequestToken(ctx context.Context, callbackURL string) (OAuthToken, error) {
	if callbackURL == "" {
		callbackURL = "oob"
	}
	return c.exchangeToken(ctx, "oauth/request_token", OAuthToken{}, url.Values{"oauth_callback": {callbackURL}})
}

// AuthorizeURL sends the user to Discogs to approve the temporary token.
func AuthorizeURL(requestToken OAuthToken) string {
	return "https://www.discogs.com/oauth/authorize?oauth_token=" + oauthEscape(requestToken.Token)
}

// AccessToken exchanges the temporary credentials and verifier for an access token.
func (c *Client) AccessToken(ctx context.Context, requestToken OAuthToken, verifier string) (OAuthToken, error) {
	if !requestToken.valid() || verifier == "" {
		return OAuthToken{}, errors.New("discogs requires an OAuth request token and verifier")
	}
	return c.exchangeToken(ctx, "oauth/access_token", requestToken, url.Values{"oauth_verifier": {verifier}})
}

func (c *Client) exchangeToken(ctx context.Context, path string, token OAuthToken, params url.Values) (OAuthToken, error) {
	if c.oauth == nil {
		return OAuthToken{}, errors.New("discogs requires an OAuth client")
	}
	request, err := c.newRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return OAuthToken{}, err
	}
	request.Header.Set("Accept", "application/x-www-form-urlencoded")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.do(request, token, params)
	if err != nil {
		return OAuthToken{}, err
	}
	defer response.Body.Close()
	const maxTokenResponseBytes = 16 << 10
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTokenResponseBytes+1))
	if err != nil || len(body) > maxTokenResponseBytes {
		return OAuthToken{}, errors.New("invalid discogs OAuth token response")
	}
	form, err := url.ParseQuery(string(body))
	result := OAuthToken{Token: form.Get("oauth_token"), Secret: form.Get("oauth_token_secret")}
	if err != nil || !result.valid() || len(form["oauth_token"]) != 1 || len(form["oauth_token_secret"]) != 1 {
		return OAuthToken{}, errors.New("invalid discogs OAuth token response")
	}
	if params.Has("oauth_callback") && (len(form["oauth_callback_confirmed"]) != 1 || form.Get("oauth_callback_confirmed") != "true") {
		return OAuthToken{}, errors.New("discogs did not confirm the OAuth callback")
	}
	return result, nil
}

func oauthAuthorization(request *http.Request, application OAuthApplication, token OAuthToken, extra url.Values) string {
	params := url.Values{
		"oauth_consumer_key":     {application.Key},
		"oauth_nonce":            {rand.Text()},
		"oauth_signature_method": {"HMAC-SHA1"},
		"oauth_timestamp":        {strconv.FormatInt(time.Now().Unix(), 10)},
		"oauth_version":          {"1.0"},
	}
	if token.Token != "" {
		params.Set("oauth_token", token.Token)
	}
	for key, values := range extra {
		params[key] = values
	}
	params.Set("oauth_signature", oauthSignature(request.Method, request.URL, application.Secret, token.Secret, params))
	var header []string
	for key, values := range params {
		header = append(header, oauthEscape(key)+`="`+oauthEscape(values[0])+`"`)
	}
	slices.Sort(header)
	return "OAuth " + strings.Join(header, ", ")
}

func oauthSignature(method string, endpoint *url.URL, applicationSecret, tokenSecret string, parameterSets ...url.Values) string {
	parameterSets = append(parameterSets, endpoint.Query())
	type parameter struct{ name, value string }
	var encoded []parameter
	for _, params := range parameterSets {
		for name, values := range params {
			if name == "oauth_signature" {
				continue
			}
			for _, value := range values {
				encoded = append(encoded, parameter{oauthEscape(name), oauthEscape(value)})
			}
		}
	}
	// OAuth sorts the encoded names first, then the encoded values of duplicates.
	slices.SortFunc(encoded, func(a, b parameter) int {
		if a.name == b.name {
			return strings.Compare(a.value, b.value)
		}
		return strings.Compare(a.name, b.name)
	})
	normalized := make([]string, 0, len(encoded))
	for _, param := range encoded {
		normalized = append(normalized, param.name+"="+param.value)
	}
	base := oauthEscape(strings.ToUpper(method)) + "&" + oauthEscape(oauthBaseURI(endpoint)) + "&" + oauthEscape(strings.Join(normalized, "&"))
	key := oauthEscape(applicationSecret) + "&" + oauthEscape(tokenSecret)
	mac := hmac.New(sha1.New, []byte(key))
	mac.Write([]byte(base))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func oauthBaseURI(endpoint *url.URL) string {
	scheme := strings.ToLower(endpoint.Scheme)
	host := strings.ToLower(endpoint.Host)
	if port := endpoint.Port(); (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		host = strings.TrimSuffix(host, ":"+port)
	}
	path := endpoint.EscapedPath()
	if path == "" {
		path = "/"
	}
	return scheme + "://" + host + path
}

// OAuth uses RFC 3986 percent encoding, including %20 for spaces.
func oauthEscape(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}
