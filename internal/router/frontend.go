package router

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/musqca/polemo-station/internal/frontend"
)

// Frontend mounts the adapter-static output embedded in the Station binary.
// API and WebSocket routes pass through to Gin; all other GET/HEAD requests
// follow the same resolution order as the nginx try_files configuration.
func Frontend(g *gin.Engine) {
	assets, err := fs.Sub(frontend.Assets, "build")
	if err != nil {
		panic("station frontend assets unavailable: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(assets))
	redirectTarget := func(c *gin.Context, directory bool) string {
		target := path.Base(strings.TrimSuffix(c.Request.URL.Path, "/"))
		if directory {
			target += "/"
		} else {
			target = "../" + target
		}
		if c.Request.URL.RawQuery != "" {
			target += "?" + c.Request.URL.RawQuery
		}
		return target
	}
	redirect := func(c *gin.Context, target string) {
		// Keep the Location relative. The browser resolves it against the
		// externally mounted URL, so a proxy prefix stripped before Gin is
		// preserved without requiring a forwarded-prefix header.
		c.Header("Location", target)
		c.Status(http.StatusPermanentRedirect)
	}
	serve := func(c *gin.Context, assetPath string) {
		requestPath := "/" + assetPath
		// net/http intentionally redirects a request ending in index.html to
		// its directory. Use the directory URL so / and /en-US/ render the
		// embedded index instead of redirecting forever to ./.
		if assetPath == "index.html" {
			requestPath = "/"
		} else if strings.HasSuffix(assetPath, "/index.html") {
			requestPath = strings.TrimSuffix(requestPath, "index.html")
		}
		c.Request.URL.Path = requestPath
		fileServer.ServeHTTP(c.Writer, c.Request)
	}

	g.Use(func(c *gin.Context) {
		requestPath := c.Request.URL.Path
		if requestPath == "/api" || strings.HasPrefix(requestPath, "/api/") || requestPath == "/ws" || requestPath == "/station-config.js" {
			c.Next()
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Next()
			return
		}
		requestPath = strings.TrimPrefix(path.Clean(requestPath), "/")
		if requestPath == "." || requestPath == "" {
			requestPath = "index.html"
		}
		info, statErr := fs.Stat(assets, requestPath)
		// SvelteKit emits both a clean-route directory (for __data.json) and
		// a .html document. Prefer the document so /en-US/device does not get
		// redirected to a directory that cannot render the page.
		if statErr == nil && !info.IsDir() {
			serve(c, requestPath)
			c.Abort()
			return
		}
		if !strings.HasSuffix(requestPath, ".html") {
			cleanRoute := requestPath + ".html"
			if _, routeErr := fs.Stat(assets, cleanRoute); routeErr == nil {
				// Concrete pages are emitted as files and therefore use asset
				// paths relative to the file URL. Remove a trailing slash before
				// serving them so /en-US/device/ cannot resolve assets from the
				// wrong directory.
				if strings.HasSuffix(c.Request.URL.Path, "/") {
					redirect(c, redirectTarget(c, false))
					c.Abort()
					return
				}
				serve(c, cleanRoute)
				c.Abort()
				return
			}
			indexRoute := path.Join(requestPath, "index.html")
			if _, indexErr := fs.Stat(assets, indexRoute); indexErr == nil {
				// Directory pages use relative asset URLs. Canonicalize the
				// directory URL before serving its index so /en-US cannot resolve
				// assets against the parent path.
				if !strings.HasSuffix(c.Request.URL.Path, "/") {
					redirect(c, redirectTarget(c, true))
					c.Abort()
					return
				}
				serve(c, indexRoute)
				c.Abort()
				return
			}
		}
		if strings.HasPrefix(requestPath, "api/") || requestPath == "ws" {
			c.Next()
			return
		}
		// Do not turn unknown routes or missing JS/CSS files into the app
		// shell. That masks deployment errors and makes browser diagnostics
		// misleading.
		c.Status(http.StatusNotFound)
		c.Abort()
	})
}
