package api_test

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/api"
)

func TestAccountRoutesRefuseSessionScopedConnection(t *testing.T) {
	e := newOAuthAPIEnv(t)
	id := e.connect(t, "scoped")
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET runner_installation_id='test-runner',runner_session_id=$2,bound_sid=$3,credential_generation=1,lifecycle='suspended',human_deadline=now()+interval '1 day',suspended_at=now(),cleanup_after=now()+interval '168 hours',refresh_fence=0 WHERE id=$1`, id, e.sidB, e.sidA); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := e.pool.QueryRow(ctx, `SELECT secret_ciphertext FROM mcp_connections WHERE id=$1`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, person := range []string{e.humanA, e.humanB} {
		for _, route := range []struct {
			method, path string
			body         any
		}{{http.MethodPatch, "/api/mcp/connections/" + id, map[string]string{"name": "changed"}}, {http.MethodDelete, "/api/mcp/connections/" + id, nil}, {http.MethodGet, "/api/mcp/connections/" + id + "/events", nil}, {http.MethodPost, "/api/mcp/connections/" + id + "/reconnect", map[string]string{}}} {
			resp, _ := e.req(t, route.method, route.path, person, route.body, sameOrigin)
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s returned %d", route.method, route.path, resp.StatusCode)
			}
		}
		resp, body := e.req(t, http.MethodGet, "/api/mcp/connections", person, nil, nil)
		if resp.StatusCode != 200 || strings.Contains(body, id) {
			t.Error("public list exposed scoped connection")
		}
	}
	for _, account := range []struct{ account, sid string }{{e.acctA, e.sidA}, {e.acctB, e.sidB}} {
		resp, body := e.internalReq(t, map[string]string{"account_id": account.account, "session_id": account.sid, "connection_id": id})
		if resp.StatusCode != 404 || strings.Contains(body, id) {
			t.Error("internal token exposed scoped connection")
		}
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/internal/mcp/connections/list", strings.NewReader(`{"account_id":"`+account.account+`","session_id":"`+account.sid+`"}`))
		req.Header.Set("Authorization", "Bearer "+testInternalKey)
		rec := httptest.NewRecorder()
		api.NewRouter(e.deps).ServeHTTP(rec, req)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), id) {
			t.Error("internal list exposed scoped connection")
		}
	}
	var after []byte
	var generation int64
	if err := e.pool.QueryRow(ctx, `SELECT secret_ciphertext,credential_generation FROM mcp_connections WHERE id=$1`, id).Scan(&after, &generation); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || generation != 1 {
		t.Fatal("account route mutated scoped credential")
	}
}

func TestCallbackExchangeErrorBodyCanary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"custom_error", 400, `{"error":"provider_error_canary","error_description":"secret description"}`},
		{"known_error", 401, `{"error":"invalid_grant","error_description":"provider_error_canary"}`},
		{"server_error", 500, `provider_error_canary`},
		{"malformed_success", 200, `provider_error_canary`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newOAuthAPIEnv(t)
			e.as.ExchangeStatus = tc.status
			e.as.ExchangeRaw = tc.body
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(old) })
			auth, cookie := e.begin(t, e.humanA, "notes")
			q, err := e.as.Approve(auth)
			if err != nil {
				t.Fatal(err)
			}
			resp, body := e.callback(t, q, cookie)
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatal("callback did not return controlled result")
			}
			for _, out := range []string{logs.String(), body, resp.Header.Get("Location")} {
				if strings.Contains(out, "provider_error_canary") || strings.Contains(out, "secret description") {
					t.Fatal("exchange failure leaked provider error")
				}
			}
			if !strings.Contains(logs.String(), "cause=") {
				t.Fatal("exchange failure missing classified cause")
			}
		})
	}
}

func TestInternalTokenErrorBodyCanary(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status         int
		errorCode, raw string
		want           int
	}{
		{"custom_error", 400, "provider_error_canary", "", 200},
		{"known_error", 400, "invalid_grant", "", 404},
		{"server_error", 500, "provider_error_canary", "", 200},
		{"malformed_success", 0, "", `provider_error_canary`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newOAuthAPIEnv(t)
			e.as.SetAccessTTL(30)
			id := e.connect(t, "notes")
			e.as.Lock()
			e.as.RefreshStatus = tc.status
			e.as.RefreshError = tc.errorCode
			e.as.RefreshRaw = tc.raw
			e.as.Unlock()
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(old) })
			resp, body := e.internalReq(t, map[string]string{"account_id": e.acctA, "session_id": e.sidA, "connection_id": id})
			if resp.StatusCode != tc.want {
				t.Fatalf("internal result: %d, want %d", resp.StatusCode, tc.want)
			}
			if strings.Contains(logs.String(), "provider_error_canary") || strings.Contains(body, "provider_error_canary") {
				t.Fatal("internal token error leaked provider body")
			}
			if !strings.Contains(logs.String(), "cause=") {
				t.Fatal("refresh failure missing classified cause")
			}
		})
	}
}

func TestLiveDataProposalsExcludeSessionScopedConnections(t *testing.T) {
	e := newLiveApprovalEnv(t)
	ctx := context.Background()
	// The account proposal path must not admit this connection merely because its
	// owner, public status and tool metadata otherwise match.
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET auth_kind='oauth',runner_installation_id='runner',runner_session_id=$2,bound_sid=$3,credential_generation=1,lifecycle='suspended',human_deadline=now()+interval '1 day',suspended_at=now(),cleanup_after=now()+interval '168 hours',refresh_fence=0 WHERE id=$1`, e.connA, e.sidB, e.sidA); err != nil {
		t.Fatal(err)
	}
	config := liveConfig(e, "00000000-0000-4000-8000-000000000514", 1)
	code, _ := liveCall(t, e, "POST", "/internal/live-data/proposals", testInternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "configuration": config, "ask_id": liveAskID(config)})
	if code != 404 {
		t.Fatalf("scoped connection accepted for account live-data proposal: %d", code)
	}
}
