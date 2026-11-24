// Package web serves the control plane's embedded browser client.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed index.html app.js api.js ui-components.js management.js terminal.js user-management.js webhook-management.js alert-settings.js ci-management.js platform-audit.js image-management.js monitoring.js node-workspace.js maintenance-page.js compose-maintenance.js container-maintenance.js style.css favicon.ico vendor/xterm.js vendor/xterm.css vendor/addon-fit.js vendor/uplot.js vendor/uplot.css
var assets embed.FS

func Mount(mux *http.ServeMux) {
	for _, path := range []string{"/", "/app.js", "/api.js", "/ui-components.js", "/management.js", "/terminal.js", "/user-management.js", "/webhook-management.js", "/alert-settings.js", "/ci-management.js", "/platform-audit.js", "/image-management.js", "/monitoring.js", "/node-workspace.js", "/maintenance-page.js", "/compose-maintenance.js", "/container-maintenance.js", "/style.css", "/favicon.ico", "/vendor/xterm.js", "/vendor/xterm.css", "/vendor/addon-fit.js", "/vendor/uplot.js", "/vendor/uplot.css"} {
		name := path[1:]
		if name == "" {
			name = "index.html"
		}
		pattern := "GET " + path
		if path == "/" {
			pattern = "GET /{$}"
		}
		// Static bytes change only with a new build, so the browser may keep
		// them and revalidate per load: the etag turns repeat visits into a
		// bodyless 304 instead of re-downloading the vendored bundles.
		etag := ""
		if data, err := fs.ReadFile(assets, name); err == nil {
			sum := sha256.Sum256(data)
			etag = `"` + hex.EncodeToString(sum[:]) + `"`
		}
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			consoleHeaders(w)
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("ETag", etag)
			if name == "favicon.ico" {
				w.Header().Set("Content-Type", "image/x-icon")
			}
			http.ServeFileFS(w, r, assets, name)
		})
	}
	// The console uses history-based routing: any non-API path that is not an
	// asset request deep-links into the single page, which renders the route
	// client-side.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if isConsoleRoutePath(r.URL.Path) {
			consoleHeaders(w)
			http.ServeFileFS(w, r, assets, "index.html")
			return
		}
		http.NotFound(w, r)
	})
}

// isConsoleRoutePath reports whether a request path should serve the console page
// so that history-routed deep links (for example /node/{id}/containers) work.
// API and Agent namespaces keep their normal semantics, and asset-like extensions
// outside the route namespaces 404 instead of returning HTML. Route identifiers
// (node IDs, project and deployment names) may contain dots, so paths under a
// known route namespace always deep-link into the console, while an unknown
// dotted path still does.
func isConsoleRoutePath(urlPath string) bool {
	for _, prefix := range []string{"/api/", "/agent/", "/webhooks/", "/healthz"} {
		if strings.HasPrefix(urlPath, prefix) {
			return false
		}
	}
	for _, root := range []string{"console", "environments", "settings", "node", "deployment"} {
		if urlPath == "/"+root || strings.HasPrefix(urlPath, "/"+root+"/") {
			return true
		}
	}
	switch path.Ext(urlPath) {
	case ".js", ".mjs", ".css", ".ico", ".map", ".json", ".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".woff", ".woff2", ".ttf", ".txt", ".xml", ".html", ".htm":
		return false
	}
	return true
}

func consoleHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
}
