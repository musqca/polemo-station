package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/musqca/polemo-station/internal/station"
)

func TestDiscogsOAuthCallbackRedirectsUnboundRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Api(engine, station.NewService(nil, "", t.TempDir()))
	request := httptest.NewRequest(http.MethodGet, station.DiscogsOAuthCallbackPath+"?oauth_token=fixture&oauth_verifier=unbound", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/en-US/tasks?discogsOAuth=failed#metadata" {
		t.Fatalf("unexpected callback response: status=%d location=%s", response.Code, response.Header().Get("Location"))
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("callback is missing cache or referrer protection")
	}
}
