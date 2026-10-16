package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type tcFx struct {
	*propFx
	sessionID string
}

// newTCFx: the proposals fixture plus the tool-change routes, and a grant for a session of the
// fixture's owner on the fixture's connection, pinned to echo's CURRENT hash. The routes read the
// grant row, never the session row, so no session row is needed.
func newTCFx(t *testing.T) *tcFx {
	t.Helper()
	fx := newPropFx(t, 0)
	fx.api.RegisterToolChangeRoutes(fx.mux)
	ctx := context.Background()
	sid := newUUID()
	_, err := mcpgw.CreateGrants(ctx, fx.pool, sid, mcpAcct, mcpgw.Proof{AccountID: mcpAcct, SessionID: testSID}, []mcpgw.GrantSpec{{
		ConnectionID: fx.connID, Name: mcpConn, URL: fx.up.url(),
		Tools:  map[string]mcpgw.ToolGrant{"echo": {Mode: "allow", Hash: fx.up.hash("echo")}},
		Preset: db.PresetCustom, KnownTools: []string{"echo", "send"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	fx.hub.SetSessionOwner(sid, fx.dc.ID)
	return &tcFx{propFx: fx, sessionID: sid}
}

// drift changes echo upstream and records the change the gateway would, with its definitions.
func (fx *tcFx) drift(desc string) *db.ToolChange {
	fx.t.Helper()
	ctx := context.Background()
	old := fx.up.hash("echo")
	for _, name := range []string{"echo"} {
		fx.storeDef(name)
	}
	fx.up.setDescription("echo", desc)
	fx.storeDef("echo")
	out, err := db.RecordToolChange(ctx, fx.pool, db.ToolChange{
		AccountID: mcpAcct, SessionID: fx.sessionID, ConnectionID: fx.connID, ConnectionName: mcpConn,
		Tool: "echo", Kind: db.ToolChangeChanged, OldHash: old, NewHash: fx.up.hash("echo"), Mode: "allow",
	}, db.ToolChangeLimits{})
	if err != nil || !out.Inserted {
		fx.t.Fatalf("record: %+v %v", out, err)
	}
	c, err := db.GetOwnedToolChange(ctx, fx.pool, mcpAcct, out.ID)
	if err != nil {
		fx.t.Fatal(err)
	}
	return c
}

func (fx *tcFx) storeDef(name string) {
	fx.t.Helper()
	fx.up.mu.Lock()
	var tl mcpTestTool
	for _, x := range fx.up.tools {
		if x.Name == name {
			tl = x
		}
	}
	fx.up.mu.Unlock()
	canon, hash := mcpgw.CanonicalTool(tl.Name, tl.Description, json.RawMessage(tl.Schema))
	if err := db.UpsertToolDefinition(context.Background(), fx.pool, db.ToolDefinition{Hash: hash, Name: name, Canonical: string(canon), Size: len(canon)}); err != nil {
		fx.t.Fatal(err)
	}
}

func (fx *tcFx) grant() db.MCPGrant {
	fx.t.Helper()
	gs, err := db.ListMCPGrantsForSession(context.Background(), fx.pool, fx.sessionID)
	if err != nil || len(gs) != 1 {
		fx.t.Fatalf("grants: %v %v", gs, err)
	}
	return gs[0]
}

func TestToolChangesRoutesAreHumanAndOwnerOnly(t *testing.T) {
	fx := newTCFx(t)
	c := fx.drift("v2")
	for _, tc := range []struct {
		name, token string
		want        int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"agent token", fx.token("agent", ""), http.StatusForbidden},
		{"other account", fx.otherHuman(), http.StatusNotFound},
	} {
		rec := fx.req(http.MethodGet, "/api/tool-changes/"+c.ID, tc.token, "")
		if rec.Code != tc.want {
			t.Errorf("%s: GET one = %d, want %d", tc.name, rec.Code, tc.want)
		}
		rec = fx.req(http.MethodPost, "/api/tool-changes/"+c.ID+"/accept", tc.token, `{"mode":"allow"}`)
		if rec.Code != tc.want {
			t.Errorf("%s: accept = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
	rec := fx.req(http.MethodGet, "/api/sessions/"+fx.sessionID+"/pin-events", fx.token("agent", ""), "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("agent token on pin events = %d", rec.Code)
	}
}

// Every tool-change route is human-only: an agent token is refused and no token is unauthenticated.
func TestToolChangeRoutesRefuseAgentsAndAnonymous(t *testing.T) {
	fx := newTCFx(t)
	c := fx.drift("v2")
	for _, rt := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/tool-changes", ""},
		{http.MethodGet, "/api/tool-changes/" + c.ID, ""},
		{http.MethodPost, "/api/tool-changes/" + c.ID + "/accept", `{"mode":"allow"}`},
		{http.MethodPost, "/api/tool-changes/" + c.ID + "/decline", ""},
		{http.MethodGet, "/api/sessions/" + fx.sessionID + "/pin-events", ""},
		{http.MethodGet, "/api/mcp/connections/" + fx.connID + "/pin-events", ""},
	} {
		if rec := fx.req(rt.method, rt.path, fx.token("agent", ""), rt.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with an agent token = %d, want 403", rt.method, rt.path, rec.Code)
		}
		if rec := fx.req(rt.method, rt.path, "", rt.body); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no token = %d, want 401", rt.method, rt.path, rec.Code)
		}
	}
}

func TestToolChangesListCountAndGet(t *testing.T) {
	fx := newTCFx(t)
	c := fx.drift("v2")
	rec := fx.req(http.MethodGet, "/api/tool-changes", fx.human(), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), c.ID) || !strings.Contains(rec.Body.String(), `"pending_count":1`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	rec = fx.req(http.MethodGet, "/api/tool-changes?scope=open&session="+fx.sessionID, fx.human(), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), c.ID) {
		t.Fatalf("list by session: %d %s", rec.Code, rec.Body.String())
	}
	rec = fx.req(http.MethodGet, "/api/proposals/count", fx.human(), "")
	if !strings.Contains(rec.Body.String(), `"tool_changes_pending":1`) || !strings.Contains(rec.Body.String(), `"pending_count":0`) {
		t.Fatalf("count: %s", rec.Body.String())
	}
	rec = fx.req(http.MethodGet, "/api/tool-changes/"+c.ID, fx.human(), "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"old_definition":{`) || !strings.Contains(body, `"new_definition":{`) || !strings.Contains(body, "v2") || !strings.Contains(body, "echoes") {
		t.Fatalf("get: %d %s", rec.Code, body)
	}
}

func TestAcceptRepinsWritesEventClosesRowAndRefreshes(t *testing.T) {
	fx := newTCFx(t)
	c := fx.drift("v2")
	for len(fx.dc.send) > 0 {
		<-fx.dc.send
	}
	rec := fx.req(http.MethodPost, "/api/tool-changes/"+c.ID+"/accept", fx.human(), `{"mode":"allow"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	g := fx.grant()
	if e := g.Tools["echo"]; e.Hash != fx.up.hash("echo") || e.IsListed() {
		t.Fatalf("grant after accept: %+v", e)
	}
	evs, _ := db.ListPinEventsForSession(context.Background(), fx.pool, mcpAcct, fx.sessionID, 10)
	if len(evs) != 1 || evs[0].Action != db.PinRepinned || evs[0].Actor != mcpAcct {
		t.Fatalf("events = %+v", evs)
	}
	row, _ := db.GetOwnedToolChange(context.Background(), fx.pool, mcpAcct, c.ID)
	if row.State != db.ToolChangeAccepted {
		t.Fatalf("row state = %s", row.State)
	}
	select {
	case raw := <-fx.dc.send:
		if !strings.Contains(string(raw), `"refresh_session_tools"`) || !strings.Contains(string(raw), fx.sessionID) {
			t.Fatalf("daemon got %s", raw)
		}
	default:
		t.Fatal("no refresh sent to the daemon")
	}
	// Idempotent.
	if rec := fx.req(http.MethodPost, "/api/tool-changes/"+c.ID+"/accept", fx.human(), `{"mode":"allow"}`); rec.Code != http.StatusOK {
		t.Fatalf("second accept: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAcceptMayTightenNeverWidenAndNeedsAMode(t *testing.T) {
	fx := newTCFx(t)
	c := fx.drift("v2")
	if rec := fx.req(http.MethodPost, "/api/tool-changes/"+c.ID+"/accept", fx.human(), `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no mode: %d", rec.Code)
	}
	if rec := fx.req(http.MethodPost, "/api/tool-changes/"+c.ID+"/accept", fx.human(), `{"mode":"propose"}`); rec.Code != http.StatusOK {
		t.Fatalf("tighten: %d %s", rec.Code, rec.Body.String())
	}
	if fx.grant().Tools["echo"].Mode != "propose" {
		t.Fatal("accept did not tighten the mode")
	}
	// A new-tool row recorded as propose cannot be accepted as allow.
	fx.up.mu.Lock()
	fx.up.tools = append(fx.up.tools, mcpTestTool{Name: "extra", Description: "new", Schema: `{"type":"object"}`})
	fx.up.mu.Unlock()
	fx.storeDef("extra")
	out, _ := db.RecordToolChange(context.Background(), fx.pool, db.ToolChange{AccountID: mcpAcct, SessionID: fx.sessionID, ConnectionID: fx.connID, ConnectionName: mcpConn,
		Tool: "extra", Kind: db.ToolChangeNew, NewHash: fx.up.hash("extra"), Mode: "propose"}, db.ToolChangeLimits{})
	if rec := fx.req(http.MethodPost, "/api/tool-changes/"+out.ID+"/accept", fx.human(), `{"mode":"allow"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("widen: %d %s", rec.Code, rec.Body.String())
	}
	if rec := fx.req(http.MethodPost, "/api/tool-changes/"+out.ID+"/accept", fx.human(), `{"mode":"propose"}`); rec.Code != http.StatusOK {
		t.Fatalf("grant new: %d %s", rec.Code, rec.Body.String())
	}
	if e, ok := fx.grant().Tools["extra"]; !ok || e.Mode != "propose" || e.IsListed() {
		t.Fatalf("new tool after accept: %+v %v", e, ok)
	}
}

// A changed row recorded as propose cannot be accepted as allow either.
func TestAcceptRefusesToWidenAChangedRow(t *testing.T) {
	fx2 := newTCFx(t)
	old := fx2.up.hash("echo")
	fx2.storeDef("echo")
	fx2.up.setDescription("echo", "v2")
	fx2.storeDef("echo")
	out2, err := db.RecordToolChange(context.Background(), fx2.pool, db.ToolChange{AccountID: mcpAcct, SessionID: fx2.sessionID, ConnectionID: fx2.connID, ConnectionName: mcpConn,
		Tool: "echo", Kind: db.ToolChangeChanged, OldHash: old, NewHash: fx2.up.hash("echo"), Mode: "propose"}, db.ToolChangeLimits{})
	if err != nil || !out2.Inserted {
		t.Fatalf("record: %+v %v", out2, err)
	}
	if rec := fx2.req(http.MethodPost, "/api/tool-changes/"+out2.ID+"/accept", fx2.human(), `{"mode":"allow"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("widen a changed row: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAcceptRefusesAStaleDefinition(t *testing.T) {
	fx := newTCFx(t)
	c := fx.drift("v2")
	fx.up.setDescription("echo", "v3") // changed again since the row was recorded
	rec := fx.req(http.MethodPost, "/api/tool-changes/"+c.ID+"/accept", fx.human(), `{"mode":"allow"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "changed again") {
		t.Fatalf("stale: %d %s", rec.Code, rec.Body.String())
	}
}

// A definition that cannot be reviewed.
func TestAcceptRefusesAnUnreviewableDefinition(t *testing.T) {
	fx2 := newTCFx(t)
	c2 := fx2.drift("reads \u202E things")
	if _, err := fx2.pool.Exec(context.Background(), `UPDATE mcp_tool_definitions SET unsafe = 'hidden_chars' WHERE hash = $1`, c2.NewHash); err != nil {
		t.Fatal(err)
	}
	rec := fx2.req(http.MethodPost, "/api/tool-changes/"+c2.ID+"/accept", fx2.human(), `{"mode":"allow"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "cannot be reviewed") {
		t.Fatalf("unsafe: %d %s", rec.Code, rec.Body.String())
	}
}

// Review Focus 5: the session ended.
func TestAcceptAfterTheSessionEndedIs410AndExpiresTheRow(t *testing.T) {
	fx := newTCFx(t)
	c := fx.drift("v2")
	if _, err := db.DeleteMCPGrantsForSession(context.Background(), fx.pool, fx.sessionID); err != nil {
		t.Fatal(err)
	}
	rec := fx.req(http.MethodPost, "/api/tool-changes/"+c.ID+"/accept", fx.human(), `{"mode":"allow"}`)
	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	row, _ := db.GetOwnedToolChange(context.Background(), fx.pool, mcpAcct, c.ID)
	if row.State != db.ToolChangeExpired {
		t.Fatalf("row state = %s", row.State)
	}
	evs, _ := db.ListPinEventsForSession(context.Background(), fx.pool, mcpAcct, fx.sessionID, 10)
	if len(evs) != 1 || evs[0].Action != db.PinExpired || evs[0].Actor != "system" {
		t.Fatalf("events = %+v", evs)
	}
}

func TestDeclineClosesTheRowAndWritesAnEvent(t *testing.T) {
	fx := newTCFx(t)
	c := fx.drift("v2")
	rec := fx.req(http.MethodPost, "/api/tool-changes/"+c.ID+"/decline", fx.human(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("decline: %d %s", rec.Code, rec.Body.String())
	}
	evs, _ := db.ListPinEventsForSession(context.Background(), fx.pool, mcpAcct, fx.sessionID, 10)
	if len(evs) != 1 || evs[0].Action != db.PinDeclined {
		t.Fatalf("events = %+v", evs)
	}
	if fx.grant().Tools["echo"].Hash == fx.up.hash("echo") {
		t.Fatal("decline changed the grant")
	}
	rec = fx.req(http.MethodGet, "/api/sessions/"+fx.sessionID+"/pin-events", fx.human(), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"action":"declined"`) {
		t.Fatalf("session events: %d %s", rec.Code, rec.Body.String())
	}
	rec = fx.req(http.MethodGet, "/api/mcp/connections/"+fx.connID+"/pin-events", fx.human(), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"action":"declined"`) {
		t.Fatalf("connection events: %d %s", rec.Code, rec.Body.String())
	}
}

// The grant already pins new_hash (a resume carried it) as allow; accepting as propose still tightens.
func TestAcceptLostCASStillTightens(t *testing.T) {
	fx := newTCFx(t)
	ctx := context.Background()
	c := fx.drift("v2")
	if ok, err := db.RepinGrantTool(ctx, fx.pool, fx.sessionID, fx.connID, "echo", c.OldHash, c.NewHash, ""); err != nil || !ok {
		t.Fatalf("repin: %v %v", ok, err)
	}
	if ok, err := db.MarkListed(ctx, fx.pool, fx.sessionID, fx.connID, "echo", c.NewHash); err != nil || !ok {
		t.Fatalf("mark listed: %v %v", ok, err)
	}
	rec := fx.req(http.MethodPost, "/api/tool-changes/"+c.ID+"/accept", fx.human(), `{"mode":"propose"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	if e := fx.grant().Tools["echo"]; e.Hash != c.NewHash || e.Mode != "propose" || e.IsListed() {
		t.Fatalf("grant after accept: %+v", e)
	}
	evs, _ := db.ListPinEventsForSession(ctx, fx.pool, mcpAcct, fx.sessionID, 10)
	if len(evs) != 1 || evs[0].Action != db.PinRepinned || evs[0].Mode != "propose" {
		t.Fatalf("events = %+v", evs)
	}
}

func TestAcceptRefusesARemovedRow(t *testing.T) {
	fx := newTCFx(t)
	out, err := db.RecordToolChange(context.Background(), fx.pool, db.ToolChange{AccountID: mcpAcct, SessionID: fx.sessionID, ConnectionID: fx.connID, ConnectionName: mcpConn,
		Tool: "echo", Kind: db.ToolChangeRemoved, OldHash: fx.up.hash("echo"), Mode: "allow"}, db.ToolChangeLimits{})
	if err != nil || !out.Inserted {
		t.Fatalf("record: %+v %v", out, err)
	}
	if rec := fx.req(http.MethodPost, "/api/tool-changes/"+out.ID+"/accept", fx.human(), `{"mode":"allow"}`); rec.Code != http.StatusConflict {
		t.Fatalf("removed: %d %s", rec.Code, rec.Body.String())
	}
}
