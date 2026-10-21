package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

const fixtureCardID = "11111111-2222-4333-8444-555555555555"

// describingFiling is the filing fixture with a card read-back (Describe).
type describingFiling struct{ *fixtureFiling }

func (describingFiling) Describe(_ context.Context, _ mcpgw.Proof, board, id string) (filedCard, error) {
	return filedCard{ID: id, BoardID: board, Number: 7, Title: "Read back", Type: "bug", Priority: "high", Column: "Backlog"}, nil
}

func newPermFx(t *testing.T) (*topicFx, *fixtureFiling) {
	t.Helper()
	fx, up := newFilingFx(t)
	up.receipt = json.RawMessage(`{"id":"` + fixtureCardID + `","board_id":"board-a","number":7}`)
	fx.api.boardFiling = describingFiling{up}
	return fx, up
}

func fileOn(t *testing.T, fx *topicFx, board, title string) (int, map[string]any) {
	t.Helper()
	return fx.postWork("file-ticket", fx.sess, fx.sessTok, map[string]any{"board": board, "title": title, "body": "Fixture details"})
}

func TestSessionPermissionAutoApprovesFilingThroughTheApprovalPath(t *testing.T) {
	fx, up := newPermFx(t)
	human := fx.token(topicOwner, "human")

	// Without a permission the filing waits for the person.
	code, out := fileOn(t, fx, "A", "First")
	if code != 202 || out["state"] != "pending" || up.creates != 0 {
		t.Fatalf("without a grant: code=%d out=%v creates=%d", code, out, up.creates)
	}

	g := filingRequest(fx, "POST", "/api/sessions/"+fx.sess+"/proposal-grants", human, map[string]any{"kind": "board_file", "board_id": "board-a"})
	if g.Code != 200 {
		t.Fatalf("grant: %d %s", g.Code, g.Body.String())
	}
	var grant proposalGrantView
	_ = json.Unmarshal(g.Body.Bytes(), &grant)
	if grant.ID == "" || grant.BoardName != "A" || grant.CreatedVia != "panel" || strings.Contains(g.Body.String(), testSID) {
		t.Fatalf("grant view = %s", g.Body.String())
	}

	code, out = fileOn(t, fx, "A", "Second")
	if code != 200 || out["state"] != "done" || out["auto_approved"] != true || up.creates != 1 {
		t.Fatalf("with a grant: code=%d out=%v creates=%d", code, out, up.creates)
	}
	card, _ := out["card"].(map[string]any)
	if card["number"] != float64(7) || card["column"] != "Backlog" || card["board_name"] != "A" {
		t.Fatalf("card = %v", card)
	}
	// The approval acted on the login the permission's creator granted it with, like a person's.
	if len(up.proofs) != 1 || up.proofs[0].SessionID != testSID || up.proofs[0].AccountID != topicOwner {
		t.Fatalf("create proof = %+v", up.proofs)
	}
	p, err := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, out["proposal_id"].(string))
	if err != nil || p.ApprovedByGrant != grant.ID || p.State != db.ProposalDone || p.DecidedBy == nil || *p.DecidedBy != topicOwner {
		t.Fatalf("proposal = %+v err=%v", p, err)
	}
	// The decision is in the chat, with the card and the grant that allowed it.
	decided := fx.kindPayloads(fx.sess, "proposal_decided")
	if len(decided) != 1 || decided[0]["auto"] != true || decided[0]["grant_id"] != grant.ID {
		t.Fatalf("decided events = %v", decided)
	}
	if c, _ := decided[0]["card"].(map[string]any); c["title"] != "Read back" || c["priority"] != "high" {
		t.Fatalf("event card = %v", decided[0]["card"])
	}
	if raw, _ := json.Marshal(decided); strings.Contains(string(raw), "Fixture details") {
		t.Fatal("a card body entered the session's events")
	}
	if reqs := fx.kindPayloads(fx.sess, "proposal_request"); len(reqs) != 2 || reqs[0]["kind"] != "board_file" || len(reqs[0]) != 2 {
		t.Fatalf("request events = %v", reqs)
	}
}

func TestSessionPermissionScopeIsOneBoard(t *testing.T) {
	fx, up := newPermFx(t)
	human := fx.token(topicOwner, "human")
	if g := filingRequest(fx, "POST", "/api/sessions/"+fx.sess+"/proposal-grants", human, map[string]any{"kind": "board_file", "board_id": "board-a"}); g.Code != 200 {
		t.Fatalf("grant: %d", g.Code)
	}
	code, out := fileOn(t, fx, "B", "Elsewhere")
	if code != 202 || out["state"] != "pending" || up.creates != 0 {
		t.Fatalf("another board was approved: code=%d out=%v", code, out)
	}
	// A board the person cannot file to is never granted.
	if g := filingRequest(fx, "POST", "/api/sessions/"+fx.sess+"/proposal-grants", human, map[string]any{"kind": "board_file", "board_id": "board-z"}); g.Code != 404 {
		t.Fatalf("unknown board grant: %d", g.Code)
	}
	// A body may not widen the scope.
	if g := filingRequest(fx, "POST", "/api/sessions/"+fx.sess+"/proposal-grants", human, map[string]any{"kind": "board_file", "board_id": "board-a", "tool": "x"}); g.Code != 400 {
		t.Fatalf("widened grant: %d", g.Code)
	}
}

func TestSessionPermissionsAreOwnerOnly(t *testing.T) {
	fx, _ := newPermFx(t)
	other := fx.token(topicOther, "human")
	agent := fx.token(topicOwner, "agent")
	body := map[string]any{"kind": "board_file", "board_id": "board-a"}
	for name, tc := range map[string]struct {
		token string
		want  int
	}{"another account": {other, 404}, "a session token": {fx.sessTok, 401}, "an agent token": {agent, 403}, "nobody": {"", 401}} {
		rec := filingRequest(fx, "POST", "/api/sessions/"+fx.sess+"/proposal-grants", tc.token, body)
		if rec.Code != tc.want {
			t.Errorf("%s: grant %d, want %d", name, rec.Code, tc.want)
		}
		if get := filingRequest(fx, "GET", "/api/sessions/"+fx.sess+"/permissions", tc.token, nil); get.Code != tc.want {
			t.Errorf("%s: read %d, want %d", name, get.Code, tc.want)
		}
	}
	human := fx.token(topicOwner, "human")
	g := filingRequest(fx, "POST", "/api/sessions/"+fx.sess+"/proposal-grants", human, body)
	var grant proposalGrantView
	_ = json.Unmarshal(g.Body.Bytes(), &grant)
	if rec := filingRequest(fx, "POST", "/api/proposal-grants/"+grant.ID+"/revoke", other, nil); rec.Code != 404 {
		t.Fatalf("another account revoked: %d", rec.Code)
	}
	live, _ := db.ListLiveProposalGrants(t.Context(), fx.pool, fx.sess)
	if len(live) != 1 {
		t.Fatalf("live grants = %d", len(live))
	}
}

func TestRevokeStopsAutoApproval(t *testing.T) {
	fx, up := newPermFx(t)
	human := fx.token(topicOwner, "human")
	g := filingRequest(fx, "POST", "/api/sessions/"+fx.sess+"/proposal-grants", human, map[string]any{"kind": "board_file", "board_id": "board-a"})
	var grant proposalGrantView
	_ = json.Unmarshal(g.Body.Bytes(), &grant)
	if rec := filingRequest(fx, "POST", "/api/proposal-grants/"+grant.ID+"/revoke", human, nil); rec.Code != 200 {
		t.Fatalf("revoke: %d", rec.Code)
	}
	code, out := fileOn(t, fx, "A", "After revoke")
	if code != 202 || out["state"] != "pending" || up.creates != 0 {
		t.Fatalf("revoked grant approved: code=%d out=%v", code, out)
	}
	get := filingRequest(fx, "GET", "/api/sessions/"+fx.sess+"/permissions", human, nil)
	if get.Code != 200 || strings.Contains(get.Body.String(), grant.ID) {
		t.Fatalf("revoked grant still listed: %s", get.Body.String())
	}
}

// A revoke that lands between finding the grant and the compare-and-set wins: the CAS requires a
// live grant.
func TestRevokedGrantCannotWinTheCompareAndSet(t *testing.T) {
	fx, _ := newPermFx(t)
	p := queueFiling(t, fx)
	g, err := db.InsertProposalGrant(t.Context(), fx.pool, db.ProposalGrant{SessionID: fx.sess, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: "board-a", ProofSessionID: testSID, CreatedVia: "panel"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RevokeProposalGrant(t.Context(), fx.pool, g.ID, topicOwner, "revoked"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginBoardFilingExecutionByGrant(t.Context(), fx.pool, topicOwner, p.ID, topicOwner, string(p.Arguments), g.ID); err == nil {
		t.Fatal("a revoked grant approved a proposal")
	}
}

func TestAllowForThisSessionApprovesAndCoversTheNextFiling(t *testing.T) {
	fx, up := newPermFx(t)
	human := fx.token(topicOwner, "human")
	p := queueFiling(t, fx)
	rec := filingRequest(fx, "POST", "/api/proposals/"+p.ID+"/approve", human, map[string]any{"expected_arguments_raw": string(p.Arguments), "allow_for_session": true})
	if rec.Code != 200 || up.creates != 1 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if done, _ := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, p.ID); done.ApprovedByGrant != "" {
		t.Fatal("a person's approval was recorded as a grant's")
	}
	live, _ := db.ListLiveProposalGrants(t.Context(), fx.pool, fx.sess)
	if len(live) != 1 || live[0].BoardID != "board-a" || live[0].CreatedVia != "card" || live[0].CreatedFromProposal != p.ID {
		t.Fatalf("grant = %+v", live)
	}
	if code, out := fileOn(t, fx, "A", "Next"); code != 200 || out["state"] != "done" || up.creates != 2 {
		t.Fatalf("next filing: %d %v", code, out)
	}
}

func TestRestrictedAndCronSessionsCannotHoldPermissions(t *testing.T) {
	fx, _ := newPermFx(t)
	human := fx.token(topicOwner, "human")
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET restrict_tools = true WHERE id = $1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	rec := filingRequest(fx, "POST", "/api/sessions/"+fx.sess+"/proposal-grants", human, map[string]any{"kind": "board_file", "board_id": "board-a"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "restricted") {
		t.Fatalf("restricted: %d %s", rec.Code, rec.Body.String())
	}
	get := filingRequest(fx, "GET", "/api/sessions/"+fx.sess+"/permissions", human, nil)
	var view struct {
		CanGrant bool   `json:"can_grant"`
		Reason   string `json:"reason"`
	}
	_ = json.Unmarshal(get.Body.Bytes(), &view)
	if view.CanGrant || view.Reason == "" {
		t.Fatalf("view = %+v", view)
	}
	cronID := "c"
	cron := &db.SessionRow{Kind: "agent", Status: "running", CronID: &cronID}
	if proposalGrantProblem(cron) != msgPermCron {
		t.Fatal("a cron session may hold a permission")
	}
	if proposalGrantProblem(&db.SessionRow{Kind: "agent", Status: "running", StartedByKind: "agent"}) != msgPermStarter {
		t.Fatal("an agent-token session may hold a permission")
	}
}

func TestPermissionsEndWithTheSessionAndAParentDiscard(t *testing.T) {
	fx, up := newPermFx(t)
	ctx := t.Context()
	g, err := db.InsertProposalGrant(ctx, fx.pool, db.ProposalGrant{SessionID: fx.sess, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: "board-a", ProofSessionID: testSID, CreatedVia: "panel"})
	if err != nil {
		t.Fatal(err)
	}
	// A child of the session, with its own grant.
	child := newUUID()
	if err := db.InsertSession(ctx, fx.pool, child, topicDaemonID, "running", "/repos/app", "app", "c", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET kind='agent', spawning_account_id=$2, parent_session_id=$3, started_by_kind='session' WHERE id=$1`, child, topicOwner, fx.sess); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertProposalGrant(ctx, fx.pool, db.ProposalGrant{SessionID: child, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: "board-a", ProofSessionID: testSID, CreatedVia: "panel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginSessionDiscard(ctx, fx.pool, fx.sess, topicOwner); err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{fx.sess, child} {
		if live, _ := db.ListLiveProposalGrants(ctx, fx.pool, sid); len(live) != 0 {
			t.Fatalf("session %s keeps %d grants after its parent's discard", sid, len(live))
		}
	}
	_ = g
	_ = up

	// A session's end revokes them too (the daemon's session_ended path).
	fx2, _ := newPermFx(t)
	if _, err := db.InsertProposalGrant(ctx, fx2.pool, db.ProposalGrant{SessionID: fx2.sess, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: "board-a", ProofSessionID: testSID, CreatedVia: "panel"}); err != nil {
		t.Fatal(err)
	}
	revokeSessionGrants(ctx, fx2.pool, fx2.sess, "session_ended")
	if live, _ := db.ListLiveProposalGrants(ctx, fx2.pool, fx2.sess); len(live) != 0 {
		t.Fatal("a grant outlived its session")
	}
	// And an ended session cannot be given one.
	if _, err := db.InsertProposalGrant(ctx, fx2.pool, db.ProposalGrant{SessionID: fx2.ended, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: "board-a", ProofSessionID: testSID, CreatedVia: "panel"}); err == nil {
		row, _ := db.GetSession(ctx, fx2.pool, fx2.ended)
		if proposalGrantProblem(row) == "" {
			t.Fatal("an ended session can hold a permission")
		}
	}
}

// The MCP side: a permission covers one connection's one tool, the gateway's sink approves through
// the person's approval path (the upstream sees the creator's login), and another tool still asks.
func TestSessionPermissionAutoApprovesOneMCPTool(t *testing.T) {
	fx := newPropFx(t, 0)
	ctx := context.Background()
	sid := newUUID()
	if err := db.InsertSession(ctx, fx.pool, sid, fx.dc.ID, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET kind='agent', spawning_account_id=$2 WHERE id=$1`, sid, mcpAcct); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertMCPGrants(ctx, fx.pool, []db.MCPGrant{{SessionID: sid, ConnectionID: fx.connID, Name: mcpConn, AccountID: mcpAcct,
		Tools:       map[string]db.MCPGrantTool{"send": {Mode: mcpgw.ModePropose, Hash: fx.up.hash("send")}, "echo": {Mode: mcpgw.ModePropose, Hash: fx.up.hash("echo")}},
		URLSnapshot: fx.up.url(), ProofKind: "session_id", ProofValue: "launch-login", TokenHash: []byte(newUUID()), CallBudget: 100}}); err != nil {
		t.Fatal(err)
	}
	sink := &sessionGrantSink{inner: mcpgw.NewProposalSink(fx.pool, "", 0), api: fx.api}
	propose := func(tool string) string {
		text, err := sink.Propose(ctx, mcpgw.ProposalRequest{SessionID: sid, AccountID: mcpAcct, ConnectionID: fx.connID, ConnectionName: mcpConn,
			URLSnapshot: fx.up.url(), Tool: tool, ToolHash: fx.up.hash(tool), Arguments: json.RawMessage(`{"to":"a@example.com"}`)})
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	if text := propose("send"); !strings.Contains(text, "NOT performed") || fx.up.callCount() != 0 {
		t.Fatalf("without a grant: %q calls=%d", text, fx.up.callCount())
	}
	rec := fx.req("POST", "/api/sessions/"+sid+"/proposal-grants", fx.human(), `{"kind":"mcp_tool","connection_id":"`+fx.connID+`","tool":"send"}`)
	if rec.Code != 200 {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}
	if text := propose("send"); !strings.Contains(text, "approved by a session permission") || !strings.Contains(text, "ok:send") || fx.up.callCount() != 1 {
		t.Fatalf("with a grant: %q calls=%d", text, fx.up.callCount())
	}
	for _, pr := range fx.tokens.proofsSeen() {
		if pr.SessionID != testSID {
			t.Fatalf("the credential was fetched with %+v, not the creator's login", pr)
		}
	}
	if text := propose("echo"); !strings.Contains(text, "NOT performed") || fx.up.callCount() != 1 {
		t.Fatalf("another tool was approved: %q", text)
	}
	// m3: the grant holds the definition it was given on. A changed definition asks again, and the
	// panel says the grant no longer applies.
	// The server changes the tool and the session re-pins it (as auto_accept does): the upstream,
	// the session's pin and the proposal all carry the new definition, which nobody granted.
	fx.up.setDescription("send", "sends, and now also forwards")
	newHash := fx.up.hash("send")
	if _, err := fx.pool.Exec(ctx, `UPDATE session_mcp_grants SET tools = jsonb_set(tools, '{send,hash}', to_jsonb($2::text)) WHERE session_id = $1::uuid`, sid, newHash); err != nil {
		t.Fatal(err)
	}
	changed, err := sink.Propose(ctx, mcpgw.ProposalRequest{SessionID: sid, AccountID: mcpAcct, ConnectionID: fx.connID, ConnectionName: mcpConn,
		URLSnapshot: fx.up.url(), Tool: "send", ToolHash: newHash, Arguments: json.RawMessage(`{"to":"b@example.com"}`)})
	if err != nil || !strings.Contains(changed, "NOT performed") || fx.up.callCount() != 1 {
		t.Fatalf("a changed definition was approved: %q %v", changed, err)
	}
	panel := fx.req("GET", "/api/sessions/"+sid+"/permissions", fx.human(), "")
	var view struct{ Grants []proposalGrantView }
	_ = json.Unmarshal(panel.Body.Bytes(), &view)
	if len(view.Grants) != 1 || view.Grants[0].Active || view.Grants[0].Inactive != "tool_changed" {
		t.Fatalf("panel grants = %s", panel.Body.String())
	}
	// A tool the session may simply call, or one it does not have, cannot be granted.
	if rec := fx.req("POST", "/api/sessions/"+sid+"/proposal-grants", fx.human(), `{"kind":"mcp_tool","connection_id":"`+fx.connID+`","tool":"nope"}`); rec.Code != 422 {
		t.Fatalf("unknown tool grant: %d", rec.Code)
	}
}

// The launch sheet's permissions are granted, through the panel's paths, as the session starts;
// what cannot be granted is named and the session still starts. Only a signed-in person may send
// them.
func TestLaunchSheetPreAuthorizesPermissions(t *testing.T) {
	fx := newMCPFx(t)
	body := func() map[string]any {
		return map[string]any{"repo": "acme/widget", "runtime": "cluster", "kind": "agent",
			"mcp": []map[string]any{{"connection": fx.connID, "tools": map[string]any{
				"echo": map[string]any{"mode": "propose", "hash": fx.up.hash("echo")}}}},
			"permissions": map[string]any{
				"mcp_tools": []map[string]any{{"connection_id": fx.connID, "tool": "echo"}},
				"boards":    []string{"board-x"},
				"children":  []string{"docker"},
			}}
	}
	if rec := fx.do(fx.api.HandleLaunchSession, fx.token("agent", ""), body()); rec.Code != http.StatusForbidden || fx.count("sessions") != 0 {
		t.Fatalf("agent token with permissions: %d, sessions %d", rec.Code, fx.count("sessions"))
	}
	bad := body()
	bad["permissions"] = map[string]any{"children": []string{"pod"}}
	if rec := fx.do(fx.api.HandleLaunchSession, fx.human(), bad); rec.Code != http.StatusUnprocessableEntity || fx.count("sessions") != 0 {
		t.Fatalf("malformed permissions: %d", rec.Code)
	}
	rec := fx.do(fx.api.HandleLaunchSession, fx.human(), body())
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		SessionID string   `json:"session_id"`
		Problems  []string `json:"permission_problems"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	live, err := db.ListLiveProposalGrants(context.Background(), fx.pool, out.SessionID)
	if err != nil || len(live) != 1 || live[0].Tool != "echo" || live[0].CreatedVia != "launch" || live[0].ProofSessionID != testSID {
		t.Fatalf("grants = %+v err=%v", live, err)
	}
	if len(out.Problems) != 2 || !strings.Contains(out.Problems[0], "sandboxed children") || !strings.Contains(out.Problems[0], msgChildRuntimeUnavailable) || !strings.Contains(out.Problems[1], "Filing cards") {
		t.Fatalf("problems = %q", out.Problems)
	}
}

// ---- fix round 1 -------------------------------------------------------------------------------

func grantBoardA(t *testing.T, fx *topicFx) proposalGrantView {
	t.Helper()
	g := filingRequest(fx, "POST", "/api/sessions/"+fx.sess+"/proposal-grants", fx.token(topicOwner, "human"), map[string]any{"kind": "board_file", "board_id": "board-a"})
	if g.Code != 200 {
		t.Fatalf("grant: %d %s", g.Code, g.Body.String())
	}
	var v proposalGrantView
	_ = json.Unmarshal(g.Body.Bytes(), &v)
	return v
}

// I1: the same draft again from the same session returns the earlier filing and its state; it never
// files a second card (an older CLI that retries on "done", a lost reply).
func TestRepeatedFilingReturnsTheEarlierProposal(t *testing.T) {
	fx, up := newPermFx(t)
	// Without a permission: the repeat is the same pending proposal.
	code1, first := fileOn(t, fx, "B", "Waits")
	code2, again := fileOn(t, fx, "B", "Waits")
	if code1 != 202 || code2 != 202 || again["proposal_id"] != first["proposal_id"] || again["repeated"] != true || again["state"] != "pending" {
		t.Fatalf("pending repeat: %v / %v", first, again)
	}
	grantBoardA(t, fx)
	_, done := fileOn(t, fx, "A", "Once")
	code, repeat := fileOn(t, fx, "A", "Once")
	if code != 200 || repeat["proposal_id"] != done["proposal_id"] || repeat["state"] != "done" || repeat["repeated"] != true || up.creates != 1 {
		t.Fatalf("done repeat: code=%d %v creates=%d", code, repeat, up.creates)
	}
	if card, _ := repeat["card"].(map[string]any); card["number"] != float64(7) {
		t.Fatalf("repeat card = %v", repeat["card"])
	}
	// A different draft is a new filing.
	if _, other := fileOn(t, fx, "A", "Another"); other["proposal_id"] == done["proposal_id"] || up.creates != 2 {
		t.Fatalf("a different draft was deduplicated: %v", other)
	}
}

// I2: a permission approves at most its hourly cap; past it, proposals ask, and say why.
func TestSessionPermissionHourlyCapFallsBackToAsking(t *testing.T) {
	fx, up := newPermFx(t)
	fx.api.grantCapOverride = 2
	grantBoardA(t, fx)
	for _, title := range []string{"One", "Two"} {
		if code, out := fileOn(t, fx, "A", title); code != 200 || out["state"] != "done" {
			t.Fatalf("%s: %d %v", title, code, out)
		}
	}
	code, out := fileOn(t, fx, "A", "Three")
	if code != 202 || out["state"] != "pending" || up.creates != 2 || !strings.Contains(fmt.Sprint(out["message"]), "limit") {
		t.Fatalf("over the cap: %d %v creates=%d", code, out, up.creates)
	}
	reqs := fx.kindPayloads(fx.sess, "proposal_request")
	if last := reqs[len(reqs)-1]; !strings.Contains(fmt.Sprint(last["note"]), "waits for you") {
		t.Fatalf("the card does not say why: %v", last)
	}
}

// A burst can pass the soft count and then meet the atomic cap; the proposal stays pending and the
// agent is told why (review M5).
func TestSessionPermissionAtomicCapSaysWhy(t *testing.T) {
	fx, up := newPermFx(t)
	fx.api.grantCapOverride = 2
	grantBoardA(t, fx)
	if code, out := fileOn(t, fx, "A", "One"); code != 200 || out["state"] != "done" {
		t.Fatalf("first: %d %v", code, out)
	}
	// A concurrent approval lands between the soft count and the approval itself.
	fx.api.beforeGrantApproval = func() { fx.api.grantCapOverride = 1 }
	code, out := fileOn(t, fx, "A", "Two")
	if code != 202 || out["state"] != "pending" || up.creates != 1 {
		t.Fatalf("over the atomic cap: %d %v creates=%d", code, out, up.creates)
	}
	if !strings.Contains(fmt.Sprint(out["message"]), "limit") {
		t.Fatalf("the agent is not told why it waits: %v", out)
	}
}

// m2: the approval's compare-and-set requires the grant's own scope, not just a live grant of the
// session.
func TestGrantOfAnotherBoardCannotWinTheCompareAndSet(t *testing.T) {
	fx, _ := newPermFx(t)
	p := queueFiling(t, fx) // board A
	g, err := db.InsertProposalGrant(t.Context(), fx.pool, db.ProposalGrant{SessionID: fx.sess, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: "board-b", ProofSessionID: testSID, CreatedVia: "panel"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginBoardFilingExecutionByGrant(t.Context(), fx.pool, topicOwner, p.ID, topicOwner, string(p.Arguments), g.ID); err == nil {
		t.Fatal("a grant for board B approved a filing on board A")
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET archived_at = now() WHERE id = $1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	ga, err := db.InsertProposalGrant(t.Context(), fx.pool, db.ProposalGrant{SessionID: fx.sess, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: "board-a", ProofSessionID: testSID, CreatedVia: "panel"})
	if err == nil {
		if _, err := db.BeginBoardFilingExecutionByGrant(t.Context(), fx.pool, topicOwner, p.ID, topicOwner, string(p.Arguments), ga.ID); err == nil {
			t.Fatal("a grant of an archived session approved a filing")
		}
	}
}

// deadLoginCore answers core's refusal for one ended login session.
type deadLoginCore struct {
	filingBoardList
	dead string
}

func (c *deadLoginCore) ListConnections(_ context.Context, p mcpgw.Proof) ([]MCPConnection, error) {
	if p.SessionID == c.dead {
		return nil, ErrMCPProofInvalid
	}
	return nil, nil
}

// m5: a grant whose sign-in ended shows as inactive, not in force.
func TestPanelShowsAGrantWhoseSignInEndedAsInactive(t *testing.T) {
	fx, _ := newPermFx(t)
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &deadLoginCore{dead: "ended-login"}})
	if _, err := db.InsertProposalGrant(t.Context(), fx.pool, db.ProposalGrant{SessionID: fx.sess, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: "board-a", ProofSessionID: "ended-login", CreatedVia: "panel"}); err != nil {
		t.Fatal(err)
	}
	get := filingRequest(fx, "GET", "/api/sessions/"+fx.sess+"/permissions", fx.token(topicOwner, "human"), nil)
	var view struct{ Grants []proposalGrantView }
	_ = json.Unmarshal(get.Body.Bytes(), &view)
	if len(view.Grants) != 1 || view.Grants[0].Active || view.Grants[0].Inactive != "signed_out" {
		t.Fatalf("grants = %+v", view.Grants)
	}
	// Giving it again from the current sign-in replaces it.
	g := grantBoardA(t, fx)
	live, _ := db.ListLiveProposalGrants(t.Context(), fx.pool, fx.sess)
	if len(live) != 1 || live[0].ID != g.ID || live[0].ProofSessionID != testSID {
		t.Fatalf("renewed grants = %+v", live)
	}
}

// m8: "Allow for this session" whose approval is then refused keeps the permission and says so.
func TestAllowForSessionRefusedApprovalSaysThePermissionStands(t *testing.T) {
	fx, up := newPermFx(t)
	p := queueFiling(t, fx)
	up.err = apiErrorf(422, "Board refused the draft.")
	rec := filingRequest(fx, "POST", "/api/proposals/"+p.ID+"/approve", fx.token(topicOwner, "human"), map[string]any{"expected_arguments_raw": string(p.Arguments), "allow_for_session": true})
	var out struct {
		Error          string `json:"error"`
		PermissionKept bool   `json:"permission_kept"`
		GrantID        string `json:"grant_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code < 400 || !out.PermissionKept || out.GrantID == "" || !strings.Contains(out.Error, "was kept") {
		t.Fatalf("refused approval: %d %s", rec.Code, rec.Body.String())
	}
	if live, _ := db.ListLiveProposalGrants(t.Context(), fx.pool, fx.sess); len(live) != 1 || live[0].ID != out.GrantID {
		t.Fatalf("grant = %+v", live)
	}
}

type countingPermissionCore struct {
	deadLoginCore
	calls atomic.Int32
}

func (c *countingPermissionCore) ListConnections(ctx context.Context, p mcpgw.Proof) ([]MCPConnection, error) {
	c.calls.Add(1)
	return c.deadLoginCore.ListConnections(ctx, p)
}

func TestPermissionPanelCachesSignInAndInvalidatesOnLevelChange(t *testing.T) {
	fx, _ := newPermFx(t)
	core := &countingPermissionCore{}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: core})
	// Another browser session made this grant, so the viewer's bearer cannot
	// supply its liveness result directly.
	_, err := db.InsertProposalGrant(t.Context(), fx.pool, db.ProposalGrant{SessionID: fx.sess, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: "board-a", ProofSessionID: "other-login", CreatedVia: "panel"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertProposalGrant(t.Context(), fx.pool, db.ProposalGrant{SessionID: fx.sess, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: "board-b", ProofSessionID: "other-login", CreatedVia: "panel"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	fx.api.permissionSignIns.now = func() time.Time { return now }
	get := func() {
		rec := filingRequest(fx, "GET", "/api/sessions/"+fx.sess+"/permissions", fx.token(topicOwner, "human"), nil)
		if rec.Code != 200 {
			t.Fatalf("panel=%d", rec.Code)
		}
	}
	get()
	get()
	if got := core.calls.Load(); got != 1 {
		t.Fatalf("panel made %d core calls, want 1", got)
	}
	g, err := db.InsertProposalGrant(t.Context(), fx.pool, db.ProposalGrant{SessionID: fx.sess, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: "board-a", Level: db.BoardLevelRead, ProofSessionID: "other-login", CreatedVia: "panel"})
	if err != nil {
		t.Fatal(err)
	}
	get()
	if got := core.calls.Load(); got != 2 {
		t.Fatalf("level change did not invalidate: calls=%d", got)
	}
	if _, err := db.RevokeProposalGrant(t.Context(), fx.pool, g.ID, topicOwner, "revoked"); err != nil {
		t.Fatal(err)
	}
	get()
	if got := core.calls.Load(); got != 3 {
		t.Fatalf("revoke did not invalidate: calls=%d", got)
	}
	now = now.Add(permissionSignInTTL)
	get()
	if got := core.calls.Load(); got != 4 {
		t.Fatalf("expired cache did not refresh: calls=%d", got)
	}
}
