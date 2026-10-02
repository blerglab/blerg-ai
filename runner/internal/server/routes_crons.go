package server

import "net/http"

// RegisterCronRoutes registers the human-only crons routes (spec 7.7) on mux. Call it after
// StartCrons, which creates the cron service the routes act through; without it the routes
// answer 503 once the caller has been authenticated.
func (a *API) RegisterCronRoutes(mux *http.ServeMux, coreURL, coreInternalKey string) {
	a.registerCronRoutes(mux, &HTTPCronMinter{BaseURL: coreURL, InternalKey: coreInternalKey})
}

func (a *API) registerCronRoutes(mux *http.ServeMux, minter CronMinter) {
	h := newCronsHandler(a, minter)
	mux.HandleFunc("GET /api/crons", h.list)
	mux.HandleFunc("POST /api/crons", h.create)
	mux.HandleFunc("GET /api/crons/{id}", h.get)
	mux.HandleFunc("PATCH /api/crons/{id}", h.patch)
	mux.HandleFunc("DELETE /api/crons/{id}", h.remove)
	mux.HandleFunc("POST /api/crons/{id}/run", h.runNow)
	mux.HandleFunc("POST /api/crons/{id}/pause", h.pause)
	mux.HandleFunc("POST /api/crons/{id}/resume", h.resume)
	mux.HandleFunc("POST /api/crons/{id}/renew", h.renew)
	mux.HandleFunc("GET /api/crons/{id}/runs", h.runs)
}
