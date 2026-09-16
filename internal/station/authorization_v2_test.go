package station

import (
	"net/http"
	"testing"
	"time"

	"github.com/wonli/aqi/ws"
)

func TestAuthorizationHTTPStateUsesSeparatedCredential(t *testing.T) {
	service := newTransferTestService(t)
	request, _ := http.NewRequest(http.MethodGet, "http://station.local/api/station/authorization/state", nil)
	state, err := service.AuthorizationHTTPState(request)
	if err != nil {
		t.Fatal(err)
	}
	if state["accessMode"] != StationAccessModePublic || state["authenticated"] != false {
		t.Fatalf("unexpected public state: %#v", state)
	}
	now := time.Now()
	account := StationAccount{AccountID: "superadmin", Name: "superadmin", Role: AuthorizationRoleSuper, Status: AuthorizationStatusActive, BoundAt: &now}
	if err := service.db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	token := "active-test-token"
	if err := service.db.Create(&StationAppCredential{TokenHash: hashStationToken(token), AccountID: account.AccountID, Status: AuthorizationStatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.saveAccessMode(StationAccessModeScan); err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	state, err = service.AuthorizationHTTPState(request)
	if err != nil {
		t.Fatal(err)
	}
	if state["authenticated"] != true || state["superadmin"] != true {
		t.Fatalf("unexpected authenticated state: %#v", state)
	}
}

func TestPublicStationAuthorizesRemoteClients(t *testing.T) {
	service := newTransferTestService(t)
	request, _ := http.NewRequest(http.MethodPost, "https://station.example/api/station/files/file-1/metadata", nil)
	request.RemoteAddr = "203.0.113.7:443"
	if !service.Authorized(request) {
		t.Fatal("public Station rejected a remote metadata request")
	}
	if err := service.saveAccessMode(StationAccessModeScan); err != nil {
		t.Fatal(err)
	}
	if service.Authorized(request) {
		t.Fatal("scan-only Station accepted an unauthenticated remote request")
	}
}

func TestBootstrapAuthorizationIsCreatedLocallyAndPreservesAccessMode(t *testing.T) {
	service := newTransferTestService(t)
	if err := service.SetPublicBaseURL("https://station.example"); err != nil {
		t.Fatal(err)
	}
	first, err := service.EnsureBootstrapAuthorization()
	if err != nil || !first.Created || first.Token == "" {
		t.Fatalf("first bootstrap ticket = %#v, %v", first, err)
	}
	if first.URI() == "" || first.Endpoint != "wss://station.example/ws" {
		t.Fatalf("invalid bootstrap authorization: %#v", first)
	}
	second, err := service.EnsureBootstrapAuthorization()
	if err != nil || second.Created || !second.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("existing bootstrap ticket was replaced: %#v, %v", second, err)
	}
	replacement, err := service.CreateBootstrapAuthorization()
	if err != nil || !replacement.Created || replacement.Token == first.Token {
		t.Fatalf("replacement bootstrap ticket = %#v, %v", replacement, err)
	}
	if service.accessMode() != StationAccessModePublic {
		t.Fatalf("bootstrap creation changed public access: %q", service.accessMode())
	}
	now := time.Now()
	if err := service.db.Create(&StationAccount{AccountID: "super", Name: "superadmin", Role: AuthorizationRoleSuper, Status: AuthorizationStatusActive, BoundAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	ready, err := service.EnsureBootstrapAuthorization()
	if err != nil || ready.Created || ready.Token != "" || !service.Initialized() {
		t.Fatalf("initialized Station created another bootstrap ticket: %#v, %v", ready, err)
	}
}

func TestWebTicketCanOnlyBeExchangedOnce(t *testing.T) {
	service := newTransferTestService(t)
	now := time.Now()
	account := StationAccount{AccountID: "account-1", Name: "listener", Role: AuthorizationRoleUser, Status: AuthorizationStatusActive, BoundAt: &now}
	if err := service.db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	ticketToken := "ticket"
	ticket := StationQrTicket{TokenHash: hashStationToken(ticketToken), Action: AuthorizationActionWebLogin,
		ApprovedAccountID: account.AccountID, Status: AuthorizationStatusApproved, ExpiresAt: now.Add(time.Minute)}
	if err := service.db.Create(&ticket).Error; err != nil {
		t.Fatal(err)
	}
	if token, err := service.ExchangeWebTicket(ticketToken); err != nil || token == "" {
		t.Fatalf("first exchange failed: %q %v", token, err)
	}
	if _, err := service.ExchangeWebTicket(ticketToken); err == nil {
		t.Fatal("second exchange unexpectedly succeeded")
	}
}

func TestClientIdentityUsesCredentialIssuedOnSameSocket(t *testing.T) {
	service := newTransferTestService(t)
	now := time.Now()
	account := StationAccount{AccountID: "account-live", Name: "listener", Role: AuthorizationRoleUser, Status: AuthorizationStatusActive, BoundAt: &now}
	if err := service.db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	credential := StationAppCredential{TokenHash: hashStationToken("new-token"), AccountID: account.AccountID, Status: AuthorizationStatusActive}
	if err := service.db.Create(&credential).Error; err != nil {
		t.Fatal(err)
	}
	client := &ws.Client{}
	setClientAppCredential(client, credential.ID)
	identity, err := service.clientIdentity(&ws.Context{Client: client})
	if err != nil {
		t.Fatalf("same-socket credential was rejected: %v", err)
	}
	if identity.Kind != "app" || identity.Account.AccountID != account.AccountID {
		t.Fatalf("unexpected identity: %#v", identity)
	}
}

func TestSameOriginRequest(t *testing.T) {
	request, _ := http.NewRequest(http.MethodPost, "https://station.local/api/station/authorization/logout", nil)
	request.Host = "station.local"
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("Origin", "https://station.local")
	if !SameOriginRequest(request) {
		t.Fatal("same origin was rejected")
	}
	request.Header.Set("Origin", "https://evil.example")
	if SameOriginRequest(request) {
		t.Fatal("cross origin was accepted")
	}
	request.Header.Set("Origin", "http://station.local")
	if SameOriginRequest(request) {
		t.Fatal("cross-scheme origin was accepted")
	}
}
