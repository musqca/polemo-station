package station

import (
	"strings"
	"sync"
	"time"

	"github.com/wonli/aqi/ws"
	"gorm.io/gorm"
)

func (s *Service) trackWebAuthorizationClient(sessionID uint, client *ws.Client) {
	if sessionID == 0 || client == nil {
		return
	}
	value, _ := s.webAuthorizationClients.LoadOrStore(sessionID, &sync.Map{})
	if clients, ok := value.(*sync.Map); ok {
		clients.Store(client, struct{}{})
	}
}

func (s *Service) notifyWebSessionsLoggedOut(sessions []StationWebSession) {
	for _, session := range sessions {
		if value, ok := s.webAuthorizationClients.LoadAndDelete(session.ID); ok {
			if clients, ok := value.(*sync.Map); ok {
				clients.Range(func(client, _ any) bool {
					if webClient, ok := client.(*ws.Client); ok && webClient != nil {
						webClient.SendActionMsg(&ws.Action{Action: "v1.station.authorization.logout", Data: map[string]any{"sessionId": session.ID}})
					}
					return true
				})
			}
		}
	}
}

func (s *Service) notifyAuthorizationChanged(kind string) {
	message := &ws.Action{Action: "v1.station.authorization.changed", Data: map[string]any{"kind": kind}}
	s.onlineDevices.Range(func(_, value any) bool {
		if client, ok := value.(*ws.Client); ok && client != nil {
			client.SendActionMsg(message)
		}
		return true
	})
}

func (s *Service) AuthorizationState(a *ws.Context) {
	// ClaimAuthorization attaches the newly issued credential to this socket.
	// Its upgrade header still contains the one-time ticket, not that token.
	identity, _ := s.clientIdentity(a)
	state, err := s.authorizationStateForIdentity(identity)
	if err != nil {
		a.SendCode(500, "station authorization state unavailable")
		return
	}
	if identity != nil && identity.Kind == "web" {
		s.trackWebAuthorizationClient(identity.SessionID, a.Client)
	}
	endpoint, _ := s.publicEndpointValue()
	state["endpoint"] = endpoint
	a.Send(state)
}

func (s *Service) authorizationState(token string) (map[string]any, error) {
	identity, _ := s.identityForToken(token)
	return s.authorizationStateForIdentity(identity)
}

func (s *Service) authorizationStateForIdentity(identity *authorizationIdentity) (map[string]any, error) {
	hasAdmin, err := s.hasSuperadmin()
	if err != nil {
		return nil, err
	}
	authenticated := identity != nil
	state := map[string]any{"initialized": hasAdmin, "authenticated": authenticated, "superadmin": false,
		"accessMode": s.accessMode(), "stationId": mustStationIdentity(s)}
	if authenticated {
		state["superadmin"] = identity.Account.Role == AuthorizationRoleSuper
		state["accountId"] = identity.Account.AccountID
		state["accountName"] = identity.Account.Name
		state["role"] = identity.Account.Role
		state["createdAt"] = identity.Account.CreatedAt
	}
	return state, nil
}

func (s *Service) ListAuthorizations(a *ws.Context) {
	identity, err := s.clientIdentity(a)
	if err != nil || identity.Account.Role != AuthorizationRoleSuper {
		a.SendCode(403, "superadmin authorization required")
		return
	}
	var accounts []StationAccount
	if err := s.db.Where("status IN ?", []string{AuthorizationStatusPending, AuthorizationStatusActive}).Order("created_at asc").Find(&accounts).Error; err != nil {
		a.SendCode(500, "station accounts unavailable")
		return
	}
	type accountView struct {
		AccountID       string    `json:"accountId"`
		AccountName     string    `json:"accountName"`
		Role            string    `json:"role"`
		Status          string    `json:"status"`
		Bound           bool      `json:"bound"`
		WebSessionCount int64     `json:"webSessionCount"`
		CreatedAt       time.Time `json:"createdAt"`
	}
	views := make([]accountView, 0, len(accounts))
	for _, account := range accounts {
		var count int64
		_ = s.db.Model(&StationWebSession{}).Where("account_id = ? AND status = ? AND expires_at > ?", account.AccountID, AuthorizationStatusActive, time.Now()).Count(&count).Error
		views = append(views, accountView{AccountID: account.AccountID, AccountName: account.Name, Role: account.Role,
			Status: account.Status, Bound: account.BoundAt != nil, WebSessionCount: count, CreatedAt: account.CreatedAt})
	}
	a.Send(views)
}

func (s *Service) RevokeAuthorization(a *ws.Context) {
	identity, err := s.clientIdentity(a)
	if err != nil || identity.Account.Role != AuthorizationRoleSuper {
		a.SendCode(403, "superadmin authorization required")
		return
	}
	var req struct {
		AccountID string `json:"accountId"`
	}
	if err := a.BindingJson(&req); err != nil || strings.TrimSpace(req.AccountID) == "" {
		a.SendCode(400, "invalid account id")
		return
	}
	if req.AccountID == identity.Account.AccountID {
		a.SendCode(409, "superadmin cannot revoke itself")
		return
	}
	var account StationAccount
	if err := s.db.Where("account_id = ?", req.AccountID).First(&account).Error; err != nil {
		a.SendCode(404, "station account not found")
		return
	}
	if account.Role == AuthorizationRoleSuper {
		a.SendCode(409, "superadmin account cannot be revoked")
		return
	}
	var sessions []StationWebSession
	now := time.Now()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("account_id = ? AND status = ?", account.AccountID, AuthorizationStatusActive).Find(&sessions).Error; err != nil {
			return err
		}
		if err := tx.Model(&StationAccount{}).Where("account_id = ?", account.AccountID).Updates(map[string]any{"status": AuthorizationStatusRevoked, "revoked_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&StationAppCredential{}).Where("account_id = ? AND status = ?", account.AccountID, AuthorizationStatusActive).Updates(map[string]any{"status": AuthorizationStatusRevoked, "revoked_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&StationWebSession{}).Where("account_id = ? AND status = ?", account.AccountID, AuthorizationStatusActive).Updates(map[string]any{"status": AuthorizationStatusRevoked, "revoked_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&StationQrTicket{}).Where("account_id = ? AND status = ?", account.AccountID, AuthorizationStatusPending).Updates(map[string]any{"status": AuthorizationStatusRevoked, "revoked_at": now}).Error
	})
	if err != nil {
		a.SendCode(500, "station account revoke failed")
		return
	}
	s.notifyWebSessionsLoggedOut(sessions)
	a.SendOk()
	s.notifyAuthorizationChanged("accounts")
}

func (s *Service) WebSessions(a *ws.Context) {
	identity, err := s.clientIdentity(a)
	if err != nil || identity.Kind != "app" {
		a.SendCode(403, "station app authorization required")
		return
	}
	var sessions []StationWebSession
	if err := s.db.Where("account_id = ? AND status = ? AND expires_at > ?", identity.Account.AccountID, AuthorizationStatusActive, time.Now()).Order("created_at desc").Find(&sessions).Error; err != nil {
		a.SendCode(500, "station web sessions unavailable")
		return
	}
	a.Send(map[string]any{"count": len(sessions), "sessions": sessions})
}

func (s *Service) LogoutWebSession(a *ws.Context) {
	identity, err := s.clientIdentity(a)
	if err != nil || identity.Kind != "app" {
		a.SendCode(403, "station app authorization required")
		return
	}
	var sessions []StationWebSession
	if err := s.db.Where("account_id = ? AND status = ?", identity.Account.AccountID, AuthorizationStatusActive).Find(&sessions).Error; err != nil {
		a.SendCode(500, "station web sessions unavailable")
		return
	}
	now := time.Now()
	if err := s.db.Model(&StationWebSession{}).Where("account_id = ? AND status = ?", identity.Account.AccountID, AuthorizationStatusActive).Updates(map[string]any{"status": AuthorizationStatusRevoked, "revoked_at": now}).Error; err != nil {
		a.SendCode(500, "station web logout failed")
		return
	}
	s.notifyWebSessionsLoggedOut(sessions)
	a.SendOk()
	s.notifyAuthorizationChanged("sessions")
}

func (s *Service) UnbindApp(a *ws.Context) {
	identity, err := s.clientIdentity(a)
	if err != nil || identity.Kind != "app" {
		a.SendCode(403, "station app authorization required")
		return
	}
	now := time.Now()
	if err := s.db.Model(&StationAppCredential{}).Where("id = ? AND status = ?", identity.CredentialID, AuthorizationStatusActive).
		Updates(map[string]any{"status": AuthorizationStatusRevoked, "revoked_at": now}).Error; err != nil {
		a.SendCode(500, "station app unbind failed")
		return
	}
	a.SendOk()
	s.notifyAuthorizationChanged("credentials")
}

func (s *Service) SetAuthorizationSettings(a *ws.Context) {
	var req struct {
		AccessMode string `json:"accessMode"`
	}
	if err := a.BindingJson(&req); err != nil || (req.AccessMode != StationAccessModePublic && req.AccessMode != StationAccessModeScan) {
		a.SendCode(400, "invalid station access mode")
		return
	}
	hasAdmin, err := s.hasSuperadmin()
	if err != nil {
		a.SendCode(500, "station authorization state unavailable")
		return
	}
	if !hasAdmin || !s.isSuperadminClient(a) {
		a.SendCode(403, "superadmin authorization required")
		return
	}
	if err := s.saveAccessMode(req.AccessMode); err != nil {
		a.SendCode(500, "authorization settings unavailable")
		return
	}
	a.Send(&StationSecuritySettings{ID: 1, AccessMode: req.AccessMode})
	s.notifyAuthorizationChanged("settings")
}

// revokeOwnedWebSession never changes another account's session.
func (s *Service) revokeOwnedWebSession(accountID string, sessionID uint) (*StationWebSession, error) {
	var session StationWebSession
	if err := s.db.Where("id = ? AND account_id = ? AND status = ?", sessionID, accountID, AuthorizationStatusActive).First(&session).Error; err != nil {
		return nil, err
	}
	now := time.Now()
	if err := s.db.Model(&StationWebSession{}).Where("id = ? AND account_id = ? AND status = ?", sessionID, accountID, AuthorizationStatusActive).Updates(map[string]any{"status": AuthorizationStatusRevoked, "revoked_at": now}).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *Service) LogoutOneWebSession(a *ws.Context) {
	identity, err := s.clientIdentity(a)
	if err != nil || identity.Kind != "app" {
		a.SendCode(403, "station app authorization required")
		return
	}
	var req struct {
		SessionID uint `json:"sessionId"`
	}
	if err := a.BindingJson(&req); err != nil || req.SessionID == 0 {
		a.SendCode(400, "invalid session id")
		return
	}
	session, err := s.revokeOwnedWebSession(identity.Account.AccountID, req.SessionID)
	if err != nil {
		a.SendCode(409, "station web session unavailable")
		return
	}
	s.notifyWebSessionsLoggedOut([]StationWebSession{*session})
	a.SendOk()
	s.notifyAuthorizationChanged("sessions")
}
