package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/plugins"
)

func TestMarketplacePolicyAdminAuditPropagationAndRetention(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	allow, _ := pluginspec.ParseAllowlist("")
	deps.Plugins = plugins.NewService(db.NewPgStore(pool), allow)
	deps.InternalKey = "marketplace-fixture"
	admin := insertCredAccount(t, pool, "marketplace-admin")
	member := insertCredAccount(t, pool, "marketplace-member")
	if _, err := pool.Exec(t.Context(), `UPDATE accounts SET role='admin' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	sid := insertLiveHumanSession(t, pool, admin)
	memberSid := insertLiveHumanSession(t, pool, member)
	mint := func(owner, login string) string {
		token, err := deps.Identity.(*identity.Service).MintHumanAccessTokenForSession(t.Context(), owner, "blerg-core", login)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	token, memberToken := mint(admin, sid), mint(member, memberSid)
	server := httptest.NewServer(api.NewRouter(deps))
	defer server.Close()
	call := func(method, path, bearer, origin, key string, body any) (int, []byte) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(t.Context(), method, server.URL+path, strings.NewReader(string(raw)))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if key != "" {
			req.Header.Set("X-Internal-Key", key)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	path := "/api/settings/plugin-marketplaces"
	code, body := call("GET", path, token, "", "", nil)
	var p pluginspec.Policy
	if code != 200 || json.Unmarshal(body, &p) != nil || len(p.Marketplaces) != 3 || p.PolicyID == "" {
		t.Fatalf("defaults %d %s", code, body)
	}
	for _, m := range p.Marketplaces {
		if !m.Enabled {
			t.Fatal("official default off", m)
		}
	}
	// Fresh Core replica ignores changed env after the first durable initialization.
	other, _ := pluginspec.ParseAllowlist("example-org/other")
	persisted, err := plugins.NewService(db.NewPgStore(pool), other).Policy(t.Context())
	if err != nil || persisted.PolicyID != p.PolicyID || !persisted.Allowlist().Allows("codex:openai-curated-remote") || persisted.Allowlist().Allows("example-org/other") {
		t.Fatal("environment overwrote policy", persisted, err)
	}
	e := pluginspec.Entry{Marketplace: "codex:openai-curated-remote", Plugin: "openai-developers"}
	if err := deps.Plugins.Replace(t.Context(), admin, "codex", []pluginspec.Entry{e}); err != nil {
		t.Fatal(err)
	}
	for i := range p.Marketplaces {
		p.Marketplaces[i].Enabled = false
	}
	update := func(p pluginspec.Policy) map[string]any {
		return map[string]any{"revision": p.Revision, "marketplaces": p.Marketplaces, "any_marketplace": p.AnyMarketplace}
	}
	for _, bearer := range []string{"", memberToken} {
		for _, method := range []string{"GET", "PUT"} {
			if code, _ := call(method, path, bearer, server.URL, "", update(p)); code == 200 {
				t.Fatal("non-admin policy access", method)
			}
		}
	}
	agent, err := deps.Identity.(*identity.Service).MintAgentToken(t.Context(), identity.AgentTokenInput{Sub: admin, Aud: "blerg-core", OnBehalfOf: admin, Lineage: admin, Caps: []string{"account.manage"}})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := call("PUT", path, agent, server.URL, "", update(p)); code != 403 {
		t.Fatal("agent changed policy", code)
	}
	if code, _ := call("PUT", path, token, "https://foreign.example", "", update(p)); code != 403 {
		t.Fatal("foreign origin", code)
	}
	invalid := p
	invalid.Marketplaces = append([]pluginspec.Marketplace(nil), p.Marketplaces[1:]...)
	if code, _ := call("PUT", path, token, server.URL, "", update(invalid)); code != 422 {
		t.Fatal("removed default", code)
	}
	invalid = p
	invalid.Marketplaces = append(append([]pluginspec.Marketplace(nil), p.Marketplaces...), pluginspec.Marketplace{Source: "../escape", Enabled: true})
	if code, _ := call("PUT", path, token, server.URL, "", update(invalid)); code != 422 {
		t.Fatal("invalid source", code)
	}
	code, body = call("PUT", path, token, server.URL, "", update(p))
	if code != 200 {
		t.Fatalf("admin save %d %s", code, body)
	}
	if code, _ := call("PUT", path, token, server.URL, "", update(p)); code != 409 {
		t.Fatal("stale save clobbered policy", code)
	}
	var revision int
	var actor, login string
	var before, after []byte
	if err := pool.QueryRow(t.Context(), `SELECT revision,actor_id::text,login_id::text,before_policy,after_policy FROM plugin_marketplace_audit WHERE revision=2`).Scan(&revision, &actor, &login, &before, &after); err != nil || actor != admin || login != sid || len(before) == 0 || len(after) == 0 {
		t.Fatal("missing audit", actor, login, err)
	}
	entries, err := deps.Plugins.List(t.Context(), admin, "codex")
	if err != nil || len(entries) != 1 || entries[0] != e {
		t.Fatal("silent uninstall", entries, err)
	}
	if err := deps.Plugins.AddApproved(t.Context(), admin, sid, "codex", pluginspec.Entry{Marketplace: e.Marketplace, Plugin: "other"}); err == nil {
		t.Fatal("disabled marketplace accepted install")
	}
	if err := deps.Plugins.Replace(t.Context(), admin, "codex", entries); err != nil {
		t.Fatal("could not retain disallowed plugin", err)
	}
	code, body = call("GET", "/api/plugins/codex", token, "", "", nil)
	if code != 200 || !strings.Contains(string(body), `"allowed_marketplaces":[]`) || !strings.Contains(string(body), e.Plugin) {
		t.Fatalf("disallowed listing %d %s", code, body)
	}
	code, body = call("POST", "/internal/plugins/marketplace-policy", "", "", deps.InternalKey, nil)
	if code != 200 || json.Unmarshal(body, &persisted) != nil || persisted.PolicyID != p.PolicyID || persisted.UpdatedBy != "" || strings.Contains(string(body), "updated_by") || persisted.Allowlist().Allows(e.Marketplace) {
		t.Fatalf("runner policy not updated %d %s", code, body)
	}
	if code, _ := call("POST", "/internal/plugins/marketplace-policy", "", "", "wrong", nil); code != 401 {
		t.Fatal("missing component authorization", code)
	}
	// A token minted while admin cannot write after demotion.
	if _, err := pool.Exec(t.Context(), `UPDATE accounts SET role='member' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	persisted.AnyMarketplace = true
	if code, _ := call("PUT", path, token, server.URL, "", update(persisted)); code != 403 {
		t.Fatal("demoted admin retained authority", code)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE accounts SET role='admin' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	if err := deps.Identity.(*identity.Service).RevokeChain(t.Context(), sid, "test"); err != nil {
		t.Fatal(err)
	}
	if code, _ := call("PUT", path, token, server.URL, "", update(persisted)); code == 200 {
		t.Fatal("revoked login changed policy")
	}
}

func TestMarketplacePolicyOversizeSeedDoesNotPersistInvalidAuthority(t *testing.T) {
	_, pool := newTestDeps(t, nil, nil)
	var sources []string
	for i := range 101 {
		sources = append(sources, fmt.Sprintf("example-org/market-%d", i))
	}
	allow, _ := pluginspec.ParseAllowlist(strings.Join(sources, ","))
	svc := plugins.NewService(db.NewPgStore(pool), allow)
	if _, err := svc.Policy(t.Context()); err == nil {
		t.Fatal("oversize initial policy accepted")
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM plugin_marketplace_policy`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid initial policy was durably saved", count, err)
	}
	defaults, _ := pluginspec.ParseAllowlist("")
	if _, err := plugins.NewService(db.NewPgStore(pool), defaults).Policy(t.Context()); err != nil {
		t.Fatal("a corrected environment could not seed the install", err)
	}
}

func TestMarketplacePolicyResetThenRevoke(t *testing.T) {
	_, pool := newTestDeps(t, nil, nil)
	allow, _ := pluginspec.ParseAllowlist("*")
	svc := plugins.NewService(db.NewPgStore(pool), allow)
	admin := insertCredAccount(t, pool, "reset-policy-admin")
	if _, err := pool.Exec(t.Context(), `UPDATE accounts SET role='admin' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	sid := insertLiveHumanSession(t, pool, admin)
	if _, err := svc.Policy(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE plugin_marketplace_policy SET revision=7 WHERE singleton`); err != nil {
		t.Fatal(err)
	}
	old, err := svc.Policy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	holder := &pluginspec.LivePolicy{}
	if err := holder.Apply(old, time.Second); err != nil {
		t.Fatal(err)
	}
	// A reset (or pre-ID backup restore followed by migration) creates a new ID.
	if _, err := pool.Exec(t.Context(), `TRUNCATE plugin_marketplace_policy, plugin_marketplace_audit`); err != nil {
		t.Fatal(err)
	}
	current, err := svc.Policy(t.Context())
	if err != nil || current.PolicyID == "" || current.PolicyID == old.PolicyID {
		t.Fatal("reset reused policy ID", current, err)
	}
	current.AnyMarketplace = false
	for i := range current.Marketplaces {
		current.Marketplaces[i].Enabled = false
	}
	revoke, err := svc.SetPolicy(t.Context(), admin, sid, current.Revision, current)
	if err != nil || revoke.PolicyID != current.PolicyID {
		t.Fatal("edit changed policy ID", revoke, err)
	}
	if err := holder.Apply(revoke, time.Second); err != nil {
		t.Fatal("reset froze revoke", err)
	}
	if holder.Allowlist(allow).Allows("codex:openai-curated") {
		t.Fatal("reset revoke ignored")
	}
}
