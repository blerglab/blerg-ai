package server

// The session's plan keeps itself honest (0.4.5, card #158). A coordinating session pins the cards
// it works on and keeps a checklist; both used to drift because keeping them current relied on the
// agent remembering. Two parts, both deterministic (no model judges anything here):
//
// Automatic, where the runner sees the fact itself:
//   - A card the session moves (its board tools: `blerg-runner ticket move` and the blerg_board
//     session tool, or its launch board connection's blerg_card_move) into an in-progress column is
//     pinned, filled from the Board. With five pinned already, the oldest pin whose card is done is
//     unpinned first; when none is done the card is not pinned and the next nudge says so.
//   - A card the session moves into a done column is unpinned, and every open checklist item whose
//     text names it (#N as a whole token, the same parsing as the chat's card chips, on the
//     session's one board) is ticked with the note "ticked: #N done".
//   - A board-started session pins the card it was started for.
//   - A child session started with a title or prompt naming #N is assigned to the parent's open
//     checklist item naming #N.
//   - A Blerg Board card pinned with `blerg-runner pin` is filled from the Board (title,
//     description, column, type, priority, tags); what the agent sent stays only where the Board
//     has nothing.
//
// Nudges are held at turn end and attached to the next input, each fact once: a marked Blerg
// system message (source planDriftSource) saying exactly what drifted and the commands that fix it,
// when all pinned tickets are done while the turn ran tools; when the checklist is all ticked, or
// none of it is doing, while the turn ran tools; when a child session or background subagent started
// this turn has no checklist item; when a pinned card was closed on the board; when
// a child looks finished (stopped or idle, and its result was read with nothing since). The person
// turns nudges off per session (the session panel); a cron session is nudged only when it uses pins
// or a checklist. Facts wait for the next input and never start a turn.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/blerglab/blerg-ai/runner/internal/checklist"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/ticket"
)

// planDriftSource is the source of a drift nudge in the session's transcript.
const planDriftSource = "plan_drift"

// planCheckTimeout bounds one turn's check: its reads, the board reads and the relay.
const planCheckTimeout = 45 * time.Second

// ─── #N in text ───────────────────────────────────────────────────────────────

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// cardRefs are the card numbers text names as whole tokens, in order, each once: "#147", not
// "&#147", "a#1", "/#1", "##1", "#1a", "#1-2" or a number of more than 7 digits (the chat's rule,
// packages/chat/src/model/refs.ts).
func cardRefs(text string) []int {
	var out []int
	for i := 0; i < len(text); i++ {
		if text[i] != '#' {
			continue
		}
		if i > 0 {
			if p := text[i-1]; isWordByte(p) || p == '&' || p == '#' || p == '/' {
				continue
			}
		}
		j := i + 1
		for j < len(text) && text[j] >= '0' && text[j] <= '9' {
			j++
		}
		if j == i+1 || j-i-1 > 7 {
			continue
		}
		if j < len(text) && (isWordByte(text[j]) || text[j] == '-') {
			continue
		}
		n, _ := strconv.Atoi(text[i+1 : j])
		if n > 0 && !slices.Contains(out, n) {
			out = append(out, n)
		}
		i = j - 1
	}
	return out
}

// ─── Columns ──────────────────────────────────────────────────────────────────

// columnInProgress is the Board's own "work column" rule (board/internal/api/review.go
// isWorkColumn): a name with progress, doing or working in it.
func columnInProgress(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "progress") || strings.Contains(n, "doing") || strings.Contains(n, "working")
}

// cardDone reports whether a card read with its columns is done: archived, or in a column the Board
// marks terminal (its is_terminal, which already folds in the "done" name rule).
func cardDone(c *liveBoardCard) bool {
	if c.ArchivedAt != nil && *c.ArchivedAt != "" {
		return true
	}
	if c.ColumnID != nil {
		for _, col := range c.Columns {
			if col.ID == *c.ColumnID {
				return col.IsTerminal
			}
		}
	}
	return strings.Contains(strings.ToLower(c.ColumnName), "done")
}

// statusDone is the rule for a pin whose card cannot be read: its recorded status names a done
// state the way trackers say it.
func statusDone(status string) bool {
	s := strings.ToLower(status)
	return strings.Contains(s, "done") || strings.Contains(s, "closed") || strings.Contains(s, "resolved")
}

// ─── Reading a card with the session's board access ──────────────────────────

var errPlanNoBoardRead = errors.New("the session cannot read this board")

// planReadCard reads card number on boardID with the session's own board access (at least read),
// with its column name and the board's columns: the same exchange the session's board tools make.
func (a *API) planReadCard(ctx context.Context, row *db.SessionRow, boardID string, number int) (*liveBoardCard, error) {
	caller := a.sessionBoardCaller()
	if caller == nil || boardID == "" || number <= 0 {
		return nil, errPlanNoBoardRead
	}
	lv, err := a.effectiveBoardLevel(ctx, row, boardID)
	if err != nil {
		return nil, err
	}
	if !mcpgw.BoardLevelAllows(lv.Level, "blerg_card_get") {
		return nil, errPlanNoBoardRead
	}
	proof, ok := lv.proof()
	if !ok || proof.AccountID == "" || row.SpawningAccountID == nil || *row.SpawningAccountID != proof.AccountID {
		return nil, errPlanNoBoardRead
	}
	key := proof.AccountID + ":" + boardID + ":" + strconv.Itoa(number)
	account := proof.AccountID
	a.planReads.mu.Lock()
	if a.planReads.cards == nil {
		a.planReads.cards = map[string]planCachedCard{}
		a.planReads.accounts = map[string]*planReadAccount{}
	}
	if cached, ok := a.planReads.cards[key]; ok && time.Since(cached.at) < time.Minute {
		a.planReads.mu.Unlock()
		return cached.card, nil
	}
	budget := a.planReads.accounts[account]
	if budget == nil {
		budget = &planReadAccount{}
		a.planReads.accounts[account] = budget
	}
	if time.Since(budget.since) >= time.Minute {
		budget.since = time.Now()
		budget.count = 0
	}
	if budget.count >= 30 || budget.active >= 2 {
		a.planReads.mu.Unlock()
		return nil, errors.New("plan read budget exhausted")
	}
	budget.count++
	budget.active++
	a.planReads.mu.Unlock()
	defer func() { a.planReads.mu.Lock(); budget.active--; a.planReads.mu.Unlock() }()
	args, _ := json.Marshal(map[string]any{"board_id": boardID, "number": number, "with_trail": true})
	raw, err := caller.CallBoard(ctx, mcpgw.BoardCall{Proof: proof, BoardID: boardID, SessionRef: row.ID, Tool: "blerg_card_get",
		Arguments: args, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	var card liveBoardCard
	if apiErr := boardToolResult(raw, &card); apiErr != nil {
		return nil, errors.New(apiErr.Message)
	}
	if card.Number != number {
		return nil, errors.New("the board answered another card")
	}
	if card.BoardID == "" {
		card.BoardID = boardID
	}
	a.planReads.mu.Lock()
	if len(a.planReads.cards) > 1024 {
		clear(a.planReads.cards)
	}
	a.planReads.cards[key] = planCachedCard{card: &card, at: time.Now()}
	a.planReads.mu.Unlock()
	return &card, nil
}

// boardTicket is the pinned copy of a card as the Board has it, cleaned like any pin.
func (a *API) boardTicket(ctx context.Context, c *liveBoardCard) (ticket.Ticket, error) {
	m := map[string]any{"source": ticket.SourceBoard, "key": "#" + strconv.Itoa(c.Number), "board_id": c.BoardID,
		"title": c.Title, "status": c.ColumnName, "type": c.Type, "priority": c.Priority, "labels": c.Tags}
	if c.Body != nil {
		m["description"] = *c.Body
	}
	raw, _ := json.Marshal(m)
	t, err := ticket.Clean(raw)
	if err != nil {
		return t, err
	}
	t.URL = a.boardCardURL(ctx, c.BoardID, c.Number)
	return t, nil
}

// fillFromBoard is sent filled from the Board's copy: the Board's title, description, status, type,
// priority and labels win; what the agent sent stays where the Board has none, and everything the
// Board does not hold (people, fields, comments, acceptance) is the agent's.
func fillFromBoard(sent, board ticket.Ticket) ticket.Ticket {
	out := sent
	pick := func(b, s string) string {
		if b != "" {
			return b
		}
		return s
	}
	out.Title = pick(board.Title, sent.Title)
	out.Description = pick(board.Description, sent.Description)
	out.Status = pick(board.Status, sent.Status)
	out.Type = pick(board.Type, sent.Type)
	out.Priority = pick(board.Priority, sent.Priority)
	if len(board.Labels) > 0 {
		out.Labels = board.Labels
	}
	if board.URL != "" {
		out.URL = board.URL
	}
	return out
}

// enrichBoardPin fills a Blerg Board card's pin from the Board, read with the session's access. A
// failed read keeps what was sent.
func (a *API) enrichBoardPin(ctx context.Context, sid string, t ticket.Ticket) ticket.Ticket {
	board, number, ok := ticket.BoardCard(t)
	if !ok || a.dbPool == nil {
		return t
	}
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil || row == nil {
		return t
	}
	a.invalidatePlanCard(sid, board, number)
	card, err := a.planReadCard(ctx, row, board, number)
	if err != nil {
		return t
	}
	bt, err := a.boardTicket(ctx, card)
	if err != nil {
		return t
	}
	return fillFromBoard(t, bt)
}

// ─── Recording work events ────────────────────────────────────────────────────

// appendWorkEvents records events (kind, payload) in tx in order, commits and fans each out.
func (a *API) appendWorkEvents(ctx context.Context, tx pgx.Tx, sid string, events []workEvent) error {
	type stored struct {
		seq int64
		id  string
		at  time.Time
		ev  workEvent
	}
	var done []stored
	for _, ev := range events {
		at := time.Now()
		id := newUUID()
		raw := protocol.StripNUL(ev.raw)
		seq, _, err := db.AppendAgentEventTx(ctx, tx, sid, id, ev.kind, string(raw), at)
		if err != nil {
			return err
		}
		done = append(done, stored{seq, id, at, workEvent{ev.kind, raw}})
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, s := range done {
		fanOutServerEvent(a.hub, sid, s.id, s.ev.kind, s.ev.raw, s.seq, s.at)
	}
	return nil
}

type workEvent struct {
	kind string
	raw  []byte
}

// planSessionBoard is the session's one board: the only board its permissions name, else the
// board it was started for; "" when it has none or several.
func (a *API) planSessionBoard(ctx context.Context, row *db.SessionRow) string {
	boards, err := a.boardLevelsFor(ctx, row)
	if err == nil {
		ids := map[string]bool{}
		for _, b := range boards {
			if b.BoardID != "" {
				ids[b.BoardID] = true
			}
		}
		if len(ids) == 1 {
			for id := range ids {
				return id
			}
		}
		if len(ids) > 1 {
			return ""
		}
	}
	return derefOrEmpty(row.BoardID)
}

// ─── Moves ────────────────────────────────────────────────────────────────────

// planCardMoved is the session's own successful move of a card (any path the runner carries): read
// the card again with its columns and apply the automatic rules. Best effort: a failure is logged.
func (a *API) planCardMoved(ctx context.Context, sid, boardID string, number int) {
	if a.dbPool == nil || number <= 0 || boardID == "" {
		return
	}
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil || row == nil || row.Kind != "agent" {
		return
	}
	a.invalidatePlanCard(sid, boardID, number)
	card, err := a.planReadCard(ctx, row, boardID, number)
	if err != nil {
		log.Printf("plan: read moved card %s#%d for %s: %v", boardID, number, sid, err)
		return
	}
	switch {
	case cardDone(card):
		if err := a.planCardClosed(ctx, row, card); err != nil {
			log.Printf("plan: closed card %s#%d for %s: %v", boardID, number, sid, err)
		}
	case columnInProgress(card.ColumnName):
		if err := a.planPinCard(ctx, row, card); err != nil {
			log.Printf("plan: pin card %s#%d for %s: %v", boardID, number, sid, err)
		}
	}
}

// pinDone reports whether a pin's ticket is done: a Blerg Board card read now, else its status.
func (a *API) pinDone(ctx context.Context, row *db.SessionRow, p ticket.Summary) bool {
	if p.Source == ticket.SourceBoard && p.BoardID != "" {
		if n, ok := cardNumberOf(p.Key); ok {
			if c, err := a.planReadCard(ctx, row, p.BoardID, n); err == nil {
				return cardDone(c)
			}
		}
		return false
	}
	return statusDone(p.Status)
}

func cardNumberOf(key string) (int, bool) {
	_, n, ok := boardUnpinKey(key)
	return n, ok
}

func pinRef(p ticket.Summary) string {
	return p.Source + ":" + p.Key
}

// planPinCard pins card (or refreshes its pin) in the session, making room under the cap by
// unpinning the oldest pin whose card is done.
func (a *API) planPinCard(ctx context.Context, row *db.SessionRow, card *liveBoardCard) error {
	t, err := a.boardTicket(ctx, card)
	if err != nil {
		return err
	}
	rows, err := db.ListTicketRows(ctx, a.dbPool, row.ID, 1<<62)
	if err != nil {
		return err
	}
	// Which pins are done is read before the lock: a board read never runs under it.
	done := map[string]bool{}
	if pinned := ticket.Pinned(rows); len(pinned) >= ticket.MaxPinned && findPin(pinned, t.Source, t.Key, t.BoardID) == nil {
		for _, p := range pinned {
			if a.pinDone(ctx, row, p) {
				done[pinRef(p)+"@"+p.BoardID] = true
			}
		}
	}
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.LockSessionWork(ctx, tx, "tickets", row.ID); err != nil {
		return err
	}
	if rows, err = db.ListTicketRows(ctx, tx, row.ID, 1<<62); err != nil {
		return err
	}
	if len(rows) >= ticket.MaxEvents-1 {
		return nil
	}
	pinned := ticket.Pinned(rows)
	var events []workEvent
	p := ticket.Payload{Ticket: t, Action: "pin"}
	if old := findPin(pinned, t.Source, t.Key, t.BoardID); old != nil {
		// A move refreshes all Board fields, including description and tags.
		p.Action = "update"
		p.Previous = &ticket.Previous{Status: old.Status, Title: old.Title}
	} else if len(pinned) >= ticket.MaxPinned {
		var drop *ticket.Summary
		for i := len(pinned) - 1; i >= 0; i-- { // oldest first
			if done[pinRef(pinned[i])+"@"+pinned[i].BoardID] {
				drop = &pinned[i]
				break
			}
		}
		if drop == nil {
			key := fmt.Sprintf("cap:%s#%d", card.BoardID, card.Number)
			text := fmt.Sprintf("#%d moved to %s but was not pinned: %d tickets are pinned and none of them is done. Unpin one you are not working on (`blerg-runner unpin %s`), then pin #%d (`blerg-runner pin`).",
				card.Number, quotedName(card.ColumnName), ticket.MaxPinned, "<source>:<key>", card.Number)
			if err := db.AddPlanFact(ctx, tx, row.ID, key, text); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		unpin, _ := json.Marshal(ticket.Payload{Ticket: ticket.Ticket{Source: drop.Source, Key: drop.Key, BoardID: drop.BoardID, Title: drop.Title, Status: drop.Status, URL: drop.URL}, Action: "unpin"})
		events = append(events, workEvent{"ticket", unpin})
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	events = append(events, workEvent{"ticket", raw})
	return a.appendWorkEvents(ctx, tx, row.ID, events)
}

// planCardClosed unpins a card the session moved to a done column and ticks every open checklist
// item naming it, when the card is on the session's one board.
func (a *API) planCardClosed(ctx context.Context, row *db.SessionRow, card *liveBoardCard) error {
	if err := a.planUnpin(ctx, row.ID, card.BoardID, card.Number); err != nil {
		return err
	}
	if a.planSessionBoard(ctx, row) != card.BoardID {
		return nil
	}
	return a.planTickCard(ctx, row.ID, card.Number)
}

// planUnpin unpins board#number from the session when it is pinned.
func (a *API) planUnpin(ctx context.Context, sid, boardID string, number int) error {
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.LockSessionWork(ctx, tx, "tickets", sid); err != nil {
		return err
	}
	rows, err := db.ListTicketRows(ctx, tx, sid, 1<<62)
	if err != nil {
		return err
	}
	target, _ := unpinTarget(ticket.Pinned(rows), ticket.SourceBoard, boardID+"#"+strconv.Itoa(number))
	if target == nil {
		return nil
	}
	raw, _ := json.Marshal(ticket.Payload{Ticket: ticket.Ticket{Source: target.Source, Key: target.Key, BoardID: target.BoardID, Title: target.Title, Status: target.Status, URL: target.URL}, Action: "unpin"})
	return a.appendWorkEvents(ctx, tx, sid, []workEvent{{"ticket", raw}})
}

// planTickCard ticks every open checklist item whose text names #number, noting why.
func (a *API) planTickCard(ctx context.Context, sid string, number int) error {
	done := map[int]bool{number: true}
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil || row == nil {
		return err
	}
	ws, err := readWorkState(ctx, a.dbPool, sid, 1<<62)
	if err != nil {
		return err
	}
	if ws.Checklist != nil {
		for _, it := range ws.Checklist.Items {
			refs := cardRefs(it.Text)
			if it.Done || !slices.Contains(refs, number) {
				continue
			}
			for _, n := range refs {
				if _, seen := done[n]; seen {
					continue
				}
				c, err := a.planReadCard(ctx, row, a.planSessionBoard(ctx, row), n)
				done[n] = err == nil && cardDone(c)
			}
		}
	}
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.LockSessionWork(ctx, tx, "checklist", sid); err != nil {
		return err
	}
	n, err := db.CountRecentChecklist(ctx, tx, sid)
	if err != nil {
		return err
	}
	latest, err := db.LatestChecklist(ctx, tx, sid, 1<<62)
	if err != nil || latest == nil {
		return err
	}
	cur, ok := checklist.ReadWithSeed([]byte(latest.Payload), latest.ClientEventID)
	if !ok {
		return nil
	}
	items := cur.Items
	var events []workEvent
	for _, it := range cur.Items {
		if it.Done || !slices.Contains(cardRefs(it.Text), number) || slices.ContainsFunc(cardRefs(it.Text), func(n int) bool { return !done[n] }) || n+len(events) >= checklist.MaxEventsPerMinute {
			continue
		}
		p, err := checklist.Apply(items, checklist.Request{Op: "tick", ItemID: it.ID})
		if err != nil {
			continue
		}
		p.Note = fmt.Sprintf("ticked: #%d done", number)
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		events = append(events, workEvent{"checklist", raw})
		items = p.Items
	}
	if len(events) == 0 {
		return nil
	}
	return a.appendWorkEvents(ctx, tx, sid, events)
}

// planBoardToolDone reads a successful blerg_card_move's result (the moved card) and applies the
// rules before the tool reply, so turn-end sees them. tool and result are the board tool's name and MCP result.
func (a *API) planBoardToolDone(parent context.Context, sid, boardID, tool string, result json.RawMessage) {
	if (tool != "blerg_card_move" && tool != "blerg_card_archive") || a == nil || a.dbPool == nil {
		return
	}
	var moved struct {
		Number  int    `json:"number"`
		BoardID string `json:"board_id"`
	}
	if boardToolResult(result, &moved) != nil || moved.Number <= 0 {
		return
	}
	if moved.BoardID != "" {
		boardID = moved.BoardID
	}
	func() {
		// Finish these rules before the caller can end its turn.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), planCheckTimeout)
		defer cancel()
		a.planCardMoved(ctx, sid, boardID, moved.Number)
		if a.planMovedHook != nil {
			a.planMovedHook(sid)
		}
	}()
}

// BoardToolCalled is the gateway's report of a launch board connection's tool call (mcpgw
// BoardObserver).
func (a *API) BoardToolCalled(sessionID, boardID, tool string, result json.RawMessage) {
	a.planBoardToolDone(context.Background(), sessionID, boardID, tool, result)
}

// ─── Starts ───────────────────────────────────────────────────────────────────

var boardStartTitle = regexp.MustCompile(`^blerg-board #(\d{1,7})(?: review| discussion)?: (.+)$`)

// planSessionStarted applies the start rules: a board-started session pins its card; a child naming
// #N is assigned to its parent's open item naming #N.
func (a *API) planSessionStarted(ctx context.Context, sid, parentID, boardID, title, prompt string) {
	if a.dbPool == nil {
		return
	}
	if m := boardStartTitle.FindStringSubmatch(title); boardID != "" && m != nil {
		n, _ := strconv.Atoi(m[1])
		if err := a.planPinStarted(ctx, sid, boardID, n, m[2]); err != nil {
			log.Printf("plan: pin the card of board-started %s: %v", sid, err)
		}
	}
	if parentID != "" {
		if err := a.planAssignChild(ctx, parentID, sid, preferredPlanRefs(title, prompt)); err != nil {
			log.Printf("plan: assign child %s of %s: %v", sid, parentID, err)
		}
	}
}

// planPinStarted pins the card a board-started session was started for, filled from the Board when
// the session can read it, else from what the start said.
func (a *API) planPinStarted(ctx context.Context, sid, boardID string, number int, title string) error {
	raw, _ := json.Marshal(map[string]any{"source": ticket.SourceBoard, "key": "#" + strconv.Itoa(number), "board_id": boardID, "title": title})
	t, err := ticket.Clean(raw)
	if err != nil {
		return err
	}
	t.URL = a.boardCardURL(ctx, boardID, number)
	t = a.enrichBoardPin(ctx, sid, t)
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.LockSessionWork(ctx, tx, "tickets", sid); err != nil {
		return err
	}
	rows, err := db.ListTicketRows(ctx, tx, sid, 1<<62)
	if err != nil {
		return err
	}
	if pinned := ticket.Pinned(rows); findPin(pinned, t.Source, t.Key, t.BoardID) != nil || len(pinned) >= ticket.MaxPinned {
		return nil
	}
	p, _ := json.Marshal(ticket.Payload{Ticket: t, Action: "pin"})
	return a.appendWorkEvents(ctx, tx, sid, []workEvent{{"ticket", p}})
}

// planAssignChild assigns child to the parent's first open checklist item naming one of numbers.
func (a *API) planAssignChild(ctx context.Context, parentID, child string, numbers []int) error {
	if len(numbers) == 0 {
		return nil
	}
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.LockSessionWork(ctx, tx, "checklist", parentID); err != nil {
		return err
	}
	if n, err := db.CountRecentChecklist(ctx, tx, parentID); err != nil || n >= checklist.MaxEventsPerMinute {
		return err
	}
	latest, err := db.LatestChecklist(ctx, tx, parentID, 1<<62)
	if err != nil || latest == nil {
		return err
	}
	cur, ok := checklist.ReadWithSeed([]byte(latest.Payload), latest.ClientEventID)
	if !ok {
		return nil
	}
	matches := 0
	for _, it := range cur.Items {
		if !it.Done && slices.ContainsFunc(cardRefs(it.Text), func(n int) bool { return slices.Contains(numbers, n) }) {
			matches++
		}
	}
	if matches != 1 {
		return nil
	}
	for _, it := range cur.Items {
		if it.Done || !slices.ContainsFunc(cardRefs(it.Text), func(n int) bool { return slices.Contains(numbers, n) }) {
			continue
		}
		worker := checklist.Assignee{Kind: "child_session", ID: child}
		if slices.Contains(it.Assignees, worker) {
			return nil
		}
		p, err := checklist.Apply(cur.Items, checklist.Request{Op: "assign", ItemID: it.ID, Assignee: &worker})
		if err != nil {
			return nil
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		return a.appendWorkEvents(ctx, tx, parentID, []workEvent{{"checklist", raw}})
	}
	return nil
}

// ─── Nudges ───────────────────────────────────────────────────────────────────

// PlanTurnEnded tells the API that sessionID's turn ended with the turn_done at seq. A no-op
// without a registered hook.
func (h *Hub) PlanTurnEnded(sessionID string, seq int64, at time.Time, stopReason string) {
	h.mu.RLock()
	fn := h.planTurn
	h.mu.RUnlock()
	if fn != nil {
		fn(sessionID, seq, at, stopReason)
	}
}

// SetPlanTurnHook registers the API's drift check (NewAPI).
func (h *Hub) SetPlanTurnHook(fn func(sessionID string, seq int64, at time.Time, stopReason string)) {
	h.mu.Lock()
	h.planTurn = fn
	h.mu.Unlock()
}

func (a *API) onPlanTurnEnded(sessionID string, seq int64, at time.Time, stopReason string) {
	if a == nil || a.dbPool == nil || sessionID == "" {
		return
	}
	a.planFlights.mu.Lock()
	if a.planFlights.done == nil {
		a.planFlights.done = map[string]chan struct{}{}
	}
	if len(a.planFlights.done) > 4096 {
		for sid, done := range a.planFlights.done {
			select {
			case <-done:
				delete(a.planFlights.done, sid)
			default:
			}
		}
	}
	finished := make(chan struct{})
	a.planFlights.done[sessionID] = finished
	a.planFlights.mu.Unlock()
	go func() {
		defer close(finished)
		ctx, cancel := context.WithTimeout(context.Background(), planCheckTimeout)
		defer cancel()
		a.checkPlanTurn(ctx, sessionID, seq, at, stopReason)
	}()
}

// planFact is one drift: its key (told once) and the line the nudge says.
type planFact struct {
	key, text string
}

// checkPlanTurn judges the turn the turn_done at seq ended and, when something drifted that no
// nudge told yet, holds each fact for the next input. It never sends an input.
func (a *API) checkPlanTurn(ctx context.Context, sid string, seq int64, at time.Time, stopReason string) {
	if stopReason == "interrupted" || stopReason == "error" {
		return
	}
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil || row == nil || row.Kind != "agent" || parentEnded(row) || row.ArchivedAt != nil || row.DiscardRequestedAt != nil {
		return
	}
	if on, err := db.PlanNudgesOn(ctx, a.dbPool, sid); err != nil || !on {
		return
	}
	clientID := fmt.Sprintf("plan-check-%s-%d", sid, seq)
	if !a.planTurns.first(clientID) {
		return
	}
	facts, err := a.planDrift(ctx, row, seq, at)
	if err != nil {
		a.planTurns.forget(clientID)
		log.Printf("plan drift %s: %v", sid, err)
		return
	}
	if len(facts) == 0 {
		return
	}
	for _, f := range facts {
		if err := db.AddPlanFact(ctx, a.dbPool, sid, f.key, f.text); err != nil {
			return
		}
	}
}

// preparePlanNudge never creates an input. It adds held facts to an existing input only.
func (a *API) preparePlanNudge(dc *DaemonConn, raw []byte) ([]byte, func(bool)) {
	return a.preparePlanNudgeMode(dc, raw, false)
}
func (a *API) preparePlanNudgeMode(dc *DaemonConn, raw []byte, initial bool) ([]byte, func(bool)) {
	var in protocol.AgentUserMessage
	if json.Unmarshal(raw, &in) != nil || in.Type != "agent_user_message" {
		return raw, nil
	}
	in.PlanNudge = "" // The transport field is never accepted from a caller or a saved client frame.
	raw, _ = json.Marshal(in)
	dc.liveMu.Lock()
	capable := dc.planNudges
	dc.liveMu.Unlock()
	if !capable || a.dbPool == nil {
		in.PlanNudge = ""
		out, _ := json.Marshal(in)
		return out, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), planCheckTimeout)
	defer cancel()
	// Retransmission is the same logical delivery. Preserve its marked block even when
	// the session is now running; the daemon journal deduplicates the carrying client id.
	if in.ClientID != "" {
		var nudge string
		err := a.dbPool.QueryRow(ctx, `SELECT nudge FROM session_plan_deliveries WHERE session_id=$1 AND client_id=$2`, in.SessionID, in.ClientID).Scan(&nudge)
		if err == nil {
			in.PlanNudge = nudge
			out, _ := json.Marshal(in)
			return out, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return raw, nil
		}
	}
	a.planFlights.mu.Lock()
	finished := a.planFlights.done[in.SessionID]
	a.planFlights.mu.Unlock()
	if finished != nil {
		select {
		case <-finished:
		case <-ctx.Done():
			return raw, nil
		}
	}
	row, err := db.GetSession(ctx, a.dbPool, in.SessionID)
	if err != nil || row == nil || (!initial && row.Status != "idle" && in.Source != limitResumeSource) {
		return raw, nil
	}
	on, err := db.PlanNudgesOn(ctx, a.dbPool, row.ID)
	if err != nil || !on {
		return raw, nil
	}
	pending, err := db.PendingPlanFacts(ctx, a.dbPool, row.ID)
	if err != nil || len(pending) == 0 {
		return raw, nil
	}
	var seq int64
	var at time.Time
	err = a.dbPool.QueryRow(ctx, `SELECT seq, ts FROM agent_events WHERE session_id=$1 AND kind='turn_done' ORDER BY seq DESC LIMIT 1`, row.ID).Scan(&seq, &at)
	if err != nil {
		return raw, nil
	}
	ws, err := readWorkState(ctx, a.dbPool, row.ID, 1<<62)
	if err != nil {
		return raw, nil
	}
	for _, held := range pending {
		if strings.HasPrefix(held.Key, "pins-done:") {
			for _, p := range ws.Tickets {
				if n, ok := cardNumberOf(p.Key); ok && p.Source == ticket.SourceBoard {
					a.invalidatePlanCard(row.ID, p.BoardID, n)
				}
			}
		} else if strings.HasPrefix(held.Key, "closed:") || strings.HasPrefix(held.Key, "cap:") {
			_, ref, _ := strings.Cut(held.Key, ":")
			board, num, ok := strings.Cut(ref, "#")
			n, _ := strconv.Atoi(num)
			if ok {
				a.invalidatePlanCard(row.ID, board, n)
			}
		}
	}
	current, err := a.planDriftAt(ctx, row, seq, at, true)
	if err != nil {
		return raw, nil
	}
	valid := map[string]planFact{}
	for _, f := range current {
		valid[f.key] = f
	}
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return raw, nil
	}
	var claimed []planFact
	for _, held := range pending {
		f, ok := valid[held.Key]
		if !ok {
			_, _ = tx.Exec(ctx, `DELETE FROM session_plan_nudges WHERE session_id=$1 AND key=$2 AND nudged_at IS NULL`, row.ID, held.Key)
			continue
		}
		var key string
		err := tx.QueryRow(ctx, `UPDATE session_plan_nudges SET nudged_at=now() WHERE session_id=$1 AND key=$2 AND nudged_at IS NULL RETURNING key`, row.ID, f.key).Scan(&key)
		if err == nil {
			claimed = append(claimed, f)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(context.Background())
			return raw, nil
		}
	}
	if len(claimed) > 0 {
		in.PlanNudge = planNudgeText(claimed)
		if in.ClientID != "" {
			// A second dispatch of the same input may have won while this one rechecked facts.
			var saved string
			err := tx.QueryRow(ctx, `INSERT INTO session_plan_deliveries(session_id,client_id,nudge) VALUES($1,$2,$3) ON CONFLICT(session_id,client_id) DO UPDATE SET nudge=session_plan_deliveries.nudge RETURNING nudge`, in.SessionID, in.ClientID, in.PlanNudge).Scan(&saved)
			if err != nil {
				_ = tx.Rollback(context.Background())
				return raw, nil
			}
			in.PlanNudge = saved
		}
	}
	if in.ClientID != "" && len(claimed) == 0 {
		var saved string
		err := tx.QueryRow(ctx, `SELECT nudge FROM session_plan_deliveries WHERE session_id=$1 AND client_id=$2`, in.SessionID, in.ClientID).Scan(&saved)
		if err == nil {
			in.PlanNudge = saved
		} else if !errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(context.Background())
			return raw, nil
		}
	}
	out, err := json.Marshal(in)
	if err != nil {
		_ = tx.Rollback(context.Background())
		return raw, nil
	}
	return out, func(sent bool) {
		endCtx, endCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer endCancel()
		if sent {
			if err := tx.Commit(endCtx); err != nil {
				log.Printf("plan claim commit: %v", err)
			}
		} else {
			_ = tx.Rollback(endCtx)
		}
	}
}

func (a *API) attachPlanNudge(dc *DaemonConn, raw []byte) []byte {
	out, finish := a.preparePlanNudge(dc, raw)
	if finish != nil {
		finish(true)
	}
	return out
}

// planNudgeText is the nudge: what drifted, one line each, with the commands that fix it.
func planNudgeText(facts []planFact) string {
	var b strings.Builder
	b.WriteString("Your plan drifted from what you are doing:\n")
	for _, f := range facts {
		b.WriteString("- ")
		b.WriteString(f.text)
		b.WriteString("\n")
	}
	b.WriteString("Fix what applies and carry on; nothing else needs saying. (Pins and checklist items for cards you move with blerg-runner ticket move are kept for you.)")
	return b.String()
}

// planWorkTool reports whether a tool call is work rather than keeping the plan: anything except a
// shell command that runs blerg-runner.
func planWorkTool(e db.TurnEvent) bool {
	if e.Kind != "tool_call" {
		return false
	}
	cmd := strings.TrimSpace(e.Command)
	return cmd == "" || (!strings.HasPrefix(cmd, "blerg-runner ") && cmd != "blerg-runner")
}

// planDrift is the turn's drift, minus what a nudge already told.
func (a *API) planDrift(ctx context.Context, row *db.SessionRow, seq int64, at time.Time) ([]planFact, error) {
	return a.planDriftAt(ctx, row, seq, at, false)
}

func (a *API) planDriftAt(ctx context.Context, row *db.SessionRow, seq int64, at time.Time, delivery bool) ([]planFact, error) {
	events, err := db.TurnEvents(ctx, a.dbPool, row.ID, seq)
	if err != nil {
		return nil, err
	}
	work := 0
	boardTouched := false
	for _, e := range events {
		if e.Kind == "tool_call" && (strings.Contains(e.Command, "ticket move") || strings.Contains(e.Command, "ticket archive") || strings.Contains(e.Tool, "blerg_card_move") || strings.Contains(e.Tool, "blerg_card_archive")) {
			boardTouched = true
		}
		if planWorkTool(e) {
			work++
		}
	}
	ws, err := readWorkState(ctx, a.dbPool, row.ID, 1<<62)
	if err != nil {
		return nil, err
	}
	var items []checklist.Item
	if ws.Checklist != nil {
		items = ws.Checklist.Items
	}
	if row.CronID != nil && len(ws.Tickets) == 0 && len(items) == 0 {
		return nil, nil // a cron that keeps no plan is never nudged
	}
	var facts []planFact

	// Pins: read each Blerg Board card now; a card closed on the board is news (the session's own
	// close unpins it, so a pinned closed card was closed by someone else).
	if len(ws.Tickets) > 0 && (work > 0 || boardTouched) {
		if boardTouched && boardStartTitle.MatchString(derefOrEmpty(row.Title)) {
			for _, p := range ws.Tickets {
				if n, ok := cardNumberOf(p.Key); ok && p.Source == ticket.SourceBoard {
					a.invalidatePlanCard(row.ID, p.BoardID, n)
				}
			}
		}
		allDone := true
		var refs []string
		for _, p := range ws.Tickets {
			done := statusDone(p.Status)
			if p.Source == ticket.SourceBoard {
				done = false
			}
			if p.Source == ticket.SourceBoard && p.BoardID != "" {
				if n, ok := cardNumberOf(p.Key); ok {
					key := fmt.Sprintf("closed:%s#%d", p.BoardID, n)
					told, err := db.NudgedPlanKeys(ctx, a.dbPool, row.ID, []string{key})
					if err != nil {
						return nil, err
					}
					if told[key] {
						refs = append(refs, pinRef(p))
						continue
					}
					if c, err := a.planReadCard(ctx, row, p.BoardID, n); err == nil {
						done = cardDone(c)
						if done && boardStartTitle.MatchString(derefOrEmpty(row.Title)) {
							if err := a.planCardClosed(ctx, row, c); err != nil {
								return nil, err
							}
							allDone = false
							continue
						}
						if done {
							facts = append(facts, planFact{key: fmt.Sprintf("closed:%s#%d", p.BoardID, n), text: fmt.Sprintf(
								"#%d %s is closed on the board (%s). Unpin it: `blerg-runner unpin %s`%s.",
								n, quotedName(c.Title), quotedName(c.ColumnName), pinRef(p), tickHint(items, n))})
						}
					}
				}
			}
			if !done {
				allDone = false
			}
			refs = append(refs, pinRef(p))
		}
		if allDone && work > 0 {
			sort.Strings(refs)
			var cmds []string
			for _, r := range refs {
				cmds = append(cmds, "`blerg-runner unpin "+r+"`")
			}
			facts = append(facts, planFact{key: "pins-done:" + strings.Join(refs, ","), text: fmt.Sprintf(
				"Every pinned ticket is done (%s), but your last turn ran tools. Unpin them (%s) and pin what you are working on now (`blerg-runner pin`).",
				strings.Join(refs, ", "), strings.Join(cmds, ", "))})
		}
	}

	// The checklist while the turn worked.
	if len(items) > 0 && work > 0 {
		doing, open := 0, -1
		for i, it := range items {
			if it.InProgress && !it.Done {
				doing++
			}
			if !it.Done && open < 0 {
				open = i
			}
		}
		switch {
		case open < 0:
			facts = append(facts, planFact{key: "checklist-done:" + planChecklistKey(items), text: "Every checklist item is ticked, but your last turn ran tools. Add what you are doing now (`blerg-runner checklist add \"<item>\"`, then `blerg-runner checklist doing <n>`), or clear the list if the work is over (`blerg-runner checklist clear`)."})
		case doing == 0:
			facts = append(facts, planFact{key: "checklist-idle:" + planChecklistKey(items), text: fmt.Sprintf(
				"No checklist item is doing, but your last turn ran tools. Mark the one you are on: `blerg-runner checklist doing <n>` (the first open one is %d, %s).",
				open+1, quotedName(items[open].Text))})
		}
	}

	// Workers started this turn with no item.
	assigned := map[string]bool{}
	for _, it := range items {
		for _, w := range it.Assignees {
			assigned[w.Kind+":"+w.ID] = true
		}
	}
	addHint := ""
	if len(items) == 0 {
		addHint = " (first `blerg-runner checklist add \"<item>\"`)"
	}
	start, err := db.TurnStart(ctx, a.dbPool, row.ID, seq)
	if err != nil {
		return nil, err
	}
	children, err := db.PlanChildren(ctx, a.dbPool, row.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range children {
		if c.StartedAt.After(start) && !c.StartedAt.After(at) && !assigned["child_session:"+c.ID] {
			facts = append(facts, planFact{key: "worker:" + c.ID, text: fmt.Sprintf(
				"Child %s (%s), started in your last turn, has no checklist item. Give it one%s: `blerg-runner checklist assign <n> --child-session %s`.",
				quotedName(childLabel(c)), c.ID, addHint, c.ID)})
		}
	}
	seen := map[string]bool{}
	for _, e := range events {
		if (e.Kind != "subagent_started" && e.Kind != "subagent_done") || !e.Background || !validPlanWorkerID(e.ChildID) || seen[e.ChildID] {
			continue
		}
		seen[e.ChildID] = true
		if !assigned["subagent:"+e.ChildID] {
			facts = append(facts, planFact{key: "worker:" + e.ChildID, text: fmt.Sprintf(
				"Background subagent %s, started in your last turn, has no checklist item. Give it one%s: `blerg-runner checklist doing <n> --agent %s` (`blerg-runner checklist workers` lists the ids).",
				e.ChildID, addHint, e.ChildID)})
		}
	}

	// Children that look finished: stopped or idle with no open question or review, and either
	// their result read with nothing since, or their wip branch with work beyond the recorded
	// clone base merged into this session's branch (a few rotated reads per turn, only
	// for those not yet told).
	finishedKeys := make([]string, 0, len(children))
	for _, c := range children {
		finishedKeys = append(finishedKeys, "child-finished:"+c.ID)
	}
	toldFinished, err := db.NudgedPlanKeys(ctx, a.dbPool, row.ID, finishedKeys)
	if err != nil {
		return nil, err
	}
	asked := 0
	if len(children) > 1 {
		offset := a.planMergeScan.offset(row.ID, seq, len(children))
		children = append(children[offset:], children[:offset]...)
	}
	for _, c := range children {
		state := ""
		if c.Blocked {
			continue
		}
		switch {
		case c.Status == "idle":
			state = "idle"
		case terminalSessionStatus(c.Status) || c.Status == "stopped" || c.Status == "ended":
			state = "stopped"
		default:
			continue
		}
		key := "child-finished:" + c.ID
		if toldFinished[key] {
			continue
		}
		why := ""
		if c.ReadSeq >= 0 && c.LastSeq <= c.ReadSeq {
			why = "you read its result and it has done nothing since"
		} else if asked < planMergedChecksPerTurn {
			asked++
			if merged, _ := a.planChildMergedCheck(ctx, row, c.ID); merged {
				why = "its branch wip/" + c.ID + " is merged"
			}
		}
		if why == "" {
			continue
		}
		facts = append(facts, planFact{key: key, text: fmt.Sprintf(
			"Child %s looks finished (%s; %s). Discard it: `blerg-runner session discard %s`.",
			quotedName(childLabel(c)), state, why, c.ID)})
	}

	// Facts found between turns (a card the cap kept from being pinned).
	pending, err := db.PendingPlanFacts(ctx, a.dbPool, row.ID)
	if err != nil {
		return nil, err
	}
	for _, f := range pending {
		if delivery && strings.HasPrefix(f.Key, "cap:") {
			board, number, ok := strings.Cut(strings.TrimPrefix(f.Key, "cap:"), "#")
			n, _ := strconv.Atoi(number)
			if !ok || findPin(ws.Tickets, ticket.SourceBoard, "#"+number, board) != nil {
				continue
			}
			c, err := a.planReadCard(ctx, row, board, n)
			if err == nil && columnInProgress(c.ColumnName) && !cardDone(c) {
				facts = append(facts, planFact{key: f.Key, text: f.Text})
			}
		}
	}

	keys := make([]string, 0, len(facts))
	for _, f := range facts {
		keys = append(keys, f.key)
	}
	told, err := db.NudgedPlanKeys(ctx, a.dbPool, row.ID, keys)
	if err != nil {
		return nil, err
	}
	out := facts[:0]
	have := map[string]bool{}
	for _, f := range facts {
		if !told[f.key] && !have[f.key] {
			have[f.key] = true
			out = append(out, f)
		}
	}
	return out, nil
}

// planMergedChecksPerTurn bounds the daemon reads one turn's check makes for merged children.
const planMergedChecksPerTurn = 5

// planChildMergedCheck asks the session's daemon whether child's wip branch is merged into the
// session's branch, using the clone's recorded base. false when it cannot tell
// (no daemon, an older daemon, no such branch).
func (a *API) planChildMergedCheck(ctx context.Context, row *db.SessionRow, child string) (bool, time.Time) {
	if a.planChildMerged != nil {
		return a.planChildMerged(ctx, row, child)
	}
	if !protocol.ValidChildBranchID(child) || a.hub == nil {
		return false, time.Time{}
	}
	dc := a.hub.FindDaemonForSession(row.ID)
	if dc == nil {
		return false, time.Time{}
	}
	requests := []protocol.ReadCommit{{SessionID: row.ID, MergedChild: child}}
	if a.dbPool != nil {
		parentWS, err := db.GetSessionWorkspace(ctx, a.dbPool, row.ID)
		if err != nil {
			return false, time.Time{}
		}
		if parentWS != nil && parentWS.Manifest != nil {
			childWS, err := db.GetSessionWorkspace(ctx, a.dbPool, child)
			if err != nil || childWS == nil || childWS.Manifest == nil {
				return false, time.Time{}
			}
			_, binding, err := db.GetWorkspaceRuntime(ctx, a.dbPool, row.ID)
			if err != nil || binding == nil || protocol.ValidateWorkspaceChild(*parentWS.Manifest, *childWS.Manifest) != nil {
				return false, time.Time{}
			}
			requests = nil
			for _, member := range childWS.Manifest.Members {
				base := ""
				for _, result := range childWS.Results {
					if result.MemberID == member.ID && result.LaunchGeneration == childWS.Manifest.LaunchGeneration {
						base = result.Base
					}
				}
				if !protocol.ValidSessionOriginCommit(base) {
					return false, time.Time{}
				}
				requests = append(requests, protocol.ReadCommit{SessionID: row.ID, MergedChild: child, ChildLaunchGeneration: childWS.Manifest.LaunchGeneration, ChildBase: base, WorkspaceGitRef: protocol.WorkspaceGitRef{WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, MemberID: member.ID}})
			}
			if len(requests) == 0 {
				return false, time.Time{}
			}
		} else if parentWS != nil && parentWS.Kind == "folder" {
			return false, time.Time{}
		}
	}
	for _, request := range requests {
		res, err := a.hub.requestCommitRead(ctx, dc, derefOrEmpty(row.SpawningAccountID), request, commitLookupTimeout)
		if err != nil || !res.OK || !res.Merged || !a.hub.currentWorkspaceGit(ctx, row.ID, res.WorkspaceGitRef) {
			return false, time.Time{}
		}
	}
	return true, time.Time{}
}

func childLabel(c db.PlanChild) string {
	if t := strings.TrimSpace(c.Title); t != "" {
		return t
	}
	return c.ID
}

// tickHint names the open checklist items about card n, when there are some.
func tickHint(items []checklist.Item, n int) string {
	var nums []string
	for i, it := range items {
		if !it.Done && len(cardRefs(it.Text)) == 1 && slices.Contains(cardRefs(it.Text), n) {
			nums = append(nums, strconv.Itoa(i+1))
		}
	}
	if len(nums) == 0 {
		return ""
	}
	cmds := make([]string, len(nums))
	for i, s := range nums {
		cmds[i] = "`blerg-runner checklist tick " + s + "`"
	}
	return ", and tick its item (" + strings.Join(cmds, ", ") + ")"
}

// ─── The setting ──────────────────────────────────────────────────────────────

// HandlePlanNudges is GET and PUT /api/sessions/{id}/plan-nudges: whether Blerg tells the session's
// agent when its plan drifts. The session's owner only (an administrator does not steer someone
// else's agent); a private session answers others as an unknown one.
func (a *API) HandlePlanNudges(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, noDatabaseMsg)
		return
	}
	id := r.PathValue("id")
	if !validSessionID(id) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	row, err := db.GetSession(r.Context(), a.dbPool, id)
	if err != nil || row == nil || !ownsSession(p.Sub, row) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if row.Kind != "agent" {
		writeError(w, http.StatusConflict, "only an agent session keeps a plan")
		return
	}
	if r.Method == http.MethodPut {
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil || body.Enabled == nil {
			writeError(w, http.StatusBadRequest, "enabled required")
			return
		}
		if err := db.SetPlanNudges(r.Context(), a.dbPool, id, *body.Enabled); err != nil {
			writeDatabaseFailure(w, "save the setting", "plan nudges "+id, err)
			return
		}
	}
	on, err := db.PlanNudgesOn(r.Context(), a.dbPool, id)
	if err != nil {
		writeDatabaseFailure(w, "read the setting", "plan nudges "+id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": on})
}

func planChecklistKey(items []checklist.Item) string {
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "%s:%t:%t;", it.ID, it.Done, it.InProgress)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}
func validPlanWorkerID(id string) bool {
	return regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`).MatchString(id)
}
func preferredPlanRefs(title, prompt string) []int {
	refs := cardRefs(title)
	if len(refs) == 0 {
		refs = cardRefs(prompt)
	}
	if len(refs) != 1 {
		return nil
	}
	return refs
}

type planCachedCard struct {
	card *liveBoardCard
	at   time.Time
}
type planReadAccount struct {
	since         time.Time
	count, active int
}
type planReadCache struct {
	mu       sync.Mutex
	cards    map[string]planCachedCard
	accounts map[string]*planReadAccount
}

func (a *API) invalidatePlanCard(_ string, board string, n int) {
	a.planReads.mu.Lock()
	for key := range a.planReads.cards {
		if strings.HasSuffix(key, ":"+board+":"+strconv.Itoa(n)) {
			delete(a.planReads.cards, key)
		}
	}
	a.planReads.mu.Unlock()
}

type planFlightState struct {
	mu   sync.Mutex
	done map[string]chan struct{}
}

// Keep the window stable at delivery, but advance it independently of event counts each turn.
type planMergeWindow struct {
	seq    int64
	offset int
}
type planMergeScan struct {
	mu      sync.Mutex
	windows map[string]planMergeWindow
}

func (s *planMergeScan) offset(sid string, seq int64, n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.windows == nil {
		s.windows = map[string]planMergeWindow{}
	}
	w, ok := s.windows[sid]
	if !ok {
		w = planMergeWindow{seq: seq}
	} else if w.seq != seq {
		w.seq = seq
		w.offset = (w.offset + planMergedChecksPerTurn) % n
	}
	s.windows[sid] = w
	return w.offset % n
}
