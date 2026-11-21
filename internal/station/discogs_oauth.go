package station

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/musqca/polemo-station/internal/thirdparty/discogs"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	DiscogsOAuthCallbackPath = "/api/station/scrape/providers/discogs/oauth/callback"
	discogsOAuthCookie       = "polemo_discogs_oauth"
	discogsOAuthTTL          = 10 * time.Minute
)

var stationLocale = regexp.MustCompile(`^[a-z]{2,3}(?:-[A-Z]{2})?$`)

type discogsOAuthPending struct {
	application discogs.OAuthApplication
	request     discogs.OAuthToken
	base        *url.URL
	browserHash string
	sessionID   uint
	language    string
	expiresAt   time.Time
	exchanging  bool
}

type discogsOAuthFlow struct {
	sync.Mutex
	pending *discogsOAuthPending
}

func (s *Service) discogsOAuthBase(r *http.Request) (*url.URL, error) {
	scheme := "http"
	if secureRequest(r) {
		scheme = "https"
	}
	base := &url.URL{Scheme: scheme, Host: r.Host}
	if configured := s.PublicBaseURL(); configured != "" {
		public, err := url.Parse(configured)
		if err != nil {
			return nil, err
		}
		if sameDiscogsOrigin(public, base) {
			base = public
		} else if host := base.Hostname(); !strings.EqualFold(host, "localhost") && !net.ParseIP(host).IsLoopback() {
			return nil, errors.New("open Station at " + configured + " before connecting Discogs")
		}
	}
	if base.Hostname() == "" {
		return nil, errors.New("Station could not determine its Discogs callback URL")
	}
	return base, nil
}

func sameDiscogsOrigin(left, right *url.URL) bool {
	port := func(endpoint *url.URL) string {
		if value := endpoint.Port(); value != "" {
			return value
		}
		if endpoint.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return left.Scheme == right.Scheme && strings.EqualFold(left.Hostname(), right.Hostname()) && port(left) == port(right)
}

func (s *Service) discogsOAuthStartBase(r *http.Request) (*url.URL, error) {
	base, err := s.discogsOAuthBase(r)
	if err != nil || base.Path != "" {
		return base, err
	}
	if mount := r.URL.Query().Get("mountPath"); mount != "" {
		prefix, err := url.Parse(mount)
		if err != nil || prefix.Host != "" || prefix.Scheme != "" || prefix.User != nil || prefix.RawQuery != "" || prefix.Fragment != "" ||
			!strings.HasPrefix(prefix.Path, "/") || path.Clean(prefix.Path) != prefix.Path || strings.Contains(prefix.Path, `\`) {
			return nil, errors.New("invalid Station mount path")
		}
		base.Path, base.RawPath = prefix.Path, prefix.RawPath
	}
	return base, nil
}

func (s *Service) DiscogsOAuthCallbackURL(r *http.Request) (string, error) {
	base, err := s.discogsOAuthStartBase(r)
	if err != nil {
		return "", err
	}
	return base.JoinPath(DiscogsOAuthCallbackPath).String(), nil
}

func (s *Service) StartDiscogsOAuth(w http.ResponseWriter, r *http.Request, key, secret, language string) (string, error) {
	cookie, err := r.Cookie(stationWebCookieName)
	if err != nil {
		return "", errors.New("a Station administrator web session is required")
	}
	identity, err := s.identityForToken(cookie.Value)
	if err != nil || identity.Kind != "web" || identity.Account.Role != AuthorizationRoleSuper {
		return "", errors.New("a Station administrator web session is required")
	}
	key, secret = strings.TrimSpace(key), strings.TrimSpace(secret)
	if key == "" || secret == "" || len(key) > 512 || len(secret) > 512 || strings.ContainsAny(key+secret, "\r\n") {
		return "", errors.New("a Discogs Consumer Key and Consumer Secret are required")
	}
	base, err := s.discogsOAuthStartBase(r)
	if err != nil {
		return "", err
	}
	settings, err := s.scrapeSettings()
	if err != nil {
		return "", err
	}
	if !stationLocale.MatchString(language) {
		language = "en-US"
	}
	browserToken, err := newStationToken()
	if err != nil {
		return "", err
	}
	application := discogs.OAuthApplication{Key: key, Secret: secret}
	pending := &discogsOAuthPending{application: application, base: base, browserHash: hashStationToken(browserToken),
		sessionID: identity.SessionID, language: language, expiresAt: time.Now().Add(discogsOAuthTTL)}
	s.discogsOAuth.Lock()
	s.discogsOAuth.pending = pending
	s.discogsOAuth.Unlock()

	timeout := time.Duration(settings.RequestTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	callback := base.JoinPath(DiscogsOAuthCallbackPath)
	client := s.newDiscogsOAuthClient(application, discogs.OAuthToken{}, timeout)
	request, err := client.RequestToken(ctx, callback.String())
	if err != nil {
		s.clearDiscogsOAuth(pending)
		return "", s.handleDiscogsError(err)
	}
	s.discogsOAuth.Lock()
	if s.discogsOAuth.pending != pending {
		s.discogsOAuth.Unlock()
		return "", errors.New("a newer Discogs connection replaced this attempt")
	}
	pending.request = request
	s.discogsOAuth.Unlock()
	time.AfterFunc(time.Until(pending.expiresAt), func() { s.clearDiscogsOAuth(pending) })

	// The regular Station cookie is SameSite=Strict. This one-use credential
	// survives Discogs' top-level redirect and is only sent to the callback.
	setDiscogsOAuthCookie(w, callback, browserToken, int(discogsOAuthTTL.Seconds()))
	return discogs.AuthorizeURL(request), nil
}

func (s *Service) clearDiscogsOAuth(pending *discogsOAuthPending) {
	s.discogsOAuth.Lock()
	defer s.discogsOAuth.Unlock()
	if s.discogsOAuth.pending == pending {
		s.discogsOAuth.pending = nil
	}
}

func setDiscogsOAuthCookie(w http.ResponseWriter, callback *url.URL, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: discogsOAuthCookie, Value: value, Path: callback.EscapedPath(), MaxAge: age,
		HttpOnly: true, Secure: callback.Scheme == "https", SameSite: http.SameSiteLaxMode})
}

func discogsOAuthReturn(base *url.URL, language, status string) string {
	path := "/" + strings.TrimLeft(base.JoinPath(language, "authorization", "providers").Path, "/")
	return (&url.URL{Path: path,
		RawQuery: url.Values{"discogsOAuth": {status}}.Encode()}).String()
}

func (s *Service) FinishDiscogsOAuth(w http.ResponseWriter, r *http.Request) string {
	base, err := s.discogsOAuthBase(r)
	if err != nil {
		return discogsOAuthReturn(&url.URL{}, "en-US", "failed")
	}
	query := r.URL.Query()
	requestToken := query.Get("oauth_token")
	denied := query.Get("denied")
	verifier := strings.TrimSpace(query.Get("oauth_verifier"))
	if denied != "" && (verifier != "" || (requestToken != "" && requestToken != denied)) {
		return discogsOAuthReturn(base, "en-US", "failed")
	}
	if requestToken == "" {
		requestToken = denied
	}
	cookie, err := r.Cookie(discogsOAuthCookie)
	if err != nil || requestToken == "" {
		return discogsOAuthReturn(base, "en-US", "failed")
	}
	s.discogsOAuth.Lock()
	pending := s.discogsOAuth.pending
	if pending == nil || pending.exchanging || !time.Now().Before(pending.expiresAt) ||
		!sameDiscogsOrigin(pending.base, base) || pending.request.Token != requestToken ||
		subtle.ConstantTimeCompare([]byte(pending.browserHash), []byte(hashStationToken(cookie.Value))) != 1 {
		s.discogsOAuth.Unlock()
		return discogsOAuthReturn(base, "en-US", "failed")
	}
	pending.exchanging = true
	s.discogsOAuth.Unlock()
	defer s.clearDiscogsOAuth(pending)
	base = pending.base
	setDiscogsOAuthCookie(w, base.JoinPath(DiscogsOAuthCallbackPath), "", -1)

	if denied != "" {
		return discogsOAuthReturn(base, pending.language, "cancelled")
	}
	failed := discogsOAuthReturn(base, pending.language, "failed")
	if verifier == "" || len(verifier) > 512 || !s.activeDiscogsOAuthSession(pending.sessionID) {
		return failed
	}
	settings, err := s.scrapeSettings()
	if err != nil {
		return failed
	}
	timeout := time.Duration(settings.RequestTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	client := s.newDiscogsOAuthClient(pending.application, discogs.OAuthToken{}, timeout)
	access, err := client.AccessToken(ctx, pending.request, verifier)
	if err != nil {
		_ = s.handleDiscogsError(err)
		return failed
	}
	if !s.saveDiscogsOAuth(pending, access) {
		return failed
	}
	return discogsOAuthReturn(base, pending.language, "connected")
}

func (s *Service) discogsOAuthSessionQuery(id uint, now time.Time) *gorm.DB {
	return s.db.Table("station_web_sessions AS session").Select("1").
		Joins("JOIN station_accounts AS account ON account.account_id = session.account_id").
		Where("session.id = ? AND session.status = ? AND session.expires_at > ?", id, AuthorizationStatusActive, now).
		Where("account.status = ? AND account.role = ?", AuthorizationStatusActive, AuthorizationRoleSuper)
}

func (s *Service) activeDiscogsOAuthSession(id uint) bool {
	var active int
	return s.discogsOAuthSessionQuery(id, time.Now()).Scan(&active).Error == nil && active == 1
}

func (s *Service) saveDiscogsOAuth(pending *discogsOAuthPending, access discogs.OAuthToken) bool {
	s.discogsOAuth.Lock()
	defer s.discogsOAuth.Unlock()
	now := time.Now()
	if s.discogsOAuth.pending != pending || !now.Before(pending.expiresAt) {
		return false
	}
	result := s.db.Session(&gorm.Session{Logger: logger.Discard}).Model(&ThirdPartyProviderConfig{}).Where("provider = ?", discogsProvider).
		Where("EXISTS (?)", s.discogsOAuthSessionQuery(pending.sessionID, now)).
		Updates(map[string]any{"oauth_consumer_key": pending.application.Key, "oauth_consumer_secret": pending.application.Secret,
			"oauth_access_token": access.Token, "oauth_access_token_secret": access.Secret, "enabled": true, "updated_at": time.Now()})
	return result.Error == nil && result.RowsAffected == 1
}

func (s *Service) DisconnectDiscogsOAuth() error {
	s.discogsOAuth.Lock()
	defer s.discogsOAuth.Unlock()
	s.discogsOAuth.pending = nil
	return s.db.Session(&gorm.Session{Logger: logger.Discard}).Model(&ThirdPartyProviderConfig{}).Where("provider = ?", discogsProvider).
		Updates(map[string]any{"oauth_consumer_key": "", "oauth_consumer_secret": "", "oauth_access_token": "", "oauth_access_token_secret": "",
			"enabled": gorm.Expr("CASE WHEN COALESCE(TRIM(api_key), '') = '' THEN ? ELSE enabled END", false), "updated_at": time.Now()}).Error
}
