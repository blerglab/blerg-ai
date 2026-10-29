package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/shellpolicy"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestShellPolicyAdminOriginLivenessAndDurability(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	deps.ShellPolicy = shellpolicy.New(pool)
	deps.InternalKey = "shell-component-fixture"
	svc := deps.Identity.(*identity.Service)
	ctx := t.Context()
	admin := insertCredAccount(t, pool, "shell-admin")
	member := insertCredAccount(t, pool, "shell-member")
	if _, err := pool.Exec(ctx, `UPDATE accounts SET role='admin' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	sid := insertLiveHumanSession(t, pool, admin)
	memberSid := insertLiveHumanSession(t, pool, member)
	mint := func(owner, login string) string {
		token, err := svc.MintHumanAccessTokenForSession(ctx, owner, "blerg-core", login)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	adminToken, memberToken := mint(admin, sid), mint(member, memberSid)
	server := httptest.NewServer(api.NewRouter(deps))
	defer server.Close()
	call := func(method, path, token, body, origin, key string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if key != "" {
			req.Header.Set("X-Internal-Key", key)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	for _, token := range []string{"", memberToken} {
		if code := call("GET", "/api/settings/shell-policy", token, "", "", ""); code == 200 {
			t.Fatal("non-admin read")
		}
	}
	if code := call("PUT", "/api/settings/shell-policy", adminToken, `{"enabled":false}`, "https://foreign.example", ""); code != 403 {
		t.Fatal("origin", code)
	}
	noSid, err := svc.MintHumanAccessToken(ctx, admin, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}
	if code := call("PUT", "/api/settings/shell-policy", noSid, `{"enabled":false}`, server.URL, ""); code != 403 {
		t.Fatal("sid-less", code)
	}
	for _, body := range []string{`{}`, `{"enabled":false,"other":true}`, `{"enabled":false}{}`} {
		if code := call("PUT", "/api/settings/shell-policy", adminToken, body, server.URL, ""); code != 400 {
			t.Fatal("bad body", body, code)
		}
	}
	if code := call("PUT", "/api/settings/shell-policy", adminToken, `{"enabled":false}`, server.URL, ""); code != 200 {
		t.Fatal("switch", code)
	}
	policy, err := shellpolicy.New(pool).Read(ctx)
	if err != nil || policy.Enabled || policy.Revision != 2 {
		t.Fatal("durability", policy, err)
	}
	if code := call("POST", "/internal/shell/authorize", "", `{"owner_id":"`+admin+`","login_id":"`+sid+`"}`, "", "wrong"); code != 401 {
		t.Fatal("component key", code)
	}
	if code := call("POST", "/internal/shell/authorize", "", `{"owner_id":"`+admin+`","login_id":"`+memberSid+`"}`, "", deps.InternalKey); code != 403 {
		t.Fatal("foreign login", code)
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET role='member' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	if code := call("PUT", "/api/settings/shell-policy", adminToken, `{"enabled":true}`, server.URL, ""); code != 403 {
		t.Fatal("demoted administrator retained switch authority", code)
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET role='admin' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeChain(ctx, sid, "test"); err != nil {
		t.Fatal(err)
	}
	if code := call("PUT", "/api/settings/shell-policy", adminToken, `{"enabled":true}`, server.URL, ""); code == 200 {
		t.Fatal("revoked admin")
	}
	if code := call("POST", "/internal/shell/authorize", "", `{"owner_id":"`+admin+`","login_id":"`+sid+`"}`, "", deps.InternalKey); code != 403 {
		t.Fatal("revoked login", code)
	}
}
func TestShellPolicyCrossReplicaCommitNotifications(t *testing.T) {
	_, pool := newTestDeps(t, nil, nil)
	svc := shellpolicy.New(pool)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	actor := insertCredAccount(t, pool, "shell-notify")
	sid := insertLiveHumanSession(t, pool, actor)
	events, stop, err := svc.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, err := shellpolicy.New(pool).Set(ctx, actor, false); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-events:
		if !ok {
			t.Fatal("listener closed")
		}
	case <-ctx.Done():
		t.Fatal("policy notification missing")
	}
	if _, err := pool.Exec(ctx, `UPDATE human_sessions SET revoked_at=now() WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-events:
		if !ok {
			t.Fatal("listener closed")
		}
	case <-ctx.Done():
		t.Fatal("logout notification missing")
	}
}
func TestShellControlAuthenticatedStreamAndCancellation(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	deps.ShellPolicy = shellpolicy.New(pool)
	deps.InternalKey = "shell-stream-fixture"
	server := httptest.NewServer(api.NewRouter(deps))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/internal/shell/control", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Internal-Key", deps.InternalKey)
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	line, err := bufio.NewReader(res.Body).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Policy struct {
			Enabled  bool
			Revision uint64
		}
		Revocations []json.RawMessage
	}
	if json.Unmarshal(line, &state) != nil || !state.Policy.Enabled || state.Policy.Revision != 1 {
		t.Fatal("initial authority", string(line))
	}
	cancel()
}

// A policy-row wait must not preserve an earlier authorization snapshot.
func TestShellPolicyQueuedWriteRechecksAuthority(t *testing.T) {
	for _, mode := range []string{"demotion", "logout"} {
		t.Run(mode, func(t *testing.T) {
			_, pool := newTestDeps(t, nil, nil)
			ctx := t.Context()
			account := insertCredAccount(t, pool, "queued-policy")
			if _, err := pool.Exec(ctx, `UPDATE accounts SET role='admin' WHERE id=$1`, account); err != nil {
				t.Fatal(err)
			}
			login := insertLiveHumanSession(t, pool, account)
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(ctx) }()
			if _, err = blocker.Exec(ctx, `SELECT singleton FROM shell_policy WHERE singleton FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			cfg := pool.Config().Copy()
			cfg.MaxConns = 5
			concurrent, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer concurrent.Close()
			result := make(chan error, 1)
			go func() { _, err := shellpolicy.New(concurrent).SetForLogin(ctx, account, login, true); result <- err }()
			deadline := time.After(5 * time.Second)
			for {
				var waiting bool
				err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND (query LIKE 'UPDATE shell_policy SET enabled=%' OR query LIKE 'SELECT singleton FROM shell_policy%'))`).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case <-deadline:
					t.Fatal("writer never blocked")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			query := `UPDATE accounts SET role='member' WHERE id=$1`
			if mode == "logout" {
				query = `UPDATE human_sessions SET revoked_at=now() WHERE account_id=$1`
			}
			if _, err = pool.Exec(ctx, query, account); err != nil {
				t.Fatal(err)
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-result; err == nil {
				t.Fatal("queued write survived withdrawal of authority")
			}
		})
	}
}
