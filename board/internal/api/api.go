// Package api wires blerg-board's REST surface: auth middleware, board/card/column
// CRUD, search, tokens, the admission gate on the agent write path, the held
// queue, and a board-scoped WebSocket broadcast.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/gate"
	"github.com/blerglab/blerg-ai/board/prompts"
	"github.com/jackc/pgx/v5/pgxpool"
)

type API struct {
	Pool    *pgxpool.Pool
	Auth    *auth.Auth
	Gate    *gate.Gate
	Hub     *Hub
	Prompts *prompts.Store
	runner  *RunnerConfig
	tickMu  sync.Mutex // held for the duration of a dispatcher pass (runTick)

	// ciErrMu/ciErrSince: per card, when the current unbroken run of
	// CI-status failures started. See ciErrorHeld.
	ciErrMu    sync.Mutex
	ciErrSince map[string]time.Time

	// ciRerunMu/ciRerunLast: per card, the last head the sweep ASKED GitHub to
	// re-run and when. See mayAttemptRerun.
	ciRerunMu   sync.Mutex
	ciRerunLast map[string]ciRerunAttempt
}

func New(pool *pgxpool.Pool, a *auth.Auth, g *gate.Gate) *API {
	// The embedded defaults are part of the build, so this can only fail if
	// one of them is broken — a programmer error, not a runtime condition.
	store, err := prompts.Load("")
	if err != nil {
		panic(err)
	}
	return &API{Pool: pool, Auth: a, Gate: g, Hub: NewHub(), Prompts: store}
}

// LoadPrompts reloads the prompt store, overlaying any role file found in
// overrideDir (BLERG_BOARD_PROMPTS_DIR) onto the embedded defaults. Callers should
// treat a non-nil error as fatal — refuse to boot rather than run with a
// broken brief.
func (a *API) LoadPrompts(overrideDir string) error {
	store, err := prompts.Load(overrideDir)
	if err != nil {
		return err
	}
	a.Prompts = store
	return nil
}

// Routes registers everything on mux. Every /api route requires a principal
// (unauthenticated = 401 — "human" is a positive assertion, never the
// absence of a token). Login itself is entirely blerg-core's now: board has
// no /api/login of its own any more.
func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /onboard", handleOnboard)

	authed := func(h func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			p, err := a.Auth.Resolve(r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			// A core-issued agent token with a Project claim is confined to
			// that board here, before any handler runs (see scope.go).
			if board, scoped := p.ProjectScoped(); scoped && !a.scopeDecision(w, r, board) {
				return
			}
			h(w, r, p)
		}
	}

	mux.HandleFunc("GET /api/me", authed(a.handleMe))

	mux.HandleFunc("GET /api/boards", authed(a.handleListBoards))
	mux.HandleFunc("POST /api/boards", authed(a.handleCreateBoard))
	mux.HandleFunc("GET /api/boards/{id}", authed(a.handleGetBoard))
	mux.HandleFunc("PATCH /api/boards/{id}", authed(a.handleUpdateBoard))
	mux.HandleFunc("DELETE /api/boards/{id}", authed(a.handleDeleteBoard))

	mux.HandleFunc("GET /api/boards/{id}/columns", authed(a.handleListColumns))
	mux.HandleFunc("POST /api/boards/{id}/columns", authed(a.handleCreateColumn))
	mux.HandleFunc("PATCH /api/columns/{id}", authed(a.handleRenameColumn))
	mux.HandleFunc("DELETE /api/columns/{id}", authed(a.handleDeleteColumn))
	mux.HandleFunc("POST /api/columns/{id}/move", authed(a.handleMoveColumn))

	mux.HandleFunc("GET /api/boards/{id}/cards", authed(a.handleListCards))
	mux.HandleFunc("POST /api/boards/{id}/cards", authed(a.handleCreateCard))
	mux.HandleFunc("GET /api/boards/{id}/cards/search", authed(a.handleSearch))
	mux.HandleFunc("GET /api/boards/{id}/schema", authed(a.handleGetSchema))
	mux.HandleFunc("GET /api/cards/{id}", authed(a.handleGetCard))
	mux.HandleFunc("PATCH /api/cards/{id}", authed(a.handleUpdateCard))
	mux.HandleFunc("POST /api/cards/{id}/move", authed(a.handleMoveCard))
	mux.HandleFunc("POST /api/cards/{id}/archive", authed(a.handleArchiveCard))
	mux.HandleFunc("DELETE /api/cards/{id}", authed(a.handleDeleteCard))
	mux.HandleFunc("POST /api/cards/{id}/dependencies", authed(a.handleAddDep))
	mux.HandleFunc("DELETE /api/cards/{id}/dependencies/{depID}", authed(a.handleRemoveDep))
	mux.HandleFunc("GET /api/cards/{id}/events", authed(a.handleCardEvents))
	mux.HandleFunc("POST /api/cards/{id}/comments", authed(a.handleCardComment))
	mux.HandleFunc("GET /api/cards/{id}/diff", authed(a.handleCardDiff))
	mux.HandleFunc("GET /api/cards/{id}/git", authed(a.handleCardGit))
	mux.HandleFunc("GET /api/cards/{id}/stats", authed(a.handleCardStats))
	mux.HandleFunc("GET /api/cards/{id}/doc", authed(a.handleCardDoc))
	mux.HandleFunc("GET /api/boards/{id}/metrics", authed(a.handleBoardMetrics))
	mux.HandleFunc("GET /api/overview", authed(a.handleBoardsOverview))
	mux.HandleFunc("GET /api/search", authed(a.handleGlobalSearch))
	mux.HandleFunc("GET /api/sessions", authed(a.handleActiveSessionsGlobal))
	mux.HandleFunc("GET /api/boards/{id}/icon", authed(a.handleBoardIcon))

	mux.HandleFunc("GET /api/tokens", authed(a.handleListTokens))
	mux.HandleFunc("POST /api/tokens", authed(a.handleMintToken))
	mux.HandleFunc("POST /api/tokens/refresh", authed(a.handleRefreshToken))
	mux.HandleFunc("POST /api/tokens/{id}/revoke", authed(a.handleRevokeToken))

	mux.HandleFunc("GET /api/reviews", authed(a.handleListReviews))
	mux.HandleFunc("GET /api/reviews/{id}", authed(a.handleGetReview))
	mux.HandleFunc("POST /api/reviews/{id}/resolve", authed(a.handleResolveReview))

	a.runnerRoutes(mux, authed)
	a.boardSessionRoutes(mux, authed)
	a.boardRunRoutes(mux, authed)
	a.standingAgentRoutes(mux, authed)

	mux.HandleFunc("GET /ws", a.handleWS)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeDBError maps db sentinel errors onto HTTP statuses.
func writeDBError(w http.ResponseWriter, err error) {
	var fe *db.FieldError
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, db.ErrStaleVersion):
		writeError(w, http.StatusConflict, "stale version (If-Match mismatch)")
	case errors.Is(err, db.ErrInvalidRepos), errors.Is(err, db.ErrInvalidLink):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, db.ErrColumnHasCards):
		writeError(w, http.StatusConflict, db.ErrColumnHasCards.Error())
	case errors.Is(err, db.ErrDependencyCycle):
		writeError(w, http.StatusConflict, db.ErrDependencyCycle.Error())
	case errors.Is(err, db.ErrDuplicateDedupKey):
		writeError(w, http.StatusConflict, "dedup_key already used by a live card")
	case errors.Is(err, db.ErrDuplicateExternal):
		writeError(w, http.StatusConflict, "external_id already exists on this board")
	case errors.Is(err, db.ErrAutomationKeyMissing):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": db.ErrAutomationKeyMissing.Error(), "field": "automation_token",
		})
	case errors.As(err, &fe):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": fe.Error(), "field": fe.Key,
		})
	default:
		log.Printf("api error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func decode[T any](r *http.Request) (T, error) {
	var v T
	err := json.NewDecoder(r.Body).Decode(&v)
	return v, err
}

// ifMatch reads the If-Match header as a version int.
func ifMatch(r *http.Request) *int {
	h := strings.Trim(r.Header.Get("If-Match"), `" `)
	if h == "" {
		return nil
	}
	n, err := strconv.Atoi(h)
	if err != nil {
		return nil
	}
	return &n
}

// gateCheck runs the admission gate for a write when the principal is gated.
// Returns (outcome, handled). When handled, the response has been written.
func (a *API) gateCheck(w http.ResponseWriter, r *http.Request, p auth.Principal, board db.Board, current *db.Card, operation string, payload json.RawMessage, disputeOf, rebuttal string) (gate.Outcome, bool) {
	if p.IsHuman() {
		return gate.Outcome{Allowed: true}, false
	}
	if p.Token != nil && p.Token.HasCap("gate.bypass") {
		return gate.Outcome{Allowed: true}, false
	}
	if !board.GateEnabled || a.Gate == nil {
		return gate.Outcome{Allowed: true}, false
	}
	cols, err := db.ListColumns(r.Context(), a.Pool, board.ID)
	if err != nil {
		writeDBError(w, err)
		return gate.Outcome{}, true
	}
	out, err := a.Gate.Check(r.Context(), board, cols, current, p.GateActorTokenID(), operation, payload, disputeOf, rebuttal)
	if err != nil {
		writeDBError(w, err)
		return gate.Outcome{}, true
	}
	if !out.Allowed {
		writeJSON(w, out.Status, out.Body)
		return out, true
	}
	return out, false
}

// stampGate records a gate flag + review linkage on a card after the write.
func (a *API) stampGate(ctx context.Context, cardID string, out gate.Outcome) {
	if out.GateFlag != nil {
		_, _ = a.Pool.Exec(ctx, `UPDATE cards SET gate_flag = $2 WHERE id = $1`, cardID, *out.GateFlag)
	}
	if out.ReviewID != nil {
		_, _ = a.Pool.Exec(ctx, `UPDATE admission_reviews SET card_id = $2 WHERE id = $1`, *out.ReviewID, cardID)
	}
}

// StartHeldSweep resolves expired held reviews per board policy: open-policy
// boards apply the payload (flag held_approved), hold-policy boards reject.
// resolved_by = timeout either way.
func (a *API) StartHeldSweep(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.sweepHeld(ctx)
			}
		}
	}()
}

func (a *API) sweepHeld(ctx context.Context) {
	expired, err := db.ExpiredHeldReviews(ctx, a.Pool)
	if err != nil {
		log.Printf("held sweep: %v", err)
		return
	}
	for _, rev := range expired {
		board, err := db.GetBoard(ctx, a.Pool, rev.BoardID)
		if err != nil {
			continue
		}
		if board.GateOnUnavailable == "open" && rev.Operation == "create" {
			if cardID, err := a.applyHeldCreate(ctx, rev); err == nil {
				_, _ = db.ResolveHeldReview(ctx, a.Pool, rev.ID, "accept", "timeout", &cardID)
				a.Hub.Broadcast(rev.BoardID, "card_changed")
				continue
			}
		}
		_, _ = db.ResolveHeldReview(ctx, a.Pool, rev.ID, "deny", "timeout", nil)
	}
}

// applyHeldCreate replays a held create payload; the card carries
// gate_flag=held_approved. Held creates have no target to go stale.
func (a *API) applyHeldCreate(ctx context.Context, rev db.Review) (string, error) {
	var p db.CardParams
	if err := json.Unmarshal(rev.Payload, &p); err != nil {
		return "", err
	}
	res, err := db.CreateCard(ctx, a.Pool, rev.BoardID, p, db.EventMeta{
		Actor: "curator", ActorTokenID: rev.ActorTokenID, ReviewID: &rev.ID,
	})
	if err != nil {
		return "", err
	}
	_, _ = a.Pool.Exec(ctx, `UPDATE cards SET gate_flag = 'held_approved' WHERE id = $1`, res.Card.ID)
	return res.Card.ID, nil
}

// Stale-target errors for applyHeldNonCreate: the card moved on enough since
// the hold that replaying the payload no longer makes sense.
var (
	errTargetGone     = errors.New("target card no longer exists")
	errTargetArchived = errors.New("target card was archived since this review was held")
)

// applyHeldNonCreate replays a held update/move/archive/delete payload
// against the card's CURRENT row (not a snapshot from hold time): it fails
// if the target was deleted or archived since the hold, and otherwise
// applies against whatever the card looks like now, returning a note when
// the card's version drifted from what was held (edited or moved meanwhile)
// so the caller can surface that divergence instead of silently losing it.
func (a *API) applyHeldNonCreate(ctx context.Context, rev db.Review) (cardID, note string, err error) {
	if rev.TargetCardID == nil {
		return "", "", fmt.Errorf("no target card recorded for this review")
	}
	card, err := db.GetCard(ctx, a.Pool, *rev.TargetCardID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return "", "", errTargetGone
		}
		return "", "", err
	}
	if card.ArchivedAt != nil {
		return "", "", errTargetArchived
	}
	if rev.TargetVersion != nil && card.Version != *rev.TargetVersion {
		note = fmt.Sprintf("card changed since this review was held (version %d -> %d); applied against current content",
			*rev.TargetVersion, card.Version)
	}
	meta := db.EventMeta{Actor: "curator", ActorTokenID: rev.ActorTokenID, ReviewID: &rev.ID}

	switch rev.Operation {
	case "update":
		var p db.CardParams
		if err := json.Unmarshal(rev.Payload, &p); err != nil {
			return "", "", err
		}
		p.IfMatch = nil // replay against current state, not the version at hold time
		updated, err := db.UpdateCard(ctx, a.Pool, card.ID, p, meta)
		if err != nil {
			return "", "", err
		}
		card = updated
	case "move":
		var p struct {
			ToColumn string `json:"to_column"`
		}
		if err := json.Unmarshal(rev.Payload, &p); err != nil {
			return "", "", err
		}
		moved, err := db.MoveCard(ctx, a.Pool, card.ID, p.ToColumn, nil, nil, meta)
		if err != nil {
			return "", "", err
		}
		card = moved
		// Mirror the live move path: entering review spawns a reviewer,
		// bouncing back to a work column relays findings.
		if board, err := db.GetBoard(ctx, a.Pool, rev.BoardID); err == nil {
			go a.afterMove(context.WithoutCancel(ctx), board, moved) // detached: the review-flow reaction (spawn reviewer / relay findings) must outlive the request
		}
	case "archive":
		archived, err := db.ArchiveCard(ctx, a.Pool, card.ID, nil, meta)
		if err != nil {
			return "", "", err
		}
		card = archived
	case "delete":
		if err := db.DeleteCard(ctx, a.Pool, card.ID); err != nil {
			return "", "", err
		}
		return card.ID, note, nil
	default:
		return "", "", fmt.Errorf("unsupported held operation %q", rev.Operation)
	}
	_, _ = a.Pool.Exec(ctx, `UPDATE cards SET gate_flag = 'held_approved' WHERE id = $1`, card.ID)
	return card.ID, note, nil
}
