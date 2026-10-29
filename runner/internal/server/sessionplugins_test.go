package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type pluginCoreFixture struct {
	mu         sync.Mutex
	adds       int
	deny       bool
	allowed    []string
	policyHook func()
}

func (f *pluginCoreFixture) Policy(context.Context, string) (sessionPluginCatalog, error) {
	if f.policyHook != nil {
		f.policyHook()
	}
	if f.deny {
		return sessionPluginCatalog{Allowed: []string{}, Plugins: []pluginspec.Entry{}}, nil
	}
	allowed := f.allowed
	if allowed == nil {
		allowed = []string{"anthropics/claude-plugins-official"}
	}
	return sessionPluginCatalog{Allowed: allowed, Plugins: []pluginspec.Entry{}}, nil
}
func (f *pluginCoreFixture) Catalog(ctx context.Context, _ string, _ coreProof, engine string) (sessionPluginCatalog, error) {
	return f.Policy(ctx, engine)
}

func (f *pluginCoreFixture) Add(context.Context, string, coreProof, string, pluginspec.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds++
	return nil
}
func pluginFx(t *testing.T) (*topicFx, *pluginCoreFixture) {
	t.Helper()
	fx := newTopicFx(t)
	fx.api.RegisterWorklistRoutes(fx.mux)
	fx.api.RegisterProposalRoutes(fx.mux)
	f := &pluginCoreFixture{}
	fx.api.sessionPlugins = f
	fx.api.pluginAllow, _ = pluginspec.ParseAllowlist("anthropics/claude-plugins-official")
	return fx, f
}
func pluginReq(fx *topicFx, method, path, token string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	fx.mux.ServeHTTP(w, r)
	return w
}
func queuePlugin(t *testing.T, fx *topicFx) *db.Proposal {
	t.Helper()
	w := pluginReq(fx, "POST", "/api/sessions/"+fx.sess+"/plugin-requests", fx.sessTok, map[string]string{"name": "release-notes", "reason": "Read the release notes."})
	if w.Code != 202 {
		t.Fatalf("queue %d %s", w.Code, w.Body.String())
	}
	var v proposalView
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	p, err := db.GetOwnedProposal(context.Background(), fx.pool, topicOwner, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestSessionPluginsAgentNeverInstallsAndDeclineExpiryDoNothing(t *testing.T) {
	fx, f := pluginFx(t)
	p := queuePlugin(t, fx)
	if f.adds != 0 || p.State != db.ProposalPending {
		t.Fatal("request caused installation")
	}
	owner := fx.token(topicOwner, "human")
	w := pluginReq(fx, "POST", "/api/proposals/"+p.ID+"/reject", owner, map[string]string{})
	if w.Code != 200 {
		t.Fatalf("reject %d", w.Code)
	}
	p = queuePlugin(t, fx)
	_, err := fx.pool.Exec(context.Background(), `UPDATE mcp_proposals SET created_at=now()-interval '8 days' WHERE id=$1`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	w = pluginReq(fx, "POST", "/api/proposals/"+p.ID+"/approve", owner, map[string]string{"scope": "always", "expected_arguments_raw": string(p.Arguments)})
	if w.Code != 409 || f.adds != 0 {
		t.Fatalf("expired %d adds %d", w.Code, f.adds)
	}
}
func TestSessionPluginsApprovalExactOwnerAndConcurrentAlwaysAdd(t *testing.T) {
	fx, f := pluginFx(t)
	p := queuePlugin(t, fx)
	url := "/api/proposals/" + p.ID + "/approve"
	body := map[string]string{"scope": "always", "expected_arguments_raw": string(p.Arguments)}
	if w := pluginReq(fx, "POST", url, fx.token(topicOther, "human"), body); w.Code != 404 {
		t.Fatalf("other %d", w.Code)
	}
	if w := pluginReq(fx, "POST", url, fx.sessTok, body); w.Code != 401 && w.Code != 403 {
		t.Fatalf("agent approve %d", w.Code)
	}
	owner := fx.token(topicOwner, "human")
	stale := map[string]string{"scope": "always", "expected_arguments_raw": "{}"}
	if w := pluginReq(fx, "POST", url, owner, stale); w.Code != 409 {
		t.Fatalf("stale %d", w.Code)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			w := pluginReq(fx, "POST", url, owner, body)
			if w.Code != 200 && w.Code != 409 {
				t.Errorf("approve %d %s", w.Code, w.Body.String())
			}
		})
	}
	wg.Wait()
	if f.adds != 1 {
		t.Fatalf("adds %d", f.adds)
	}
	got, _ := db.GetOwnedProposal(context.Background(), fx.pool, topicOwner, p.ID)
	if got.State != db.ProposalDone || !bytes.Contains(got.Result, []byte("new_sessions")) {
		t.Fatalf("result %s %s", got.State, got.Result)
	}
}
func TestSessionPluginsPolicyOwnershipAndUnsupportedSession(t *testing.T) {
	fx, f := pluginFx(t)
	path := "/api/sessions/" + fx.sess + "/plugin-requests"
	for _, body := range []any{map[string]string{"name": "evil@evil/plugins", "reason": "x"}, map[string]string{"name": "../bad", "reason": "x"}, map[string]string{"name": "release-notes", "reason": "x", "account_id": topicOther}} {
		w := pluginReq(fx, "POST", path, fx.sessTok, body)
		if w.Code != 400 && w.Code != 403 {
			t.Fatalf("policy %d", w.Code)
		}
	}
	if w := pluginReq(fx, "POST", path, fx.otherTok, map[string]string{"name": "release-notes", "reason": "x"}); w.Code != 403 {
		t.Fatalf("foreign %d", w.Code)
	}
	p := queuePlugin(t, fx)
	w := pluginReq(fx, "POST", "/api/proposals/"+p.ID+"/approve", fx.token(topicOwner, "human"), map[string]string{"scope": "session", "expected_arguments_raw": string(p.Arguments)})
	if w.Code != 409 || f.adds != 0 {
		t.Fatalf("unsupported %d", w.Code)
	}
	for _, tok := range []string{fx.token(topicOther, "human"), fx.token(topicOwner, "agent")} {
		w := pluginReq(fx, "GET", "/api/sessions/"+fx.sess+"/plugins", tok, nil)
		if w.Code != 404 && w.Code != 403 {
			t.Fatalf("options %d", w.Code)
		}
	}
}

func livePluginDaemon(fx *topicFx) *DaemonConn {
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 32)}
	dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	dc.SetSessionPluginEngines([]string{"claude"})
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	return dc
}
func TestSessionPluginsSessionOnlyDurableReplayAndReplyFences(t *testing.T) {
	fx, f := pluginFx(t)
	dc := livePluginDaemon(fx)
	p := queuePlugin(t, fx)
	owner := fx.token(topicOwner, "human")
	w := pluginReq(fx, "POST", "/api/proposals/"+p.ID+"/approve", owner, map[string]string{"scope": "session", "expected_arguments_raw": string(p.Arguments)})
	if w.Code != 200 || f.adds != 0 {
		t.Fatalf("session approve %d adds%d", w.Code, f.adds)
	}
	var command protocol.ApplySessionPlugins
	select {
	case raw := <-dc.send:
		if json.Unmarshal(raw, &command) != nil || command.RequestID != p.ID || command.SessionID != fx.sess || len(command.Plugins) != 1 {
			t.Fatalf("wrong command %s", raw)
		}
	default:
		t.Fatal("approved request not dispatched")
	}
	row, _ := db.GetOwnedProposal(context.Background(), fx.pool, topicOwner, p.ID)
	if row.State != db.ProposalDone || !bytes.Contains(row.Result, []byte("queued")) {
		t.Fatalf("approval notdurable %s", row.Result)
	}
	replaySessionPlugins(context.Background(), fx.api.hub, fx.pool, dc)
	select {
	case <-dc.send:
	default:
		t.Fatal("queued approval not replayed")
	}
	old := dc
	dc = &DaemonConn{ID: old.ID, send: make(chan []byte, 8)}
	dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	dc.SetSessionPluginEngines([]string{"claude"})
	fx.api.hub.Register(dc)
	reply := protocol.SessionPluginsResult{RequestID: p.ID, SessionID: fx.sess, State: "applied", Error: "private provider detail"}
	handleSessionPluginsResult(context.Background(), fx.api.hub, fx.pool, old, reply)
	row, _ = db.GetOwnedProposal(context.Background(), fx.pool, topicOwner, p.ID)
	if !bytes.Contains(row.Result, []byte("queued")) {
		t.Fatal("superseded socket changedresult")
	}
	wrong := reply
	wrong.SessionID = fx.privSess
	handleSessionPluginsResult(context.Background(), fx.api.hub, fx.pool, dc, wrong)
	handleSessionPluginsResult(context.Background(), fx.api.hub, fx.pool, dc, reply)
	row, _ = db.GetOwnedProposal(context.Background(), fx.pool, topicOwner, p.ID)
	if !bytes.Contains(row.Result, []byte("applied")) || bytes.Contains(row.Result, []byte("private provider")) {
		t.Fatalf("bad terminalreply %s", row.Result)
	}
	reply.State = "queued"
	handleSessionPluginsResult(context.Background(), fx.api.hub, fx.pool, dc, reply)
	reply.State = "failed"
	handleSessionPluginsResult(context.Background(), fx.api.hub, fx.pool, dc, reply)
	row, _ = db.GetOwnedProposal(context.Background(), fx.pool, topicOwner, p.ID)
	if !bytes.Contains(row.Result, []byte("applied")) {
		t.Fatal("late result overwrote applied")
	}
	replaySessionPlugins(context.Background(), fx.api.hub, fx.pool, dc)
	select {
	case <-dc.send:
		t.Fatal("terminal approval replayed")
	default:
	}
}
func TestSessionPluginsRestrictedAndAlwaysOnlyDoNotWiden(t *testing.T) {
	fx, f := pluginFx(t)
	dc := livePluginDaemon(fx)
	if err := db.SetSessionRestrictTools(context.Background(), fx.pool, fx.sess); err != nil {
		t.Fatal(err)
	}
	p := queuePlugin(t, fx)
	owner := fx.token(topicOwner, "human")
	path := "/api/proposals/" + p.ID + "/approve"
	w := pluginReq(fx, "POST", path, owner, map[string]string{"scope": "session", "expected_arguments_raw": string(p.Arguments)})
	if w.Code != 409 {
		t.Fatalf("restricted sessionapply %d", w.Code)
	}
	w = pluginReq(fx, "POST", path, owner, map[string]string{"scope": "always", "expected_arguments_raw": string(p.Arguments)})
	if w.Code != 200 || f.adds != 1 {
		t.Fatalf("always future %d adds%d", w.Code, f.adds)
	}
	select {
	case <-dc.send:
		t.Fatal("restricted plugin dispatched")
	default:
	}
}

func TestSessionPluginsAbandonedRequestsCannotStarveLiveReplay(t *testing.T) {
	fx, _ := pluginFx(t)
	dc := livePluginDaemon(fx)
	ctx := context.Background()
	if err := db.UpdateSessionStatus(ctx, fx.pool, fx.ended, "running", nil); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(sessionPluginArguments{Entry: pluginspec.Entry{Marketplace: "anthropics/claude-plugins-official", Plugin: "release-notes"}, Engine: "claude"})
	for range 20 {
		p, err := db.InsertProposal(ctx, fx.pool, db.Proposal{AccountID: topicOwner, SessionID: fx.ended, ConnectionID: db.SessionPluginConnectionID, ConnectionName: "session-plugin", Tool: "request_plugin", ToolHash: "session-plugin-v1", URLSnapshot: "session-plugins:" + dc.ID, Arguments: args}, 50)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.BeginSessionPluginExecution(ctx, fx.pool, topicOwner, p.ID, string(p.Arguments)); err != nil {
			t.Fatal(err)
		}
		if _, err = db.FinishProposal(ctx, fx.pool, p.ID, db.ProposalDone, json.RawMessage(`{"state":"queued"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpdateSessionStatus(ctx, fx.pool, fx.ended, "stopped", nil); err != nil {
		t.Fatal(err)
	}
	live := queuePlugin(t, fx)
	if _, err := db.BeginSessionPluginExecution(ctx, fx.pool, topicOwner, live.ID, string(live.Arguments)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishProposal(ctx, fx.pool, live.ID, db.ProposalDone, json.RawMessage(`{"state":"queued"}`)); err != nil {
		t.Fatal(err)
	}
	queued, err := db.QueuedSessionPlugins(ctx, fx.pool, dc.ID)
	if err != nil || len(queued) != 1 || queued[0].ID != live.ID {
		t.Fatalf("live replay starved: count=%d err=%v", len(queued), err)
	}
	replaySessionPlugins(ctx, fx.api.hub, fx.pool, dc)
	select {
	case raw := <-dc.send:
		var command protocol.ApplySessionPlugins
		_ = json.Unmarshal(raw, &command)
		if command.RequestID != live.ID {
			t.Fatal("wrong replay")
		}
	default:
		t.Fatal("live request notdispatched")
	}
}

func TestSessionPluginsPolicyRefusalAndLifecycleLeaveNoDraft(t *testing.T) {
	for _, mode := range []string{"policy", "ended", "owner"} {
		t.Run(mode, func(t *testing.T) {
			fx, _ := pluginFx(t)
			f := fx.api.sessionPlugins.(*pluginCoreFixture)
			if mode == "policy" {
				f.deny = true
			} else {
				f.policyHook = func() {
					if mode == "ended" {
						if err := db.UpdateSessionStatus(context.Background(), fx.pool, fx.sess, "stopped", nil); err != nil {
							t.Error(err)
						}
					} else {
						if err := db.SetSessionSpawningAccount(context.Background(), fx.pool, fx.sess, topicOther); err != nil {
							t.Error(err)
						}
					}
				}
			}
			w := pluginReq(fx, "POST", "/api/sessions/"+fx.sess+"/plugin-requests", fx.sessTok, map[string]string{"name": "release-notes", "reason": "x"})
			if w.Code != 403 && w.Code != 409 {
				t.Fatalf("agent refusal %d", w.Code)
			}
			rows, err := db.ListSessionPluginProposals(context.Background(), fx.pool, topicOwner, fx.sess)
			if err != nil || len(rows) != 0 {
				t.Fatalf("refusal left drafts %d %v", len(rows), err)
			}
			if mode == "policy" {
				w = pluginReq(fx, "POST", "/api/sessions/"+fx.sess+"/plugins", fx.token(topicOwner, "human"), map[string]string{"marketplace": "anthropics/claude-plugins-official", "plugin": "release-notes", "scope": "always"})
				if w.Code != 403 {
					t.Fatalf("human policy %d", w.Code)
				}
				rows, err = db.ListSessionPluginProposals(context.Background(), fx.pool, topicOwner, fx.sess)
				if err != nil || len(rows) != 0 {
					t.Fatal("human refusal leftdraft")
				}
			}
		})
	}
}

func TestSessionPluginsBuiltinRequestWorksWithoutShellAndCannotInstall(t *testing.T) {
	fx, f := pluginFx(t)
	if err := db.SetSessionRestrictTools(context.Background(), fx.pool, fx.sess); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "blerg_request_plugin", "arguments": map[string]string{"name": "release-notes", "reason": "Needed for this task."}}}
	w := pluginReq(fx, "POST", "/api/sessions/"+fx.sess+"/filing-tools", fx.sessTok, request)
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte(`"isError":true`)) {
		t.Fatalf("builtin request %d %s", w.Code, w.Body.String())
	}
	rows, err := db.ListSessionPluginProposals(context.Background(), fx.pool, topicOwner, fx.sess)
	if err != nil || len(rows) != 1 || rows[0].State != db.ProposalPending || f.adds != 0 {
		t.Fatalf("builtin did not only queue: count%d err%v adds%d", len(rows), err, f.adds)
	}
	w = pluginReq(fx, "POST", "/api/sessions/"+fx.sess+"/filing-tools", fx.otherTok, request)
	if w.Code != 403 {
		t.Fatalf("foreign builtin %d", w.Code)
	}
}

func TestSessionPluginsCodexLiveAndStartupOnly(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "startup-only", true: "live"}[live], func(t *testing.T) {
			fx, f := pluginFx(t)
			if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET engine='codex' WHERE id=$1`, fx.sess); err != nil {
				t.Fatal(err)
			}
			dc := livePluginDaemon(fx)
			if live {
				dc.SetSessionPluginEngines([]string{"claude", "codex"})
			}
			owner := fx.token(topicOwner, "human")
			options := pluginReq(fx, "GET", "/api/sessions/"+fx.sess+"/plugins", owner, nil)
			var settings struct {
				SessionApply bool `json:"session_apply"`
				Reason       string
			}
			if options.Code != 200 || json.Unmarshal(options.Body.Bytes(), &settings) != nil || settings.SessionApply != live {
				t.Fatalf("options %d %s", options.Code, options.Body.String())
			}
			if !live && !bytes.Contains([]byte(settings.Reason), []byte("loads plugins when a session starts")) {
				t.Fatalf("unclear startup-only reason: %q", settings.Reason)
			}
			body := map[string]string{"marketplace": "anthropics/claude-plugins-official", "plugin": "release-notes", "scope": "session"}
			if !live {
				if refused := pluginReq(fx, "POST", "/api/sessions/"+fx.sess+"/plugins", owner, body); refused.Code != 409 {
					t.Fatal("session-only should be refused")
				}
				body["scope"] = "always"
			}
			add := pluginReq(fx, "POST", "/api/sessions/"+fx.sess+"/plugins", owner, body)
			if add.Code != 202 {
				t.Fatalf("add %d %s", add.Code, add.Body.String())
			}
			if !live {
				if f.adds != 1 || !bytes.Contains(add.Body.Bytes(), []byte("added for your next session")) {
					t.Fatal("always-on did not save with clear next-session result")
				}
				select {
				case <-dc.send:
					t.Fatal("startup-only request dispatched")
				default:
				}
				return
			}
			var command protocol.ApplySessionPlugins
			if json.Unmarshal(<-dc.send, &command) != nil || len(command.Plugins) != 1 {
				t.Fatal("Codex addition not dispatched")
			}
			// Both Core and the local holder revoke the installed marketplace.
			fx.api.marketplacePolicy = &pluginspec.LivePolicy{}
			if err := fx.api.marketplacePolicy.Apply(pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{}), time.Second); err != nil {
				t.Fatal(err)
			}
			f.deny = true
			body["action"] = "remove"
			remove := pluginReq(fx, "POST", "/api/sessions/"+fx.sess+"/plugins", owner, body)
			if remove.Code != 202 {
				t.Fatalf("remove %d %s", remove.Code, remove.Body.String())
			}
			if json.Unmarshal(<-dc.send, &command) != nil || len(command.Remove) != 1 {
				t.Fatal("Codex removal not dispatched")
			}
			body["plugin"] = "../invalid"
			if invalid := pluginReq(fx, "POST", "/api/sessions/"+fx.sess+"/plugins", owner, body); invalid.Code != 403 {
				t.Fatal("invalid removal accepted")
			}
			body["plugin"] = "release-notes"
			body["scope"] = "always"
			if refused := pluginReq(fx, "POST", "/api/sessions/"+fx.sess+"/plugins", owner, body); refused.Code != 403 || f.adds != 0 {
				t.Fatal("removal changed account settings")
			}
		})
	}
}

func TestSessionPluginsFailedReplacementTellsOwnerAndStaysFailed(t *testing.T) {
	fx, _ := pluginFx(t)
	dc := livePluginDaemon(fx)
	p := queuePlugin(t, fx)
	owner := fx.token(topicOwner, "human")
	w := pluginReq(fx, "POST", "/api/proposals/"+p.ID+"/approve", owner, map[string]string{"scope": "session", "expected_arguments_raw": string(p.Arguments)})
	if w.Code != 200 {
		t.Fatalf("approve: %d", w.Code)
	}
	<-dc.send
	handleSessionPluginsResult(context.Background(), fx.api.hub, fx.pool, dc, protocol.SessionPluginsResult{RequestID: p.ID, SessionID: fx.sess, State: "failed", Error: "private plugin diagnostic"})
	row, err := db.GetOwnedProposal(context.Background(), fx.pool, topicOwner, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(row.Result, []byte(`"state": "failed"`)) || !bytes.Contains(row.Result, []byte("Your session is continuing with its previous plugins.")) || bytes.Contains(row.Result, []byte("private plugin diagnostic")) {
		t.Fatalf("unclear or unsafe failure: %s", row.Result)
	}
	handleSessionPluginsResult(context.Background(), fx.api.hub, fx.pool, dc, protocol.SessionPluginsResult{RequestID: p.ID, SessionID: fx.sess, State: "applied"})
	after, err := db.GetOwnedProposal(context.Background(), fx.pool, topicOwner, p.ID)
	if err != nil || !bytes.Equal(after.Result, row.Result) {
		t.Fatal("late applied reply overwrote failed replacement")
	}
	replaySessionPlugins(context.Background(), fx.api.hub, fx.pool, dc)
	select {
	case <-dc.send:
		t.Fatal("failed replacement replayed")
	default:
	}
}

func TestSessionPluginsNativeCodexApprovalAndPolicy(t *testing.T) {
	for _, scope := range []string{"session", "always"} {
		t.Run(scope, func(t *testing.T) {
			fx, f := pluginFx(t)
			if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET engine='codex' WHERE id=$1`, fx.sess); err != nil {
				t.Fatal(err)
			}
			dc := livePluginDaemon(fx)
			dc.SetSessionPluginEngines([]string{"codex"})
			fx.api.pluginAllow, _ = pluginspec.ParseAllowlist("codex:openai-curated")
			f.allowed = []string{"codex:openai-curated"}
			body := map[string]string{"marketplace": "codex:openai-curated", "plugin": "selected", "scope": scope}
			owner := fx.token(topicOwner, "human")
			// Neither agent-token calls nor a Core policy refusal may record approval.
			if w := pluginReq(fx, "POST", "/api/sessions/"+fx.sess+"/plugins", fx.sessTok, body); w.Code == 202 {
				t.Fatal("agent installed native plugin")
			}
			f.deny = true
			if w := pluginReq(fx, "POST", "/api/sessions/"+fx.sess+"/plugins", owner, body); w.Code != 403 {
				t.Fatalf("native policy refusal %d", w.Code)
			}
			f.deny = false
			w := pluginReq(fx, "POST", "/api/sessions/"+fx.sess+"/plugins", owner, body)
			if w.Code != 202 {
				t.Fatalf("native approval %d %s", w.Code, w.Body.String())
			}
			var command protocol.ApplySessionPlugins
			select {
			case raw := <-dc.send:
				if json.Unmarshal(raw, &command) != nil || len(command.Plugins) != 1 || command.Plugins[0].Marketplace != "codex:openai-curated" {
					t.Fatal("native command identity changed")
				}
			default:
				t.Fatal("approved native install not dispatched")
			}
			wantAdds := 0
			if scope == "always" {
				wantAdds = 1
			}
			if f.adds != wantAdds {
				t.Fatalf("always adds %d", f.adds)
			}
			p, err := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, command.RequestID)
			if err != nil || p.State != db.ProposalDone || !bytes.Contains(p.Result, []byte("queued")) {
				t.Fatal("native approval not durably audited")
			}
			rows, err := db.ListSessionPluginProposals(t.Context(), fx.pool, topicOwner, fx.sess)
			if err != nil || len(rows) != 1 {
				t.Fatal("refused requests left approval records")
			}
		})
	}
}
