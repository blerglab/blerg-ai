package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// postChild sends POST /api/sessions/{parent}/children with the key (none when key is "").
func (fx *delegFx) postChild(parent, tok string, body any, key string) (int, map[string]any, http.Header) {
	fx.t.Helper()
	hdr := map[string]string{}
	if key != "" {
		hdr["Idempotency-Key"] = key
	}
	return fx.do("POST", "/api/sessions/"+parent+"/children", tok, body, hdr)
}

// rule stores a standing rule of owner-1 on fx.dc and returns its id.
func (fx *delegFx) rule(engine, runtime string, localOnly bool) string {
	fx.t.Helper()
	r, err := db.InsertDelegationRule(context.Background(), fx.pool, db.DelegationRule{AccountID: "owner-1", Engine: engine,
		Runtime: runtime, DaemonID: fx.dc.ID, LocalOnly: localOnly, CreatedVia: "settings"})
	if err != nil {
		fx.t.Fatal(err)
	}
	return r.ID
}

// grant stores a session grant of parent (owner-1, fx.dc) and returns its id.
func (fx *delegFx) grant(parent, engine, runtime, repo string, noRepo bool) string {
	fx.t.Helper()
	ctx := context.Background()
	tx, err := fx.pool.Begin(ctx)
	if err != nil {
		fx.t.Fatal(err)
	}
	g, err := db.InsertDelegationGrantTx(ctx, tx, db.DelegationGrant{ParentSessionID: parent, AccountID: "owner-1",
		Engine: engine, Runtime: runtime, DaemonID: fx.dc.ID, Repo: repo, NoRepo: noRepo})
	if err != nil {
		_ = tx.Rollback(ctx)
		fx.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		fx.t.Fatal(err)
	}
	return g.ID
}

func (fx *delegFx) count(sql string, args ...any) int {
	fx.t.Helper()
	var n int
	if err := fx.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		fx.t.Fatal(err)
	}
	return n
}

// noSpawn fails when a spawn_session frame is waiting for the daemon.
func (fx *delegFx) noSpawn() {
	fx.t.Helper()
	for {
		select {
		case raw := <-fx.dc.send:
			var msg protocol.SpawnSession
			if json.Unmarshal(raw, &msg) == nil && msg.Type == "spawn_session" {
				fx.t.Fatalf("a spawn reached the daemon: %s", raw)
			}
		case <-time.After(50 * time.Millisecond):
			return
		}
	}
}

// storedNothing asserts the parent has no request and no child.
func (fx *delegFx) storedNothing(parent string) {
	fx.t.Helper()
	if n := fx.count(`SELECT count(*)::int FROM delegation_requests WHERE parent_session_id = $1::uuid`, parent); n != 0 {
		fx.t.Fatalf("%d requests stored", n)
	}
	if n := fx.count(`SELECT count(*)::int FROM sessions WHERE parent_session_id = $1::uuid`, parent); n != 0 {
		fx.t.Fatalf("%d children stored", n)
	}
	fx.noSpawn()
}

// pendingRequest stores a pending request of parent with a unique body hash.
func (fx *delegFx) pendingRequest(parent string) db.DelegationRequest {
	fx.t.Helper()
	r, _, err := db.InsertDelegationRequest(context.Background(), fx.pool, db.DelegationRequest{
		ParentSessionID: parent, AccountID: "owner-1", DaemonID: fx.dc.ID, IdempotencyKey: newUUID(),
		RequestHash: newUUID(), Request: json.RawMessage(`{}`), Offered: []string{"once", "deny"},
		ExpiresAt: time.Now().Add(delegationRequestTTL),
	})
	if err != nil {
		fx.t.Fatal(err)
	}
	return r
}

func codexBody() map[string]any {
	return map[string]any{"engine": "codex", "repo": "app", "prompt": "review the diff"}
}

func claudeBody(title string) map[string]any {
	return map[string]any{"engine": "claude", "repo": "app", "prompt": "do the thing", "title": title}
}

func TestChildrenCredentialTable(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	_, otherTok := fx.session(sessOpts{owner: "owner-1"})
	child, childTok := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	ownerless, ownerlessTok := fx.session(sessOpts{owner: ""})
	revoked, revokedTok := fx.session(sessOpts{owner: "owner-1"})
	if err := db.RevokeBoardTokensForSession(ctx, fx.pool, revoked); err != nil {
		t.Fatal(err)
	}
	ended, endedTok := fx.session(sessOpts{owner: "owner-1", status: "stopped"})
	cluster, clusterTok := fx.session(sessOpts{owner: "owner-1", runtime: runnerRuntimeCluster})
	const offline = "00000000-0000-4000-8000-0000000000d2"
	if err := db.UpsertDaemon(ctx, fx.pool, offline, "desk", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	gone, goneTok := fx.session(sessOpts{owner: "owner-1", daemonID: offline})
	for name, c := range map[string]struct {
		sid, tok string
		want     int
		msg      string
	}{
		"own session token":       {fx.parent, fx.parentTok, 202, ""},
		"no credential":           {fx.parent, "", 401, ""},
		"another session's token": {fx.parent, otherTok, 403, "only for its own session"},
		"daemon master token":     {fx.parent, delegDaemonToken, 403, "use the session's own token"},
		"assist board token":      {fx.parent, fx.assistToken(fx.parent), 403, "a board token cannot start sessions"},
		"agent token":             {fx.parent, fx.agentToken("owner-1"), 401, ""},
		"browser login":           {fx.parent, fx.human("owner-1"), 401, ""},
		"runner key":              {fx.parent, runnerTestKey, 401, ""},
		"a child's own token":     {child, childTok, 403, "a started session cannot start sessions"},
		"owner-less parent":       {ownerless, ownerlessTok, 403, "delegation needs a session owned by a person"},
		"revoked session token":   {revoked, revokedTok, 401, "revoked or expired"},
		"ended parent":            {ended, endedTok, 409, "the session has ended"},
		"cluster parent":          {cluster, clusterTok, 422, "children run on the parent's desktop daemon"},
		"daemon disconnected":     {gone, goneTok, 409, "the session's daemon is not connected"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, _ := fx.postChild(c.sid, c.tok, codexBody(), "k-"+name)
			if code != c.want {
				t.Fatalf("status = %d (%v), want %d", code, out, c.want)
			}
			if c.msg != "" && !strings.Contains(asString(out["error"]), c.msg) {
				t.Fatalf("error = %q, want it to say %q", out["error"], c.msg)
			}
			if c.want != 202 && c.sid != fx.parent {
				fx.storedNothing(c.sid)
			}
		})
	}
	// Only the own-token case stored anything.
	if n := fx.count(`SELECT count(*)::int FROM delegation_requests WHERE parent_session_id = $1::uuid`, fx.parent); n != 1 {
		t.Fatalf("requests for the parent = %d, want 1", n)
	}
}

func TestChildrenBodyAllowlist(t *testing.T) {
	fx := newDelegFx(t)
	fx.rule("codex", runnerRuntimeDocker, false) // would start at once if the field slipped through
	for _, field := range []string{"env", "callback_url", "callback_secret", "git_url", "provider",
		"auto_stop", "daemon_id", "interaction", "grant", "kind", "dangerously_skip_permissions",
		"cron_id", "target_daemon_id", "parent_session_id", "delegation_via", "idem_scope"} {
		t.Run(field, func(t *testing.T) {
			body := codexBody()
			body[field] = "y"
			code, out, _ := fx.postChild(fx.parent, fx.parentTok, body, field)
			if code != http.StatusUnprocessableEntity || asString(out["error"]) != fmt.Sprintf("field %q is not accepted here", field) {
				t.Fatalf("%s: %d %v", field, code, out)
			}
			fx.storedNothing(fx.parent)
		})
	}
	// The positive control: the same body without the field starts under the rule.
	if code, out, _ := fx.postChild(fx.parent, fx.parentTok, codexBody(), "ok"); code != http.StatusCreated {
		t.Fatalf("control: %d %v", code, out)
	}
	fx.spawn()
}

func TestChildrenBodyValidation(t *testing.T) {
	fx := newDelegFx(t)
	fx.rule("claude", runnerRuntimeDocker, false)
	fx.rule("openclaw", runnerRuntimeDocker, false)
	long := strings.Repeat("é", maxDelegationTitleRunes+1)
	for name, c := range map[string]struct {
		body any
		key  string
		want int
		msg  string
	}{
		"missing prompt":      {map[string]any{"repo": "app"}, "k", 422, "prompt is required"},
		"blank prompt":        {map[string]any{"repo": "app", "prompt": "  \n "}, "k", 422, "prompt is required"},
		"huge prompt":         {map[string]any{"repo": "app", "prompt": strings.Repeat("x", maxDelegationPromptBytes+1)}, "k", 413, "too large"},
		"huge body":           {map[string]any{"repo": "app", "prompt": strings.Repeat("x", maxDelegationPromptBytes+9<<10)}, "k", 413, "too large"},
		"repo and no_repo":    {map[string]any{"repo": "app", "no_repo": true, "prompt": "x"}, "k", 422, "no_repo cannot be combined"},
		"no repo at all":      {map[string]any{"prompt": "x"}, "k", 422, "repo is required"},
		"bad repo name":       {map[string]any{"repo": "../etc", "prompt": "x"}, "k", 422, "repo must be a folder name"},
		"repo not on daemon":  {map[string]any{"repo": "other", "prompt": "x"}, "k", 422, "the parent's daemon has no repo other"},
		"runtime cluster":     {map[string]any{"repo": "app", "prompt": "x", "runtime": "cluster"}, "k", 422, `runtime must be "docker" or "daemon"`},
		"title too long":      {map[string]any{"repo": "app", "prompt": "x", "title": long}, "k", 422, "title is longer"},
		"engine cannot run":   {map[string]any{"repo": "app", "prompt": "x", "engine": "openclaw", "runtime": "docker"}, "k", 422, "the parent's daemon cannot run OpenClaw in docker"},
		"unknown engine":      {map[string]any{"repo": "app", "prompt": "x", "engine": "nope"}, "k", 422, "cannot run"},
		"bad effort":          {map[string]any{"repo": "app", "prompt": "x", "effort": "bogus"}, "k", 422, "effort must be"},
		"wrong type":          {map[string]any{"repo": "app", "prompt": 3}, "k", 422, "wrong type"},
		"not an object":       {`["x"]`, "k", 400, "JSON object"},
		"missing key":         {map[string]any{"repo": "app", "prompt": "x"}, "", 400, "Idempotency-Key"},
		"key longer than 128": {map[string]any{"repo": "app", "prompt": "x"}, strings.Repeat("k", 129), 400, "Idempotency-Key"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, _ := fx.postChild(fx.parent, fx.parentTok, c.body, c.key)
			if code != c.want || !strings.Contains(asString(out["error"]), c.msg) {
				t.Fatalf("%d %v, want %d %q", code, out, c.want, c.msg)
			}
			if strings.Contains(asString(out["error"]), "cluster") {
				t.Fatalf("the refusal mentions the cluster: %q", out["error"])
			}
			fx.storedNothing(fx.parent)
		})
	}
}

func TestStartWithoutMatchCreatesRequestAndEvent(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	prompt := "Review the diff\n" + strings.Repeat("ü", 700)
	code, out, _ := fx.postChild(fx.parent, fx.parentTok, map[string]any{"engine": "codex", "repo": "app", "prompt": prompt}, "k1")
	if code != http.StatusAccepted {
		t.Fatalf("status %d %v", code, out)
	}
	for _, k := range []string{"request_id", "status", "expires_at", "offered", "budget"} {
		if _, ok := out[k]; !ok {
			t.Fatalf("reply lacks %q: %v", k, out)
		}
	}
	if out["status"] != "pending" || fmt.Sprint(out["offered"]) != "[once session always deny]" {
		t.Fatalf("reply = %v", out)
	}
	if b, _ := out["budget"].(map[string]any); b["engine"] != "codex" {
		t.Fatalf("budget = %v", out["budget"])
	}
	id := asString(out["request_id"])
	req, err := db.GetDelegationRequest(ctx, fx.pool, id)
	if err != nil || req == nil {
		t.Fatalf("request %s: %v", id, err)
	}
	var stored map[string]any
	if err := json.Unmarshal(req.Request, &stored); err != nil {
		t.Fatal(err)
	}
	if req.Status != "pending" || stored["prompt"] != prompt || stored["runtime"] != "docker" || stored["engine"] != "codex" ||
		stored["title"] != "Review the diff" || req.AccountID != "owner-1" || req.DaemonID != fx.dc.ID {
		t.Fatalf("stored = %+v %v", req, stored)
	}
	if b, _ := stored["budget"].(map[string]any); b["engine"] != "codex" {
		t.Fatalf("stored budget = %v", stored["budget"])
	}
	if d := req.ExpiresAt.Sub(req.CreatedAt); d < delegationRequestTTL-time.Minute || d > delegationRequestTTL+time.Minute {
		t.Fatalf("expires after %v", d)
	}

	var kind, raw string
	if err := fx.pool.QueryRow(ctx, `SELECT kind, payload::text FROM agent_events WHERE session_id = $1::uuid AND client_event_id = $2`,
		fx.parent, delegationEventID(id, "delegation_request")).Scan(&kind, &raw); err != nil {
		t.Fatalf("no delegation_request event: %v", err)
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	excerpt := asString(ev["prompt"])
	if kind != "delegation_request" || utf8.RuneCountInString(excerpt) != delegationPromptExcerpt || !strings.HasPrefix(prompt, excerpt) ||
		ev["prompt_truncated"] != true || ev["runtime"] != "docker" || ev["daemon_name"] != "laptop" || ev["request_id"] != id ||
		ev["engine"] != "codex" || ev["repo"] != "app" || ev["title"] != "Review the diff" ||
		fmt.Sprint(ev["offered"]) != "[once session always deny]" || ev["expires_at"] == nil {
		t.Fatalf("event %s = %v", kind, ev)
	}
	if b, _ := ev["budget"].(map[string]any); b["engine"] != "codex" {
		t.Fatalf("event budget = %v", ev["budget"])
	}
	fx.noSpawn()
}

func TestRepeatPostReturnsSameRequestOrChild(t *testing.T) {
	fx := newDelegFx(t)
	code, first, _ := fx.postChild(fx.parent, fx.parentTok, codexBody(), "k1")
	if code != http.StatusAccepted {
		t.Fatalf("first: %d %v", code, first)
	}
	code, again, hdr := fx.postChild(fx.parent, fx.parentTok, codexBody(), "k1")
	if code != http.StatusAccepted || again["request_id"] != first["request_id"] || hdr.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("same key: %d %v %v", code, again, hdr)
	}
	for _, k := range []string{"request_id", "status", "choice", "child_session_id", "start_error", "expires_at"} {
		if _, ok := again[k]; !ok {
			t.Fatalf("replay lacks %q: %v", k, again)
		}
	}
	other := codexBody()
	other["prompt"] = "something else"
	if code, out, _ := fx.postChild(fx.parent, fx.parentTok, other, "k1"); code != http.StatusConflict ||
		asString(out["error"]) != idempotencyConflictMessage {
		t.Fatalf("same key, other body: %d %v", code, out)
	}
	// Ruling 3: the same body under a new key answers the pending request (200, same id).
	if code, out, _ := fx.postChild(fx.parent, fx.parentTok, codexBody(), "k2"); code != http.StatusOK || out["request_id"] != first["request_id"] {
		t.Fatalf("same body, new key: %d %v", code, out)
	}
	if n := fx.count(`SELECT count(*)::int FROM delegation_requests WHERE parent_session_id = $1::uuid`, fx.parent); n != 1 {
		t.Fatalf("%d requests stored, want 1", n)
	}
	// A request decided and started answers with its child (Task 5 decides; simulated here).
	if _, err := fx.pool.Exec(context.Background(), `UPDATE delegation_requests SET status = 'allowed', choice = 'once' WHERE id = $1::uuid`,
		first["request_id"]); err != nil {
		t.Fatal(err)
	}
	startedChild := fx.childOf(fx.parent)
	if err := db.SetDelegationRequestStart(context.Background(), fx.pool, asString(first["request_id"]), startedChild, ""); err != nil {
		t.Fatal(err)
	}
	code, out, _ := fx.postChild(fx.parent, fx.parentTok, codexBody(), "k1")
	if child, _ := out["child"].(map[string]any); code != http.StatusCreated || child["id"] != startedChild ||
		out["via"] != "request:"+asString(first["request_id"]) {
		t.Fatalf("allowed replay: %d %v", code, out)
	}
	fx.noSpawn()

	// A rule start, then the same key again: the same child, no second spawn.
	fx.rule("claude", runnerRuntimeDocker, false)
	code, started, hdr := fx.postChild(fx.parent, fx.parentTok, claudeBody("One"), "k3")
	if code != http.StatusCreated || hdr.Get("Idempotent-Replayed") != "" {
		t.Fatalf("rule start: %d %v", code, started)
	}
	fx.spawn()
	childID := started["child"].(map[string]any)["id"]
	code, replay, hdr := fx.postChild(fx.parent, fx.parentTok, claudeBody("One"), "k3")
	if code != http.StatusCreated || replay["child"].(map[string]any)["id"] != childID || hdr.Get("Idempotent-Replayed") != "true" ||
		replay["via"] != started["via"] {
		t.Fatalf("rule replay: %d %v %v", code, replay, hdr)
	}
	// Plan-review 9: the same key with another body is a conflict, not the old child.
	if code, out, _ := fx.postChild(fx.parent, fx.parentTok, claudeBody("Two"), "k3"); code != http.StatusConflict ||
		asString(out["error"]) != idempotencyConflictMessage {
		t.Fatalf("rule key, other body: %d %v", code, out)
	}
	fx.noSpawn()
	// Review m1: the child started under the key is gone → 410, never "started" with a dead id.
	if _, err := fx.pool.Exec(context.Background(), `DELETE FROM agent_events WHERE session_id = $1::uuid`, childID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSession(context.Background(), fx.pool, asString(childID)); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := fx.postChild(fx.parent, fx.parentTok, claudeBody("One"), "k3"); code != http.StatusGone ||
		asString(out["error"]) != msgChildKeyDeleted {
		t.Fatalf("deleted child replay: %d %v", code, out)
	}
	fx.noSpawn()
}

func TestDelegationMatchMatrix(t *testing.T) {
	full := "[once session always deny]"
	short := "[once deny]"
	type setup func(fx *delegFx, parent string) (wantID string)
	ruleOf := func(engine, runtime string, localOnly bool) setup {
		return func(fx *delegFx, _ string) string { return fx.rule(engine, runtime, localOnly) }
	}
	grantOf := func(engine, runtime, repo string, noRepo bool) setup {
		return func(fx *delegFx, parent string) string { return fx.grant(parent, engine, runtime, repo, noRepo) }
	}
	none := func(*delegFx, string) string { return "" }
	rows := []struct {
		name    string
		parent  sessOpts
		body    map[string]any
		setup   setup
		local   []string
		wantVia string // "rule" | "grant" | "request"
		offered string
	}{
		{"direct docker parent, docker child, docker rule", sessOpts{}, nil, ruleOf("claude", "docker", false), nil, "rule", ""},
		{"direct docker parent, daemon child, daemon rule", sessOpts{}, map[string]any{"runtime": "daemon"}, ruleOf("claude", "daemon", false), nil, "request", short},
		{"direct daemon parent, daemon child, daemon rule", sessOpts{runtime: "daemon"}, map[string]any{"runtime": "daemon"}, ruleOf("claude", "daemon", false), nil, "rule", ""},
		{"direct daemon parent, docker child, docker rule", sessOpts{runtime: "daemon"}, nil, ruleOf("claude", "docker", false), nil, "rule", ""},
		{"direct daemon parent, daemon child, no rule", sessOpts{runtime: "daemon"}, map[string]any{"runtime": "daemon"}, none, nil, "request", full},
		{"board-started parent", sessOpts{boardID: "new"}, nil, ruleOf("claude", "docker", false), nil, "request", short},
		{"agent-token parent", sessOpts{startedByKind: "agent"}, nil, ruleOf("claude", "docker", false), nil, "request", short},
		{"cron-started parent", sessOpts{startedByKind: "cron"}, nil, ruleOf("claude", "docker", false), nil, "request", short},
		{"private parent", sessOpts{private: true}, nil, ruleOf("claude", "docker", false), nil, "request", short},
		{"board grant parent", sessOpts{boardID: "new"}, nil, grantOf("claude", "docker", "app", false), nil, "request", short},
		{"hermes local_only rule, reported local", sessOpts{}, map[string]any{"engine": "hermes"}, ruleOf("hermes", "docker", true), []string{"hermes"}, "rule", ""},
		{"hermes local_only rule, not reported", sessOpts{}, map[string]any{"engine": "hermes"}, ruleOf("hermes", "docker", true), nil, "request", full},
		{"hermes rule without local_only", sessOpts{}, map[string]any{"engine": "hermes"}, ruleOf("hermes", "docker", false), nil, "rule", ""},
		{"rule and grant both match", sessOpts{}, nil, func(fx *delegFx, p string) string {
			fx.grant(p, "claude", "docker", "app", false)
			return fx.rule("claude", "docker", false)
		}, nil, "rule", ""},
		{"grant matches", sessOpts{}, nil, grantOf("claude", "docker", "app", false), nil, "grant", ""},
		{"grant no_repo matches no_repo", sessOpts{}, map[string]any{"repo": nil, "no_repo": true}, grantOf("claude", "docker", "", true), nil, "grant", ""},
		{"grant app, request other repo", sessOpts{}, map[string]any{"repo": "other"}, grantOf("claude", "docker", "app", false), nil, "request", full},
		{"grant app, request no_repo", sessOpts{}, map[string]any{"repo": nil, "no_repo": true}, grantOf("claude", "docker", "app", false), nil, "request", full},
		{"grant claude, request codex", sessOpts{}, map[string]any{"engine": "codex"}, grantOf("claude", "docker", "app", false), nil, "request", full},
		{"grant docker, request daemon", sessOpts{}, map[string]any{"runtime": "daemon"}, grantOf("claude", "docker", "app", false), nil, "request", short},
		{"host grant never widens a docker parent", sessOpts{}, map[string]any{"runtime": "daemon"}, grantOf("claude", "daemon", "app", false), nil, "request", short},
		{"rule for another engine", sessOpts{}, nil, ruleOf("codex", "docker", false), nil, "request", full},
		{"revoked grant", sessOpts{}, nil, func(fx *delegFx, p string) string {
			id := fx.grant(p, "claude", "docker", "app", false)
			if ok, err := db.RevokeDelegationGrant(context.Background(), fx.pool, id, "owner-1"); !ok || err != nil {
				fx.t.Fatal(err)
			}
			return ""
		}, nil, "request", full},
		{"revoked rule", sessOpts{}, nil, func(fx *delegFx, _ string) string {
			id := fx.rule("claude", "docker", false)
			if ok, err := db.RevokeDelegationRule(context.Background(), fx.pool, id, "owner-1"); !ok || err != nil {
				fx.t.Fatal(err)
			}
			return ""
		}, nil, "request", full},
		{"rule pinned to another daemon", sessOpts{}, nil, func(fx *delegFx, _ string) string {
			const other = "00000000-0000-4000-8000-0000000000d3"
			if err := db.UpsertDaemon(context.Background(), fx.pool, other, "desk", "local", "/repos"); err != nil {
				fx.t.Fatal(err)
			}
			if _, err := db.InsertDelegationRule(context.Background(), fx.pool, db.DelegationRule{AccountID: "owner-1", Engine: "claude",
				Runtime: "docker", DaemonID: other, CreatedVia: "settings"}); err != nil {
				fx.t.Fatal(err)
			}
			return ""
		}, nil, "request", full},
		{"assist parent", sessOpts{boardID: "new"}, nil, func(fx *delegFx, p string) string {
			if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET assist = true WHERE id = $1::uuid`, p); err != nil {
				fx.t.Fatal(err)
			}
			return fx.rule("claude", "docker", false)
		}, nil, "request", short},
		{"docker parent, runtime omitted, daemon without sandbox", sessOpts{}, nil, func(fx *delegFx, _ string) string {
			fx.dc.SetSandboxAvailable(false) // the default runtime resolves to the host
			fx.rule("claude", "daemon", false)
			return fx.rule("claude", "docker", false)
		}, nil, "request", short},
		{"another account's rule", sessOpts{}, nil, func(fx *delegFx, _ string) string {
			if _, err := db.InsertDelegationRule(context.Background(), fx.pool, db.DelegationRule{AccountID: "owner-2", Engine: "claude",
				Runtime: "docker", DaemonID: fx.dc.ID, CreatedVia: "settings"}); err != nil {
				fx.t.Fatal(err)
			}
			return ""
		}, nil, "request", full},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			fx := newDelegFx(t)
			fx.dc.SetCheckedOutRepos([]string{"app", "other"})
			fx.dc.SetLocalEngines(row.local)
			o := row.parent
			o.owner, o.title = "owner-1", "Matrix"
			parent, tok := fx.session(o)
			wantID := row.setup(fx, parent)
			body := map[string]any{"engine": "claude", "repo": "app", "prompt": "x"}
			for k, v := range row.body {
				if v == nil {
					delete(body, k)
				} else {
					body[k] = v
				}
			}
			code, out, _ := fx.postChild(parent, tok, body, "m")
			switch row.wantVia {
			case "request":
				if code != http.StatusAccepted || fmt.Sprint(out["offered"]) != row.offered {
					t.Fatalf("want a request offering %s: %d %v", row.offered, code, out)
				}
				fx.noSpawn()
			default:
				if code != http.StatusCreated || out["via"] != row.wantVia+":"+wantID {
					t.Fatalf("want %s:%s: %d %v", row.wantVia, wantID, code, out)
				}
				fx.spawn()
			}
		})
	}
}

func TestTrustDirectAndOffered(t *testing.T) {
	s := func(v string) *string { return &v }
	docker, daemon := s("docker"), s("daemon")
	for name, c := range map[string]struct {
		row  db.SessionRow
		want bool
	}{
		"direct":             {db.SessionRow{Runtime: docker}, true},
		"started by a human": {db.SessionRow{Runtime: docker, StartedByKind: "human"}, true},
		"board":              {db.SessionRow{Runtime: docker, BoardID: s("b")}, false},
		"cron":               {db.SessionRow{Runtime: docker, CronID: s("c")}, false},
		"private":            {db.SessionRow{Runtime: docker, Private: true}, false},
		"agent token":        {db.SessionRow{Runtime: docker, StartedByKind: "agent"}, false},
		"runner key":         {db.SessionRow{Runtime: docker, StartedByKind: "runner_key"}, false},
		"child":              {db.SessionRow{Runtime: docker, ParentSessionID: s("p")}, false},
		"child, parent gone": {db.SessionRow{Runtime: docker, DelegationVia: "rule:x"}, false},
		"started by session": {db.SessionRow{Runtime: docker, StartedByKind: "session"}, false},
		"service principal":  {db.SessionRow{Runtime: docker, StartedByKind: "service"}, false},
	} {
		if got := trustDirect(&c.row); got != c.want {
			t.Errorf("%s: trustDirect = %v", name, got)
		}
		wantDocker := []string{"once", "deny"}
		if c.want {
			wantDocker = []string{"once", "session", "always", "deny"}
		}
		if got := offeredChoices(&c.row, childStart{Runtime: "docker"}); !slices.Equal(got, wantDocker) {
			t.Errorf("%s: offered = %v", name, got)
		}
		if got := offeredChoices(&c.row, childStart{Runtime: "daemon"}); !slices.Equal(got, []string{"once", "deny"}) {
			t.Errorf("%s: a docker parent's host child offered %v", name, got)
		}
	}
	if got := offeredChoices(&db.SessionRow{Runtime: daemon}, childStart{Runtime: "daemon"}); len(got) != 4 {
		t.Errorf("a host parent's host child offered %v", got)
	}
}

func TestDelegationViaRecorded(t *testing.T) {
	fx := newDelegFx(t)
	ruleID := fx.rule("claude", runnerRuntimeDocker, false)
	body := claudeBody("Ruled")
	body["keep_running"] = true
	code, out, _ := fx.postChild(fx.parent, fx.parentTok, body, "r")
	if code != http.StatusCreated || out["via"] != "rule:"+ruleID || out["plugins"] != "skipped" {
		t.Fatalf("rule start: %d %v", code, out)
	}
	fx.spawn()
	child, _ := out["child"].(map[string]any)
	for _, k := range []string{"id", "engine", "model", "effort", "runtime", "repo", "no_repo", "title", "keep_running"} {
		if _, ok := child[k]; !ok {
			t.Fatalf("child lacks %q: %v", k, child)
		}
	}
	if b, _ := out["budget"].(map[string]any); b["engine"] != "claude" {
		t.Fatalf("budget = %v", out["budget"])
	}
	row := fx.row(asString(child["id"]))
	if row.DelegationVia != "rule:"+ruleID || derefOrEmpty(row.ParentSessionID) != fx.parent || !row.KeepRunning ||
		row.StartedByKind != "session" || child["runtime"] != "docker" || child["keep_running"] != true || child["title"] != "Ruled" {
		t.Fatalf("child row = %+v, reply %v", row, child)
	}

	parent2, tok2 := fx.session(sessOpts{owner: "owner-1", title: "Second"})
	grantID := fx.grant(parent2, "codex", runnerRuntimeDocker, "app", false)
	code, out, _ = fx.postChild(parent2, tok2, codexBody(), "g")
	if code != http.StatusCreated || out["via"] != "grant:"+grantID {
		t.Fatalf("grant start: %d %v", code, out)
	}
	fx.spawn()
	if row := fx.row(asString(out["child"].(map[string]any)["id"])); row.DelegationVia != "grant:"+grantID || row.KeepRunning {
		t.Fatalf("grant child row = %+v", row)
	}
}

func TestDelegationLimits(t *testing.T) {
	ctx := context.Background()
	t.Run("3 running children", func(t *testing.T) {
		fx := newDelegFx(t)
		fx.rule("claude", runnerRuntimeDocker, false)
		for range maxRunningChildrenPerParent {
			fx.childOf(fx.parent)
		}
		code, out, hdr := fx.postChild(fx.parent, fx.parentTok, claudeBody("x"), "a")
		if code != http.StatusTooManyRequests || hdr.Get("Retry-After") != "60" {
			t.Fatalf("%d %v %v", code, out, hdr)
		}
		// The request path is limited too.
		code, _, _ = fx.postChild(fx.parent, fx.parentTok, codexBody(), "b")
		if code != http.StatusTooManyRequests {
			t.Fatalf("request path: %d", code)
		}
		fx.noSpawn()
	})
	t.Run("20 starts are final", func(t *testing.T) {
		fx := newDelegFx(t)
		fx.rule("claude", runnerRuntimeDocker, false)
		for range maxStartsPerParent - 3 {
			fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "stopped"})
		}
		for range 3 {
			fx.pendingRequest(fx.parent) // pending requests count as starts
		}
		code, out, hdr := fx.postChild(fx.parent, fx.parentTok, claudeBody("x"), "a")
		if code != http.StatusTooManyRequests || hdr.Get("Retry-After") != "" || !strings.Contains(asString(out["error"]), "does not reset") {
			t.Fatalf("%d %v %v", code, out, hdr)
		}
		fx.noSpawn()
	})
	t.Run("6 running across the account", func(t *testing.T) {
		fx := newDelegFx(t)
		fx.rule("claude", runnerRuntimeDocker, false)
		p2, _ := fx.session(sessOpts{owner: "owner-1"})
		p3, _ := fx.session(sessOpts{owner: "owner-1"})
		for _, p := range []string{fx.parent, p2, p3} {
			fx.childOf(p)
			fx.childOf(p)
		}
		code, out, hdr := fx.postChild(fx.parent, fx.parentTok, claudeBody("x"), "a")
		if code != http.StatusTooManyRequests || hdr.Get("Retry-After") != "60" || !strings.Contains(asString(out["error"]), "your sessions") {
			t.Fatalf("%d %v %v", code, out, hdr)
		}
		fx.noSpawn()
	})
	t.Run("60 starts an hour", func(t *testing.T) {
		fx := newDelegFx(t)
		fx.rule("claude", runnerRuntimeDocker, false)
		others := make([]string, 4)
		for i := range others {
			others[i], _ = fx.session(sessOpts{owner: "owner-1"})
		}
		for i := range maxStartsPerAccountPerHour {
			r := fx.pendingRequest(others[i%len(others)])
			if _, err := fx.pool.Exec(ctx, `UPDATE delegation_requests SET status = 'denied', created_at = now() - interval '30 minutes' WHERE id = $1::uuid`, r.ID); err != nil {
				t.Fatal(err)
			}
		}
		code, out, hdr := fx.postChild(fx.parent, fx.parentTok, claudeBody("x"), "a")
		retry, _ := strconv.Atoi(hdr.Get("Retry-After"))
		if code != http.StatusTooManyRequests || retry < 1700 || retry > 1800 {
			t.Fatalf("%d %v Retry-After %q", code, out, hdr.Get("Retry-After"))
		}
		fx.noSpawn()
	})
	t.Run("concurrent starts do not pass the limit together", func(t *testing.T) {
		fx := newDelegFx(t)
		fx.rule("claude", runnerRuntimeDocker, false)
		var wg sync.WaitGroup
		codes := make([]int, maxRunningChildrenPerParent+1)
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i], _, _ = fx.postChild(fx.parent, fx.parentTok, claudeBody("c"+strconv.Itoa(i)), "c"+strconv.Itoa(i))
			}()
		}
		wg.Wait()
		created, limited := 0, 0
		for _, c := range codes {
			switch c {
			case http.StatusCreated:
				created++
			case http.StatusTooManyRequests:
				limited++
			}
		}
		if created != maxRunningChildrenPerParent || limited != 1 {
			t.Fatalf("codes = %v", codes)
		}
		if n := fx.count(`SELECT count(*)::int FROM sessions WHERE parent_session_id = $1::uuid`, fx.parent); n != maxRunningChildrenPerParent {
			t.Fatalf("%d children", n)
		}
	})
}

// Review B1: more concurrent posts than the pool has connections finish quickly and still keep
// the limit; posts of many accounts at once all proceed.
func TestDelegationLockNeverExhaustsThePool(t *testing.T) {
	fx := newDelegFx(t)
	fx.rule("claude", runnerRuntimeDocker, false)
	n := 2 * int(fx.pool.Config().MaxConns)
	fx.dc.send = make(chan []byte, 4*n)
	posts := func(parents, toks []string) []int {
		codes := make([]int, len(parents))
		var wg sync.WaitGroup
		for i := range parents {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i], _, _ = fx.postChild(parents[i], toks[i], claudeBody("c"+strconv.Itoa(i)), "c"+strconv.Itoa(i))
			}()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("%d concurrent posts did not finish within 30 s (pool of %d)", len(parents), fx.pool.Config().MaxConns)
		}
		return codes
	}
	parents, toks := make([]string, n), make([]string, n)
	for i := range n {
		parents[i], toks[i] = fx.parent, fx.parentTok
	}
	created, limited := 0, 0
	for _, c := range posts(parents, toks) {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if created != maxRunningChildrenPerParent || limited != n-maxRunningChildrenPerParent {
		t.Fatalf("one account: %d created, %d limited of %d", created, limited, n)
	}

	// Many accounts at once: each holds its own lock; none waits on another.
	for i := range n {
		owner := "acct-" + strconv.Itoa(i)
		if _, err := db.InsertDelegationRule(context.Background(), fx.pool, db.DelegationRule{AccountID: owner, Engine: "claude",
			Runtime: runnerRuntimeDocker, DaemonID: fx.dc.ID, CreatedVia: "settings"}); err != nil {
			t.Fatal(err)
		}
		parents[i], toks[i] = fx.session(sessOpts{owner: owner})
	}
	for i, c := range posts(parents, toks) {
		if c != http.StatusCreated {
			t.Fatalf("account %d: %d", i, c)
		}
	}
}

func TestDelegationLocksAcquire(t *testing.T) {
	var l delegationLocks
	release, err := l.acquire(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := l.acquire(ctx, "a"); err == nil {
		t.Fatal("a second holder got the lock")
	}
	other, err := l.acquire(context.Background(), "b")
	if err != nil {
		t.Fatal("another account waited:", err)
	}
	other()
	release()
	again, err := l.acquire(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	again()
	if len(l.m) != 0 {
		t.Fatalf("entries left: %v", l.m)
	}
}

// Review m2: a parent that ends while its post waits for the lock asks nothing.
func TestChildrenParentEndedWhileWaiting(t *testing.T) {
	fx := newDelegFx(t)
	release, err := fx.api.delegLocks.acquire(context.Background(), "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		code int
		out  map[string]any
	}
	got := make(chan result, 1)
	go func() {
		code, out, _ := fx.postChild(fx.parent, fx.parentTok, codexBody(), "w")
		got <- result{code, out}
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET status = 'stopped', ended_at = now(), end_reason = 'stopped_by_user' WHERE id = $1::uuid`, fx.parent); err != nil {
		t.Fatal(err)
	}
	release()
	r := <-got
	if r.code != http.StatusConflict || asString(r.out["error"]) != msgDelegateEnded {
		t.Fatalf("%d %v", r.code, r.out)
	}
	fx.storedNothing(fx.parent)
}

// Review m3: a request whose card could not be written is withdrawn (503), never left pending.
func TestChildrenRequestWithdrawnWhenTheEventFails(t *testing.T) {
	fx := newDelegFx(t)
	pushed := 0
	fx.api.delegationPush = func(string, string, string) { pushed++ }
	ctx := context.Background()
	if _, err := fx.pool.Exec(ctx, `
		CREATE FUNCTION fail_delegation_event() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.kind = 'delegation_request' THEN RAISE EXCEPTION 'no'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER fail_delegation_event BEFORE INSERT ON agent_events FOR EACH ROW EXECUTE FUNCTION fail_delegation_event();`); err != nil {
		t.Fatal(err)
	}
	code, out, hdr := fx.postChild(fx.parent, fx.parentTok, codexBody(), "e")
	if code != http.StatusServiceUnavailable || asString(out["error"]) != msgRequestNotShown || hdr.Get("Retry-After") == "" {
		t.Fatalf("%d %v", code, out)
	}
	if n := fx.count(`SELECT count(*)::int FROM delegation_requests WHERE parent_session_id = $1::uuid AND status = 'pending'`, fx.parent); n != 0 {
		t.Fatalf("%d requests left pending", n)
	}
	if pushed != 0 {
		t.Fatal("pushed a request nobody can see")
	}
}

// Review m4: a daemon-path start records who made it for every principal but the person, so a
// service or runner-key start never classifies as a direct parent.
func TestDaemonStartRecordsStartedByForEveryPrincipal(t *testing.T) {
	fx := newDelegFx(t)
	for kind, p := range map[string]runnerPrincipal{
		"service":    {Kind: "service", Sub: "svc-1", OnBehalfOf: "owner-1"},
		"runner_key": {Kind: runnerKeyPrincipalKind},
		"":           {Kind: "human", Sub: "owner-1"},
	} {
		resp, apiErr := fx.api.StartSession(context.Background(), p,
			runnerStartRequest{Repo: "app", Prompt: "x", Runtime: runnerRuntimeDocker, Interaction: protocol.InteractionInteractive}, "")
		if apiErr != nil {
			t.Fatalf("%s: %d %s", kind, apiErr.Status, apiErr.Message)
		}
		fx.spawn()
		row := fx.row(resp.SessionID)
		if row.StartedByKind != kind || trustDirect(row) != (kind == "") {
			t.Fatalf("%q start: started_by_kind %q, trustDirect %v", kind, row.StartedByKind, trustDirect(row))
		}
	}
}

func TestDelegatedSpawnCarriesNoEnvOrCallback(t *testing.T) {
	fx := newDelegFx(t)
	fx.rule("claude", runnerRuntimeDocker, false)
	code, out, _ := fx.postChild(fx.parent, fx.parentTok, claudeBody("Plain"), "s")
	if code != http.StatusCreated {
		t.Fatalf("%d %v", code, out)
	}
	msg := fx.spawn()
	id := asString(out["child"].(map[string]any)["id"])
	if msg.SessionID != id || msg.ExtraEnv != nil || msg.Interaction != protocol.InteractionUnattended || len(msg.Plugins) != 0 ||
		msg.BoardID != "" || msg.BoardToken != "" || msg.DangerouslySkipPermissions {
		t.Fatalf("spawn = %+v", msg)
	}
	if n := fx.count(`SELECT count(*)::int FROM sessions WHERE id = $1::uuid AND callback_url IS NULL AND callback_secret IS NULL
		AND NOT auto_stop AND interaction = 'unattended'`, id); n != 1 {
		t.Fatal("the child row carries a callback, auto_stop or an interactive mode")
	}
}

func TestNoPushForPrivateOrHeadlessParent(t *testing.T) {
	fx := newDelegFx(t)
	var mu sync.Mutex
	var pushes [][3]string
	fx.api.delegationPush = func(heading, body, link string) {
		mu.Lock()
		defer mu.Unlock()
		pushes = append(pushes, [3]string{heading, body, link})
	}
	private, privateTok := fx.session(sessOpts{owner: "owner-1", private: true, title: "Hidden"})
	headless, headlessTok := fx.session(sessOpts{owner: "owner-1", title: "Quiet"})
	if err := db.SetSessionInteraction(context.Background(), fx.pool, headless, protocol.InteractionUnattended); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ sid, tok string }{"private": {private, privateTok}, "headless": {headless, headlessTok}} {
		if code, out, _ := fx.postChild(c.sid, c.tok, codexBody(), "p"); code != http.StatusAccepted {
			t.Fatalf("%s: %d %v", name, code, out)
		}
	}
	if len(pushes) != 0 {
		t.Fatalf("pushed for a private or headless parent: %v", pushes)
	}
	body := codexBody()
	body["title"] = "Review"
	if code, out, _ := fx.postChild(fx.parent, fx.parentTok, body, "p"); code != http.StatusAccepted {
		t.Fatalf("direct: %d %v", code, out)
	}
	want := [3]string{"Parent — wants to start a session", "Start a Codex session: Review", "/sessions/" + fx.parent}
	if len(pushes) != 1 || pushes[0] != want {
		t.Fatalf("pushes = %v, want [%v]", pushes, want)
	}
}
