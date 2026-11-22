package router

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/musqca/polemo-station/internal/middlewares"
	"github.com/musqca/polemo-station/internal/station"
	"github.com/wonli/aqi/ws"
	"gorm.io/gorm"
)

func Api(g *gin.Engine, syncService *station.Service) {
	g.Use(middlewares.GinCORS())
	Frontend(g)
	g.GET("/station-config.js", func(c *gin.Context) {
		script := `window.__POLEMO_STATION_CONFIG__ = {
	mountPath: (() => {
		const source = document.currentScript?.src;
		if (!source) return undefined;
		const pathname = new URL(source, location.href).pathname;
		if (!pathname.endsWith('/station-config.js')) return undefined;
		return pathname.slice(0, -'/station-config.js'.length).replace(/\/+$/, '');
	})()
};`
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "application/javascript; charset=utf-8", []byte(script))
	})

	g.GET("/ws", func(c *gin.Context) {
		if !station.SameOriginRequest(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if !syncService.Authorized(c.Request) && !syncService.AuthorizationHandshakeAllowed(c.Request) {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		ws.HttpHandler(c.Writer, c.Request)
	})
	g.GET("/api/station/authorization/state", func(c *gin.Context) {
		state, err := syncService.AuthorizationHTTPState(c.Request)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "station_authorization_state_unavailable"})
			return
		}
		syncService.RefreshWebSession(c.Writer, c.Request)
		c.JSON(http.StatusOK, state)
	})
	g.POST("/api/station/authorization/exchange", func(c *gin.Context) {
		if !station.SameOriginRequest(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		var request struct {
			Ticket string `json:"ticket"`
		}
		if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.Ticket) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "authorization_ticket_required"})
			return
		}
		token, err := syncService.ExchangeWebTicket(request.Ticket)
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "authorization_ticket_not_approved"})
			return
		}
		station.SetStationWebCookie(c.Writer, c.Request, token, 30*24*60*60)
		c.JSON(http.StatusOK, gin.H{"authenticated": true})
	})
	g.POST("/api/station/authorization/logout", func(c *gin.Context) {
		if !station.SameOriginRequest(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		_ = syncService.LogoutCurrentWebSession(c.Request)
		station.SetStationWebCookie(c.Writer, c.Request, "", -1)
		c.JSON(http.StatusOK, gin.H{"authenticated": false})
	})
	stationAPI := g.Group("/api/station")
	stationAPI.Use(func(c *gin.Context) {
		if !syncService.Initialized() {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		if !syncService.Authorized(c.Request) {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	})
	registerDiscogsOAuth(g, stationAPI, syncService)
	stationAPI.GET("/scrape/settings", func(c *gin.Context) {
		if !syncService.SuperadminAuthorized(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		view, err := syncService.ScrapeSettingsView()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scrape_settings_unavailable"})
			return
		}
		c.JSON(http.StatusOK, view)
	})
	stationAPI.PUT("/scrape/providers/:provider", func(c *gin.Context) {
		if !syncService.SuperadminAuthorized(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		var request struct {
			Enabled bool    `json:"enabled"`
			APIKey  *string `json:"apiKey"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_scrape_provider"})
			return
		}
		if err := syncService.SaveScrapeProvider(c.Param("provider"), request.Enabled, request.APIKey); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scrape_provider_save_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"saved": true})
	})
	stationAPI.POST("/scrape/providers/:provider/test", func(c *gin.Context) {
		if !syncService.SuperadminAuthorized(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if err := syncService.TestScrapeProvider(c.Param("provider")); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"ok": false, "error": "scrape_provider_test_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	stationAPI.POST("/scrape/providers/:provider/process", func(c *gin.Context) {
		if !syncService.SuperadminAuthorized(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		runID, count, err := syncService.StartScrapeProvider(c.Param("provider"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scrape_provider_reprocess_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"accepted": true, "count": count, "runId": runID})
	})
	stationAPI.POST("/scrape/providers/:provider/stop", func(c *gin.Context) {
		if !syncService.SuperadminAuthorized(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		count, err := syncService.StopScrapeProvider(c.Param("provider"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scrape_provider_stop_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"stopped": true, "count": count})
	})
	stationAPI.PUT("/scrape/settings", func(c *gin.Context) {
		if !syncService.SuperadminAuthorized(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		var settings station.ScrapeSettings
		if err := c.ShouldBindJSON(&settings); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_scrape_settings"})
			return
		}
		if err := syncService.SaveScrapeSettings(settings); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scrape_settings_save_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, settings)
	})
	stationAPI.GET("/scrape/items", func(c *gin.Context) {
		if !syncService.SuperadminAuthorized(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		items, err := syncService.ListScrapeItems()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scrape_items_unavailable"})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
	stationAPI.GET("/scrape/stats", func(c *gin.Context) {
		if !syncService.SuperadminAuthorized(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		stats, err := syncService.ScrapeDailyStats()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scrape_stats_unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"stats": stats})
	})
	stationAPI.GET("/scrape/stats/:provider/:kind/songs", func(c *gin.Context) {
		if !syncService.SuperadminAuthorized(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		items, err := syncService.ScrapeDailyStatSongs(c.Param("provider"), c.Param("kind"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scrape_stat_songs_unavailable", "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
	stationAPI.POST("/scrape", func(c *gin.Context) {
		if !syncService.SuperadminAuthorized(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		var request struct {
			ServerFileIDs []string `json:"serverFileIds"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_scrape_request"})
			return
		}
		count, err := syncService.EnqueueScrape(request.ServerFileIDs)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scrape_enqueue_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"accepted": true, "count": count})
	})
	stationAPI.POST("/scrape/items/:id/retry", func(c *gin.Context) {
		if !syncService.SuperadminAuthorized(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_scrape_item"})
			return
		}
		if err := syncService.RetryScrapeItem(uint(id)); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "scrape_retry_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"accepted": true})
	})
	stationAPI.GET("/devices", func(c *gin.Context) {
		devices, err := syncService.ListDevices()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "devices_unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"devices": devices})
	})
	stationAPI.DELETE("/devices/:deviceId", func(c *gin.Context) {
		var request struct {
			Confirmation string `json:"confirmation"`
		}
		if err := c.ShouldBindJSON(&request); err != nil || (request.Confirmation != "删除" && request.Confirmation != "DELETE") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "device_delete_confirmation_required"})
			return
		}
		if err := syncService.DeleteDevice(c.Param("deviceId")); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "device_not_found"})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"error": "device_delete_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": true})
	})
	stationAPI.GET("/devices/:deviceId/files", func(c *gin.Context) {
		tracks, err := syncService.ListTracks(c.Param("deviceId"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "files_unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"files": tracks})
	})
	stationAPI.POST("/devices/:deviceId/files/:fileId/upload", func(c *gin.Context) {
		if err := syncService.RequestDeviceUpload(c.Param("deviceId"), c.Param("fileId")); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "upload_unavailable"})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"accepted": true})
	})
	stationAPI.POST("/devices/:deviceId/files/:fileId/download", func(c *gin.Context) {
		if err := syncService.RequestDeviceDownload(c.Param("deviceId"), c.Param("fileId")); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "download_unavailable", "message": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"accepted": true})
	})
	stationAPI.GET("/transfers", func(c *gin.Context) {
		tasks, err := syncService.ListTransfers()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "transfers_unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"transfers": tasks})
	})
	stationAPI.POST("/transfers/:taskId/retry", func(c *gin.Context) {
		if err := syncService.RetryTransfer(c.Param("taskId")); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "transfer_retry_unavailable", "message": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"accepted": true})
	})
	stationAPI.POST("/transfers/:taskId/cancel", func(c *gin.Context) {
		if err := syncService.CancelTransfer(c.Param("taskId")); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "transfer_cancel_unavailable", "message": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"accepted": true})
	})
	stationAPI.Any("/transfers/:taskId/content", func(c *gin.Context) {
		if c.Request.Method == http.MethodPut {
			syncService.UploadHTTP(c.Writer, c.Request, c.Param("taskId"))
			return
		}
		if c.Request.Method == http.MethodGet {
			syncService.DownloadHTTP(c.Writer, c.Request, c.Param("taskId"))
			return
		}
		c.Status(http.StatusMethodNotAllowed)
	})
	stationAPI.GET("/files/:serverFileId", func(c *gin.Context) {
		file, err := syncService.GetServerFile(c.Param("serverFileId"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "server_file_not_found"})
			return
		}
		c.JSON(http.StatusOK, file)
	})
	stationAPI.GET("/library", func(c *gin.Context) {
		files, err := syncService.ListServerFiles()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "library_unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"files": files})
	})
	stationAPI.POST("/files/upload", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<30)
		fileHeader, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "upload_file_required"})
			return
		}
		file, err := fileHeader.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "upload_file_unavailable"})
			return
		}
		defer file.Close()
		imported, err := syncService.ImportWebUpload(fileHeader.Filename, file)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "upload_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, imported)
	})
	stationAPI.GET("/media/roots", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"roots": syncService.MediaRoots()})
	})
	stationAPI.GET("/media/browse", func(c *gin.Context) {
		result, err := syncService.BrowseMediaDirectory(c.Query("path"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "media_browse_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	})
	stationAPI.GET("/media/sources", func(c *gin.Context) {
		sources, err := syncService.ListMediaSources()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "media_sources_unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"sources": sources})
	})
	stationAPI.POST("/media/sources", func(c *gin.Context) {
		var request station.CreateMediaSourceRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_media_source"})
			return
		}
		source, err := syncService.CreateMediaSource(request)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "media_source_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, source)
	})
	stationAPI.POST("/media/sources/:sourceId/scan", func(c *gin.Context) {
		if err := syncService.StartMediaSourceScan(c.Param("sourceId")); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "media_source_not_found"})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"accepted": true})
	})
	stationAPI.PUT("/media/sources/:sourceId", func(c *gin.Context) {
		var request station.CreateMediaSourceRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_media_source"})
			return
		}
		source, err := syncService.UpdateMediaSource(c.Param("sourceId"), request)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "media_source_update_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, source)
	})
	stationAPI.DELETE("/media/sources/:sourceId", func(c *gin.Context) {
		if err := syncService.RemoveMediaSource(c.Param("sourceId")); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "media_source_not_found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"removed": true})
	})
	stationAPI.POST("/sync-offers", func(c *gin.Context) {
		var request station.CreateSyncOfferRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_sync_selection"})
			return
		}
		offers, err := syncService.CreateSyncOffers(request)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "sync_offer_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"offers": offers})
	})
	stationAPI.GET("/files/:serverFileId/stream", func(c *gin.Context) {
		fileView, err := syncService.GetServerFile(c.Param("serverFileId"))
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		file, err := syncService.OpenAudio(c.Param("serverFileId"))
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		defer file.Close()
		c.Header("Content-Type", station.WebMIMEForFormat(fileView.Format))
		info, err := file.Stat()
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
	})
	stationAPI.GET("/files/:serverFileId/playback", func(c *gin.Context) {
		decision, err := syncService.ResolveWebPlayback(c.Param("serverFileId"))
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.Status(http.StatusNotFound)
				return
			}
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "transcode_unavailable", "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, decision)
	})
	stationAPI.GET("/files/:serverFileId/transcode/:profile", func(c *gin.Context) {
		// A browser can cancel and recreate a Range request while the first
		// rendition is being generated. Keep the cache-producing FFmpeg job
		// alive so a client-side retry does not leave us with no cache at all.
		transcodeContext, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Minute)
		defer cancel()
		file, err := syncService.OpenWebTranscode(transcodeContext, c.Param("serverFileId"), c.Param("profile"))
		if err != nil {
			if errors.Is(err, station.ErrTranscodeBusy) {
				c.Header("Retry-After", "2")
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "transcode_busy"})
				return
			}
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "transcode_failed", "message": err.Error()})
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Header("Content-Type", "audio/mp4")
		// The profile URL is stable while the server-side profile parameters may
		// change. Require clients to revalidate instead of pinning old audio for a
		// year under immutable caching.
		c.Header("Cache-Control", "private, no-cache")
		http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
	})
	stationAPI.GET("/files/:serverFileId/resources", func(c *gin.Context) {
		resources, err := syncService.ListResources(c.Param("serverFileId"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "resources_unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"resources": resources})
	})
	stationAPI.GET("/files/:serverFileId/cover", func(c *gin.Context) {
		cover, contentType, err := syncService.OpenCover(c.Param("serverFileId"))
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		defer cover.Close()
		c.Header("Content-Type", contentType)
		c.Header("Cache-Control", "private, max-age=31536000, immutable")
		_, _ = io.Copy(c.Writer, cover)
	})
	stationAPI.GET("/resources/:resourceId", func(c *gin.Context) {
		cover, contentType, err := syncService.OpenCover(c.Param("resourceId"))
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		defer cover.Close()
		c.Header("Content-Type", contentType)
		c.Header("Cache-Control", "private, max-age=31536000, immutable")
		_, _ = io.Copy(c.Writer, cover)
	})
	stationAPI.POST("/resources/:resourceId/activate", func(c *gin.Context) {
		file, err := syncService.ActivateResource(c.Param("resourceId"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "resource_not_found"})
			return
		}
		c.JSON(http.StatusOK, file)
	})
	stationAPI.DELETE("/resources/:resourceId", func(c *gin.Context) {
		file, err := syncService.DeleteResource(c.Param("resourceId"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "resource_not_found"})
			return
		}
		c.JSON(http.StatusOK, file)
	})
	stationAPI.POST("/resources/:resourceId/delete", func(c *gin.Context) {
		file, err := syncService.DeleteResource(c.Param("resourceId"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "resource_not_found"})
			return
		}
		c.JSON(http.StatusOK, file)
	})
	stationAPI.POST("/files/:serverFileId/metadata", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 13<<20)
		if err := c.Request.ParseMultipartForm(12 << 20); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_metadata_form"})
			return
		}
		trackNumber, _ := strconv.Atoi(c.PostForm("track"))
		discNumber, _ := strconv.Atoi(c.PostForm("disc"))
		update := station.MetadataUpdate{
			Title: c.PostForm("title"), Artist: c.PostForm("artist"), Album: c.PostForm("album"),
			AlbumArtist: c.PostForm("albumArtist"), Lyrics: c.PostForm("lyrics"),
			KeepLyrics:  c.PostForm("keepLyrics") == "true",
			TrackNumber: trackNumber, DiscNumber: discNumber, RemoveCover: c.PostForm("removeCover") == "true",
		}
		cover, header, err := c.Request.FormFile("cover")
		if err == nil {
			defer cover.Close()
			update.CoverMIME = station.CoverMIME(header.Header.Get("Content-Type"))
			update.Cover, err = io.ReadAll(io.LimitReader(cover, (10<<20)+1))
			if update.CoverMIME != "image/jpeg" && update.CoverMIME != "image/png" && len(update.Cover) > 0 {
				update.CoverMIME = station.CoverMIME(http.DetectContentType(update.Cover))
			}
		}
		if err != nil && !errors.Is(err, http.ErrMissingFile) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_cover"})
			return
		}
		file, err := syncService.UpdateServerFileMetadata(c.Param("serverFileId"), update)
		switch {
		case errors.Is(err, os.ErrNotExist):
			c.JSON(http.StatusNotFound, gin.H{"error": "server_file_not_found"})
		case err != nil:
			c.JSON(http.StatusBadRequest, gin.H{"error": "metadata_update_failed", "message": err.Error()})
		default:
			c.JSON(http.StatusOK, file)
		}
	})
	stationAPI.GET("/stats", func(c *gin.Context) {
		stats, err := syncService.Stats()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "stats_unavailable"})
			return
		}
		c.JSON(http.StatusOK, stats)
	})
}
