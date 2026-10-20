package api

import "net/http"

// registerInternalTokenRoutes registers cron tokens, target exchange, and Board discovery. All are
// component-to-component (internal key), never browser facing, and set no CORS headers.
func (d Deps) registerInternalTokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/boards/list", d.handleInternalBoardsList)
	mux.HandleFunc("POST /internal/tokens/mint", d.handleInternalTokenMint)
	mux.HandleFunc("POST /internal/tokens/revoke", d.handleInternalTokenRevoke)
	mux.HandleFunc("POST /internal/tokens/status", d.handleInternalTokenStatus)
	mux.HandleFunc("POST /internal/tokens/exchange", d.handleInternalTokenExchange)
	mux.HandleFunc("POST /internal/tokens/exchange/board-create", d.handleInternalBoardCreateExchange)
	mux.HandleFunc("POST /internal/tokens/exchange/revoke", d.handleInternalTokenExchangeRevoke)
	mux.HandleFunc("POST /internal/tokens/board-automation", d.handleBoardAutomationMint)
	mux.HandleFunc("POST /internal/tokens/board-automation/revoke", d.handleBoardAutomationRevoke)
}
