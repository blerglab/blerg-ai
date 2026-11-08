package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

func TestPersonChannelLaunch(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	status, body, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/channels", fx.human("owner-1"), map[string]any{"title": "Design", "engine": "codex", "private": true}, nil)
	if status != http.StatusAccepted {
		t.Fatalf("status %d: %v", status, body)
	}
	id, _ := body["session_id"].(string)
	row, err := db.GetSession(t.Context(), fx.pool, id)
	if err != nil || row == nil {
		t.Fatalf("session: %v %v", row, err)
	}
	if derefOrEmpty(row.ParentSessionID) != fx.parent || row.StartedByKind != "" || row.Interaction != "interactive" {
		t.Fatalf("not a person channel: %+v", row)
	}
	parent, _ := db.GetSession(t.Context(), fx.pool, fx.parent)
	if canSee(fx.api.delegatePrincipalFor(t.Context(), parent, ""), row) {
		t.Fatal("Main can read a private channel")
	}
}

func TestChannelLaunchOwnerOnly(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	for _, token := range []string{fx.human("other"), fx.parentTok, runnerTestKey} {
		status, body, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/channels", token, map[string]any{"title": "Design", "engine": "codex"}, nil)
		if status < 400 {
			t.Fatalf("unauthorized start: %d %v", status, body)
		}
	}
}

func TestChannelLaunchDoesNotInheritDelegationGrants(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	tx, err := fx.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.InsertDelegationGrantTx(t.Context(), tx, db.DelegationGrant{ParentSessionID: fx.parent, AccountID: "owner-1", Engine: "codex", Runtime: "docker", DaemonID: fx.dc.ID, Repo: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, inherit := range []bool{true, false} {
		status, body, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/channels", fx.human("owner-1"), map[string]any{"title": "Review", "engine": "hermes", "inherit_permissions": inherit}, nil)
		if status != 202 {
			t.Fatalf("status %d: %v", status, body)
		}
		spawn := fx.spawn()
		if spawn.Engine != "hermes" || spawn.InitialPrompt != "" || spawn.Interaction != "interactive" || spawn.WorkBranch == "" {
			t.Fatalf("spawn: %+v", spawn)
		}
		grants, err := db.ListLiveDelegationGrants(t.Context(), fx.pool, spawn.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(grants) != 0 {
			t.Fatalf("channel inherited worker grants: %+v", grants)
		}
	}
}

func TestPrivateChannelMainMetadataOnly(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	status, body, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/channels", fx.human("owner-1"), map[string]any{"title": "Design", "engine": "codex", "private": true}, nil)
	if status != 202 {
		t.Fatalf("start: %d %v", status, body)
	}
	id := body["session_id"].(string)
	spawn := fx.spawn()
	status, body, _ = fx.do("GET", "/api/sessions/"+fx.parent+"/children", fx.parentTok, nil, nil)
	if status != 200 || len(body["children"].([]any)) != 1 {
		t.Fatalf("metadata: %d %v", status, body)
	}
	for _, suffix := range []string{"", "/events", "/result", "/artifacts"} {
		status, body, _ = fx.do("GET", "/api/sessions/"+fx.parent+"/children/"+id+suffix, fx.parentTok, nil, nil)
		if status != 404 {
			t.Fatalf("content %s: %d %v", suffix, status, body)
		}
	}
	status, body, _ = fx.do("POST", "/api/sessions/"+fx.parent+"/categories", fx.parentTok, map[string]any{"action": "create", "name": "Research"}, nil)
	if status != 200 {
		t.Fatalf("category: %d %v", status, body)
	}
	category := body["categories"].([]any)[0].(map[string]any)["id"].(string)
	status, body, _ = fx.do("POST", "/api/sessions/"+fx.parent+"/categories", fx.parentTok, map[string]any{"action": "file", "id": category, "session": id}, nil)
	if status != 200 {
		t.Fatalf("file private channel: %d %v", status, body)
	}
	rec := privDo(fx.api.HandlePostMessages, "POST", "/api/messages", "", spawn.SessionToken, `{"kind":"ask","body":"Private question","no_wait":true}`)
	if rec.Code != 201 {
		t.Fatalf("question: %d %s", rec.Code, rec.Body.String())
	}
	var reply struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &reply)
	question, err := db.GetMessage(t.Context(), fx.pool, reply.ID)
	if err != nil || question == nil {
		t.Fatalf("question: %v", err)
	}
	row, _ := db.GetSession(t.Context(), fx.pool, id)
	if childQuestionPending(t.Context(), fx.pool, *question) || !questionCallsOwner(row, *question) {
		t.Fatal("channel question did not go to its person")
	}
	if !fx.api.noteChildTurn(t.Context(), id, childTurnFinished, "Private answer") {
		t.Fatal("channel notice was not consumed")
	}
}

func TestChannelLaunchWorkspaceSubset(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	fx.dc.DiscoveryVersion = 1
	fx.dc.WorkspaceCapabilities = protocol.WorkspaceCapabilities{ManifestVersion: 2, Sandbox: true}
	parent := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner-1", Runtime: "sandbox", LaunchGeneration: newUUID(), Root: protocol.WorkspaceRoot{Inode: 1}, Members: []protocol.WorkspaceMember{
		{ID: "a", Name: "App", Path: "app", SourceIdentity: "source-a", CommonDirectoryIdentity: "common-a", Destination: "source-a"},
		{ID: "b", Name: "Docs", Path: "docs", SourceIdentity: "source-b", CommonDirectoryIdentity: "common-b", Destination: "source-b"},
	}}
	parent.Digest = workspaceselection.Digest(parent)
	if err := db.InsertSessionWorkspace(t.Context(), fx.pool, fx.parent, parent); err != nil {
		t.Fatal(err)
	}
	done, joined := make(chan struct{}), make(chan struct{})
	spawns := make(chan protocol.SpawnSession, 2)
	go func() {
		defer close(joined)
		for {
			select {
			case <-done:
				return
			case raw := <-fx.dc.send:
				var header struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal(raw, &header)
				if header.Type == "discover_workspace" {
					var req protocol.DiscoverWorkspace
					_ = json.Unmarshal(raw, &req)
					fx.hub.deliverWorkspaceDiscovery(fx.dc, protocol.WorkspaceDiscoveryResult{Type: "workspace_discovery_result", RequestID: req.RequestID, OK: true})
				}
				if header.Type == "spawn_session" {
					var msg protocol.SpawnSession
					_ = json.Unmarshal(raw, &msg)
					spawns <- msg
				}
			}
		}
	}()
	defer func() { close(done); <-joined }()
	for _, selection := range [][]string{nil, {"b"}} {
		body := map[string]any{"title": "Review", "engine": "codex"}
		if selection != nil {
			body["repos"] = selection
		}
		status, result, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/channels", fx.human("owner-1"), body, nil)
		if status != 202 {
			t.Fatalf("start: %d %v", status, result)
		}
		msg := <-spawns
		if msg.Workspace == nil || msg.Workspace.LaunchGeneration == parent.LaunchGeneration {
			t.Fatal("no separate child workspace")
		}
		if selection == nil {
			if len(msg.Workspace.Members) != 2 {
				t.Fatal("default did not inherit all repositories")
			}
		} else if len(msg.Workspace.Members) != 1 || msg.Workspace.Members[0].ID != "b" {
			t.Fatalf("subset: %+v", msg.Workspace)
		}
	}
}

func TestChannelLaunchInheritsInteractiveConnections(t *testing.T) {
	fx := newMCPFx(t)
	rec := fx.do(fx.api.HandleLaunchSession, fx.human(), map[string]any{
		"no_repo": true, "runtime": "cluster", "kind": "agent", "title": "Main",
		"mcp":         echoSel(fx, "propose", fx.up.hash("echo")),
		"permissions": map[string]any{"mcp_tools": []map[string]any{{"connection_id": fx.connID, "tool": "echo"}}},
	})
	if rec.Code != 202 {
		t.Fatalf("parent: %d %s", rec.Code, rec.Body.String())
	}
	var parent struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &parent)
	rec = fx.do(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", parent.SessionID)
		fx.api.HandlePostChannel(w, r)
	}, fx.human(), map[string]any{"title": "Focused", "engine": "claude"})
	if rec.Code != 202 {
		t.Fatalf("channel: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		SessionID string   `json:"session_id"`
		Problems  []string `json:"permission_problems"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Problems) > 0 {
		t.Fatalf("permissions: %v", out.Problems)
	}
	row, err := db.GetSession(t.Context(), fx.pool, out.SessionID)
	if err != nil || row == nil || row.RestrictTools || !row.Private || row.StartedByKind != "" {
		t.Fatalf("interactive channel: %+v %v", row, err)
	}
	grants, err := db.ListMCPGrantsForSession(t.Context(), fx.pool, out.SessionID)
	if err != nil || len(grants) != 1 || grants[0].Tools["echo"].Mode != "propose" {
		t.Fatalf("connections: %+v %v", grants, err)
	}
	permissions, err := db.ListLiveProposalGrants(t.Context(), fx.pool, out.SessionID)
	if err != nil || len(permissions) != 1 || permissions[0].Tool != "echo" {
		t.Fatalf("permissions: %+v %v", permissions, err)
	}
}

func TestChannelLaunchIgnoresWorkerLimits(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	limit := fx.api.effectiveChildLimits(t.Context()).parallel
	for range limit {
		fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	}
	status, body, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/channels", fx.human("owner-1"), map[string]any{"title": "Another", "engine": "codex"}, nil)
	if status != http.StatusAccepted {
		t.Fatalf("admission: %d %v", status, body)
	}
	if count := fx.count(`SELECT count(*) FROM sessions WHERE parent_session_id=$1`, fx.parent); count != limit+1 {
		t.Fatalf("channel count: %d", count)
	}
}

func TestPublicChannelRefusesEveryMainControlRoute(t *testing.T) {
	fx := newDelegFx(t)
	child, token := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET delegation_via='person' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/events", "/result", "/artifacts"} {
		status, body, _ := fx.do("GET", "/api/sessions/"+fx.parent+"/children/"+child+suffix, fx.parentTok, nil, nil)
		if status != 403 {
			t.Errorf("Main must not read a person channel %s: %d %v", suffix, status, body)
		}
	}
	for _, suffix := range []string{"/message", "/questions/" + newUUID() + "/answer", "/questions/" + newUUID() + "/escalate", "/model", "/title", "/interrupt", "/stop", "/discard", "/uploads"} {
		status, body, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/children/"+child+suffix, fx.parentTok, map[string]any{}, nil)
		if status != 403 {
			t.Errorf("control %s: %d %v", suffix, status, body)
		}
	}
	status, body, _ := fx.postChild(child, token, claudeBody("worker"), "channel-worker")
	if status != 403 || !strings.Contains(asString(body["error"]), "Channels cannot start workers yet") {
		t.Errorf("channel delegation: %d %v", status, body)
	}
	if trustDirect(fx.row(child)) {
		t.Error("channel is trusted for delegation")
	}
	_, problem := fx.api.startChild(t.Context(), fx.row(child), childStart{}, "rule:test", "")
	if problem == nil || problem.Status != 403 || !strings.Contains(problem.Message, "Channels cannot start workers yet") {
		t.Errorf("internal start: %+v", problem)
	}
}

func TestChannelOptOutCannotWidenRestrictedTools(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.SetRestrictTools(true)
	fx.dc.SetCodexRestricted(true)
	fx.api.RegisterCategoryRoutes(fx.mux)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET restrict_tools=true WHERE id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	status, body, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/channels", fx.human("owner-1"), map[string]any{"title": "Restricted", "engine": "codex", "inherit_permissions": false}, nil)
	if status != 202 {
		t.Fatalf("start: %d %v", status, body)
	}
	if !fx.row(asString(body["session_id"])).RestrictTools {
		t.Fatal("opt out widened tool access")
	}
}

func TestChannelsDoNotConsumeMainWorkerLimits(t *testing.T) {
	t.Setenv("BLERG_RUNNER_MAX_PARALLEL_CHILDREN", "1")
	t.Setenv("BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR", "1")
	fx := newDelegFx(t)
	channel, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET delegation_via='person' WHERE id=$1`, channel); err != nil {
		t.Fatal(err)
	}
	fx.rule("claude", runnerRuntimeDocker, false)
	status, body, _ := fx.postChild(fx.parent, fx.parentTok, claudeBody("worker"), "worker-after-channel")
	if status != 201 {
		t.Fatalf("worker: %d %v", status, body)
	}
	fx.spawn()
}

func TestChannelCreateIdempotency(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	input := map[string]any{"title": "Design", "engine": "codex"}
	headers := map[string]string{"Idempotency-Key": "channel-create"}
	status, body, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/channels", fx.human("owner-1"), input, headers)
	if status != 202 {
		t.Fatalf("first: %d %v", status, body)
	}
	id := body["session_id"]
	fx.spawn()
	for range maxRunningChildrenPerAccount - 1 {
		fx.childOf(fx.parent)
	}
	status, body, _ = fx.do("POST", "/api/sessions/"+fx.parent+"/channels", fx.human("owner-1"), input, headers)
	if status != 202 || body["session_id"] != id {
		t.Fatalf("replay: %d %v want %v", status, body, id)
	}
	fx.noSpawn()
	input["private"] = true
	status, body, _ = fx.do("POST", "/api/sessions/"+fx.parent+"/channels", fx.human("owner-1"), input, headers)
	if status != 409 {
		t.Fatalf("changed privacy: %d %v", status, body)
	}
}

func TestPlanChannelHasNoWorkerNudges(t *testing.T) {
	fx := newDelegFx(t)
	ctx := t.Context()
	if _, _, err := db.AppendAgentEventAt(ctx, fx.pool, fx.parent, newUUID(), "user_message", `{}`, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	channel := fx.childOf(fx.parent)
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET delegation_via='person',status='idle' WHERE id=$1`, channel); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordChildResultRead(ctx, fx.pool, fx.parent, channel); err != nil {
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
		if strings.Contains(f.key, channel) {
			t.Errorf("channel got worker nudge: %+v", f)
		}
	}
}

func TestChannelAccountCaps(t *testing.T) {
	for _, hourly := range []bool{false, true} {
		t.Run(fmt.Sprint(hourly), func(t *testing.T) {
			fx := newDelegFx(t)
			fx.api.RegisterCategoryRoutes(fx.mux)
			n, status := maxRunningChildrenPerAccount, "running"
			if hourly {
				n, status = maxStartsPerAccountPerHour, "stopped"
			}
			for range n {
				id, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: status})
				if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET delegation_via='person' WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
			}
			code, body, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/channels", fx.human("owner-1"), map[string]any{"title": "Another", "engine": "codex"}, nil)
			if code != 429 || !strings.Contains(asString(body["error"]), "channel") {
				t.Fatalf("account cap: %d %v", code, body)
			}
		})
	}
}

func TestChannelClusterResumeRequiresRecoveryReady(t *testing.T) {
	cx := newClusterDelegFx(t)
	id, _ := cx.session(sessOpts{owner: "owner-1", parent: cx.cparent, runtime: runnerRuntimeCluster, status: "disconnected"})
	if _, err := cx.pool.Exec(t.Context(), `UPDATE sessions SET delegation_via='person' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	cx.k8s.setJobMissing("blerg-runner-agent-" + id)
	resumeClusterSession(t.Context(), cx.hub, cx.pool, id, "continue", "owner-1", testSID)
	job := cx.job(id)
	if job == nil {
		t.Fatal("resume created no job")
	}
	if got := jobEnv(t, job)["BLERG_RUNNER_REQUIRE_RECOVERY_READY"]["value"]; got != "1" {
		t.Fatalf("recovery ready: %v", got)
	}
}

// TestChannelClusterResumeOnMainStaysReadOnly: BLG-460 security review finding 1 — a resumed
// person channel on a cluster Main must stay read-only exactly like any other cluster worker.
// isChildSession deliberately exempts DelegationVia=="person" for other purposes
// (RequireRecoveryReady above compensates with its own "|| person" clause); the credential
// rule must not inherit that exemption, which is exactly the bug this guards.
func TestChannelClusterResumeOnMainStaysReadOnly(t *testing.T) {
	cx := newClusterDelegFx(t)
	// This fixture wires its JobManager onto the hub after NewAPI, unlike production (and
	// mcpFx), so NewAPI's own historyPool wiring never runs for it — set it explicitly, the
	// same value NewAPI would have used, so CreateSessionJob can derive worker-ness at all.
	cx.hub.JobManager().historyPool = cx.pool
	cx.k8s.credentialFetchResponses["owner-1:github"] = []byte("ghp_personal_write")
	id, _ := cx.session(sessOpts{owner: "owner-1", parent: cx.cparent, runtime: runnerRuntimeCluster, status: "disconnected"})
	if _, err := cx.pool.Exec(t.Context(), `UPDATE sessions SET delegation_via='person' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionGitURL(t.Context(), cx.pool, id, "https://github.com/acme/widget.git"); err != nil {
		t.Fatal(err)
	}
	cx.k8s.setJobMissing("blerg-runner-agent-" + id)
	resumeClusterSession(t.Context(), cx.hub, cx.pool, id, "continue", "owner-1", testSID)
	job := cx.job(id)
	if job == nil {
		t.Fatal("resume created no job")
	}
	cx.k8s.mu.Lock()
	secrets := append([]map[string]any(nil), cx.k8s.createdSecrets...)
	cx.k8s.mu.Unlock()
	raw, _ := json.Marshal(secrets)
	if strings.Contains(string(raw), "ghp_personal_write") {
		t.Fatalf("a resumed person channel on a cluster Main must stay read-only, not regain the write-capable personal token: %s", raw)
	}
}

func TestChannelClusterGrantRefusedBeforeRecording(t *testing.T) {
	fx := newMCPFx(t)
	ctx := t.Context()
	rec := fx.do(fx.api.HandleLaunchSession, fx.human(), map[string]any{"no_repo": true, "runtime": "cluster", "kind": "agent", "title": "Main"})
	if rec.Code != 202 {
		t.Fatalf("parent: %d %s", rec.Code, rec.Body.String())
	}
	id := fx.sessionID(rec)
	parent, err := db.GetSession(ctx, fx.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	grant := fx.mustGrant(clusterTarget())
	problem := fx.api.startClusterChild(ctx, runnerStartRequest{ParentSessionID: id, DelegationVia: "person", personChannel: true, KeepRunning: true, NoRepo: true, Engine: "openclaw", Grant: grant}, fx.api.delegateFor(ctx, parent, "cluster", testSID), newUUID())
	if problem == nil || problem.Status != 422 || problem.Message != msgMCPEngine {
		t.Errorf("grant target refusal: %+v", problem)
	}
	if n := fx.count("sessions"); n != 1 {
		t.Errorf("refused grant recorded a session: %d", n)
	}
}

// TestChannelClusterWorkerRefusesGitProviderMCPConnection: BLG-460 L5 — a cluster worker
// (startClusterChild always starts one) is refused by default when its grant would attach
// an MCP connection pointed at a known git provider, even though the grant is otherwise
// entirely valid and would be accepted for an ordinary engine/target.
func TestChannelClusterWorkerRefusesGitProviderMCPConnection(t *testing.T) {
	fx := newMCPFx(t)
	ctx := t.Context()
	rec := fx.do(fx.api.HandleLaunchSession, fx.human(), map[string]any{"no_repo": true, "runtime": "cluster", "kind": "agent", "title": "Main"})
	if rec.Code != 202 {
		t.Fatalf("parent: %d %s", rec.Code, rec.Body.String())
	}
	id := fx.sessionID(rec)
	parent, err := db.GetSession(ctx, fx.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	grant := fx.mustGrant(clusterTarget())
	grant.Connections[0].URL = "https://github.com/acme/widget"
	problem := fx.api.startClusterChild(ctx, runnerStartRequest{ParentSessionID: id, DelegationVia: "person", personChannel: true, KeepRunning: true, NoRepo: true, Engine: "claude", Grant: grant}, fx.api.delegateFor(ctx, parent, "cluster", testSID), newUUID())
	if problem == nil || problem.Status != http.StatusForbidden {
		t.Fatalf("expected a forbidden refusal, got %+v", problem)
	}
	if n := fx.count("sessions"); n != 1 {
		t.Errorf("refused git-provider grant left a session behind: %d", n)
	}
}

// TestChannelOnClusterMainStartsReadOnly: BLG-460 security review finding 1 — a freshly
// started person channel on a cluster Main must never receive the launcher's write-capable
// personal git token, even though it is otherwise available and would be used for a plain
// (non-channel) cluster child.
func TestChannelOnClusterMainStartsReadOnly(t *testing.T) {
	fx := newMCPFx(t)
	ctx := t.Context()
	fx.k8s.credentialFetchResponses = map[string][]byte{mcpAcct + ":github": []byte("ghp_personal_write")}
	rec := fx.do(fx.api.HandleLaunchSession, fx.human(), map[string]any{"no_repo": true, "runtime": "cluster", "kind": "agent", "title": "Main"})
	if rec.Code != 202 {
		t.Fatalf("parent: %d %s", rec.Code, rec.Body.String())
	}
	id := fx.sessionID(rec)
	parent, err := db.GetSession(ctx, fx.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	childID := newUUID()
	problem := fx.api.startClusterChild(ctx, runnerStartRequest{ParentSessionID: id, DelegationVia: "person", personChannel: true, KeepRunning: true, Repo: "acme/widget", Engine: "claude"}, fx.api.delegateFor(ctx, parent, "cluster", testSID), childID)
	if problem != nil {
		t.Fatalf("channel start: %+v", problem)
	}
	fx.k8s.mu.Lock()
	secrets := append([]map[string]any(nil), fx.k8s.createdSecrets...)
	fx.k8s.mu.Unlock()
	raw, _ := json.Marshal(secrets)
	if strings.Contains(string(raw), "ghp_personal_write") {
		t.Fatalf("a person channel on a cluster Main must be read-only, not receive the write-capable personal token: %s", raw)
	}
}

// TestChannelClusterWorkerRefusesEnterpriseGitHostMCPConnection: BLG-460 security review
// finding 3 — a self-hosted git host (no fixed hostname, so the static provider list cannot
// name it in advance) is refused for an MCP connection once it matches this launch's own
// resolved repository host, even though the exact same host is unrelated to any built-in
// provider name.
func TestChannelClusterWorkerRefusesEnterpriseGitHostMCPConnection(t *testing.T) {
	fx := newMCPFx(t)
	ctx := t.Context()
	jm := fx.hub.JobManager()
	jm.GitURLBase = "https://git.acme-corp.internal"
	rec := fx.do(fx.api.HandleLaunchSession, fx.human(), map[string]any{"no_repo": true, "runtime": "cluster", "kind": "agent", "title": "Main"})
	if rec.Code != 202 {
		t.Fatalf("parent: %d %s", rec.Code, rec.Body.String())
	}
	id := fx.sessionID(rec)
	parent, err := db.GetSession(ctx, fx.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	grant := fx.mustGrant(clusterTarget())
	grant.Connections[0].URL = "https://git.acme-corp.internal/mcp"
	problem := fx.api.startClusterChild(ctx, runnerStartRequest{ParentSessionID: id, DelegationVia: "person", personChannel: true, KeepRunning: true, Repo: "myrepo", Engine: "claude", Grant: grant}, fx.api.delegateFor(ctx, parent, "cluster", testSID), newUUID())
	if problem == nil || problem.Status != http.StatusForbidden {
		t.Fatalf("expected a forbidden refusal for a connection matching this launch's own repository host, got %+v", problem)
	}
}
