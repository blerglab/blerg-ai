package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Exercise the internal wire boundary with legacy stored chain IDs, without
// changing the browser session or rewriting the stored grant provenance.
func TestStandingLiveness(t *testing.T) {
	for _, tc := range []struct {
		name, reason, lineage string
		standing              bool
		want                  int
	}{
		{"expired with replacement", "", "", true, 200},
		{"exact stays expired", "", "", false, 404},
		{"logout survives", "logout", "", true, 200},
		{"rotated survives", "rotated", "", true, 200},
		{"superseded survives", "superseded", "", true, 200},
		{"reuse denied", "reuse", "", true, 404},
		{"logout all denied", "logout_all", "", true, 404},
		{"unknown revocation denied", "future_reason", "", true, 404},
		{"empty revocation denied", "EMPTY", "", true, 404},
		{"reset after expired grant", "", "after", true, 404},
		{"reset at chain creation", "", "equal", true, 404},
		{"reset before chain creation", "", "before", true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, pool := newInternalTestDeps(t)
			account := insertCredAccount(t, pool, "standing-test")
			old := insertLiveHumanSession(t, pool, account)
			insertLiveHumanSession(t, pool, account)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, `UPDATE human_sessions SET created_at=now()-interval '2 days', expires_at=now()-interval '1 day' WHERE chain_id=$1`, old); err != nil {
				t.Fatal(err)
			}
			if tc.reason != "" {
				reason := tc.reason
				if reason == "EMPTY" {
					reason = ""
				}
				if _, err := pool.Exec(ctx, `UPDATE human_sessions SET revoked_at=now(),revoke_reason=$2 WHERE chain_id=$1`, old, reason); err != nil {
					t.Fatal(err)
				}
			}
			if tc.lineage != "" {
				offset := "0 seconds"
				if tc.lineage == "before" {
					offset = "-1 second"
				}
				if tc.lineage == "after" {
					offset = "1 second"
				}
				if _, err := pool.Exec(ctx, `INSERT INTO revocations(kind,value,revoked_at) SELECT 'lineage',$2,created_at+$3::interval FROM human_sessions WHERE chain_id=$1`, old, account, offset); err != nil {
					t.Fatal(err)
				}
			}
			srv := httptest.NewServer(api.NewRouter(deps))
			defer srv.Close()
			body, _ := json.Marshal(map[string]any{"account_id": account, "session_id": old, "standing": tc.standing})
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/credentials/list", bytes.NewReader(body))
			req.Header.Set("X-Internal-Key", testInternalKey)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("standing authority status=%d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

func TestStandingLivenessProvenance(t *testing.T) {
	for _, mode := range []string{"no live login", "foreign account", "unknown chain", "empty proof", "mixed proof", "token origin", "disabled", "password change required", "deleted account", "foreign refresh row", "first row timestamp", "restored login", "missing internal key"} {
		t.Run(mode, func(t *testing.T) {
			deps, pool := newInternalTestDeps(t)
			ctx := context.Background()
			account := insertCredAccount(t, pool, "standing-provenance")
			old := insertLiveHumanSession(t, pool, account)
			_, err := pool.Exec(ctx, `UPDATE human_sessions SET created_at=now()-interval '2 days',expires_at=now()-interval '1 day' WHERE chain_id=$1`, old)
			if err != nil {
				t.Fatal(err)
			}
			replacement := insertLiveHumanSession(t, pool, account)
			want := http.StatusNotFound
			mustExec := func(sql string, args ...any) {
				t.Helper()
				if _, err := pool.Exec(ctx, sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			body := map[string]any{"account_id": account, "session_id": old, "standing": true}
			switch mode {
			case "no live login":
				mustExec(`UPDATE human_sessions SET expires_at=now()-interval '1 day' WHERE id=$1`, replacement)
			case "foreign account":
				body["account_id"] = insertCredAccount(t, pool, "standing-other")
			case "unknown chain":
				body["session_id"] = noSuchSession
			case "empty proof":
				delete(body, "session_id")
				want = 400
			case "token origin":
				delete(body, "session_id")
				body["token_id"] = noSuchSession
			case "mixed proof":
				body["token_id"] = noSuchSession
				want = 400
			case "disabled":
				mustExec(`UPDATE accounts SET disabled_at=now() WHERE id=$1`, account)
			case "password change required":
				mustExec(`UPDATE accounts SET must_change_password=true WHERE id=$1`, account)
			case "deleted account":
				mustExec(`DELETE FROM accounts WHERE id=$1`, account)
			case "foreign refresh row":
				other := insertCredAccount(t, pool, "standing-foreign")
				row := insertLiveHumanSession(t, pool, other)
				mustExec(`UPDATE human_sessions SET chain_id=$1 WHERE id=$2`, old, row)
			case "first row timestamp":
				mustExec(`UPDATE human_sessions SET chain_id=$1 WHERE id=$2`, old, replacement)
				mustExec(`INSERT INTO revocations(kind,value,revoked_at) VALUES('lineage',$1,now()-interval '1 day')`, account)
			case "restored login":
				want = 200
			case "missing internal key":
				want = 401
			}
			srv := httptest.NewServer(api.NewRouter(deps))
			defer srv.Close()
			raw, _ := json.Marshal(body)
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/credentials/list", bytes.NewReader(raw))
			if mode != "missing internal key" {
				req.Header.Set("X-Internal-Key", testInternalKey)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != want {
				t.Fatalf("status=%d want %d", resp.StatusCode, want)
			}
		})
	}
}

func TestStandingBoardExchangeRechecksAfterMint(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(map[bool]string{false: "survives post-mint check", true: "reset during mint cleans up"}[reset], func(t *testing.T) {
			var e *mcpEnv
			e = boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/internal/boards/") {
					_ = json.NewEncoder(w).Encode(map[string]any{"board_id": "b1", "owner_account_id": e.acctA})
					return
				}
				if reset {
					if err := e.idSvc.RevokeAccountEverywhere(r.Context(), e.acctA); err != nil {
						t.Error(err)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"id": "b1"})
			})
			if _, err := e.pool.Exec(t.Context(), `UPDATE human_sessions SET expires_at=now()-interval '1 day' WHERE chain_id=$1`, e.sidA); err != nil {
				t.Fatal(err)
			}
			insertLiveHumanSession(t, e.pool, e.acctA)
			code, _ := e.exchange(t, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "standing": true, "target": "board", "board_id": "b1"})
			want := 200
			if reset {
				want = 404
			}
			if code != want {
				t.Fatalf("exchange=%d want %d", code, want)
			}
			var live int
			if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_tokens WHERE kind='exchange' AND revoked_at IS NULL`).Scan(&live); err != nil {
				t.Fatal(err)
			}
			if reset && live != 0 {
				t.Fatalf("authority loss left %d minted credentials live", live)
			}
		})
	}
}

func TestStandingCannotReviveProviderSessionMCP(t *testing.T) {
	e, in := newSessionDispatchAPI(t)
	insertLiveHumanSession(t, e.pool, e.acctA)
	if _, err := e.pool.Exec(t.Context(), `UPDATE human_sessions SET expires_at=now()-interval '1 day' WHERE chain_id=$1`, e.sidA); err != nil {
		t.Fatal(err)
	}
	// Single-browser sign-out suspends provider custody even while the same
	// account has another live chain and its ordinary standing grants can work.
	const refresh = "standing-provider-fixture-refresh"
	sum := sha256.Sum256([]byte(refresh))
	if _, err := e.pool.Exec(t.Context(), `UPDATE human_sessions SET token_hash=$2 WHERE chain_id=$1`, e.sidA, base64.RawURLEncoding.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if err := e.idSvc.RevokeHumanSession(t.Context(), refresh); err != nil {
		t.Fatal(err)
	}
	var lifecycle string
	if err := e.pool.QueryRow(t.Context(), `SELECT lifecycle FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "suspended" {
		t.Fatalf("provider lifecycle=%q want suspended", lifecycle)
	}
	raw, _ := json.Marshal(in)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	body["standing"] = true
	for _, path := range []string{"/internal/mcp/session-connections/admit", "/internal/mcp/session-connections/token"} {
		code, _, _ := e.internal(t, path, e.deps.InternalKey, body)
		if code < 400 {
			t.Fatalf("provider path %s revived another chain: %d", path, code)
		}
	}
	code, _, _ := e.internal(t, "/internal/mcp/connections/token", e.deps.InternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "connection_id": in.ConnectionID, "standing": true})
	if code != 404 {
		t.Fatalf("standing account fetch escaped provider custody: %d", code)
	}
}

func TestStandingFetchLogsLiveAuthorizingRow(t *testing.T) {
	for _, sameChain := range []bool{true, false} {
		t.Run(map[bool]string{true: "refreshed original chain", false: "replacement chain"}[sameChain], func(t *testing.T) {
			deps, pool := newInternalTestDeps(t)
			account := insertCredAccount(t, pool, "standing-fetch-audit")
			original := insertLiveHumanSession(t, pool, account)
			live := insertLiveHumanSession(t, pool, account)
			if _, err := pool.Exec(t.Context(), `UPDATE human_sessions SET expires_at=now()-interval '1 day' WHERE id=$1`, original); err != nil {
				t.Fatal(err)
			}
			if sameChain {
				if _, err := pool.Exec(t.Context(), `UPDATE human_sessions SET chain_id=$1 WHERE id=$2`, original, live); err != nil {
					t.Fatal(err)
				}
			}
			if err := deps.Credentials.Store(t.Context(), account, "claude", []byte("synthetic-test-credential")); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]any{"account_id": account, "session_id": original, "engine": "claude", "standing": true})
			req := httptest.NewRequest(http.MethodPost, "/internal/credentials/fetch", bytes.NewReader(body))
			req.Header.Set("X-Internal-Key", testInternalKey)
			rec := httptest.NewRecorder()
			api.NewRouter(deps).ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("fetch status=%d", rec.Code)
			}
			var logged string
			if err := pool.QueryRow(t.Context(), `SELECT fetched_by_session_id::text FROM credential_access_log WHERE account_id=$1`, account).Scan(&logged); err != nil {
				t.Fatal(err)
			}
			if logged != live {
				t.Fatalf("audit authorizer=%s want live row=%s (original provenance=%s)", logged, live, original)
			}
		})
	}
}

func TestStandingRefusalHeadersOnRC(t *testing.T) {
	for _, mode := range []string{"suspended", "revoked", "removed MCP", "board unavailable", "disabled", "password change required"} {
		t.Run(mode, func(t *testing.T) {
			e := boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) {
				if mode == "board unavailable" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				http.Error(w, "not found", http.StatusNotFound)
			})
			if mode == "disabled" {
				if _, err := e.pool.Exec(t.Context(), `UPDATE accounts SET disabled_at=now() WHERE id=$1`, e.acctA); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "password change required" {
				if _, err := e.pool.Exec(t.Context(), `UPDATE accounts SET must_change_password=true WHERE id=$1`, e.acctA); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "suspended" {
				if _, err := e.pool.Exec(t.Context(), `UPDATE human_sessions SET expires_at=now()-interval '1 day' WHERE account_id=$1`, e.acctA); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "revoked" {
				if err := e.idSvc.RevokeAccountEverywhere(t.Context(), e.acctA); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{"/internal/tokens/exchange", "/internal/mcp/connections/token", "/internal/boards/list"} {
				body := map[string]any{"account_id": e.acctA, "session_id": e.sidA, "standing": true, "target": "board", "board_id": "b1", "connection_id": noSuchSession}
				code, _, headers := e.internal(t, path, testInternalKey, body)
				wantState := "unavailable"
				switch {
				case mode == "suspended" || mode == "password change required":
					wantState = "suspended"
				case mode == "revoked" || mode == "disabled" || path == "/internal/mcp/connections/token":
					wantState = "revoked"
				}
				if headers.Get("X-Blerg-Standing-State") != wantState {
					t.Errorf("%s status=%d state=%q want %s", path, code, headers.Get("X-Blerg-Standing-State"), wantState)
				}

				if path == "/internal/mcp/connections/token" && mode == "removed MCP" && headers.Get("X-Blerg-Standing-State") != "revoked" {
					t.Error("removed MCP must explicitly classify revoked")
				}
			}
		})
	}
}

func TestStandingScopedBoardDiscoveryKeepsUnprovenAbsenceUnavailable(t *testing.T) {
	for _, tc := range []struct{ name, response, state string }{
		{"absent", `[]`, "unavailable"},
		{"present", `[{"id":"b1","name":"Fixture"}]`, "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.response)) })
			code, _, header := e.internal(t, "/internal/boards/list", testInternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "standing": true, "board_id": "b1"})
			if code != http.StatusOK || header.Get("X-Blerg-Standing-State") != tc.state {
				t.Fatalf("scoped discovery status=%d state=%q want %s", code, header.Get("X-Blerg-Standing-State"), tc.state)
			}
		})
	}
}
