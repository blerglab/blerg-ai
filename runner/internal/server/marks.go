package server

// The History feed's agent-marked strokes (topics/strokes.go).
//
//   POST /api/sessions/{id}/marks — `blerg-runner mark decision|milestone|finding "<text>"` records one
//     mark; `blerg-runner mark --import <file.jsonl>` sends many ({"marks": [...]}, at most
//     topics.MaxImport). Who may mark: the session's own credential (its messaging token, bound to the
//     path's session: a child's token cannot mark its parent), the daemon token (the native loop), or
//     the session's owner signed in. The text is data: cleaned like a title (topics.CleanText), drawn
//     as text. Each mark is an agent event of kind "mark" (agent.MarkPayload), appended under the
//     session's mark lock (topics.MaxMarks per session) and fanned out after the commit. A back-dated
//     mark (at) has an event id derived from what it says, so an import sent twice records it once.

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/idempotency"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/topics"
)

// RegisterMarkRoutes registers the marks route on mux.
func (a *API) RegisterMarkRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/sessions/{id}/marks", a.HandlePostMarks)
	mux.HandleFunc("GET /api/board-cards/{board}/{number}/data", a.HandleCardData)
	mux.HandleFunc("GET /api/sessions/{id}/workspace/metrics", a.HandleWorkspaceMetrics)
	mux.HandleFunc("POST /api/sessions/{id}/workspace/metrics/runs", a.HandleWorkspaceMetrics)
	mux.HandleFunc("GET /api/insights/activity", a.HandleInsightsActivity)
	mux.HandleFunc("POST /api/sessions/{id}/skill-evidence", a.HandleSkillEvidence)
}

// maxMarksBody is the largest request body: an import of topics.MaxImport marks of the longest text.
const maxMarksBody = 1 << 20

type marksRequest struct {
	topics.MarkInput
	Marks []topics.MarkInput `json:"marks"`
}

// marksReply answers a post: the marks recorded (an imported one already held is in Duplicates).
type marksReply struct {
	Marks      []agent.MarkPayload `json:"marks"`
	Recorded   int                 `json:"recorded"`
	Duplicates int                 `json:"duplicates"`
}

// HandlePostMarks is POST /api/sessions/{id}/marks.
func (a *API) HandlePostMarks(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, noDatabaseMsg)
		return
	}
	ctx := r.Context()
	poster := "agent"
	var owner string // the signed-in person, when a person posts
	if p, ok := a.verifyBrowserToken(a.rawBearer(r)); ok {
		if p.Kind != "human" {
			writeError(w, http.StatusForbidden, "only the session's agent or its owner can mark its history")
			return
		}
		poster, owner = "owner", p.Sub
	} else {
		tokenSession, ok := a.authMessaging(w, r)
		if !ok {
			return
		}
		if tokenSession != "" && tokenSession != sid {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
	}
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	sess, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "check the session", "marks GetSession", err)
		return
	}
	if sess == nil || (owner != "" && !ownsSession(owner, sess)) {
		// Someone else's session reads as missing, as a private one does on every other route.
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if terminalSessionStatus(sess.Status) {
		writeError(w, http.StatusConflict, "This session has ended; its history can be marked only while it runs.")
		return
	}

	var req marksRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMarksBody)).Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the marks are larger than %d KiB; send fewer at a time", maxMarksBody>>10))
			return
		}
		writeError(w, http.StatusBadRequest, "The request body is not valid JSON.")
		return
	}
	inputs := req.Marks
	if inputs == nil {
		inputs = []topics.MarkInput{req.MarkInput}
	} else if req.Kind != "" || req.Text != "" {
		writeError(w, http.StatusBadRequest, "send one mark or a list of marks, not both")
		return
	}
	if len(inputs) == 0 {
		writeError(w, http.StatusBadRequest, "no marks to record")
		return
	}
	if len(inputs) > topics.MaxImport {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d marks at a time, got %d", topics.MaxImport, len(inputs)))
		return
	}
	key, keyErr := requestIdempotencyKey(r, false)
	if keyErr != nil {
		writeAPIError(w, keyErr)
		return
	}
	now := time.Now()
	marks := make([]agent.MarkPayload, 0, len(inputs))
	for i, in := range inputs {
		if (strings.EqualFold(strings.TrimSpace(in.Kind), "review") || strings.EqualFold(strings.TrimSpace(in.Kind), "failure")) && in.BoardID == "" {
			if strings.TrimSpace(in.At) != "" {
				writeError(w, http.StatusBadRequest, "An imported review or failure needs its explicit board_id.")
				return
			}
			var boards []string
			err := a.dbPool.QueryRow(ctx, `WITH RECURSIVE family AS (SELECT id,parent_session_id FROM sessions WHERE id=$1 UNION ALL SELECT s.id,s.parent_session_id FROM sessions s JOIN family f ON s.id=f.parent_session_id WHERE s.spawning_account_id=$2)
 SELECT ARRAY(SELECT DISTINCT b FROM (SELECT l.board_id AS b FROM session_card_links l JOIN family f ON l.session_id=f.id WHERE l.card_number=$3 UNION SELECT g.builtin_ref FROM session_mcp_grants g JOIN family f ON g.session_id=f.id WHERE g.builtin='board' AND g.builtin_ref<>'') candidates)`, sid, sess.SpawningAccountID, in.Card).Scan(&boards)
			if err != nil {
				writeDatabaseFailure(w, "resolve review card", "review board context", err)
				return
			}
			if len(boards) != 1 {
				writeError(w, http.StatusBadRequest, "A review or failure needs one board context: pin the card with board_id first, or include board_id in the import.")
				return
			}
			in.BoardID = boards[0]
		}
		m, err := topics.CleanMark(in, poster, now)
		if err != nil {
			if len(inputs) > 1 {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("mark %d: %s", i+1, err.Error()))
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if m.Kind == "review" || m.Kind == "failure" {
			var allowed bool
			err := a.dbPool.QueryRow(ctx, `WITH RECURSIVE family AS (
              SELECT id,parent_session_id FROM sessions WHERE id=$1 AND spawning_account_id=$2
              UNION SELECT s.id,s.parent_session_id FROM sessions s JOIN family f ON s.id=f.parent_session_id WHERE s.spawning_account_id=$2
            ) SELECT EXISTS(SELECT 1 FROM session_card_links l JOIN family f ON l.session_id=f.id WHERE l.board_id=$3 AND l.card_number=$4)`, sid, sess.SpawningAccountID, m.BoardID, m.Card).Scan(&allowed)
			if err != nil {
				writeDatabaseFailure(w, "check card access", "structured mark context", err)
				return
			}
			if !allowed {
				level, levelErr := a.effectiveBoardLevel(ctx, sess, m.BoardID)
				if levelErr != nil {
					writeDatabaseFailure(w, "check board access", "structured mark level", levelErr)
					return
				}
				proof, valid := level.proof()
				allowed = db.BoardLevelRank(level.Level) >= db.BoardLevelRank(db.BoardLevelRead) && valid && sess.SpawningAccountID != nil && proof.AccountID == *sess.SpawningAccountID
			}
			if !allowed {
				writeError(w, http.StatusForbidden, "Link this workspace to the card or grant board access before recording a review or failure.")
				return
			}
		}
		marks = append(marks, m)
	}

	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		writeDatabaseFailure(w, "record the marks", "marks begin "+sid, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.LockMarks(ctx, tx, sid); err != nil {
		writeDatabaseFailure(w, "record the marks", "marks lock "+sid, err)
		return
	}
	// Claim the request and append its events in the same transaction. A lost reply can then
	// be retried without claiming a request whose mark was rolled back.
	scope := idempotency.Scope("marks", sid, poster, owner)
	requestHash := ""
	if key != "" {
		raw, _ := json.Marshal(marks)
		requestHash = fmt.Sprintf("%x", sha256.Sum256(raw))
		same, err := db.ClaimMarkRequest(ctx, tx, scope, key, requestHash, sid)
		if err != nil {
			writeDatabaseFailure(w, "record the marks", "marks request", err)
			return
		}
		if !same {
			writeError(w, http.StatusConflict, idempotencyConflictMessage)
			return
		}
	}
	type fresh struct {
		id  string
		raw []byte
		seq int64
		at  time.Time
	}
	var out []fresh
	reply := marksReply{Marks: make([]agent.MarkPayload, 0, len(marks))}
	for i, m := range marks {
		eventID := topics.MarkEventID(sid, m)
		if key != "" && m.At == "" {
			eventID = markRequestEventID(scope, key, requestHash, i)
		}
		m.ID = topics.NewMarkID()
		if m.At != "" || key != "" {
			m.ID = "mk_" + eventID[:8]
		}
		raw, err := json.Marshal(m)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encode failed")
			return
		}
		raw = protocol.StripNUL(raw)
		at := time.Now()
		seq, inserted, err := db.AppendAgentEventTx(ctx, tx, sid, eventID, "mark", string(raw), at)
		if err != nil {
			writeDatabaseFailure(w, "record the marks", "marks append "+sid, err)
			return
		}
		reply.Marks = append(reply.Marks, m)
		if !inserted {
			reply.Duplicates++
			continue
		}
		reply.Recorded++
		out = append(out, fresh{eventID, raw, seq, at})
	}
	// The cap counts what the session holds after this request, so a mark already recorded (an import
	// sent again) never counts twice; over the cap, nothing of this request is kept.
	warning := ""
	if reply.Recorded > 0 {
		n, err := db.CountMarks(ctx, tx, sid)
		if err != nil {
			writeDatabaseFailure(w, "record the marks", "marks count "+sid, err)
			return
		}
		if n > topics.MaxMarks {
			slog.Warn("marks refused", "cause", "mark_capacity", "session_id", sid, "count", n, "limit", topics.MaxMarks)
			writeError(w, http.StatusConflict, fmt.Sprintf("this session would hold %d marks; at most %d (nothing was recorded)", n, topics.MaxMarks))
			return
		}
		warning = markCapacityWarning(n-reply.Recorded, n)
	}
	if err := tx.Commit(ctx); err != nil {
		writeDatabaseFailure(w, "record the marks", "marks commit "+sid, err)
		return
	}
	a.hub.invalidateDatasetSession(ctx, a.dbPool, sid, true)
	if reply.Recorded > 0 {
		// A card can have work in several workspace roots. Its account-scoped
		// source is re-read only by existing authorized subscribers.
		a.hub.datasets.mu.Lock()
		for key, scope := range a.hub.datasets.scopes {
			if key.name == "blerg.cards.time" || key.name == "blerg.reviews.pairs" {
				scope.revision.Add(1)
			}
		}
		a.hub.datasets.mu.Unlock()
	}
	for _, f := range out {
		fanOutServerEvent(a.hub, sid, f.id, "mark", f.raw, f.seq, f.at)
	}
	if warning != "" {
		principal := runnerPrincipal{Kind: "human", Sub: derefOrEmpty(sess.SpawningAccountID)}
		if problem := a.sendRelay(ctx, principal, sid, warning, markCapacitySource); problem != nil {
			slog.Warn("mark capacity warning delivery failed", "cause", "relay_refused", "status", problem.Status)
		}
		a.hub.BroadcastJSON(protocol.SessionNotice{ //nolint:contextcheck // browser fan-out owns its per-account privacy lookup contexts
			Type: "session_notice", SessionID: sid, Status: "warning",
			Title: "History capacity", Body: warning, URL: "/sessions/" + sid,
		})
	}
	writeJSON(w, http.StatusCreated, reply)
}

// An event identity belongs to the authorized caller, session, request and position in its batch.
func markRequestEventID(scope, key, hash string, index int) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + key + "\x00" + hash + "\x00" + strconv.Itoa(index)))
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

const markCapacitySource = "mark_capacity"

// Only new marks crossing a threshold warn; duplicate imports never repeat a warning.
// An import spanning several thresholds emits one notice for its highest threshold.
func markCapacityWarning(before, after int) string {
	reached := 0
	for _, percent := range []int{80, 90, 95} {
		threshold := topics.MaxMarks * percent / 100
		if before < threshold && after >= threshold {
			reached = percent
		}
	}
	if reached == 0 {
		return ""
	}
	return fmt.Sprintf("This session has used %d%% of its history mark capacity (%d of %d; %d remaining). Plan a replacement session before capacity is exhausted and preserve its context. Reviews and failures will be refused at the limit; existing history is retained.", reached, after, topics.MaxMarks, topics.MaxMarks-after)
}
