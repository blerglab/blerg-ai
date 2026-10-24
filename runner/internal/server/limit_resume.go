package server

// Usage-limit continuations belong to the server, so neither a sleeping laptop
// nor a replaced pod owns a timer. Delivery is deliberately at most once: the
// durable dispatch record precedes enqueue, and an ambiguous send needs a person.
import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

const limitResumeSource = "limit_resume"
const limitResumePrompt = "Continue the task that was interrupted by the provider usage limit. Check the current state before repeating any action."

type limitRefusal struct {
	Provider string     `json:"provider"`
	TurnID   string     `json:"turn_id"`
	ResetAt  *time.Time `json:"reset_at,omitempty"`
}

func parseLimitRefusal(raw []byte, now time.Time) (limitRefusal, bool) {
	var p limitRefusal
	if json.Unmarshal(raw, &p) != nil || p.Provider == "" || !engineLimitResumeSupported(p.Provider) || p.TurnID == "" || len(p.TurnID) > 128 {
		return p, false
	}
	if p.ResetAt != nil && (p.ResetAt.After(now.Add(8*24*time.Hour)) || p.ResetAt.Before(now.Add(-8*24*time.Hour))) {
		return p, false
	}
	return p, true
}

func limitResumeEligible(row *db.SessionRow) bool {
	return row != nil && row.Kind == "agent" && (row.Status == "idle" || row.Status == "disconnected") && row.PausedReason == "" && row.BackgroundTasks == 0
}

// Owner-only, because permission to view a shared chat is not permission to
// arrange unattended work using its owner's provider account.
func (a *API) HandleLimitResume(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "continuations require a database")
		return
	}
	id := r.PathValue("id")
	row, err := db.GetSession(r.Context(), a.dbPool, id)
	if err != nil || !ownsSession(p.Sub, row) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if row.Kind != "agent" || !engineLimitResumeSupported(derefOrEmpty(row.Engine)) {
		writeError(w, http.StatusConflict, "this engine does not support usage-limit continuation")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if r.Method == http.MethodPut {
		if p.Sid == "" {
			writeError(w, http.StatusForbidden, "sign in again to authorize unattended continuation")
			return
		}
		if terminalSessionStatus(row.Status) {
			writeError(w, http.StatusConflict, "session has ended")
			return
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil || body.Enabled == nil {
			writeError(w, http.StatusBadRequest, "enabled required")
			return
		}
	}
	var state *db.LimitResume
	err = db.WithLimitResumeLock(r.Context(), a.dbPool, id, func(conn *pgxpool.Conn) error { //nolint:contextcheck // completion webhooks deliberately outlive the request, as on ordinary stop
		if r.Method == http.MethodPut {
			previous, e := db.GetLimitResume(r.Context(), conn, id)
			if e != nil {
				return e
			}
			if e := db.SetLimitResume(r.Context(), conn, id, *body.Enabled, p.Sid); e != nil {
				return e
			}
			if !*body.Enabled && row.AutoStop && previous.State == "pending" {
				var idle bool
				if e := conn.QueryRow(r.Context(), `SELECT status='idle' FROM sessions WHERE id=$1`, id).Scan(&idle); e != nil {
					return e
				}
				if idle {
					claimed, e := db.ClaimSessionAutoStop(r.Context(), a.dbPool, id)
					if e != nil {
						return e
					}
					if claimed {
						a.stopSessionUnlocked(r.Context(), id, systemEnd(db.EndReasonAutoStopped))
					}
				}
			}
		}
		if r.Method == http.MethodPut {
			text := "Automatic continuation disabled."
			if *body.Enabled {
				text = "Automatic continuation enabled for provider usage limits."
			}
			limitResumeAudit(r.Context(), a.hub, a.dbPool, id, newUUID(), text)
		}
		var e error
		state, e = db.GetLimitResume(r.Context(), conn, id)
		return e
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "continuation is busy; retry shortly")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// limitResumeIngestWait bounds how long event ingestion waits for a session's
// limit-resume lock.
const limitResumeIngestWait = 1500 * time.Millisecond

// limitResumeIngestMargin is left of the caller's deadline after the wait: in a
// pod, ingestion runs inside the runtime fence, which must still commit.
const limitResumeIngestMargin = 500 * time.Millisecond

// limitResumeIngestWaitFor is limitResumeIngestWait, capped by what remains of
// ctx's deadline less limitResumeIngestMargin. Zero or less means no time to wait.
func limitResumeIngestWaitFor(ctx context.Context) time.Duration {
	wait := limitResumeIngestWait
	if deadline, ok := ctx.Deadline(); ok {
		wait = min(wait, time.Until(deadline)-limitResumeIngestMargin)
	}
	return wait
}

// Persist the retry side effects before acknowledging an engine event. The
// sequence fence makes reprocessing safe if append succeeded but this write failed.
func ingestLimitResume(ctx context.Context, h *Hub, pool *pgxpool.Pool, msg protocol.AgentEvent, seq int64) error {
	if pool == nil || (msg.Kind != "user_message" && msg.Kind != "usage_limit") {
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, msg.Ts)
	if err != nil {
		return nil //nolint:nilerr // malformed optional timestamps cannot schedule work and must not stall event ingestion
	}
	var turn struct {
		TurnID string `json:"turn_id"`
		Source string `json:"source"`
	}
	if msg.Kind == "user_message" {
		if json.Unmarshal(msg.Payload, &turn) != nil || turn.TurnID == "" {
			return nil //nolint:nilerr // old daemons have no turn ID; ignore optional scheduling metadata
		}
		// Most sessions never opt in: no row, no lock, no upsert. A refusal or
		// a later opt-in backfills the turn from the transcript.
		if exists, err := db.HasLimitResume(ctx, pool, msg.SessionID); err != nil || !exists {
			return err
		}
	}
	// A short blocking wait, not a try-lock: a busy lock (a heartbeat replay or
	// a browser send) must not leave the event unacknowledged until the
	// daemon's next resend.
	wait := limitResumeIngestWaitFor(ctx)
	if wait <= 0 {
		// No time left before the caller's deadline (a pod's fenced frame):
		// skip the scheduling work without failing the frame. The event stays
		// unacknowledged, so the daemon re-sends it and the work is redone then.
		return db.ErrLimitResumeBusy
	}
	return db.WithLimitResumeLockWait(ctx, pool, msg.SessionID, wait, func(conn *pgxpool.Conn) error {
		if msg.Kind == "user_message" {
			return db.TrackLimitResumeTurn(ctx, conn, msg.SessionID, turn.TurnID, turn.Source, at, seq)
		}
		p, ok := parseLimitRefusal(msg.Payload, time.Now())
		if !ok {
			return nil
		}
		if err := db.BackfillLimitResumeTurn(ctx, conn, msg.SessionID, seq); err != nil {
			return err
		}
		if err := db.RecordLimitRefusal(ctx, conn, msg.SessionID, p.Provider, p.TurnID, p.ResetAt, seq, at); err != nil {
			return err
		}
		state, err := db.GetLimitResume(ctx, conn, msg.SessionID)
		if err != nil {
			return err
		}
		if state.State == "pending" && state.EventSeq == seq {
			limitResumeAudit(ctx, h, pool, msg.SessionID, messageEventID(msg.ClientEventID, "limit-resume-scheduled"), "Automatic continuation scheduled after the provider usage limit resets.")
		}
		return nil
	})
}

// Cancellation and the manual operation use the same lock as dispatch. A
// cancellation that completed cannot be followed by a previously claimed send.
func withCancelledPendingLimitResume(ctx context.Context, pool *pgxpool.Pool, id string, fn func() error) error {
	return withCancelledPendingLimitResumeConn(ctx, pool, id, func(_ *pgxpool.Conn) error { return fn() })
}

func withCancelledPendingLimitResumeConn(ctx context.Context, pool *pgxpool.Pool, id string, fn func(*pgxpool.Conn) error) error {
	if pool == nil {
		return fn(nil)
	}
	return withLimitResumeActionLock(ctx, pool, id, func(conn *pgxpool.Conn) error {
		previous, err := db.GetLimitResume(ctx, conn, id)
		if err != nil {
			return err
		}
		if err := db.CancelPendingLimitResume(ctx, conn, id); err != nil {
			return err
		}
		if previous.State == "pending" || previous.State == "dispatched" {
			limitResumeAudit(ctx, nil, pool, id, newUUID(), "Automatic continuation canceled by manual intervention.")
		}
		return fn(conn)
	})
}

func withCancelledLimitResume(ctx context.Context, pool *pgxpool.Pool, id string, fn func() error) error {
	if pool == nil {
		return fn()
	}
	return withLimitResumeActionLock(ctx, pool, id, func(conn *pgxpool.Conn) error {
		previous, err := db.GetLimitResume(ctx, conn, id)
		if err != nil {
			return err
		}
		if err := db.CancelLimitResume(ctx, conn, id); err != nil {
			return err
		}
		if previous.State == "pending" || previous.State == "dispatched" {
			limitResumeAudit(ctx, nil, pool, id, newUUID(), "Automatic continuation canceled by manual intervention.")
		}
		return fn()
	})
}

func withLimitResumeActionLock(ctx context.Context, pool *pgxpool.Pool, id string, fn func(*pgxpool.Conn) error) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	for {
		err := db.WithLimitResumeLock(ctx, pool, id, fn)
		if !errors.Is(err, db.ErrLimitResumeBusy) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (a *API) RunLimitResumes(ctx context.Context) {
	if a.dbPool == nil {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.checkLimitResumes(ctx)
		}
	}
}

func (a *API) checkLimitResumes(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ids, err := db.ListDueLimitResumes(ctx, a.dbPool)
	if err != nil {
		log.Printf("limit resume: list: %v", err)
		return
	}
	// One dispatch per tick across the install. This also staggers sessions
	// sharing credentials without storing provider account identifiers.
	for _, id := range ids {
		dispatched := false
		settleAutoStop := false
		err = db.WithLimitResumeLock(ctx, a.dbPool, "limit-resume-dispatch", func(global *pgxpool.Conn) error {
			return db.WithLimitResumeLock(ctx, a.dbPool, id, func(conn *pgxpool.Conn) error {
				var next *time.Time
				if e := global.QueryRow(ctx, `SELECT max(dispatched_at) FROM session_limit_resume`).Scan(&next); e != nil {
					return e
				}
				if next != nil && time.Since(*next) < 15*time.Second {
					return nil
				}
				state, e := db.GetLimitResume(ctx, conn, id)
				if e != nil {
					return e
				}
				if !state.Enabled || state.State != "pending" || state.ResetAt == nil || time.Now().Before(state.ResetAt.Add(time.Minute)) {
					return nil
				}
				blocked, err := db.DelegationResumeBlocked(ctx, conn, id)
				if err != nil {
					return err
				}
				if blocked {
					return db.SetLimitResumeState(ctx, conn, id, "blocked", "The session's parent ended; this child cannot resume.")
				}
				if state.ExpiresAt == nil || time.Now().After(*state.ExpiresAt) {
					settleAutoStop = true
					return db.SetLimitResumeState(ctx, conn, id, "blocked", "The continuation window expired; continue manually.")
				}
				row, e := db.GetSession(ctx, a.dbPool, id)
				if e != nil {
					return e
				}
				// A workstation disconnect is revivable even though its stored status
				// is error. Leave the durable retry pending until its host returns.
				if row != nil && row.Status == "error" {
					awaiting, e := db.SessionAwaitsDaemon(ctx, a.dbPool, id)
					if e != nil {
						return e
					}
					if awaiting {
						return nil
					}
				}
				if row == nil || terminalSessionStatus(row.Status) {
					settleAutoStop = true
					return db.SetLimitResumeState(ctx, conn, id, "blocked", "The session ended; continue manually.")
				}
				if row.PausedReason != "" {
					settleAutoStop = true
					return db.SetLimitResumeState(ctx, conn, id, "blocked", "The session was paused by a restart; continue manually.")
				}
				if !limitResumeEligible(row) {
					return nil
				}
				asks, e := db.ListOpenAsks(ctx, a.dbPool, id)
				if e != nil {
					return e
				}
				if len(asks) > 0 {
					settleAutoStop = true
					return db.SetLimitResumeState(ctx, conn, id, "blocked", "The session needs an answer; continue manually.")
				}
				dc := a.hub.FindDaemonForSession(id)
				if dc == nil && row.Status != "disconnected" {
					return nil
				} // sleeping workstation
				if dc == nil {
					if derefOrEmpty(row.Runtime) != "cluster" || a.hub.JobManager() == nil {
						settleAutoStop = true
						return db.SetLimitResumeState(ctx, conn, id, "blocked", "The runtime is unavailable; continue manually.")
					}
					if state.AuthSessionID == "" || derefOrEmpty(row.SpawningAccountID) == "" {
						settleAutoStop = true
						return db.SetLimitResumeState(ctx, conn, id, "blocked", "Sign in and enable continuation again.")
					}
					if _, problem := grantResumeProblem(ctx, a.dbPool, id, derefOrEmpty(row.SpawningAccountID), derefOrEmpty(row.SpawningAccountID), state.AuthSessionID); problem != nil {
						settleAutoStop = true
						return db.SetLimitResumeState(ctx, conn, id, "blocked", "MCP authorization requires manual continuation.")
					}
				}
				dispatchErr := withParentInputGuard(ctx, a.dbPool, id, func() error {
					attemptID := newUUID()
					claimed, e := db.ClaimLimitResumeAttempt(ctx, conn, id, attemptID)
					if e != nil || !claimed {
						return e
					}
					dispatched = true
					limitResumeAudit(ctx, a.hub, a.dbPool, id, messageEventID(attemptID, "limit-resume-dispatch"), "Automatic continuation requested after the provider usage limit reset.")
					if dc != nil {
						raw, _ := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: id, Text: limitResumePrompt, Source: limitResumeSource, ExpectedTurnID: state.TurnID})
						if a.hub.trySendToDaemon(dc, raw) {
							return nil
						}
						settleAutoStop = true
						return db.SetLimitResumeState(ctx, conn, id, "blocked", "The runtime could not accept continuation; continue manually.")
					}
					// The strict path validates retained owner authority at actual credential
					// fetch and forbids operator fallback even if the proof expired meanwhile.
					if e := resumeClusterSessionStrict(ctx, a.hub, a.dbPool, id, limitResumePrompt, derefOrEmpty(row.SpawningAccountID), state.AuthSessionID); e != nil {
						settleAutoStop = true
						return db.SetLimitResumeState(ctx, conn, id, "blocked", "The pod could not resume with your authorization; sign in and continue manually.")
					}
					return nil
				})
				if errors.Is(dispatchErr, db.ErrDelegationEnded) {
					dispatched = false
					settleAutoStop = false
					return db.SetLimitResumeState(ctx, conn, id, "blocked", "The session's parent ended; this child cannot resume.")
				}
				return dispatchErr
			})
		})
		if err != nil && !errors.Is(err, db.ErrLimitResumeBusy) {
			log.Printf("limit resume %s: %v", id, err)
		}
		if settleAutoStop {
			a.autoStopOnTurnDone(id) //nolint:contextcheck // auto-stop owns a bounded cleanup context independent of the scheduler
		}
		if dispatched {
			return
		}
	}
}

// Audit text is fixed server prose; never copy provider errors into these notes.
func limitResumeAudit(ctx context.Context, h *Hub, pool *pgxpool.Pool, id, eventID, text string) {
	if h != nil {
		recordServerAgentEvent(ctx, h, pool, id, eventID, "note", messagingPayload{Body: text})
		return
	}
	raw, _ := json.Marshal(messagingPayload{Body: text})
	if _, _, err := db.AppendAgentEventAt(ctx, pool, id, eventID, "note", string(raw), time.Now()); err != nil {
		log.Printf("limit resume audit %s: %v", id, err)
	}
}

func engineLimitResumeSupported(engine string) bool {
	descriptor, ok := engines.Lookup(engine)
	return ok && descriptor.LimitResume
}
