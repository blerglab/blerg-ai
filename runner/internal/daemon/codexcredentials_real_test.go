package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercises the installed native protocol against loopback only, with synthetic
// access JWTs and an empty home. No host login or upstream inference is used.
func TestCodexManagedCredentialRealCLI(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(fmt.Sprint(restricted), func(t *testing.T) { testCodexManagedCredentialRealCLI(t, restricted) })
	}
}
func testCodexManagedCredentialRealCLI(t *testing.T, restricted bool) {
	if os.Getenv("BLERG_REAL_CODEX") != "1" {
		t.Skip("set BLERG_REAL_CODEX=1 for installed native external-auth protocol")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("Codex unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if err = codexRestrictedVersionCheck(ctx, binary); err != nil {
		t.Fatal(err)
	}
	makeToken := func(marker string) string {
		claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": "fixture-user", "email": "fixture@example.test", "jti": marker, "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "fixture-provider-account", "chatgpt_plan_type": "pro", "chatgpt_user_id": "fixture-user"}})
		return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".fixture"
	}
	first, next := makeToken("first"), makeToken("next")
	var firstSeen, nextSeen, refreshes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/accounts/check") {
			_ = json.NewEncoder(w).Encode(map[string]any{"accounts": []map[string]string{{"id": "fixture-provider-account", "workspace_backend_origin": "https://chatgpt.com", "account_routing_override": "NO_CONSTRAINT"}}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/config/bundle") {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/settings/user") {
			_, _ = io.WriteString(w, `{"commit_attribution_enabled":false}`)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			w.WriteHeader(404)
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 4<<20))
		switch r.Header.Get("Authorization") {
		case "Bearer " + first:
			firstSeen.Add(1)
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":{"message":"expired fixture token","type":"invalid_token"}}`)
		case "Bearer " + next:
			nextSeen.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture-response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0,\"total_tokens\":1}}}\n\n")
		default:
			t.Error("native inference omitted or changed access binding")
			w.WriteHeader(403)
		}
	}))
	defer upstream.Close()
	home, work := t.TempDir(), t.TempDir()
	config := "model_provider=\"probe\"\nchatgpt_base_url=" + strconvQuote(upstream.URL) + "\n[model_providers.probe]\nname=\"Loopback fixture\"\nbase_url=" + strconvQuote(upstream.URL+"/v1") + "\nwire_api=\"responses\"\nrequires_openai_auth=true\nsupports_websockets=false\n"
	if err = os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: work, Model: "gpt-5-codex", Emitter: em, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + home, "HTTPS_PROXY=http://127.0.0.1:9", "HTTP_PROXY=http://127.0.0.1:9", "ALL_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost"}, CredentialSource: func(_ context.Context, force bool, previous string) (EngineCredentialAccess, error) {
		access := fixtureEngineAccess()
		access.PlanType = "pro"
		access.AccessToken = first
		if force {
			refreshes.Add(1)
			if previous != "fixture-generation" {
				t.Error("native refresh lost generation")
			}
			access.Generation = "fixture-next-generation"
			access.AccessToken = next
		}
		return access, nil
	}})
	privateHome := home
	if restricted {
		runtime, err := prepareCodexRestrictedRuntime(ctx, work, home, nil, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		policy := codexRestrictedConfig("gpt-5-codex", "probe", map[string]any{"probe": map[string]any{"name": "Loopback fixture", "base_url": upstream.URL + "/v1", "wire_api": "responses", "requires_openai_auth": true, "supports_websockets": false}})
		policy["chatgpt_base_url"] = upstream.URL
		if err := writeCodexRestrictedConfig(runtime.Home, policy); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(runtime.Home, "auth.json")); !os.IsNotExist(err) {
			t.Fatal("managed restricted home inherited a file login")
		}
		d.RestrictedRuntime, d.CodexHome = runtime, runtime.Home
		privateHome = runtime.Home
	}
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("native runtime did not stop")
		}
	}()
	d.Enqueue("Reply with no tools.", "chat")
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for countKind(em, "turn_done") == 0 {
		select {
		case <-deadline.C:
			t.Fatalf("native access refresh timed out; event kinds=%v first=%d refreshed=%d next=%d", em.kinds(), firstSeen.Load(), refreshes.Load(), nextSeen.Load())
		case <-time.After(25 * time.Millisecond):
		}
	}
	if firstSeen.Load() == 0 || refreshes.Load() != 1 || nextSeen.Load() == 0 {
		for _, event := range em.snapshot() {
			if event.Kind == "error" || event.Kind == "turn_done" {
				t.Logf("native fixture %s: %+v", event.Kind, event.Payload)
			}
		}
		t.Fatalf("native refresh path incomplete first=%d refreshed=%d next=%d", firstSeen.Load(), refreshes.Load(), nextSeen.Load())
	}
	for _, root := range []string{home, privateHome} {
		if raw, readErr := os.ReadFile(filepath.Join(root, "auth.json")); readErr == nil && (strings.Contains(string(raw), first) || strings.Contains(string(raw), next) || strings.Contains(string(raw), "refresh_token")) {
			t.Fatal("external native authentication persisted credential state")
		}
	}
	t.Log("installed native external login, 401 refresh, retry and nonpersistent access passed")
}
func strconvQuote(value string) string { raw, _ := json.Marshal(value); return string(raw) }
