package cmd

import (
	"testing"
	"time"

	"github.com/libtnb/sqlite"
	"github.com/musqca/polemo-station/internal/station"
	"gorm.io/gorm"
)

func TestResetStationSuperadminPreservesUsers(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:reset-superadmin-v2?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&station.StationAccount{}, &station.StationAppCredential{}, &station.StationWebSession{}, &station.StationQrTicket{}, &station.StationSecuritySettings{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	super := station.StationAccount{AccountID: "super", Name: "superadmin", Role: station.AuthorizationRoleSuper, Status: station.AuthorizationStatusActive, BoundAt: &now}
	user := station.StationAccount{AccountID: "user", Name: "user", Role: station.AuthorizationRoleUser, Status: station.AuthorizationStatusActive, BoundAt: &now}
	for _, account := range []*station.StationAccount{&super, &user} {
		if err := database.Create(account).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, credential := range []*station.StationAppCredential{
		{TokenHash: "super-token", AccountID: super.AccountID, Status: station.AuthorizationStatusActive},
		{TokenHash: "user-token", AccountID: user.AccountID, Status: station.AuthorizationStatusActive},
	} {
		if err := database.Create(credential).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Create(&station.StationQrTicket{TokenHash: "pending", Action: station.AuthorizationActionBootstrap, Status: station.AuthorizationStatusPending, ExpiresAt: now.Add(time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&station.StationSecuritySettings{ID: 1, AccessMode: station.StationAccessModeScan, StationID: "station-id"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := resetStationSuperadmin(database); err != nil {
		t.Fatal(err)
	}
	var gotUser station.StationAccount
	if err := database.Where("account_id = ?", user.AccountID).First(&gotUser).Error; err != nil {
		t.Fatal(err)
	}
	if gotUser.Status != station.AuthorizationStatusActive {
		t.Fatalf("user changed: %#v", gotUser)
	}
	var settings station.StationSecuritySettings
	if err := database.First(&settings, 1).Error; err != nil {
		t.Fatal(err)
	}
	if settings.AccessMode != station.StationAccessModePublic || settings.StationID != "station-id" {
		t.Fatalf("unexpected settings: %#v", settings)
	}
	// Reset keeps the revoked audit row, but the replacement superadmin must be
	// allowed to reuse the conventional display name.
	replacement := station.StationAccount{AccountID: "replacement-super", Name: "superadmin", Role: station.AuthorizationRoleSuper, Status: station.AuthorizationStatusPending}
	if err := database.Create(&replacement).Error; err != nil {
		t.Fatalf("superadmin name could not be reused after reset: %v", err)
	}
}
