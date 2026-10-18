package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

func internalPluginPost(t *testing.T, srv *httptest.Server, path, key string, body any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Internal-Key", key)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func TestInternalPluginCatalogAndApprovedAdd(t *testing.T) {
	deps, srv, account, human := newPluginsTestDeps(t, "")
	sid := insertLiveHumanSession(t, deps.Store.Pool(), account)
	body := map[string]any{"account_id": account, "session_id": sid, "engine": "claude"}
	if code, _ := pluginReq(t, srv, http.MethodPut, human, putBody{[]pluginspec.Entry{entry("existing")}}); code != 200 {
		t.Fatal(code)
	}
	code, raw := internalPluginPost(t, srv, "/internal/plugins/catalog", testInternalKey, body)
	if code != 200 || !strings.Contains(raw, officialMarketplace) || !strings.Contains(raw, `"max":20`) || !strings.Contains(raw, "existing") {
		t.Fatalf("catalog=%d %s", code, raw)
	}
	body["entry"] = entry("added")
	for range 2 {
		if code, raw = internalPluginPost(t, srv, "/internal/plugins/add", testInternalKey, body); code != 200 {
			t.Fatalf("add=%d %s", code, raw)
		}
	}
	_, raw = pluginReq(t, srv, http.MethodGet, human, nil)
	var list struct {
		Plugins []pluginspec.Entry `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Plugins) != 2 || list.Plugins[0] != entry("existing") || list.Plugins[1] != entry("added") {
		t.Fatalf("did not preserve/idempotently append: %+v", list.Plugins)
	}
}

func TestInternalPluginPolicyContainsNoAccountSettings(t *testing.T) {
	_, srv, _, human := newPluginsTestDeps(t, "")
	if code, _ := pluginReq(t, srv, http.MethodPut, human, putBody{[]pluginspec.Entry{entry("private-choice")}}); code != 200 {
		t.Fatal(code)
	}
	for _, engine := range []string{"claude", "codex"} {
		code, raw := internalPluginPost(t, srv, "/internal/plugins/policy", testInternalKey, map[string]string{"engine": engine})
		if code != 200 || !strings.Contains(raw, `"plugins":[]`) || !strings.Contains(raw, officialMarketplace) || strings.Contains(raw, "private-choice") {
			t.Fatalf("policy=%d %s", code, raw)
		}
	}
	for _, tc := range []struct {
		key  string
		body any
		want int
	}{
		{"", map[string]string{"engine": "claude"}, 401},
		{testInternalKey, map[string]string{"engine": "unknown"}, 400},
		{testInternalKey, map[string]string{"engine": "claude", "account_id": "caller-input"}, 400},
		{testInternalKey, map[string]string{"engine": strings.Repeat("x", 17<<10)}, 413},
	} {
		if code, _ := internalPluginPost(t, srv, "/internal/plugins/policy", tc.key, tc.body); code != tc.want {
			t.Fatalf("invalid policy=%d want%d", code, tc.want)
		}
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/internal/plugins/policy", strings.NewReader(`{"engine":"claude"}{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Internal-Key", testInternalKey)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Fatal("trailing policy JSON accepted")
	}
}

func TestInternalPluginAddRefusesInvalidAuthorityAndPolicy(t *testing.T) {
	deps, srv, account, human := newPluginsTestDeps(t, "")
	pool := deps.Store.Pool()
	sid := insertLiveHumanSession(t, pool, account)
	other := insertCredAccount(t, pool, "plugins-add-other")
	foreign := insertLiveHumanSession(t, pool, other)
	tok, _, err := deps.Identity.(*identity.Service).CreateAgentToken(t.Context(), account, "plugins-agent", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, key, sid, token string
		entry                 pluginspec.Entry
		want                  int
	}{
		{"missing key", "", sid, "", entry("ok"), 401},
		{"foreign session", testInternalKey, foreign, "", entry("ok"), 404},
		{"no proof", testInternalKey, "", "", entry("ok"), 400},
		{"agent proof", testInternalKey, "", tok.ID, entry("ok"), 400},
		{"both proofs", testInternalKey, sid, tok.ID, entry("ok"), 400},
		{"denied marketplace", testInternalKey, sid, "", pluginspec.Entry{Marketplace: "evil/plugins", Plugin: "ok"}, 422},
		{"path name", testInternalKey, sid, "", entry("../bad"), 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"account_id": account, "engine": "claude", "entry": tc.entry}
			if tc.sid != "" {
				body["session_id"] = tc.sid
			}
			if tc.token != "" {
				body["token_id"] = tc.token
			}
			code, raw := internalPluginPost(t, srv, "/internal/plugins/add", tc.key, body)
			if code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", code, tc.want, raw)
			}
		})
	}
	if _, err := pool.Exec(t.Context(), `UPDATE human_sessions SET revoked_at=now() WHERE chain_id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if code, _ := internalPluginPost(t, srv, "/internal/plugins/add", testInternalKey, map[string]any{"account_id": account, "session_id": sid, "engine": "claude", "entry": entry("ok")}); code != 404 {
		t.Fatalf("revoked=%d", code)
	}
	_, raw := pluginReq(t, srv, http.MethodGet, human, nil)
	if !strings.Contains(raw, `"plugins":[]`) {
		t.Fatal("refused request mutated settings")
	}
}

func TestInternalPluginConcurrentAddsPreserveEntries(t *testing.T) {
	deps, srv, account, human := newPluginsTestDeps(t, "")
	sid := insertLiveHumanSession(t, deps.Store.Pool(), account)
	const n = 12
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			code, raw := internalPluginPost(t, srv, "/internal/plugins/add", testInternalKey, map[string]any{"account_id": account, "session_id": sid, "engine": "codex", "entry": entry(fmt.Sprintf("plugin-%d", i))})
			if code != 200 {
				t.Errorf("concurrent add=%d %s", code, raw)
			}
		})
	}
	wg.Wait()
	code, raw := internalPluginPost(t, srv, "/internal/plugins/catalog", testInternalKey, map[string]any{"account_id": account, "session_id": sid, "engine": "codex"})
	var list struct {
		Plugins []pluginspec.Entry `json:"plugins"`
	}
	if code != 200 || json.Unmarshal([]byte(raw), &list) != nil || len(list.Plugins) != n {
		t.Fatalf("concurrent list=%d %s", code, raw)
	}
	// Appending for Codex never changes the same account's Claude list.
	_, raw = pluginReq(t, srv, http.MethodGet, human, nil)
	if !strings.Contains(raw, `"plugins":[]`) {
		t.Fatal("cross-engine mutation")
	}
}

func TestInternalPluginAddRechecksProofAfterSettingsLock(t *testing.T) {
	for _, change := range []string{"logout", "disabled", "role", "expired", "password"} {
		t.Run(change, func(t *testing.T) {
			deps, srv, account, _ := newPluginsTestDeps(t, "")
			pool := deps.Store.Pool()
			// The fixture deliberately caps its shared pool at two connections;
			// one holds the fence and the other is the blocked request. Observe
			// and revoke through a separate control connection to the same DB.
			control, err := pgxpool.NewWithConfig(t.Context(), pool.Config())
			if err != nil {
				t.Fatal(err)
			}
			defer control.Close()
			sid := insertLiveHumanSession(t, pool, account)
			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(t.Context()) }()
			if _, err := tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "user_plugins:"+account+":claude"); err != nil {
				t.Fatal(err)
			}
			result := make(chan int, 1)
			go func() {
				code, _ := internalPluginPost(t, srv, "/internal/plugins/add", testInternalKey, map[string]any{"account_id": account, "session_id": sid, "engine": "claude", "entry": entry("waited")})
				result <- code
			}()
			deadline := time.After(5 * time.Second)
			for {
				var waiting bool
				if err := control.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%pg_advisory_xact_lock%')`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case <-deadline:
					t.Fatal("request never waited on the Settings fence")
				case <-time.After(time.Millisecond):
				}
			}
			sql, arg := `UPDATE human_sessions SET revoked_at=now() WHERE chain_id=$1`, sid
			switch change {
			case "disabled":
				sql, arg = `UPDATE accounts SET disabled_at=now() WHERE id=$1`, account
			case "role":
				sql, arg = `UPDATE accounts SET role='unknown-no-capabilities' WHERE id=$1`, account
			case "expired":
				sql = `UPDATE human_sessions SET expires_at=now()-interval '1 second' WHERE chain_id=$1`
			case "password":
				sql, arg = `UPDATE accounts SET must_change_password=true WHERE id=$1`, account
			}
			if _, err := control.Exec(t.Context(), sql, arg); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-result:
				if code != 404 {
					t.Fatalf("changed proof=%d want404", code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("add did not finish")
			}
			var count int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM user_plugins WHERE account_id=$1`, account).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("stale proof persisted a plugin")
			}
		})
	}
}

func TestInternalPluginAddBoundsAndStrictProtocol(t *testing.T) {
	deps, srv, account, human := newPluginsTestDeps(t, "")
	sid := insertLiveHumanSession(t, deps.Store.Pool(), account)
	entries := make([]pluginspec.Entry, pluginspec.MaxEntries)
	for i := range entries {
		entries[i] = entry(fmt.Sprintf("kept-%d", i))
	}
	if code, _ := pluginReq(t, srv, http.MethodPut, human, putBody{entries}); code != 200 {
		t.Fatal(code)
	}
	body := map[string]any{"account_id": account, "session_id": sid, "engine": "claude", "entry": entry("extra")}
	if code, _ := internalPluginPost(t, srv, "/internal/plugins/add", testInternalKey, body); code != 422 {
		t.Fatalf("capacity=%d", code)
	}
	body["entry"] = entries[0]
	if code, _ := internalPluginPost(t, srv, "/internal/plugins/add", testInternalKey, body); code != 200 {
		t.Fatalf("idempotent full list=%d", code)
	}
	valid, _ := json.Marshal(body)
	for _, tc := range []struct {
		name, raw string
		forwarded bool
		want      int
	}{
		{"trailing", string(valid) + `{}`, false, 400},
		{"oversized", strings.Repeat(" ", 17<<10) + string(valid), false, 413},
		{"unknown", strings.TrimSuffix(string(valid), "}") + `,"plugins":[]}`, false, 400},
		{"forwarded", string(valid), true, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/internal/plugins/add", strings.NewReader(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Internal-Key", testInternalKey)
			if tc.forwarded {
				req.Header.Set("Forwarded", "for=example")
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d", resp.StatusCode, tc.want)
			}
		})
	}
	_, raw := pluginReq(t, srv, http.MethodGet, human, nil)
	var list struct {
		Plugins []pluginspec.Entry `json:"plugins"`
	}
	if json.Unmarshal([]byte(raw), &list) != nil || len(list.Plugins) != pluginspec.MaxEntries {
		t.Fatal("invalid request altered existing settings")
	}
}

func TestInternalPluginAddRejectsProofExpiringDuringWrite(t *testing.T) {
	deps, srv, account, _ := newPluginsTestDeps(t, "")
	pool := deps.Store.Pool()
	sid := insertLiveHumanSession(t, pool, account)
	control, err := pgxpool.NewWithConfig(t.Context(), pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	// A trigger gives the write a deterministic wait after its initial proof
	// check. Time can expire even while the approval holds every row lock.
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION pause_plugin_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtextextended(current_database()||':plugin-test-write',0)); RETURN NEW; END $$; CREATE TRIGGER pause_plugin_write BEFORE INSERT ON user_plugins FOR EACH ROW EXECUTE FUNCTION pause_plugin_write()`); err != nil {
		t.Fatal(err)
	}
	tx, err := control.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended(current_database()||':plugin-test-write',0))`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE human_sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE chain_id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() {
		code, _ := internalPluginPost(t, srv, "/internal/plugins/add", testInternalKey, map[string]any{"account_id": account, "session_id": sid, "engine": "claude", "entry": entry("expired-write")})
		result <- code
	}()
	deadline := time.After(5 * time.Second)
	for {
		var waiting bool
		if err := control.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE 'INSERT INTO user_plugins%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-deadline:
			t.Fatal("write never reached trigger")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := control.Exec(t.Context(), `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM expires_at-clock_timestamp()))+0.02) FROM human_sessions WHERE chain_id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-result:
		if code != 404 {
			t.Fatalf("expired write=%d want404", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write did not finish")
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM user_plugins WHERE account_id=$1`, account).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("expired proof committed plugin")
	}
}
