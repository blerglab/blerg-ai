package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// Run-the-board: a deterministic server-side dispatcher. While a board run
// is active it drains the ready column top-down, keeping up to the board's
// `concurrency` cards in flight (default 1), through the existing machinery:
// spawn worker → PR → review → adversarial review → (auto_merge) merge. A worker
// that goes quiet gets one respawn, then the card is flagged stuck and the
// queue moves on. A run stays active — idling when ready is empty and
// nothing is in flight — until a human stops it; new cards dropped into
// ready are picked up on the next tick with no click required.
//
// Concurrency is a property of the BOARD (boards.concurrency, PATCHable), not
// of the run, and it is re-read on every tick rather than snapshotted when Run
// was pressed. It is the operator's cross-board priority dial — one Claude
// account, many boards, so raising the board being pushed on is how "this
// project matters right now" gets expressed — and a dial you have to stop and
// restart a run to turn (losing that run's counters) is not one. At 0 the
// board is PARKED: the run stays active, in-flight cards finish and still
// auto-merge, and nothing new is dispatched.
//
// It is a per-board SHAPING knob and emphatically not a spend or resource
// ceiling. Two reasons, both worth knowing before turning it up:
//
//   - There is no global cap above this loop. runTick dispatches every running
//     board independently, so effective parallelism is the SUM across boards —
//     eleven boards at 3 is up to 33 concurrent workers, not 3. A global,
//     session-aware limit is card #46's job, not this one's.
//   - inflight counts CARDS in the work column (runCounts), not live sessions.
//     Reviewer sessions spawned on entry into review, discuss sessions and
//     board chats are all live agent work this number cannot see, so
//     concurrency N does not mean N live sessions.
//
// Dependent cards ARE serialised: TopDispatchable skips any card whose hard
// dependencies have not landed, so no pass — at any concurrency — can start a
// card and its own blocker together. It skips rather than stalls, so one
// blocked card never halts the queue behind it.
//
// The auto-merge sweep runs for every board (not just active runs): a card
// in review with auto_merge, a PR, and a fresh adversarial approval merges
// without a human click.

const workerQuietGrace = 10 * time.Minute

// isCapacityErr: the runner refused for lack of slots — retry later, never
// stuck-flag the card for it.
func isCapacityErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "cap reached") || strings.Contains(msg, "HTTP 503")
}

func (a *API) boardRunRoutes(mux *http.ServeMux, authed func(func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc) {
	mux.HandleFunc("POST /api/boards/{id}/run", authed(a.handleStartRun))
	mux.HandleFunc("POST /api/boards/{id}/run/stop", authed(a.handleStopRun))
	mux.HandleFunc("GET /api/boards/{id}/run", authed(a.handleGetRun))
}

func (a *API) handleStartRun(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if a.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no runner configured")
		return
	}
	// A run that cannot start a single session is not a run: say so now,
	// while a human is looking, rather than stamping the first ready card.
	if !a.checkStartIdentity(r.Context(), w, boardID) {
		return
	}
	_, err := a.Pool.Exec(r.Context(), `
		INSERT INTO board_runs (board_id) VALUES ($1)
		ON CONFLICT (board_id) WHERE state = 'running' DO NOTHING`, boardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	// unstick everything for a fresh run — a human pressing Run is consent
	_, _ = a.Pool.Exec(r.Context(),
		`UPDATE cards SET stuck_at = NULL, run_attempts = 0 WHERE board_id = $1 AND stuck_at IS NOT NULL`, boardID)
	// and forget any capacity refusal: explicit human intent outranks a
	// cached "the runner was full a minute ago". Without this, pressing Run —
	// which several service comments tell the user to do — is a no-op until
	// the backoff window lapses, which is indistinguishable from broken.
	a.noteCapacityAvailable(r.Context())
	a.Hub.Broadcast(boardID, "card_changed")
	go a.runTick(context.WithoutCancel(r.Context())) // detached: the tick must outlive this response
	a.writeRunStatus(r.Context(), w, boardID)
}

func (a *API) handleStopRun(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	_, _ = a.Pool.Exec(r.Context(), `
		UPDATE board_runs SET state = 'stopped', finished_at = now()
		WHERE board_id = $1 AND state = 'running'`, boardID)
	a.Hub.Broadcast(boardID, "card_changed")
	a.writeRunStatus(r.Context(), w, boardID)
}

func (a *API) handleGetRun(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	a.writeRunStatus(r.Context(), w, boardID)
}

func (a *API) writeRunStatus(ctx context.Context, w http.ResponseWriter, boardID string) {
	// The dial rides along with the counts so the UI can tell a PARKED run
	// (running, concurrency 0, dispatching nothing) from a stopped one without
	// a second request — from the board they look identical otherwise, and a
	// parked board that reads as a hung board is the whole risk of allowing 0.
	concurrency := a.boardConcurrency(ctx, boardID)

	var state string
	var done, stuck int
	var started *time.Time
	err := a.Pool.QueryRow(ctx, `
		SELECT state, cards_done, cards_stuck, started_at FROM board_runs
		WHERE board_id = $1 ORDER BY started_at DESC LIMIT 1`, boardID).
		Scan(&state, &done, &stuck, &started)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"state": "none", "concurrency": concurrency})
		return
	}
	ready, inflight, blocked := a.runCounts(ctx, boardID)
	writeJSON(w, http.StatusOK, map[string]any{
		"state": state, "cards_done": done, "cards_stuck": stuck,
		"ready": ready, "in_flight": inflight, "blocked": blocked, "started_at": started,
		"concurrency": concurrency,
		// Why a run with ready work can look asleep: dispatch waits for a
		// session slot rather than claiming a card it cannot start. Before,
		// the wait announced itself by flapping a card between columns. This
		// is the other half of the parked/hung question the dial answers —
		// concurrency 0 is blerg-board holding back, runner_full is the runner
		// holding blerg-board back.
		"runner_full": a.runnerOutOfSlots(ctx),
	})
}

// boardConcurrency reads the board's dispatch limit, falling back to the
// column default on any error. Reporting 1 for a board that can't be read beats
// reporting 0, which would paint a live board as parked.
func (a *API) boardConcurrency(ctx context.Context, boardID string) int {
	concurrency := 1
	_ = a.Pool.QueryRow(ctx, `SELECT concurrency FROM boards WHERE id = $1`, boardID).Scan(&concurrency)
	return concurrency
}

func isReadyColumn(name string) bool {
	return strings.Contains(strings.ToLower(name), "ready")
}

// runCounts: ready-column cards, non-stuck work-column cards, and how many of
// the ready ones the dispatcher is skipping because a hard dependency has not
// landed. `blocked` exists so a board waiting on its own dependency graph reads
// as blocked rather than as inexplicably idle.
func (a *API) runCounts(ctx context.Context, boardID string) (ready, inflight, blocked int) {
	cols, err := db.ListColumns(ctx, a.Pool, boardID)
	if err != nil {
		return 0, 0, 0
	}
	for _, c := range cols {
		switch {
		case isReadyColumn(c.Name):
			_ = a.Pool.QueryRow(ctx, `
				SELECT count(*) FROM cards
				WHERE column_id = $1 AND archived_at IS NULL`, c.ID).Scan(&ready)
			blocked, _ = db.CountBlocked(ctx, a.Pool, c.ID)
		case isWorkColumn(c.Name):
			var n int
			_ = a.Pool.QueryRow(ctx, `
				SELECT count(*) FROM cards
				WHERE column_id = $1 AND archived_at IS NULL AND stuck_at IS NULL`, c.ID).Scan(&n)
			inflight += n
		}
	}
	return ready, inflight, blocked
}

// StartBoardRuns launches the dispatcher ticker.
func (a *API) StartBoardRuns(ctx context.Context) {
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.runTick(ctx)
			}
		}
	}()
}

// RunTick runs one dispatcher pass synchronously — exported for tests that
// need deterministic dispatch instead of waiting on the 20s ticker.
func (a *API) RunTick(ctx context.Context) { a.runTick(ctx) }

func (a *API) runTick(ctx context.Context) {
	if a.runner == nil {
		return
	}
	// One pass per process at a time. Passes overlap routinely — pressing Run
	// fires a tick of its own while the 20s ticker may already be mid-pass —
	// and a second concurrent pass can only redo the first's work or race it.
	// This also bounds the pooled connections withBoardDispatch holds open to
	// one per replica, so a server with several running boards can't park its
	// whole connection pool on dispatch locks. The per-board lock below is the
	// one that holds across replicas; this is the cheap in-process half.
	if !a.tickMu.TryLock() {
		return
	}
	defer a.tickMu.Unlock()

	a.autoMergeSweep(ctx)
	a.reapAllQuietWork(ctx)
	a.reviveStalledReviews(ctx)
	a.reapIdleSessions(ctx)

	// concurrency comes off the board, not the run: turning the dial has to
	// take effect on the run that is already going (see the package comment)
	rows, err := a.Pool.Query(ctx, `
		SELECT br.id, br.board_id, b.concurrency
		FROM board_runs br JOIN boards b ON b.id = br.board_id
		WHERE br.state = 'running'`)
	if err != nil {
		return
	}
	type run struct {
		id          int64
		boardID     string
		concurrency int
	}
	var runs []run
	for rows.Next() {
		var r run
		if rows.Scan(&r.id, &r.boardID, &r.concurrency) == nil {
			runs = append(runs, r)
		}
	}
	rows.Close()

	for _, r := range runs {
		a.dispatchBoard(ctx, r.id, r.boardID, r.concurrency)
	}
}

// withBoardDispatch runs fn holding a per-board advisory lock, and does
// nothing at all when another pass already holds it — dispatch is a claim, so
// two passes racing must not both act. Without it two overlapping ticks read
// the same top-of-ready card and both spawn a worker on it: two sessions
// editing one repo, with only one of the two moves into the work column
// showing for it. The lock has to span the spawn, not just the move, because
// the spawn is what happens first — see the dispatch loop on why. Overlap is
// routine: pressing Run fires a tick of its own while the 20s ticker may
// already be mid-pass.
//
// The lock lives in Postgres, not in this process, so it holds across
// replicas too. Keys are hashtext(board id) — same convention as
// RunMigrations — so a hash collision between two boards costs at most a
// skipped pass on one of them, never a missed card: the next tick retries.
func (a *API) withBoardDispatch(ctx context.Context, boardID string, fn func()) {
	acquired, err := a.Pool.Acquire(ctx)
	if err != nil {
		return
	}
	key := "blerg_board_dispatch:" + boardID
	var held bool
	// the lock is session-scoped, so every statement below must run on THIS
	// connection — a.Pool.QueryRow would take an arbitrary one
	if err := acquired.QueryRow(ctx,
		`SELECT pg_try_advisory_lock(hashtext($1))`, key).Scan(&held); err != nil || !held {
		acquired.Release()
		return
	}
	defer func() {
		// unlock even when ctx died mid-pass (shutdown): a leaked advisory
		// lock would wedge the board's dispatch until that connection closes
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := acquired.Exec(uctx, `SELECT pg_advisory_unlock(hashtext($1))`, key); err != nil {
			// the connection owns the lock; closing it is the only other way
			// to hand the lock back, so don't return it to the pool
			log.Printf("board run: unlock dispatch for board %s: %v", boardID, err)
			_ = acquired.Hijack().Close(uctx)
			return
		}
		acquired.Release()
	}()
	fn()
}

func (a *API) dispatchBoard(ctx context.Context, runID int64, boardID string, concurrency int) {
	a.withBoardDispatch(ctx, boardID, func() {
		a.dispatchBoardLocked(ctx, runID, boardID, concurrency)
	})
}

// dispatchBoardLocked drains the ready column. Callers hold the board's
// dispatch lock.
func (a *API) dispatchBoardLocked(ctx context.Context, runID int64, boardID string, concurrency int) {
	board, err := db.GetBoard(ctx, a.Pool, boardID)
	if err != nil {
		return
	}
	cols, err := db.ListColumns(ctx, a.Pool, boardID)
	if err != nil {
		return
	}
	var readyCol, workCol string
	for _, c := range cols {
		if readyCol == "" && isReadyColumn(c.Name) {
			readyCol = c.ID
		}
		if workCol == "" && isWorkColumn(c.Name) {
			workCol = c.ID
		}
	}
	if readyCol == "" || workCol == "" {
		return
	}

	a.reapQuietWorkers(ctx, runID, board, workCol)

	_, inflight, _ := a.runCounts(ctx, boardID)
	// hard iteration cap: no single tick may loop unboundedly, and the run
	// state is re-checked so Pause takes effect mid-dispatch. The cap bounds
	// ONE TICK, not the board — a concurrency above 20 fills up over several
	// ticks rather than being silently clamped to 20.
	//
	// At concurrency 0 the loop never enters, which is the whole of "parked":
	// the reap above still runs, in-flight cards still finish and auto-merge,
	// and the run keeps its counters. Nothing else is needed for it, but it is
	// a supported state rather than a happy accident — negatives are rejected
	// at the API (db.checkConcurrency) and by the column's CHECK.
	for guard := 0; inflight < concurrency && guard < 20; guard++ {
		var state string
		if a.Pool.QueryRow(ctx, `SELECT state FROM board_runs WHERE id = $1`, runID).
			Scan(&state) != nil || state != "running" {
			break
		}
		if a.runnerOutOfSlots(ctx) {
			// The runner has no room and blerg-board has not seen a slot free since
			// it said so. Skip the pass entirely: the ready column is left
			// exactly as it was, and the tick after a slot opens picks it up.
			// This is checked BEFORE picking a card on purpose — the pick is
			// now a dependency walk (TopDispatchable), and there is no point
			// paying for it, or for the prompt render after it, to arrive at a
			// runner that has already said no.
			break
		}
		card, err := db.TopDispatchable(ctx, a.Pool, readyCol)
		if err != nil {
			break // ready holds nothing startable — empty, or all blocked/stuck
		}
		if len(card.Repos) == 0 {
			// can't run repo-less cards — flag stuck (once) and move on
			tag, err := a.Pool.Exec(ctx,
				`UPDATE cards SET stuck_at = now() WHERE id = $1 AND stuck_at IS NULL`, card.ID)
			if err == nil && tag.RowsAffected() > 0 {
				_ = db.AppendComment(ctx, a.Pool, card.ID,
					"Board run: card has no repo, so no session can work it — flagged stuck. Add a repo and press Run to retry.",
					db.EventMeta{Actor: "service"})
			}
			_, _ = a.Pool.Exec(ctx,
				`UPDATE board_runs SET cards_stuck = cards_stuck + 1 WHERE id = $1`, runID)
			a.Hub.Broadcast(boardID, "card_changed")
			continue
		}
		// Spawn first, claim after. A claim the runner then refuses has to be
		// undone, and the undo is not free: two `moved` events per refused
		// tick, every 20s for as long as the runner is full, burying the
		// card's real history. Starting the session IS the proof that a slot
		// existed, and this loop holds the board's dispatch lock across the
		// spawn, so no other pass can take the card in the gap.
		prompt, err := a.buildCardPrompt(board, card, "")
		if err != nil {
			log.Printf("board run: render prompt for card #%d: %v", card.Number, err)
			_, _ = a.Pool.Exec(ctx, `UPDATE cards SET stuck_at = now() WHERE id = $1`, card.ID)
			continue
		}
		if _, _, spawnErr := a.spawnSession(ctx, board, card, "worker", prompt, ""); spawnErr != nil {
			log.Printf("board run: spawn for card #%d: %v", card.Number, spawnErr)
			if isAutomationErr(spawnErr) {
				// The board cannot start ANY session (no automation token,
				// or a dead one) — not this card's fault, and every card
				// after it would fail the same way. Stop the run, leave the
				// card where it is, and say why on it, once.
				a.haltRunForAutomation(ctx, runID, card, spawnErr)
				break
			}
			if isCapacityErr(spawnErr) {
				// Runner is full — a global condition, not this card's fault.
				// Nothing was claimed, so there is nothing to undo: the card
				// stays put, unmarked, and spawnSession has recorded the
				// refusal so the following ticks don't ask again.
				break
			}
			// This card's own problem: flag it stuck where it stands (a stuck
			// card drops out of TopDispatchable, so the queue moves on) and say
			// why, since it never reaches the work column to show for it.
			tag, err := a.Pool.Exec(ctx,
				`UPDATE cards SET stuck_at = now() WHERE id = $1 AND stuck_at IS NULL`, card.ID)
			if err == nil && tag.RowsAffected() > 0 {
				_ = db.AppendComment(ctx, a.Pool, card.ID,
					"Board run: no session could be started for this card ("+spawnErr.Error()+
						") — flagged stuck. Press Run on the board to retry.",
					db.EventMeta{Actor: "service"})
				// count it, like the repo-less branch above: the run's "N
				// stuck" is what tells a human to come look
				_, _ = a.Pool.Exec(ctx,
					`UPDATE board_runs SET cards_stuck = cards_stuck + 1 WHERE id = $1`, runID)
			}
			a.Hub.Broadcast(boardID, "card_changed")
			continue
		}
		// The slot is ours and the session is live: now claim the card. This
		// claim must land even if the dispatcher's context dies right here —
		// it is the server's context, so a deploy does exactly that mid-pass.
		// A started session with its card still in ready is the one state that
		// double-spawns: the next pass reads the same top-of-ready card and
		// puts a second worker on the same repo. Detaching costs a few
		// statements past shutdown and buys back the invariant the dispatch
		// lock only guarantees while this process lives.
		//
		// The claim is GUARDED on the card still being in ready. Spawning
		// first put a several-second pod start between reading the card and
		// writing the move, and a human editing the board in that window is
		// not a race to win: an unguarded move would drag a card they had just
		// filed away back into the work column, and MoveCard clears
		// archived_at, so it would un-archive one they had just archived. That
		// is the same "dispatch moved a card nobody asked it to move" this
		// change exists to stop.
		cctx, done := detached(ctx, 15*time.Second)
		if _, err := db.ClaimCard(cctx, a.Pool, card.ID, readyCol, workCol,
			db.EventMeta{Actor: "service"}); errors.Is(err, db.ErrCardMoved) {
			// A human (or another writer) moved or archived the card while the
			// session was starting. Their placement stands — the session is
			// real and linked to the card either way, so say where it went and
			// leave the board alone. The card is no longer in ready, so no
			// later pass picks it up again; count the slot as spent.
			log.Printf("board run: card #%d left ready during spawn — keeping its new place", card.Number)
			_, _ = a.Pool.Exec(cctx,
				`UPDATE cards SET run_attempts = run_attempts + 1 WHERE id = $1`, card.ID)
			_ = db.AppendComment(cctx, a.Pool, card.ID,
				"Board run: a worker session was starting for this card when it left the ready column — "+
					"the board run did not move it back. The session is live and linked above; close it "+
					"from the card if this card is no longer meant to be worked.",
				db.EventMeta{Actor: "service"})
			done()
			inflight++
			a.Hub.Broadcast(boardID, "card_changed")
			continue
		} else if err != nil {
			// Postgres itself is refusing. Flagging the card stuck is what
			// stops the next tick from spawning a second worker on it, and
			// puts the orphaned session in front of a human.
			log.Printf("board run: claim card #%d after spawn: %v", card.Number, err)
			_, _ = a.Pool.Exec(cctx,
				`UPDATE cards SET stuck_at = now(), run_attempts = run_attempts + 1 WHERE id = $1`, card.ID)
			_ = db.AppendComment(cctx, a.Pool, card.ID,
				"Board run: a worker session started for this card but the card could not be moved "+
					"into the work column ("+err.Error()+") — flagged stuck so a second worker is not "+
					"started on top of the first. The session is linked above.",
				db.EventMeta{Actor: "service"})
			done()
			a.Hub.Broadcast(boardID, "card_changed")
			break
		}
		_, _ = a.Pool.Exec(cctx,
			`UPDATE cards SET run_attempts = run_attempts + 1 WHERE id = $1`, card.ID)
		done()
		inflight++
		a.Hub.Broadcast(boardID, "card_changed")
	}
}

// stopRun ends a run the way the Stop button does. Reports whether this call
// stopped it (false: it was already stopped).
func (a *API) stopRun(ctx context.Context, runID int64) bool {
	tag, err := a.Pool.Exec(ctx, `
		UPDATE board_runs SET state = 'stopped', finished_at = now()
		WHERE id = $1 AND state = 'running'`, runID)
	return err == nil && tag.RowsAffected() > 0
}

// haltRunForAutomation stops a run whose board cannot start sessions and
// tells the card it was about to start why nothing happened. The card is not
// flagged stuck: nothing is wrong with it, and it is picked up as normal once
// the board has a working automation token and someone presses Run.
func (a *API) haltRunForAutomation(ctx context.Context, runID int64, card db.Card, cause error) {
	if a.stopRun(ctx, runID) {
		_ = db.AppendComment(ctx, a.Pool, card.ID,
			"Board run stopped before starting this card: "+automationErrText(cause)+
				". The card was left where it is — press Run on the board once that is fixed.",
			db.EventMeta{Actor: "service"})
	}
	a.Hub.Broadcast(card.BoardID, "card_changed")
}

// reapQuietWorkers: a work-column card whose sessions have all gone quiet
// past the grace period gets one respawn, then a stuck flag.
func (a *API) reapQuietWorkers(ctx context.Context, runID int64, board db.Board, workColID string) {
	rows, err := a.Pool.Query(ctx, `
		SELECT c.id FROM cards c
		WHERE c.column_id = $1 AND c.archived_at IS NULL AND c.stuck_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM runner_sessions rs
			WHERE rs.card_id = c.id AND rs.role = 'worker'
			  AND rs.lifecycle IN ('starting','running','waiting')
			  AND (rs.last_activity_at IS NULL OR rs.last_activity_at > now() - $2::interval)
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM runner_sessions rs2
			WHERE rs2.card_id = c.id
			  AND rs2.last_activity_at > now() - $2::interval
		  )
		  AND c.updated_at < now() - $2::interval`,
		workColID, workerQuietGrace.String())
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	for _, id := range ids {
		card, err := db.GetCard(ctx, a.Pool, id)
		if err != nil {
			continue
		}
		if card.RunAttempts < 2 && len(card.Repos) > 0 {
			if a.runnerOutOfSlots(ctx) {
				// No slot to respawn into. A card that only lacks capacity is
				// not stuck — leave it for a tick that has room, exactly as
				// dispatch does with the ready column.
				continue
			}
			log.Printf("board run: card #%d went quiet — respawning (attempt %d)", card.Number, card.RunAttempts+1)
			prompt, err := a.buildCardPrompt(board, card,
				"A previous session on this card went quiet without finishing. Read the card's comments and any existing branch/PR first, continue from where it left off, and finish the card.")
			if err == nil {
				_, _, spawnErr := a.spawnSession(ctx, board, card, "worker", prompt, "")
				if spawnErr == nil {
					// detached for the same reason as the claim in dispatch:
					// the attempt is spent whatever happens to this context,
					// and a lost count is a card respawned past its limit
					rctx, done := detached(ctx, 15*time.Second)
					_, _ = a.Pool.Exec(rctx,
						`UPDATE cards SET run_attempts = run_attempts + 1, updated_at = now() WHERE id = $1`, card.ID)
					done()
					continue
				}
				if isCapacityErr(spawnErr) {
					// The runner filled up between the check and the ask. Same
					// answer: transient, global, and never this card's flag.
					log.Printf("board run: respawn for card #%d: %v", card.Number, spawnErr)
					continue
				}
				if isAutomationErr(spawnErr) {
					// No replacement worker can start on this board at all.
					// The card is stuck either way; say the real reason rather
					// than "went quiet", and stop the run if one is going.
					log.Printf("board run: respawn for card #%d: %v", card.Number, spawnErr)
					if runID != 0 {
						a.stopRun(ctx, runID)
					}
					tag, err := a.Pool.Exec(ctx,
						`UPDATE cards SET stuck_at = now() WHERE id = $1 AND stuck_at IS NULL`, card.ID)
					if err == nil && tag.RowsAffected() > 0 {
						_ = db.AppendComment(ctx, a.Pool, card.ID,
							"Board run: this card's worker went quiet and no replacement could be started — "+
								automationErrText(spawnErr)+". Flagged stuck; press Run on the board to retry once that is fixed.",
							db.EventMeta{Actor: "service"})
					}
					a.Hub.Broadcast(card.BoardID, "card_changed")
					continue
				}
			} else {
				log.Printf("board run: render prompt for card #%d: %v", card.Number, err)
			}
		}
		_, _ = a.Pool.Exec(ctx, `UPDATE cards SET stuck_at = now() WHERE id = $1`, card.ID)
		_, _ = a.Pool.Exec(ctx,
			`UPDATE board_runs SET cards_stuck = cards_stuck + 1 WHERE id = $1`, runID)
		_ = db.AppendComment(ctx, a.Pool, card.ID,
			fmt.Sprintf("Board run: %d session attempt(s) went quiet without finishing — flagged stuck; the queue moves on. Press Run on the board to retry stuck cards.", card.RunAttempts),
			db.EventMeta{Actor: "service"})
		a.Hub.Broadcast(card.BoardID, "card_changed")
	}
}

// autoMergeSweep merges review-column cards that earned it: auto_merge set,
// a PR attached, and the newest adversarial verdict is an approval fresher
// than the newest worker activity.
//
// Every way this can NOT merge is reported on the card (noteAutoMergeBlocked),
// because a card that has earned the merge and silently doesn't get it is
// indistinguishable, from the board, from one still under review. Note the
// query does not exclude stuck cards on purpose: flagging stuck says "a human
// should look", not "stop trying", so a jam that clears on its own still
// merges by itself.
func (a *API) autoMergeSweep(ctx context.Context) {
	rows, err := a.Pool.Query(ctx, `
		SELECT DISTINCT c.id FROM cards c
		JOIN board_columns bc ON bc.id = c.column_id
		WHERE c.auto_merge AND c.archived_at IS NULL
		  AND lower(bc.name) LIKE '%review%'`)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	// cards that left the sweep take their in-memory clocks with them
	defer a.forgetCIErrors(ids)
	defer a.forgetCIReruns(ids)

	// One read per board per pass, not per card. Scoped to the pass on
	// purpose: a policy changed between sweeps takes effect on the next one,
	// twenty seconds later, with no cache to invalidate.
	boardCIPolicy := map[string]bool{}

	for _, id := range ids {
		card, err := db.GetCard(ctx, a.Pool, id)
		if err != nil {
			continue
		}
		// Approval first, PR second. Choosing the PR can cost GitHub calls
		// (mergeTargetPR asks about each link when a card carries more than
		// one), and a card that hasn't earned its merge shouldn't spend them —
		// the sweep runs every 20 seconds over every review-column card on
		// every board.
		blockedFor, approved := a.hasFreshApproval(ctx, card)
		if !approved {
			continue
		}
		prURL, err := mergeTargetPR(ctx, card)
		if err != nil {
			log.Printf("auto-merge: card #%d: choosing the PR to merge: %v", card.Number, err)
			if errors.Is(err, errNoOpenPR) {
				// Terminal, and timed by the approval like the other terminal
				// reason: no push is coming to reopen a merged PR, so this is
				// as true in an hour as it is now.
				a.noteAutoMergeBlocked(ctx, card, blockedFor, blockNoOpenPR)
				continue
			}
			// GitHub was unreadable rather than unambiguous. Same clock as the
			// CI-status errors below — the question both answer is "how long
			// has GitHub been failing us", not "how long has this card been
			// eligible" — so a lone 502 stays off the card.
			a.noteAutoMergeBlocked(ctx, card, a.ciErrorHeld(card.ID, true), blockPRStateError(err))
			continue
		}
		if prURL == "" {
			continue // no PR on the card at all: nothing to merge, as before
		}
		ci, err := prCIReport(ctx, prURL)
		if err != nil {
			log.Printf("auto-merge: card #%d: CI status: %v", card.Number, err)
			// This block is timed by how long the ERROR has held, not by how
			// long the card has been eligible: a single 5xx on an hour-old
			// approval is a blip, not an hour-old jam.
			a.noteAutoMergeBlocked(ctx, card, a.ciErrorHeld(card.ID, true), blockCIError(err))
			continue
		}
		a.ciErrorHeld(card.ID, false) // GitHub answered — any run of errors ends here
		// A board whose repo has no CI can opt out of waiting for one. The
		// policy read is inside the ciNone guard so the common case — a board
		// with CI, checks reported — costs no extra query: only a head with
		// nothing reported ever asks what the board's policy is.
		if ci.state == ciNone {
			ci = ciAfterPolicy(ci, a.ciRequiredFor(ctx, boardCIPolicy, card.BoardID))
		}
		if ci.state != ciGreen {
			reruns := 0
			if ci.state == ciRed {
				var settled bool
				reruns, settled = a.ciRerunsFor(ctx, card.ID, ci.headSHA)
				// settled is the durable half of the interval, and it only
				// knows about re-runs that HAPPENED. mayAttemptRerun is the
				// other half: an attempt that re-queues nothing — no Actions
				// run behind the red, or a token without actions:write —
				// writes no comment, so without it the sweep would re-ask
				// GitHub every twenty seconds for as long as the head stays
				// red, which is the same never-changing loop this whole
				// change exists to end. settled is also false when the ledger
				// could not be read at all — an unknown budget is not one to
				// spend.
				if reruns < maxCIReruns && settled && a.mayAttemptRerun(card.ID, ci.headSHA) &&
					a.rerunRedCI(ctx, card, prURL, ci, reruns) {
					continue // jobs re-queued; the next sweep reads a fresh result
				}
			}
			a.noteAutoMergeBlocked(ctx, card, blockedFor, ciBlockFor(ci, reruns))
			continue
		}
		sha, err := mergePR(ctx, prURL)
		if err != nil {
			log.Printf("auto-merge: card #%d: %v", card.Number, err)
			// Only a bounce that can never work falls through to the failure
			// comment below. A bounce that merely lost this round — a DB error
			// mid-move — leaves the card alone for the next sweep to retry,
			// exactly as it did before conflict bouncing existed.
			if isConflictErr(err) && card.RunAttempts < conflictBounceLimit &&
				a.bounceForConflict(ctx, card, prURL, conflictAutoMerge) != bounceImpossible {
				continue
			}
			_ = db.AppendComment(ctx, a.Pool, card.ID,
				"Auto-merge failed: "+err.Error()+" — left in review for a human.", db.EventMeta{Actor: "service"})
			// don't retry forever: clear the flag so the human decides
			_, _ = a.Pool.Exec(ctx, `UPDATE cards SET auto_merge = false WHERE id = $1`, card.ID)
			continue
		}
		_ = db.AppendComment(ctx, a.Pool, card.ID,
			fmt.Sprintf("Auto-merged after adversarial approval — %s as %.7s.%s", prURL, sha,
				mergedWithoutCINote(ci)),
			db.EventMeta{Actor: "service"})
		// the jam, if there was one, is over — don't carry a stuck chip to done
		_, _ = a.Pool.Exec(ctx,
			`UPDATE cards SET merged_sha = $2, merged_at = now(), stuck_at = NULL WHERE id = $1`,
			card.ID, sha)
		a.finishCard(ctx, card)
		_, _ = a.Pool.Exec(ctx, `
			UPDATE board_runs SET cards_done = cards_done + 1
			WHERE board_id = $1 AND state = 'running'`, card.BoardID)
		a.Hub.Broadcast(card.BoardID, "card_changed")
	}
}

// ciAfterPolicy applies a board's ci_policy to a CI read.
//
// The whole policy, in one place: an ABSENT result (ciNone — no check-runs and
// no commit statuses on the head) becomes green on a board that does not
// require CI, and is marked unchecked so everything downstream knows no
// machine ever looked. Every other state is returned untouched, which is the
// property that matters most here — red stays red and pending stays pending
// under either policy, so no value of ci_policy can merge a failing PR. The
// setting governs the absence of a check, never the verdict of one.
func ciAfterPolicy(ci ciReport, required bool) ciReport {
	if ci.state != ciNone || required {
		return ci
	}
	ci.state = ciGreen
	ci.unchecked = true
	return ci
}

// mergedWithoutCINote is the sentence the merge comment gains when the board's
// ci_policy let an unchecked head through. Empty for a genuine green.
//
// It exists so the card trail can never be misread as "a machine check
// passed". Whoever reads this card in three months — or bisects to it after a
// regression — is entitled to know that the review was the only gate, and that
// this was a board setting rather than an accident.
func mergedWithoutCINote(ci ciReport) string {
	if !ci.unchecked {
		return ""
	}
	return " No CI ran: the repo reports no checks for this head and the board's ci_policy is " +
		db.CIIfPresent + ", so the adversarial review was the only gate."
}

// ciRequiredFor answers whether THIS board insists on a green machine check
// before auto-merge, memoising per sweep pass through cache.
//
// Fail-safe by construction: an unreadable board answers "required", which is
// the strict policy and the behaviour that predates ci_policy entirely. A
// database blip must not become a merge that nobody authorised, and the cost
// of being wrong in this direction is a card that waits twenty seconds longer.
func (a *API) ciRequiredFor(ctx context.Context, cache map[string]bool, boardID string) bool {
	if required, ok := cache[boardID]; ok {
		return required
	}
	required := true
	board, err := db.GetBoard(ctx, a.Pool, boardID)
	if err != nil {
		log.Printf("auto-merge: ci_policy for board %s: %v — treating CI as required", boardID, err)
	} else {
		required = board.CIPolicy != db.CIIfPresent
	}
	cache[boardID] = required
	return required
}

// hasFreshApproval: newest "Adversarial review" comment approves, and is
// newer than the newest worker activity (i.e. approves THIS iteration). Its
// age comes back with it — the approval is the moment the card became
// merge-eligible, so its age is how long a blocked card has been blocked, the
// clock noteAutoMergeBlocked measures a jam by. Postgres computes that age, so
// it can't be thrown off by skew between the database's clock and this
// process's.
func (a *API) hasFreshApproval(ctx context.Context, card db.Card) (time.Duration, bool) {
	var text string
	var at time.Time
	var ageSec float64
	err := a.Pool.QueryRow(ctx, `
		SELECT data->>'text', created_at, extract(epoch from now() - created_at) FROM card_events
		WHERE card_id = $1 AND type = 'comment' AND data->>'text' LIKE 'Adversarial review%'
		ORDER BY created_at DESC LIMIT 1`, card.ID).Scan(&text, &at, &ageSec)
	if err != nil {
		return 0, false
	}
	m := verdictRE.FindStringSubmatch(text)
	if m == nil || !strings.EqualFold(m[1], "approve") {
		return 0, false
	}
	var workAt *time.Time
	_ = a.Pool.QueryRow(ctx, `
		SELECT max(last_activity_at) FROM runner_sessions
		WHERE card_id = $1 AND role = 'worker'`, card.ID).Scan(&workAt)
	return time.Duration(ageSec * float64(time.Second)), workAt == nil || at.After(workAt.Add(-time.Minute))
}

// ── auto-merge is blocked: say so on the card ────────────────────────────────

// How long a card that has earned its merge may sit blocked before the board
// itself shows the jam. Pending checks and red CI clear on their own — a run
// finishes, a worker pushes a fix — so they get an hour of quiet. A head commit
// that reports no checks at all never clears, so it gets only enough rope for
// GitHub to be a few minutes late registering a check-run that does exist.
const (
	autoMergeBlockedGrace = time.Hour
	autoMergeNoCIGrace    = 10 * time.Minute
)

// ciErrorHeld reports how long the current unbroken run of CI-status failures
// for this card has lasted, recording the start of a new run when one begins.
// Pass failed=false on any sweep that got an answer out of GitHub: that ends
// the run and returns zero.
//
// The clock has to live here rather than being read off the approval, because
// "how long has the card been merge-eligible" and "how long has GitHub been
// unreachable" are different questions, and only the second one should decide
// whether a 502 is worth a comment. It is deliberately in memory: a restart
// forgets the run and starts counting again, which errs toward silence, and a
// second replica sweeping the same card keeps its own count — the comment's
// rate limit and the stuck flag's WHERE clause are both in the database, so
// the worst a disagreement costs is a delayed note.
func (a *API) ciErrorHeld(cardID string, failed bool) time.Duration {
	a.ciErrMu.Lock()
	defer a.ciErrMu.Unlock()
	if !failed {
		delete(a.ciErrSince, cardID)
		return 0
	}
	if a.ciErrSince == nil {
		a.ciErrSince = map[string]time.Time{}
	}
	since, ok := a.ciErrSince[cardID]
	if !ok {
		a.ciErrSince[cardID] = time.Now()
		return 0
	}
	return time.Since(since)
}

// forgetCIErrors drops error clocks for cards the sweep no longer considers —
// merged, moved out of review, auto_merge cleared. Without it the map keeps an
// entry for every card that ever saw a GitHub error.
func (a *API) forgetCIErrors(swept []string) {
	a.ciErrMu.Lock()
	defer a.ciErrMu.Unlock()
	if len(a.ciErrSince) == 0 {
		return
	}
	live := make(map[string]bool, len(swept))
	for _, id := range swept {
		live[id] = true
	}
	for id := range a.ciErrSince {
		if !live[id] {
			delete(a.ciErrSince, id)
		}
	}
}

// ── a red head is not necessarily a broken PR ────────────────────────────────

// The re-run budget, and how it is spent.
//
// maxCIReruns is per head SHA, not per card: a push produces a new SHA and a
// new budget, which is right, because the new head is a genuinely different
// question. Two is the number that separates a flake from a breakage without
// pretending it separates it from a rare one — a check that fails three times
// in a row on unchanged code is a check the author has to look at.
//
// ciRerunSettle keeps the sweep from spending the whole budget in forty
// seconds. GitHub takes a moment to move the re-run jobs back to "queued", and
// until it does, the check-runs API keeps serving the failure that prompted
// the re-run; without this the second and third sweeps would read that same
// stale red as a fresh one and re-run again.
//
// ciStaleRed is when a red stops being news and starts being an old answer
// nothing has revisited. It exists for the comment's wording only.
const (
	maxCIReruns   = 2
	ciRerunSettle = 5 * time.Minute
	ciStaleRed    = 30 * time.Minute
)

// ciRerunPrefix opens the note recording a re-run, and — with the short SHA
// appended — is the LIKE pattern that counts them. The ledger is the card's
// own comment log rather than a map in this process on purpose: the budget has
// to survive a restart (an in-memory counter would hand a permanently broken
// PR a fresh two re-runs on every deploy), it has to be shared by every
// replica sweeping the same card, and a human reading the card should be able
// to see that auto-merge already tried this.
const ciRerunPrefix = "Auto-merge: re-ran the failed CI jobs on "

// ciRerunsFor reports how many re-runs auto-merge has already spent on this
// head, and whether the last one has had time to show up in GitHub's answers
// (true when there hasn't been one at all).
//
// The count is not only a budget: it is also what the red note says auto-merge
// has already tried. So an unreadable ledger answers "none, and not settled" —
// the false half of the pair is what declines the spend (the caller requires
// it before asking GitHub), and the zero is what keeps the note from telling
// the author about re-runs that may never have happened. Claiming the budget
// was spent would be the safe number and the wrong sentence, which is the
// exact failure this whole change is about.
func (a *API) ciRerunsFor(ctx context.Context, cardID, headSHA string) (int, bool) {
	var n int
	var ageSec *float64
	if err := a.Pool.QueryRow(ctx, `
		SELECT count(*), extract(epoch from now() - max(created_at)) FROM card_events
		WHERE card_id = $1 AND type = 'comment' AND data->>'text' LIKE $2`,
		cardID, ciRerunPrefix+shortSHA(headSHA)+"%").Scan(&n, &ageSec); err != nil {
		return 0, false
	}
	if n == 0 {
		return 0, true
	}
	return n, ageSec != nil && *ageSec >= ciRerunSettle.Seconds()
}

// ciRerunAttempt is the last head this card's sweep ASKED GitHub to re-run,
// and when it asked.
type ciRerunAttempt struct {
	headSHA string
	at      time.Time
}

// mayAttemptRerun reports whether the sweep should ask GitHub to re-run this
// head now, stamping the attempt when it says yes.
//
// This bounds the ASK; ciRerunsFor bounds the successes. The two are separate
// because an ask that re-queues nothing leaves no trace in the ledger: a red
// with no Actions run behind it (an external CI's commit status) and a token
// without actions:write both return "nothing re-run" forever, and both would
// otherwise be retried on every twenty-second tick for as long as the card sat
// in review.
//
// Deliberately in memory, where the ledger is deliberately not: forgetting an
// attempt errs toward asking again, which costs one API call, while forgetting
// a spent re-run would hand a permanently broken PR a fresh budget on every
// deploy. Keyed by card and compared by head, so a push clears it at once —
// the new head deserves an immediate try, not the tail of the old one's wait.
func (a *API) mayAttemptRerun(cardID, headSHA string) bool {
	a.ciRerunMu.Lock()
	defer a.ciRerunMu.Unlock()
	if last, ok := a.ciRerunLast[cardID]; ok && last.headSHA == headSHA && time.Since(last.at) < ciRerunSettle {
		return false
	}
	if a.ciRerunLast == nil {
		a.ciRerunLast = map[string]ciRerunAttempt{}
	}
	a.ciRerunLast[cardID] = ciRerunAttempt{headSHA: headSHA, at: time.Now()}
	return true
}

// forgetCIReruns drops attempt clocks for cards the sweep no longer considers,
// the same housekeeping forgetCIErrors does for its map and for the same
// reason: without it the map keeps an entry for every card ever swept.
func (a *API) forgetCIReruns(swept []string) {
	a.ciRerunMu.Lock()
	defer a.ciRerunMu.Unlock()
	if len(a.ciRerunLast) == 0 {
		return
	}
	live := make(map[string]bool, len(swept))
	for _, id := range swept {
		live[id] = true
	}
	for id := range a.ciRerunLast {
		if !live[id] {
			delete(a.ciRerunLast, id)
		}
	}
}

// rerunRedCI asks GitHub to re-run this head's failed jobs and records the
// attempt on the card. Reports whether anything was actually re-queued — false
// means the sweep should fall through and say the head is red, because nothing
// has been done about it.
func (a *API) rerunRedCI(ctx context.Context, card db.Card, prURL string, ci ciReport, spent int) bool {
	names, err := rerunFailedCIJobs(ctx, prURL, ci.headSHA)
	if err != nil {
		// Not worth its own comment: the red note the caller falls through to
		// is the visible signal, and a token that can't re-run jobs shows up
		// there as a red that never clears.
		log.Printf("auto-merge: card #%d: re-run failed CI jobs on %s: %v", card.Number, shortSHA(ci.headSHA), err)
		return false
	}
	if len(names) == 0 {
		// Red checks with no failed Actions run behind them — an external CI's
		// commit status, say. Nothing here can re-run that.
		return false
	}
	_ = db.AppendComment(ctx, a.Pool, card.ID, fmt.Sprintf(
		"%s%s — %s came back red, so auto-merge asked GitHub to re-run the failed jobs in %s (attempt %d of %d). "+
			"Nothing to do: if it was a flake the sweep merges by itself once the re-run reports green, and if it "+
			"isn't, this card says so.",
		ciRerunPrefix, shortSHA(ci.headSHA), joinChecks(ci.failed), strings.Join(names, ", "), spent+1, maxCIReruns),
		db.EventMeta{Actor: "service"})
	a.Hub.Broadcast(card.BoardID, "card_changed")
	return true
}

// maxNamedChecks caps how many failing jobs a note lists. A wide build matrix
// can fail in dozens of places at once, and the point of naming the job is to
// save the reader a trip to GitHub — a hundred-name list doesn't, and this
// note is one the sweep posts by itself, repeatedly.
const maxNamedChecks = 4

// joinChecks renders the failing check names for a sentence.
func joinChecks(failed []string) string {
	switch {
	case len(failed) == 0:
		return "the PR head's checks"
	case len(failed) == 1:
		return failed[0]
	case len(failed) > maxNamedChecks:
		return fmt.Sprintf("%s and %d more", strings.Join(failed[:maxNamedChecks], ", "), len(failed)-maxNamedChecks)
	default:
		return strings.Join(failed[:len(failed)-1], ", ") + " and " + failed[len(failed)-1]
	}
}

// times renders a small repeat count the way a person says it — and keeps the
// notes below from having to agree with maxCIReruns about plurals.
func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	default:
		return fmt.Sprintf("%d times", n)
	}
}

// humanAgo renders how long ago something happened, coarsely — the reader is
// deciding "is this a new failure or the same old one", and minutes of
// precision past the first hour don't help with that.
func humanAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "moments"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// ciBlock is what a card is told when auto-merge can't proceed, how long the
// reason has to hold before it is worth saying, and how long it may hold
// before the card is flagged stuck.
//
// prefix is the comment's opening phrase and doubles as the LIKE pattern that
// rate-limits it, so the sweep (every 20s) posts each reason at most once per
// window — but a card whose reason CHANGES (checks finally started, the run
// went red) says so on the next sweep instead of waiting the window out.
// why and fix are the reason and its remedy in subordinate clauses, for the
// stuck note; fix is per-reason because the advice genuinely differs — Run
// retries a dead reviewer, but it does nothing whatever for a repo that has
// no CI, so telling that card's human to press Run is telling them to raise
// the same flag again twenty seconds later.
//
// quiet keeps the ordinary case ordinary. A CI run that is still going a few
// seconds after approval is not news, and a card that narrates it on the way
// to a clean auto-merge is noise a human learns to skip — which is how the
// signal this whole change adds would get lost.
//
// Both durations are measured against how long THIS reason has held, which is
// the approval's age for the three CI states (any push stales the approval and
// drops the card out of the sweep, so the reason and the eligibility start
// together) and the error run's own age for blockCIError.
//
// repeats is the other way out, for a reason that time alone won't resolve:
// once the card has been told the same thing this many times under the same
// prefix, saying it again is not communication. See noteAutoMergeBlocked.
type ciBlock struct {
	prefix  string
	text    string
	why     string
	fix     string
	quiet   time.Duration
	grace   time.Duration
	repeats int
}

var (
	// The template blockCIRedFor specialises per head. Left whole and valid on
	// its own so the generic phrasing is what a caller with no report falls
	// back to, and so the prefix — the part that rate-limits — lives in one
	// place: every specialisation EXTENDS it, so a count of comments matching
	// this prefix still counts them all.
	blockCIRed = ciBlock{
		prefix: "Auto-merge waiting: CI is failing",
		text: "Auto-merge waiting: CI is failing on the PR head. The adversarial reviewer approved, " +
			"but the machine check didn't — push a fix; the sweep merges once CI is green.",
		why:   "CI is failing on the PR head",
		fix:   "Push a fix — auto-merge still merges by itself once CI goes green",
		grace: autoMergeBlockedGrace,
		// Red is the reason with a real chance of never resolving on its own,
		// and the one whose note is most misleading when it repeats — it tells
		// the author to fix a diff that may not be at fault. Three notes on one
		// head (the first ~immediately, then every 30 minutes) is over an hour
		// of the board saying the same thing; after that it needs a human, not
		// a fourth copy.
		repeats: 3,
	}
	blockCIPending = ciBlock{
		prefix: "Auto-merge waiting: CI checks are still running",
		text: "Auto-merge waiting: CI checks are still running on the PR head. The adversarial reviewer " +
			"approved; nothing to do — the sweep merges by itself once the checks report.",
		why: "CI checks are still running on the PR head",
		fix: "Look at the run on GitHub — auto-merge still merges by itself if the checks report",
		// the one reason that is usually just the system working
		quiet: 5 * time.Minute,
		grace: autoMergeBlockedGrace,
	}
	// The one that never resolves: no check-runs AND no commit statuses on the
	// head, which is what a repo with no CI workflow looks like. Worth spelling
	// out, because it reads exactly like "still running" from the board and the
	// remedy is completely different.
	blockCINone = ciBlock{
		prefix: "Auto-merge blocked: this repo reports no CI checks",
		text: "Auto-merge blocked: this repo reports no CI checks at all for the PR head — no check-runs " +
			"and no commit statuses, which is what a repo with no CI workflow looks like. The adversarial " +
			"reviewer approved, but auto-merge needs a green machine check and none is coming: waiting will " +
			"not fix this. Add a CI workflow to the repo, or merge this PR yourself with Accept on the card.",
		why: "the repo reports no CI checks at all for the PR head",
		// explicitly NOT "press Run": Run clears the flag, the next sweep finds
		// the same repo with the same absent workflow and raises it again
		fix:   "Add a CI workflow to the repo, or Accept the card to merge it by hand",
		grace: autoMergeNoCIGrace,
	}
)

// Not every block is about CI. These two are about the pull request itself:
// which of the card's `pr` links auto-merge should be merging at all — see
// mergeTargetPR. They live in the same table because they are the same kind of
// fact to the human reading the card ("your merge isn't happening, here's
// why"), and they go through the same rate limit and the same stuck clock.
var blockNoOpenPR = ciBlock{
	prefix: "Auto-merge blocked: no open pull request",
	text: "Auto-merge blocked: no open pull request. Every `pr` link on this card is merged or closed on " +
		"GitHub, so there is nothing left to merge. The adversarial reviewer approved, but auto-merge has " +
		"no target and waiting will not give it one — a merged PR does not reopen. Link the live pull " +
		"request to the card, or move the card to done if the work already landed.",
	why: "every pr link on the card is merged or closed on GitHub",
	// as with the no-CI note, explicitly NOT "press Run": nothing about a
	// re-run gives this card a PR to merge
	fix:   "Link the live PR to the card, or move it to done if the work already landed",
	grace: autoMergeNoCIGrace,
}

// prStateErrorPrefix opens (and rate-limits) the could-not-read-the-PR note.
const prStateErrorPrefix = "Auto-merge waiting: could not read the pull request state"

// blockPRStateError: GitHub would not say which of the card's pull requests is
// open, so the sweep declines to guess — the guessing is what this whole
// change removes.
func blockPRStateError(err error) ciBlock {
	return ciBlock{
		prefix: prStateErrorPrefix,
		text: prStateErrorPrefix + " — " + err.Error() +
			". This card carries more than one `pr` link, so auto-merge has to ask GitHub which one is open " +
			"before it merges anything. The sweep keeps retrying, but an error that persists (a bad PR link, " +
			"a GitHub token without access to the repo) needs a human.",
		why: "the pull request state could not be read (" + err.Error() + ")",
		fix: "Check the card's PR links and the GitHub token's access to the repo",
		// same reasoning as blockCIError: GitHub 5xx and secondary rate limits
		// are routine, so wait for the error to hold across ~six sweeps
		quiet: 2 * time.Minute,
		grace: autoMergeBlockedGrace,
	}
}

// ciErrorPrefix opens (and rate-limits) the could-not-read note. Named
// separately because that note's body is built per-error.
const ciErrorPrefix = "Auto-merge waiting: could not read CI status"

// blockCIError: the CI status could not be read. e039de9 made the CI read
// return these instead of silently decoding a zero value; this puts them where
// the person who can act on them will see them.
func blockCIError(err error) ciBlock {
	return ciBlock{
		prefix: ciErrorPrefix,
		text: ciErrorPrefix + " for the PR head — " + err.Error() +
			". The adversarial reviewer approved; the sweep keeps retrying, but an error that persists " +
			"(a bad PR link, a GitHub token without access to the repo) needs a human.",
		why: "the CI status could not be read (" + err.Error() + ")",
		fix: "Check the PR link and the GitHub token's access to the repo",
		// GitHub 5xx and secondary rate limits are routine, so this waits for
		// the error to hold for two minutes of sweeps — roughly six in a row —
		// before it is worth a word. See ciErrorHeld for what times it.
		quiet: 2 * time.Minute,
		grace: autoMergeBlockedGrace,
	}
}

// ciBlockFor maps a non-green CI read to what the card gets told. A switch,
// not a table: an unhandled state falls back to the conservative "still
// waiting" note rather than to silence.
func ciBlockFor(ci ciReport, reruns int) ciBlock {
	switch ci.state {
	case ciRed:
		return blockCIRedFor(ci, reruns)
	case ciNone:
		return blockCINone
	default:
		return blockCIPending
	}
}

// blockCIRedFor writes the red note for THIS head: which checks failed, how
// old the failure is, and what auto-merge has already tried about it.
//
// The head SHA joins the prefix, so the note is rate-limited per head rather
// than per card. That is the fix for the second half of the reported bug: a
// push that produces a new head and goes red again used to be swallowed by the
// previous head's 30-minute window, and a card already flagged stuck for an
// old red would never mention a new one at all.
//
// Naming the failing job is the difference between a note the author can act
// on and one that sends them to GitHub to find out what the board already
// knows. Naming the failure's AGE is the difference between "CI is failing"
// and the truth on a jammed card, which is that CI failed once, some hours
// ago, and the sweep has been re-reading that same answer ever since.
func blockCIRedFor(ci ciReport, reruns int) ciBlock {
	b := blockCIRed
	if s := shortSHA(ci.headSHA); s != "" {
		b.prefix = blockCIRed.prefix + " on " + s
	}
	failing := joinChecks(ci.failed)
	b.why = "CI is failing on the PR head (" + failing + ")"

	sentences := []string{
		b.prefix + " — " + failing + ". The adversarial reviewer approved, but the machine check didn't.",
	}
	if !ci.newest.IsZero() {
		if age := time.Since(ci.newest); age >= ciStaleRed {
			sentences = append(sentences, fmt.Sprintf("This result is %s old and nothing has changed on this"+
				" head since: the sweep is re-reading one old failure, not watching a new one.", humanAgo(age)))
		} else {
			sentences = append(sentences, fmt.Sprintf("It finished %s ago.", humanAgo(age)))
		}
	}
	switch {
	case reruns >= maxCIReruns:
		sentences = append(sentences, fmt.Sprintf("Auto-merge has already asked GitHub to re-run the failed jobs"+
			" %s on this head and they stayed red, so this is unlikely to be a flake — look at %s and"+
			" push a fix.", times(reruns), failing))
		b.fix = "Auto-merge already re-ran the failed jobs and they stayed red — look at " + failing + " and push a fix"
	case reruns > 0:
		sentences = append(sentences, fmt.Sprintf("Auto-merge has re-run the failed jobs %s on this head"+
			" already; it will try %d more before giving up on this being a flake.", times(reruns), maxCIReruns-reruns))
	default:
		// Nothing was re-run: either the failure isn't a GitHub Actions run, or
		// the re-run call itself failed. Don't claim a retry that didn't happen.
		sentences = append(sentences, "Auto-merge could not re-run it automatically — push a fix, or re-run the"+
			" job on GitHub; the sweep merges by itself once CI is green.")
		b.fix = "Push a fix, or re-run " + failing + " on GitHub — auto-merge merges by itself once CI goes green"
	}
	b.text = strings.Join(sentences, " ")
	return b
}

// noteAutoMergeBlocked reports on the card that a merge the card has earned
// isn't happening, and why. Silence here is the bug this exists to fix: the
// fail-safe (never merge unless CI is green) is correct, but a card waiting
// forever on a check that will never arrive looks, from the board, exactly
// like a card still under review.
//
// heldFor is how long THIS reason has held — see ciBlock for which clock that
// is per reason.
func (a *API) noteAutoMergeBlocked(ctx context.Context, card db.Card, heldFor time.Duration, b ciBlock) {
	if heldFor >= b.quiet {
		// A card already flagged stuck has been surfaced, so it doesn't need
		// this explanation every window — but it does need it ONCE, even when
		// the flag was raised for something else entirely (a dead reviewer, a
		// worker that went quiet), or the auto-merge jam stays invisible under
		// a chip that explains the wrong thing. So the limit widens from
		// "recently" to "ever" rather than going silent.
		window := any("30 minutes")
		if card.StuckAt != nil {
			window = nil
		}
		// Both counts in one pass: recent arms the rate limit exactly as an
		// EXISTS did, and told is how many times this reason has EVER been put
		// on the card, which is what b.repeats escalates on.
		var told, recent int
		_ = a.Pool.QueryRow(ctx, `
			SELECT count(*),
			       count(*) FILTER (WHERE $3::interval IS NULL OR created_at > now() - $3::interval)
			FROM card_events
			WHERE card_id = $1 AND type = 'comment' AND data->>'text' LIKE $2`,
			card.ID, b.prefix+"%", window).Scan(&told, &recent)
		if recent == 0 {
			_ = db.AppendComment(ctx, a.Pool, card.ID, b.text, db.EventMeta{Actor: "service"})
			a.Hub.Broadcast(card.BoardID, "card_changed")
			told++
		}
		// Escalate before falling through to the time-based grace, so a reason
		// that has worn out its welcome says so instead of quoting a duration.
		if b.repeats > 0 && told >= b.repeats {
			a.flagAutoMergeStuck(ctx, card, fmt.Sprintf(
				"Auto-merge stuck: this card has now been told %s that %s, and repeating it a %s time "+
					"won't change anything — flagged stuck so the board shows it needs a human. Auto-merge "+
					"keeps trying and still merges by itself the moment CI goes green. %s.",
				times(told), b.why, ordinal(told+1), b.fix))
			return
		}
	}
	if b.grace > 0 && heldFor > b.grace {
		// "once that clears" rather than "once CI goes green": the reasons are
		// no longer all about CI (blockNoOpenPR is about the card's pr links),
		// and promising a green check would merge a card that has nothing to
		// merge is the wrong sentence to leave under a stuck chip. why names
		// the actual reason immediately before it.
		a.flagAutoMergeStuck(ctx, card, fmt.Sprintf(
			"Auto-merge stuck: this card has been ready to merge and blocked for over %s — %s. "+
				"Flagged stuck so the board shows the jam; auto-merge keeps trying and still merges by "+
				"itself the moment that clears. %s.",
			humanDur(b.grace), b.why, b.fix))
	}
}

// ordinal renders a small count as an English ordinal, for the escalation
// note. Only ever called with the handful of values b.repeats reaches.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

// flagAutoMergeStuck raises the stuck flag and explains it — the flag every
// time it is cleared and re-earned, the comment at most once per window.
//
// The two need different lifetimes because pressing Run clears stuck_at for a
// whole board (handleStartRun), and Run does nothing at all for the jam this
// most often reports: the repo still has no CI, so the flag is back within
// twenty seconds. Tying the comment to the flag's NULL->now() transition, the
// way flagReviewStuck does, would append a byte-identical note on every press.
func (a *API) flagAutoMergeStuck(ctx context.Context, card db.Card, note string) {
	tag, err := a.Pool.Exec(ctx,
		`UPDATE cards SET stuck_at = now() WHERE id = $1 AND stuck_at IS NULL`, card.ID)
	if err != nil || tag.RowsAffected() == 0 {
		return // already flagged — the chip is up and the note is on the card
	}
	var recent bool
	_ = a.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM card_events
			WHERE card_id = $1 AND type = 'comment'
			  AND data->>'text' LIKE 'Auto-merge stuck:%'
			  AND created_at > now() - interval '30 minutes'
		)`, card.ID).Scan(&recent)
	if !recent {
		_ = db.AppendComment(ctx, a.Pool, card.ID, note, db.EventMeta{Actor: "service"})
	}
	a.Hub.Broadcast(card.BoardID, "card_changed")
}

// humanDur renders a grace period the way a person would say it. The graces
// are round by construction, so handling whole hours and whole minutes covers
// every value that reaches a card.
func humanDur(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return d.String()
	}
}

// finishCard: move to done and stop the card's sessions (shared with accept).
func (a *API) finishCard(ctx context.Context, card db.Card) {
	cols, err := db.ListColumns(ctx, a.Pool, card.BoardID)
	if err == nil {
		for _, c := range cols {
			if c.Terminal() {
				_, _ = db.MoveCard(ctx, a.Pool, card.ID, c.ID, nil, nil, db.EventMeta{Actor: "service"})
				break
			}
		}
	}
	rows, err := a.Pool.Query(ctx, `
		SELECT external_session_id FROM runner_sessions
		WHERE card_id = $1 AND lifecycle NOT IN ('stopped','error')`, card.ID)
	if err == nil {
		var exts []string
		for rows.Next() {
			var e string
			if rows.Scan(&e) == nil {
				exts = append(exts, e)
			}
		}
		rows.Close()
		for _, e := range exts {
			if err := a.runner.Driver.Stop(ctx, e); err != nil {
				_ = a.runner.Driver.Interrupt(ctx, e) // older runner: best effort
			}
		}
		tag, err := a.Pool.Exec(ctx, `
			UPDATE runner_sessions SET lifecycle = 'stopped'
			WHERE card_id = $1 AND lifecycle NOT IN ('stopped','error')`, card.ID)
		if err == nil && tag.RowsAffected() > 0 {
			// blerg-board just handed slots back to the runner: whatever refusal is
			// on record, it predates them (see runnerOutOfSlots)
			a.noteCapacityAvailable(ctx)
		}
	}
}

// reapAllQuietWork extends the run reaper to every board: an in-progress
// card that HAS had sessions but whose sessions all went quiet past the
// grace period gets the same respawn-once-then-stuck treatment, run or no
// run. Cards a human parked in progress without ever spawning a session are
// left alone.
func (a *API) reapAllQuietWork(ctx context.Context) {
	rows, err := a.Pool.Query(ctx, `
		SELECT DISTINCT c.board_id, bc.id FROM cards c
		JOIN board_columns bc ON bc.id = c.column_id
		WHERE c.archived_at IS NULL AND c.stuck_at IS NULL
		  AND (lower(bc.name) LIKE '%progress%' OR lower(bc.name) LIKE '%doing%')
		  AND EXISTS (SELECT 1 FROM runner_sessions rs WHERE rs.card_id = c.id)`)
	if err != nil {
		return
	}
	type target struct{ boardID, colID string }
	var targets []target
	for rows.Next() {
		var t target
		if rows.Scan(&t.boardID, &t.colID) == nil {
			targets = append(targets, t)
		}
	}
	rows.Close()
	for _, t := range targets {
		board, err := db.GetBoard(ctx, a.Pool, t.boardID)
		if err != nil {
			continue
		}
		var runID int64 // credit stuck counts to an active run if one exists
		_ = a.Pool.QueryRow(ctx,
			`SELECT id FROM board_runs WHERE board_id = $1 AND state = 'running'`, t.boardID).Scan(&runID)
		// same lock as dispatch: respawning a quiet worker is a claim too, and
		// two overlapping ticks would otherwise each spawn one
		a.withBoardDispatch(ctx, t.boardID, func() {
			a.reapQuietWorkers(ctx, runID, board, t.colID)
		})
	}
}

// reviveStalledReviews: a review-column card with something to review, no
// fresh approval, and a reviewer that has been quiet past the grace period
// gets its review re-triggered (nudge if reachable, fresh spawn otherwise).
//
// Bounded three ways, because this fires every 20s for every board: the
// service comment maybeSpawnReviewer leaves rate-limits it to one attempt per
// grace period (a FAILED spawn leaves one too, or the limit would never arm);
// reviewerAttemptCap stops the round after a few dead reviewers; and a card
// flagged stuck drops out here entirely, so "gave up, a human should look"
// stays given up.
//
// Spec cards (no PR, body is the artifact) qualify only once a reviewer
// session has existed for them: this sweep revives reviews, it never
// cold-starts one. Entry into the review column is the only thing that starts
// a spec review, so switching this on can't stampede a backlog of no-PR cards
// already parked in a review column.
func (a *API) reviveStalledReviews(ctx context.Context) {
	rows, err := a.Pool.Query(ctx, `
		SELECT DISTINCT c.id FROM cards c
		JOIN board_columns bc ON bc.id = c.column_id
		WHERE c.archived_at IS NULL AND c.stuck_at IS NULL AND lower(bc.name) LIKE '%review%'
		  AND (
			EXISTS (SELECT 1 FROM card_links cl WHERE cl.card_id = c.id AND cl.kind = 'pr')
			OR (btrim(coalesce(c.body, '')) <> '' AND EXISTS (
				SELECT 1 FROM runner_sessions rs0 WHERE rs0.card_id = c.id AND rs0.role = 'reviewer'))
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM runner_sessions rs
			WHERE rs.card_id = c.id AND rs.role = 'reviewer'
			  AND rs.lifecycle IN ('starting','running','waiting')
			  AND rs.last_activity_at > now() - $1::interval
		  )
		  AND NOT EXISTS (  -- rate limit: a recent review-flow service comment
			SELECT 1 FROM card_events ce
			WHERE ce.card_id = c.id AND ce.type = 'comment'
			  AND ce.created_at > now() - $1::interval
			  AND (ce.data->>'text' LIKE 'Adversarial re-review requested%'
			       OR ce.data->>'text' LIKE 'Adversarial review session spawned%'
			       OR ce.data->>'text' LIKE 'Adversarial review%')
		  )`, workerQuietGrace.String())
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		card, err := db.GetCard(ctx, a.Pool, id)
		if err != nil {
			continue
		}
		board, err := db.GetBoard(ctx, a.Pool, card.BoardID)
		if err != nil {
			continue
		}
		// maybeSpawnReviewer's own guards handle fresh-verdict and mid-review
		a.maybeSpawnReviewer(ctx, board, card)
	}
}

// reviewReapReprieve bounds how long a mid-review exemption may hold a worker
// open (see reapIdleSessions). A reviewer that is genuinely working keeps its
// session row warm — every event it emits moves last_activity_at — while one
// whose pod died silently stops moving it, and after this long the worker it
// was holding open goes back to being ordinary idle capacity.
//
// Something has to say when a review stops counting as "in flight", because a
// reviewer wedged in 'running' would otherwise make its worker immortal: that
// is the same slot rent the reaper exists to collect, just wearing an excuse.
// Two hours is the ceiling the board/discuss rule already accepts, and it is
// well past the checkout-build-suite loop that makes reviewers outlast the
// 30-minute worker threshold in the first place. In practice the reprieve
// rarely runs its length — the reaper stops an idle reviewer at 30 minutes,
// and a stopped reviewer fails the liveness test on the very next sweep.
const reviewReapReprieve = 2 * time.Hour

// verdictHandoffGrace covers the gap between a verdict being posted and the
// findings reaching the worker (see reapIdleSessions). The two are separate
// actions by the reviewer: it posts the "Adversarial review … Verdict:" comment,
// and only the *move* it makes afterwards runs afterMove -> maybeRelayFindings.
// Treating the comment as the end of the review would put the worker back on
// the ordinary clock — which it is already 30 minutes past — during those
// seconds, and the 20s dispatcher tick fits in them comfortably. The window is
// wider than the reviewer's two calls, too: a woken worker's last_activity_at
// only moves when ingestTick next drains it, and idle sessions are drained
// every tenth tick.
//
// Losing that race produces exactly the outcome this exemption exists to
// prevent, and worse than a plain reap: the relay finds no live worker, returns
// early, and the findings sit on the card until reapQuietWorkers rebuilds a
// cold session ten minutes later — or flags the card stuck, if it has already
// spent its respawns. So the newest verdict keeps the worker for a few minutes
// more. It only has to cover a move plus ingest lag; it is deliberately far
// shorter than reviewReapReprieve because nothing is being waited on here, only
// handed over.
const verdictHandoffGrace = 5 * time.Minute

// reapIdleSessions frees runner capacity from sessions whose act is over:
// workers/reviewers idle past 30 minutes, board/discuss chats idle past two
// hours. Every re-engagement path (findings relay, review revival, quiet-work
// respawn, human Run-again) already copes with a dead session by spawning
// fresh — an idle pod is pure slot rent. Waiting sessions are never reaped
// (they're waiting on a human).
//
// One exemption, and it is about cost rather than correctness: a worker that
// has pushed its PR and handed the card to review is *correctly* idle — it is
// waiting on a verdict it will have to act on. Stopping it is safe (the
// findings relay spawns a fresh worker) but expensive: the replacement
// re-clones, re-reads the card and its whole comment trail, and rebuilds state
// the stopped session already held, only to address findings it never saw
// arrive. Reviewers routinely take longer than the worker's 30-minute
// threshold — checkout, build, full suite — so on a card already through a
// round or two that rebuild is both large and avoidable.
//
// So a worker is spared while the review it is waiting on is:
//
//   - OUTSTANDING — the newest review request (a reviewer session started, or
//     a re-review asked of an existing one) is newer than the newest verdict.
//     A verdict is a comment carrying a Verdict line, matched exactly as
//     maybeSpawnReviewer matches it, so the two can't disagree about whether a
//     round is over; the prefix alone would count this service's own "session
//     spawned" breadcrumbs as verdicts.
//   - ALIVE — some reviewer session on the card that hasn't stopped or errored
//     has been heard from within reviewReapReprieve. Outstanding alone is a
//     latch nothing reliably clears: a reviewer that dies before posting
//     anything leaves the request standing forever.
//
// …or while the verdict has landed but not yet been handed over: a verdict
// younger than verdictHandoffGrace that this worker has not acted on keeps it
// too, because posting the verdict and moving the card (which is what actually
// relays the findings) are separate steps with a reapable gap between them.
//
// Two narrowings keep the exemption to the session that will really be handed
// the findings, rather than to the card at large:
//
//   - only the newest non-terminal worker on the card, because that is the one
//     maybeRelayFindings picks. Older workers — reapQuietWorkers can leave one
//     behind after a respawn — are waiting on nothing and pay ordinary rent.
//     (On a spec card the relay may prefer a newer discuss session instead, in
//     which case this spares a worker that won't be messaged; harmless, and
//     bounded by the same clocks as everything else here.)
//   - only live cards. A card a human archived, or one already flagged stuck,
//     is not a card whose worker is about to be re-engaged; the stuck flag in
//     particular is the board saying it has given up waiting.
//
// Once a verdict lands and is handed over, the ordinary 30-minute rule applies
// to the worker again. Only workers are exempted — a reviewer, board or discuss
// session on the same card is reaped on its own clock as before.
//
// One branch of the card's "live reviewer OR outstanding request" deliberately
// does not qualify: when spawnSession fails for want of runner slots, review.go
// writes a "could not start — out of slots" comment and no session row, so the
// review is neither alive nor requested and the worker is reaped. That is the
// right way round — slots are exactly what is scarce in that moment — but it
// means a worker is not held open across a capacity stall.
func (a *API) reapIdleSessions(ctx context.Context) {
	rows, err := a.Pool.Query(ctx, `
		SELECT s.id, s.external_session_id, s.role FROM runner_sessions s
		LEFT JOIN LATERAL (
			SELECT
				-- newest verdict: same LIKE + regexp maybeSpawnReviewer uses
				coalesce((
					SELECT max(created_at) FROM card_events v
					WHERE v.card_id = s.card_id AND v.type = 'comment'
					  AND v.data->>'text' LIKE 'Adversarial review%'
					  AND v.data->>'text' ~* 'verdict:[[:space:]]*(approve|request[- ]changes)'
				), '-infinity') AS verdict_at,
				-- newest request: a reviewer session started, or a re-review
				-- asked of an existing one (which writes no session row)
				coalesce((
					SELECT max(t) FROM (
						SELECT max(created_at) AS t FROM runner_sessions rq
							WHERE rq.card_id = s.card_id AND rq.role = 'reviewer'
						UNION ALL
						SELECT max(created_at) FROM card_events rc
							WHERE rc.card_id = s.card_id AND rc.type = 'comment'
							  AND rc.data->>'text' LIKE 'Adversarial re-review requested%'
					) req
				), '-infinity') AS request_at,
				EXISTS (
					SELECT 1 FROM runner_sessions r
					WHERE r.card_id = s.card_id AND r.role = 'reviewer'
					  AND r.lifecycle NOT IN ('stopped','error')
					  AND greatest(r.created_at, r.last_activity_at) > now() - $1::interval
				) AS reviewer_alive
		) rev ON s.role = 'worker' AND s.card_id IS NOT NULL
		WHERE s.lifecycle = 'idle'
		  AND s.last_activity_at IS NOT NULL
		  AND ((s.role IN ('worker','reviewer') AND s.last_activity_at < now() - interval '30 minutes')
		       OR (s.role IN ('board','discuss') AND s.last_activity_at < now() - interval '2 hours'))
		  AND NOT (
			s.role = 'worker' AND s.card_id IS NOT NULL
			-- the session the findings would actually reach (review.go's pick).
			-- Never NULL for a candidate: s is idle, so s is in its own subquery.
			AND s.id = (
				SELECT w.id FROM runner_sessions w
				WHERE w.card_id = s.card_id AND w.role = 'worker'
				  AND w.lifecycle NOT IN ('stopped','error')
				ORDER BY w.created_at DESC LIMIT 1
			)
			AND EXISTS (
				SELECT 1 FROM cards c
				WHERE c.id = s.card_id AND c.archived_at IS NULL AND c.stuck_at IS NULL
			)
			AND (
				-- a round is outstanding and someone is still running it
				(rev.reviewer_alive AND rev.verdict_at < rev.request_at)
				-- or the verdict is in and the hand-off hasn't reached us yet
				OR (rev.verdict_at > now() - $2::interval
				    AND s.last_activity_at < rev.verdict_at)
			)
		  )`, reviewReapReprieve.String(), verdictHandoffGrace.String())
	if err != nil {
		return
	}
	type victim struct{ id, ext, role string }
	var victims []victim
	for rows.Next() {
		var v victim
		if rows.Scan(&v.id, &v.ext, &v.role) == nil {
			victims = append(victims, v)
		}
	}
	rows.Close()
	for _, v := range victims {
		if err := a.runner.Driver.Stop(ctx, v.ext); err != nil {
			log.Printf("idle reap: stop %s (%s): %v", v.ext, v.role, err)
			continue
		}
		_, _ = a.Pool.Exec(ctx,
			`UPDATE runner_sessions SET lifecycle = 'stopped' WHERE id = $1`, v.id)
		a.revokeSessionToken(ctx, v.id)
		// a slot blerg-board freed itself — dispatch should not wait out a refusal
		// that was recorded before it
		a.noteCapacityAvailable(ctx)
		log.Printf("idle reap: stopped %s session %s", v.role, v.ext)
	}
}

// isConflictErr: GitHub's merge endpoint refusing because the branch
// conflicts with main (405 not-mergeable / dirty state).
func isConflictErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not mergeable") || strings.Contains(msg, "merge conflict") ||
		strings.Contains(msg, "http 405") || strings.Contains(msg, "http 409")
}

// conflictBounceLimit: how many times one card may be sent back to a worker
// for the same conflict before the merge stops being blerg-board's problem. Shared
// by both merge paths so they can't drift apart.
const conflictBounceLimit = 3

// conflictSource is which merge path hit the conflict. It changes the wording
// on the card and the actor recorded against the bounce — never the mechanics
// — so the trail doesn't tell a human who pressed Accept that auto-merge did
// it.
type conflictSource int

const (
	conflictAutoMerge conflictSource = iota
	conflictHumanAccept
)

func (s conflictSource) actor() string {
	if s == conflictHumanAccept {
		return "human"
	}
	return "service"
}

func (s conflictSource) comment() string {
	if s == conflictHumanAccept {
		return "Accept hit merge conflicts with main — nothing was merged, so the card went back to the worker to resolve. A fresh adversarial review runs before the next merge attempt."
	}
	return "Auto-merge hit merge conflicts with main — sent back to the worker to resolve. A fresh adversarial review runs before the next merge attempt."
}

func (s conflictSource) waitingOn() string {
	if s == conflictHumanAccept {
		return "a human pressed Accept and it would not merge"
	}
	return "auto-merge is waiting on you"
}

// bounceResult is how a bounce attempt ended. The distinction that matters is
// between bounceImpossible — a standing fact about this card and board that
// retrying cannot change — and bounceFailed, which is about this attempt only:
// mistaking the second for the first would tell a human a card is unrecoverable
// because the database blinked once.
type bounceResult int

const (
	bounced          bounceResult = iota // the card is in the work column, attempt counted
	bounceImpossible                     // nothing to bounce into, or nobody to work it
	bounceFailed                         // this attempt lost to an error; the next may not
)

// bounceForConflict sends a conflicted PR back through the machine: the card
// returns to in-progress, the worker (live or freshly spawned) resolves the
// conflict and pushes, which stales the old approval — so a fresh adversarial
// review runs before auto-merge tries again. auto_merge stays set; attempts
// are counted so a persistent conflict eventually falls to the human.
//
// Anything but bounced means nothing was sent back and the caller still owns
// the failure. Waking the worker can spawn a session, so request handlers use
// bounceCardToWork + wakeWorkerForConflict directly and background the slow
// half rather than calling this.
func (a *API) bounceForConflict(ctx context.Context, card db.Card, prURL string, src conflictSource) bounceResult {
	board, moved, res := a.bounceCardToWork(ctx, card, src)
	if res != bounced {
		return res
	}
	a.wakeWorkerForConflict(ctx, board, moved, prURL, src)
	return bounced
}

// bounceCardToWork is the half of the bounce that must happen before anyone is
// told it did: the card moves out of review into the work column, the attempt
// is counted, and the card says why. Anything other than bounced leaves the
// card exactly as it was.
func (a *API) bounceCardToWork(ctx context.Context, card db.Card, src conflictSource) (db.Board, db.Card, bounceResult) {
	board, err := db.GetBoard(ctx, a.Pool, card.BoardID)
	if err != nil {
		log.Printf("conflict bounce: load board for card #%d: %v", card.Number, err)
		return db.Board{}, db.Card{}, bounceFailed
	}
	workCol := a.workColumnID(ctx, card.BoardID)
	if workCol == "" {
		log.Printf("conflict bounce: card #%d: board %q has no work column — not bouncing", card.Number, board.Name)
		return db.Board{}, db.Card{}, bounceImpossible
	}
	moved, err := db.MoveCard(ctx, a.Pool, card.ID, workCol, nil, nil, db.EventMeta{Actor: src.actor()})
	if err != nil {
		log.Printf("conflict bounce: move card #%d to work: %v", card.Number, err)
		return db.Board{}, db.Card{}, bounceFailed
	}
	_, _ = a.Pool.Exec(ctx,
		`UPDATE cards SET run_attempts = run_attempts + 1 WHERE id = $1`, card.ID)
	_ = db.AppendComment(ctx, a.Pool, card.ID, src.comment(), db.EventMeta{Actor: src.actor()})
	return board, moved, bounced
}

// workColumnID is the column a bounce lands in, "" if the board has none that
// isWorkColumn recognises.
func (a *API) workColumnID(ctx context.Context, boardID string) string {
	cols, err := db.ListColumns(ctx, a.Pool, boardID)
	if err != nil {
		return ""
	}
	for _, c := range cols {
		if isWorkColumn(c.Name) {
			return c.ID
		}
	}
	return ""
}

// canBounceConflict answers whether a conflict bounce is available for this
// card at all: a column to bounce into, a runner to work it, and someone that
// runner can reach — a live worker session, or the repo a fresh session would
// be cloned into. Callers ask BEFORE deciding what to tell a human: without
// this, a card whose attempts are spent would be told its retries ran out on a
// deployment where no bounce was ever possible in the first place.
func (a *API) canBounceConflict(ctx context.Context, card db.Card) bool {
	if a.runner == nil || a.workColumnID(ctx, card.BoardID) == "" {
		return false
	}
	return len(card.Repos) > 0 || a.liveWorkerSession(ctx, card.ID) != ""
}

// liveWorkerSession is the card's still-breathing worker session, "" if it has
// none — 25 minutes of silence counts as gone.
func (a *API) liveWorkerSession(ctx context.Context, cardID string) string {
	var ext string
	if err := a.Pool.QueryRow(ctx, `
		SELECT external_session_id FROM runner_sessions
		WHERE card_id = $1 AND role = 'worker' AND lifecycle NOT IN ('stopped','error')
		  AND last_activity_at > now() - interval '25 minutes'
		ORDER BY created_at DESC LIMIT 1`, cardID).Scan(&ext); err != nil {
		return ""
	}
	return ext
}

// wakeWorkerForConflict puts a worker on the conflict: the live worker session
// if one is still around, otherwise a fresh spawn. The spawn is a pod start —
// slow enough that callers on a request path run this in a goroutine.
func (a *API) wakeWorkerForConflict(ctx context.Context, board db.Board, card db.Card, prURL string, src conflictSource) {
	if a.runner == nil {
		return
	}
	instr := "The PR (" + prURL + ") has merge conflicts with main and " + src.waitingOn() + ": " +
		"merge origin/main into the PR branch, resolve every conflict (preserve both the branch's intent and main's newer changes), " +
		"re-run the checks, push, refresh the 'Review guide' comment, and move the card back to review."
	if ext := a.liveWorkerSession(ctx, card.ID); ext != "" &&
		a.runner.Driver.Message(ctx, ext, instr, "blerg-board") == nil {
		a.Hub.Broadcast(card.BoardID, "card_changed")
		return
	}
	// A fresh session needs a repo to clone: spawnSession indexes Repos[0], and
	// Accept runs this detached from the request goroutine, where net/http
	// cannot recover a panic — one repo-less card would take the process down.
	if len(card.Repos) == 0 {
		a.noteNoWorkerOnConflict(ctx, card, "the card has no repo for a session to work in")
		return
	}
	prompt, err := a.buildCardPrompt(board, card, instr)
	if err != nil {
		log.Printf("conflict bounce: render prompt for card #%d: %v", card.Number, err)
		a.noteNoWorkerOnConflict(ctx, card, "blerg-board could not render the worker prompt")
		return
	}
	if _, _, err := a.spawnSession(ctx, board, card, "worker", prompt, ""); err != nil {
		log.Printf("conflict bounce: spawn for card #%d: %v", card.Number, err)
		a.noteNoWorkerOnConflict(ctx, card, "the worker session would not start ("+err.Error()+")")
		return
	}
	a.Hub.Broadcast(card.BoardID, "card_changed")
}

// noteNoWorkerOnConflict records the half of the bounce that didn't happen. The
// card has already been moved and already says a worker is resolving the
// conflict — if nobody could be put on it, the trail has to say so, or the card
// sits in the work column waiting on a session that never existed.
func (a *API) noteNoWorkerOnConflict(ctx context.Context, card db.Card, why string) {
	_ = db.AppendComment(ctx, a.Pool, card.ID,
		"...but no worker could be started on the conflict: "+why+
			". The merge conflict needs resolving by hand, or press Run to try again.",
		db.EventMeta{Actor: "service"})
	a.Hub.Broadcast(card.BoardID, "card_changed")
}
