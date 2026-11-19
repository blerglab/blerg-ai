package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

// A separate Core process owns the router, real identity signing keys, login
// chains, migrations and SQL. Tokens travel only through a private fixture file.
func TestLiveDataApprovalProcessHelper(t *testing.T) {
	if os.Getenv("BLERG_LIVE_APPROVAL_HELPER") != "1" {
		t.Skip("process fixture only")
	}
	e := newLiveApprovalEnv(t)
	config := map[string]string{"url": e.srv.URL, "owner": liveToken(t, e, e.acctA, e.sidA, "blerg-runner"), "other": liveToken(t, e, e.acctB, e.sidB, "blerg-runner"), "core": liveToken(t, e, e.acctA, e.sidA, "blerg-core"), "account": e.acctA, "sid": e.sidA, "connection": e.connA}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal("fixture encoding")
	}
	if os.WriteFile(os.Getenv("BLERG_LIVE_APPROVAL_FIXTURE"), raw, 0600) != nil {
		t.Fatal("fixture write")
	}
	println("live-approval-ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
}
func TestLiveDataApprovalRealProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "private.json")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveDataApprovalProcessHelper$")
	cmd.Env = append(os.Environ(), "BLERG_LIVE_APPROVAL_HELPER=1", "BLERG_LIVE_APPROVAL_FIXTURE="+path)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Start() != nil {
		t.Fatal("start Core fixture")
	}
	var logs bytes.Buffer
	drained := make(chan struct{})
	startedDrain := false
	defer func() {
		_ = input.Close()
		if cmd.Wait() != nil {
			t.Error("Core fixture failed")
		}
		if startedDrain {
			<-drained
			if logs.Len() == 0 {
				t.Error("access log fixture did not record failures")
			}
			for _, secret := range []string{"canary-forged-human", "process-credential-canary", cfgTokenForLogCheck(path, "owner"), cfgTokenForLogCheck(path, "core"), testInternalKey} {
				if secret != "" && strings.Contains(logs.String(), secret) {
					t.Error("credential leaked to Core access logs")
				}
			}
		}
	}()
	scanner := bufio.NewScanner(output)
	ready := false
	for scanner.Scan() {
		if scanner.Text() == "live-approval-ready" {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatal("Core fixture readiness")
	}
	startedDrain = true
	go func() { _, _ = io.Copy(&logs, output); close(drained) }()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read private fixture")
	}
	var cfg map[string]string
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("decode fixture")
	}
	e := &mcpEnv{acctA: cfg["account"], sidA: cfg["sid"], connA: cfg["connection"]}
	// httptest.Server supplies only URL/Client here; all handlers run in the child.
	e.srv = &httptest.Server{URL: cfg["url"]}
	secretConfig := liveConfig(e, "00000000-0000-4000-8000-000000000713", 1)
	secretConfig.Arguments = json.RawMessage(`{"symbol":"Bearer process-credential-canary"}`)
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/proposals", testInternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "configuration": secretConfig, "ask_id": liveAskID(secretConfig)}); code != 400 {
		t.Fatal("process secret refusal", code)
	}
	p := proposeLive(t, e, liveConfig(e, "00000000-0000-4000-8000-000000000714", 1))
	decision := ld.Decision{AccountID: e.acctA, AccessToken: cfg["owner"], BindingID: p.Configuration.Scope.Binding, Revision: 1, Digest: p.Digest, AskID: p.AskID, Approve: true}
	for _, tc := range []struct{ name, token string }{{"forged", "canary-forged-human"}, {"wrong person", cfg["other"]}} {
		t.Run(tc.name, func(t *testing.T) {
			bad := decision
			bad.AccessToken = tc.token
			if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, bad); code != 404 {
				t.Fatalf("refusal %d", code)
			}
			policy := ld.PolicyWrite{AccountID: e.acctA, AccessToken: tc.token, BindingID: decision.BindingID, Revision: 1, Digest: p.Digest, Mode: "allow"}
			if code, _ := liveCall(t, e, "POST", "/internal/live-data/policy", testInternalKey, policy); code != 403 {
				t.Fatalf("policy refusal %d", code)
			}
		})
	}
	policy := ld.PolicyWrite{AccountID: e.acctA, AccessToken: cfg["owner"], BindingID: decision.BindingID, Revision: 1, Digest: p.Digest, Mode: "allow"}
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data/policy", cfg["core"], policy); code != 200 {
		t.Fatalf("policy %d", code)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 409 {
		t.Fatalf("unsafe runtime %d", code)
	}
	if code, _ := liveCall(t, e, "PUT", "/api/settings/live-data", cfg["core"], map[string]any{"unsafe_host_opt_in": true, "acknowledgement": ld.UnsafeHostWarning}); code != 200 {
		t.Fatalf("consent %d", code)
	}
	if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 200 {
		t.Fatalf("approval %d", code)
	}
	t.Run("replayed", func(t *testing.T) {
		bad := decision
		bad.AskID = "00000000-0000-4000-8000-000000000716"
		if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, bad); code != 409 {
			t.Fatalf("replay %d", code)
		}
	})
	_ = proposeLive(t, e, liveConfig(e, decision.BindingID, 2))
	t.Run("stale revision", func(t *testing.T) {
		if code, _ := liveCall(t, e, "POST", "/internal/live-data/approve", testInternalKey, decision); code != 409 {
			t.Fatalf("stale %d", code)
		}
	})
}

func cfgTokenForLogCheck(path, key string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg map[string]string
	if json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	return cfg[key]
}
