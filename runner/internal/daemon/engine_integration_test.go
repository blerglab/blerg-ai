package daemon

// TestEngineIntegration drives one real, minimal Agent-kind turn against
// each registered CLI engine's REAL binary — not a fake stub — and checks
// the reply actually came back. This is exactly the gap the unit tests in
// codexdriver_test.go/hermesdriver_test.go/openclawdriver_test.go can't
// close: they prove the Go code parses a canned fixture correctly, not that
// it invokes the real CLI with the right args/cwd/env and parses what that
// CLI actually prints.
//
// Opt-in and real-infra-dependent by design (same convention as
// TEST_DATABASE_URL-gated tests elsewhere in this package): it needs each
// engine's CLI installed and already authenticated on this machine (a
// logged-in `codex`, a configured `~/.hermes`, an `openclaw` provider
// registered — see DAEMON.md's OpenClaw section for how this was built and
// last verified against a local OpenAI-compatible inference server), and it spends real
// time/quota/GPU against whatever backend that auth points at. Run
// explicitly:
//
//	BLERG_ENGINE_INTEGRATION_TEST=1 go test ./internal/daemon/ -run TestEngineIntegration -v -timeout 5m
//
// New engines added to engineRegistry are covered automatically — this
// loops the registry rather than listing engines by name, so there is
// nothing to remember to update here when engine #5 shows up.
import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// engineIntegrationModel lets each engine's test model be overridden without
// editing this file — most useful for OpenClaw, whose model id names a
// specific registered provider (e.g. "mybox/qwen3-30b") that only exists on
// whatever box you've pointed OpenClaw at.
func engineIntegrationModel(engineID string) string {
	return os.Getenv("BLERG_ENGINE_TEST_MODEL_" + strings.ToUpper(engineID))
}

func TestEngineIntegration(t *testing.T) {
	if os.Getenv("BLERG_ENGINE_INTEGRATION_TEST") == "" {
		t.Skip("BLERG_ENGINE_INTEGRATION_TEST not set; skipping real-CLI engine integration tests (see this file's doc comment)")
	}

	// OpenClaw has no CLI-native default model that's guaranteed valid (see
	// DAEMON.md) — without an explicit one it fails at model resolution
	// before ever reaching a provider. This fallback names a provider id
	// that exists only on whoever originally verified this test's own
	// machine; it's a placeholder, not a real default anyone else can use —
	// set BLERG_ENGINE_TEST_MODEL_OPENCLAW to whatever you registered via
	// configure-openclaw-endpoint.sh.
	defaultModel := map[string]string{"openclaw": "mybox/qwen3-30b"}

	for _, id := range engineOrder {
		id := id
		spec := engineRegistry[id]
		t.Run(spec.DisplayName, func(t *testing.T) {
			if _, err := exec.LookPath(spec.Binary); err != nil {
				t.Skipf("%s CLI (%q) not found on PATH", spec.DisplayName, spec.Binary)
			}

			model := engineIntegrationModel(id)
			if model == "" {
				model = defaultModel[id]
			}

			reposRoot := t.TempDir()
			workDir := filepath.Join(reposRoot, "proj")
			if err := os.MkdirAll(workDir, 0o755); err != nil {
				t.Fatal(err)
			}

			sender := &agentTestSender{}
			host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir()})
			// Unique per run: Hermes resolves --continue by this exact name in
			// its own on-disk SQLite session store (see hermesdriver.go) — a
			// fixed id would accumulate real, permanent conversation history
			// there across repeated test runs instead of starting clean.
			sessionID := "engine-integration-" + id + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			host.Spawn(protocol.SpawnSession{
				Type: "spawn_session", SessionID: sessionID, Repo: "proj",
				InitialPrompt: "Reply with exactly one word and nothing else: pineapple",
				Kind:          "agent", Engine: id, Model: model,
			})

			ev := waitForAgentEventKind(t, sender, "turn_done", 3*time.Minute)
			var done agent.TurnDonePayload
			if err := json.Unmarshal(ev.Payload, &done); err != nil {
				t.Fatalf("turn_done payload: %v", err)
			}

			var errMsg string
			var sawAssistantText bool
			var assistantText string
			for _, e := range sender.agentEvents() {
				switch e.Kind {
				case "error":
					var p agent.ErrorPayload
					if json.Unmarshal(e.Payload, &p) == nil {
						errMsg = p.Message
					}
				case "assistant_text":
					var p agent.AssistantTextPayload
					if json.Unmarshal(e.Payload, &p) == nil && p.Done {
						sawAssistantText = true
						assistantText = p.Text
					}
				}
			}

			if errMsg != "" {
				t.Fatalf("%s real turn failed: %s (turn_done stop reason: %s)", spec.DisplayName, errMsg, done.StopReason)
			}
			if !sawAssistantText {
				t.Fatalf("%s produced no assistant_text; turn_done stop reason: %s", spec.DisplayName, done.StopReason)
			}
			if !strings.Contains(strings.ToLower(assistantText), "pineapple") {
				t.Errorf("%s reply = %q, want it to contain \"pineapple\"", spec.DisplayName, assistantText)
			}
			t.Logf("%s replied: %q", spec.DisplayName, assistantText)
		})
	}
}

// waitForAgentEventKind is waitForKind with a caller-supplied deadline —
// real CLI turns (especially against a small self-hosted model) can take
// well over agentTestSender.waitForKind's fixed 5s.
func waitForAgentEventKind(t *testing.T, sender *agentTestSender, kind string, timeout time.Duration) protocol.AgentEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, ev := range sender.agentEvents() {
			if ev.Kind == kind {
				return ev
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no agent_event kind %q within %s; got %v", kind, timeout, sender.kinds())
	return protocol.AgentEvent{}
}
