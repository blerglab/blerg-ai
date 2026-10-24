package server

// Child sessions on the cluster (0.4.3): a parent running in a cluster pod starts children that run
// as cluster pods, under the same rules, grants, cards, limits and cascades as desktop children.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const clusterParentSID = "5b1f0c3e-7a2d-4e8f-9c6b-1d2e3f4a5b6c"

// clusterDelegFx is the delegation fixture with a cluster runtime (the k8s fake) and a direct
// cluster parent of owner-1 whose launch proof (a browser session) is on record.
type clusterDelegFx struct {
	*delegFx
	k8s                *fakeK8s
	cparent, cparentTk string
	pod                *DaemonConn // the cluster parent's own pod connection
}

func newClusterDelegFx(t *testing.T) *clusterDelegFx {
	t.Helper()
	fx := newDelegFx(t)
	f := &fakeK8s{
		credentialFetchResponses: map[string][]byte{"owner-1:claude": []byte("oauth-token"), "owner-1:codex": []byte(`{"tokens":{}}`)},
		secrets:                  map[string]map[string]string{"blerg-runner-agent": {"CLAUDE_CODE_OAUTH_TOKEN": b64("x"), "CODEX_AUTH_JSON": b64("{}")}},
	}
	jm := newTestJobManager(t, f)
	jm.MaxSessions = 20
	fx.hub.SetJobManager(jm)
	cx := &clusterDelegFx{delegFx: fx, k8s: f}
	cx.cparent, cx.cparentTk = fx.session(sessOpts{owner: "owner-1", runtime: runnerRuntimeCluster, title: "Cluster parent"})
	if err := db.SetSessionLaunchAuthSession(context.Background(), fx.pool, cx.cparent, clusterParentSID); err != nil {
		t.Fatal(err)
	}
	// As on a real cluster: the parent's pod connects as its own daemon and its session_started
	// moves the row off the placeholder cluster daemon.
	cx.pod = cx.podTakesOver(cx.cparent, "claude")
	return cx
}

// podTakesOver is what a real session pod does once its Job runs: it connects as its own daemon
// with the session's per-session runtime token (a fresh daemon id, mode "runner", named
// runner-<session id>, bound to that one session; daemon_conn.go's runtime hello), and its
// session_started goes through the frame path with that runtime authority, whose upsert moves the
// session's daemon_id from clusterDaemonID to the pod's id (runtime stays "cluster"). The session
// then reports running. A resumed session's new pod does the same with another id and token.
func (cx *clusterDelegFx) podTakesOver(session, engine string) *DaemonConn {
	cx.t.Helper()
	ctx := context.Background()
	raw, _, err := db.MintRuntimeLaunch(ctx, cx.pool, session, "", time.Hour)
	if err != nil {
		cx.t.Fatal(err)
	}
	rt, err := db.ValidateRuntimeToken(ctx, cx.pool, raw)
	if err != nil {
		cx.t.Fatal(err)
	}
	podID := newUUID()
	pod := &DaemonConn{ID: podID, Name: "runner-" + session, Mode: "runner", BoundSessionID: session, RuntimeTokenID: rt.ID, send: make(chan []byte, 64)}
	pod.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	pod.SetAvailableEngines([]string{engine})
	cx.hub.Register(pod)
	if err := db.UpsertDaemon(ctx, cx.pool, podID, pod.Name, "runner", ""); err != nil {
		cx.t.Fatal(err)
	}
	frame, _ := json.Marshal(protocol.SessionStarted{Type: "session_started", SessionID: session, Kind: "agent", Title: "Cluster parent"})
	cx.podFrame(pod, frame)
	if _, err := cx.pool.Exec(ctx, `UPDATE sessions SET status = 'running' WHERE id = $1::uuid`, session); err != nil {
		cx.t.Fatal(err)
	}
	row := cx.row(session)
	if row.DaemonID != podID || derefOrEmpty(row.Runtime) != runnerRuntimeCluster {
		cx.t.Fatalf("pod takeover: daemon %s runtime %q, want the pod %s and cluster", row.DaemonID, derefOrEmpty(row.Runtime), podID)
	}
	return pod
}

// clusterRule stores a standing rule of owner-1 for engine on the cluster (making the cluster's
// daemon row first, as Settings does, for an install where no cluster session has run).
func (cx *clusterDelegFx) clusterRule(engine string) string {
	cx.t.Helper()
	if err := db.UpsertDaemon(context.Background(), cx.pool, clusterDaemonID, "cluster", "runner", ""); err != nil {
		cx.t.Fatal(err)
	}
	r, err := db.InsertDelegationRule(context.Background(), cx.pool, db.DelegationRule{AccountID: "owner-1", Engine: engine,
		Runtime: runnerRuntimeCluster, DaemonID: clusterDaemonID, CreatedVia: "settings"})
	if err != nil {
		cx.t.Fatal(err)
	}
	return r.ID
}

// job returns the Job created for session (nil when none).
func (cx *clusterDelegFx) job(session string) map[string]any {
	cx.k8s.mu.Lock()
	defer cx.k8s.mu.Unlock()
	want := "blerg-runner-agent-" + strings.ToLower(session)
	for _, j := range cx.k8s.created {
		if meta, _ := j["metadata"].(map[string]any); meta != nil && meta["name"] == want && j["kind"] == "Job" {
			return j
		}
	}
	return nil
}

func (cx *clusterDelegFx) jobDeleted(session string) bool {
	cx.k8s.mu.Lock()
	defer cx.k8s.mu.Unlock()
	return slices.Contains(cx.k8s.deleted, "blerg-runner-agent-"+strings.ToLower(session))
}

func (cx *clusterDelegFx) fetchedWith(sid string) bool {
	cx.k8s.mu.Lock()
	defer cx.k8s.mu.Unlock()
	return slices.Contains(cx.k8s.credentialFetchSessionIDs, sid)
}

// startClusterChildViaRule starts a Claude child of the cluster parent under a cluster rule and
// returns its id.
func (cx *clusterDelegFx) startClusterChildViaRule() string {
	cx.t.Helper()
	cx.clusterRule("claude")
	code, out, _ := cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "do it"}, newUUID())
	if code != http.StatusCreated {
		cx.t.Fatalf("start: %d %v", code, out)
	}
	child, _ := out["child"].(map[string]any)
	id := asString(child["id"])
	cx.podTakesOver(id, "claude") // the child's own pod connects too
	return id
}

func TestClusterParentStartsAClusterChildUnderARule(t *testing.T) {
	cx := newClusterDelegFx(t)
	rule := cx.clusterRule("claude")
	code, out, _ := cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "do the thing", "title": "Worker"}, newUUID())
	if code != http.StatusCreated {
		t.Fatalf("start = %d %v", code, out)
	}
	child, _ := out["child"].(map[string]any)
	id := asString(child["id"])
	if child["runtime"] != runnerRuntimeCluster || out["via"] != "rule:"+rule || out["plugins"] != childPluginsSkipped {
		t.Fatalf("reply = %v", out)
	}
	row := cx.row(id)
	if derefOrEmpty(row.ParentSessionID) != cx.cparent || row.DelegationVia != "rule:"+rule || row.StartedByKind != "session" ||
		derefOrEmpty(row.Runtime) != runnerRuntimeCluster || row.DaemonID != clusterDaemonID || derefOrEmpty(row.SpawningAccountID) != "owner-1" {
		t.Fatalf("child row = %+v", row)
	}
	if !isChildSession(row) {
		t.Fatal("the cluster child is not a child: it could start sessions of its own")
	}
	job := cx.job(id)
	if job == nil {
		t.Fatal("no Job was created for the child")
	}
	env := jobEnv(t, job)
	for name, want := range map[string]string{"BLERG_RUNNER_REQUIRE_RECOVERY_READY": "1", "BLERG_RUNNER_REPO": "app", "BLERG_RUNNER_ENGINE": "claude"} {
		if got, _ := env[name]["value"].(string); got != want {
			t.Fatalf("job env %s = %q, want %q", name, got, want)
		}
	}
	if !cx.fetchedWith(clusterParentSID) {
		t.Fatal("the child's credential was not fetched with the parent's launch proof")
	}
	cx.noSpawn() // nothing reached the desktop daemon
	if n := cx.count(`SELECT count(*)::int FROM board_tokens WHERE session_id = $1::uuid AND revoked_at IS NULL`, id); n == 0 {
		t.Fatal("the child has no session token of its own")
	}
	// The child appears in its parent's list.
	code, list, _ := cx.do("GET", "/api/sessions/"+cx.cparent+"/children", cx.cparentTk, nil, nil)
	if code != http.StatusOK || !strings.Contains(string(mustJSON(t, list)), id) {
		t.Fatalf("children = %d %v", code, list)
	}
}

func TestClusterChildRefusals(t *testing.T) {
	cx := newClusterDelegFx(t)
	cx.clusterRule("claude")
	for name, c := range map[string]struct {
		body map[string]any
		want int
		msg  string
	}{
		"docker runtime":    {map[string]any{"repo": "app", "prompt": "x", "runtime": "docker"}, 422, msgClusterChildRuntime},
		"daemon runtime":    {map[string]any{"repo": "app", "prompt": "x", "runtime": "daemon"}, 422, msgClusterChildRuntime},
		"engine off-target": {map[string]any{"repo": "app", "prompt": "x", "engine": "openclaw"}, 422, "the cluster cannot run OpenClaw"},
		"bad repo":          {map[string]any{"repo": "../x", "prompt": "x"}, 422, "repo must be a folder name"},
		"no repo choice":    {map[string]any{"prompt": "x"}, 422, "repo is required"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, _ := cx.postChild(cx.cparent, cx.cparentTk, c.body, newUUID())
			if code != c.want || !strings.Contains(asString(out["error"]), c.msg) {
				t.Fatalf("%d %v, want %d %q", code, out, c.want, c.msg)
			}
			cx.storedNothing(cx.cparent)
		})
	}
	// A repo the parent never checked out is fine on the cluster: it is cloned, as a cluster
	// launch's repo is.
	code, out, _ := cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"repo": "org/other", "prompt": "x"}, newUUID())
	if code != http.StatusCreated {
		t.Fatalf("cluster repo = %d %v", code, out)
	}
}

// A desktop parent asking for a cluster child (0.4.4) is asked for on the card when nothing allows
// it: the request is the cluster's (keyed on its daemon), never a start on the desktop. (Before
// 0.4.4 this was a refusal; delegation_desktop_cluster_test.go covers the rest.)
func TestDesktopParentAskingForAClusterChild(t *testing.T) {
	cx := newClusterDelegFx(t)
	code, out, _ := cx.postChild(cx.parent, cx.parentTok, map[string]any{"repo": "org/app", "prompt": "x", "runtime": "cluster"}, newUUID())
	if code != http.StatusAccepted {
		t.Fatalf("desktop parent, runtime cluster = %d %v", code, out)
	}
	if r := cx.request(asString(out["request_id"])); r.DaemonID != clusterDaemonID {
		t.Fatalf("request daemon = %s, want the cluster's", r.DaemonID)
	}
	cx.noSpawn()
}

func TestClusterParentWithoutAClusterRuntimeIsRefused(t *testing.T) {
	fx := newDelegFx(t)
	parent, tok := fx.session(sessOpts{owner: "owner-1", runtime: runnerRuntimeCluster})
	code, out, _ := fx.postChild(parent, tok, map[string]any{"repo": "app", "prompt": "x"}, newUUID())
	if code != http.StatusUnprocessableEntity || asString(out["error"]) != msgClusterNotConfigured {
		t.Fatalf("%d %v", code, out)
	}
	fx.storedNothing(parent)
}

func TestClusterChildCardAndAlwaysRule(t *testing.T) {
	cx := newClusterDelegFx(t)
	id := cx.ask(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "review it", "engine": "codex"})
	var raw string
	if err := cx.pool.QueryRow(context.Background(), `SELECT payload::text FROM agent_events WHERE session_id = $1::uuid AND kind = 'delegation_request'`,
		cx.cparent).Scan(&raw); err != nil {
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
		t.Fatalf("request daemon = %s", r.DaemonID)
	}
	code, out := cx.decide(id, cx.human("owner-1"), choice("always"))
	if code != http.StatusOK || asString(out["child_session_id"]) == "" {
		t.Fatalf("decide = %d %v", code, out)
	}
	child := asString(out["child_session_id"])
	if cx.job(child) == nil || derefOrEmpty(cx.row(child).Runtime) != runnerRuntimeCluster {
		t.Fatal("the decided child is not on the cluster")
	}
	if !cx.fetchedWith(testSID) {
		t.Fatal("the decided child was not authorized with the decider's browser session")
	}
	rules, err := db.CandidateDelegationRules(context.Background(), cx.pool, "owner-1", "codex", runnerRuntimeCluster, clusterDaemonID)
	if err != nil || len(rules) != 1 || rules[0].CreatedVia != "card" {
		t.Fatalf("rules = %v %v", rules, err)
	}
	// The next start matches the rule.
	code, out2, _ := cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "again", "engine": "codex"}, newUUID())
	if code != http.StatusCreated || out2["via"] != "rule:"+rules[0].ID {
		t.Fatalf("second start = %d %v", code, out2)
	}
}

func TestClusterChildSessionGrant(t *testing.T) {
	cx := newClusterDelegFx(t)
	id := cx.ask(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "one"})
	if code, out := cx.decide(id, cx.human("owner-1"), choice("session")); code != http.StatusOK {
		t.Fatalf("decide = %d %v", code, out)
	}
	g, err := db.FindDelegationGrant(context.Background(), cx.pool, cx.cparent, "claude", runnerRuntimeCluster, clusterDaemonID)
	if err != nil || g == nil {
		t.Fatalf("grant = %v %v", g, err)
	}
	code, out, _ := cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"no_repo": true, "prompt": "two"}, newUUID())
	if code != http.StatusCreated || out["via"] != "grant:"+g.ID {
		t.Fatalf("second start = %d %v", code, out)
	}
}

// Rules and grants name the runtime: a desktop rule or grant never starts a cluster child.
func TestClusterChildIgnoresDesktopRulesAndGrants(t *testing.T) {
	cx := newClusterDelegFx(t)
	cx.rule("claude", runnerRuntimeDocker, false)
	if _, err := db.InsertDelegationRule(context.Background(), cx.pool, db.DelegationRule{AccountID: "owner-1", Engine: "claude",
		Runtime: runnerRuntimeDocker, DaemonID: clusterDaemonID, CreatedVia: "settings"}); err != nil {
		t.Fatal(err)
	}
	code, out, _ := cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "x"}, newUUID())
	if code != http.StatusAccepted {
		t.Fatalf("a desktop rule started a cluster child: %d %v", code, out)
	}
}

// With no owner proof on record, a rule cannot authorize the child's credentials: the person is
// asked instead, and their decision carries the proof.
func TestClusterRuleWithoutOwnerProofAsksThePerson(t *testing.T) {
	cx := newClusterDelegFx(t)
	bare, bareTok := cx.session(sessOpts{owner: "owner-1", runtime: runnerRuntimeCluster})
	cx.clusterRule("claude")
	code, out, _ := cx.postChild(bare, bareTok, map[string]any{"repo": "app", "prompt": "x"}, newUUID())
	if code != http.StatusAccepted {
		t.Fatalf("no proof = %d %v", code, out)
	}
	if cx.count(`SELECT count(*)::int FROM sessions WHERE parent_session_id = $1::uuid`, bare) != 0 {
		t.Fatal("a child started without an owner proof")
	}
}

// The parent's recorded sign-in has expired (or the owner signed out): a rule or a grant does not
// start the child, which would fail at the credential fetch; the person is asked on the card.
func TestClusterRuleWithAStaleOwnerSignInAsksThePerson(t *testing.T) {
	for _, via := range []string{"rule", "grant"} {
		t.Run(via, func(t *testing.T) {
			cx := newClusterDelegFx(t)
			before := 0
			if via == "rule" {
				cx.clusterRule("claude")
			} else {
				// The person allows a first start for the session (a live decision), then signs out.
				id := cx.ask(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "one"})
				if code, out := cx.decide(id, cx.human("owner-1"), choice("session")); code != http.StatusOK {
					t.Fatalf("decide = %d %v", code, out)
				}
				before = 1
			}
			cx.k8s.mu.Lock()
			cx.k8s.deadSessionIDs = map[string]bool{clusterParentSID: true, testSID: true}
			cx.k8s.mu.Unlock()
			code, out, _ := cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "x"}, newUUID())
			if code != http.StatusAccepted || asString(out["request_id"]) == "" {
				t.Fatalf("stale sign-in = %d %v, want 202 with a request", code, out)
			}
			if n := cx.count(`SELECT count(*)::int FROM sessions WHERE parent_session_id = $1::uuid`, cx.cparent); n != before {
				t.Fatalf("children = %d, want %d: a child was started (and failed) on a stale sign-in", n, before)
			}
		})
	}
}

// A child whose launch failed (the cluster refused its Job) ran nothing: it counts toward neither
// the parent's starts nor the account's hourly starts, so a retry is not spent on it.
func TestFailedClusterChildStartDoesNotCountAsAStart(t *testing.T) {
	cx := newClusterDelegFx(t)
	cx.clusterRule("claude")
	cx.k8s.mu.Lock()
	cx.k8s.secretCreateStatus, cx.k8s.secretCreateBody = http.StatusForbidden, `{"message":"quota"}`
	cx.k8s.mu.Unlock()
	code, out, _ := cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "x"}, newUUID())
	if code < 500 {
		t.Fatalf("start with a refused Job = %d %v, want a 5xx", code, out)
	}
	ctx := context.Background()
	if n, _, err := db.ParentStartWindow(ctx, cx.pool, cx.cparent, time.Now().Add(-time.Hour), "", 20); err != nil || n != 0 {
		t.Fatalf("parent starts = %d %v, want 0", n, err)
	}
	if n, _, err := db.AccountStartsSince(ctx, cx.pool, "owner-1", time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("account starts = %d %v, want 0", n, err)
	}
	if n := cx.count(`SELECT count(*)::int FROM board_tokens t JOIN sessions s ON s.id = t.session_id
		WHERE s.parent_session_id = $1::uuid AND t.revoked_at IS NULL`, cx.cparent); n != 0 {
		t.Fatalf("the failed child kept %d live session token(s)", n)
	}
}

func TestClusterChildDiscardByItsParent(t *testing.T) {
	cx := newClusterDelegFx(t)
	child := cx.startClusterChildViaRule()
	code, out, _ := cx.do("POST", "/api/sessions/"+cx.cparent+"/children/"+child+"/discard", cx.cparentTk, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("discard = %d %v", code, out)
	}
	row := cx.row(child)
	if row.ArchivedAt == nil || !db.TerminallyEnded(row) {
		t.Fatalf("child not stopped and archived: %+v", row)
	}
	if !cx.jobDeleted(child) {
		t.Fatal("the child's Job was not deleted")
	}
}

func TestClusterParentDiscardCascadesToClusterChildren(t *testing.T) {
	cx := newClusterDelegFx(t)
	child := cx.startClusterChildViaRule()
	code, out, _ := cx.do("POST", "/api/sessions/"+cx.cparent+"/discard", cx.human("owner-1"), nil, nil)
	if code != http.StatusOK {
		t.Fatalf("discard = %d %v", code, out)
	}
	for _, id := range []string{child, cx.cparent} {
		row := cx.row(id)
		if row.ArchivedAt == nil || row.EndedAt == nil {
			t.Fatalf("%s not stopped and archived", id)
		}
		if !cx.jobDeleted(id) {
			t.Fatalf("%s: Job not deleted", id)
		}
	}
}

// A cluster parent whose agent ends (its pod's daemon reports the exit, as sessions.go records it)
// stops its cluster children through their Jobs.
func TestClusterParentEndStopsClusterChildren(t *testing.T) {
	cx := newClusterDelegFx(t)
	child := cx.startClusterChildViaRule()
	now := time.Now()
	if err := db.EndSessionStatus(context.Background(), cx.pool, cx.cparent, "ended", &now, systemEnd(db.EndReasonProcessExited)); err != nil {
		t.Fatal(err)
	}
	cx.hub.SessionTerminallyEnded(cx.cparent)
	waitFor(t, "the child to end", func() bool { return db.TerminallyEnded(cx.row(child)) })
	if !cx.jobDeleted(child) {
		t.Fatal("the child's Job was not deleted")
	}
	if got := derefOrEmpty(cx.row(child).EndReason); got != db.EndReasonParentEnded {
		t.Fatalf("end reason = %q", got)
	}
}

func TestClusterChildApprovalPanel(t *testing.T) {
	cx := newClusterDelegFx(t)
	owner := cx.human("owner-1")
	var view map[string]any
	waitFor(t, "cluster engines in the approval panel", func() bool {
		var code int
		code, view = cx.approval(cx.cparent, owner)
		if code != http.StatusOK {
			t.Fatalf("GET = %d %v", code, view)
		}
		runtimes, _ := view["runtimes"].([]any)
		if len(runtimes) != 1 {
			t.Fatalf("runtimes = %v", view["runtimes"])
		}
		r, _ := runtimes[0].(map[string]any)
		return r["runtime"] == runnerRuntimeCluster && r["can_enable"] == true
	})
	code, after := cx.setApproval(cx.cparent, owner, runnerRuntimeCluster, true, asString(view["revision"]))
	if code != http.StatusOK {
		t.Fatalf("PUT = %d %v", code, after)
	}
	g, err := db.FindDelegationGrant(context.Background(), cx.pool, cx.cparent, "claude", runnerRuntimeCluster, clusterDaemonID)
	if err != nil || g == nil {
		t.Fatalf("grant = %v %v", g, err)
	}
	code, out, _ := cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "x"}, newUUID())
	if code != http.StatusCreated || out["via"] != "grant:"+g.ID {
		t.Fatalf("start = %d %v", code, out)
	}
}

func TestClusterParentBudget(t *testing.T) {
	cx := newClusterDelegFx(t)
	code, body, _ := cx.do("GET", "/api/sessions/"+cx.cparent+"/budget", cx.cparentTk, nil, nil)
	if code != http.StatusOK || body["daemon"] != clusterDaemonLabel {
		t.Fatalf("budget = %d %v", code, body)
	}
	if list, _ := body["engines"].([]any); len(list) == 0 {
		t.Fatalf("engines = %v", body["engines"])
	}
}

// The parent acts on its cluster children with its owner's launch proof, so a message to a child
// whose pod has gone idle can resume it with the owner's credentials.
func TestClusterDelegateCarriesTheOwnerProof(t *testing.T) {
	cx := newClusterDelegFx(t)
	p := cx.api.delegatePrincipalFor(context.Background(), cx.row(cx.cparent), "")
	if p.proof() != (coreProof{SessionID: clusterParentSID}) {
		t.Fatalf("proof = %+v", p.proof())
	}
	if p := cx.api.delegatePrincipalFor(context.Background(), cx.row(cx.cparent), testSID); p.proof() != (coreProof{SessionID: testSID}) {
		t.Fatalf("a decision's proof was not preferred: %+v", p.proof())
	}
	if p := cx.api.delegatePrincipalFor(context.Background(), cx.row(cx.parent), ""); p.proof().valid() {
		t.Fatalf("a desktop parent's delegate carries a proof: %+v", p.proof())
	}
}

// A cluster session records the browser session it was launched with.
func TestClusterLaunchRecordsTheOwnerProof(t *testing.T) {
	f := &fakeK8s{credentialFetchResponses: map[string][]byte{"owner-1:claude": []byte("oauth-token")}}
	api, _, pool := clusterRunnerAPI(t, f)
	id := newUUID()
	p := runnerPrincipal{Kind: "human", Sub: "owner-1", Sid: testSID}
	if apiErr := api.startRunnerSession(context.Background(), runnerStartRequest{Engine: "claude", Repo: "app", Prompt: "x",
		Runtime: runnerRuntimeCluster}, p, id); apiErr != nil {
		t.Fatal(apiErr)
	}
	if _, sid, err := db.GetSessionLaunchProof(context.Background(), pool, id); err != nil || sid != testSID {
		t.Fatalf("launch proof = %q %v", sid, err)
	}
}

// A cluster parent's pod is ephemeral: a resume brings a new pod with a new daemon id. Rules and
// grants for its children are keyed on the cluster, not on whichever pod is connected, so they
// keep matching; a request asked before the resume can still be decided after it.
func TestClusterRulesGrantsAndRequestsSurviveANewPod(t *testing.T) {
	cx := newClusterDelegFx(t)
	owner := cx.human("owner-1")
	// A session grant made while the first pod is connected...
	id := cx.ask(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "one"})
	if code, out := cx.decide(id, owner, choice("session")); code != http.StatusOK {
		t.Fatalf("decide = %d %v", code, out)
	}
	g, err := db.FindDelegationGrant(context.Background(), cx.pool, cx.cparent, "claude", runnerRuntimeCluster, clusterDaemonID)
	if err != nil || g == nil {
		t.Fatalf("the grant is not keyed on the cluster: %v %v", g, err)
	}
	// ...still starts a child once the parent runs in a new pod.
	cx.podTakesOver(cx.cparent, "claude")
	code, out, _ := cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"no_repo": true, "prompt": "two"}, newUUID())
	if code != http.StatusCreated || out["via"] != "grant:"+g.ID {
		t.Fatalf("start after a new pod = %d %v", code, out)
	}
	// A request asked under one pod is decided under the next.
	req := cx.ask(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "three", "engine": "codex"})
	cx.podTakesOver(cx.cparent, "claude")
	if code, out := cx.decide(req, owner, choice("always")); code != http.StatusOK || asString(out["child_session_id"]) == "" {
		t.Fatalf("decide after a new pod = %d %v", code, out)
	}
	rules, err := db.CandidateDelegationRules(context.Background(), cx.pool, "owner-1", "codex", runnerRuntimeCluster, clusterDaemonID)
	if err != nil || len(rules) != 1 {
		t.Fatalf("the card's rule is not keyed on the cluster: %v %v", rules, err)
	}
}

// `blerg-runner budget` in a real Hermes pod: the cluster's budget, with Hermes local when the
// operator's shared providers are local and the owner saved no Hermes credential of their own.
func TestClusterParentBudgetInARealHermesPod(t *testing.T) {
	t.Setenv("BLERG_RUNNER_DATA_DIR", t.TempDir())
	cx := newClusterDelegFx(t)
	cx.hub.JobManager().Hermes = hermesShared(sharedHermesFragment)
	parent, tok := cx.session(sessOpts{owner: "owner-1", runtime: runnerRuntimeCluster, title: "Hermes parent"})
	if err := db.SetSessionEngine(context.Background(), cx.pool, parent, "hermes"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionLaunchAuthSession(context.Background(), cx.pool, parent, clusterParentSID); err != nil {
		t.Fatal(err)
	}
	pod := cx.podTakesOver(parent, "hermes")
	pod.SetLocalEngines([]string{"hermes"})
	code, body, _ := cx.do("GET", "/api/sessions/"+parent+"/budget", tok, nil, nil)
	if code != http.StatusOK || body["daemon"] != clusterDaemonLabel {
		t.Fatalf("budget = %d %v", code, body)
	}
	engines, _ := body["engines"].([]any)
	var hermes map[string]any
	for _, e := range engines {
		if m, _ := e.(map[string]any); m["engine"] == "hermes" {
			hermes = m
		}
	}
	if hermes == nil || hermes["local"] != true || hermes["available"] != true {
		t.Fatalf("hermes = %v (engines %v)", hermes, engines)
	}
}

// A desktop daemon (the install-wide daemon token, no runtime token) that sends session_started for
// a cluster session id is refused: the row stays where it is, routing does not move to it, it is
// sent no token, and the session is not taken over as a cluster parent through it.
func TestDesktopDaemonCannotTakeOverAClusterSession(t *testing.T) {
	cx := newClusterDelegFx(t)
	ctx := context.Background()
	before := cx.row(cx.cparent)
	// Between pods: the session's own pod is gone and its messaging tokens are revoked, the case in
	// which a heal would mint a fresh one for whoever claims the session.
	cx.hub.UnregisterIfCurrent(cx.pod)
	if _, err := cx.pool.Exec(ctx, `UPDATE sessions SET status = 'idle' WHERE id = $1::uuid`, cx.cparent); err != nil {
		t.Fatal(err)
	}
	if _, err := cx.pool.Exec(ctx, `UPDATE board_tokens SET revoked_at = now() WHERE session_id = $1::uuid`, cx.cparent); err != nil {
		t.Fatal(err)
	}
	cx.dc.SetSessionTokenFile(true)
	drainFrames(cx.dc)
	HandleSessionStarted(ctx, cx.hub, cx.pool, cx.dc.ID, protocol.SessionStarted{Type: "session_started", SessionID: cx.cparent, Kind: "agent"})
	after := cx.row(cx.cparent)
	if after.DaemonID != before.DaemonID || after.Status != "idle" {
		t.Fatalf("the desktop daemon moved the cluster row: daemon %s -> %s, status %s", before.DaemonID, after.DaemonID, after.Status)
	}
	if owner := cx.hub.FindDaemonForSession(cx.cparent); owner == cx.dc {
		t.Fatal("routing moved to the desktop daemon")
	}
	if n := cx.count(`SELECT count(*)::int FROM board_tokens WHERE session_id = $1::uuid AND revoked_at IS NULL`, cx.cparent); n != 0 {
		t.Fatalf("a token was minted for the cluster session: %d live", n)
	}
	for _, f := range drainFrames(cx.dc) {
		if strings.Contains(string(f), cx.cparent) {
			t.Fatalf("the desktop daemon was sent a frame for the cluster session: %s", f)
		}
	}
	// The SQL upsert refuses it on its own too (no runtime authority on a cluster row).
	if err := db.ReviveSessionAs(ctx, cx.pool, cx.cparent, cx.dc.ID, "starting", "", "", "", "", db.SessionOrigin{}); err == nil {
		t.Fatal("the upsert let a daemon without runtime authority move a cluster row")
	}
	if got := cx.row(cx.cparent).DaemonID; got != before.DaemonID {
		t.Fatalf("daemon_id = %s, want %s", got, before.DaemonID)
	}
	// A real pod still takes it over.
	cx.podTakesOver(cx.cparent, "claude")
}

// podFrame delivers raw as the pod's own frame, through the same handler (and runtime authority)
// the daemon read loop uses.
func (cx *clusterDelegFx) podFrame(pod *DaemonConn, raw []byte) {
	cx.t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		cx.t.Fatal(err)
	}
	if !handleDaemonFrame(context.Background(), cx.hub, pod, cx.pool, raw, env) {
		cx.t.Fatalf("the pod's %s frame was refused", env.Type)
	}
}

// drainFrames empties dc's send queue and returns what was in it.
func drainFrames(dc *DaemonConn) [][]byte {
	var out [][]byte
	for {
		select {
		case f := <-dc.send:
			out = append(out, f)
		default:
			return out
		}
	}
}
