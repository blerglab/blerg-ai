package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// ask posts body for parent under a new key and returns the stored request's id (202 expected).
func (fx *delegFx) ask(parent, tok string, body any) string {
	fx.t.Helper()
	code, out, _ := fx.postChild(parent, tok, body, newUUID())
	if code != http.StatusAccepted {
		fx.t.Fatalf("ask: %d %v", code, out)
	}
	return asString(out["request_id"])
}

// decide posts a decision with tok; body is {"choice": choice} unless raw is given.
func (fx *delegFx) decide(id, tok string, body any) (int, map[string]any) {
	fx.t.Helper()
	code, out, _ := fx.do("POST", "/api/delegation-requests/"+id+"/decide", tok, body, nil)
	return code, out
}

func choice(c string) map[string]any { return map[string]any{"choice": c} }

// request reads a delegation request back.
func (fx *delegFx) request(id string) db.DelegationRequest {
	fx.t.Helper()
	r, err := db.GetDelegationRequest(context.Background(), fx.pool, id)
	if err != nil || r == nil {
		fx.t.Fatalf("request %s: %v", id, err)
	}
	return *r
}

// decidedEvent returns the delegation_decided event of request id in parent (nil when none).
func (fx *delegFx) decidedEvent(parent, id string) map[string]any {
	fx.t.Helper()
	var raw string
	err := fx.pool.QueryRow(context.Background(),
		`SELECT payload::text FROM agent_events WHERE session_id = $1::uuid AND client_event_id = $2 AND kind = 'delegation_decided'`,
		parent, delegationEventID(id, "delegation_decided")).Scan(&raw)
	if err != nil {
		return nil
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		fx.t.Fatal(err)
	}
	return ev
}

func hasKeys(t *testing.T, what string, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Fatalf("%s lacks %q: %v", what, k, m)
		}
	}
}

func TestOnlyTheOwnerWithASidDecides(t *testing.T) {
	fx := newDelegFx(t)
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	_, childTok := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	_, otherSessTok := fx.session(sessOpts{owner: "owner-1"})
	noSid := fx.coreToken(identity.Claims{Kind: "human", Sub: "owner-1", Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap}})
	onBehalf := fx.coreToken(identity.Claims{Kind: "human", Sub: "owner-2", OnBehalfOf: "owner-1", Sid: testSID,
		Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap}})
	agentBrowser := fx.coreToken(identity.Claims{Kind: agentPrincipalKind, Sub: "tok-owner-1", OnBehalfOf: "owner-1", Sid: testSID,
		Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap}})
	for name, tok := range map[string]string{
		"owner without sid":          noSid,
		"another person":             fx.human("owner-2"),
		"agent token for the owner":  fx.agentToken("owner-1"),
		"agent token with a browser": agentBrowser,
		"human on behalf of owner":   onBehalf,
		"parent's session token":     fx.parentTok,
		"a child's session token":    childTok,
		"another session's token":    otherSessTok,
		"runner key":                 runnerTestKey,
		"daemon token":               delegDaemonToken,
		"garbage":                    "not-a-token",
	} {
		code, out := fx.decide(id, tok, choice("once"))
		if code != http.StatusNotFound {
			t.Errorf("%s: %d %v, want 404", name, code, out)
		}
	}
	if code, _ := fx.decide(id, "", choice("once")); code != http.StatusUnauthorized {
		t.Errorf("no credential: %d, want 401", code)
	}
	if r := fx.request(id); r.Status != "pending" {
		t.Fatalf("a refused decision changed the request: %+v", r)
	}
	fx.noSpawn()
	code, out := fx.decide(id, fx.human("owner-1"), choice("once"))
	if code != http.StatusOK || out["status"] != "allowed" {
		t.Fatalf("owner with sid: %d %v", code, out)
	}
	fx.spawn()
	if r := fx.request(id); r.DecidedBy != "owner-1" || r.DecidedSid != testSID {
		t.Fatalf("decided by %q sid %q", r.DecidedBy, r.DecidedSid)
	}
}

func TestDecideIsCompareAndSet(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	// Concurrent decisions: exactly one wins, one child.
	var wg sync.WaitGroup
	codes := make(chan int, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := fx.decide(id, owner, choice("once"))
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	won, lost := 0, 0
	for c := range codes {
		switch c {
		case http.StatusOK:
			won++
		case http.StatusConflict:
			lost++
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}
	if won != 1 || lost != 4 {
		t.Fatalf("won %d lost %d", won, lost)
	}
	fx.spawn()
	fx.noSpawn()
	code, out := fx.decide(id, owner, choice("deny"))
	if code != http.StatusConflict || out["status"] != "allowed" || asString(out["child_session_id"]) == "" {
		t.Fatalf("second decision: %d %v", code, out)
	}
	if n := fx.count(`SELECT count(*)::int FROM sessions WHERE parent_session_id = $1::uuid`, fx.parent); n != 1 {
		t.Fatalf("%d children", n)
	}

	// Decide after expire.
	id2 := fx.ask(fx.parent, fx.parentTok, claudeBody("Other"))
	if code, out, _ := fx.do("POST", "/api/delegation-requests/"+id2+"/expire", fx.parentTok, nil, nil); code != http.StatusOK {
		t.Fatalf("expire: %d %v", code, out)
	}
	code, out = fx.decide(id2, owner, choice("once"))
	if code != http.StatusConflict || out["status"] != "expired" {
		t.Fatalf("decide after expire: %d %v", code, out)
	}
	// Decide after the deadline (the sweeper has not run yet): also refused.
	id3 := fx.ask(fx.parent, fx.parentTok, claudeBody("Third"))
	if _, err := fx.pool.Exec(context.Background(), `UPDATE delegation_requests SET expires_at = now() - interval '1 second' WHERE id = $1::uuid`, id3); err != nil {
		t.Fatal(err)
	}
	if code, out = fx.decide(id3, owner, choice("once")); code != http.StatusConflict {
		t.Fatalf("decide after the deadline: %d %v", code, out)
	}
	fx.noSpawn()
}

func TestDecideRefusesAChoiceNotOffered(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	untrusted, untrustedTok := fx.session(sessOpts{owner: "owner-1", startedByKind: "agent"})
	id := fx.ask(untrusted, untrustedTok, codexBody())
	if got := fx.request(id).Offered; fmt.Sprint(got) != "[once deny]" {
		t.Fatalf("offered %v", got)
	}
	for _, c := range []string{"session", "always", "maybe", ""} {
		if code, out := fx.decide(id, owner, choice(c)); code != http.StatusUnprocessableEntity {
			t.Errorf("untrusted %q: %d %v", c, code, out)
		}
	}
	// A host child of a docker parent: per request only.
	host := codexBody()
	host["runtime"] = "daemon"
	id2 := fx.ask(fx.parent, fx.parentTok, host)
	if code, out := fx.decide(id2, owner, choice("always")); code != http.StatusUnprocessableEntity {
		t.Errorf("host child always: %d %v", code, out)
	}
	if code, out := fx.decide(id2, owner, choice("session")); code != http.StatusUnprocessableEntity {
		t.Errorf("host child session: %d %v", code, out)
	}
	for _, rid := range []string{id, id2} {
		if r := fx.request(rid); r.Status != "pending" {
			t.Fatalf("refused choice changed %s: %+v", rid, r)
		}
	}
	if n := fx.count(`SELECT count(*)::int FROM delegation_rules`) + fx.count(`SELECT count(*)::int FROM delegation_grants`); n != 0 {
		t.Fatalf("%d rules/grants stored", n)
	}
	fx.noSpawn()
	if code, _ := fx.decide(id, owner, choice("deny")); code != http.StatusOK {
		t.Fatalf("deny: %d", code)
	}
	if r := fx.request(id); r.Status != "denied" || r.Choice != "" {
		t.Fatalf("denied row %+v", r)
	}
	fx.noSpawn()
}

func TestDecideStartsFromStoredRequest(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	owner := fx.human("owner-1")
	id := fx.ask(fx.parent, fx.parentTok, map[string]any{"engine": "codex", "repo": "app", "prompt": "the asked prompt"})
	// The stored row is the only source: change it, and the start must follow it.
	if _, err := fx.pool.Exec(ctx, `UPDATE delegation_requests SET request = jsonb_set(request, '{prompt}', '"the stored prompt"') WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	if code, out := fx.decide(id, owner, map[string]any{"choice": "once", "prompt": "a decide-body prompt"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("decide body with a prompt: %d %v", code, out)
	}
	if code, out := fx.decide(id, owner, `{"choice":"once","engine":"claude"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("decide body with an engine: %d %v", code, out)
	}
	fx.noSpawn()
	code, out := fx.decide(id, owner, choice("once"))
	if code != http.StatusOK {
		t.Fatalf("decide: %d %v", code, out)
	}
	hasKeys(t, "decide reply", out, "request_id", "status", "choice", "child_session_id")
	if _, ok := out["start_error"]; ok {
		t.Fatalf("a started child's reply carries start_error: %v", out)
	}
	child := asString(out["child_session_id"])
	if out["request_id"] != id || out["status"] != "allowed" || out["choice"] != "once" || child == "" {
		t.Fatalf("reply %v", out)
	}
	spawn := fx.spawn()
	if spawn.InitialPrompt != "the stored prompt" || spawn.SessionID != child || spawn.Engine != "codex" {
		t.Fatalf("spawn prompt %q engine %q id %s (child %s)", spawn.InitialPrompt, spawn.Engine, spawn.SessionID, child)
	}
	if len(spawn.ExtraEnv) > 0 {
		t.Fatalf("spawn carries env: %+v", spawn.ExtraEnv)
	}
	r := fx.request(id)
	if r.ChildSessionID != child || r.StartError != "" {
		t.Fatalf("row %+v", r)
	}
	row := fx.row(child)
	if row.DelegationVia != "request:"+id || derefOrEmpty(row.ParentSessionID) != fx.parent {
		t.Fatalf("child via %q parent %v", row.DelegationVia, row.ParentSessionID)
	}
	ev := fx.decidedEvent(fx.parent, id)
	if ev == nil || ev["request_id"] != id || ev["status"] != "allowed" || ev["choice"] != "once" || ev["child_session_id"] != child {
		t.Fatalf("decided event %v", ev)
	}
	if _, ok := ev["start_error"]; ok {
		t.Fatalf("event carries start_error: %v", ev)
	}
	// The child's start is idempotent in the server's own scope, never the parent's.
	if n := fx.count(`SELECT count(*)::int FROM runner_idempotency WHERE scope = $1 AND key = $2`,
		delegateDecideScope(fx.parent), "dr:"+id); n != 1 {
		t.Fatalf("decide scope records %d", n)
	}
	if n := fx.count(`SELECT count(*)::int FROM runner_idempotency WHERE scope = $1`, sessionDelegateKind+":"+fx.parent); n != 0 {
		t.Fatalf("the parent's own scope got %d records", n)
	}
}

func TestDelegateStartProblemAllowsOnlyTheDecideScope(t *testing.T) {
	p := runnerPrincipal{Kind: sessionDelegateKind, Sub: "p1"}
	base := runnerStartRequest{Runtime: runnerRuntimeDocker, DaemonID: "d1", ParentSessionID: "p1"}
	ok := base
	ok.IdemScope = delegateDecideScope("p1")
	if e := delegateStartProblem(ok, p); e != nil {
		t.Fatalf("decide scope refused: %v", e.Message)
	}
	for _, scope := range []string{delegateDecideScope("p2"), sessionDelegateKind + ":p1", "cron:x", "session_delegate_decide:"} {
		bad := base
		bad.IdemScope = scope
		if e := delegateStartProblem(bad, p); e == nil {
			t.Errorf("scope %q accepted", scope)
		}
	}
}

func TestDecideStartFailureIsRecorded(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	fx.hub.UnregisterIfCurrent(fx.dc)
	code, out := fx.decide(id, owner, choice("once"))
	if code != http.StatusOK || out["status"] != "allowed" || out["start_error"] != msgDelegateDisconnected {
		t.Fatalf("decide: %d %v", code, out)
	}
	if asString(out["child_session_id"]) != "" {
		t.Fatalf("a child id with a failed start: %v", out)
	}
	r := fx.request(id)
	if r.Status != "allowed" || r.StartError != msgDelegateDisconnected || r.ChildSessionID != "" {
		t.Fatalf("row %+v", r)
	}
	ev := fx.decidedEvent(fx.parent, id)
	if ev == nil || ev["start_error"] != msgDelegateDisconnected || ev["status"] != "allowed" {
		t.Fatalf("event %v", ev)
	}
	if _, ok := ev["child_session_id"]; ok {
		t.Fatalf("event names a child: %v", ev)
	}
	// The waiting parent sees the outcome at once.
	code, got, _ := fx.do("GET", "/api/delegation-requests/"+id+"?wait=5", fx.parentTok, nil, nil)
	if code != http.StatusOK || got["start_error"] != msgDelegateDisconnected {
		t.Fatalf("wait: %d %v", code, got)
	}
}

func TestDecideStartRechecksTheParentAndLimits(t *testing.T) {
	t.Setenv("BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR", "20")
	fx := newDelegFx(t)
	ctx := context.Background()
	owner := fx.human("owner-1")
	// The parent ended between the request and the decision.
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status = 'stopped', ended_at = now() WHERE id = $1::uuid`, fx.parent); err != nil {
		t.Fatal(err)
	}
	code, out := fx.decide(id, owner, choice("once"))
	if code != http.StatusOK || out["start_error"] != msgDelegateEnded {
		t.Fatalf("ended parent: %d %v", code, out)
	}
	fx.noSpawn()

	// Limits: three running children already; the decided start is refused with a start_error.
	p2, p2Tok := fx.session(sessOpts{owner: "owner-1"})
	id2 := fx.ask(p2, p2Tok, codexBody())
	for i := 0; i < maxRunningChildrenPerParent; i++ {
		fx.childOf(p2)
	}
	code, out = fx.decide(id2, owner, choice("once"))
	if code != http.StatusOK || !strings.Contains(asString(out["start_error"]), "stop one first") {
		t.Fatalf("over the limit: %d %v", code, out)
	}
	fx.noSpawn()

	// The request being decided is not counted twice: at 20-1 children plus this
	// request, the start still goes through.
	p3, p3Tok := fx.session(sessOpts{owner: "owner-1"})
	for i := 0; i < 20-1; i++ {
		c := fx.childOf(p3)
		if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET ended_at = now(), status = 'stopped' WHERE id = $1::uuid`, c); err != nil {
			t.Fatal(err)
		}
	}
	id3 := fx.ask(p3, p3Tok, codexBody())
	code, out = fx.decide(id3, owner, choice("once"))
	if code != http.StatusOK || asString(out["child_session_id"]) == "" {
		t.Fatalf("the last start: %d %v", code, out)
	}
	fx.spawn()
}

func TestAllowOnceIsSingleUse(t *testing.T) {
	fx := newDelegFx(t)
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	if code, out := fx.decide(id, fx.human("owner-1"), choice("once")); code != http.StatusOK {
		t.Fatalf("decide: %d %v", code, out)
	}
	fx.spawn()
	code, out, _ := fx.postChild(fx.parent, fx.parentTok, codexBody(), newUUID())
	if code != http.StatusAccepted || asString(out["request_id"]) == id {
		t.Fatalf("second identical start: %d %v", code, out)
	}
	fx.noSpawn()
}

func TestAllowForSessionCreatesGrant(t *testing.T) {
	for _, approvedNoRepo := range []bool{false, true} {
		t.Run(fmt.Sprintf("approved_no_repo_%t", approvedNoRepo), func(t *testing.T) {
			fx := newDelegFx(t)
			body := codexBody()
			if approvedNoRepo {
				delete(body, "repo")
				body["no_repo"] = true
			}
			id := fx.ask(fx.parent, fx.parentTok, body)
			if code, out := fx.decide(id, fx.human("owner-1"), choice("session")); code != http.StatusOK {
				t.Fatalf("decide: %d %v", code, out)
			}
			fx.spawn()
			grants, err := db.ListLiveDelegationGrants(context.Background(), fx.pool, fx.parent)
			if err != nil || len(grants) != 1 {
				t.Fatalf("grants %v %v", grants, err)
			}
			g := grants[0]
			if g.Engine != "codex" || g.Runtime != runnerRuntimeDocker || g.DaemonID != fx.dc.ID || g.NoRepo != approvedNoRepo || (approvedNoRepo && g.Repo != "") || (!approvedNoRepo && g.Repo != "app") ||
				g.CreatedFromRequest != id || g.AccountID != "owner-1" {
				t.Fatalf("grant %+v", g)
			}
			other := codexBody()
			other["prompt"] = "another prompt"
			other["model"] = ""
			code, out, _ := fx.postChild(fx.parent, fx.parentTok, other, newUUID())
			if code != http.StatusCreated || out["via"] != "grant:"+g.ID {
				t.Fatalf("next start: %d %v", code, out)
			}
			fx.spawn()
			noRepo := map[string]any{"engine": "codex", "no_repo": true, "prompt": "outside the repo"}
			if code, out, _ := fx.postChild(fx.parent, fx.parentTok, noRepo, newUUID()); code != http.StatusCreated || out["via"] != "grant:"+g.ID {
				t.Fatalf("no-repo grant start: %d %v", code, out)
			}
			fx.spawn()
		})
	}
}

func TestAlwaysCreatesRule(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	owner := fx.human("owner-1")
	fx.dc.SetLocalEngines([]string{"hermes"})
	hermes := map[string]any{"engine": "hermes", "repo": "app", "prompt": "summarize"}
	id := fx.ask(fx.parent, fx.parentTok, hermes)
	if code, out := fx.decide(id, owner, choice("always")); code != http.StatusOK || asString(out["child_session_id"]) == "" {
		t.Fatalf("decide: %d %v", code, out)
	}
	fx.spawn()
	rules, err := db.ListDelegationRules(ctx, fx.pool, "owner-1")
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules %v %v", rules, err)
	}
	r := rules[0]
	if r.Engine != "hermes" || r.Runtime != runnerRuntimeDocker || r.DaemonID != fx.dc.ID || !r.LocalOnly || r.CreatedVia != "card" {
		t.Fatalf("rule %+v", r)
	}
	p2, p2Tok := fx.session(sessOpts{owner: "owner-1", title: "Another"})
	code, out, _ := fx.postChild(p2, p2Tok, hermes, newUUID())
	if code != http.StatusCreated || out["via"] != "rule:"+r.ID {
		t.Fatalf("another direct parent: %d %v", code, out)
	}
	fx.spawn()
	// Codex from a card: not local-only.
	id2 := fx.ask(fx.parent, fx.parentTok, codexBody())
	if code, out := fx.decide(id2, owner, choice("always")); code != http.StatusOK {
		t.Fatalf("decide codex: %d %v", code, out)
	}
	fx.spawn()
	rules, _ = db.ListDelegationRules(ctx, fx.pool, "owner-1")
	if len(rules) != 2 || rules[1].Engine != "codex" || rules[1].LocalOnly {
		t.Fatalf("rules %+v", rules)
	}
}

func TestWaitLongPollWakesOnDecision(t *testing.T) {
	fx := newDelegFx(t)
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	start := time.Now()
	code, out, _ := fx.do("GET", "/api/delegation-requests/"+id, fx.parentTok, nil, nil)
	if code != http.StatusOK || out["status"] != "pending" || time.Since(start) > time.Second {
		t.Fatalf("no wait: %d %v after %v", code, out, time.Since(start))
	}
	type res struct {
		code int
		out  map[string]any
		took time.Duration
	}
	done := make(chan res, 1)
	go func() {
		s := time.Now()
		c, o, _ := fx.do("GET", "/api/delegation-requests/"+id+"?wait=5", fx.parentTok, nil, nil)
		done <- res{c, o, time.Since(s)}
	}()
	time.Sleep(300 * time.Millisecond)
	if code, out := fx.decide(id, fx.human("owner-1"), choice("once")); code != http.StatusOK {
		t.Fatalf("decide: %d %v", code, out)
	}
	select {
	case r := <-done:
		if r.code != http.StatusOK || r.out["status"] != "allowed" || asString(r.out["child_session_id"]) == "" {
			t.Fatalf("wait: %d %v", r.code, r.out)
		}
		if r.took < 250*time.Millisecond || r.took > 2*time.Second {
			t.Fatalf("wait took %v", r.took)
		}
		hasKeys(t, "wait reply", r.out, "request_id", "status", "choice", "child_session_id", "start_error", "expires_at")
	case <-time.After(15 * time.Second):
		t.Fatal("the wait did not wake on the decision")
	}
	fx.spawn()
}

func TestWaitCapsTheHold(t *testing.T) {
	fx := newDelegFx(t)
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	start := time.Now()
	code, out, _ := fx.do("GET", "/api/delegation-requests/"+id+"?wait=1", fx.parentTok, nil, nil)
	if took := time.Since(start); code != http.StatusOK || out["status"] != "pending" || took < 900*time.Millisecond || took > 3*time.Second {
		t.Fatalf("wait=1: %d %v after %v", code, out, took)
	}
	if got := delegationHold("600"); got != delegationMaxWait {
		t.Fatalf("hold for 600 = %v", got)
	}
	if got := delegationHold(""); got != 0 {
		t.Fatalf("hold for none = %v", got)
	}
	if got := delegationHold("-3"); got != 0 {
		t.Fatalf("hold for -3 = %v", got)
	}
}

func TestWaitAccess(t *testing.T) {
	fx := newDelegFx(t)
	prompt := strings.Repeat("long prompt ", 100)
	id := fx.ask(fx.parent, fx.parentTok, map[string]any{"engine": "codex", "repo": "app", "prompt": prompt})
	_, otherTok := fx.session(sessOpts{owner: "owner-1"})
	_, childTok := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	path := "/api/delegation-requests/" + id

	code, out, _ := fx.do("GET", path, fx.parentTok, nil, nil)
	if code != http.StatusOK || out["request_id"] != id {
		t.Fatalf("parent: %d %v", code, out)
	}
	hasKeys(t, "parent reply", out, "request_id", "status", "choice", "child_session_id", "start_error", "expires_at")
	for _, k := range []string{"request", "offered", "parent_session_id"} {
		if _, ok := out[k]; ok {
			t.Fatalf("the parent's reply carries %q: %v", k, out)
		}
	}
	code, out, _ = fx.do("GET", path, fx.human("owner-1"), nil, nil)
	if code != http.StatusOK || out["parent_session_id"] != fx.parent || fmt.Sprint(out["offered"]) != "[once session always deny]" {
		t.Fatalf("owner: %d %v", code, out)
	}
	if req, _ := out["request"].(map[string]any); req["prompt"] != prompt || req["engine"] != "codex" {
		t.Fatalf("owner's request %v", out["request"])
	}
	for name, tok := range map[string]string{
		"another session's token": otherTok,
		"a child's token":         childTok,
		"another person":          fx.human("owner-2"),
		"agent token for owner":   fx.agentToken("owner-1"),
		"runner key":              runnerTestKey,
		"daemon token":            delegDaemonToken,
	} {
		if code, out, _ := fx.do("GET", path, tok, nil, nil); code != http.StatusNotFound {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if code, _, _ := fx.do("GET", path, "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("no credential: %d", code)
	}
	if code, _, _ := fx.do("GET", "/api/delegation-requests/not-a-uuid", fx.parentTok, nil, nil); code != http.StatusNotFound {
		t.Errorf("bad id: %d", code)
	}
	if code, _, _ := fx.do("GET", "/api/delegation-requests/"+newUUID(), fx.human("owner-1"), nil, nil); code != http.StatusNotFound {
		t.Errorf("unknown id: %d", code)
	}
}

func TestExpireAfterAllowAnswers409WithChild(t *testing.T) {
	fx := newDelegFx(t)
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	code, out := fx.decide(id, fx.human("owner-1"), choice("once"))
	if code != http.StatusOK {
		t.Fatalf("decide: %d %v", code, out)
	}
	child := asString(out["child_session_id"])
	fx.spawn()
	code, out, _ = fx.do("POST", "/api/delegation-requests/"+id+"/expire", fx.parentTok, nil, nil)
	if code != http.StatusConflict || out["status"] != "allowed" || out["child_session_id"] != child || out["request_id"] != id {
		t.Fatalf("expire after allow: %d %v", code, out)
	}
	if r := fx.request(id); r.Status != "allowed" {
		t.Fatalf("expire changed an allowed request: %+v", r)
	}

	// A pending request: only the parent's token expires it.
	id2 := fx.ask(fx.parent, fx.parentTok, claudeBody("Two"))
	_, otherTok := fx.session(sessOpts{owner: "owner-1"})
	for name, tok := range map[string]string{
		"owner browser":           fx.human("owner-1"),
		"another session's token": otherTok,
		"agent token":             fx.agentToken("owner-1"),
		"runner key":              runnerTestKey,
	} {
		if code, out, _ := fx.do("POST", "/api/delegation-requests/"+id2+"/expire", tok, nil, nil); code != http.StatusNotFound {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if code, _, _ := fx.do("POST", "/api/delegation-requests/"+id2+"/expire", "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("no credential: %d", code)
	}
	code, out, _ = fx.do("POST", "/api/delegation-requests/"+id2+"/expire", fx.parentTok, nil, nil)
	if code != http.StatusOK || out["status"] != "expired" || out["request_id"] != id2 {
		t.Fatalf("expire: %d %v", code, out)
	}
	if ev := fx.decidedEvent(fx.parent, id2); ev == nil || ev["status"] != "expired" {
		t.Fatalf("expire event %v", ev)
	}
	code, out, _ = fx.do("POST", "/api/delegation-requests/"+id2+"/expire", fx.parentTok, nil, nil)
	if code != http.StatusConflict || out["status"] != "expired" {
		t.Fatalf("second expire: %d %v", code, out)
	}
	if _, ok := out["child_session_id"]; ok {
		t.Fatalf("an expired request names a child: %v", out)
	}
}

// An expire that races an allow: whichever lands first, the other answers 409, and an allow that
// won hands the child back to the expire.
func TestExpireRacingAllow(t *testing.T) {
	for i := 0; i < 6; i++ {
		fx := newDelegFx(t)
		id := fx.ask(fx.parent, fx.parentTok, codexBody())
		var wg sync.WaitGroup
		var dCode, eCode int
		var dOut, eOut map[string]any
		wg.Add(2)
		go func() { defer wg.Done(); dCode, dOut = fx.decide(id, fx.human("owner-1"), choice("once")) }()
		go func() {
			defer wg.Done()
			eCode, eOut, _ = fx.do("POST", "/api/delegation-requests/"+id+"/expire", fx.parentTok, nil, nil)
		}()
		wg.Wait()
		switch {
		case dCode == http.StatusOK && eCode == http.StatusConflict:
			if eOut["status"] != "allowed" || eOut["child_session_id"] != dOut["child_session_id"] || asString(dOut["child_session_id"]) == "" {
				t.Fatalf("allow won, expire said %v (decide %v)", eOut, dOut)
			}
			fx.spawn()
		case eCode == http.StatusOK && dCode == http.StatusConflict:
			if dOut["status"] != "expired" {
				t.Fatalf("expire won, decide said %v", dOut)
			}
			fx.noSpawn()
		default:
			t.Fatalf("decide %d %v, expire %d %v", dCode, dOut, eCode, eOut)
		}
	}
}

func TestSweeperExpiresStaleRequests(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	fresh := fx.ask(fx.parent, fx.parentTok, claudeBody("Fresh"))
	if _, err := fx.pool.Exec(ctx, `UPDATE delegation_requests SET expires_at = now() - interval '1 minute' WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan map[string]any, 1)
	go func() {
		_, o, _ := fx.do("GET", "/api/delegation-requests/"+id+"?wait=10", fx.parentTok, nil, nil)
		done <- o
	}()
	time.Sleep(200 * time.Millisecond)
	if n := fx.api.maintainDelegations(ctx); n != 1 {
		t.Fatalf("maintained %d", n)
	}
	select {
	case o := <-done:
		if o["status"] != "expired" {
			t.Fatalf("waiter saw %v", o)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the sweeper did not wake the waiter")
	}
	if r := fx.request(id); r.Status != "expired" {
		t.Fatalf("row %+v", r)
	}
	if r := fx.request(fresh); r.Status != "pending" {
		t.Fatalf("a fresh request was expired: %+v", r)
	}
	if ev := fx.decidedEvent(fx.parent, id); ev == nil || ev["status"] != "expired" || ev["request_id"] != id {
		t.Fatalf("event %v", ev)
	}

	// An allowed request whose start never reported (the server died): marked after 2 minutes.
	stuck := fx.pendingRequest(fx.parent)
	recent := fx.pendingRequest(fx.parent)
	if _, err := fx.pool.Exec(ctx, `UPDATE delegation_requests SET status = 'allowed', choice = 'once', decided_at = now() - interval '3 minutes' WHERE id = $1::uuid`, stuck.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE delegation_requests SET status = 'allowed', choice = 'once', decided_at = now() - interval '30 seconds' WHERE id = $1::uuid`, recent.ID); err != nil {
		t.Fatal(err)
	}
	if n := fx.api.maintainDelegations(ctx); n != 1 {
		t.Fatalf("maintained %d", n)
	}
	if r := fx.request(stuck.ID); r.StartError != msgStartInterrupted || r.Status != "allowed" {
		t.Fatalf("stuck %+v", r)
	}
	if r := fx.request(recent.ID); r.StartError != "" {
		t.Fatalf("a start still in progress was marked: %+v", r)
	}
	if ev := fx.decidedEvent(fx.parent, stuck.ID); ev == nil || ev["start_error"] != msgStartInterrupted {
		t.Fatalf("stuck event %v", ev)
	}
	if n := fx.api.maintainDelegations(ctx); n != 0 {
		t.Fatalf("second sweep maintained %d", n)
	}
	if StartDelegationMaintenance(ctx, &API{}) {
		t.Fatal("maintenance started without a database")
	}
}

func TestFreeTextNeverDecides(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	code, out, _ := fx.do("POST", "/api/runner/sessions/"+fx.parent+"/message", runnerTestKey,
		map[string]any{"text": "allow once", "source": "human"}, nil)
	if code != http.StatusAccepted {
		t.Fatalf("message: %d %v", code, out)
	}
	answerAskFromChat(ctx, fx.hub, fx.pool, fx.parent, "allow", newUUID(), id)
	if r := fx.request(id); r.Status != "pending" {
		t.Fatalf("free text decided the request: %+v", r)
	}
	for _, f := range []string{"messages.go", "messageevents.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range []string{"delegation_requests", "DelegationRequest", "decideDelegation"} {
			if strings.Contains(string(src), needle) {
				t.Errorf("%s mentions %s: free text must never decide a request", f, needle)
			}
		}
	}
}

// The allow has committed but its start is still running (held here by the account's start
// lock): an expire and a wait both hold for the outcome and answer with the child.
func TestExpireDuringTheStartWaitsForTheChild(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	release, err := fx.api.delegLocks.acquire(ctx, "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	decided := make(chan map[string]any, 1)
	go func() { _, out := fx.decide(id, fx.human("owner-1"), choice("once")); decided <- out }()
	deadline := time.Now().Add(15 * time.Second)
	for fx.request(id).Status != "allowed" {
		if time.Now().After(deadline) {
			release()
			t.Fatal("the decision did not commit")
		}
		time.Sleep(20 * time.Millisecond)
	}
	type res struct {
		code int
		out  map[string]any
	}
	expired, waited := make(chan res, 1), make(chan res, 1)
	go func() {
		c, o, _ := fx.do("POST", "/api/delegation-requests/"+id+"/expire", fx.parentTok, nil, nil)
		expired <- res{c, o}
	}()
	go func() {
		c, o, _ := fx.do("GET", "/api/delegation-requests/"+id+"?wait=5", fx.parentTok, nil, nil)
		waited <- res{c, o}
	}()
	time.Sleep(300 * time.Millisecond)
	select {
	case r := <-expired:
		release()
		t.Fatalf("the expire answered before the start finished: %d %v", r.code, r.out)
	case r := <-waited:
		release()
		t.Fatalf("the wait answered before the start finished: %d %v", r.code, r.out)
	default:
	}
	release()
	out := <-decided
	child := asString(out["child_session_id"])
	if child == "" {
		t.Fatalf("decide %v", out)
	}
	fx.spawn()
	if r := <-expired; r.code != http.StatusConflict || r.out["child_session_id"] != child || r.out["status"] != "allowed" {
		t.Fatalf("expire %d %v", r.code, r.out)
	}
	if r := <-waited; r.code != http.StatusOK || r.out["child_session_id"] != child {
		t.Fatalf("wait %d %v", r.code, r.out)
	}
}

func TestPendingSessionApprovalsShareOneRevocableGrant(t *testing.T) {
	fx := newDelegFx(t)
	first := fx.ask(fx.parent, fx.parentTok, codexBody())
	second := fx.ask(fx.parent, fx.parentTok, map[string]any{"engine": "codex", "no_repo": true, "prompt": "work without a repo"})
	for _, id := range []string{first, second} {
		if code, out := fx.decide(id, fx.human("owner-1"), choice("session")); code != http.StatusOK {
			t.Fatalf("decide: %d %v", code, out)
		}
		fx.spawn()
	}
	grants, err := db.ListLiveDelegationGrants(t.Context(), fx.pool, fx.parent)
	if err != nil || len(grants) != 1 {
		t.Fatalf("duplicate effective grants: %+v %v", grants, err)
	}
	grant := grants[0]
	if grant.CreatedFromRequest != first || grant.Repo != "app" || grant.NoRepo {
		t.Fatalf("canonical approval audit changed: %+v", grant)
	}
	for _, id := range []string{first, second} {
		if got := fx.request(id); got.Status != "allowed" || got.Choice != "session" {
			t.Fatalf("approval audit lost: %+v", got)
		}
	}
	if ok, err := db.RevokeDelegationGrant(t.Context(), fx.pool, grant.ID, "owner-1"); err != nil || !ok {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if code, out, _ := fx.postChild(fx.parent, fx.parentTok, map[string]any{"engine": "codex", "no_repo": true, "prompt": "another request after revoke"}, newUUID()); code != http.StatusAccepted {
		t.Fatalf("revocation did not require new approval: %d %v", code, out)
	}
	fx.noSpawn()
}
