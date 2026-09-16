package station

import (
	"github.com/wonli/aqi/ws"
	"testing"
)

func TestWebPlaybackControlRequiresSuperadminApp(t *testing.T) {
	s := newTransferTestService(t)
	if s.canControlWebPlayback(&ws.Context{Client: &ws.Client{}}) {
		t.Fatal("anonymous client allowed")
	}
	for _, role := range []string{AuthorizationRoleUser, AuthorizationRoleSuper} {
		account := StationAccount{AccountID: role, Name: role, Role: role, Status: AuthorizationStatusActive}
		if err := s.db.Create(&account).Error; err != nil {
			t.Fatal(err)
		}
		credential := StationAppCredential{AccountID: role, TokenHash: hashStationToken(role), Status: AuthorizationStatusActive}
		if err := s.db.Create(&credential).Error; err != nil {
			t.Fatal(err)
		}
		client := &ws.Client{}
		setClientAppCredential(client, credential.ID)
		if got := s.canControlWebPlayback(&ws.Context{Client: client}); got != (role == AuthorizationRoleSuper) {
			t.Fatalf("unexpected permission for %s: %v", role, got)
		}
		s.db.Model(&credential).Update("status", AuthorizationStatusRevoked)
		if s.canControlWebPlayback(&ws.Context{Client: client}) {
			t.Fatal("revoked credential allowed")
		}
	}
}
