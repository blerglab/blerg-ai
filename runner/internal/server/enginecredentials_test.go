package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engineaccess"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestFetchManagedAccessUsesRegisteredEngineMode(t *testing.T) {
	t.Cleanup(engineaccess.Register(engineaccess.TokenCodec("fixture-engine", "fixtureAccessTokens")))
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(engineaccess.Access{Mode: "fixtureAccessTokens", AccessToken: "fixture-private-access", AccountID: "fixture-provider", Generation: "fixture-generation", ExpiresAt: time.Now().Add(time.Hour)})
	}))
	defer core.Close()
	access, found, err := fetchManagedAccess(t.Context(), nil, core.URL, "fixture-key", managedAccessRequest{AccountID: "owner", Engine: "fixture-engine", SessionID: "proof"})
	if err != nil || !found || access.AccountID != "fixture-provider" {
		t.Fatal("registered second mode was rejected")
	}
	if _, found, err = fetchManagedAccess(t.Context(), nil, core.URL, "fixture-key", managedAccessRequest{AccountID: "owner", Engine: "codex", SessionID: "proof"}); err == nil || found {
		t.Fatal("foreign access mode crossed engine binding")
	}
}

func managedAccessFixture() managedAccess {
	return managedAccess{Mode: "chatgptAuthTokens", AccessToken: "fixture-access-private", AccountID: "fixture-provider-account", Generation: "00000000-0000-4000-8000-000000000011", ExpiresAt: time.Now().Add(time.Hour)}
}

func TestFetchManagedAccessFailsClosedAndRedacts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		ok, legacy bool
	}{
		{"usable", 200, "", true, false},
		{"opaque", 409, `{"code":"not_managed"}`, false, true},
		{"reconnect", 409, `{"code":"reconnect_required","detail":"fixture-private"}`, false, false},
		{"contention", 202, `{"code":"refresh_pending"}`, false, false},
		{"revoked", 404, "fixture-private", false, false},
		{"provider", 503, "fixture-private", false, false},
		{"malformed", 200, `{"access_token":"fixture-private"}`, false, false},
		{"oversized", 200, strings.Repeat("x", 33<<10), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request managedAccessRequest
				if r.URL.Path != "/internal/credentials/managed/access" || r.Header.Get("X-Internal-Key") != "fixture-key" || json.NewDecoder(r.Body).Decode(&request) != nil || request.AccountID != "fixture-owner" || request.SessionID != "fixture-sid" || request.TokenID != "" {
					t.Error("lost principal binding")
				}
				w.WriteHeader(tc.status)
				if tc.body == "" {
					_ = json.NewEncoder(w).Encode(managedAccessFixture())
				} else {
					_, _ = w.Write([]byte(tc.body))
				}
			}))
			defer core.Close()
			access, found, err := fetchManagedAccess(context.Background(), nil, core.URL, "fixture-key", managedAccessRequest{AccountID: "fixture-owner", Engine: "codex", SessionID: "fixture-sid"})
			if found != tc.ok || (access != nil) != tc.ok || (err == nil) != (tc.ok || tc.legacy) {
				t.Fatal("unexpected managed access result")
			}
			if err != nil && strings.Contains(err.Error(), "fixture-private") {
				t.Fatal("response escaped error boundary")
			}
		})
	}
}

func TestManagedAccessRejectsRedirect(t *testing.T) {
	called := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(managedAccessFixture())
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, _, err := fetchManagedAccess(context.Background(), nil, source.URL, "fixture-key", managedAccessRequest{AccountID: "owner", Engine: "codex", SessionID: "proof"})
	if err == nil || called {
		t.Fatal("followed credential redirect")
	}
}

// The access route takes only the pod's runtime credential (the daemon's
// BLERG_RUNNER_DAEMON_TOKEN, stripped from every agent process). The messaging
// token sits in the agent's own environment, so it is refused with 403.
func TestManagedCredentialCurrentPodAndOwnerBinding(t *testing.T) {
	for _, scenario := range []string{"current", "messaging-token", "foreign-session", "replaced-pod", "daemon", "stopped", "stop-during-provider", "replace-during-provider", "caller-account", "provider-failure"} {
		t.Run(scenario, func(t *testing.T) {
			api, _, dc, pool := spawnTokenFixture(t)
			ctx := context.Background()
			sid := "00000000-0000-4000-8000-000000000001"
			other := "00000000-0000-4000-8000-000000000002"
			for _, id := range []string{sid, other} {
				_, err := pool.Exec(ctx, `INSERT INTO sessions(id,daemon_id,status,project_path,repo,engine,runtime,spawning_account_id) VALUES($1,$2,'running','/repos/project','project','codex','cluster','fixture-owner')`, id, dc.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			launch := func(id string) (runtimeRaw, messageRaw, runtimeID string) {
				runtimeRaw, messageRaw, err := db.MintRuntimeLaunch(ctx, pool, id, "", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				row, err := db.ValidateRuntimeToken(ctx, pool, runtimeRaw)
				if err != nil {
					t.Fatal(err)
				}
				return runtimeRaw, messageRaw, row.ID
			}
			token, messaging, runtimeID := launch(sid)
			otherToken, _, _ := launch(other)
			_, err := pool.Exec(ctx, `INSERT INTO session_managed_credential(session_id,account_id,engine,auth_session_id,runtime_token_id) VALUES($1,'fixture-owner','codex','fixture-sid',$2)`, sid, runtimeID)
			if err != nil {
				t.Fatal(err)
			}
			replace := func() {
				r, _, e := db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour)
				if e != nil {
					t.Error(e)
					return
				}
				row, e := db.ValidateRuntimeToken(ctx, pool, r)
				if e != nil {
					t.Error(e)
					return
				}
				if _, e = pool.Exec(ctx, `UPDATE session_managed_credential SET runtime_token_id=$2 WHERE session_id=$1`, sid, row.ID); e != nil {
					t.Error(e)
				}
			}
			stop := func() {
				_, e := pool.Exec(ctx, `UPDATE sessions SET ended_at=now() WHERE id=$1`, sid)
				if e != nil {
					t.Error(e)
				}
			}
			calls := 0
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request managedAccessRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.AccountID != "fixture-owner" || request.Engine != "codex" || request.SessionID != "fixture-sid" || request.TokenID != "" {
					t.Error("caller controlled Core principal")
				}
				if scenario == "stop-during-provider" {
					stop()
				}
				if scenario == "replace-during-provider" {
					replace()
				}
				if scenario == "provider-failure" {
					w.WriteHeader(503)
					_, _ = w.Write([]byte("fixture-access-private"))
					return
				}
				_ = json.NewEncoder(w).Encode(managedAccessFixture())
			}))
			defer core.Close()
			api.SetCoreCredentials(core.URL, "fixture-key")
			body := `{}`
			switch scenario {
			case "messaging-token":
				token = messaging
			case "foreign-session":
				token = otherToken
			case "replaced-pod":
				replace()
			case "daemon":
				token = spawnTokenDaemonTok
			case "stopped":
				stop()
			case "caller-account":
				body = `{"account_id":"other"}`
			}
			request := httptest.NewRequest(http.MethodPost, "/api/session/credentials/access", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+token)
			recorder := httptest.NewRecorder()
			api.HandleManagedCredentialAccess(recorder, request)
			if scenario == "current" {
				if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "fixture-access-private") {
					t.Fatalf("current session denied: %d", recorder.Code)
				}
			} else if recorder.Code == 200 || strings.Contains(recorder.Body.String(), "fixture-access-private") {
				t.Fatal("unbound runtime obtained credential")
			}
			if scenario == "messaging-token" && recorder.Code != http.StatusForbidden {
				t.Fatalf("messaging token: got %d, want 403", recorder.Code)
			}
			if scenario != "current" && scenario != "stop-during-provider" && scenario != "replace-during-provider" && scenario != "provider-failure" && calls != 0 {
				t.Fatal("unbound caller reached Core")
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("secret response may be cached")
			}
		})
	}
}

func TestRawCredentialSignalsManagedWithoutFallback(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"code":"not_managed"}`))
	}))
	defer core.Close()
	raw, found, err := fetchCoreCredential(context.Background(), nil, core.URL, "fixture-key", "owner", "codex", coreProof{SessionID: "proof"})
	if !errors.Is(err, errCoreManagedCredential) || found || raw != nil {
		t.Fatal("managed credential fell back to operator")
	}
}

func TestManagedCredentialLaunchNeverShipsRefreshOrOperatorAuth(t *testing.T) {
	for _, scenario := range []string{"managed", "unavailable", "caller-credential", "wrong-owner"} {
		t.Run(scenario, func(t *testing.T) {
			_, _, dc, pool := spawnTokenFixture(t)
			ctx := context.Background()
			sid := "00000000-0000-4000-8000-000000000003"
			_, err := pool.Exec(ctx, `INSERT INTO sessions(id,daemon_id,status,project_path,repo,engine,runtime,spawning_account_id) VALUES($1,$2,'starting','/repos/project','project','codex','cluster','fixture-owner')`, sid, dc.ID)
			if err != nil {
				t.Fatal(err)
			}
			token, err := db.MintSessionToken(ctx, pool, sid, []string{"message"}, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			fake := &fakeK8s{}
			jm := newTestJobManager(t, fake)
			jm.historyPool = pool
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/internal/credentials/fetch":
					w.WriteHeader(409)
					_, _ = w.Write([]byte(`{"code":"not_managed"}`))
				case "/internal/credentials/managed/access":
					if scenario == "unavailable" {
						w.WriteHeader(503)
						return
					}
					_ = json.NewEncoder(w).Encode(managedAccessFixture())
				default:
					w.WriteHeader(404)
				}
			}))
			defer core.Close()
			jm.CoreURL = core.URL
			jm.CoreInternalKey = "fixture-key"
			spec := SessionJobSpec{SessionID: sid, Repo: "project", Engine: "codex", NoRepo: true, SpawningAccountID: "fixture-owner", AuthSessionID: testSID, PluginsResolved: true, ExtraEnv: map[string]string{sessionTokenEnv: token}}
			if scenario == "caller-credential" {
				spec.ExtraEnv["CODEX_AUTH_JSON"] = "fixture-raw-refresh"
			}
			if scenario == "wrong-owner" {
				spec.SpawningAccountID = "other-owner"
			}
			err = jm.CreateSessionJob(spec)
			if scenario != "managed" {
				if err == nil || len(fake.created) != 0 || len(fake.createdSecrets) != 0 {
					t.Fatal("unsafe managed launch created a job/secret")
				}
				return
			}
			if err != nil || len(fake.created) != 1 {
				t.Fatalf("managed launch failed: %v", err)
			}
			raw, _ := json.Marshal(fake.created)
			body := string(raw)
			if !strings.Contains(body, managedCredentialEnv) || strings.Contains(body, "CODEX_AUTH_JSON") || strings.Contains(body, "fixture-access-private") {
				t.Fatal("managed launch exposed or inherited native credential")
			}
			raw, _ = json.Marshal(fake.createdSecrets)
			if strings.Contains(string(raw), "fixture-access-private") || strings.Contains(string(raw), "refresh_token") {
				t.Fatal("managed launch shipped refresh/access credential")
			}
			var bound string
			if err = pool.QueryRow(ctx, `SELECT account_id FROM session_managed_credential WHERE session_id=$1`, sid).Scan(&bound); err != nil || bound != "fixture-owner" {
				t.Fatal("launch did not retain exact owner proof")
			}
			var runtimeBound bool
			if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_managed_credential m JOIN session_runtime r ON r.session_id=m.session_id AND r.runtime_token_id=m.runtime_token_id WHERE m.session_id=$1)`, sid).Scan(&runtimeBound); err != nil || !runtimeBound {
				t.Fatal("launch bound managed access to a token other than the pod's runtime token")
			}
		})
	}
}

func TestRawCredentialFailuresNeverSelectOperator(t *testing.T) {
	for _, scenario := range []string{"core-offline", "revoked-principal", "missing-for-live-principal", "malformed", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/internal/credentials/list" {
					if scenario == "missing-for-live-principal" {
						_, _ = w.Write([]byte(`{"engines":[]}`))
						return
					}
					w.WriteHeader(404)
					return
				}
				switch scenario {
				case "core-offline":
					w.WriteHeader(503)
					_, _ = w.Write([]byte("fixture-private"))
				case "malformed":
					_, _ = w.Write([]byte(`{"plaintext_base64":"!fixture-private"}`))
				case "redirect":
					http.Redirect(w, r, "https://example.invalid/fixture", http.StatusTemporaryRedirect)
				default:
					w.WriteHeader(404)
				}
			}))
			defer core.Close()
			_, found, err := fetchCoreCredential(context.Background(), nil, core.URL, "fixture-key", "owner", "codex", coreProof{SessionID: "proof"})
			if found || (err == nil) != (scenario == "missing-for-live-principal") {
				t.Fatal("ambiguous credential failure permitted operator fallback")
			}
			if err != nil && strings.Contains(err.Error(), "fixture-private") {
				t.Fatal("provider detail escaped")
			}
		})
	}
}
