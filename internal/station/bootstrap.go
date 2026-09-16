package station

import (
	"errors"
	"net/url"
	"time"

	"gorm.io/gorm"
)

// BootstrapAuthorization is the one-time credential used to bind the first
// Station superadmin. Token is only available while the ticket is created.
type BootstrapAuthorization struct {
	Token     string
	Endpoint  string
	StationID string
	ExpiresAt time.Time
	Created   bool
}

func (a BootstrapAuthorization) URI() string {
	values := url.Values{
		"v":         {"2"},
		"endpoint":  {a.Endpoint},
		"ticket":    {a.Token},
		"action":    {AuthorizationActionBootstrap},
		"stationId": {a.StationID},
	}
	return "polemo://station/auth?" + values.Encode()
}

// EnsureBootstrapAuthorization creates the first-superadmin ticket only when
// no active superadmin or pending bootstrap ticket exists. A pending ticket is
// deliberately not reprinted: only its hash is stored in the database.
func (s *Service) EnsureBootstrapAuthorization() (BootstrapAuthorization, error) {
	return s.bootstrapAuthorization(false)
}

// CreateBootstrapAuthorization replaces any pending bootstrap ticket. It is
// used by a local recovery command, where the new secret can be shown safely
// to the administrator who invoked it.
func (s *Service) CreateBootstrapAuthorization() (BootstrapAuthorization, error) {
	return s.bootstrapAuthorization(true)
}

func (s *Service) bootstrapAuthorization(replacePending bool) (BootstrapAuthorization, error) {
	endpoint, err := s.publicEndpointValue()
	if err != nil {
		return BootstrapAuthorization{}, err
	}
	stationID, err := s.stationIdentity()
	if err != nil {
		return BootstrapAuthorization{}, err
	}

	stationAuthorizationMu.Lock()
	defer stationAuthorizationMu.Unlock()
	result := BootstrapAuthorization{Endpoint: endpoint, StationID: stationID}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&StationAccount{}).Where("role = ? AND status = ?", AuthorizationRoleSuper, AuthorizationStatusActive).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			if replacePending {
				return errors.New("station is already initialized")
			}
			return nil
		}

		var pending StationQrTicket
		err := tx.Where("action = ? AND status = ? AND expires_at > ?", AuthorizationActionBootstrap, AuthorizationStatusPending, time.Now()).
			Order("created_at desc").First(&pending).Error
		if err == nil && !replacePending {
			result.ExpiresAt = pending.ExpiresAt
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil {
			if err := tx.Model(&StationQrTicket{}).Where("id = ?", pending.ID).
				Updates(map[string]any{"status": AuthorizationStatusRevoked, "revoked_at": time.Now()}).Error; err != nil {
				return err
			}
		}
		token, ticket, err := s.createTicket(tx, AuthorizationActionBootstrap, "", "Station bootstrap")
		if err != nil {
			return err
		}
		result.Token = token
		result.ExpiresAt = ticket.ExpiresAt
		result.Created = true
		return nil
	})
	return result, err
}
