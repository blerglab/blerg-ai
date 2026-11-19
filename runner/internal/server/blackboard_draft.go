package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// blackboardDraftPlan is what HandleDraftBlackboardPlan generates and stores, and what the
// read-only blackboard tab displays. Main asks the owner to create the exact
// saved id and revision through an existing person-required question.
//
// Every field here can carry text derived from the board or the workspace's history; the CLI and
// the browser both read it back through the "untrusted_data" envelope already used for agent plan
// reads (blackboard_agent.go), so nothing it contains is ever authority to do anything by itself.
type blackboardDraftPlan struct {
	Name      string                   `json:"name"`
	Vision    string                   `json:"vision"`
	Goals     []string                 `json:"goals"`
	BoardID   string                   `json:"board_id,omitempty"`
	Milestone blackboardDraftMilestone `json:"milestone"`
	Thrusts   []blackboardDraftThrust  `json:"thrusts"`
}

// blackboardDraftMilestone groups the milestone-related fields of a draft (BLG-450 review M3):
// name, an optional release and an optional target date, so a richer draft (or a later guided
// setup, BLG-454) can carry all three without threading them through separate top-level fields.
type blackboardDraftMilestone struct {
	Name      string `json:"name"`
	ReleaseID string `json:"release_id,omitempty"`
	// Target is an ISO date ("2006-01-02"), the same format and parsing the existing
	// "date_moved" milestone event already uses (blackboard/snapshot.go) — reused here rather
	// than inventing a second date convention. nil/omitted means no target yet.
	Target *string `json:"target,omitempty"`
}
type blackboardDraftThrust struct {
	Name      string `json:"name"`
	GoalIndex int    `json:"goal_index"`
	// Reason explains, in words a person can check, why the draft proposes this thrust. It is
	// derived text by default (today: whether a board is linked; the generator is written so a
	// later pass can add board card titles, release names and commit subjects without changing
	// this shape), or Main's own stated reason when it supplies one — never itself a decision.
	Reason string `json:"reason,omitempty"`
}

// blackboardDraftMaxReasonBytes bounds a thrust's reason the same way createBlackboard bounds a
// thrust's name (200) and a goal's name (200): long enough for a real sentence or two, short
// enough that a draft stays bounded. Chosen, not derived from any existing constant.
const blackboardDraftMaxReasonBytes = 500

// generateBlackboardDraftPlan is the server-side generation entry point (ticket wording). It
// never invents placeholder text (owner correction, BLG-450 round 5): every field it sets is a
// real fact already known about the session — its title (BLG-454's guided setup fills in the
// rest), and whether a board is linked. An undetermined field stays genuinely empty rather than
// carrying canned "edit this" text, so a draft only ever shows what someone or something actually
// said. Pulling actual board card titles, release history and commit subjects would reuse the
// session's own board-tool access checks (session_board_access.go) and the daemon's git RPC
// (commitlookup.go) respectively — both nontrivial existing subsystems with their own auth;
// wiring them into draft generation is left for BLG-454 rather than duplicated here.
func generateBlackboardDraftPlan(row *db.SessionRow) blackboardDraftPlan {
	name := ""
	if row.Title != nil && *row.Title != "" {
		name = *row.Title + " plan"
	}
	// Cap to createBlackboard's own len(body.Name) > 200 bound, so a long session title still
	// produces a draft Create can accept rather than a confusing 400 later (BLG-450 review L8).
	if len(name) > 200 {
		name = name[:200]
	}
	boardID := ""
	if row.BoardID != nil {
		boardID = *row.BoardID
	}
	return blackboardDraftPlan{Name: name, BoardID: boardID}
}

// mergeBlackboardDraftPlan layers supplied's non-empty fields over gen's generated defaults, one
// field at a time — never a wholesale replace — so a caller who supplies only a vision keeps the
// real facts generation already determined (its derived name, its linked board) without losing
// them, while every field neither side determined stays empty (BLG-450 review M3; no-placeholder
// correction round 5). BoardID is deliberately never taken from supplied: it is always the
// session's own linked board, not something a caller can invent through a draft.
func mergeBlackboardDraftPlan(gen, supplied blackboardDraftPlan) blackboardDraftPlan {
	merged := gen
	if supplied.Name != "" {
		merged.Name = supplied.Name
	}
	if supplied.Vision != "" {
		merged.Vision = supplied.Vision
	}
	if len(supplied.Goals) > 0 {
		merged.Goals = supplied.Goals
	}
	if supplied.Milestone.Name != "" {
		merged.Milestone.Name = supplied.Milestone.Name
	}
	merged.Milestone.ReleaseID = supplied.Milestone.ReleaseID
	merged.Milestone.Target = supplied.Milestone.Target
	if len(supplied.Thrusts) > 0 {
		merged.Thrusts = supplied.Thrusts
	}
	return merged
}

// validateBlackboardDraftPlan is shared by storage and bound creation.
// Errors wrap blackboard.ErrInvalid so blackboardError
// maps them to 400 with a specific, non-generic message, never a silent clamp or drop.
func validateBlackboardDraftPlan(plan blackboardDraftPlan) error {
	bad := func(msg string) error { return fmt.Errorf("%w: %s", blackboard.ErrInvalid, msg) }
	if len(plan.Name) > 200 {
		return bad("name too long")
	}
	if len(plan.Vision) > 1000 {
		return bad("vision too long")
	}
	if len(plan.BoardID) > 200 {
		return bad("board id too long")
	}
	if len(plan.Goals) > 30 {
		return bad("too many goals")
	}
	for _, g := range plan.Goals {
		if g == "" || len(g) > 200 {
			return bad("invalid goal")
		}
	}
	if len(plan.Milestone.Name) > 200 {
		return bad("milestone name too long")
	}
	if len(plan.Milestone.ReleaseID) > 200 {
		return bad("release id too long")
	}
	if plan.Milestone.Target != nil && *plan.Milestone.Target != "" {
		if _, err := time.Parse("2006-01-02", *plan.Milestone.Target); err != nil {
			return bad("invalid milestone target date")
		}
	}
	if len(plan.Thrusts) > 30 {
		return bad("too many thrusts")
	}
	for _, t := range plan.Thrusts {
		if t.Name == "" || len(t.Name) > 200 {
			return bad("invalid thrust name")
		}
		if len(t.Reason) > blackboardDraftMaxReasonBytes {
			return bad("thrust reason too long")
		}
		if t.GoalIndex < 0 || t.GoalIndex >= len(plan.Goals) {
			return bad("invalid thrust goal_index")
		}
	}
	return nil
}

// HandleDraftBlackboardPlan is POST /api/sessions/{session}/plan/draft: the server side of
// `blerg-runner plan draft`. Same own-session credential as the existing plan routes
// (agentBlackboardID/HandleBlackboardAgent) — an agent may ask for a draft of its own workspace;
// only the owner's bound answer may later turn it into a real blackboard.
func (a *API) HandleDraftBlackboardPlan(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, 503, noDatabaseMsg)
		return
	}
	sid, ok := a.authMessaging(w, r)
	if !ok {
		return
	}
	session := r.PathValue("session")
	if sid == "" || sid != session {
		writeError(w, 403, "Use this session's own credential.")
		return
	}
	row, err := db.GetSession(r.Context(), a.dbPool, session)
	if err != nil {
		blackboardError(w, err)
		return
	}
	if row == nil {
		writeError(w, 404, "Session not found.")
		return
	}
	root, err := sessionWorkspaceRoot(r.Context(), a.dbPool, row)
	if err != nil {
		blackboardError(w, err)
		return
	}
	// An optional JSON body lets Main hand over what it actually drafted (BLG-450 review M3); an
	// empty body generates a draft carrying only the real facts already known about the session
	// (its derived name, its linked board) — never placeholder text (round 5 correction). Read and
	// size-bound the body the same way decodeBlackboard
	// does, but treat a genuinely empty body as "nothing supplied" rather than a decode error —
	// json.Decoder.Decode on empty input returns io.EOF, which decodeBlackboard treats as invalid.
	var supplied blackboardDraftPlan
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		failurelog.Default.Event("blackboard_draft_request", slog.LevelWarn, "component", "runner", "stage", "draft_body_read", "cause", failurelog.Cause(err))
		writeError(w, 400, "Invalid draft request body.")
		return
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&supplied); err != nil {
			failurelog.Default.Event("blackboard_draft_request", slog.LevelWarn, "component", "runner", "stage", "draft_body_decode", "cause", failurelog.Cause(err))
			writeError(w, 400, "Invalid draft content.")
			return
		}
	}
	plan := mergeBlackboardDraftPlan(generateBlackboardDraftPlan(row), supplied)
	if err := validateBlackboardDraftPlan(plan); err != nil {
		blackboardError(w, err)
		return
	}
	data, err := json.Marshal(plan)
	if err != nil {
		failurelog.Default.Event("blackboard_draft_request", slog.LevelWarn, "component", "runner", "stage", "draft_marshal", "cause", failurelog.Cause(err))
		writeError(w, 500, "Could not build a draft.")
		return
	}
	if len(data) > db.MaxBlackboardDraftBytes {
		failurelog.Default.Event("blackboard_draft_request", slog.LevelWarn, "component", "runner", "stage", "draft_oversized", "cause", failurelog.Cause(nil))
		writeError(w, 500, "Draft too large to store.")
		return
	}
	draft, err := db.SaveBlackboardDraftRevision(r.Context(), a.dbPool, root, sid, data)
	if err != nil {
		blackboardError(w, err)
		return
	}
	a.broadcastBlackboardDraftChanged(r, root) //nolint:contextcheck // browser visibility checks use an independent bounded context after persistence
	writeJSON(w, 201, map[string]any{"id": draft.ID, "revision": draft.Revision, "untrusted_data": plan, "status": "draft_pending"})
}

// HandleGetBlackboardDraft is GET /api/blackboards/draft?session=ID. A draft is a private,
// not-yet-approved, person-only artifact (BLG-450 review L6), so this uses ownedSession — the
// same owner-only fence HandleDiscardBlackboardDraft and HandleCreateBlackboardFromDraft already
// use — not the broader canSeeAccount that HandleBlackboard/HandleListBlackboards use for an
// already-committed, approved blackboard. A non-owner who can merely see the workspace gets the
// same 404 an unowned session gets everywhere else, never a hint that a draft exists.
func (a *API) HandleGetBlackboardDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	session := r.URL.Query().Get("session")
	row, ok := a.ownedSession(r.Context(), w, p.Account, session)
	if !ok {
		return
	}
	root, err := sessionWorkspaceRoot(r.Context(), a.dbPool, row)
	if err != nil {
		blackboardError(w, err)
		return
	}
	draft, err := db.GetBlackboardDraft(r.Context(), a.dbPool, root)
	if err != nil {
		blackboardError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if draft == nil {
		writeJSON(w, 200, map[string]any{"draft": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"draft": map[string]any{
		"id":             draft.ID,
		"revision":       draft.Revision,
		"created_at":     draft.CreatedAt,
		"created_by":     draft.CreatedBy,
		"untrusted_data": draft.Draft,
	}})
}

// HandleDiscardBlackboardDraft is DELETE /api/blackboards/draft?session=ID: person-only, same
// ownership fence as other owner-only session actions (ownedSession).
// Retired entry points preserve authentication but cannot record a decision.
func (a *API) HandleDiscardBlackboardDraft(w http.ResponseWriter, r *http.Request) {
	a.retiredDraftDecision(w, r)
}
func (a *API) HandleCreateBlackboardFromDraft(w http.ResponseWriter, r *http.Request) {
	a.retiredDraftDecision(w, r)
}
func (a *API) retiredDraftDecision(w http.ResponseWriter, r *http.Request) {
	p, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	if _, ok = a.ownedSession(r.Context(), w, p.Account, r.URL.Query().Get("session")); !ok {
		return
	}
	writeError(w, http.StatusGone, "Reply to Main's bound question in chat or Needs you to decide this draft.")
}

// The workspace root is the event's session scope, so normal browser visibility
// filtering applies. No unreviewed content crosses the event bus.
func (a *API) broadcastBlackboardDraftChanged(r *http.Request, root string) {
	if a.hub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row, err := db.GetSession(ctx, a.dbPool, root)
	if err != nil {
		failurelog.Default.Event("blackboard_draft_request", slog.LevelWarn, "component", "runner", "stage", "broadcast_owner", "cause", failurelog.Cause(err))
		return
	}
	answers, err := db.UndeliveredQuestionAnswers(ctx, a.dbPool, root)
	if err != nil {
		failurelog.Default.Event("bound_draft_notice", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(err))
	} else {
		for _, answer := range answers {
			receipt, bound, receiptErr := a.boundReceipt(ctx, answer.ID)
			if receiptErr != nil {
				failurelog.Default.Event("bound_draft_notice", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(receiptErr))
				continue
			}
			if !bound || receipt["cause"] != "stale_draft" {
				continue
			}
			a.hub.BroadcastJSONPerAccount(func(account string) any {
				if row == nil || row.SpawningAccountID == nil || account != *row.SpawningAccountID {
					return nil
				}
				return protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(answer)}
			})
			a.hub.SignalAskAnswered(answer.ID)
			a.hub.QuestionAnswered(answer, derefOrEmpty(answer.Answer), "")
		}
	}
	// A random per-browser correlation marker contains no identity or draft content.
	origin := r.Header.Get("X-Blackboard-Origin")
	if !validSessionID(origin) {
		origin = ""
	}
	a.hub.BroadcastJSONPerAccount(func(account string) any {
		return map[string]any{"type": "blackboard_draft_changed", "session_id": root, "draft_readable": row != nil && ownsSession(account, row), "origin_id": origin}
	})
}

// blackboardSetupSource is the sendRelay source for "Set it up with Main" (part A of BLG-450
// phase 2): a fixed message Blerg writes itself, same pattern as membershipSource.
const blackboardSetupSource = "blackboard_setup"

// HandleRequestBlackboardSetup is POST /api/sessions/{session}/plan/request-setup: "Set it up
// with Main" on the empty blackboard screen. It reuses the existing worker-notice/relay
// mechanism (sendRelay, runner.go) that other UI actions already use to make Main do something
// (see membershipNotify) rather than inventing a new one. The text is Blerg's own fixed template,
// never anything the caller supplies — sendRelay refuses any source but a closed, known list, and
// this adds one more member of that list.
func (a *API) HandleRequestBlackboardSetup(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if p.Kind != "human" || p.OnBehalfOf != "" || p.Sid == "" {
		writeError(w, 403, "Sign in as a person.")
		return
	}
	if a.dbPool == nil {
		writeError(w, 503, "Database unavailable.")
		return
	}
	session := r.PathValue("session")
	main, err := blackboardMainSession(r.Context(), a.dbPool, p.Sub, session)
	if err != nil {
		blackboardError(w, err)
		return
	}
	if main == "" {
		writeError(w, 404, "Workspace not found.")
		return
	}
	if apiErr := a.sendBlackboardSetupRequest(r.Context(), p.Sub, main); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, 200, map[string]any{"status": "requested", "session": main})
}

// sendBlackboardSetupRequest is "Set it up with Main" itself: the empty-blackboard screen's own
// button (HandleRequestBlackboardSetup above) and the blackboard recommendation card's "Set it up
// with Main" action (blackboard_recommendation.go, BLG-454) both end here, so the two entry
// points ask Main to do exactly the same thing the exact same way.
func (a *API) sendBlackboardSetupRequest(ctx context.Context, account, main string) *APIError {
	text := "Set up this workspace's blackboard: run the planning skill's setup flow " +
		"(see skills/planning) to ask the person for a vision, a first goal and a first " +
		"milestone, then run `blerg-runner plan draft` and raise a person-required bound ask " +
		"with its draft ID and revision. The owner decides in chat or Needs you."
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	principal := runnerPrincipal{Kind: "human", Sub: account}
	// A deterministic client id, derived from the workspace's own root, dedupes two rapid "Set it
	// up with Main" clicks through the outbox's existing one-row-per-id uniqueness (same pattern
	// as membershipNotify's "membership-"+op.ID) rather than adding new state (BLG-450 review L5).
	return a.sendRelay(rctx, principal, main, text, blackboardSetupSource, "blackboard-setup-"+main)
}

// blackboardMainSession walks a session's own ancestry (same walk as agentBlackboardID) to find
// its root — the workspace's Main — stopping as soon as ownership no longer matches, so the
// caller can only ever be pointed at its own workspace's root, never another account's session.
func blackboardMainSession(ctx context.Context, pool db.DBTX, account, session string) (string, error) {
	row, err := db.GetSession(ctx, pool, session)
	if err != nil || row == nil {
		return "", err
	}
	if !ownsSession(account, row) {
		return "", nil
	}
	current := row
	for i := 0; i < 100 && current.ParentSessionID != nil; i++ {
		next, err := db.GetSession(ctx, pool, *current.ParentSessionID)
		if err != nil {
			return "", err
		}
		if next == nil || !ownsSession(account, next) {
			break
		}
		current = next
	}
	return current.ID, nil
}

// sessionWorkspaceRoot walks row's own ancestry (same bound as blackboardMainSession:
// agentBlackboardID's 100 hops) to find the workspace's root (Main), with no ownership check of
// its own: every caller here has already proven it may act on `row` (its own session credential
// for HandleDraftBlackboardPlan, or ownedSession/canSeeAccount for the person-facing handlers)
// before this is called, so the walk only needs to follow the real parent chain the database
// already has — it never widens who may read or write anything. This is what keys a blackboard
// draft by the workspace's root rather than by whichever session happened to generate or query it
// (BLG-450 review M4): a draft from a channel session is then visible from Main and vice versa.
func sessionWorkspaceRoot(ctx context.Context, pool db.DBTX, row *db.SessionRow) (string, error) {
	current := row
	for i := 0; i < 100 && current.ParentSessionID != nil; i++ {
		next, err := db.GetSession(ctx, pool, *current.ParentSessionID)
		if err != nil {
			return "", err
		}
		if next == nil {
			break
		}
		current = next
	}
	return current.ID, nil
}
