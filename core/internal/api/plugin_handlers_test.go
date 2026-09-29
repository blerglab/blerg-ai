package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/plugins"
)

const officialMarketplace = "anthropics/claude-plugins-official"

func newPluginsTestDeps(t *testing.T, allowlist string) (api.Deps, *httptest.Server, string, string) {
	t.Helper()
	deps, pool := newInternalTestDeps(t)
	allow, _ := pluginspec.ParseAllowlist(allowlist)
	deps.Plugins = plugins.NewService(db.NewPgStore(pool), allow)
	accountID := insertCredAccount(t, pool, "plugins-owner")
	human, err := deps.Identity.(*identity.Service).MintHumanAccessToken(context.Background(), accountID, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)
	return deps, srv, accountID, human
}

func pluginReq(t *testing.T, srv *httptest.Server, method, bearer string, body any) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, srv.URL+"/api/plugins/claude", rdr)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

type putBody struct {
	Plugins []pluginspec.Entry `json:"plugins"`
}

func entry(p string) pluginspec.Entry {
	return pluginspec.Entry{Marketplace: officialMarketplace, Plugin: p}
}

func TestPluginsHumanRoundTripKeepsOrder(t *testing.T) {
	_, srv, _, human := newPluginsTestDeps(t, "")
	if code, body := pluginReq(t, srv, http.MethodGet, human, nil); code != 200 || !strings.Contains(body, `"plugins":[]`) {
		t.Fatalf("empty GET = %d %s", code, body)
	}
	code, body := pluginReq(t, srv, http.MethodPut, human, putBody{[]pluginspec.Entry{entry("superpowers"), entry("frontend-design")}})
	if code != 200 {
		t.Fatalf("PUT = %d %s", code, body)
	}
	var got struct {
		Plugins []pluginspec.Entry `json:"plugins"`
		Allowed []string           `json:"allowed_marketplaces"`
		Max     int                `json:"max"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Plugins) != 2 || got.Plugins[0].Plugin != "superpowers" || got.Plugins[1].Plugin != "frontend-design" {
		t.Fatalf("order not kept: %+v", got.Plugins)
	}
	if len(got.Allowed) != 1 || got.Allowed[0] != officialMarketplace || got.Max != 20 {
		t.Fatalf("allow-list/max not reported: %+v", got)
	}
	// Reorder and remove in one replace.
	if code, _ = pluginReq(t, srv, http.MethodPut, human, putBody{[]pluginspec.Entry{entry("frontend-design")}}); code != 200 {
		t.Fatalf("PUT 2 = %d", code)
	}
	_, body = pluginReq(t, srv, http.MethodGet, human, nil)
	if strings.Contains(body, "superpowers") || !strings.Contains(body, "frontend-design") {
		t.Fatalf("replace did not replace: %s", body)
	}
}

// The security core: only a human session may write; an agent token may neither write nor read.
func TestPluginsAgentTokenCannotReadOrWrite(t *testing.T) {
	deps, srv, accountID, human := newPluginsTestDeps(t, "")
	if code, _ := pluginReq(t, srv, http.MethodPut, human, putBody{[]pluginspec.Entry{entry("superpowers")}}); code != 200 {
		t.Fatal("setup PUT failed")
	}
	agentTok, err := deps.Identity.(*identity.Service).MintAgentToken(context.Background(), identity.AgentTokenInput{
		Sub: "11111111-1111-1111-1111-111111111111", Aud: "blerg-core",
		OnBehalfOf: accountID, Lineage: accountID, Caps: []string{"card.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		if code, _ := pluginReq(t, srv, m, agentTok, putBody{[]pluginspec.Entry{entry("evil")}}); code != http.StatusForbidden {
			t.Errorf("agent token %s = %d, want 403", m, code)
		}
		if code, _ := pluginReq(t, srv, m, "", putBody{[]pluginspec.Entry{}}); code != http.StatusUnauthorized {
			t.Errorf("no token %s = %d, want 401", m, code)
		}
	}
	_, body := pluginReq(t, srv, http.MethodGet, human, nil)
	if strings.Contains(body, "evil") {
		t.Fatalf("agent write leaked in: %s", body)
	}
}

func TestPluginsAreScopedToTheAccount(t *testing.T) {
	deps, srv, _, human := newPluginsTestDeps(t, "")
	pluginReq(t, srv, http.MethodPut, human, putBody{[]pluginspec.Entry{entry("superpowers")}})
	pool := deps.Store.Pool()
	otherID := insertCredAccount(t, pool, "plugins-other")
	other, _ := deps.Identity.(*identity.Service).MintHumanAccessToken(context.Background(), otherID, "blerg-core")
	_, body := pluginReq(t, srv, http.MethodGet, other, nil)
	if strings.Contains(body, "superpowers") {
		t.Fatalf("another account read this account's list: %s", body)
	}
	// Writing as the other account changes only the other account's list.
	pluginReq(t, srv, http.MethodPut, other, putBody{[]pluginspec.Entry{entry("frontend-design")}})
	_, mine := pluginReq(t, srv, http.MethodGet, human, nil)
	if !strings.Contains(mine, "superpowers") || strings.Contains(mine, "frontend-design") {
		t.Fatalf("cross-account write leaked: %s", mine)
	}
}

func TestPluginsValidation(t *testing.T) {
	_, srv, _, human := newPluginsTestDeps(t, "")
	many := make([]pluginspec.Entry, 21)
	for i := range many {
		many[i] = entry(fmt.Sprintf("p%d", i))
	}
	exact := many[:20]
	cases := []struct {
		name    string
		list    []pluginspec.Entry
		want    int
		errPart string
	}{
		{"ok", []pluginspec.Entry{entry("superpowers")}, 200, ""},
		{"20 entries ok", exact, 200, ""},
		{"21 entries", many, 422, "at most 20"},
		{"duplicate", []pluginspec.Entry{entry("a"), entry("a")}, 422, "duplicate"},
		{"duplicate case-insensitive marketplace", []pluginspec.Entry{entry("a"), {Marketplace: "Anthropics/Claude-Plugins-Official", Plugin: "a"}}, 422, "duplicate"},
		{"bad name uppercase", []pluginspec.Entry{entry("Super")}, 422, "plugin name"},
		{"bad name shell", []pluginspec.Entry{entry("a;rm -rf /")}, 422, "plugin name"},
		{"bad name at", []pluginspec.Entry{entry("a@b")}, 422, "plugin name"},
		{"empty name", []pluginspec.Entry{entry("")}, 422, "plugin name"},
		{"url marketplace", []pluginspec.Entry{{Marketplace: "https://github.com/anthropics/claude-plugins-official", Plugin: "x"}}, 422, "GitHub owner/repo"},
		{"file marketplace", []pluginspec.Entry{{Marketplace: "file:///etc", Plugin: "x"}}, 422, "GitHub owner/repo"},
		{"path marketplace", []pluginspec.Entry{{Marketplace: "../evil", Plugin: "x"}}, 422, "GitHub owner/repo"},
		{"valid but not allowed", []pluginspec.Entry{{Marketplace: "evil/plugins", Plugin: "x"}}, 422, "marketplace not allowed by this install"},
	}
	for _, tc := range cases {
		code, body := pluginReq(t, srv, http.MethodPut, human, putBody{tc.list})
		if code != tc.want || !strings.Contains(body, tc.errPart) {
			t.Errorf("%s: %d %q, want %d containing %q", tc.name, code, body, tc.want, tc.errPart)
		}
	}
	// A rejected write must leave the previous list untouched.
	pluginReq(t, srv, http.MethodPut, human, putBody{[]pluginspec.Entry{entry("keepme")}})
	pluginReq(t, srv, http.MethodPut, human, putBody{[]pluginspec.Entry{entry("keepme"), {Marketplace: "evil/x", Plugin: "y"}}})
	_, body := pluginReq(t, srv, http.MethodGet, human, nil)
	if !strings.Contains(body, "keepme") || strings.Contains(body, "evil/x") {
		t.Fatalf("rejected write changed the list: %s", body)
	}
	// Missing "plugins" and unknown fields are 400s.
	if code, _ := pluginReq(t, srv, http.MethodPut, human, map[string]any{}); code != 400 {
		t.Errorf("missing plugins = %d, want 400", code)
	}
	if code, _ := pluginReq(t, srv, http.MethodPut, human, map[string]any{"plugins": []any{}, "extra": 1}); code != 400 {
		t.Errorf("unknown field = %d, want 400", code)
	}
}

func TestPluginsAllowlistWildcardAndUnknownEngine(t *testing.T) {
	_, srv, _, human := newPluginsTestDeps(t, "*")
	if code, body := pluginReq(t, srv, http.MethodPut, human, putBody{[]pluginspec.Entry{{Marketplace: "some/marketplace", Plugin: "x"}}}); code != 200 {
		t.Fatalf("* allow-list = %d %s", code, body)
	}
	if code, _ := pluginReq(t, srv, http.MethodPut, human, putBody{[]pluginspec.Entry{{Marketplace: "file:///x", Plugin: "x"}}}); code != 422 {
		t.Fatalf("* must still reject invalid sources, got %d", code)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/plugins/codex", nil)
	req.Header.Set("Authorization", "Bearer "+human)
	resp, _ := srv.Client().Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown engine = %d, want 404", resp.StatusCode)
	}
}

func TestInternalPluginsListRequiresLivenessProof(t *testing.T) {
	deps, srv, accountID, human := newPluginsTestDeps(t, "")
	ctx := context.Background()
	pluginReq(t, srv, http.MethodPut, human, putBody{[]pluginspec.Entry{entry("superpowers")}})
	pool := deps.Store.Pool()
	post := func(key string, body map[string]any) (int, string) {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/plugins/list", bytes.NewReader(raw))
		if key != "" {
			req.Header.Set("X-Internal-Key", key)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	base := func(extra map[string]any) map[string]any {
		m := map[string]any{"account_id": accountID, "engine": "claude"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	if code, _ := post("", base(map[string]any{"session_id": noSuchSession})); code != 401 {
		t.Errorf("no key = %d, want 401", code)
	}
	if code, _ := post("wrong", base(map[string]any{"session_id": noSuchSession})); code != 401 {
		t.Errorf("wrong key = %d, want 401", code)
	}
	// The proof is mandatory: neither, or both, is a 400 before any lookup.
	if code, _ := post(testInternalKey, base(nil)); code != 400 {
		t.Errorf("no liveness field = %d, want 400", code)
	}
	rec, _, err := deps.Identity.(*identity.Service).CreateAgentToken(ctx, accountID, "runner", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	sid := insertLiveHumanSession(t, pool, accountID)
	if code, _ := post(testInternalKey, base(map[string]any{"session_id": sid, "token_id": rec.ID})); code != 400 {
		t.Errorf("both liveness fields = %d, want 400", code)
	}
	if code, _ := post(testInternalKey, base(map[string]any{"token_id": "nope"})); code != 400 {
		t.Errorf("bad token_id = %d, want 400", code)
	}
	if code, _ := post(testInternalKey, map[string]any{"account_id": accountID, "engine": "nope", "session_id": sid}); code != 400 {
		t.Errorf("unknown engine = %d, want 400", code)
	}

	// The old bare claim is gone: human_session:true names nothing, so it is "neither" = 400.
	if code, _ := post(testInternalKey, base(map[string]any{"human_session": true})); code != 400 {
		t.Errorf("human_session:true = %d, want 400", code)
	}
	// A session that is not this account's (or does not exist): 404, the anti-enumeration answer.
	otherAcct := insertCredAccount(t, pool, "plugins-session-other")
	otherSid := insertLiveHumanSession(t, pool, otherAcct)
	if code, _ := post(testInternalKey, base(map[string]any{"session_id": otherSid})); code != 404 {
		t.Errorf("another account's session = %d, want 404", code)
	}
	if code, _ := post(testInternalKey, base(map[string]any{"session_id": noSuchSession})); code != 404 {
		t.Errorf("unknown session = %d, want 404", code)
	}
	code, body := post(testInternalKey, base(map[string]any{"session_id": sid}))
	if code != 200 || !strings.Contains(body, "superpowers") || strings.Contains(body, "allowed") {
		t.Fatalf("live session = %d %s", code, body)
	}
	// A live token works; a foreign account's token does not; a revoked token does not.
	if code, _ := post(testInternalKey, base(map[string]any{"token_id": rec.ID})); code != 200 {
		t.Errorf("live token = %d, want 200", code)
	}
	otherID := insertCredAccount(t, pool, "plugins-other-internal")
	if code, _ := post(testInternalKey, map[string]any{"account_id": otherID, "engine": "claude", "token_id": rec.ID}); code != 404 {
		t.Errorf("foreign token = %d, want 404", code)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_tokens SET revoked_at = now() WHERE id = $1`, rec.ID); err != nil {
		t.Fatal(err)
	}
	// A revoked token is the whole gate: the live human session must not rescue it.
	if code, _ := post(testInternalKey, base(map[string]any{"token_id": rec.ID})); code != 404 {
		t.Errorf("revoked token = %d, want 404", code)
	}
}

func TestPluginsMigrationCreatesTable(t *testing.T) {
	deps, _, _, _ := newPluginsTestDeps(t, "")
	var n int
	if err := deps.Store.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM schema_migrations WHERE name = '014_user_plugins.sql'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("migration 014 not applied: n=%d err=%v", n, err)
	}
}

// Two saves racing on the same account: each answers 200 or 409 (never a 500), and whatever
// wins leaves a valid list.
func TestPluginsConcurrentPutsAreNeverA500(t *testing.T) {
	_, srv, _, human := newPluginsTestDeps(t, "")
	var wg sync.WaitGroup
	codes := make(chan int, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := pluginReq(t, srv, http.MethodPut, human, putBody{[]pluginspec.Entry{entry("superpowers"), entry("frontend-design")}})
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != http.StatusOK && c != http.StatusConflict {
			t.Errorf("concurrent PUT = %d, want 200 or 409", c)
		}
	}
	_, body := pluginReq(t, srv, http.MethodGet, human, nil)
	if !strings.Contains(body, "superpowers") || !strings.Contains(body, "frontend-design") {
		t.Fatalf("list after the race = %s", body)
	}
}
