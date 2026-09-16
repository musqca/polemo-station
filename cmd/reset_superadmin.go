package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/libtnb/sqlite"
	"github.com/spf13/cobra"
	"github.com/wonli/aqi"
	"github.com/musqca/polemo-station/internal/station"
	"gorm.io/gorm"
)

func init() {
	rootCmd.AddCommand(resetSuperadminCmd)
}

var resetSuperadminCmd = &cobra.Command{
	Use:   "reset-superadmin",
	Short: "重置 Station 超管授权",
	RunE: func(cmd *cobra.Command, args []string) error {
		app := aqi.Init(aqi.ConfigFile(configFile))
		databasePath := app.GetDataPath(filepath.Join("station", "polemo.db"))
		if _, err := os.Stat(databasePath); err != nil {
			return fmt.Errorf("station database not found: %w", err)
		}
		database, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
		if err != nil {
			return fmt.Errorf("station database open failed: %w", err)
		}
		if err := database.AutoMigrate(&station.StationAccount{}, &station.StationAppCredential{}, &station.StationWebSession{}, &station.StationQrTicket{}, &station.StationSecuritySettings{}); err != nil {
			return fmt.Errorf("station authorization migration failed: %w", err)
		}
		result, err := resetStationSuperadmin(database)
		if err != nil {
			return err
		}
		cmd.Printf("Station superadmin reset complete; revoked %d authorization(s) and restored public access. Restart Station to display a new superadmin QR code.\n", result.revokedAuthorizations)
		return nil
	},
}

type superadminResetResult struct {
	revokedAuthorizations int64
}

func resetStationSuperadmin(database *gorm.DB) (superadminResetResult, error) {
	var reset superadminResetResult
	err := database.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		var superadmins []station.StationAccount
		if err := tx.Where("role = ? AND status = ?", station.AuthorizationRoleSuper, station.AuthorizationStatusActive).Find(&superadmins).Error; err != nil {
			return fmt.Errorf("superadmin lookup failed: %w", err)
		}
		for _, account := range superadmins {
			for _, model := range []any{&station.StationAppCredential{}, &station.StationWebSession{}} {
				result := tx.Model(model).Where("account_id = ? AND status = ?", account.AccountID, station.AuthorizationStatusActive).
					Updates(map[string]any{"status": station.AuthorizationStatusRevoked, "revoked_at": now})
				if result.Error != nil {
					return fmt.Errorf("superadmin credential reset failed: %w", result.Error)
				}
				reset.revokedAuthorizations += result.RowsAffected
			}
			result := tx.Model(&station.StationAccount{}).Where("account_id = ?", account.AccountID).
				Updates(map[string]any{"status": station.AuthorizationStatusRevoked, "revoked_at": now})
			if result.Error != nil {
				return fmt.Errorf("superadmin reset failed: %w", result.Error)
			}
			reset.revokedAuthorizations += result.RowsAffected
		}
		tickets := tx.Model(&station.StationQrTicket{}).Where("status IN ?", []string{station.AuthorizationStatusPending, station.AuthorizationStatusApproved}).
			Updates(map[string]any{"status": station.AuthorizationStatusRevoked, "revoked_at": now})
		if tickets.Error != nil {
			return fmt.Errorf("authorization ticket reset failed: %w", tickets.Error)
		}
		reset.revokedAuthorizations += tickets.RowsAffected

		var settings station.StationSecuritySettings
		_ = tx.First(&settings, 1).Error
		settings.ID = 1
		settings.AccessMode = station.StationAccessModePublic
		if err := tx.Save(&settings).Error; err != nil {
			return fmt.Errorf("station access mode reset failed: %w", err)
		}
		return nil
	})
	return reset, err
}
