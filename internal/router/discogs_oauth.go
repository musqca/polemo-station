package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/musqca/polemo-station/internal/station"
)

func registerDiscogsOAuth(g *gin.Engine, stationAPI *gin.RouterGroup, service *station.Service) {
	const path = "/scrape/providers/discogs/oauth"
	oauth := stationAPI.Group(path, func(c *gin.Context) {
		if !station.SameOriginRequest(c.Request) || !service.SuperadminAuthorized(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Next()
	})
	oauth.GET("", func(c *gin.Context) {
		callback, err := service.DiscogsOAuthCallbackURL(c.Request)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"callbackUrl": callback})
	})
	oauth.POST("", func(c *gin.Context) {
		var payload struct {
			ConsumerKey    string `json:"consumerKey"`
			ConsumerSecret string `json:"consumerSecret"`
			Language       string `json:"language"`
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_discogs_oauth_request"})
			return
		}
		authorize, err := service.StartDiscogsOAuth(c.Writer, c.Request, payload.ConsumerKey, payload.ConsumerSecret, payload.Language)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "discogs_oauth_start_failed", "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"authorizeUrl": authorize})
	})
	oauth.DELETE("", func(c *gin.Context) {
		if err := service.DisconnectDiscogsOAuth(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "discogs_oauth_disconnect_failed"})
			return
		}
		c.Status(http.StatusNoContent)
	})
	// Discogs redirects across sites, so the callback verifies its separate
	// one-use cookie and original administrator session inside the service.
	g.GET(station.DiscogsOAuthCallbackPath, func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Referrer-Policy", "no-referrer")
		c.Redirect(http.StatusSeeOther, service.FinishDiscogsOAuth(c.Writer, c.Request))
	})
}
