package station

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wonli/aqi/ws"
	"gorm.io/gorm"
)

type stationAuthorizationRequest struct {
	Action      string `json:"action"`
	AccountID   string `json:"accountId"`
	AccountName string `json:"accountName"`
	ClientName  string `json:"clientName"`
}

var stationAuthorizationMu sync.Mutex

func (s *Service) AuthorizationHandshakeAllowed(r *http.Request) bool {
	return r != nil && r.URL.Query().Get("authorization") == "1"
}

func (s *Service) AuthorizationGuard(a *ws.Context) {
	if strings.HasPrefix(a.Action, "v1.station.authorization.") {
		return
	}
	hasAdmin, err := s.hasSuperadmin()
	if err != nil {
		a.SendCode(500, "station authorization state unavailable")
		a.Abort()
		return
	}
	if !hasAdmin {
		a.SendCode(503, "station superadmin setup required")
		a.Abort()
		return
	}
	if !s.authorizationRequired() {
		return
	}
	identity, err := s.clientIdentity(a)
	if err == nil {
		if identity.Kind == "web" {
			s.trackWebAuthorizationClient(identity.SessionID, a.Client)
		}
		return
	}
	a.SendCode(401, "station authorization required")
	a.Abort()
}

func (s *Service) createTicket(tx *gorm.DB, action, accountID, clientName string) (string, *StationQrTicket, error) {
	token, err := newStationToken()
	if err != nil {
		return "", nil, err
	}
	ticket := &StationQrTicket{TokenHash: hashStationToken(token), Action: action, AccountID: accountID,
		ClientName: strings.TrimSpace(clientName), Status: AuthorizationStatusPending, ExpiresAt: time.Now().Add(stationAuthorizationTTL)}
	if err := tx.Create(ticket).Error; err != nil {
		return "", nil, err
	}
	return token, ticket, nil
}

func (s *Service) findTicket(token string) (*StationQrTicket, error) {
	var ticket StationQrTicket
	err := s.db.Where("token_hash = ?", hashStationToken(strings.TrimSpace(token))).First(&ticket).Error
	return &ticket, err
}

func (s *Service) isSuperadminClient(a *ws.Context) bool {
	identity, err := s.clientIdentity(a)
	return err == nil && identity.Account.Role == AuthorizationRoleSuper
}

func (s *Service) CreateAuthorization(a *ws.Context) {
	var req stationAuthorizationRequest
	if err := a.BindingJson(&req); err != nil {
		a.SendCode(400, "invalid authorization request")
		return
	}
	publicEndpoint, err := s.publicEndpointValue()
	if err != nil {
		a.SendCode(400, "stationPublicBaseUrl must be a configured http:// or https:// URL")
		return
	}
	stationID, err := s.stationIdentity()
	if err != nil {
		a.SendCode(500, "station identity unavailable")
		return
	}
	hasAdmin, err := s.hasSuperadmin()
	if err != nil {
		a.SendCode(500, "station authorization state unavailable")
		return
	}
	action := strings.TrimSpace(req.Action)
	if action == "" {
		switch {
		case !hasAdmin:
			action = AuthorizationActionBootstrap
		case req.AccountID != "" || req.AccountName != "":
			action = AuthorizationActionAccountBind
		default:
			action = AuthorizationActionWebLogin
		}
	}
	if action == AuthorizationActionBootstrap && hasAdmin {
		a.SendCode(409, "station is already initialized")
		return
	}
	if action == AuthorizationActionBootstrap {
		a.SendCode(403, "bootstrap tickets can only be created from the Station command line")
		return
	}
	if action != AuthorizationActionBootstrap && action != AuthorizationActionWebLogin && action != AuthorizationActionAccountBind {
		a.SendCode(400, "invalid authorization action")
		return
	}
	if action == AuthorizationActionAccountBind && !s.isSuperadminClient(a) {
		a.SendCode(403, "superadmin authorization required")
		return
	}

	stationAuthorizationMu.Lock()
	defer stationAuthorizationMu.Unlock()
	accountID := strings.TrimSpace(req.AccountID)
	var token string
	var ticket *StationQrTicket
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if action == AuthorizationActionAccountBind {
			if accountID == "" {
				name := strings.TrimSpace(req.AccountName)
				if len([]rune(name)) < 1 || len([]rune(name)) > 64 {
					return errors.New("account name must contain 1 to 64 characters")
				}
				var count int64
				if err := tx.Model(&StationAccount{}).
					Where("name = ? AND status IN ?", name, []string{AuthorizationStatusPending, AuthorizationStatusActive}).
					Count(&count).Error; err != nil {
					return err
				}
				if count > 0 {
					return errors.New("station account name already exists")
				}
				accountID, err = newStationAccountID()
				if err != nil {
					return err
				}
				if err := tx.Create(&StationAccount{AccountID: accountID, Name: name, Role: AuthorizationRoleUser, Status: AuthorizationStatusPending}).Error; err != nil {
					return err
				}
			} else {
				var account StationAccount
				if err := tx.Where("account_id = ? AND status IN ?", accountID, []string{AuthorizationStatusPending, AuthorizationStatusActive}).First(&account).Error; err != nil {
					return errors.New("station account not found")
				}
				if account.Role == AuthorizationRoleSuper {
					return errors.New("superadmin cannot be rebound from the account list")
				}
			}
			now := time.Now()
			if err := tx.Model(&StationQrTicket{}).Where("action = ? AND account_id = ? AND status = ?", action, accountID, AuthorizationStatusPending).
				Updates(map[string]any{"status": AuthorizationStatusRevoked, "revoked_at": now}).Error; err != nil {
				return err
			}
		}
		token, ticket, err = s.createTicket(tx, action, accountID, req.ClientName)
		return err
	})
	if err != nil {
		a.SendCode(409, err.Error())
		return
	}
	s.authorizationClients.Store(ticket.TokenHash, a.Client)
	a.Send(map[string]any{"token": token, "endpoint": publicEndpoint, "stationId": stationID, "action": action,
		"accountId": accountID, "expiresAt": ticket.ExpiresAt.UnixMilli(), "expiresIn": int(stationAuthorizationTTL.Seconds())})
}

func (s *Service) createAppCredential(tx *gorm.DB, accountID, clientName string) (string, error) {
	token, err := newStationToken()
	if err != nil {
		return "", err
	}
	now := time.Now()
	credential := &StationAppCredential{TokenHash: hashStationToken(token), AccountID: accountID,
		ClientName: strings.TrimSpace(clientName), Status: AuthorizationStatusActive, LastUsedAt: &now}
	return token, tx.Create(credential).Error
}

func (s *Service) ClaimAuthorization(a *ws.Context) {
	stationAuthorizationMu.Lock()
	defer stationAuthorizationMu.Unlock()
	ticket, err := s.findTicket(stationTokenFromContext(a))
	if err != nil {
		a.SendCode(401, "station authorization ticket is invalid")
		return
	}
	var req stationAuthorizationRequest
	_ = a.BindingJson(&req)
	if ticket.Status != AuthorizationStatusPending || !ticket.ExpiresAt.After(time.Now()) ||
		(ticket.Action != AuthorizationActionBootstrap && ticket.Action != AuthorizationActionAccountBind) {
		a.SendCode(401, "station authorization ticket is invalid or expired")
		return
	}
	var account StationAccount
	var accessToken string
	err = s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if ticket.Action == AuthorizationActionBootstrap {
			var count int64
			if err := tx.Model(&StationAccount{}).Where("role = ? AND status = ?", AuthorizationRoleSuper, AuthorizationStatusActive).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return errors.New("station is already initialized")
			}
			accountID, createErr := newStationAccountID()
			if createErr != nil {
				return createErr
			}
			account = StationAccount{AccountID: accountID, Name: "superadmin", Role: AuthorizationRoleSuper, Status: AuthorizationStatusActive, BoundAt: &now}
			if err := tx.Create(&account).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Where("account_id = ? AND status IN ?", ticket.AccountID, []string{AuthorizationStatusPending, AuthorizationStatusActive}).First(&account).Error; err != nil {
				return err
			}
			if err := tx.Model(&StationAppCredential{}).Where("account_id = ? AND status = ?", account.AccountID, AuthorizationStatusActive).
				Updates(map[string]any{"status": AuthorizationStatusRevoked, "revoked_at": now}).Error; err != nil {
				return err
			}
			account.Status = AuthorizationStatusActive
			account.BoundAt = &now
			if err := tx.Save(&account).Error; err != nil {
				return err
			}
		}
		accessToken, err = s.createAppCredential(tx, account.AccountID, req.ClientName)
		if err != nil {
			return err
		}
		ticket.ApprovedAccountID = account.AccountID
		if ticket.Action == AuthorizationActionBootstrap {
			ticket.Status = AuthorizationStatusApproved
		} else {
			ticket.Status = AuthorizationStatusUsed
			ticket.UsedAt = &now
		}
		return tx.Save(ticket).Error
	})
	if err != nil {
		a.SendCode(409, err.Error())
		return
	}
	if identity, identityErr := s.identityForToken(accessToken); identityErr == nil {
		// Authorize the same live socket immediately, so device.hello does not
		// require a reconnect after the one-time ticket is claimed.
		setClientAppCredential(a.Client, identity.CredentialID)
	}
	publicEndpoint, _ := s.publicEndpointValue()
	a.Send(map[string]any{"token": accessToken, "role": account.Role, "accountId": account.AccountID,
		"accountName": account.Name, "endpoint": publicEndpoint, "stationId": mustStationIdentity(s)})
	if ticket.Action == AuthorizationActionBootstrap {
		s.notifyTicketApproved(ticket)
	} else {
		s.notifyAccountCreated(ticket, &account)
	}
	s.notifyAuthorizationChanged("accounts")
}

func (s *Service) ApproveLogin(a *ws.Context) {
	identity, err := s.clientIdentity(a)
	if err != nil || identity.Kind != "app" {
		a.SendCode(403, "station app authorization required")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := a.BindingJson(&req); err != nil || strings.TrimSpace(req.Token) == "" {
		a.SendCode(400, "invalid login token")
		return
	}
	stationAuthorizationMu.Lock()
	defer stationAuthorizationMu.Unlock()
	ticket, err := s.findTicket(req.Token)
	if err != nil || ticket.Action != AuthorizationActionWebLogin || ticket.Status != AuthorizationStatusPending || !ticket.ExpiresAt.After(time.Now()) {
		a.SendCode(401, "station login ticket is invalid or expired")
		return
	}
	ticket.Status = AuthorizationStatusApproved
	ticket.ApprovedAccountID = identity.Account.AccountID
	if err := s.db.Save(ticket).Error; err != nil {
		a.SendCode(500, "station login approval failed")
		return
	}
	s.notifyTicketApproved(ticket)
	a.Send(map[string]any{"accountId": identity.Account.AccountID, "accountName": identity.Account.Name, "role": identity.Account.Role})
}

func (s *Service) notifyTicketApproved(ticket *StationQrTicket) {
	if value, ok := s.authorizationClients.LoadAndDelete(ticket.TokenHash); ok {
		if client, ok := value.(*ws.Client); ok && client != nil {
			client.SendActionMsg(&ws.Action{Action: "station.authorization.approved", Data: map[string]any{"action": ticket.Action}})
		}
	}
}

func (s *Service) notifyAccountCreated(ticket *StationQrTicket, account *StationAccount) {
	if value, ok := s.authorizationClients.LoadAndDelete(ticket.TokenHash); ok {
		if client, ok := value.(*ws.Client); ok && client != nil {
			client.SendActionMsg(&ws.Action{Action: "station.authorization.account.created", Data: account})
		}
	}
}
