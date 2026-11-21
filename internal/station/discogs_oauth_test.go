package station

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/musqca/polemo-station/internal/thirdparty/discogs"
)

func TestDiscogsOAuthUsesVerifiedCallbackAndPreservesPersonalToken(t *testing.T) {
	s := newTransferTestService(t)
	account := StationAccount{AccountID: "oauth-admin", Role: AuthorizationRoleSuper, Status: AuthorizationStatusActive}
	if err := s.db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	session := StationWebSession{AccountID: account.AccountID, Status: AuthorizationStatusActive, TokenHash: hashStationToken("web-cookie"), ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	personalToken := "original-personal-token"
	if err := s.SaveScrapeProvider("discogs", false, &personalToken); err != nil {
		t.Fatal(err)
	}
	var requests []string
	s.SetScrapeHTTPClient(&http.Client{Transport: discogsTestTransport(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.Path)
		auth := r.Header.Get("Authorization")
		var body string
		switch r.URL.Path {
		case "/oauth/request_token":
			if !strings.Contains(auth, `oauth_consumer_key="app-key"`) || !strings.Contains(auth, `oauth_callback="https%3A%2F%2Fstation.test%2Fprefix%2Fapi%2Fstation%2Fscrape%2Fproviders%2Fdiscogs%2Foauth%2Fcallback"`) {
				t.Fatal("request-token call was not signed for the registered callback")
			}
			body = "oauth_token=temporary-key&oauth_token_secret=temporary-secret&oauth_callback_confirmed=true"
		case "/oauth/access_token":
			if !strings.Contains(auth, `oauth_token="temporary-key"`) || !strings.Contains(auth, `oauth_verifier="approved"`) {
				t.Fatal("access-token call was not signed for the approved temporary token")
			}
			body = "oauth_token=access-key&oauth_token_secret=access-secret"
		case "/database/search":
			if len(requests) == 3 && !strings.Contains(auth, `oauth_token="access-key"`) {
				t.Fatal("catalogue search did not use the connected account")
			}
			if len(requests) == 4 && auth != "Discogs token="+personalToken {
				t.Fatal("catalogue search did not return to the personal token after disconnect")
			}
			body = `{"results":[]}`
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
	start := httptest.NewRequest(http.MethodPost, "https://station.test/api/station/scrape/providers/discogs/oauth?mountPath=%2Fprefix", nil)
	start.AddCookie(&http.Cookie{Name: stationWebCookieName, Value: "web-cookie"})
	response := httptest.NewRecorder()
	authorizeURL, err := s.StartDiscogsOAuth(response, start, "app-key", "app-secret", "zh-CN")
	if err != nil || authorizeURL != "https://www.discogs.com/oauth/authorize?oauth_token=temporary-key" {
		t.Fatalf("Discogs authorization start: %s, %v", authorizeURL, err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/prefix/api/station/scrape/providers/discogs/oauth/callback" {
		t.Fatalf("callback cookie does not survive a safe cross-site redirect: %+v", cookies)
	}
	callback := httptest.NewRequest(http.MethodGet, "https://station.test/api/station/scrape/providers/discogs/oauth/callback?oauth_token=temporary-key&oauth_verifier=approved", nil)
	if result := s.FinishDiscogsOAuth(httptest.NewRecorder(), callback); !strings.HasSuffix(result, "?discogsOAuth=failed") || len(requests) != 1 {
		t.Fatalf("unbound callback made a request: result=%s requests=%v", result, requests)
	}
	otherBrowser := callback.Clone(callback.Context())
	otherBrowser.AddCookie(&http.Cookie{Name: cookies[0].Name, Value: "other-browser"})
	if result := s.FinishDiscogsOAuth(httptest.NewRecorder(), otherBrowser); !strings.HasSuffix(result, "?discogsOAuth=failed") || len(requests) != 1 {
		t.Fatalf("another browser could finish the callback: result=%s requests=%v", result, requests)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	publicRequest := &url.URL{Scheme: "https", Host: "station.test", Path: "/prefix" + start.URL.Path}
	publicCallback := &url.URL{Scheme: "https", Host: "station.test", Path: "/prefix" + callback.URL.Path}
	jar.SetCookies(publicRequest, cookies)
	for _, cookie := range jar.Cookies(publicCallback) {
		callback.AddCookie(cookie)
	}
	if redirect := s.FinishDiscogsOAuth(httptest.NewRecorder(), callback); redirect != "/prefix/zh-CN/authorization/providers?discogsOAuth=connected" {
		t.Fatalf("unexpected authorization callback redirect: %s", redirect)
	}
	if retry := s.FinishDiscogsOAuth(httptest.NewRecorder(), callback); !strings.HasSuffix(retry, "?discogsOAuth=failed") || len(requests) != 2 {
		t.Fatalf("callback could be replayed: result=%s requests=%v", retry, requests)
	}
	if err := s.TestScrapeProvider("discogs"); err != nil || len(requests) != 3 {
		t.Fatalf("OAuth catalogue check: requests=%v, err=%v", requests, err)
	}
	view, err := s.ScrapeSettingsView()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(view)
	if err != nil || !strings.Contains(string(data), `"oauthConnected":true`) {
		t.Fatalf("Discogs connection state is missing: %s, %v", data, err)
	}
	for _, value := range []string{"app-key", "app-secret", "access-key", "access-secret", personalToken} {
		if strings.Contains(string(data), value) {
			t.Fatal("provider settings expose a Discogs credential")
		}
	}
	if err := s.DisconnectDiscogsOAuth(); err != nil {
		t.Fatal(err)
	}
	provider, err := s.providerConfig(discogsProvider)
	if err != nil || provider.discogsOAuthConnected() || !provider.Enabled || provider.APIKey != personalToken {
		t.Fatalf("disconnect did not restore the personal token fallback: err=%v", err)
	}
	if err := s.TestScrapeProvider("discogs"); err != nil || len(requests) != 4 {
		t.Fatalf("personal token catalogue check: requests=%v, err=%v", requests, err)
	}
}

func TestDiscogsOAuthCallbackUsesConfiguredOriginAndRejectsUnsafeMount(t *testing.T) {
	s := newTransferTestService(t)
	if err := s.SetPublicBaseURL("https://station.test:443/prefix"); err != nil {
		t.Fatal(err)
	}
	start := httptest.NewRequest(http.MethodGet, "https://station.test/api/station/scrape/providers/discogs/oauth", nil)
	if callback, err := s.DiscogsOAuthCallbackURL(start); err != nil || callback != "https://station.test:443/prefix"+DiscogsOAuthCallbackPath {
		t.Fatalf("configured callback rejected an equivalent origin: %s, %v", callback, err)
	}
	if _, err := s.DiscogsOAuthCallbackURL(httptest.NewRequest(http.MethodGet, "https://unrelated.test/oauth", nil)); err == nil {
		t.Fatal("a different origin received the configured callback")
	}
	s = newTransferTestService(t)
	for _, mount := range []string{"//unrelated.test", "/prefix/../other", "/%5Cfoo"} {
		start := httptest.NewRequest(http.MethodGet, "https://station.test/oauth?mountPath="+url.QueryEscape(mount), nil)
		if _, err := s.DiscogsOAuthCallbackURL(start); err == nil {
			t.Fatalf("unsafe mount path accepted: %q", mount)
		}
	}
}

func TestDiscogsOAuthCallbackStaysOnLoopbackOrigin(t *testing.T) {
	s := newTransferTestService(t)
	if err := s.SetPublicBaseURL("https://station.test/public"); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"http://localhost:20156", "http://127.0.0.1:20156", "http://[::1]:20156"} {
		start := httptest.NewRequest(http.MethodGet, origin+"/oauth?mountPath=/local", nil)
		want := origin + "/local" + DiscogsOAuthCallbackPath
		if callback, err := s.DiscogsOAuthCallbackURL(start); err != nil || callback != want {
			t.Errorf("callback for %s = %q, %v; want %q", origin, callback, err, want)
		}
	}
}

func TestDiscogsOAuthSaveRequiresActiveOriginalSession(t *testing.T) {
	s := newTransferTestService(t)
	account := StationAccount{AccountID: "revoked-oauth-admin", Role: AuthorizationRoleSuper, Status: AuthorizationStatusActive}
	if err := s.db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	session := StationWebSession{AccountID: account.AccountID, Status: AuthorizationStatusRevoked, TokenHash: hashStationToken("revoked"), ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	pending := &discogsOAuthPending{sessionID: session.ID, expiresAt: time.Now().Add(time.Minute), application: discogs.OAuthApplication{Key: "app", Secret: "secret"}}
	s.discogsOAuth.pending = pending
	if s.saveDiscogsOAuth(pending, discogs.OAuthToken{Token: "access", Secret: "secret"}) {
		t.Fatal("revoked session persisted a Discogs connection")
	}
}
