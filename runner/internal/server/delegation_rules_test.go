package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func (fx *delegFx) addRule(tok string, body any) (int, map[string]any) {
	fx.t.Helper()
	code, out, _ := fx.do("POST", "/api/delegation-rules", tok, body, nil)
	return code, out
}

func (fx *delegFx) listRules(tok string) (int, map[string]any) {
	fx.t.Helper()
	code, out, _ := fx.do("GET", "/api/delegation-rules", tok, nil, nil)
	return code, out
}

func rulesOf(out map[string]any) []map[string]any {
	list, _ := out["rules"].([]any)
	rules := make([]map[string]any, 0, len(list))
	for _, r := range list {
		m, _ := r.(map[string]any)
		rules = append(rules, m)
	}
	return rules
}

func TestRulesCRUDOwnerOnly(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	other := fx.human("owner-2")
	noSid := fx.coreToken(identity.Claims{Kind: "human", Sub: "owner-1", Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap}})

	code, out := fx.listRules(owner)
	if code != http.StatusOK || len(rulesOf(out)) != 0 {
		t.Fatalf("empty list: %d %v", code, out)
	}
	if _, ok := out["rules"].([]any); !ok {
		t.Fatalf("rules is not a list: %v", out)
	}
	body := map[string]any{"engine": "codex", "runtime": "docker", "daemon_id": fx.dc.ID}
	if code, out := fx.addRule(noSid, body); code != http.StatusUnauthorized {
		t.Fatalf("add without sid: %d %v", code, out)
	}
	for name, tok := range map[string]string{"agent token": fx.agentToken("owner-1"), "session token": fx.parentTok, "runner key": runnerTestKey} {
		if code, out := fx.addRule(tok, body); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Errorf("add with %s: %d %v", name, code, out)
		}
		if code, _ := fx.listRules(tok); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Errorf("list with %s: %d", name, code)
		}
	}
	if n := fx.count(`SELECT count(*)::int FROM delegation_rules`); n != 0 {
		t.Fatalf("%d rules stored by refused callers", n)
	}
	code, rule := fx.addRule(owner, body)
	if code != http.StatusCreated {
		t.Fatalf("add: %d %v", code, rule)
	}
	hasKeys(t, "rule", rule, "id", "engine", "runtime", "daemon_id", "daemon_name", "local_only", "created_via", "created_at")
	if rule["engine"] != "codex" || rule["runtime"] != "docker" || rule["daemon_id"] != fx.dc.ID || rule["daemon_name"] != "laptop" ||
		rule["local_only"] != false || rule["created_via"] != "settings" {
		t.Fatalf("rule %v", rule)
	}
	id := asString(rule["id"])
	code, out = fx.listRules(owner)
	if rules := rulesOf(out); code != http.StatusOK || len(rules) != 1 || rules[0]["id"] != id || rules[0]["daemon_name"] != "laptop" {
		t.Fatalf("list: %d %v", code, out)
	}
	if code, out := fx.listRules(other); code != http.StatusOK || len(rulesOf(out)) != 0 {
		t.Fatalf("another person sees %d %v", code, out)
	}
	if code, _, _ := fx.do("DELETE", "/api/delegation-rules/"+id, other, nil, nil); code != http.StatusNotFound {
		t.Fatalf("another person removes: %d", code)
	}
	if code, _, _ := fx.do("DELETE", "/api/delegation-rules/"+id, noSid, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("remove without sid: %d", code)
	}
	if code, _, _ := fx.do("DELETE", "/api/delegation-rules/"+id, fx.agentToken("owner-1"), nil, nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("agent removes: %d", code)
	}
	if code, out := fx.listRules(owner); len(rulesOf(out)) != 1 {
		t.Fatalf("a refused remove removed: %d %v", code, out)
	}
	if code, _, _ := fx.do("DELETE", "/api/delegation-rules/"+id, owner, nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove: %d", code)
	}
	if code, out := fx.listRules(owner); len(rulesOf(out)) != 0 {
		t.Fatalf("after remove: %d %v", code, out)
	}
	if code, _, _ := fx.do("DELETE", "/api/delegation-rules/"+id, owner, nil, nil); code != http.StatusNotFound {
		t.Fatalf("second remove: %d", code)
	}
	if code, _, _ := fx.do("DELETE", "/api/delegation-rules/not-a-uuid", owner, nil, nil); code != http.StatusNotFound {
		t.Fatalf("bad id: %d", code)
	}
}

func TestRuleValidation(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	owner := fx.human("owner-1")
	if err := db.UpsertDaemon(ctx, fx.pool, clusterDaemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]any{
		"unknown engine":    map[string]any{"engine": "gpt", "runtime": "docker", "daemon_id": fx.dc.ID},
		"no engine":         map[string]any{"runtime": "docker", "daemon_id": fx.dc.ID},
		"cluster runtime":   map[string]any{"engine": "codex", "runtime": "cluster", "daemon_id": fx.dc.ID},
		"no runtime":        map[string]any{"engine": "codex", "daemon_id": fx.dc.ID},
		"unknown daemon":    map[string]any{"engine": "codex", "runtime": "docker", "daemon_id": newUUID()},
		"bad daemon id":     map[string]any{"engine": "codex", "runtime": "docker", "daemon_id": "laptop"},
		"no daemon":         map[string]any{"engine": "codex", "runtime": "docker"},
		"cluster daemon":    map[string]any{"engine": "codex", "runtime": "docker", "daemon_id": clusterDaemonID},
		"openclaw sandbox":  map[string]any{"engine": "openclaw", "runtime": "docker", "daemon_id": fx.dc.ID},
		"unknown field":     map[string]any{"engine": "codex", "runtime": "docker", "daemon_id": fx.dc.ID, "account_id": "owner-2"},
		"local_only string": map[string]any{"engine": "codex", "runtime": "docker", "daemon_id": fx.dc.ID, "local_only": "yes"},
	} {
		if code, out := fx.addRule(owner, body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if code, out := fx.addRule(owner, "not json"); code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
		t.Errorf("not json: %d %v", code, out)
	}
	if n := fx.count(`SELECT count(*)::int FROM delegation_rules`); n != 0 {
		t.Fatalf("%d invalid rules stored", n)
	}
	for _, c := range []struct {
		body map[string]any
		want bool
	}{
		{map[string]any{"engine": "hermes", "runtime": "docker", "daemon_id": fx.dc.ID}, true},
		{map[string]any{"engine": "openclaw", "runtime": "daemon", "daemon_id": fx.dc.ID}, true},
		{map[string]any{"engine": "hermes", "runtime": "daemon", "daemon_id": fx.dc.ID, "local_only": false}, false},
		{map[string]any{"engine": "claude", "runtime": "daemon", "daemon_id": fx.dc.ID}, false},
		{map[string]any{"engine": "claude", "runtime": "docker", "daemon_id": fx.dc.ID, "local_only": true}, true},
	} {
		code, out := fx.addRule(owner, c.body)
		if code != http.StatusCreated || out["local_only"] != c.want {
			t.Errorf("%v: %d %v, want local_only %v", c.body, code, out, c.want)
		}
	}
	// A daemon that is not connected is still a valid target (the laptop sleeps).
	const desk = "00000000-0000-4000-8000-0000000000d3"
	if err := db.UpsertDaemon(ctx, fx.pool, desk, "desk", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if code, out := fx.addRule(owner, map[string]any{"engine": "codex", "runtime": "docker", "daemon_id": desk}); code != http.StatusCreated || out["daemon_name"] != "desk" {
		t.Errorf("offline daemon: %d %v", code, out)
	}
}

func TestRevokedRuleStopsNewStartsOnly(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	code, rule := fx.addRule(owner, map[string]any{"engine": "claude", "runtime": "docker", "daemon_id": fx.dc.ID})
	if code != http.StatusCreated {
		t.Fatalf("add: %d %v", code, rule)
	}
	code, out, _ := fx.postChild(fx.parent, fx.parentTok, claudeBody("One"), newUUID())
	if code != http.StatusCreated || out["via"] != "rule:"+asString(rule["id"]) {
		t.Fatalf("start under the rule: %d %v", code, out)
	}
	fx.spawn()
	child, _ := out["child"].(map[string]any)
	childID := asString(child["id"])
	if code, _, _ := fx.do("DELETE", "/api/delegation-rules/"+asString(rule["id"]), owner, nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove: %d", code)
	}
	if code, out, _ := fx.postChild(fx.parent, fx.parentTok, claudeBody("Two"), newUUID()); code != http.StatusAccepted {
		t.Fatalf("start after revoke: %d %v", code, out)
	}
	fx.noSpawn()
	row := fx.row(childID)
	if row.EndedAt != nil || terminalSessionStatus(row.Status) {
		t.Fatalf("revoking the rule ended the child: %+v", row)
	}
}

func TestRevokeGrant(t *testing.T) {
	fx := newDelegFx(t)
	owner := fx.human("owner-1")
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	if code, out := fx.decide(id, owner, choice("session")); code != http.StatusOK {
		t.Fatalf("decide: %d %v", code, out)
	}
	fx.spawn()
	grants, err := db.ListLiveDelegationGrants(context.Background(), fx.pool, fx.parent)
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants %v %v", grants, err)
	}
	path := "/api/delegation-grants/" + grants[0].ID + "/revoke"
	noSid := fx.coreToken(identity.Claims{Kind: "human", Sub: "owner-1", Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap}})
	if code, _, _ := fx.do("POST", path, fx.human("owner-2"), nil, nil); code != http.StatusNotFound {
		t.Fatalf("another person: %d", code)
	}
	if code, _, _ := fx.do("POST", path, noSid, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("without sid: %d", code)
	}
	if code, _, _ := fx.do("POST", path, fx.parentTok, nil, nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("the parent's token: %d", code)
	}
	if code, out, _ := fx.postChild(fx.parent, fx.parentTok, codexBody(), newUUID()); code != http.StatusCreated {
		t.Fatalf("grant still live: %d %v", code, out)
	}
	fx.spawn()
	if code, _, _ := fx.do("POST", path, owner, nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	if code, out, _ := fx.postChild(fx.parent, fx.parentTok, codexBody(), newUUID()); code != http.StatusAccepted {
		t.Fatalf("after revoke: %d %v", code, out)
	}
	fx.noSpawn()
	if code, _, _ := fx.do("POST", path, owner, nil, nil); code != http.StatusNotFound {
		t.Fatalf("second revoke: %d", code)
	}
	if code, _, _ := fx.do("POST", "/api/delegation-grants/nope/revoke", owner, nil, nil); code != http.StatusNotFound {
		t.Fatalf("bad id: %d", code)
	}
}

func TestRulesListShowsLimits(t *testing.T) {
	fx := newDelegFx(t)
	code, out := fx.listRules(fx.human("owner-1"))
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, out)
	}
	limits, _ := out["limits"].(map[string]any)
	want := map[string]float64{"per_parent_running": 3, "per_parent_starts": 20, "per_account_running": 6, "per_account_starts_per_hour": 60}
	if len(limits) != len(want) {
		t.Fatalf("limits %v", limits)
	}
	for k, v := range want {
		if limits[k] != v {
			t.Errorf("limits[%s] = %v, want %v", k, limits[k], v)
		}
	}
}
