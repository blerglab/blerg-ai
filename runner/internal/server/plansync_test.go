package server

// The session's plan keeps itself honest (card #158): the automatic pin, unpin and tick on the
// session's own card moves, the Board-filled pinned copy, the start rules, and the drift nudges.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/checklist"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// planBoard is the Board behind the session's board tools: cards in columns, moved and read.
type planBoard struct {
	mu    sync.Mutex
	cards map[int]*planCard
	reads int
}

type planCard struct {
	title, body, column string
	archived            bool
}

var planColumns = []struct {
	ID, Name string
	Terminal bool
}{{"col-ready", "Ready", false}, {"col-prog", "In progress", false}, {"col-review", "Review", false}, {"col-done", "Done", true}}

func newPlanBoard() *planBoard {
	return &planBoard{cards: map[int]*planCard{
		12: {title: "Parser", body: "Parse the config file.", column: "Ready"},
		13: {title: "Lexer", body: "Tokens.", column: "Ready"},
		14: {title: "Docs", body: "Write them.", column: "Ready"},
	}}
}

func (*planBoard) BoardConfigured() bool                 { return true }
func (*planBoard) BoardMCPURL() string                   { return "http://board.example.test/mcp" }
func (*planBoard) RevokeSession(context.Context, string) {}

func (b *planBoard) set(n int, column string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cards[n].column = column
}

func (b *planBoard) cardJSON(n int, trail bool) string {
	c := b.cards[n]
	colID := ""
	for _, col := range planColumns {
		if col.Name == c.column {
			colID = col.ID
		}
	}
	out := map[string]any{"id": "card-" + strconv.Itoa(n), "board_id": "board-a", "number": n, "title": c.title, "body": c.body,
		"type": "feature", "priority": "high", "tags": []string{"parser"}, "column_id": colID, "version": 1}
	if c.archived {
		out["archived_at"] = "2026-10-22T00:00:00Z"
	}
	if trail {
		out["column_name"] = c.column
		var cols []map[string]any
		for _, col := range planColumns {
			cols = append(cols, map[string]any{"id": col.ID, "name": col.Name, "is_terminal": col.Terminal})
		}
		out["columns"] = cols
		out["trail"] = []any{}
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

func (b *planBoard) CallBoard(_ context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var args struct {
		Number     int    `json:"number"`
		ColumnName string `json:"column_name"`
		WithTrail  bool   `json:"with_trail"`
	}
	_ = json.Unmarshal(c.Arguments, &args)
	text := func(s string, isErr bool) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"isError": isErr, "content": []any{map[string]any{"type": "text", "text": s}}})
		return raw
	}
	card := b.cards[args.Number]
	if card == nil {
		return text("card not found", true), nil
	}
	switch c.Tool {
	case "blerg_card_move":
		card.column = args.ColumnName
		return text(b.cardJSON(args.Number, false), false), nil
	case "blerg_card_get":
		b.reads++
		return text(b.cardJSON(args.Number, args.WithTrail), false), nil
	}
	return text("ok:"+c.Tool, false), nil
}

// newPlanFx is a session with full access to board-a (the board above) and a channel that hears
// each move's or start's rules applied.
type planFx struct {
	*topicFx
	board *planBoard
	moved chan string
}

func newPlanFx(t *testing.T, level string) *planFx {
	t.Helper()
	fx, _, _ := newBoardLevelFx(t)
	pb := newPlanBoard()
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: pb})
	pf := &planFx{topicFx: fx, board: pb, moved: make(chan string, 16)}
	fx.api.planMovedHook = func(sid string) { pf.moved <- sid }
	fx.mux.HandleFunc("GET /api/sessions/{id}/plan-nudges", fx.api.HandlePlanNudges)
	fx.mux.HandleFunc("PUT /api/sessions/{id}/plan-nudges", fx.api.HandlePlanNudges)
	if level != "" {
		if code, body := setLevel(fx, fx.token(topicOwner, "human"), "board-a", level); code != 200 {
			t.Fatalf("set level: %d %s", code, body)
		}
	}
	return pf
}

func (fx *planFx) move(n int, column string) {
	fx.t.Helper()
	if code, body := boardTool(fx.topicFx, fx.sessTok, "board-a", "blerg_card_move", map[string]any{"number": n, "column_name": column}); code != 200 {
		fx.t.Fatalf("move #%d: %d %s", n, code, body)
	}
	select {
	case <-fx.moved:
	case <-time.After(10 * time.Second):
		fx.t.Fatal("the move's rules never ran")
	}
}

func (fx *planFx) work() workState {
	fx.t.Helper()
	ws, err := readWorkState(context.Background(), fx.pool, fx.sess, 1<<62)
	if err != nil {
		fx.t.Fatal(err)
	}
	return ws
}

func (fx *planFx) pins() []string {
	var out []string
	for _, p := range fx.work().Tickets {
		out = append(out, p.Source+":"+p.BoardID+p.Key)
	}
	return out
}

func (fx *planFx) checklistSet(items ...string) {
	fx.t.Helper()
	if code, out := fx.postWork("checklist", fx.sess, fx.sessTok, map[string]any{"op": "set", "items": items}); code != 201 {
		fx.t.Fatalf("checklist set: %d %v", code, out)
	}
}

func (fx *planFx) checklistOp(body map[string]any) {
	fx.t.Helper()
	if code, out := fx.postWork("checklist", fx.sess, fx.sessTok, body); code != 201 {
		fx.t.Fatalf("checklist %v: %d %v", body, code, out)
	}
}

// event appends one agent event of the session and returns its seq.
func (fx *planFx) event(kind, payload string) int64 {
	fx.t.Helper()
	seq, _, err := db.AppendAgentEventAt(context.Background(), fx.pool, fx.sess, newUUID(), kind, payload, time.Now())
	if err != nil {
		fx.t.Fatal(err)
	}
	return seq
}

// turn records a turn: a person's message, the tool calls (shell commands) and its end; it returns
// what the check at its end sent ("" for nothing).
func (fx *planFx) turn(source string, commands ...string) string {
	fx.t.Helper()
	fx.event("user_message", fmt.Sprintf(`{"text":"go","source":%q}`, source))
	for i, c := range commands {
		fx.event("tool_call", fmt.Sprintf(`{"tool":"Bash","call_id":"c%d","input":{"command":%q}}`, i, c))
	}
	seq := fx.event("turn_done", `{"stop_reason":"end_turn"}`)
	fx.api.checkPlanTurn(context.Background(), fx.sess, seq, time.Now(), "end_turn")
	return planCarryingInput(fx.t, fx.api, fx.sess, true)
}

func TestCardRefsAreWholeTokens(t *testing.T) {
	for text, want := range map[string][]int{
		"Ship #12 and #13.":         {12, 13},
		"(#7), #7 again; [#8]":      {7, 8},
		"&#147 a#1 /#2 ##3 #4a #5-": nil,
		"#12345678 too long":        nil,
		"#1234567 ok":               {1234567},
		"#0 none":                   nil,
	} {
		if got := cardRefs(text); !slices.Equal(got, want) {
			t.Errorf("cardRefs(%q) = %v, want %v", text, got, want)
		}
	}
}

// A card the session moves into an in-progress column is pinned, its copy filled from the Board.
func TestMoveToInProgressPinsTheCardFromTheBoard(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	fx.move(12, "In progress")
	if got := fx.pins(); !slices.Equal(got, []string{"blerg-board:board-a#12"}) {
		t.Fatalf("pins = %v", got)
	}
	cur := fx.work().Ticket
	if cur == nil || cur.Title != "Parser" || cur.Description != "Parse the config file." || cur.Status != "In progress" ||
		cur.Type != "feature" || cur.Priority != "high" || !slices.Equal(cur.Labels, []string{"parser"}) {
		t.Fatalf("pinned copy = %+v", cur)
	}
	// A move elsewhere (Review) pins nothing more; a second card's move to in progress does.
	fx.move(13, "Review")
	fx.move(14, "In progress")
	if got := fx.pins(); !slices.Equal(got, []string{"blerg-board:board-a#14", "blerg-board:board-a#12"}) {
		t.Fatalf("pins after more moves = %v", got)
	}
}

// A card moved to a done column is unpinned and every open item naming it is ticked, with a note.
func TestMoveToDoneUnpinsAndTicksItsItems(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	fx.move(12, "In progress")
	fx.checklistSet("Build the #12 parser", "Docs for #120", "Review a#12 later", "Tests for #12")
	fx.move(12, "Done")
	if got := fx.pins(); len(got) != 0 {
		t.Fatalf("pins after done = %v", got)
	}
	items := fx.work().Checklist.Items
	done := []bool{items[0].Done, items[1].Done, items[2].Done, items[3].Done}
	if !slices.Equal(done, []bool{true, false, false, true}) {
		t.Fatalf("ticked = %v", done)
	}
	events := fx.kindPayloads(fx.sess, "checklist")
	last := events[len(events)-1]
	if last["note"] != "ticked: #12 done" || last["op"] != "tick" {
		t.Fatalf("last checklist event = %v", last)
	}
}

// Five pins: the oldest done one makes room; with none done the card is not pinned and the next
// nudge says so.
func TestAutoPinRespectsTheCap(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	pin := func(key, status string) {
		t.Helper()
		if code, out := fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "pin",
			"ticket": map[string]any{"source": "jira", "key": key, "title": key, "status": status}}); code != 201 {
			t.Fatalf("pin %s: %d %v", key, code, out)
		}
	}
	pin("A-1", "Open")
	pin("A-2", "Done")
	pin("A-3", "Closed")
	pin("A-4", "In Progress")
	pin("A-5", "Open")
	fx.move(12, "In progress")
	got := fx.pins()
	if len(got) != 5 || got[0] != "blerg-board:board-a#12" || slices.Contains(got, "jira:A-2") || !slices.Contains(got, "jira:A-3") {
		t.Fatalf("pins = %v, want #12 in and the oldest done (A-2) out", got)
	}
	// Unpin the other done one: five open pins now, so #13 cannot be pinned.
	if code, out := fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "unpin", "source": "jira", "key": "A-3"}); code != 201 {
		t.Fatalf("unpin: %d %v", code, out)
	}
	pin("A-6", "Open")
	fx.move(13, "In progress")
	if got := fx.pins(); slices.Contains(got, "blerg-board:board-a#13") || len(got) != 5 {
		t.Fatalf("pins = %v, want #13 left out", got)
	}
	nudge := fx.turn("human")
	if !strings.Contains(nudge, "#13 moved to \"In progress\" but was not pinned") {
		t.Fatalf("nudge = %q", nudge)
	}
}

// A bare pin of a Board card (key and title only) carries the Board's description; a failed read
// keeps what was sent.
func TestBarePinIsFilledFromTheBoard(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelRead)
	if code, out := fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "pin",
		"ticket": map[string]any{"source": "blerg-board", "board_id": "board-a", "key": "#12", "title": "my words", "assignee": "sam"}}); code != 201 {
		t.Fatalf("pin: %d %v", code, out)
	}
	cur := fx.work().Ticket
	if cur.Description != "Parse the config file." || cur.Title != "Parser" || cur.Status != "Ready" || cur.Assignee != "sam" {
		t.Fatalf("pinned copy = %+v", cur)
	}
	// Re-pinning refreshes the copy from the Board.
	fx.board.set(12, "Review")
	fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "pin",
		"ticket": map[string]any{"source": "blerg-board", "board_id": "board-a", "key": "#12", "title": "my words"}})
	if cur := fx.work().Ticket; cur.Status != "Review" {
		t.Fatalf("refreshed copy = %+v", cur)
	}
	// No read access: what was sent stays.
	setLevel(fx.topicFx, fx.token(topicOwner, "human"), "board-a", db.BoardLevelPropose)
	fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "pin",
		"ticket": map[string]any{"source": "blerg-board", "board_id": "board-a", "key": "#13", "title": "sent title", "description": "sent body"}})
	if cur := fx.work().Ticket; cur.Title != "sent title" || cur.Description != "sent body" {
		t.Fatalf("unreadable card copy = %+v", cur)
	}
}

// A board-started session pins its card; a child naming #N is assigned to the parent's item.
func TestStartRulesPinAndAssign(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	fx.api.planSessionStarted(context.Background(), fx.sess, "", "board-a", "blerg-board #12: Parser", "")
	if got := fx.pins(); !slices.Equal(got, []string{"blerg-board:board-a#12"}) || fx.work().Ticket.Description != "Parse the config file." {
		t.Fatalf("board-started pins = %v %+v", got, fx.work().Ticket)
	}
	fx.checklistSet("Parser #12", "Lexer #13")
	child := fx.ended // any other session row stands in for the child
	fx.api.planSessionStarted(context.Background(), child, fx.sess, "", "Build the lexer", "Card #13 needs tokens")
	items := fx.work().Checklist.Items
	if len(items[0].Assignees) != 0 || !slices.Equal(items[1].Assignees, []checklist.Assignee{{Kind: "child_session", ID: child}}) {
		t.Fatalf("assignments = %+v", items)
	}
}

// A nudge: once per turn, at the turn's end, only when something drifted, never when off.
func TestPlanNudgeChecklistIdle(t *testing.T) {
	fx := newPlanFx(t, "")
	fx.checklistSet("Write the parser", "Write tests")
	// Consistent: an item is doing.
	fx.checklistOp(map[string]any{"op": "doing", "index": 1})
	if n := fx.turn("human", "go test ./..."); n != "" {
		t.Fatalf("consistent plan nudged: %q", n)
	}
	fx.checklistOp(map[string]any{"op": "tick", "index": 1})
	// Only plan keeping (blerg-runner commands) is not work.
	if n := fx.turn("human", "blerg-runner checklist tick 1"); n != "" {
		t.Fatalf("plan keeping nudged: %q", n)
	}
	n := fx.turn("human", "go build ./...")
	if !strings.Contains(n, "No checklist item is doing") || !strings.Contains(n, "`blerg-runner checklist doing <n>` (the first open one is 2, \"Write tests\")") {
		t.Fatalf("nudge = %q", n)
	}
	// The same fact is told once; the same turn is judged once.
	if again := fx.turn("human", "go build ./..."); again != "" {
		t.Fatalf("told twice: %q", again)
	}
	// All ticked while working.
	fx.checklistOp(map[string]any{"op": "tick", "index": 2})
	if n := fx.turn("human", "make lint"); !strings.Contains(n, "Every checklist item is ticked") {
		t.Fatalf("all-ticked nudge = %q", n)
	}
	// A pending fact made stale before delivery is dropped.
	fx.checklistOp(map[string]any{"op": "add", "item": "More"})
	fx.event("tool_call", `{"tool":"Bash","input":{"command":"go vet ./..."}}`)
	staleSeq := fx.event("turn_done", `{}`)
	fx.api.checkPlanTurn(context.Background(), fx.sess, staleSeq, time.Now(), "end_turn")
	fx.checklistOp(map[string]any{"op": "doing", "index": 3})
	if n := planCarryingInput(t, fx.api, fx.sess, true); n != "" {
		t.Fatalf("stale fact delivered: %q", n)
	}
	// Turned off: nothing, whatever drifted.
	rec := filingRequest(fx.topicFx, http.MethodPut, "/api/sessions/"+fx.sess+"/plan-nudges", fx.token(topicOwner, "human"), map[string]any{"enabled": false})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("turn off: %d %s", rec.Code, rec.Body.String())
	}
	fx.checklistOp(map[string]any{"op": "add", "item": "Even more"})
	if n := fx.turn("human", "go vet ./..."); n != "" {
		t.Fatalf("nudged while off: %q", n)
	}
	// An interrupted turn is never judged either.
	filingRequest(fx.topicFx, http.MethodPut, "/api/sessions/"+fx.sess+"/plan-nudges", fx.token(topicOwner, "human"), map[string]any{"enabled": true})
	seq := fx.event("turn_done", `{"stop_reason":"interrupted"}`)
	fx.api.checkPlanTurn(context.Background(), fx.sess, seq, time.Now(), "interrupted")
	if n := planCarryingInput(t, fx.api, fx.sess, true); n != "" {
		t.Fatalf("interrupted turn nudged: %q", n)
	}
}

// The setting is the owner's: on by default, others get the uniform 404.
func TestPlanNudgesSetting(t *testing.T) {
	fx := newPlanFx(t, "")
	rec := filingRequest(fx.topicFx, http.MethodGet, "/api/sessions/"+fx.sess+"/plan-nudges", fx.token(topicOwner, "human"), nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("default: %d %s", rec.Code, rec.Body.String())
	}
	for name, tok := range map[string]string{"another person": fx.token(topicOther, "human"), "the session": fx.sessTok} {
		if rec := filingRequest(fx.topicFx, http.MethodPut, "/api/sessions/"+fx.sess+"/plan-nudges", tok, map[string]any{"enabled": false}); rec.Code == 200 {
			t.Fatalf("%s changed the setting", name)
		}
	}
	if on, _ := db.PlanNudgesOn(context.Background(), fx.pool, fx.sess); !on {
		t.Fatal("a refused caller turned nudges off")
	}
}

// A pinned card closed on the board by someone else is told once, with the unpin and tick commands;
// all pins done while working is told too.
func TestPlanNudgePinnedCardClosedElsewhere(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	fx.move(12, "In progress")
	fx.checklistSet("Parser #12")
	fx.checklistOp(map[string]any{"op": "doing", "index": 1})
	if n := fx.turn("human", "go test ./..."); n != "" {
		t.Fatalf("nudged before anything drifted: %q", n)
	}
	fx.board.set(12, "Done")                          // the person closed it on the board
	fx.api.invalidatePlanCard(fx.sess, "board-a", 12) // advance past the read cache
	n := fx.turn("human", "go test ./...")
	if !strings.Contains(n, "#12 \"Parser\" is closed on the board (\"Done\"). Unpin it: `blerg-runner unpin blerg-board:#12`, and tick its item (`blerg-runner checklist tick 1`)") ||
		!strings.Contains(n, "Every pinned ticket is done (blerg-board:#12)") {
		t.Fatalf("nudge = %q", n)
	}
	if again := fx.turn("human", "go test ./..."); again != "" {
		t.Fatalf("told twice: %q", again)
	}
}

// A cron session that keeps no plan is never nudged.
func TestPlanNudgeSkipsACronWithoutAPlan(t *testing.T) {
	fx := newPlanFx(t, "")
	row, _ := db.GetSession(context.Background(), fx.pool, fx.sess)
	cron := "c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0c2"
	row.CronID = &cron
	child := newUUID()
	if err := db.InsertSession(context.Background(), fx.pool, child, topicDaemonID, "idle", "/repos/app", "app", "Child", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET parent_session_id=$2,kind='agent' WHERE id=$1`, child, fx.sess); err != nil {
		t.Fatal(err)
	}
	facts, err := fx.api.planDrift(context.Background(), row, fx.event("turn_done", `{}`), time.Now())
	if err != nil || len(facts) != 0 {
		t.Fatalf("cron facts = %v %v", facts, err)
	}
}

// A child started this turn with no checklist item, and a finished child, are each told once; the
// nudge waits for the next input and never starts an extra child or parent turn.
func TestPlanNudgeChildren(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	ctx := context.Background()
	event := func(sid, kind, payload string) int64 {
		t.Helper()
		seq, _, err := db.AppendAgentEventAt(ctx, fx.pool, sid, newUUID(), kind, payload, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	event(fx.parent, "user_message", `{"text":"go"}`)
	child := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	cp, err := checklist.Apply(nil, checklist.Request{Op: "set", Items: []string{"Child work"}})
	if err != nil {
		t.Fatal(err)
	}
	cpRaw, _ := json.Marshal(cp)
	event(child, "checklist", string(cpRaw))
	event(child, "tool_call", `{"tool":"Bash","input":{"command":"make"}}`)
	// Through the real event hook: the child drift stays pending without waking its parent.
	HandleAgentEvent(ctx, fx.hub, fx.pool, fx.dc, protocol.AgentEvent{Type: "agent_event", SessionID: child, ClientEventID: newUUID(), Kind: "turn_done", Payload: json.RawMessage(`{"stop_reason":"end_turn"}`), Ts: time.Now().UTC().Format(time.RFC3339Nano)})
	// Wait for the check itself, not a wall-clock quiet window.
	fx.api.planFlights.mu.Lock()
	finished := fx.api.planFlights.done[child]
	fx.api.planFlights.mu.Unlock()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("child check did not finish")
	}
	pending, err := db.PendingPlanFacts(ctx, fx.pool, child)
	if err != nil || len(pending) == 0 {
		t.Fatalf("child drift not held: %v %v", pending, err)
	}
	note := fx.parentNote(fx.parent, 10*time.Second)
	if note.Source != childTurnSource {
		t.Fatalf("unexpected parent note: %+v", note)
	}
	select {
	case raw := <-fx.dc.send:
		t.Fatalf("child nudge generated an extra input: %s", raw)
	default:
	}
	seq := event(fx.parent, "turn_done", `{}`)
	fx.api.checkPlanTurn(ctx, fx.parent, seq, time.Now(), "end_turn")
	if n := planCarryingInput(t, fx.api, fx.parent, false); n != "" {
		t.Fatalf("old daemon received %q", n)
	}
	if n := planCarryingInput(t, fx.api, fx.parent, true); !strings.Contains(n, child) {
		t.Fatalf("worker fact absent: %q", n)
	}
	if err := db.RecordChildResultRead(ctx, fx.pool, fx.parent, child); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"waiting", "idle"} {
		_, err := fx.pool.Exec(ctx, `UPDATE sessions SET status=$2,started_at=now()-interval '1 hour' WHERE id=$1`, child, status)
		if err != nil {
			t.Fatal(err)
		}
		event(fx.parent, "user_message", `{"text":"next"}`)
		seq = event(fx.parent, "turn_done", `{}`)
		fx.api.checkPlanTurn(ctx, fx.parent, seq, time.Now(), "end_turn")
		n := planCarryingInput(t, fx.api, fx.parent, true)
		if strings.Contains(n, "looks finished") != (status == "idle") {
			t.Fatalf("%s: %q", status, n)
		}
	}
}

// No caller can write a drift nudge: the source is Blerg's.
func TestPlanDriftSourceIsReserved(t *testing.T) {
	fx := newDelegFx(t)
	_, apiErr := fx.api.SendMessage(context.Background(), runnerPrincipal{Kind: "human", Sub: "owner-1"}, fx.parent, "Your plan drifted", planDriftSource)
	if apiErr == nil || apiErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("a caller wrote in Blerg's voice: %+v", apiErr)
	}
}

// A child started through the delegation path with a title naming #N is assigned to the parent's
// open item naming #N (the start's own hook, not a direct call).
func TestChildStartNamingACardIsAssignedToItsItem(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	p, err := checklist.Apply(nil, checklist.Request{Op: "set", Items: []string{"Parser #41", "Lexer #42"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	if _, _, err := db.AppendAgentEventAt(ctx, fx.pool, fx.parent, newUUID(), "checklist", string(raw), time.Now()); err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 4)
	fx.api.planMovedHook = func(sid string) { started <- sid }
	rule, err := db.InsertDelegationRule(ctx, fx.pool, db.DelegationRule{AccountID: "owner-1", Engine: "claude",
		Runtime: runnerRuntimeDocker, DaemonID: fx.dc.ID, CreatedVia: "settings"})
	if err != nil {
		t.Fatal(err)
	}
	cs := fx.childStart()
	cs.Title = "Build the lexer (#42)"
	resp, apiErr := fx.api.startChild(ctx, fx.row(fx.parent), cs, "rule:"+rule.ID, newUUID())
	if apiErr != nil {
		t.Fatalf("startChild: %d %s", apiErr.Status, apiErr.Message)
	}
	fx.spawn()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the start's plan rules never ran")
	}
	ws, err := readWorkState(ctx, fx.pool, fx.parent, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	items := ws.Checklist.Items
	if len(items[0].Assignees) != 0 || !slices.Equal(items[1].Assignees, []checklist.Assignee{{Kind: "child_session", ID: resp.SessionID}}) {
		t.Fatalf("assignments = %+v", items)
	}
}

// A stopped child whose wip branch, with work committed after it started, is merged into the
// parent's branch looks finished, once; a branch that never moved, or one not merged, does not.
func TestPlanNudgeMergedChild(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	merged, idle, never := fx.childOf(fx.parent), fx.childOf(fx.parent), fx.childOf(fx.parent)
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status = 'stopped', title = 'Merged one', started_at = now() - interval '2 hours' WHERE id = $1;`, merged); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status = 'idle', started_at = now() - interval '2 hours' WHERE id = ANY($1)`, []string{idle, never}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	asked := map[string]int{}
	fx.api.planChildMerged = func(_ context.Context, _ *db.SessionRow, child string) (bool, time.Time) {
		mu.Lock()
		asked[child]++
		mu.Unlock()
		switch child {
		case merged:
			return true, time.Now().Add(-time.Hour)
		case never: // the daemon found no work beyond the recorded clone base
			return false, time.Time{}
		}
		return false, time.Time{}
	}
	turn := func() string {
		t.Helper()
		if _, _, err := db.AppendAgentEventAt(ctx, fx.pool, fx.parent, newUUID(), "user_message", `{"text":"go","source":"human"}`, time.Now()); err != nil {
			t.Fatal(err)
		}
		seq, _, err := db.AppendAgentEventAt(ctx, fx.pool, fx.parent, newUUID(), "turn_done", `{"stop_reason":"end_turn"}`, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		fx.api.checkPlanTurn(ctx, fx.parent, seq, time.Now(), "end_turn")
		return planCarryingInput(t, fx.api, fx.parent, true)
	}
	n := turn()
	want := `Child "Merged one" looks finished (stopped; its branch wip/` + merged + " is merged). Discard it: `blerg-runner session discard " + merged + "`"
	if !strings.Contains(n, want) || strings.Contains(n, idle) || strings.Contains(n, never) {
		t.Fatalf("nudge = %q", n)
	}
	if again := turn(); again != "" {
		t.Fatalf("told twice: %q", again)
	}
	mu.Lock()
	defer mu.Unlock()
	if asked[merged] != 2 {
		t.Fatalf("a told child was asked again: %v", asked)
	}
}

// The merged question goes to the session's own daemon as read_commit with merged_child, and its
// answer is read back.
func TestPlanChildMergedAsksTheDaemon(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	child := fx.childOf(fx.parent)
	type answer struct {
		merged bool
		tip    time.Time
	}
	got := make(chan answer, 1)
	go func() {
		m, tip := fx.api.planChildMergedCheck(context.Background(), fx.row(fx.parent), child)
		got <- answer{m, tip}
	}()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case raw := <-fx.dc.send:
			var req protocol.ReadCommit
			if json.Unmarshal(raw, &req) != nil || req.Type != "read_commit" {
				continue
			}
			if req.MergedChild != child || req.SessionID != fx.parent || req.SHA != "" {
				t.Fatalf("request = %+v", req)
			}
			fx.hub.deliverCommitResult(fx.dc, protocol.CommitResult{Type: "commit_result", RequestID: req.RequestID, SessionID: fx.parent,
				OK: true, Merged: true, TipDate: "2026-10-22T10:00:00Z"})
			a := <-got
			if !a.merged || !a.tip.IsZero() {
				t.Fatalf("answer = %+v", a)
			}
			return
		case <-deadline:
			t.Fatal("no read_commit reached the daemon")
		}
	}
}

func planCarryingInput(t *testing.T, a *API, sid string, capable bool) string {
	t.Helper()
	if _, err := a.dbPool.Exec(context.Background(), `UPDATE sessions SET status='idle' WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: sid, Text: "next input", Source: "chat"})
	out := a.attachPlanNudge(&DaemonConn{planNudges: capable}, raw)
	var in protocol.AgentUserMessage
	if err := json.Unmarshal(out, &in); err != nil {
		t.Fatal(err)
	}
	if in.Text != "next input" || in.Source != "chat" {
		t.Fatalf("carrying input changed: %+v", in)
	}
	return in.PlanNudge
}

func TestPlanMultiCardTickWaitsForAll(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	fx.checklistSet("Land #12 and #13 together")
	fx.move(12, "Done")
	if fx.work().Checklist.Items[0].Done {
		t.Fatal("ticked with #13 open")
	}
	fx.move(13, "Done")
	if !fx.work().Checklist.Items[0].Done {
		t.Fatal("all cards done but item open")
	}
}

func TestPlanReadCacheAndChatOnlyTurns(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	fx.move(12, "In progress")
	fx.checklistSet("Work #12")
	fx.checklistOp(map[string]any{"op": "doing", "index": 1})
	before := fx.board.reads
	fx.api.invalidatePlanCard(fx.sess, "board-a", 12)
	fx.turn("human")
	if fx.board.reads != before {
		t.Fatal("chat-only turn read Board")
	}
	fx.turn("human", "go test ./...")
	fx.turn("human", "go test ./...")
	if fx.board.reads != before+1 {
		t.Fatal("unchanged card was not cached")
	}
}

func TestPlanPendingClaimRollsBackAndIsOnce(t *testing.T) {
	fx := newPlanFx(t, "")
	fx.checklistSet("Work")
	fx.event("tool_call", `{"tool":"Bash","input":{"command":"go test ./..."}}`)
	seq := fx.event("turn_done", `{}`)
	fx.api.checkPlanTurn(context.Background(), fx.sess, seq, time.Now(), "end_turn")
	_, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET status='idle' WHERE id=$1`, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: fx.sess, Text: "continue", Source: "review"})
	out, finish := fx.api.preparePlanNudge(&DaemonConn{planNudges: true}, raw)
	var in protocol.AgentUserMessage
	_ = json.Unmarshal(out, &in)
	if in.PlanNudge == "" || finish == nil {
		t.Fatal("missing pending fact")
	}
	finish(false)
	if n := planCarryingInput(t, fx.api, fx.sess, true); n == "" {
		t.Fatal("failed dispatch lost fact")
	}
	if n := planCarryingInput(t, fx.api, fx.sess, true); n != "" {
		t.Fatal("fact claimed twice")
	}
}

func TestPlanWorkerIDsAndPreferredRefs(t *testing.T) {
	for _, id := range []string{"bad id", "x\ncommand", "x`cmd`", "../id"} {
		if validPlanWorkerID(id) {
			t.Fatalf("accepted %q", id)
		}
	}
	if got := preferredPlanRefs("Fix #12", "unlike #13"); !slices.Equal(got, []int{12}) {
		t.Fatal(got)
	}
	if got := preferredPlanRefs("Research", "Compare #12 and #13"); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestPlanChildWithOpenAskIsNotFinished(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	child := fx.childOf(fx.parent)
	_, err := fx.pool.Exec(ctx, `UPDATE sessions SET status='idle',started_at=now()-interval '1 hour' WHERE id=$1`, child)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RecordChildResultRead(ctx, fx.pool, fx.parent, child); err != nil {
		t.Fatal(err)
	}
	_, err = fx.pool.Exec(ctx, `INSERT INTO messages(session_id,kind,body) VALUES($1,'ask','Need an answer')`, child)
	if err != nil {
		t.Fatal(err)
	}
	seq, _, err := db.AppendAgentEventAt(ctx, fx.pool, fx.parent, newUUID(), "turn_done", `{}`, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	facts, err := fx.api.planDrift(ctx, fx.row(fx.parent), seq, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range facts {
		if f.key == "child-finished:"+child {
			t.Fatal("blocked child called finished")
		}
	}
}

func TestPlanArchiveAppliesBeforeToolReturns(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	fx.move(12, "In progress")
	fx.checklistSet("Finish #12")
	fx.board.mu.Lock()
	fx.board.cards[12].archived = true
	raw := fx.board.cardJSON(12, false)
	fx.board.mu.Unlock()
	result, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": raw}}})
	fx.api.planBoardToolDone(context.Background(), fx.sess, "board-a", "blerg_card_archive", result)
	if len(fx.pins()) != 0 || !fx.work().Checklist.Items[0].Done {
		t.Fatal("archive rules did not finish synchronously")
	}
}

func TestPlanBoardStartedCloseIsOwn(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	fx.move(12, "In progress")
	fx.checklistSet("Finish #12")
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET title='blerg-board #12: Parser' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	fx.board.set(12, "Done")
	fx.api.invalidatePlanCard(fx.sess, "board-a", 12)
	if n := fx.turn("human", "blerg-runner ticket move 12 Done"); strings.Contains(n, "closed on the board") {
		t.Fatalf("own close nudged: %q", n)
	}
	if len(fx.pins()) != 0 || !fx.work().Checklist.Items[0].Done {
		t.Fatal("board-started close not recorded")
	}
}
