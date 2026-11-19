package server

import (
	"github.com/blerglab/blerg-ai/contracts/theme"
	"net/http"
)

// RegisterThemeRoutes keeps account theme traffic same-origin. Core validates
// the original bearer, fixed runner audience and live human browser session.
func (a *API) RegisterThemeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me/theme", theme.Proxy(a.coreURL, "blerg-runner"))
	mux.HandleFunc("PUT /api/me/theme", theme.Proxy(a.coreURL, "blerg-runner"))
}
