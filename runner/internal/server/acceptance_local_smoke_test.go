//go:build linux

package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestAcceptanceHostSmoke(t *testing.T)    { acceptanceLocalSmoke(t, false, false) }
func TestAcceptanceSandboxSmoke(t *testing.T) { acceptanceLocalSmoke(t, true, false) }

func TestAcceptanceRealEngineSmoke(t *testing.T) {
	if os.Getenv("BLERG_RUNTIME_SMOKE_REAL") != "1" {
		t.Skip("BLERG_RUNTIME_SMOKE_REAL=1 enables paid Claude host smoke")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Fatal("real smoke requires ANTHROPIC_API_KEY")
	}
	acceptanceLocalSmoke(t, false, true)
}

func acceptanceLocalSmoke(t *testing.T, sandbox, useRealEngine bool) {
	t.Helper()
	if os.Getenv("BLERG_HOST_HELPER_TEST_BINARY") == "" {
		t.Skip("build daemon and set BLERG_HOST_HELPER_TEST_BINARY; use go run ./cmd/runtime-smoke")
	}
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	image := ""
	if sandbox {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
			t.Skip("local sandbox: Docker daemon unavailable")
		}
		image = os.Getenv("BLERG_RUNNER_SANDBOX_IMAGE")
		if image == "" {
			image = "blerg-runner-sandbox:latest"
		}
		if err := exec.CommandContext(ctx, "docker", "image", "inspect", image).Run(); err != nil {
			t.Skip("local sandbox: base image unavailable (no automatic pull)")
		}
		image = p6SandboxImage(t, image)
	}
	var engines []string
	if useRealEngine {
		path, err := exec.LookPath("claude")
		if err != nil {
			t.Fatal("real smoke requires Claude CLI")
		}
		engines = append(engines, path)
	}
	s := newMembershipStack(t, image, engines...)
	one := s.repository("project", "one")
	two := s.repository("project", "two")
	runtime := "daemon"
	if sandbox {
		runtime = "docker"
	}
	runtimeName := "host"
	if sandbox {
		runtimeName = "sandbox"
	}
	raw := s.request("POST", "/api/workspace/preview", map[string]any{"runtime": runtimeName, "selection": s.selection(one, two)}, 200)
	var preview struct {
		Selection protocol.WorkspaceSelectionRequest
	}
	if err := json.Unmarshal(raw, &preview); err != nil {
		t.Fatal(err)
	}
	raw = s.request("POST", "/api/sessions", map[string]any{"selection": preview.Selection, "runtime": runtime, "kind": "agent", "engine": "claude", "title": "Runtime smoke"}, 202)
	var launch struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &launch); err != nil || launch.SessionID == "" {
		t.Fatal("start did not return a session ID")
	}
	sid := launch.SessionID
	// Always stop a launched session, even if startup/transcript assertions fail.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := s.f.api.stopSessionWithDelivery(ctx, sid, systemEnd(db.EndReasonStoppedByUser), false); err != nil {
			t.Errorf("cleanup session: %v", err)
		}
	}()
	p6EnabledWait(t, 90*time.Second, "public running lifecycle", func() bool {
		var status struct {
			Lifecycle string `json:"lifecycle"`
		}
		raw := s.request("GET", "/api/runner/sessions/"+sid, nil, 200)
		if json.Unmarshal(raw, &status) != nil {
			t.Fatal("invalid status response")
		}
		if status.Lifecycle == "error" || status.Lifecycle == "ended" {
			t.Fatalf("startup ended with lifecycle %s", status.Lifecycle)
		}
		return status.Lifecycle == "running"
	})
	if sandbox {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		raw, err := exec.CommandContext(ctx, "docker", "ps", "-q", "--filter", "label=blerg.runner.workspace-session="+sid).Output()
		if err != nil || len(strings.Fields(string(raw))) != 1 {
			t.Fatal("sandbox session must have exactly one running container with its session label")
		}
	}
	clientID := newUUID()
	prompt := "commit both members"
	if useRealEngine {
		prompt = "Reply with exactly BLERG_SMOKE_OK. Do not use tools or edit files."
	}
	s.request("POST", "/api/runner/sessions/"+sid+"/message", map[string]string{"text": prompt, "client_id": clientID}, 202)
	want := "Committed both members."
	if useRealEngine {
		want = "BLERG_SMOKE_OK"
	}
	p6EnabledWait(t, 90*time.Second, "HTTP transcript: acknowledged message, assistant reply and successful turn", func() bool {
		raw := s.request("GET", "/api/runner/sessions/"+sid+"/events?limit=400", nil, 200)
		return smokeReply(raw, clientID, want)
	})
	s.request("DELETE", "/api/sessions/"+sid, nil, 204)
	p6EnabledWait(t, 30*time.Second, "public ended lifecycle", func() bool {
		var status struct {
			Lifecycle string `json:"lifecycle"`
		}
		raw := s.request("GET", "/api/runner/sessions/"+sid, nil, 200)
		return json.Unmarshal(raw, &status) == nil && status.Lifecycle == "ended"
	})
	p6EnabledWait(t, 30*time.Second, "runtime final stop receipt", func() bool {
		var n int
		return s.f.pool.QueryRow(s.ctx, `SELECT count(*) FROM local_workspace_authority WHERE session_id=$1 AND final_digest IS NOT NULL`, sid).Scan(&n) == nil && n == 1
	})
}

// Require a successful turn after this message and a completed assistant text;
// an HTTP 202, replayed startup result or an unrelated turn cannot pass.
func smokeReply(raw []byte, clientID, want string) bool {
	var transcript struct {
		Events []struct {
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		} `json:"events"`
	}
	if json.Unmarshal(raw, &transcript) != nil {
		return false
	}
	seen, reply := false, false
	for _, ev := range transcript.Events {
		if ev.Kind == "user_message" {
			var p struct {
				ClientID string `json:"client_id"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil && p.ClientID == clientID {
				seen = true
				reply = false
			}
		}
		if !seen {
			continue
		}
		if ev.Kind == "assistant_text" {
			var p struct {
				Text string `json:"text"`
				Done bool   `json:"done"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil && p.Done && strings.Contains(p.Text, want) {
				reply = true
			}
		}
		if ev.Kind == "turn_done" {
			var p struct {
				StopReason string `json:"stop_reason"`
			}
			return reply && json.Unmarshal(ev.Payload, &p) == nil && p.StopReason == "end_turn"
		}
	}
	return false
}

func TestAcceptanceSmokeReply(t *testing.T) {
	good := `{"events":[{"kind":"user_message","payload":{"client_id":"message"}},{"kind":"assistant_text","payload":{"text":"reply","done":true}},{"kind":"turn_done","payload":{"stop_reason":"end_turn"}}]}`
	if !smokeReply([]byte(good), "message", "reply") {
		t.Fatal("valid turn refused")
	}
	for _, bad := range []string{strings.ReplaceAll(good, "message", "other"), strings.ReplaceAll(good, "true", "false"), strings.ReplaceAll(good, "end_turn", "error"), strings.ReplaceAll(good, "reply", "wrong"), `{}`, `invalid`} {
		if smokeReply([]byte(bad), "message", "reply") {
			t.Fatalf("invalid turn passed: %s", bad)
		}
	}
}
