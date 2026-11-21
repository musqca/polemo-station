package discogs

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOAuthSignatureMatchesRFC5849(t *testing.T) {
	endpoint, err := url.Parse("http://EXAMPLE.COM:80/request?b5=%3D%253D&a3=a&c%40=&a2=r%20b")
	if err != nil {
		t.Fatal(err)
	}
	protocol := url.Values{
		"oauth_consumer_key":     {"9djdj82h48djs9d2"},
		"oauth_nonce":            {"7d8f3e4a"},
		"oauth_signature_method": {"HMAC-SHA1"},
		"oauth_timestamp":        {"137131201"},
		"oauth_token":            {"kkk9d7dh3k39sjv7"},
	}
	form := url.Values{"c2": {""}, "a3": {"2 q"}}
	// RFC 5849 section 3.1 with verified erratum 2550 (the POST signature).
	const want = "r6/TJjbCOr97/+UU0NsvSne7s5g="
	if got := oauthSignature(http.MethodPost, endpoint, "j49sk3j29djd", "dh893hdasih9", protocol, form); got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
}

func TestOAuthAuthorizesCatalogueRequests(t *testing.T) {
	const callback = "https://station.example/discogs?state=x+y"
	app := OAuthApplication{Key: "consumer-key", Secret: "consumer&secret"}
	requestToken := OAuthToken{Token: "temporary+token", Secret: "temporary/token-secret"}
	accessToken := OAuthToken{Token: "access-token", Secret: "access/token-secret"}
	waits, requests := 0, 0
	nonces := make(map[string]bool)
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if waits != requests || r.URL.Host != "api.discogs.com" || r.URL.Scheme != "https" || r.UserAgent() == "" {
			t.Fatal("each request must wait and use the HTTPS Discogs API with a User-Agent")
		}
		oauth := readOAuthHeader(t, r.Header.Get("Authorization"))
		nonce := oauth.Get("oauth_nonce")
		timestamp, err := strconv.ParseInt(oauth.Get("oauth_timestamp"), 10, 64)
		if nonce == "" || nonces[nonce] || err != nil || timestamp < time.Now().Add(-time.Minute).Unix() || timestamp > time.Now().Add(time.Minute).Unix() {
			t.Fatal("expected a fresh nonce and current timestamp for each request")
		}
		nonces[nonce] = true
		if oauth.Get("oauth_consumer_key") != app.Key || oauth.Get("oauth_signature_method") != "HMAC-SHA1" || oauth.Get("oauth_version") != "1.0" {
			t.Fatal("expected the application key, signature method and version in the OAuth header")
		}
		for _, secret := range []string{app.Secret, requestToken.Secret, accessToken.Secret} {
			if strings.Contains(r.URL.String(), secret) || strings.Contains(r.Header.Get("Authorization"), secret) {
				t.Fatal("OAuth secrets must not appear in the request URL or headers")
			}
		}
		var prefix, callbackParameter, tokenParameters, token, tokenSecret, response string
		switch {
		case r.URL.Path == "/oauth/request_token":
			if r.Method != http.MethodPost || oauth.Get("oauth_callback") != callback || r.URL.RawQuery != "" {
				t.Fatal("expected a POST with the callback in the OAuth header")
			}
			callbackParameter = "oauth_callback=https%3A%2F%2Fstation.example%2Fdiscogs%3Fstate%3Dx%2By&"
			response = "oauth_token=temporary%2Btoken&oauth_token_secret=temporary%2Ftoken-secret&oauth_callback_confirmed=true"
		case r.URL.Path == "/oauth/access_token":
			if r.Method != http.MethodPost || oauth.Get("oauth_verifier") != "a + b" || r.URL.RawQuery != "" {
				t.Fatal("expected a POST with the verifier in the OAuth header")
			}
			token, tokenSecret = requestToken.Token, "temporary%2Ftoken-secret"
			tokenParameters = "&oauth_token=temporary%2Btoken&oauth_verifier=a%20%2B%20b"
			response = "oauth_token=access-token&oauth_token_secret=access%2Ftoken-secret"
		case r.URL.Path == "/database/search" && r.URL.Query().Has("q"):
			prefix = ""
			token, tokenSecret = accessToken.Token, "access%2Ftoken-secret"
			tokenParameters = "&oauth_token=access-token&oauth_version=1.0&per_page=1&q=__polemo_connection_test__&type=release"
			response = `{"results":[]}`
		case r.URL.Path == "/database/search":
			prefix = "artist=The%20A%2BB&"
			token, tokenSecret = accessToken.Token, "access%2Ftoken-secret"
			tokenParameters = "&oauth_token=access-token&oauth_version=1.0&per_page=25&track=N%C3%A9on%20%26%20Light~&type=release"
			response = `{"results":[{"id":31,"type":"release","title":"The A+B - Album"}]}`
		case r.URL.Path == "/releases/31":
			token, tokenSecret = accessToken.Token, "access%2Ftoken-secret"
			tokenParameters = "&oauth_token=access-token&oauth_version=1.0"
			response = `{"id":31,"title":"Album","artists":[{"name":"The A+B"}],"tracklist":[{"title":"Néon & Light~","type_":"track"}]}`
		default:
			t.Fatalf("unexpected Discogs endpoint %s", r.URL.Path)
		}
		if strings.HasPrefix(r.URL.Path, "/oauth/") {
			tokenParameters += "&oauth_version=1.0"
		}
		if oauth.Get("oauth_token") != token || r.URL.Query().Has("oauth_token") {
			t.Fatal("wrong credentials for this stage of OAuth")
		}
		params := prefix + callbackParameter + "oauth_consumer_key=consumer-key&oauth_nonce=" + nonce +
			"&oauth_signature_method=HMAC-SHA1&oauth_timestamp=" + oauth.Get("oauth_timestamp") + tokenParameters
		base := r.Method + "&" + url.QueryEscape("https://api.discogs.com"+r.URL.Path) + "&" + url.QueryEscape(params)
		mac := hmac.New(sha1.New, []byte("consumer%26secret&"+tokenSecret))
		mac.Write([]byte(base))
		if got, want := oauth.Get("oauth_signature"), base64.StdEncoding.EncodeToString(mac.Sum(nil)); got != want {
			t.Fatalf("incorrect OAuth signature for %s", r.URL.Path)
		}
		return jsonResponse(response), nil
	})}
	wait := func(context.Context) error {
		waits++
		return nil
	}
	ctx := context.Background()
	begin := NewOAuthClient(app, OAuthToken{}, "", hc, wait)
	if err := begin.Check(ctx); err == nil || requests != 0 {
		t.Fatalf("catalogue check without an access token sent %d requests, error %v", requests, err)
	}
	temporary, err := begin.RequestToken(ctx, callback)
	if err != nil || temporary != requestToken {
		t.Fatalf("request token exchange failed: %v", err)
	}
	if want := "https://www.discogs.com/oauth/authorize?oauth_token=temporary%2Btoken"; AuthorizeURL(temporary) != want {
		t.Fatal("approval URL must point to Discogs and carry only the temporary token")
	}
	access, err := begin.AccessToken(ctx, temporary, "a + b")
	if err != nil || access != accessToken {
		t.Fatalf("access token exchange failed: %v", err)
	}
	client := NewOAuthClient(app, access, "", hc, wait)
	if err := client.Check(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := client.Search(ctx, Query{Title: "Néon & Light~", Artist: "The A+B"})
	want := Result{Title: "Néon & Light~", Artist: "The A+B", Album: "Album", ReleaseURL: "https://www.discogs.com/release/31"}
	if err != nil || got != want || requests != 5 {
		t.Fatalf("search = %+v, %v, %d requests, want %+v after five requests", got, err, requests, want)
	}
}

func TestOAuthRejectsInvalidRequestTokenResponses(t *testing.T) {
	for name, body := range map[string]string{
		"unconfirmed callback": "oauth_token=token&oauth_token_secret=secret&oauth_callback_confirmed=false",
		"missing confirmation": "oauth_token=token&oauth_token_secret=secret",
		"missing secret":       "oauth_token=token&oauth_callback_confirmed=true",
		"duplicate secret":     "oauth_token=token&oauth_token_secret=secret&oauth_token_secret=other&oauth_callback_confirmed=true",
		"invalid form":         "oauth_token=%zz&oauth_token_secret=secret&oauth_callback_confirmed=true",
		"oversized response":   "oauth_token=token&oauth_token_secret=secret&oauth_callback_confirmed=true&oversized=" + strings.Repeat("s", 1<<20),
	} {
		t.Run(name, func(t *testing.T) {
			hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(body), nil
			})}
			client := NewOAuthClient(OAuthApplication{Key: "key", Secret: "secret"}, OAuthToken{}, "", hc, nil)
			got, err := client.RequestToken(context.Background(), "https://station.example/callback")
			if err == nil || strings.Contains(err.Error(), "oauth_token_secret=secret") || got != (OAuthToken{}) {
				t.Fatal("expected a sanitized error and no token from the invalid response")
			}
		})
	}
}

func TestOAuthTokenRequestDoesNotFollowRedirect(t *testing.T) {
	requests := 0
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Header:     http.Header{"Location": {"https://elsewhere.example/"}, "Retry-After": {"7"}},
			Body:       http.NoBody,
		}, nil
	})}
	client := NewOAuthClient(OAuthApplication{Key: "key", Secret: "secret"}, OAuthToken{}, "http://discogs.test", hc, nil)
	got, err := client.RequestToken(context.Background(), "oob")
	var status *HTTPError
	if !errors.As(err, &status) || status.StatusCode != http.StatusTemporaryRedirect || status.RetryAfter != 7*time.Second || requests != 1 || got != (OAuthToken{}) {
		t.Fatalf("expected the original redirect and Retry-After after one request, got %v after %d requests", err, requests)
	}
}

func TestOAuthReturnsCancellationFromWait(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("a canceled wait must prevent the request")
		return nil, nil
	})}
	client := NewOAuthClient(OAuthApplication{Key: "key", Secret: "c"}, OAuthToken{}, "", hc, func(context.Context) error {
		return context.Canceled
	})
	if _, err := client.RequestToken(context.Background(), "oob"); !errors.Is(err, context.Canceled) {
		t.Fatalf("request token error = %v, want context.Canceled", err)
	}
}

func TestOAuthRequiresHTTPSForDiscogs(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("OAuth credentials must not be sent to Discogs over HTTP")
		return nil, nil
	})}
	client := NewOAuthClient(OAuthApplication{Key: "key", Secret: "secret"}, OAuthToken{}, "http://api.discogs.com", hc, nil)
	if _, err := client.RequestToken(context.Background(), "oob"); err == nil {
		t.Fatal("expected an error for an HTTP Discogs API URL")
	}
}

func readOAuthHeader(t *testing.T, header string) url.Values {
	t.Helper()
	value, ok := strings.CutPrefix(header, "OAuth ")
	if !ok {
		t.Fatal("missing OAuth Authorization header")
	}
	parameters := make(url.Values)
	for _, pair := range strings.Split(value, ",") {
		key, quoted, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || len(quoted) < 2 || quoted[0] != '"' || quoted[len(quoted)-1] != '"' {
			t.Fatal("malformed OAuth header parameter")
		}
		decoded, err := url.PathUnescape(quoted[1 : len(quoted)-1])
		if err != nil || parameters.Has(key) {
			t.Fatal("invalid or repeated OAuth header parameter")
		}
		parameters.Set(key, decoded)
	}
	return parameters
}
