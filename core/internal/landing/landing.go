// Package landing holds blerg-core's embedded brand assets (tokens.css, logo.svg,
// favicon.svg), served under /brand/ for the SPA and for any other surface that wants the
// canonical palette, plus the build-version helper the SPA's footer shows.
//
// The assets embedded here are a copy of the canonical files in /brand at the repo root —
// that directory is the single source of truth other surfaces (board, runner) adopt from.
// Go's //go:embed can only reach files under this package's own directory tree, so the copy
// lives at static/; keep the two in sync when the brand changes.
//
// The server-rendered landing page that used to live here is gone: the root now redirects
// into the SPA, whose home is the signed-in landing and whose login form is what a signed-out
// visitor sees first.
package landing

import (
	"io/fs"
	"net/http"
	"os"

	"embed"
)

//go:embed static/tokens.css static/logo.svg static/favicon.svg
var assetFS embed.FS

// Links are the browser-facing component URLs the SPA's home tiles render (GET /api/site).
// Empty values make the SPA fall back to the desktop-compose ports client-side (core at
// :8081, board :8082, runner :8083).
type Links struct{ BoardURL, RunnerURL string }

// Version returns the running blerg-core build version. It reads BLERG_VERSION
// (set by the deploy pipeline / Dockerfile) and falls back to "dev" for local
// builds, so the SPA footer never shows a blank version.
func Version() string {
	if v := os.Getenv("BLERG_VERSION"); v != "" {
		return v
	}
	return "dev"
}

// AssetHandler serves the embedded brand assets (tokens.css, logo.svg,
// favicon.svg) under /brand/.
func AssetHandler() http.Handler {
	sub, err := fs.Sub(assetFS, "static")
	if err != nil {
		panic(err) // static/ is embedded above; this can only fail on a build-time typo
	}
	return http.FileServer(http.FS(sub))
}
