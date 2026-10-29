package server

// Desktop sessions start cluster children (0.4.4): a session on a desktop daemon asks for runtime
// "cluster" and its child runs as a cluster pod, under the cluster's own rules, grants and cards,
// with the owner's credentials released against a live proof that never leaves the server.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// desktopParentSID is the browser session fx.parent (a desktop session) was launched from.
const desktopParentSID = "6c2e1d4f-8b3a-4f9e-9d7c-2e3f4a5b6c7d"

// newDesktopClusterFx is the delegation fixture on an install with a cluster runtime where no
// cluster session has run yet (no cluster daemon row): fx.parent is a direct, UNSANDBOXED desktop
// session of owner-1 (runtime daemon, on the laptop) launched from the browser session
// desktopParentSID. (A sandboxed parent asks for each cluster child: sandboxedParent.)
func newDesktopClusterFx(t *testing.T) *clusterDelegFx {
	t.Helper()
	fx := newDelegFx(t)
	f := &fakeK8s{
		credentialFetchResponses: map[string][]byte{"owner-1:claude": []byte("oauth-token"), "owner-1:codex": []byte(`{"tokens":{"access_token":"fixture-access-private"}}`)},
		secrets:                  map[string]map[string]string{"blerg-runner-agent": {"CLAUDE_CODE_OAUTH_TOKEN": b64("x"), "CODEX_AUTH_JSON": b64(`{"OPENAI_API_KEY":"fixture-api-private"}`)}},
	}
	jm := newTestJobManager(t, f)
	jm.GitHubOrg = "" // exercise a desktop cluster launch with no default organization
	jm.MaxSessions = 20
	fx.hub.SetJobManager(jm)
	if err := db.SetSessionPosture(context.Background(), fx.pool, fx.parent, runnerRuntimeDaemon, false); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionLaunchAuthSession(context.Background(), fx.pool, fx.parent, desktopParentSID); err != nil {
		t.Fatal(err)
	}
	return &clusterDelegFx{delegFx: fx, k8s: f}
}

// sandboxedParent is a direct sandboxed (docker) desktop session of owner-1 with a launch sign-in.
func (cx *clusterDelegFx) sandboxedParent() (string, string) {
	cx.t.Helper()
	id, tok := cx.session(sessOpts{owner: "owner-1", title: "Sandboxed"})
	if err := db.SetSessionLaunchAuthSession(context.Background(), cx.pool, id, desktopParentSID); err != nil {
		cx.t.Fatal(err)
	}
	return id, tok
}

// clusterBody is a desktop parent's start of a cluster child on a repository the cluster clones.
func clusterBody(extra map[string]any) map[string]any {
	b := map[string]any{"repo": "org/app", "prompt": "do it", "runtime": runnerRuntimeCluster}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

// startDesktopClusterChild starts a Claude cluster child of the desktop parent under a cluster rule;
// the child's own pod connects. It returns the child id and its pod.
func (cx *clusterDelegFx) startDesktopClusterChild() (string, *DaemonConn) {
	cx.t.Helper()
	cx.clusterRule("claude")
	code, out, _ := cx.postChild(cx.parent, cx.parentTok, clusterBody(nil), newUUID())
	if code != http.StatusCreated {
		cx.t.Fatalf("start: %d %v", code, out)
	}
	child, _ := out["child"].(map[string]any)
	id := asString(child["id"])
	return id, cx.podTakesOver(id, "claude")
}

func (cx *clusterDelegFx) launchProof(id string) string {
	cx.t.Helper()
	_, sid, err := db.GetSessionLaunchProof(context.Background(), cx.pool, id)
	if err != nil {
		cx.t.Fatal(err)
	}
	return sid
}

func TestDesktopParentStartsAClusterChildUnderARule(t *testing.T) {
	cx := newDesktopClusterFx(t)
	rule := cx.clusterRule("claude")
	code, out, _ := cx.postChild(cx.parent, cx.parentTok, clusterBody(map[string]any{"title": "Worker"}), newUUID())
	if code != http.StatusCreated {
		t.Fatalf("start = %d %v", code, out)
	}
	child, _ := out["child"].(map[string]any)
	id := asString(child["id"])
	if child["runtime"] != runnerRuntimeCluster || out["via"] != "rule:"+rule {
		t.Fatalf("reply = %v", out)
	}
	row := cx.row(id)
	if derefOrEmpty(row.ParentSessionID) != cx.parent || derefOrEmpty(row.Runtime) != runnerRuntimeCluster ||
		row.DaemonID != clusterDaemonID || !isChildSession(row) || derefOrEmpty(row.SpawningAccountID) != "owner-1" {
		t.Fatalf("child row = %+v", row)
	}
	if cx.job(id) == nil {
		t.Fatal("no Job was created for the child")
	}
	if !cx.fetchedWith(desktopParentSID) {
		t.Fatal("the child's credentials were not released against the parent's launch sign-in")
	}
	if got := cx.launchProof(id); got != desktopParentSID {
		t.Fatalf("the child's own proof = %q, want the one it started with", got)
	}
	// Nothing reached the desktop daemon: no spawn, and no frame naming the child or a credential.
	for _, f := range drainFrames(cx.dc) {
		if strings.Contains(string(f), id) || strings.Contains(string(f), desktopParentSID) || strings.Contains(string(f), "oauth-token") {
			t.Fatalf("the desktop daemon was sent a frame for the cluster child: %s", f)
		}
	}
	// The parent lists it.
	code, list, _ := cx.do("GET", "/api/sessions/"+cx.parent+"/children", cx.parentTok, nil, nil)
	if code != http.StatusOK || !strings.Contains(string(mustJSON(t, list)), id) {
		t.Fatalf("children = %d %v", code, list)
	}
}

// The person decides on the card; "always" makes a cluster rule even on an install where no cluster
// session ever ran (the cluster's daemon row is made for it), and the next start matches it.
func TestDesktopParentClusterChildByCardAndAlwaysRule(t *testing.T) {
	cx := newDesktopClusterFx(t)
	id := cx.ask(cx.parent, cx.parentTok, clusterBody(map[string]any{"engine": "codex"}))
	var raw string
	if err := cx.pool.QueryRow(context.Background(), `SELECT payload::text FROM agent_events WHERE session_id = $1::uuid AND kind = 'delegation_request'`,
		cx.parent).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var ev delegationRequestPayload
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Runtime != runnerRuntimeCluster || ev.DaemonName != clusterDaemonLabel || !slices.Equal(ev.Offered, []string{"once", "session", "always", "deny"}) {
		t.Fatalf("card = %+v", ev)
	}
	if r := cx.request(id); r.DaemonID != clusterDaemonID {
		t.Fatalf("request daemon = %s, want the cluster's", r.DaemonID)
	}
	code, out := cx.decide(id, cx.human("owner-1"), choice("always"))
	if code != http.StatusOK || asString(out["child_session_id"]) == "" {
		t.Fatalf("decide = %d %v", code, out)
	}
	child := asString(out["child_session_id"])
	if cx.job(child) == nil || derefOrEmpty(cx.row(child).Runtime) != runnerRuntimeCluster {
		t.Fatal("the decided child is not on the cluster")
	}
	if !cx.fetchedWith(testSID) || cx.launchProof(child) != testSID {
		t.Fatal("the decided child was not authorized with (and did not record) the decider's sign-in")
	}
	if cx.launchProof(cx.parent) != testSID {
		t.Fatal("the decision did not become the parent's newest proof")
	}
	rules, err := db.CandidateDelegationRules(context.Background(), cx.pool, "owner-1", "codex", runnerRuntimeCluster, clusterDaemonID)
	if err != nil || len(rules) != 1 || rules[0].CreatedVia != "card" {
		t.Fatalf("rules = %v %v", rules, err)
	}
	code, out2, _ := cx.postChild(cx.parent, cx.parentTok, clusterBody(map[string]any{"engine": "codex", "prompt": "again"}), newUUID())
	if code != http.StatusCreated || out2["via"] != "rule:"+rules[0].ID {
		t.Fatalf("second start = %d %v", code, out2)
	}
	cx.noSpawn()
}

func TestDesktopParentClusterChildBySessionGrant(t *testing.T) {
	cx := newDesktopClusterFx(t)
	id := cx.ask(cx.parent, cx.parentTok, clusterBody(nil))
	if code, out := cx.decide(id, cx.human("owner-1"), choice("session")); code != http.StatusOK {
		t.Fatalf("decide = %d %v", code, out)
	}
	g, err := db.FindDelegationGrant(context.Background(), cx.pool, cx.parent, "claude", runnerRuntimeCluster, clusterDaemonID)
	if err != nil || g == nil {
		t.Fatalf("grant = %v %v", g, err)
	}
	code, out, _ := cx.postChild(cx.parent, cx.parentTok, clusterBody(map[string]any{"no_repo": true, "repo": nil, "prompt": "two"}), newUUID())
	if code != http.StatusCreated || out["via"] != "grant:"+g.ID {
		t.Fatalf("second start = %d %v", code, out)
	}
	// The cluster grant is not a desktop one: a sandboxed child still asks.
	code, out, _ = cx.postChild(cx.parent, cx.parentTok, map[string]any{"repo": "app", "prompt": "three", "runtime": "docker"}, newUUID())
	if code != http.StatusAccepted {
		t.Fatalf("a cluster grant started a desktop child: %d %v", code, out)
	}
	cx.noSpawn()
}

// Rules and grants name their runtime and daemon: the desktop set never starts a cluster child,
// nor the cluster set a desktop child.
func TestDesktopParentRuleSetsAreSeparate(t *testing.T) {
	t.Run("desktop rule and grant, cluster child", func(t *testing.T) {
		cx := newDesktopClusterFx(t)
		cx.rule("claude", runnerRuntimeDocker, false)
		cx.rule("claude", runnerRuntimeDaemon, false)
		cx.grant(cx.parent, "claude", runnerRuntimeDocker, "", false)
		code, out, _ := cx.postChild(cx.parent, cx.parentTok, clusterBody(nil), newUUID())
		if code != http.StatusAccepted {
			t.Fatalf("a desktop rule or grant started a cluster child: %d %v", code, out)
		}
		if cx.count(`SELECT count(*)::int FROM sessions WHERE parent_session_id = $1::uuid`, cx.parent) != 0 {
			t.Fatal("a cluster child was started")
		}
	})
	t.Run("cluster rule, desktop child", func(t *testing.T) {
		cx := newDesktopClusterFx(t)
		cx.clusterRule("claude")
		code, out, _ := cx.postChild(cx.parent, cx.parentTok, map[string]any{"repo": "app", "prompt": "x", "runtime": "docker"}, newUUID())
		if code != http.StatusAccepted {
			t.Fatalf("a cluster rule started a desktop child: %d %v", code, out)
		}
		cx.noSpawn()
	})
}

// A rule or grant does not start a cluster child the owner's proof cannot authorize: none on record,
// or a recorded sign-in core no longer accepts. The person is asked on the card.
func TestDesktopParentClusterRuleWithoutALiveProofAsks(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		cx := newDesktopClusterFx(t)
		cx.clusterRule("claude")
		cx.k8s.mu.Lock()
		cx.k8s.deadSessionIDs = map[string]bool{desktopParentSID: true}
		cx.k8s.mu.Unlock()
		code, out, _ := cx.postChild(cx.parent, cx.parentTok, clusterBody(nil), newUUID())
		if code != http.StatusAccepted || asString(out["request_id"]) == "" {
			t.Fatalf("stale sign-in = %d %v, want 202 with a request", code, out)
		}
		if n := cx.count(`SELECT count(*)::int FROM sessions WHERE parent_session_id = $1::uuid`, cx.parent); n != 0 {
			t.Fatalf("%d children started on a stale sign-in", n)
		}
	})
	t.Run("none", func(t *testing.T) {
		cx := newDesktopClusterFx(t)
		bare, bareTok := cx.session(sessOpts{owner: "owner-1", runtime: runnerRuntimeDaemon, title: "No proof"})
		cx.clusterRule("claude")
		code, out, _ := cx.postChild(bare, bareTok, clusterBody(nil), newUUID())
		if code != http.StatusAccepted {
			t.Fatalf("no proof = %d %v", code, out)
		}
		if n := cx.count(`SELECT count(*)::int FROM sessions WHERE parent_session_id = $1::uuid`, bare); n != 0 {
			t.Fatal("a child started without an owner proof")
		}
	})
}

// On an install with no cluster runtime a desktop session asking for one gets a clear refusal, and
// nothing is stored.
func TestDesktopOnlyInstallRefusesAClusterChild(t *testing.T) {
	fx := newDelegFx(t)
	fx.rule("claude", runnerRuntimeDocker, false)
	code, out, _ := fx.postChild(fx.parent, fx.parentTok, clusterBody(nil), newUUID())
	if code != http.StatusUnprocessableEntity || asString(out["error"]) != msgNoClusterRuntime {
		t.Fatalf("desktop-only install = %d %v", code, out)
	}
	fx.storedNothing(fx.parent)
	code, out, _ = fx.do("GET", "/api/sessions/"+fx.parent+"/children/models?runtime=cluster", fx.parentTok, nil, nil)
	if code != http.StatusUnprocessableEntity || asString(out["error"]) != msgNoClusterRuntime {
		t.Fatalf("models on a desktop-only install = %d %v", code, out)
	}
	// Settings cannot add a cluster rule there either, and the approval panel has no cluster row.
	if code, out := fx.addRule(fx.human("owner-1"), map[string]any{"engine": "claude", "runtime": "cluster"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("cluster rule on a desktop-only install = %d %v", code, out)
	}
	code, view := fx.approval(fx.parent, fx.human("owner-1"))
	if code != http.StatusOK {
		t.Fatalf("approval = %d %v", code, view)
	}
	if rows, _ := view["runtimes"].([]any); len(rows) != 2 {
		t.Fatalf("approval rows = %v, want docker and daemon only", view["runtimes"])
	}
}

// A desktop folder name the cluster cannot resolve (no org, no explicit git base) is refused at the
// start, as a cluster launch refuses it.
func TestDesktopParentClusterChildBareRepoIsRefused(t *testing.T) {
	cx := newDesktopClusterFx(t)
	cx.clusterRule("claude")
	code, out, _ := cx.postChild(cx.parent, cx.parentTok, clusterBody(map[string]any{"repo": "app"}), newUUID())
	if code != http.StatusUnprocessableEntity || asString(out["error"]) != msgClusterChildBareRepo {
		t.Fatalf("bare repo = %d %v", code, out)
	}
	cx.storedNothing(cx.parent)
	// With an org the cluster resolves it, as it does for a launch.
	cx.hub.JobManager().GitHubOrg = "org"
	if code, out, _ := cx.postChild(cx.parent, cx.parentTok, clusterBody(map[string]any{"repo": "app"}), newUUID()); code != http.StatusCreated {
		t.Fatalf("bare repo with an org = %d %v", code, out)
	}
}

// A message to an idle cluster child resumes it with the proof the child was started with, not the
// parent's newer one and never nothing; with no live proof the parent is told what to do.
func TestDesktopParentResumesItsClusterChildWithTheChildsProof(t *testing.T) {
	cx := newDesktopClusterFx(t)
	child, pod := cx.startDesktopClusterChild()
	const newerParentSID = "7d3f2e5a-9c4b-4a0f-8e8d-3f4a5b6c7d8e"
	if err := db.SetSessionLaunchAuthSession(context.Background(), cx.pool, cx.parent, newerParentSID); err != nil {
		t.Fatal(err)
	}
	idle := func() {
		t.Helper()
		cx.hub.UnregisterIfCurrent(pod)
		if _, err := cx.pool.Exec(context.Background(), `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, child); err != nil {
			t.Fatal(err)
		}
		cx.k8s.finishSessionJob(child)
	}
	idle()
	cx.k8s.mu.Lock()
	fetchesBefore, jobsBefore := len(cx.k8s.credentialFetchSessionIDs), len(cx.k8s.created)
	cx.k8s.mu.Unlock()
	code, out, _ := cx.do("POST", childRoute(cx.parent, child, "/message"), cx.parentTok, map[string]any{"text": "continue"}, nil)
	if code != http.StatusAccepted || out["resumed"] != true {
		t.Fatalf("message = %d %v", code, out)
	}
	cx.k8s.mu.Lock()
	fetches := append([]string{}, cx.k8s.credentialFetchSessionIDs[fetchesBefore:]...)
	jobs := len(cx.k8s.created) - jobsBefore
	cx.k8s.mu.Unlock()
	if jobs == 0 || !slices.Contains(fetches, desktopParentSID) || slices.Contains(fetches, newerParentSID) {
		t.Fatalf("resume: %d new objects, fetched with %v; want the child's own proof %s", jobs, fetches, desktopParentSID)
	}
	// The child's proof is no longer live: the resume is refused with what to do, and nothing starts.
	pod = cx.podTakesOver(child, "claude")
	idle()
	cx.k8s.mu.Lock()
	cx.k8s.deadSessionIDs = map[string]bool{desktopParentSID: true}
	jobsBefore = len(cx.k8s.created)
	cx.k8s.mu.Unlock()
	code, out, _ = cx.do("POST", childRoute(cx.parent, child, "/message"), cx.parentTok, map[string]any{"text": "again"}, nil)
	if code != http.StatusConflict || asString(out["error"]) != msgClusterChildResumeNoProof {
		t.Fatalf("stale child proof = %d %v", code, out)
	}
	cx.k8s.mu.Lock()
	defer cx.k8s.mu.Unlock()
	if len(cx.k8s.created) != jobsBefore {
		t.Fatal("a pod was started on a stale proof")
	}
}

// The child's proof is the one it started with even when there is none: a child recorded without
// one is not resumed on the parent's (refused with what to do).
func TestDesktopParentClusterChildWithoutAProofIsNotResumed(t *testing.T) {
	cx := newDesktopClusterFx(t)
	child, pod := cx.startDesktopClusterChild()
	if _, err := cx.pool.Exec(context.Background(), `UPDATE sessions SET launch_auth_session_id = NULL, token_id = NULL, status = 'disconnected' WHERE id = $1`, child); err != nil {
		t.Fatal(err)
	}
	cx.hub.UnregisterIfCurrent(pod)
	cx.k8s.finishSessionJob(child)
	code, out, _ := cx.do("POST", childRoute(cx.parent, child, "/message"), cx.parentTok, map[string]any{"text": "continue"}, nil)
	if code != http.StatusConflict || asString(out["error"]) != msgClusterChildResumeNoProof {
		t.Fatalf("no child proof = %d %v", code, out)
	}
}

func TestDesktopParentDiscardsItsClusterChild(t *testing.T) {
	cx := newDesktopClusterFx(t)
	child, _ := cx.startDesktopClusterChild()
	code, out, _ := cx.do("POST", childRoute(cx.parent, child, "/discard"), cx.parentTok, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("discard = %d %v", code, out)
	}
	row := cx.row(child)
	if row.ArchivedAt == nil || !db.TerminallyEnded(row) || !cx.jobDeleted(child) {
		t.Fatalf("child not stopped through its Job and archived: %+v", row)
	}
}

func TestDesktopParentDiscardCascadesToItsClusterChild(t *testing.T) {
	cx := newDesktopClusterFx(t)
	child, _ := cx.startDesktopClusterChild()
	cx.hub.SetSessionOwner(cx.parent, cx.dc.ID) // the parent runs on the connected laptop
	code, out, _ := cx.do("POST", "/api/sessions/"+cx.parent+"/discard", cx.human("owner-1"), nil, nil)
	if code != http.StatusOK {
		t.Fatalf("discard = %d %v", code, out)
	}
	for _, id := range []string{child, cx.parent} {
		if row := cx.row(id); row.ArchivedAt == nil || row.EndedAt == nil {
			t.Fatalf("%s not stopped and archived", id)
		}
	}
	if !cx.jobDeleted(child) {
		t.Fatal("the cluster child's Job was not deleted")
	}
}

func TestDesktopParentEndStopsItsClusterChild(t *testing.T) {
	cx := newDesktopClusterFx(t)
	child, _ := cx.startDesktopClusterChild()
	now := time.Now()
	if err := db.EndSessionStatus(context.Background(), cx.pool, cx.parent, "ended", &now, systemEnd(db.EndReasonProcessExited)); err != nil {
		t.Fatal(err)
	}
	cx.hub.SessionTerminallyEnded(cx.parent)
	waitFor(t, "the cluster child to end", func() bool { return db.TerminallyEnded(cx.row(child)) })
	if !cx.jobDeleted(child) || derefOrEmpty(cx.row(child).EndReason) != db.EndReasonParentEnded {
		t.Fatalf("child: job deleted %v, end reason %q", cx.jobDeleted(child), derefOrEmpty(cx.row(child).EndReason))
	}
}

// Parent to child: a message reaches the child's pod. Child to parent: the child's question shows in
// the parent's view of it, and the parent's message answers it.
func TestDesktopParentAndClusterChildMessagesBothWays(t *testing.T) {
	cx := newDesktopClusterFx(t)
	child, pod := cx.startDesktopClusterChild()
	drainFrames(pod)
	code, out, _ := cx.do("POST", childRoute(cx.parent, child, "/message"), cx.parentTok, map[string]any{"text": "status?"}, nil)
	if code != http.StatusAccepted {
		t.Fatalf("message = %d %v", code, out)
	}
	delivered := false
	for _, f := range drainFrames(pod) {
		var msg protocol.AgentUserMessage
		if json.Unmarshal(f, &msg) == nil && msg.Type == "agent_user_message" && msg.Text == "status?" && msg.Source == "session" {
			delivered = true
		}
	}
	if !delivered {
		t.Fatal("the parent's message did not reach the child's pod")
	}
	for _, f := range drainFrames(cx.dc) {
		if strings.Contains(string(f), child) {
			t.Fatalf("the desktop daemon was sent the child's message: %s", f)
		}
	}
	mid, err := db.CreateMessage(t.Context(), cx.pool, child, "ask", "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEvent(t.Context(), cx.pool, child, messageEventID(mid, "ask"), "ask", `{"body":"Which branch?"}`); err != nil {
		t.Fatal(err)
	}
	code, status, _ := cx.do("GET", childRoute(cx.parent, child, ""), cx.parentTok, nil, nil)
	if asks, _ := status["open_asks"].([]any); code != http.StatusOK || len(asks) != 1 || !strings.Contains(string(mustJSON(t, asks)), "Which branch?") {
		t.Fatalf("child status = %d %v", code, status)
	}
	code, out, _ = cx.do("POST", childRoute(cx.parent, child, "/message"), cx.parentTok, map[string]any{"text": "main"}, nil)
	if code != http.StatusAccepted || out["answered_ask"] != true {
		t.Fatalf("answer = %d %v", code, out)
	}
}

// Running now: the parent's live state lists its cluster child, and its cluster grant by the
// cluster's name.
func TestDesktopParentRunningNowShowsItsClusterChild(t *testing.T) {
	cx := newDesktopClusterFx(t)
	child, _ := cx.startDesktopClusterChild()
	id := cx.ask(cx.parent, cx.parentTok, clusterBody(map[string]any{"engine": "codex"}))
	if code, out := cx.decide(id, cx.human("owner-1"), choice("session")); code != http.StatusOK {
		t.Fatalf("decide = %d %v", code, out)
	}
	snap, err := cx.api.readChildrenSnapshot(t.Context(), cx.row(cx.parent), "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range snap.Rows {
		found = found || r.ID == child
	}
	if !found {
		t.Fatalf("Running now has no cluster child: %+v", snap.Rows)
	}
	if len(snap.Grants) != 1 || snap.Grants[0].Runtime != runnerRuntimeCluster || snap.Grants[0].DaemonName != clusterDaemonLabel {
		t.Fatalf("grants = %+v", snap.Grants)
	}
}

// A model change of a running cluster child goes to its pod (resolved against the cluster's
// catalog, with the child's own proof), never to the desktop.
func TestDesktopParentChangesItsClusterChildsEffort(t *testing.T) {
	cx := newDesktopClusterFx(t)
	child, pod := cx.startDesktopClusterChild()
	drainFrames(pod)
	code, out, _ := cx.do("POST", childRoute(cx.parent, child, "/model"), cx.parentTok, map[string]any{"effort": "high"}, nil)
	if code != http.StatusAccepted {
		t.Fatalf("model = %d %v", code, out)
	}
	sent := false
	for _, f := range drainFrames(pod) {
		var msg protocol.SetSessionModel
		if json.Unmarshal(f, &msg) == nil && msg.Type == "set_session_model" && msg.SessionID == child && msg.Effort == "high" {
			sent = true
		}
	}
	if !sent {
		t.Fatal("the change did not reach the child's pod")
	}
}

// The Permissions dialog of a desktop session: its desktop's two rows and the cluster's, each on
// its own daemon. Turning the cluster row on grants the cluster children and nothing on the desktop.
func TestDesktopParentApprovalHasAClusterRow(t *testing.T) {
	cx := newDesktopClusterFx(t)
	owner := cx.human("owner-1")
	var view map[string]any
	waitFor(t, "the cluster row to be enabled", func() bool {
		var code int
		code, view = cx.approval(cx.parent, owner)
		if code != http.StatusOK {
			t.Fatalf("GET = %d %v", code, view)
		}
		rows, _ := view["runtimes"].([]any)
		if len(rows) != 3 {
			t.Fatalf("runtimes = %v, want docker, daemon and cluster", view["runtimes"])
		}
		r, _ := rows[2].(map[string]any)
		return r["runtime"] == runnerRuntimeCluster && r["can_enable"] == true
	})
	code, after := cx.setApproval(cx.parent, owner, runnerRuntimeCluster, true, asString(view["revision"]))
	if code != http.StatusOK {
		t.Fatalf("PUT = %d %v", code, after)
	}
	g, err := db.FindDelegationGrant(context.Background(), cx.pool, cx.parent, "claude", runnerRuntimeCluster, clusterDaemonID)
	if err != nil || g == nil {
		t.Fatalf("cluster grant = %v %v", g, err)
	}
	if n := cx.count(`SELECT count(*)::int FROM delegation_grants WHERE parent_session_id = $1::uuid AND revoked_at IS NULL AND daemon_id <> $2::uuid`, cx.parent, clusterDaemonID); n != 0 {
		t.Fatalf("turning the cluster row on granted %d desktop rows", n)
	}
	code, out, _ := cx.postChild(cx.parent, cx.parentTok, clusterBody(nil), newUUID())
	if code != http.StatusCreated || out["via"] != "grant:"+g.ID {
		t.Fatalf("start = %d %v", code, out)
	}
	// Turning it off revokes the cluster grants only.
	cx.grant(cx.parent, "claude", runnerRuntimeDocker, "", false)
	_, view = cx.approval(cx.parent, owner)
	if code, after := cx.setApproval(cx.parent, owner, runnerRuntimeCluster, false, asString(view["revision"])); code != http.StatusOK {
		t.Fatalf("PUT off = %d %v", code, after)
	}
	if g, _ := db.FindDelegationGrant(context.Background(), cx.pool, cx.parent, "claude", runnerRuntimeDocker, cx.dc.ID); g == nil {
		t.Fatal("turning the cluster row off revoked the desktop grant")
	}
	if g, _ := db.FindDelegationGrant(context.Background(), cx.pool, cx.parent, "claude", runnerRuntimeCluster, clusterDaemonID); g != nil {
		t.Fatal("the cluster grant survived turning the row off")
	}
}

// Settings → Sessions starting sessions can add a cluster rule; it names the cluster's daemon.
func TestSettingsAddsAClusterRule(t *testing.T) {
	cx := newDesktopClusterFx(t)
	code, out := cx.addRule(cx.human("owner-1"), map[string]any{"engine": "claude", "runtime": "cluster"})
	if code != http.StatusCreated || out["daemon_id"] != clusterDaemonID || out["runtime"] != runnerRuntimeCluster {
		t.Fatalf("add = %d %v", code, out)
	}
	if code, out := cx.addRule(cx.human("owner-1"), map[string]any{"engine": "claude", "runtime": "cluster", "daemon_id": cx.dc.ID}); code != http.StatusUnprocessableEntity {
		t.Fatalf("a cluster rule on a desktop daemon = %d %v", code, out)
	}
	code, out, _ = cx.postChild(cx.parent, cx.parentTok, clusterBody(nil), newUUID())
	if code != http.StatusCreated || !strings.HasPrefix(asString(out["via"]), "rule:") {
		t.Fatalf("start under the Settings rule = %d %v", code, out)
	}
}

// `blerg-runner budget` for the cluster children of a desktop session, and models on the cluster.
func TestDesktopParentClusterBudgetAndModels(t *testing.T) {
	cx := newDesktopClusterFx(t)
	code, body, _ := cx.do("GET", "/api/sessions/"+cx.parent+"/budget?runtime=cluster", cx.parentTok, nil, nil)
	if code != http.StatusOK || body["daemon"] != clusterDaemonLabel {
		t.Fatalf("cluster budget = %d %v", code, body)
	}
	code, body, _ = cx.do("GET", "/api/sessions/"+cx.parent+"/budget", cx.parentTok, nil, nil)
	if code != http.StatusOK || body["daemon"] != cx.dc.Name {
		t.Fatalf("desktop budget = %d %v", code, body)
	}
	code, body, _ = cx.do("GET", "/api/sessions/"+cx.parent+"/children/models?runtime=cluster", cx.parentTok, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("cluster models = %d %v", code, body)
	}
	for _, e := range body["engines"].([]any) {
		if m, _ := e.(map[string]any); m["runtime"] != runnerRuntimeCluster {
			t.Fatalf("models = %v", body)
		}
	}
}

// A desktop session launched from the browser records the sign-in it was launched from, which its
// cluster children are later authorized with.
func TestDesktopBrowserLaunchRecordsTheSignIn(t *testing.T) {
	api, dc, mint := spawnPolicyAPI(t)
	rec := postSessions(t, api, mint("session.start"), map[string]any{"daemon_id": dc.ID, "repo": "app", "runtime": "docker", "kind": "agent"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("launch = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	spawn := <-dc.send
	if strings.Contains(string(spawn), testSID) {
		t.Fatal("the launch sign-in was sent to the desktop daemon")
	}
	if _, sid, err := db.GetSessionLaunchProof(context.Background(), api.dbPool, out.SessionID); err != nil || sid != testSID {
		t.Fatalf("launch proof = %q %v, want the browser session", sid, err)
	}
}

// A person's start through the runner contract on a desktop records their sign-in too.
func TestDesktopContractLaunchRecordsTheSignIn(t *testing.T) {
	fx := newDelegFx(t)
	id := newUUID()
	p := runnerPrincipal{Kind: "human", Sub: "owner-1", Sid: testSID}
	if apiErr := fx.api.startRunnerSession(context.Background(), runnerStartRequest{Engine: "claude", Repo: "app", Prompt: "x",
		Runtime: runnerRuntimeDocker, DaemonID: fx.dc.ID}, p, id); apiErr != nil {
		t.Fatal(apiErr)
	}
	spawn := fx.spawn()
	if strings.Contains(string(mustJSON(t, spawn)), testSID) {
		t.Fatal("the sign-in was sent to the desktop daemon")
	}
	if _, sid, err := db.GetSessionLaunchProof(context.Background(), fx.pool, id); err != nil || sid != testSID {
		t.Fatalf("launch proof = %q %v", sid, err)
	}
}

// The person resuming the cluster child themselves (from the browser) makes their sign-in the
// child's proof: the parent's next resume of it uses that one.
func TestPersonsResumeRenewsTheClusterChildsProof(t *testing.T) {
	cx := newDesktopClusterFx(t)
	child, pod := cx.startDesktopClusterChild()
	cx.hub.UnregisterIfCurrent(pod)
	if _, err := cx.pool.Exec(context.Background(), `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, child); err != nil {
		t.Fatal(err)
	}
	cx.k8s.finishSessionJob(child)
	const personSID = "8e4a3f6b-0d5c-4b1a-9f9e-4a5b6c7d8e9f"
	if err := resumeClusterSessionMode(context.Background(), cx.hub, cx.pool, child, "go on", "owner-1", personSID, false,
		resumeInput{Proof: coreProof{SessionID: personSID}}); err != nil {
		t.Fatal(err)
	}
	if got := cx.launchProof(child); got != personSID {
		t.Fatalf("child proof = %q, want the person's newest sign-in", got)
	}
}

// I1: a sandboxed (docker) desktop session asks for each cluster child under the default
// BLERG_RUNNER_SANDBOX_CLUSTER_CHILDREN ("once"): the card offers once/deny only, a cluster rule
// and a cluster grant start nothing, a "session" decision is refused, and the Permissions row cannot
// be turned on. "Once" still starts the child.
func TestSandboxedDesktopParentAsksForEachClusterChild(t *testing.T) {
	for _, setting := range []string{"", "once", "bogus"} {
		t.Run("setting="+setting, func(t *testing.T) {
			t.Setenv(sandboxClusterChildrenEnv, setting)
			cx := newDesktopClusterFx(t)
			parent, tok := cx.sandboxedParent()
			cx.clusterRule("claude")
			tx, err := cx.pool.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.InsertDelegationGrantTx(context.Background(), tx, db.DelegationGrant{ParentSessionID: parent, AccountID: "owner-1",
				Engine: "claude", Runtime: runnerRuntimeCluster, DaemonID: clusterDaemonID}); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			id := cx.ask(parent, tok, clusterBody(nil))
			if r := cx.request(id); !slices.Equal(r.Offered, []string{"once", "deny"}) {
				t.Fatalf("offered = %v, want once and deny only", r.Offered)
			}
			if code, out := cx.decide(id, cx.human("owner-1"), choice("session")); code != http.StatusUnprocessableEntity {
				t.Fatalf("session choice = %d %v", code, out)
			}
			code, out := cx.decide(id, cx.human("owner-1"), choice("once"))
			if code != http.StatusOK || cx.job(asString(out["child_session_id"])) == nil {
				t.Fatalf("once = %d %v", code, out)
			}
			// The Permissions row says why and cannot be turned on (panel and launch sheet alike).
			owner := cx.human("owner-1")
			code, view := cx.approval(parent, owner)
			if code != http.StatusOK {
				t.Fatalf("approval = %d %v", code, view)
			}
			rows, _ := view["runtimes"].([]any)
			row, _ := rows[len(rows)-1].(map[string]any)
			if row["runtime"] != runnerRuntimeCluster || row["can_enable"] != false || row["reason"] != msgSandboxedClusterChildren {
				t.Fatalf("cluster row = %v", row)
			}
			if code, out := cx.setApproval(parent, owner, runnerRuntimeCluster, true, asString(view["revision"])); code != http.StatusConflict {
				t.Fatalf("enable = %d %v", code, out)
			}
		})
	}
}

// BLERG_RUNNER_SANDBOX_CLUSTER_CHILDREN=any: a sandboxed desktop session's cluster children are
// preapprovable like an unsandboxed one's.
func TestSandboxedDesktopParentClusterChildrenWithSettingAny(t *testing.T) {
	t.Setenv(sandboxClusterChildrenEnv, "any")
	cx := newDesktopClusterFx(t)
	parent, tok := cx.sandboxedParent()
	rule := cx.clusterRule("claude")
	code, out, _ := cx.postChild(parent, tok, clusterBody(nil), newUUID())
	if code != http.StatusCreated || out["via"] != "rule:"+rule {
		t.Fatalf("start under the rule = %d %v", code, out)
	}
	id := cx.ask(parent, tok, clusterBody(map[string]any{"engine": "codex"}))
	if r := cx.request(id); !slices.Equal(r.Offered, []string{"once", "session", "always", "deny"}) {
		t.Fatalf("offered = %v", r.Offered)
	}
	waitFor(t, "the cluster row to be enabled", func() bool {
		_, view := cx.approval(parent, cx.human("owner-1"))
		rows, _ := view["runtimes"].([]any)
		row, _ := rows[len(rows)-1].(map[string]any)
		return row["runtime"] == runnerRuntimeCluster && row["can_enable"] == true
	})
}

func TestSandboxClusterChildrenSettingIsReadInOnePlace(t *testing.T) {
	for in, want := range map[string]string{"": "once", "once": "once", "any": "any", "ANY": "any", "always": "once"} {
		t.Setenv(sandboxClusterChildrenEnv, in)
		if got := sandboxClusterChildren(); got != want {
			t.Fatalf("%q = %q, want %q", in, got, want)
		}
	}
	// A legacy desktop row with no runtime fails closed under "once".
	t.Setenv(sandboxClusterChildrenEnv, "")
	if confinementOK(&db.SessionRow{DaemonID: "00000000-0000-4000-8000-0000000000d1"}, runnerRuntimeCluster) {
		t.Fatal("a desktop row with no runtime may preapprove cluster children")
	}
}

// M1: on an install without personal credentials wired, a resume needs no live sign-in, so a child
// recorded with none resumes (a new Job, no 503 from the resume path); with them wired, a dead one is
// refused before anything is created.
func TestClusterChildResumeCheckOnBothInstalls(t *testing.T) {
	cx := newDesktopClusterFx(t)
	child, pod := cx.startDesktopClusterChild()
	cx.hub.UnregisterIfCurrent(pod)
	if _, err := cx.pool.Exec(context.Background(), `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, child); err != nil {
		t.Fatal(err)
	}
	cx.k8s.finishSessionJob(child)
	// A child recorded with no proof (started under a rule where none was needed).
	if _, err := cx.pool.Exec(context.Background(), `UPDATE sessions SET launch_auth_session_id = NULL, token_id = NULL WHERE id = $1`, child); err != nil {
		t.Fatal(err)
	}
	jm := cx.hub.JobManager()
	key := jm.CoreInternalKey
	jm.CoreInternalKey = "" // not wired: nothing is released against a proof
	jobsBefore := len(cx.k8s.created)
	code, out, _ := cx.do("POST", childRoute(cx.parent, child, "/message"), cx.parentTok, map[string]any{"text": "go"}, nil)
	if code != http.StatusAccepted || out["resumed"] != true {
		t.Fatalf("not wired: %d %v, want the child resumed", code, out)
	}
	if len(cx.k8s.created) == jobsBefore {
		t.Fatal("not wired: no Job was created for the resume")
	}
	jm.CoreInternalKey = key
	cx.k8s.finishSessionJob(child)
	if _, err := cx.pool.Exec(context.Background(), `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, child); err != nil {
		t.Fatal(err)
	}
	jobsBefore = len(cx.k8s.created)
	code, out, _ = cx.do("POST", childRoute(cx.parent, child, "/message"), cx.parentTok, map[string]any{"text": "go"}, nil)
	if code != http.StatusConflict || asString(out["error"]) != msgClusterChildResumeNoProof {
		t.Fatalf("wired, no sign-in = %d %v", code, out)
	}
	if len(cx.k8s.created) != jobsBefore {
		t.Fatal("wired, no sign-in: a Job was created")
	}
}

// The resume path itself asks for a live proof only where personal credentials are wired: an
// owner's resume with none starts a pod on an install without them, and is refused (nothing created)
// on one with them.
func TestClusterResumeWithoutProofOnBothInstalls(t *testing.T) {
	for _, wired := range []bool{false, true} {
		t.Run(map[bool]string{false: "not wired", true: "wired"}[wired], func(t *testing.T) {
			cx := newDesktopClusterFx(t)
			child, pod := cx.startDesktopClusterChild()
			cx.hub.UnregisterIfCurrent(pod)
			if _, err := cx.pool.Exec(context.Background(), `UPDATE sessions SET status = 'disconnected', launch_auth_session_id = NULL, token_id = NULL WHERE id = $1`, child); err != nil {
				t.Fatal(err)
			}
			cx.k8s.finishSessionJob(child)
			jm := cx.hub.JobManager()
			if !wired {
				key := jm.CoreInternalKey
				jm.CoreInternalKey = ""
				t.Cleanup(func() { jm.CoreInternalKey = key })
			}
			jobsBefore := len(cx.k8s.created)
			err := resumeClusterSessionMode(context.Background(), cx.hub, cx.pool, child, "go on", "owner-1", "", false, resumeInput{})
			created := len(cx.k8s.created) != jobsBefore
			if wired && (err == nil || created) {
				t.Fatalf("wired, no proof: err=%v created=%v, want refused before a Job", err, created)
			}
			if !wired && (err != nil || !created) {
				t.Fatalf("not wired, no proof: err=%v created=%v, want resumed", err, created)
			}
		})
	}
}

// M2: core that cannot answer is a retryable 503, not "the sign-in is no longer live".
func TestClusterChildResumeDuringACoreOutageIsRetryable(t *testing.T) {
	cx := newDesktopClusterFx(t)
	child, pod := cx.startDesktopClusterChild()
	cx.hub.UnregisterIfCurrent(pod)
	if _, err := cx.pool.Exec(context.Background(), `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, child); err != nil {
		t.Fatal(err)
	}
	cx.k8s.finishSessionJob(child)
	cx.k8s.mu.Lock()
	cx.k8s.credentialListStatus = http.StatusBadGateway
	cx.k8s.mu.Unlock()
	code, out, hdr := cx.do("POST", childRoute(cx.parent, child, "/message"), cx.parentTok, map[string]any{"text": "go"}, nil)
	if code != http.StatusServiceUnavailable || hdr.Get("Retry-After") == "" || asString(out["error"]) == msgClusterChildResumeNoProof {
		t.Fatalf("core outage = %d %v (Retry-After %q)", code, out, hdr.Get("Retry-After"))
	}
}

// M3: a resume whose pod could not be created leaves the child's proof as it was.
func TestFailedResumeKeepsTheClusterChildsProof(t *testing.T) {
	cx := newDesktopClusterFx(t)
	child, pod := cx.startDesktopClusterChild()
	cx.hub.UnregisterIfCurrent(pod)
	if _, err := cx.pool.Exec(context.Background(), `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, child); err != nil {
		t.Fatal(err)
	}
	cx.k8s.finishSessionJob(child)
	cx.k8s.mu.Lock()
	cx.k8s.secretCreateStatus, cx.k8s.secretCreateBody = http.StatusForbidden, `{"message":"quota"}`
	cx.k8s.mu.Unlock()
	const personSID = "9f5b4a7c-1e6d-4c2b-8a0f-5b6c7d8e9fa0"
	if err := resumeClusterSessionMode(context.Background(), cx.hub, cx.pool, child, "go on", "owner-1", personSID, false,
		resumeInput{Proof: coreProof{SessionID: personSID}}); err == nil {
		t.Fatal("the resume succeeded with the Secret refused")
	}
	if got := cx.launchProof(child); got != desktopParentSID {
		t.Fatalf("child proof = %q after a failed resume, want it unchanged", got)
	}
}

// The launch sheet learns the setting from the cluster status.
func TestClusterStatusReportsTheSandboxClusterChildrenSetting(t *testing.T) {
	cx := newDesktopClusterFx(t)
	for env, want := range map[string]string{"": "once", "any": "any"} {
		t.Setenv(sandboxClusterChildrenEnv, env)
		req := httptest.NewRequest(http.MethodGet, "/api/cluster/status", nil)
		req.Header.Set("Authorization", "Bearer "+cx.human("owner-1"))
		rec := httptest.NewRecorder()
		cx.api.HandleGetClusterStatus(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != http.StatusOK || out["sandbox_cluster_children"] != want {
			t.Fatalf("%q: %d %v", env, rec.Code, out)
		}
	}
}
