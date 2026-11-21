package router

import (
	"io/fs"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/musqca/polemo-station/internal/frontend"
)

func TestFrontendRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Frontend(engine)
	request := httptest.NewRequest("GET", "/", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "<!doctype html>") || !strings.Contains(recorder.Body.String(), `href="./_app/`) {
		t.Fatalf("root response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestFrontendCleanRoute(t *testing.T) {
	if _, err := fs.Stat(frontend.Assets, "build/en-US/device.html"); err != nil {
		t.Skip("full frontend build is not embedded; run make pkg")
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Frontend(engine)
	request := httptest.NewRequest("GET", "/en-US/device", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "<!doctype html>") || !strings.Contains(recorder.Body.String(), `href="../logo.png"`) {
		t.Fatalf("clean route response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestFrontendDirectoryRouteRedirectsToTrailingSlash(t *testing.T) {
	if _, err := fs.Stat(frontend.Assets, "build/en-US/index.html"); err != nil {
		t.Skip("full frontend build is not embedded; run make pkg")
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Frontend(engine)
	request := httptest.NewRequest("GET", "/en-US", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != 308 || recorder.Header().Get("Location") != "en-US/" {
		t.Fatalf("directory redirect = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestFrontendDirectoryRedirectPreservesProxyPrefix(t *testing.T) {
	if _, err := fs.Stat(frontend.Assets, "build/en-US/index.html"); err != nil {
		t.Skip("full frontend build is not embedded; run make pkg")
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Frontend(engine)
	request := httptest.NewRequest("GET", "/en-US?lang=zh-CN", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://station.example/v1/en-US")
	if recorder.Code != 308 || base.ResolveReference(location).String() != "https://station.example/v1/en-US/?lang=zh-CN" {
		t.Fatalf("prefixed directory redirect = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestFrontendConcreteRouteRedirectsAwayFromTrailingSlash(t *testing.T) {
	if _, err := fs.Stat(frontend.Assets, "build/en-US/device.html"); err != nil {
		t.Skip("full frontend build is not embedded; run make pkg")
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Frontend(engine)
	request := httptest.NewRequest("GET", "/en-US/device/", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != 308 || recorder.Header().Get("Location") != "../device" {
		t.Fatalf("concrete route redirect = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestFrontendUnknownAssetIsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Frontend(engine)
	request := httptest.NewRequest("GET", "/_app/missing.js", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != 404 {
		t.Fatalf("unknown asset response = %d, body = %q", recorder.Code, recorder.Body.String())
	}
}

func TestFrontendAuthorizationProvidersRoute(t *testing.T) {
	if _, err := fs.Stat(frontend.Assets, "build/zh-CN/authorization/providers.html"); err != nil {
		t.Skip("full frontend build is not embedded; run make pkg")
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Frontend(engine)
	request := httptest.NewRequest("GET", "/zh-CN/authorization/providers", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "<!doctype html>") {
		t.Fatalf("authorization providers response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestFrontendLeavesAPIForGin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Frontend(engine)
	engine.GET("/api/test", func(c *gin.Context) { c.Status(204) })
	request := httptest.NewRequest("GET", "/api/test", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != 204 {
		t.Fatalf("API response = %d", recorder.Code)
	}
}
