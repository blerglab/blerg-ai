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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/topics"
)

// RegisterMarkRoutes registers the marks route on mux.
func (a *API) RegisterMarkRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/sessions/{id}/marks", a.HandlePostMarks)
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
	now := time.Now()
	marks := make([]agent.MarkPayload, 0, len(inputs))
	for i, in := range inputs {
		m, err := topics.CleanMark(in, poster, now)
		if err != nil {
			if len(inputs) > 1 {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("mark %d: %s", i+1, err.Error()))
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		marks = append(marks, m)
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
	type fresh struct {
		id  string
		raw []byte
		seq int64
		at  time.Time
	}
	var out []fresh
	reply := marksReply{Marks: make([]agent.MarkPayload, 0, len(marks))}
	for _, m := range marks {
		eventID := topics.MarkEventID(sid, m)
		m.ID = topics.NewMarkID()
		if m.At != "" {
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
	if reply.Recorded > 0 {
		n, err := db.CountMarks(ctx, tx, sid)
		if err != nil {
			writeDatabaseFailure(w, "record the marks", "marks count "+sid, err)
			return
		}
		if n > topics.MaxMarks {
			writeError(w, http.StatusConflict, fmt.Sprintf("this session would hold %d marks; at most %d (nothing was recorded)", n, topics.MaxMarks))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeDatabaseFailure(w, "record the marks", "marks commit "+sid, err)
		return
	}
	for _, f := range out {
		fanOutServerEvent(a.hub, sid, f.id, "mark", f.raw, f.seq, f.at)
	}
	writeJSON(w, http.StatusCreated, reply)
}
