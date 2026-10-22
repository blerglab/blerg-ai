package server

// What a session works through, for the session panel and the chat (docs/talking-to-an-agent.md):
//
//   POST /api/sessions/{id}/checklist — `blerg-runner checklist`: set, add, tick, untick, remove or clear the
//     session's checklist. Each write records a checklist event holding the FULL list after the change
//     and the item it touched (checklist.Payload), so a reload replays it from the event alone.
//   POST /api/sessions/{id}/tickets — `blerg-runner pin` / `unpin`: pin a tracker's ticket (any
//     tracker, one source-neutral shape: ticket.Clean) into the session, or unpin one. Each records a
//     ticket event with the whole ticket and what it did (pin, update: a re-pin, with the status and
//     title it had; unpin).
//
// Both take the session's own credential, as topics do (its messaging token, bound to the path's
// session, or the daemon token); a person's is refused, and no browser writes here. The read of the
// current state and the append run in one transaction under a per-session advisory lock, so two quick
// writes never lose one; an ended session refuses with 409. GET /api/sessions/{id}/live carries the
// checklist, the pinned tickets (the current one in full) and the engine's plan (workState).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/blerglab/blerg-ai/runner/internal/checklist"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/livestate"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/ticket"
)

const (
	checklistBodyLimit = 64 << 10
	ticketBodyLimit    = 256 << 10
)

// RegisterWorklistRoutes registers the checklist's and the pinned tickets' routes on mux.
func (a *API) RegisterWorklistRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/{id}/board-options", a.HandleFilingBoards)
	mux.HandleFunc("GET /api/sessions/{id}/board-search", a.HandleFilingSearch)
	mux.HandleFunc("POST /api/sessions/{id}/file-ticket", a.HandleFileTicket)
	mux.HandleFunc("POST /api/sessions/{id}/board-create", a.HandleProposeBoard)
	mux.HandleFunc("GET /api/sessions/{id}/proposals/{pid}", a.HandleSessionProposalStatus)
	mux.HandleFunc("POST /api/sessions/{id}/filing-tools", a.HandleFilingTools)
	mux.HandleFunc("GET /api/sessions/{id}/plugins", a.HandleSessionPlugins)
	mux.HandleFunc("POST /api/sessions/{id}/plugins", a.HandleAddSessionPlugin)
	mux.HandleFunc("POST /api/sessions/{id}/plugin-requests", a.HandleSessionPluginRequest)
	mux.HandleFunc("POST /api/sessions/{id}/checklist", a.HandlePostChecklist)
	mux.HandleFunc("POST /api/sessions/{id}/title", a.HandleRenameOwnSession)
	mux.HandleFunc("GET /api/sessions/{id}/checklist/workers", a.HandleChecklistWorkers)
	mux.HandleFunc("POST /api/sessions/{id}/progress", a.HandlePostProgress)
	mux.HandleFunc("POST /api/sessions/{id}/tickets", a.HandlePostTickets)
}

// agentSessionWrite admits an agent's write to its own running session and reads the body (at most
// limit bytes). ok=false when it already answered.
func (a *API) agentSessionWrite(w http.ResponseWriter, r *http.Request, what string, limit int64) (sid string, body []byte, ok bool) {
	sid = r.PathValue("id")
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, noDatabaseMsg)
		return "", nil, false
	}
	tokenSession, ok := a.authMessaging(w, r)
	if !ok {
		return "", nil, false
	}
	if tokenSession != "" && tokenSession != sid {
		writeError(w, http.StatusForbidden, "forbidden")
		return "", nil, false
	}
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "session not found")
		return "", nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("The request body is larger than %d KiB.", limit>>10))
			return "", nil, false
		}
		writeError(w, http.StatusBadRequest, "The request body could not be read.")
		return "", nil, false
	}
	sess, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "check the session", what+" GetSession", err)
		return "", nil, false
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return "", nil, false
	}
	if sess.DiscardRequestedAt != nil || sess.ArchivedAt != nil || terminalSessionStatus(sess.Status) {
		writeError(w, http.StatusConflict, fmt.Sprintf("This session has ended; its %s can change only while it runs.", what))
		return "", nil, false
	}
	return sid, body, true
}

// appendWorkEvent records one event of kind in tx, commits and fans it out.
func (a *API) appendWorkEvent(ctx context.Context, tx pgx.Tx, sid, kind string, raw []byte) (seq int64, eventID string, at time.Time, err error) {
	at = time.Now()
	eventID = newUUID()
	if seq, _, err = db.AppendAgentEventTx(ctx, tx, sid, eventID, kind, string(raw), at); err != nil {
		return 0, "", at, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, "", at, err
	}
	fanOutServerEvent(a.hub, sid, eventID, kind, raw, seq, at)
	return seq, eventID, at, nil
}

// HandlePostChecklist is POST /api/sessions/{id}/checklist.
func (a *API) HandlePostChecklist(w http.ResponseWriter, r *http.Request) {
	sid, body, ok := a.agentSessionWrite(w, r, "checklist", checklistBodyLimit)
	if !ok {
		return
	}
	req, err := checklist.Parse(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		writeDatabaseFailure(w, "record the checklist", "checklist begin "+sid, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.LockSessionWork(ctx, tx, "checklist", sid); err != nil {
		writeDatabaseFailure(w, "record the checklist", "checklist lock "+sid, err)
		return
	}
	n, err := db.CountSessionKind(ctx, tx, sid, "checklist")
	if err != nil {
		writeDatabaseFailure(w, "record the checklist", "checklist count "+sid, err)
		return
	}
	if n >= checklist.MaxEvents {
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("This session's checklist has changed %d times, its most; it cannot change again.", checklist.MaxEvents))
		return
	}
	latest, err := db.LatestChecklist(ctx, tx, sid, 1<<62)
	if err != nil {
		writeDatabaseFailure(w, "record the checklist", "checklist latest "+sid, err)
		return
	}
	var cur []checklist.Item
	if latest != nil {
		if p, ok := checklist.ReadWithSeed([]byte(latest.Payload), latest.ClientEventID); ok {
			cur = p.Items
		}
	}
	// Resolve native aliases in one bounded batch, including existing references.
	nativeIDs := checklistNativeIDs(cur)
	if req.Assignee != nil && req.Assignee.Kind == "subagent" {
		nativeIDs = append(nativeIDs, req.Assignee.ID)
	}
	resolved, resolveErr := checklistNativeWorkers(ctx, tx, sid, nativeIDs, 1<<62)
	if resolveErr != nil {
		writeDatabaseFailure(w, "check checklist workers", "checklist workers "+sid, resolveErr)
		return
	}
	checklistCanonicalAssignments(cur, resolved)
	if req.Assignee != nil {
		worker, workerErr := *req.Assignee, error(nil)
		if worker.Kind == "subagent" {
			if id, ok := resolved[worker.ID]; ok && checklist.ValidAssignee(worker) {
				worker.ID = id
			} else {
				workerErr = errChecklistWorker
			}
		} else {
			worker, workerErr = checklistWorker(ctx, tx, sid, worker)
		}
		if workerErr != nil {
			if errors.Is(workerErr, errChecklistWorker) {
				writeError(w, http.StatusBadRequest, workerErr.Error())
			} else {
				writeDatabaseFailure(w, "check the checklist worker", "checklist worker "+sid, workerErr)
			}
			return
		}
		req.Assignee = &worker
	}
	p, err := checklist.Apply(cur, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, err := json.Marshal(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode failed")
		return
	}
	raw = protocol.StripNUL(raw)
	if _, _, _, err := a.appendWorkEvent(ctx, tx, sid, "checklist", raw); err != nil {
		writeDatabaseFailure(w, "record the checklist", "checklist append "+sid, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

type ticketRequest struct {
	Action string          `json:"action"`
	Ticket json.RawMessage `json:"ticket"`
	Source string          `json:"source"`
	Key    string          `json:"key"`
}

// ticketReply answers a pin or an unpin: the event's payload and what is pinned after it, the
// current one first.
type ticketReply struct {
	ticket.Payload
	Pinned []ticket.Summary `json:"pinned"`
}

// HandlePostTickets is POST /api/sessions/{id}/tickets: {action: "pin", ticket: {...}} or
// {action: "unpin", source?, key?} (neither: the current pin).
func (a *API) HandlePostTickets(w http.ResponseWriter, r *http.Request) {
	sid, body, ok := a.agentSessionWrite(w, r, "pinned tickets", ticketBodyLimit)
	if !ok {
		return
	}
	var req ticketRequest
	if err := json.Unmarshal(body, &req); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			writeError(w, http.StatusBadRequest, te.Field+" must be a string")
			return
		}
		writeError(w, http.StatusBadRequest, "The request body is not valid JSON.")
		return
	}
	var t ticket.Ticket
	switch req.Action {
	case "pin":
		var err error
		if t, err = ticket.Clean(req.Ticket); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// A Blerg Board card's address is the runner's to set: the Board's page for it, inside
		// Blerg, never a link the agent wrote.
		if board, number, ok := ticket.BoardCard(t); ok {
			t.URL = a.boardCardURL(r.Context(), board, number)
		}
	case "unpin":
	default:
		writeError(w, http.StatusBadRequest, `action must be "pin" or "unpin"`)
		return
	}
	ctx := r.Context()
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		writeDatabaseFailure(w, "record the ticket", "tickets begin "+sid, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.LockSessionWork(ctx, tx, "tickets", sid); err != nil {
		writeDatabaseFailure(w, "record the ticket", "tickets lock "+sid, err)
		return
	}
	rows, err := db.ListTicketRows(ctx, tx, sid, 1<<62)
	if err != nil {
		writeDatabaseFailure(w, "record the ticket", "tickets rows "+sid, err)
		return
	}
	pinned := ticket.Pinned(rows)
	var p ticket.Payload
	if req.Action == "pin" {
		// The cap refuses a pin only: an unpin is always allowed, so nothing stays pinned for good.
		if len(rows) >= ticket.MaxEvents {
			writeError(w, http.StatusTooManyRequests, fmt.Sprintf("This session has pinned tickets %d times, its most; unpin still works.", ticket.MaxEvents))
			return
		}
		p = ticket.Payload{Ticket: t, Action: "pin"}
		if old := findPin(pinned, t.Source, t.Key, t.BoardID); old != nil {
			p.Action = "update"
			p.Previous = &ticket.Previous{Status: old.Status, Title: old.Title}
		} else if len(pinned) >= ticket.MaxPinned {
			writeError(w, http.StatusConflict, fmt.Sprintf("%d tickets are pinned already, the most; unpin one first.", ticket.MaxPinned))
			return
		}
	} else {
		target, msg := unpinTarget(pinned, strings.TrimSpace(req.Source), strings.TrimSpace(req.Key))
		if target == nil {
			writeError(w, http.StatusConflict, msg)
			return
		}
		p = ticket.Payload{Ticket: ticket.Ticket{Source: target.Source, Key: target.Key, BoardID: target.BoardID, Title: target.Title, Status: target.Status, URL: target.URL}, Action: "unpin"}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode failed")
		return
	}
	raw = protocol.StripNUL(raw)
	seq, eventID, at, err := a.appendWorkEvent(ctx, tx, sid, "ticket", raw)
	if err != nil {
		writeDatabaseFailure(w, "record the ticket", "tickets append "+sid, err)
		return
	}
	after := ticket.Pinned(append(rows, ticket.Row{Seq: seq, EventID: eventID, Ts: at, Action: p.Action,
		Source: p.Ticket.Source, Key: p.Ticket.Key, BoardID: p.Ticket.BoardID, Title: p.Ticket.Title, Status: p.Ticket.Status, URL: p.Ticket.URL}))
	writeJSON(w, http.StatusCreated, ticketReply{Payload: p, Pinned: after})
}

// findPin is the pin (source, key, board) names; board is a Blerg Board card's board ("" for any
// other tracker).
func findPin(pinned []ticket.Summary, source, key, board string) *ticket.Summary {
	for i := range pinned {
		if pinned[i].Source == source && pinned[i].Key == key && pinned[i].BoardID == board {
			return &pinned[i]
		}
	}
	return nil
}

// unpinTarget is the pin an unpin names: (source, key), a key alone when one pin has it, the
// current pin when neither is given; else nil and why.
func unpinTarget(pinned []ticket.Summary, source, key string) (*ticket.Summary, string) {
	if len(pinned) == 0 {
		return nil, "No ticket is pinned."
	}
	if key == "" {
		if source == "" {
			return &pinned[0], ""
		}
		return nil, "Name the ticket as <source>:<key>, or give neither to unpin the current one."
	}
	var match *ticket.Summary
	// unknownBoard: a legacy card pin with the asked number but no recorded board at all (no
	// board ID, no board name in its key), asked for on a named board: it may or may not be on
	// that board, so when no other pin matches, the unpin is refused rather than guessed.
	unknownBoard := false
	for i := range pinned {
		p := &pinned[i]
		if source != "" && p.Source != source {
			continue
		}
		matches := p.Key == key
		if p.Source == ticket.SourceBoard {
			prefix, number, ok := boardUnpinKey(key)
			oldPrefix, oldNumber, oldOK := boardUnpinKey(p.Key)
			if ok && oldOK && number == oldNumber {
				// Old pins kept a display name in Key, sometimes with no BoardID. A qualified key
				// names the recorded board ID or the stored board name; a pin with neither
				// recorded matches only a bare #number.
				switch {
				case prefix == "":
					matches = true
				case p.BoardID != "" && prefix == p.BoardID:
					matches = true
				case oldPrefix != "":
					matches = prefix == oldPrefix
				case p.BoardID == "":
					matches, unknownBoard = false, true
				default:
					matches = false
				}
			} else if ok && oldOK {
				matches = false
			}
		}
		if !matches {
			continue
		}
		if match != nil {
			if source == ticket.SourceBoard {
				return nil, fmt.Sprintf("Two pinned cards are %s; name it as %s:<board id or board name>#<number>.", key, source)
			}
			return nil, fmt.Sprintf("Two pinned tickets have the key %s; name it as <source>:<key> (with a board id or name for Blerg cards).", key)
		}
		match = p
	}
	// Refused only when nothing else matched: a pin recorded on the named board is the one meant,
	// and the board-less pin can still be unpinned by its bare #number afterwards.
	if match == nil && unknownBoard {
		_, number, _ := boardUnpinKey(key)
		return nil, fmt.Sprintf("A pinned card #%d has no recorded board, so it cannot be told apart by %s; unpin it as #%d.", number, key, number)
	}
	if match != nil {
		return match, ""
	}
	if source != "" {
		return nil, fmt.Sprintf("%s:%s is not pinned.", source, key)
	}
	return nil, fmt.Sprintf("%s is not pinned.", key)
}

// boardUnpinKey compares a modern #number with a legacy "Board name #number".
// The caller retains the stored key and board ID in the unpin event so replay removes it.
func boardUnpinKey(key string) (prefix string, number int, ok bool) {
	prefix, raw, found := strings.Cut(strings.TrimSpace(key), "#")
	if !found {
		raw, prefix = prefix, ""
	}
	number, err := strconv.Atoi(strings.TrimSpace(raw))
	return strings.TrimSpace(prefix), number, err == nil && number > 0
}

// liveChecklist is the live state's checklist: the newest list, when it has items.
type liveChecklist struct {
	Items     []checklist.Item `json:"items"`
	EventID   string           `json:"event_id"`
	UpdatedAt time.Time        `json:"updated_at"`
	// CommitURLs maps a commit sha the items hold to its web page, when the repository is known.
	CommitURLs map[string]string `json:"commit_urls,omitempty"`
	// WorkerTitles names the items' assignees, keyed "<kind>:<id>" (checklistWorkerTitles).
	WorkerTitles map[string]string `json:"worker_titles,omitempty"`
}

// liveTicket is the current pin in full (its event's ticket), with the event that pinned it last.
type liveTicket struct {
	ticket.Ticket
	EventID   string    `json:"event_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// workState is what GET /live carries besides livestate.State: each null (tickets empty) when the
// session has none.
type workState struct {
	Progress  *livestate.Progress `json:"-"`
	Checklist *liveChecklist      `json:"checklist"`
	Plan      *livestate.Plan     `json:"plan"`
	Tickets   []ticket.Summary    `json:"tickets"`
	Ticket    *liveTicket         `json:"ticket"`
}

// readWorkState reads a session's checklist, engine plan and pins as of seq through.
func readWorkState(ctx context.Context, q db.Querier, sid string, through int64) (workState, error) {
	ws := workState{Tickets: []ticket.Summary{}}
	latest, err := db.LatestChecklist(ctx, q, sid, through)
	if err != nil {
		return ws, err
	}
	if latest != nil {
		if p, ok := checklist.ReadWithSeed([]byte(latest.Payload), latest.ClientEventID); ok && len(p.Items) > 0 {
			resolved, resolveErr := checklistNativeWorkers(ctx, q, sid, checklistNativeIDs(p.Items), through)
			if resolveErr != nil {
				return ws, resolveErr
			}
			checklistCanonicalAssignments(p.Items, resolved)
			titles, titleErr := checklistWorkerTitles(ctx, q, sid, p.Items, through)
			if titleErr != nil {
				return ws, titleErr
			}
			ws.Checklist = &liveChecklist{Items: p.Items, EventID: latest.ClientEventID, UpdatedAt: latest.Ts}
			if len(titles) > 0 {
				ws.Checklist.WorkerTitles = titles
			}
		}
	}
	planRows, err := db.ListPlanEvents(ctx, q, sid, through)
	if err != nil {
		return ws, err
	}
	ws.Plan = livestate.PlanFrom(liveEvents(planRows))
	progressRows, err := db.ListScopeProgressEvents(ctx, q, sid, through, "", 0)
	if err != nil {
		return ws, err
	}
	ws.Progress = livestate.ProgressFor(liveEvents(progressRows), "", 0)
	rows, err := db.ListTicketRows(ctx, q, sid, through)
	if err != nil {
		return ws, err
	}
	ws.Tickets = ticket.Pinned(rows)
	if len(ws.Tickets) > 0 {
		cur := ws.Tickets[0]
		raw, err := db.TicketAt(ctx, q, sid, cur.Seq)
		if err != nil {
			return ws, err
		}
		var t ticket.Ticket
		if raw != nil && json.Unmarshal(raw, &t) == nil {
			ws.Ticket = &liveTicket{Ticket: t, EventID: cur.EventID, UpdatedAt: cur.UpdatedAt}
		}
	}
	return ws, nil
}

// addChecklistCommitURLs gives the checklist's commits their web pages when the session's
// repository is known (the same rule as the History tab's commit chips).
func (a *API) addChecklistCommitURLs(row *db.SessionRow, ws *workState) {
	if ws.Checklist == nil {
		return
	}
	var repo gitprovider.Ref
	have, looked := false, false
	for _, it := range ws.Checklist.Items {
		for _, sha := range it.Commits {
			if !looked {
				repo, have = a.sessionRepo(row)
				looked = true
			}
			if !have {
				return
			}
			if u, ok := gitprovider.Default.CommitURL(repo, sha); ok && u != "" {
				if ws.Checklist.CommitURLs == nil {
					ws.Checklist.CommitURLs = map[string]string{}
				}
				ws.Checklist.CommitURLs[sha] = u
			}
		}
	}
}
