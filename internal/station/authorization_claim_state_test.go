package station

import (
	"net/http"
	"testing"
	"time"

	"github.com/wonli/aqi/ws"
)

// Exercise the actual claim and state handlers on the same connection. Its
// upgrade header retains the one-time ticket even after claim issues a token.
func TestClaimThenAuthorizationStateOnSameSocket(t *testing.T) {
	for _, action := range []string{AuthorizationActionBootstrap, AuthorizationActionAccountBind} {
		t.Run(action, func(t *testing.T) {
			s := newTransferTestService(t)
			if err := s.saveAccessMode(StationAccessModeScan); err != nil {
				t.Fatal(err)
			}
			old := StationAccount{AccountID: "old-super", Name: "superadmin", Role: AuthorizationRoleSuper, Status: AuthorizationStatusRevoked}
			if err := s.db.Create(&old).Error; err != nil {
				t.Fatal(err)
			}
			accountID := ""
			expectedRole := AuthorizationRoleSuper
			if action == AuthorizationActionAccountBind {
				accountID = "new-user"
				expectedRole = AuthorizationRoleUser
				if err := s.db.Create(&StationAccount{AccountID: accountID, Name: "Listener", Role: expectedRole, Status: AuthorizationStatusPending}).Error; err != nil {
					t.Fatal(err)
				}
			}
			ticketToken, _, err := s.createTicket(s.db, action, accountID, "Browser")
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequest(http.MethodGet, "http://station.local/ws?authorization=1", nil)
			request.Header.Set("Authorization", "Bearer "+ticketToken)
			client := &ws.Client{HttpRequest: request, Send: make(chan []byte, 10)}
			claim := &ws.Context{Client: client, Action: "v1.station.authorization.claim", Params: `{}`}
			s.ClaimAuthorization(claim)
			if claim.Response == nil || claim.Response.Code != 0 {
				t.Fatalf("claim failed: %#v", claim.Response)
			}
			data := claim.Response.Data.(map[string]any)
			token := data["token"].(string)
			if token == "" {
				t.Fatal("no credential issued")
			}

			stateContext := &ws.Context{Client: client, Action: "v1.station.authorization.state"}
			s.AuthorizationState(stateContext)
			state := stateContext.Response.Data.(map[string]any)
			if state["authenticated"] != true || state["role"] != expectedRole || state["accountId"] != data["accountId"] {
				t.Fatalf("claimed socket is not authenticated: %#v", state)
			}
			if state["superadmin"] != (expectedRole == AuthorizationRoleSuper) {
				t.Fatalf("wrong admin flag: %#v", state)
			}
			// An HTTP request with the original ticket must still be unauthenticated.
			httpState, err := s.AuthorizationHTTPState(request)
			if err != nil || httpState["authenticated"] != false {
				t.Fatalf("ticket accepted as HTTP credential: %#v, %v", httpState, err)
			}
			requestWithToken, _ := http.NewRequest(http.MethodGet, "http://station.local/api/station/authorization/state", nil)
			requestWithToken.Header.Set("Authorization", "Bearer "+token)
			httpState, err = s.AuthorizationHTTPState(requestWithToken)
			if err != nil || httpState["authenticated"] != true {
				t.Fatalf("new token rejected: %#v, %v", httpState, err)
			}

			// Revocation must invalidate the cached credential ID on the next query.
			if err := s.db.Model(&StationAppCredential{}).Where("token_hash = ?", hashStationToken(token)).Updates(map[string]any{"status": AuthorizationStatusRevoked, "revoked_at": time.Now()}).Error; err != nil {
				t.Fatal(err)
			}
			s.AuthorizationState(stateContext)
			state = stateContext.Response.Data.(map[string]any)
			if state["authenticated"] != false || state["superadmin"] != false {
				t.Fatalf("revoked socket stayed authorized: %#v", state)
			}
		})
	}
}
