package server

import (
	"context"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func (fx *delegFx) approval(parent, token string) (int, map[string]any) {
	code, body, _ := fx.do("GET", "/api/sessions/"+parent+"/child-approval", token, nil, nil)
	return code, body
}
func (fx *delegFx) setApproval(parent, token, runtime string, enabled bool, revision string) (int, map[string]any) {
	code, body, _ := fx.do("PUT", "/api/sessions/"+parent+"/child-approval", token, map[string]any{"runtime": runtime, "enabled": enabled, "revision": revision}, nil)
	return code, body
}
func TestChildApprovalExactScopesAndIdempotence(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	code, view := fx.approval(fx.parent, owner)
	if code != http.StatusOK {
		t.Fatalf("GET %d", code)
	}
	revision := asString(view["revision"])
	if len(revision) != 64 {
		t.Fatal("missing bounded revision")
	}
	code, view = fx.setApproval(fx.parent, owner, "docker", true, revision)
	if code != 200 {
		t.Fatalf("enable %d", code)
	}
	grants, err := db.ListLiveDelegationGrants(context.Background(), fx.pool, fx.parent)
	if err != nil || len(grants) != 3 {
		t.Fatalf("grants %d %v", len(grants), err)
	}
	for _, g := range grants {
		if g.Runtime != "docker" || g.DaemonID != fx.dc.ID || g.CreatedFromRequest != "" {
			t.Fatal("incorrect grant scope/provenance")
		}
	}
	code, _ = fx.setApproval(fx.parent, owner, "docker", true, asString(view["revision"]))
	if code != 200 {
		t.Fatal(code)
	}
	if n := fx.count(`SELECT count(*) FROM delegation_grants WHERE parent_session_id=$1`, fx.parent); n != 3 {
		t.Fatal("duplicate grants")
	}
	code, view = fx.approval(fx.parent, owner)
	if code != 200 {
		t.Fatal(code)
	}
	code, _ = fx.setApproval(fx.parent, owner, "docker", false, asString(view["revision"]))
	if code != 200 {
		t.Fatal(code)
	}
	grants, err = db.ListLiveDelegationGrants(context.Background(), fx.pool, fx.parent)
	if err != nil || len(grants) != 0 {
		t.Fatal("revoke failed")
	}
	fx.noSpawn()
}
func TestChildApprovalRejectsNonOwnerCredentials(t *testing.T) {
	fx := newDelegFx(t)
	for name, token := range map[string]string{
		"owner without sid":       fx.coreToken(identity.Claims{Kind: "human", Sub: "owner-1", Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap}}),
		"delegated human":         fx.coreToken(identity.Claims{Kind: "human", Sub: "owner-2", OnBehalfOf: "owner-1", Sid: testSID, Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap}}),
		"agent with browser caps": fx.coreToken(identity.Claims{Kind: agentPrincipalKind, Sub: "fixture-agent", OnBehalfOf: "owner-1", Sid: testSID, Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap}}), "foreign": fx.human("owner-2"), "agent": fx.agentToken("owner-1"), "parent": fx.parentTok, "daemon": delegDaemonToken, "runner": runnerTestKey} {
		t.Run(name, func(t *testing.T) {
			for _, method := range []string{"GET", "PUT"} {
				code, _, _ := fx.do(method, "/api/sessions/"+fx.parent+"/child-approval", token, map[string]any{"runtime": "docker", "enabled": true, "revision": "invalid"}, nil)
				if code == 200 {
					t.Fatal("foreign credential authorized")
				}
			}
		})
	}
	if n := fx.count(`SELECT count(*) FROM delegation_grants`); n != 0 {
		t.Fatal("rejected caller mutated grants")
	}
}

func approvalRuntime(t *testing.T, view map[string]any, runtime string) map[string]any {
	t.Helper()
	rows, _ := view["runtimes"].([]any)
	for _, row := range rows {
		state, _ := row.(map[string]any)
		if state["runtime"] == runtime {
			return state
		}
	}
	t.Fatal("missing runtime")
	return nil
}
func TestChildApprovalPartialAndStandingRulesStaySeparate(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	original := fx.grant(fx.parent, "hermes", "docker", "app", false)
	rule := fx.rule("codex", "docker", false)
	_, view := fx.approval(fx.parent, owner)
	state := approvalRuntime(t, view, "docker")
	if state["partial"] != true || state["enabled"] != false {
		t.Fatal("existing session grant not reflected as partial")
	}
	if len(state["standing_rule_engines"].([]any)) != 1 {
		t.Fatal("standing rule missing")
	}
	code, out := fx.setApproval(fx.parent, owner, "docker", false, asString(view["revision"]))
	if code != 200 {
		t.Fatal(code)
	}
	state = approvalRuntime(t, out, "docker")
	if state["enabled"] != false || state["partial"] != false || len(state["effective_allowed_engines"].([]any)) != 1 {
		t.Fatal("revocation misrepresented inherited desktop rule")
	}
	if n := fx.count(`SELECT count(*) FROM delegation_rules WHERE id=$1 AND revoked_at IS NULL`, rule); n != 1 {
		t.Fatal("desktop rule changed")
	}
	if n := fx.count(`SELECT count(*) FROM delegation_grants WHERE id=$1 AND revoked_at IS NOT NULL`, original); n != 1 {
		t.Fatal("original grant audit lost")
	}
}
func TestChildApprovalUnsafeParentsCannotEnable(t *testing.T) {
	for name, options := range map[string]sessOpts{"private, agent-started": {owner: "owner-1", private: true, startedByKind: "agent"}, "board": {owner: "owner-1", boardID: "new"}, "child": {owner: "owner-1", parent: "placeholder"}, "agent-started": {owner: "owner-1", startedByKind: "agent"}, "ended": {owner: "owner-1", status: "ended"}} {
		t.Run(name, func(t *testing.T) {
			fx := newDelegFx(t)
			if options.parent != "" {
				options.parent = fx.parent
			}
			parent, _ := fx.session(options)
			owner := fx.human("owner-1")
			code, view := fx.approval(parent, owner)
			if code != 200 {
				t.Fatal(code)
			}
			if approvalRuntime(t, view, "docker")["can_enable"] != false {
				t.Fatal("unsafe parent offered enable")
			}
			code, _ = fx.setApproval(parent, owner, "docker", true, asString(view["revision"]))
			if code != 409 {
				t.Fatal(code)
			}
			if n := fx.count(`SELECT count(*) FROM delegation_grants`); n != 0 {
				t.Fatal("unsafe grant")
			}
		})
	}
	t.Run("restricted", func(t *testing.T) {
		fx := newDelegFx(t)
		if err := db.SetSessionRestrictTools(t.Context(), fx.pool, fx.parent); err != nil {
			t.Fatal(err)
		}
		owner := fx.human("owner-1")
		_, view := fx.approval(fx.parent, owner)
		if approvalRuntime(t, view, "docker")["can_enable"] != false {
			t.Fatal("restricted parent offered grant")
		}
	})
}

// BLG-338: a person's own launch that picked a board or MCP connection is private (visible only to
// its owner). That must not take away session approval: the owner can enable it, and the grant lands.
func TestChildApprovalPrivateOwnLaunchCanEnable(t *testing.T) {
	fx := newDelegFx(t)
	parent, _ := fx.session(sessOpts{owner: "owner-1", private: true})
	owner := fx.human("owner-1")
	code, view := fx.approval(parent, owner)
	if code != 200 {
		t.Fatal(code)
	}
	if approvalRuntime(t, view, "docker")["can_enable"] != true {
		t.Fatalf("private own launch cannot enable: %v", view)
	}
	code, _ = fx.setApproval(parent, owner, "docker", true, asString(view["revision"]))
	if code != 200 {
		t.Fatal(code)
	}
	if n := fx.count(`SELECT count(*) FROM delegation_grants WHERE revoked_at IS NULL`); n == 0 {
		t.Fatal("no grant for a private own launch")
	}
}

func TestChildApprovalRuntimeConfinementAndOfflineRevocation(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	_, view := fx.approval(fx.parent, owner)
	code, _ := fx.setApproval(fx.parent, owner, "daemon", true, asString(view["revision"]))
	if code != 409 {
		t.Fatal("sandbox parent widened confinement")
	}
	host, _ := fx.session(sessOpts{owner: "owner-1", runtime: "daemon"})
	_, view = fx.approval(host, owner)
	code, view = fx.setApproval(host, owner, "daemon", true, asString(view["revision"]))
	if code != 200 {
		t.Fatal(code)
	}
	if approvalRuntime(t, view, "docker")["enabled"] != false {
		t.Fatal("host consent silently enabled sandbox runtime")
	}
	fx.hub.UnregisterIfCurrent(fx.dc)
	_, view = fx.approval(host, owner)
	if approvalRuntime(t, view, "daemon")["can_enable"] != false {
		t.Fatal("offline enable offered")
	}
	code, _ = fx.setApproval(host, owner, "daemon", false, asString(view["revision"]))
	if code != 200 {
		t.Fatal("offline revoke refused")
	}
}
func TestChildApprovalRevisionRejectsNewEnginesAndParentEpoch(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	fx.dc.SetAvailableEngines([]string{"hermes"})
	_, view := fx.approval(fx.parent, owner)
	revision := asString(view["revision"])
	fx.dc.SetAvailableEngines([]string{"hermes", "codex"})
	code, _ := fx.setApproval(fx.parent, owner, "docker", true, revision)
	if code != 409 {
		t.Fatal("new engine silently authorized")
	}
	_, view = fx.approval(fx.parent, owner)
	revision = asString(view["revision"])
	now := time.Now()
	if err := db.EndSessionStatus(t.Context(), fx.pool, fx.parent, "ended", &now, systemEnd(db.EndReasonProcessExited)); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSessionAs(t.Context(), fx.pool, fx.parent, fx.dc.ID, "running", "/repos/app", "app", "Parent", "", db.SessionOrigin{}); err != nil {
		t.Fatal(err)
	}
	code, _ = fx.setApproval(fx.parent, owner, "docker", true, revision)
	if code != 409 {
		t.Fatal("old lifetime authorized new grant")
	}
	if n := fx.count(`SELECT count(*) FROM delegation_grants`); n != 0 {
		t.Fatal("stale write mutated authority")
	}
}
func TestChildApprovalConcurrentEditsHaveOneWinner(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	_, view := fx.approval(fx.parent, owner)
	revision := asString(view["revision"])
	codes := make(chan int, 2)
	for range 2 {
		go func() { code, _ := fx.setApproval(fx.parent, owner, "docker", true, revision); codes <- code }()
	}
	one, two := <-codes, <-codes
	if (one != 200 || two != 409) && (one != 409 || two != 200) {
		t.Fatalf("concurrent results %d/%d", one, two)
	}
	if n := fx.count(`SELECT count(*) FROM delegation_grants WHERE parent_session_id=$1`, fx.parent); n != 3 {
		t.Fatal("non-atomic grant set")
	}
	if n := fx.count(`SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='delegation_permissions_changed'`, fx.parent); n != 1 {
		t.Fatal("audit duplicated or missing")
	}
}
func TestChildApprovalLegacyRevokeSharesAccountFence(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	grant := fx.grant(fx.parent, "hermes", "docker", "app", false)
	_, view := fx.approval(fx.parent, owner)
	revision := asString(view["revision"])
	release, err := fx.api.delegLocks.acquire(t.Context(), "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	unlock := func() { once.Do(release) }
	defer unlock()
	done := make(chan int, 1)
	go func() {
		code, _, _ := fx.do("POST", "/api/delegation-grants/"+grant+"/revoke", owner, nil, nil)
		done <- code
	}()
	awaitLifecycle(t, func() bool {
		fx.api.delegLocks.mu.Lock()
		defer fx.api.delegLocks.mu.Unlock()
		entry := fx.api.delegLocks.m["owner-1"]
		return entry != nil && entry.refs == 2
	})
	if n := fx.count(`SELECT count(*) FROM delegation_grants WHERE id=$1 AND revoked_at IS NULL`, grant); n != 1 {
		t.Fatal("legacy revoke bypassed held account fence")
	}
	unlock()
	select {
	case code := <-done:
		if code != 204 {
			t.Fatal(code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("revoke blocked")
	}
	code, _ := fx.setApproval(fx.parent, owner, "docker", true, revision)
	if code != 409 {
		t.Fatal("stale enable recreated revoked authority")
	}
	if n := fx.count(`SELECT count(*) FROM delegation_grants WHERE revoked_at IS NULL`); n != 0 {
		t.Fatal("grant resurrected")
	}
}

func TestChildApprovalStartsUseGrantAndRevocationRequiresNewDecision(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	_, view := fx.approval(fx.parent, owner)
	code, view := fx.setApproval(fx.parent, owner, "docker", true, asString(view["revision"]))
	if code != 200 {
		t.Fatal(code)
	}
	code, child, _ := fx.postChild(fx.parent, fx.parentTok, codexBody(), "proactive-child-fixture")
	if code != 201 {
		t.Fatalf("approved start %d", code)
	}
	fx.spawn()
	via := asString(child["via"])
	if !strings.HasPrefix(via, "grant:") {
		t.Fatal("start did not use exact session grant")
	}
	code, _ = fx.setApproval(fx.parent, owner, "docker", false, asString(view["revision"]))
	if code != 200 {
		t.Fatal(code)
	}
	request := fx.ask(fx.parent, fx.parentTok, codexBody())
	if request == "" {
		t.Fatal("revoked start did not ask")
	}
	fx.noSpawn()
}
func TestChildApprovalRetainsRequestAuditAndOtherScopes(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	request := fx.ask(fx.parent, fx.parentTok, codexBody())
	code, _ := fx.decide(request, owner, choice("session"))
	if code != 200 {
		t.Fatal(code)
	}
	fx.spawn()
	before := fx.request(request)
	other, _ := fx.session(sessOpts{owner: "owner-1"})
	otherGrant := fx.grant(other, "hermes", "docker", "app", false)
	_, view := fx.approval(fx.parent, owner)
	code, _ = fx.setApproval(fx.parent, owner, "docker", true, asString(view["revision"]))
	if code != 200 {
		t.Fatal(code)
	}
	after := fx.request(request)
	if before.Status != after.Status || before.Choice != after.Choice || before.ChildSessionID != after.ChildSessionID {
		t.Fatal("completed decision rewritten")
	}
	if n := fx.count(`SELECT count(*) FROM delegation_grants WHERE parent_session_id=$1 AND created_from_request=$2`, fx.parent, request); n != 1 {
		t.Fatal("request provenance lost")
	}
	_, view = fx.approval(fx.parent, owner)
	code, _ = fx.setApproval(fx.parent, owner, "docker", false, asString(view["revision"]))
	if code != 200 {
		t.Fatal(code)
	}
	if n := fx.count(`SELECT count(*) FROM delegation_grants WHERE id=$1 AND revoked_at IS NULL`, otherGrant); n != 1 {
		t.Fatal("other parent authority revoked")
	}
}
func TestChildApprovalUnknownOrUnsupportedTargetsFailClosed(t *testing.T) {
	for name, change := range map[string]func(*delegFx){"old-daemon": func(fx *delegFx) { fx.dc.RecoveryReadyCapable = false }, "unknown-engines": func(fx *delegFx) { fx.dc.SetAvailableEngines(nil) }, "no-sandbox": func(fx *delegFx) { fx.dc.SetSandboxAvailable(false) }, "unregistered-engine": func(fx *delegFx) { fx.dc.SetAvailableEngines([]string{"unregistered-fixture"}) }} {
		t.Run(name, func(t *testing.T) {
			fx := newDelegFx(t)
			change(fx)
			owner := fx.human("owner-1")
			_, view := fx.approval(fx.parent, owner)
			if approvalRuntime(t, view, "docker")["can_enable"] != false {
				t.Fatal("unsupported target offered enable")
			}
			code, _ := fx.setApproval(fx.parent, owner, "docker", true, asString(view["revision"]))
			if code != 409 {
				t.Fatal(code)
			}
		})
	}
}
func TestChildApprovalStrictBodyAndReadDoNotMutate(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	_, view := fx.approval(fx.parent, owner)
	revision := asString(view["revision"])
	for _, body := range []any{map[string]any{"runtime": "docker", "enabled": true, "revision": revision, "engine": "codex"}, map[string]any{"runtime": "cluster", "enabled": true, "revision": revision}, map[string]any{"runtime": "docker", "revision": revision}, map[string]any{"runtime": "docker", "enabled": true, "revision": revision, "daemon_id": fx.dc.ID}, strings.Repeat("x", 5000)} {
		code, _, _ := fx.do("PUT", "/api/sessions/"+fx.parent+"/child-approval", owner, body, nil)
		if code == 200 {
			t.Fatal("invalid body accepted")
		}
	}
	if n := fx.count(`SELECT count(*) FROM delegation_grants`); n != 0 {
		t.Fatal("read or invalid write mutated grants")
	}
	if n := fx.count(`SELECT count(*) FROM agent_events WHERE kind='delegation_permissions_changed'`); n != 0 {
		t.Fatal("read or invalid write added audit")
	}
}

func TestChildApprovalEndWinsBeforeWaitingParentFence(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	_, view := fx.approval(fx.parent, owner)
	tx, err := fx.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.LockDelegationParent(t.Context(), tx, fx.parent); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var code int
	go func() {
		code, _ = fx.setApproval(fx.parent, owner, "docker", true, asString(view["revision"]))
		close(done)
	}()
	defer func() {
		_ = tx.Rollback(context.Background())
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("waiting permission write did not exit")
		}
	}()
	awaitLifecycle(t, func() bool {
		var n int
		err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FOR UPDATE%'`).Scan(&n)
		return err == nil && n > 0
	})
	if _, err := tx.Exec(t.Context(), `UPDATE sessions SET status='ended',ended_at=now(),delegation_ended_at=now(),end_reason='process_exited' WHERE id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("write blocked after end")
	}
	if code != 409 {
		t.Fatalf("ended parent permission write %d", code)
	}
	if n := fx.count(`SELECT count(*) FROM delegation_grants`); n != 0 {
		t.Fatal("end race minted grants")
	}
}

func TestChildApprovalRestrictedParentNeverUsesPersistentAuthority(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	fx.grant(fx.parent, "codex", "docker", "app", false)
	fx.rule("codex", "docker", false)
	if err := db.SetSessionRestrictTools(t.Context(), fx.pool, fx.parent); err != nil {
		t.Fatal(err)
	}
	request := fx.ask(fx.parent, fx.parentTok, codexBody())
	stored := fx.request(request)
	if len(stored.Offered) != 2 || stored.Offered[0] != "once" || stored.Offered[1] != "deny" {
		t.Fatal("restricted parent offered persistent authority")
	}
	_, view := fx.approval(fx.parent, owner)
	state := approvalRuntime(t, view, "docker")
	if state["can_enable"] != false || len(state["effective_allowed_engines"].([]any)) != 0 {
		t.Fatal("restricted UI disagrees with start policy")
	}
	fx.noSpawn()
}
func TestChildApprovalRestrictionAddedBeforeDecisionRefusesPersistentGrant(t *testing.T) {
	fx := newDelegFx(t)
	request := fx.ask(fx.parent, fx.parentTok, codexBody())
	if err := db.SetSessionRestrictTools(t.Context(), fx.pool, fx.parent); err != nil {
		t.Fatal(err)
	}
	code, _ := fx.decide(request, fx.human("owner-1"), choice("session"))
	if code != 422 {
		t.Fatalf("late restriction accepted persistent choice: %d", code)
	}
	if n := fx.count(`SELECT count(*) FROM delegation_grants`); n != 0 {
		t.Fatal("late restricted request minted a grant")
	}
	fx.noSpawn()
}

func TestChildApprovalFailuresRollBackWholeGrantSetAndAudit(t *testing.T) {
	for _, target := range []string{"grant", "audit"} {
		t.Run(target, func(t *testing.T) {
			fx := newDelegFx(t)
			owner := fx.human("owner-1")
			_, view := fx.approval(fx.parent, owner)
			statement := `CREATE FUNCTION fixture_reject_approval() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.engine='codex' THEN RAISE EXCEPTION 'fixture grant refusal'; END IF; RETURN NEW; END $$; CREATE TRIGGER fixture_reject_approval BEFORE INSERT ON delegation_grants FOR EACH ROW EXECUTE FUNCTION fixture_reject_approval()`
			if target == "audit" {
				statement = `CREATE FUNCTION fixture_reject_approval() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='delegation_permissions_changed' THEN RAISE EXCEPTION 'fixture audit refusal'; END IF; RETURN NEW; END $$; CREATE TRIGGER fixture_reject_approval BEFORE INSERT ON agent_events FOR EACH ROW EXECUTE FUNCTION fixture_reject_approval()`
			}
			if _, err := fx.pool.Exec(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
			code, _ := fx.setApproval(fx.parent, owner, "docker", true, asString(view["revision"]))
			if code != 500 {
				t.Fatalf("injected failure status %d", code)
			}
			if n := fx.count(`SELECT count(*) FROM delegation_grants`); n != 0 {
				t.Fatal("partial grant set survived transaction failure")
			}
			if n := fx.count(`SELECT count(*) FROM agent_events WHERE kind='delegation_permissions_changed'`); n != 0 {
				t.Fatal("partial audit survived transaction failure")
			}
		})
	}
}
