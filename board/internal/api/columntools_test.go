package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// columnTool calls one board MCP tool as a session's exchanged board credential would arrive: a
// core agent token scoped to the board, with the runner session the runner names.
func columnTool(t *testing.T, fx scopeFixture, token, session, tool string, args map[string]any) mcpResult {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	req, _ := http.NewRequestWithContext(context.Background(), "POST", fx.srv.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if session != "" {
		req.Header.Set("X-Blerg-Runner-Session", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	decodeBody(t, resp, &out)
	r := mcpResult{IsError: out.Result.IsError}
	if len(out.Result.Content) > 0 {
		r.Text = out.Result.Content[0].Text
	}
	return r
}

func columnNames(t *testing.T, fx scopeFixture, boardID string) []string {
	t.Helper()
	cols, err := db.ListColumns(context.Background(), fx.pool, boardID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range cols {
		out = append(out, c.Name)
	}
	return out
}

// A session at Full board access (its exchanged credential carries column.write) manages the
// board's columns: add (placed), rename and set terminal, move, and remove (empty, or moving its
// cards). Every change is in the column audit with who made it and which runner session.
func TestColumnToolsManageColumnsAndAudit(t *testing.T) {
	fx := newScopeFixture(t)
	ctx := context.Background()
	tok := projectToken(t, fx.A.ID, boardAgentCaps)
	const session = "6f1c2c1e-0000-4000-8000-000000000001"

	var created db.Column
	r := columnTool(t, fx, tok, session, "blerg_column_create", map[string]any{"board_id": fx.A.ID, "name": "0.5.0 to do", "before_id": fx.colA2.ID})
	if r.IsError || json.Unmarshal([]byte(r.Text), &created) != nil {
		t.Fatalf("create: %+v", r)
	}
	if got := strings.Join(columnNames(t, fx, fx.A.ID), ","); !strings.HasSuffix(got, ",Inbox,0.5.0 to do,Done") {
		t.Fatalf("columns after a placed create = %s", got)
	}
	if r := columnTool(t, fx, tok, session, "blerg_column_update", map[string]any{"column_id": created.ID, "name": "Next", "is_terminal": true}); r.IsError {
		t.Fatalf("update: %+v", r)
	}
	col, _ := db.GetColumn(ctx, fx.pool, created.ID)
	if col.Name != "Next" || !col.IsTerminal {
		t.Fatalf("updated column = %+v", col)
	}
	if r := columnTool(t, fx, tok, session, "blerg_column_move", map[string]any{"column_id": created.ID}); r.IsError {
		t.Fatalf("move: %+v", r)
	}
	if got := strings.Join(columnNames(t, fx, fx.A.ID), ","); !strings.HasSuffix(got, ",Inbox,Done,Next") {
		t.Fatalf("columns after a move = %s", got)
	}

	// Inbox holds cards: removing it needs a target for them, which must be another column of this board.
	if r := columnTool(t, fx, tok, session, "blerg_column_delete", map[string]any{"column_id": fx.colA.ID}); !r.IsError || !strings.Contains(r.Text, "move_cards_to") {
		t.Fatalf("delete with cards and no target: %+v", r)
	}
	if r := columnTool(t, fx, tok, session, "blerg_column_delete", map[string]any{"column_id": fx.colA.ID, "move_cards_to": fx.colB.ID}); !r.IsError {
		t.Fatalf("delete moving cards to another board: %+v", r)
	}
	var inInbox int
	_ = fx.pool.QueryRow(ctx, `SELECT count(*) FROM cards WHERE column_id = $1`, fx.colA.ID).Scan(&inInbox)
	if inInbox == 0 {
		t.Fatal("the fixture's Inbox has no cards")
	}
	if r := columnTool(t, fx, tok, session, "blerg_column_delete", map[string]any{"column_id": fx.colA.ID, "move_cards_to": created.ID}); r.IsError {
		t.Fatalf("delete moving cards: %+v", r)
	}
	var inNext int
	_ = fx.pool.QueryRow(ctx, `SELECT count(*) FROM cards WHERE column_id = $1`, created.ID).Scan(&inNext)
	if inNext != inInbox {
		t.Fatalf("moved %d cards, want %d", inNext, inInbox)
	}
	if r := columnTool(t, fx, tok, "", "blerg_column_delete", map[string]any{"column_id": created.ID}); !r.IsError {
		t.Fatalf("delete of a column that now holds cards: %+v", r)
	}
	empty, _ := db.CreateColumn(ctx, fx.pool, fx.A.ID, "Empty", false)
	if r := columnTool(t, fx, tok, "", "blerg_column_delete", map[string]any{"column_id": empty.ID}); r.IsError {
		t.Fatalf("delete of an empty column: %+v", r)
	}

	events, err := db.ListColumnEvents(ctx, fx.pool, fx.A.ID)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range events {
		types = append(types, e.Type)
		if e.Actor != "agent" || e.ActorAccount != "acct-1" || !strings.HasPrefix(e.ActorToken, "core:") {
			t.Errorf("event %s actor = %q/%q/%q", e.Type, e.Actor, e.ActorAccount, e.ActorToken)
		}
	}
	if got := strings.Join(types, ","); got != "created,moved,updated,moved,deleted,deleted" {
		t.Fatalf("audit = %s", got)
	}
	if events[0].RunnerSession != session || events[len(events)-1].RunnerSession != "" {
		t.Fatalf("runner sessions = %q … %q", events[0].RunnerSession, events[len(events)-1].RunnerSession)
	}
}

// Without column.write (a credential below Full) every column tool is refused and nothing changes.
func TestColumnToolsNeedColumnWrite(t *testing.T) {
	fx := newScopeFixture(t)
	tok := projectToken(t, fx.A.ID, []string{"card.read", "card.write"})
	before := strings.Join(columnNames(t, fx, fx.A.ID), ",")
	for tool, args := range map[string]map[string]any{
		"blerg_column_create": {"board_id": fx.A.ID, "name": "x"},
		"blerg_column_update": {"column_id": fx.colA.ID, "name": "x"},
		"blerg_column_move":   {"column_id": fx.colA.ID},
		"blerg_column_delete": {"column_id": fx.colA2.ID, "move_cards_to": fx.colA.ID},
	} {
		if r := columnTool(t, fx, tok, "", tool, args); !r.IsError {
			t.Errorf("%s without column.write: %+v", tool, r)
		}
	}
	if after := strings.Join(columnNames(t, fx, fx.A.ID), ","); after != before {
		t.Fatalf("columns changed: %s → %s", before, after)
	}
	if events, _ := db.ListColumnEvents(context.Background(), fx.pool, fx.A.ID); len(events) != 0 {
		t.Fatalf("refused calls were audited: %+v", events)
	}
}

// Removing a column that holds cards moves those cards, so it is a card move: it needs card.write
// and every card passes the board's admission gate as a move. A denial changes nothing.
func TestColumnRemovalHonoursTheAdmissionGate(t *testing.T) {
	fx := newScopeFixture(t)
	ctx := context.Background()
	before, _ := db.ColumnCardIDs(ctx, fx.pool, fx.colA.ID)
	if len(before) == 0 {
		t.Fatal("the fixture's Inbox has no cards")
	}
	columnOnly := projectToken(t, fx.A.ID, []string{"card.read", "column.write"})
	if r := columnTool(t, fx, columnOnly, "", "blerg_column_delete", map[string]any{"column_id": fx.colA.ID, "move_cards_to": fx.colA2.ID}); !r.IsError {
		t.Fatalf("removal moving cards without card.write: %+v", r)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE boards SET gate_enabled = true WHERE id = $1`, fx.A.ID); err != nil {
		t.Fatal(err)
	}
	tok := projectToken(t, fx.A.ID, boardAgentCaps)
	// The same principal's ordinary move is denied by the gate…
	if r := columnTool(t, fx, tok, "", "blerg_card_move", map[string]any{"card_id": before[0], "column_id": fx.colA2.ID}); !r.IsError {
		t.Fatalf("ordinary move passed the gate: %+v", r)
	}
	// …and so is removing the column with a move target.
	if r := columnTool(t, fx, tok, "", "blerg_column_delete", map[string]any{"column_id": fx.colA.ID, "move_cards_to": fx.colA2.ID}); !r.IsError || !strings.Contains(r.Text, "deny") {
		t.Fatalf("removal moving gated cards: %+v", r)
	}
	after, _ := db.ColumnCardIDs(ctx, fx.pool, fx.colA.ID)
	if _, err := db.GetColumn(ctx, fx.pool, fx.colA.ID); err != nil || len(after) != len(before) {
		t.Fatalf("a denied removal changed the board: column err %v, cards %d → %d", err, len(before), len(after))
	}
	if events, _ := db.ListColumnEvents(ctx, fx.pool, fx.A.ID); len(events) != 0 {
		t.Fatalf("a denied removal was audited: %+v", events)
	}
}

// Cards moved by a column's removal get what any card move gets: the destination's standing agents
// are queued for each.
func TestColumnRemovalQueuesStandingAgents(t *testing.T) {
	fx := newScopeFixture(t)
	ctx := context.Background()
	cards, _ := db.ColumnCardIDs(ctx, fx.pool, fx.colA.ID)
	if _, err := db.CreateStandingAgent(ctx, fx.pool, fx.A.ID, fx.colA2.ID, "triage", "blerg-runner", "look", "per_card"); err != nil {
		t.Fatal(err)
	}
	tok := projectToken(t, fx.A.ID, boardAgentCaps)
	if r := columnTool(t, fx, tok, "", "blerg_column_delete", map[string]any{"column_id": fx.colA.ID, "move_cards_to": fx.colA2.ID}); r.IsError {
		t.Fatalf("removal: %+v", r)
	}
	var queued int
	if err := fx.pool.QueryRow(ctx, `SELECT count(*) FROM standing_agent_queue q JOIN standing_agents s ON s.id = q.standing_agent_id WHERE s.column_id = $1`, fx.colA2.ID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != len(cards) {
		t.Fatalf("queued %d standing-agent jobs, want %d", queued, len(cards))
	}
}

// A column change commits only with its audit row: an audit that cannot be written rolls it back.
func TestColumnChangeRollsBackWithoutItsAudit(t *testing.T) {
	fx := newScopeFixture(t)
	ctx := context.Background()
	if _, err := fx.pool.Exec(ctx, `
		CREATE FUNCTION refuse_column_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit refused'; END $$;
		CREATE TRIGGER refuse_column_audit BEFORE INSERT ON column_events FOR EACH ROW EXECUTE FUNCTION refuse_column_audit();`); err != nil {
		t.Fatal(err)
	}
	tok := projectToken(t, fx.A.ID, boardAgentCaps)
	before := strings.Join(columnNames(t, fx, fx.A.ID), ",")
	for tool, args := range map[string]map[string]any{
		"blerg_column_create": {"board_id": fx.A.ID, "name": "unaudited"},
		"blerg_column_update": {"column_id": fx.colA2.ID, "name": "unaudited"},
		"blerg_column_move":   {"column_id": fx.colA.ID},
		"blerg_column_delete": {"column_id": fx.colA.ID, "move_cards_to": fx.colA2.ID},
	} {
		if r := columnTool(t, fx, tok, "", tool, args); !r.IsError {
			t.Errorf("%s succeeded without its audit: %+v", tool, r)
		}
	}
	if after := strings.Join(columnNames(t, fx, fx.A.ID), ","); after != before {
		t.Fatalf("columns changed without an audit: %s → %s", before, after)
	}
}
