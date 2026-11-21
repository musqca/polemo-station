package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/wonli/aqi"

	"github.com/libtnb/sqlite"
	"github.com/musqca/polemo-station/internal"
	"github.com/musqca/polemo-station/internal/mdns"
	"github.com/musqca/polemo-station/internal/router"
	"github.com/musqca/polemo-station/internal/station"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"log"
)

func init() {
	rootCmd.AddCommand(api)
}

func publicBaseURLFromLegacyEndpoint(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "ws://") || strings.HasPrefix(value, "wss://") {
		value = strings.TrimSuffix(value, "/ws")
		value = strings.Replace(value, "wss://", "https://", 1)
		value = strings.Replace(value, "ws://", "http://", 1)
	}
	return value
}

func configureStationPublicBaseURL(syncService *station.Service) error {
	publicBaseURL := viper.GetString("stationPublicBaseUrl")
	if publicBaseURL != "" {
		if err := syncService.SetPublicBaseURL(publicBaseURL); err != nil {
			return fmt.Errorf("stationPublicBaseUrl is invalid: %w", err)
		}
		return nil
	}
	if legacyEndpoint := viper.GetString("stationPublicEndpoint"); legacyEndpoint != "" {
		if err := syncService.SetPublicBaseURL(publicBaseURLFromLegacyEndpoint(legacyEndpoint)); err != nil {
			return fmt.Errorf("stationPublicEndpoint is invalid: %w", err)
		}
	}
	return nil
}

var api = &cobra.Command{
	Use:   "api",
	Short: "启动API",
	RunE: func(cmd *cobra.Command, args []string) error {
		app := aqi.Init(
			aqi.ConfigFile(configFile),
			aqi.HttpServer("Api", "port"),
			aqi.DefaultConfig(func(c *aqi.ConfigBuilder) {
				c.Delete("mysql.logic")
				c.Delete("redis.store")

				c.Set("port", 20156)
				c.AfterFields("port", internal.NewAppConfig())
			}),
		)

		databasePath := app.GetDataPath("station/polemo.db")
		if err := os.MkdirAll(filepath.Dir(databasePath), 0755); err != nil {
			return fmt.Errorf("station data directory creation failed: %w", err)
		}
		gormLogger := logger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			logger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  logger.Warn,
				IgnoreRecordNotFoundError: true,
				Colorful:                  true,
			},
		)
		database, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{
			Logger: gormLogger,
		})
		if err != nil {
			return fmt.Errorf("station sqlite database open failed: %w", err)
		}
		syncService := station.NewService(
			database,
			viper.GetString("stationPairingToken"),
			app.GetDataPath("station/library"),
		)
		if err := configureStationPublicBaseURL(syncService); err != nil {
			return err
		}
		syncService.SetFFmpegPath(viper.GetString("stationFFmpegPath"))
		var transcodeProfiles map[string]station.TranscodeProfile
		if err := viper.UnmarshalKey("stationTranscodeProfiles", &transcodeProfiles); err != nil {
			return fmt.Errorf("stationTranscodeProfiles is invalid: %w", err)
		}
		if len(transcodeProfiles) > 0 {
			if err := syncService.SetTranscodeProfiles(transcodeProfiles); err != nil {
				return fmt.Errorf("stationTranscodeProfiles is invalid: %w", err)
			}
		}
		syncService.SetMediaRoots(viper.GetStringSlice("stationMediaRoots"))
		if err := syncService.Migrate(); err != nil {
			return fmt.Errorf("station sync database migration failed: %w", err)
		}
		bootstrap, err := syncService.EnsureBootstrapAuthorization()
		if err != nil {
			return fmt.Errorf("station superadmin initialization failed: %w", err)
		}
		syncService.StartScrapeWorker()

		advertiser, err := mdns.Start(viper.GetInt("port"), map[string]string{
			"id":   viper.GetString("stationId"),
			"name": "Polemo Station",
			"v":    "1",
			"path": "/ws",
		})
		if err != nil {
			cmd.Printf("mDNS discovery disabled: %v\n", err)
		} else {
			defer advertiser.Stop()
			cmd.Printf(
				"mDNS discovery enabled: _polemo._tcp on %s (port %d)\n",
				strings.Join(advertiser.Interfaces(), ", "),
				viper.GetInt("port"),
			)
		}

		g := gin.New()
		g.Use(gin.LoggerWithConfig(gin.LoggerConfig{SkipPaths: []string{station.DiscogsOAuthCallbackPath}}), gin.Recovery())

		router.Api(g, syncService)
		router.Actions(app, syncService)
		syncService.WatchDisconnects()

		app.WithHttpServer(g)
		// Print the bootstrap QR after mDNS and route setup have finished, so it
		// remains at the bottom of the terminal instead of being buried in logs.
		if bootstrap.Created {
			printBootstrapAuthorization(cmd.OutOrStdout(), bootstrap)
		} else if bootstrap.ExpiresAt.After(time.Now()) {
			cmd.Printf("Station is waiting for the existing superadmin authorization ticket to be claimed before %s.\n", bootstrap.ExpiresAt.Local().Format("2006-01-02 15:04:05 MST"))
		}
		app.Start()
		return nil
	},
}
