package api

// Board call limits (identity/board_limits.go). An administrator (account.manage) reads and sets
// them under Settings; the runner reads the ones it enforces with its internal key.
//
//   GET  /api/settings/board-limits   the limits in force, their defaults and ranges
//   PUT  /api/settings/board-limits   set all four (a live sign-in required); core's live-token
//                                     cap applies to the next exchange, runners pick the rest up
//                                     within BoardLimitsRunnerRefresh
//   POST /internal/board-limits       the runner's read: the four numbers, no account data

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// BoardLimitsRunnerRefresh is how long a runner keeps the limits it read (runner
// boardLimitsTTL); the settings page says a change reaches runners within it.
const BoardLimitsRunnerRefresh = time.Minute

type boardLimitsView struct {
	identity.BoardLimits
	Defaults  identity.BoardLimits `json:"defaults"`
	Ranges    map[string][2]int    `json:"ranges"`
	Custom    bool                 `json:"custom"`
	UpdatedAt *time.Time           `json:"updated_at"`
	// RunnerRefreshSeconds: runners use a change within this many seconds.
	RunnerRefreshSeconds int `json:"runner_refresh_seconds"`
}

func (d Deps) boardLimitsService(w http.ResponseWriter) (*identity.Service, bool) {
	w.Header().Set("Cache-Control", "no-store")
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "identity service unavailable", http.StatusServiceUnavailable)
	}
	return svc, ok
}

func (d Deps) writeBoardLimits(w http.ResponseWriter, r *http.Request, svc *identity.Service) {
	st, err := svc.BoardLimits(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, boardLimitsView{BoardLimits: st.BoardLimits, Defaults: identity.DefaultBoardLimits, Ranges: identity.BoardLimitRange,
		Custom: st.Set, UpdatedAt: st.UpdatedAt, RunnerRefreshSeconds: int(BoardLimitsRunnerRefresh / time.Second)})
}

func (d Deps) handleGetBoardLimits(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.boardLimitsService(w)
	if !ok {
		return
	}
	d.writeBoardLimits(w, r, svc)
}

func (d Deps) handlePutBoardLimits(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.boardLimitsService(w)
	if !ok {
		return
	}
	p, ok := principalFromCtx(r.Context())
	if !ok || p.Sid == "" {
		http.Error(w, "live browser session required", http.StatusUnauthorized)
		return
	}
	if _, _, live, err := d.principalLiveness(r.Context(), p.Sub, "", p.Sid); err != nil || !live {
		http.Error(w, "live browser session required", http.StatusUnauthorized)
		return
	}
	var body identity.BoardLimits
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	before, err := svc.BoardLimits(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := svc.SetBoardLimits(r.Context(), p.Sub, body); err != nil {
		if errors.Is(err, identity.ErrInvalidBoardLimits) {
			http.Error(w, strings.TrimPrefix(err.Error(), identity.ErrInvalidBoardLimits.Error()+": "), http.StatusBadRequest)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("board limits set by %s: per minute %d->%d, in flight %d->%d, unrevoked %d->%d, live tokens %d->%d", p.Sub,
		before.CallsPerMinute, body.CallsPerMinute, before.CallsInFlight, body.CallsInFlight,
		before.UnrevokedPerAccount, body.UnrevokedPerAccount, before.LiveExchangeTokens, body.LiveExchangeTokens)
	d.writeBoardLimits(w, r, svc)
}

// handleInternalBoardLimits is POST /internal/board-limits: the limits the runner enforces. They are
// no account's data, so the internal key is the only gate.
func (d Deps) handleInternalBoardLimits(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.boardLimitsService(w)
	if !ok {
		return
	}
	if d.InternalKey == "" {
		http.Error(w, "internal board limits unavailable", http.StatusServiceUnavailable)
		return
	}
	if !validInternalKey(r, d.InternalKey) {
		http.Error(w, "invalid internal key", http.StatusUnauthorized)
		return
	}
	st, err := svc.BoardLimits(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, st.BoardLimits)
}
