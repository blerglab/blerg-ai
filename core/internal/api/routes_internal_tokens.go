package api

import "net/http"

// registerInternalTokenRoutes owns the registration of the cron-token routes. All three are
// component-to-component (internal key), never browser facing, and set no CORS headers.
func (d Deps) registerInternalTokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/tokens/mint", d.handleInternalTokenMint)
	mux.HandleFunc("POST /internal/tokens/revoke", d.handleInternalTokenRevoke)
	mux.HandleFunc("POST /internal/tokens/status", d.handleInternalTokenStatus)
	mux.HandleFunc("POST /internal/tokens/exchange", d.handleInternalTokenExchange)
	mux.HandleFunc("POST /internal/tokens/exchange/revoke", d.handleInternalTokenExchangeRevoke)
}
