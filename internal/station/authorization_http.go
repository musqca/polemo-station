package station

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
)

func (s *Service) AuthorizationHTTPState(r *http.Request) (map[string]any, error) {
	return s.authorizationState(stationTokenFromRequest(r))
}

func SameOriginRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	expectedScheme := "http"
	if secureRequest(r) {
		expectedScheme = "https"
	}
	return err == nil && strings.EqualFold(parsed.Scheme, expectedScheme) && strings.EqualFold(parsed.Host, r.Host)
}

func secureRequest(r *http.Request) bool {
	return r != nil && (r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https"))
}

func SetStationWebCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: stationWebCookieName, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode})
}

func (s *Service) ExchangeWebTicket(token string) (string, error) {
	stationAuthorizationMu.Lock()
	defer stationAuthorizationMu.Unlock()
	ticket, err := s.findTicket(token)
	if err != nil || ticket.Status != AuthorizationStatusApproved || !ticket.ExpiresAt.After(time.Now()) ||
		(ticket.Action != AuthorizationActionWebLogin && ticket.Action != AuthorizationActionBootstrap) || ticket.ApprovedAccountID == "" {
		return "", errors.New("station login ticket is not approved")
	}
	var sessionToken string
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var account StationAccount
		if err := tx.Where("account_id = ? AND status = ?", ticket.ApprovedAccountID, AuthorizationStatusActive).First(&account).Error; err != nil {
			return err
		}
		sessionToken, err = newStationToken()
		if err != nil {
			return err
		}
		now := time.Now()
		session := StationWebSession{TokenHash: hashStationToken(sessionToken), AccountID: ticket.ApprovedAccountID,
			ClientName: ticket.ClientName, Status: AuthorizationStatusActive, ExpiresAt: now.Add(stationWebSessionTTL), LastSeenAt: &now}
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		result := tx.Model(&StationQrTicket{}).Where("id = ? AND status = ?", ticket.ID, AuthorizationStatusApproved).
			Updates(map[string]any{"status": AuthorizationStatusUsed, "used_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("station login ticket was already used")
		}
		return nil
	})
	if err == nil {
		s.notifyAuthorizationChanged("sessions")
	}
	return sessionToken, err
}

func (s *Service) RefreshWebSession(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(stationWebCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return
	}
	var session StationWebSession
	if err := s.db.Where("token_hash = ? AND status = ? AND expires_at > ?", hashStationToken(cookie.Value), AuthorizationStatusActive, time.Now()).First(&session).Error; err != nil {
		return
	}
	if time.Until(session.ExpiresAt) >= stationWebRefreshWindow {
		return
	}
	now := time.Now()
	if err := s.db.Model(&StationWebSession{}).Where("id = ? AND status = ?", session.ID, AuthorizationStatusActive).
		Updates(map[string]any{"expires_at": now.Add(stationWebSessionTTL), "last_seen_at": now}).Error; err == nil {
		SetStationWebCookie(w, r, cookie.Value, int(stationWebSessionTTL.Seconds()))
	}
}

func (s *Service) LogoutCurrentWebSession(r *http.Request) error {
	identity, err := s.identityForToken(stationTokenFromRequest(r))
	if err != nil || identity.Kind != "web" {
		return gorm.ErrRecordNotFound
	}
	var session StationWebSession
	if err := s.db.First(&session, identity.SessionID).Error; err != nil {
		return err
	}
	now := time.Now()
	if err := s.db.Model(&StationWebSession{}).Where("id = ?", session.ID).Updates(map[string]any{"status": AuthorizationStatusRevoked, "revoked_at": now}).Error; err != nil {
		return err
	}
	s.notifyWebSessionsLoggedOut([]StationWebSession{session})
	s.notifyAuthorizationChanged("sessions")
	return nil
}
