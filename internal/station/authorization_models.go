package station

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wonli/aqi/ws"
	"gorm.io/gorm"
)

const (
	AuthorizationRoleUser          = "user"
	AuthorizationRoleSuper         = "superadmin"
	AuthorizationStatusPending     = "pending"
	AuthorizationStatusActive      = "active"
	AuthorizationStatusApproved    = "approved"
	AuthorizationStatusUsed        = "used"
	AuthorizationStatusRevoked     = "revoked"
	AuthorizationActionBootstrap   = "bootstrap"
	AuthorizationActionWebLogin    = "web_login"
	AuthorizationActionAccountBind = "account_bind"
	StationAccessModePublic        = "public"
	StationAccessModeScan          = "scan"
	stationAuthorizationTTL        = 2 * time.Minute
	stationWebSessionTTL           = 30 * 24 * time.Hour
	stationWebRefreshWindow        = 15 * 24 * time.Hour
	stationWebCookieName           = "polemo_station_session"
)

type StationAccount struct {
	ID        uint       `gorm:"primaryKey" json:"-"`
	AccountID string     `gorm:"uniqueIndex;size:64" json:"accountId"`
	Name      string     `gorm:"index:idx_station_account_names;size:64" json:"accountName"`
	Role      string     `gorm:"size:32;index" json:"role"`
	Status    string     `gorm:"size:16;index" json:"status"`
	CreatedAt time.Time  `json:"createdAt"`
	BoundAt   *time.Time `json:"boundAt,omitempty"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}

type StationAppCredential struct {
	ID         uint       `gorm:"primaryKey" json:"-"`
	TokenHash  string     `gorm:"uniqueIndex;size:64" json:"-"`
	AccountID  string     `gorm:"size:64;index" json:"accountId"`
	ClientName string     `gorm:"size:255" json:"clientName,omitempty"`
	Status     string     `gorm:"size:16;index" json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}

type StationWebSession struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	TokenHash  string     `gorm:"uniqueIndex;size:64" json:"-"`
	AccountID  string     `gorm:"size:64;index" json:"accountId"`
	ClientName string     `gorm:"size:255" json:"clientName,omitempty"`
	Status     string     `gorm:"size:16;index" json:"status"`
	ExpiresAt  time.Time  `gorm:"index" json:"expiresAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}

type StationQrTicket struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	TokenHash         string     `gorm:"uniqueIndex;size:64" json:"-"`
	Action            string     `gorm:"size:32;index" json:"action"`
	AccountID         string     `gorm:"size:64;index" json:"accountId,omitempty"`
	ApprovedAccountID string     `gorm:"size:64;index" json:"approvedAccountId,omitempty"`
	ClientName        string     `gorm:"size:255" json:"clientName,omitempty"`
	Status            string     `gorm:"size:16;index" json:"status"`
	ExpiresAt         time.Time  `gorm:"index" json:"expiresAt"`
	CreatedAt         time.Time  `json:"createdAt"`
	UsedAt            *time.Time `json:"usedAt,omitempty"`
	RevokedAt         *time.Time `json:"revokedAt,omitempty"`
}

type authorizationIdentity struct {
	Account      StationAccount
	Kind         string
	CredentialID uint
	SessionID    uint
}

func hashStationToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", digest[:])
}

func newStationToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func newStationAccountID() (string, error) {
	token, err := newStationToken()
	return "sa_" + token, err
}

func (s *Service) hasSuperadmin() (bool, error) {
	var count int64
	err := s.db.Model(&StationAccount{}).Where("role = ? AND status = ?", AuthorizationRoleSuper, AuthorizationStatusActive).Count(&count).Error
	return count > 0, err
}

// Initialized reports whether Station has a bound superadmin and can serve
// normal Station APIs. Bootstrap authorization remains available beforehand.
func (s *Service) Initialized() bool {
	hasAdmin, err := s.hasSuperadmin()
	return err == nil && hasAdmin
}

func (s *Service) activeAccount(accountID string) (*StationAccount, error) {
	var account StationAccount
	if err := s.db.Where("account_id = ? AND status = ?", accountID, AuthorizationStatusActive).First(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (s *Service) identityForToken(token string) (*authorizationIdentity, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, gorm.ErrRecordNotFound
	}
	hash := hashStationToken(token)
	var credential StationAppCredential
	if err := s.db.Where("token_hash = ? AND status = ?", hash, AuthorizationStatusActive).First(&credential).Error; err == nil {
		account, accountErr := s.activeAccount(credential.AccountID)
		if accountErr != nil {
			return nil, accountErr
		}
		return &authorizationIdentity{Account: *account, Kind: "app", CredentialID: credential.ID}, nil
	}
	var session StationWebSession
	if err := s.db.Where("token_hash = ? AND status = ? AND expires_at > ?", hash, AuthorizationStatusActive, time.Now()).First(&session).Error; err != nil {
		return nil, err
	}
	account, err := s.activeAccount(session.AccountID)
	if err != nil {
		return nil, err
	}
	return &authorizationIdentity{Account: *account, Kind: "web", SessionID: session.ID}, nil
}

// SuperadminAuthorized is used by HTTP settings endpoints that are not part of
// the websocket action protocol.
func (s *Service) SuperadminAuthorized(r *http.Request) bool {
	identity, err := s.identityForToken(stationTokenFromRequest(r))
	return err == nil && identity.Account.Role == AuthorizationRoleSuper && identity.Account.Status == AuthorizationStatusActive
}

func stationTokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if header := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(header, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	}
	if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
		return token
	}
	if cookie, err := r.Cookie(stationWebCookieName); err == nil {
		return strings.TrimSpace(cookie.Value)
	}
	return ""
}

func stationTokenFromContext(a *ws.Context) string {
	if a == nil || a.Client == nil || a.Client.HttpRequest == nil {
		return ""
	}
	return stationTokenFromRequest(a.Client.HttpRequest)
}

func (s *Service) clientIdentity(a *ws.Context) (*authorizationIdentity, error) {
	if a != nil && a.Client != nil {
		if value := a.Client.GetKey("stationAppCredentialId"); value != nil {
			if id, err := strconv.ParseUint(value.String(), 10, 64); err == nil {
				var credential StationAppCredential
				if err := s.db.Where("id = ? AND status = ?", uint(id), AuthorizationStatusActive).First(&credential).Error; err == nil {
					account, accountErr := s.activeAccount(credential.AccountID)
					if accountErr == nil {
						return &authorizationIdentity{Account: *account, Kind: "app", CredentialID: credential.ID}, nil
					}
				}
			}
		}
	}
	return s.identityForToken(stationTokenFromContext(a))
}

func setClientAppCredential(client *ws.Client, credentialID uint) {
	if client == nil || credentialID == 0 {
		return
	}
	// aqi's Value.String only returns values stored as strings.
	client.SetKey("stationAppCredentialId", strconv.FormatUint(uint64(credentialID), 10))
}

func (s *Service) authorizationRequired() bool { return s.accessMode() != StationAccessModePublic }

func (s *Service) accessMode() string {
	var settings StationSecuritySettings
	if s.db.First(&settings, 1).Error == nil && settings.AccessMode != "" {
		return settings.AccessMode
	}
	return StationAccessModePublic
}

func (s *Service) saveAccessMode(mode string) error {
	var settings StationSecuritySettings
	_ = s.db.First(&settings, 1).Error
	settings.ID = 1
	settings.AccessMode = mode
	return s.db.Save(&settings).Error
}

func (s *Service) stationIdentity() (string, error) {
	var settings StationSecuritySettings
	_ = s.db.First(&settings, 1).Error
	if settings.StationID != "" {
		return settings.StationID, nil
	}
	token, err := newStationToken()
	if err != nil {
		return "", err
	}
	settings.ID = 1
	settings.StationID = "st_" + token
	if settings.AccessMode == "" {
		settings.AccessMode = StationAccessModePublic
	}
	return settings.StationID, s.db.Save(&settings).Error
}

func mustStationIdentity(s *Service) string {
	identity, _ := s.stationIdentity()
	return identity
}
