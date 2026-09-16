package station

import (
	"testing"
	"time"
)

func TestRevokeOwnedWebSession(t *testing.T) {
	s := newTransferTestService(t)
	sessions := []StationWebSession{
		{AccountID: "a", TokenHash: "one", Status: AuthorizationStatusActive, ExpiresAt: time.Now().Add(time.Hour)},
		{AccountID: "a", TokenHash: "two", Status: AuthorizationStatusActive, ExpiresAt: time.Now().Add(time.Hour)},
		{AccountID: "b", TokenHash: "three", Status: AuthorizationStatusActive, ExpiresAt: time.Now().Add(time.Hour)},
	}
	if err := s.db.Create(&sessions).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.revokeOwnedWebSession("a", sessions[2].ID); err == nil {
		t.Fatal("revoked another account's session")
	}
	if _, err := s.revokeOwnedWebSession("a", sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	for i, session := range sessions {
		var actual StationWebSession
		if err := s.db.First(&actual, session.ID).Error; err != nil {
			t.Fatal(err)
		}
		expected := AuthorizationStatusActive
		if i == 0 {
			expected = AuthorizationStatusRevoked
		}
		if actual.Status != expected {
			t.Fatalf("session %d: got %s", i, actual.Status)
		}
	}
}
