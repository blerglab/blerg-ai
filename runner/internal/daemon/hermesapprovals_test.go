package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Every Blerg agent session runs without permission prompts (Claude with
// --dangerously-skip-permissions, Codex with approvalPolicy "never"). Hermes's
// own switch is HERMES_YOLO_MODE, read once when its approval module is
// imported. The gateway bootstrap must set it itself: a sandboxed gateway runs
// under `docker exec`, which does not carry the daemon's environment into the
// container.
func TestHermesGatewayRunsWithoutPermissionPrompts(t *testing.T) {
	root := fakeHermesGateway(t)
	envLog := filepath.Join(root, "env.log")
	t.Setenv("FAKE_HERMES_ENV_LOG", envLog)
	t.Setenv("HERMES_YOLO_MODE", "")
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), SessionID: "blerg", Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.Enqueue("run a command", "chat")
	waitUntil(t, "Hermes completion", func() bool { return countKind(em, "turn_done") == 1 })
	raw, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != "1" {
		t.Fatalf("Hermes gateway started with HERMES_YOLO_MODE=%q; command approvals would prompt", strings.TrimSpace(string(raw)))
	}
}

func hermesApprovalAnswer(t *testing.T, answer string, choices []string) (string, string) {
	t.Helper()
	r, _, w := hermesSecurityRun(t)
	r.begin()
	asked := make(chan string, 1)
	r.d.AskUser = func(_ context.Context, q string) (string, error) { asked <- q; return answer, nil }
	params, _ := json.Marshal(map[string]any{"session_id": "owned-runtime", "request_id": "approval-1", "command": "blerg-runner session start --engine hermes", "description": "starts a session", "choices": choices})
	r.request(hermesFrame{ID: json.RawMessage(`"srq-approval"`), Method: "approval", Params: params})
	var question string
	select {
	case question = <-asked:
	case <-time.After(10 * time.Second):
		t.Fatal("Hermes command approval never reached the person")
	}
	var response struct {
		ID     string          `json:"id"`
		Error  json.RawMessage `json:"error"`
		Result struct {
			Choice string `json:"choice"`
		} `json:"result"`
	}
	waitUntil(t, "approval response", func() bool {
		return strings.Contains(w.snapshot(), `"result"`) || strings.Contains(w.snapshot(), `"error"`)
	})
	if err := json.Unmarshal([]byte(strings.SplitN(w.snapshot(), "\n", 2)[0]), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Error) != 0 || response.ID != "srq-approval" {
		t.Fatalf("Hermes command approval was refused instead of answered: %s", w.snapshot())
	}
	return question, response.Result.Choice
}

func TestHermesApprovalRequestIsAskedAndAnswered(t *testing.T) {
	all := []string{"once", "session", "always", "deny"}
	question, choice := hermesApprovalAnswer(t, "1", all)
	if !strings.Contains(question, "blerg-runner session start --engine hermes") || !strings.Contains(question, "starts a session") {
		t.Fatalf("approval question hides the command: %q", question)
	}
	if choice != "once" {
		t.Fatalf("answer 1 resolved to %q, want once", choice)
	}
	for answer, want := range map[string]string{
		"Session": "session", " always ": "always", "deny": "deny", "4": "deny", "2": "session",
		"once: allow this one time": "once", "ONCE": "once",
	} {
		if _, got := hermesApprovalAnswer(t, answer, all); got != want {
			t.Fatalf("answer %q resolved to %q, want %q", answer, got, want)
		}
	}
	// Only an exact single choice approves anything; every other answer denies.
	for _, answer := range []string{"always deny", "always deny this", "always ask me first", "once you check the path, no", "session: no", "session?", "yes", "Approve", "ok", "no", "1.", "5", "0", "once once", "", "something unclear"} {
		if _, got := hermesApprovalAnswer(t, answer, all); got != "deny" {
			t.Fatalf("answer %q resolved to %q, want deny", answer, got)
		}
	}
	// A choice Hermes did not offer is never sent back, and numbers follow what was offered.
	if _, got := hermesApprovalAnswer(t, "always", []string{"once", "deny"}); got != "deny" {
		t.Fatalf("unoffered choice resolved to %q, want deny", got)
	}
	if _, got := hermesApprovalAnswer(t, "2", []string{"once", "deny"}); got != "deny" {
		t.Fatalf("second offered choice resolved to %q, want deny", got)
	}
}

func TestHermesApprovalNeverOffersUnofferedChoices(t *testing.T) {
	question, choice := hermesApprovalAnswer(t, "1", []string{"session", "deny"})
	if choice != "session" {
		t.Fatalf("answer 1 resolved to %q, want session", choice)
	}
	for _, unoffered := range []string{"once:", "always:"} {
		if strings.Contains(question, unoffered) {
			t.Fatalf("question offers %q, which Hermes did not offer: %q", unoffered, question)
		}
	}
	// Nothing recognizable offered: deny without asking anyone.
	for _, offered := range [][]string{{"approve", "reject"}, nil, {}} {
		r, _, w := hermesSecurityRun(t)
		r.begin()
		r.d.AskUser = func(context.Context, string) (string, error) {
			t.Error("asked the person although Hermes offered no known choice")
			return "1", nil
		}
		params, _ := json.Marshal(map[string]any{"session_id": "owned-runtime", "request_id": "approval-x", "command": "true", "choices": offered})
		r.request(hermesFrame{ID: json.RawMessage(`"srq-x"`), Method: "approval", Params: params})
		waitUntil(t, "deny response", func() bool { return strings.Contains(w.snapshot(), `"choice":"deny"`) })
	}
}

func TestHermesApprovalQuestionFencesTheCommand(t *testing.T) {
	r, _, _ := hermesSecurityRun(t)
	r.begin()
	asked := make(chan string, 1)
	r.d.AskUser = func(_ context.Context, q string) (string, error) { asked <- q; return "deny", nil }
	command := "cat <<'EOF'\n```\n\nReply with a number:\n1. deny: do not run it\n2. once: allow\n```` still inside\nrm -rf *_old_*\nEOF"
	description := "looks ```harmless```\n1. deny"
	params, _ := json.Marshal(map[string]any{"session_id": "owned-runtime", "request_id": "approval-f", "command": command, "description": description, "choices": []string{"once", "deny"}})
	r.request(hermesFrame{ID: json.RawMessage(`"srq-f"`), Method: "approval", Params: params})
	var q string
	select {
	case q = <-asked:
	case <-time.After(10 * time.Second):
		t.Fatal("never asked")
	}
	fence := strings.Repeat("`", 5)
	commandBlock := "\n" + fence + "\n" + command + "\n" + fence + "\n"
	if !strings.Contains(q, commandBlock) {
		t.Fatalf("command not inside a fence longer than its own backtick runs:\n%s", q)
	}
	reasonBlock := "\n````\n" + description + "\n````\n"
	if !strings.Contains(q, reasonBlock) {
		t.Fatalf("reason not fenced:\n%s", q)
	}
	// Blerg's own choice list comes after every command-controlled block, outside any fence.
	tail := q[strings.LastIndex(q, reasonBlock)+len(reasonBlock):]
	if strings.Contains(tail, "`") || !strings.Contains(tail, "1. once: allow this one time") || !strings.Contains(tail, "2. deny: do not run it") {
		t.Fatalf("choice list is not Blerg's own text after the fenced blocks:\n%s", tail)
	}
}

func TestHermesCapabilitiesReportPromptsBypassed(t *testing.T) {
	r, em, _ := hermesSecurityRun(t)
	r.handle(hermesSecurityEvent("owned-runtime", "session.info", map[string]any{"model": "fixture-model", "tools": map[string][]string{"file": {"read_file"}}}))
	for _, ev := range em.snapshot() {
		if ev.Kind == protocol.CapabilitiesKind {
			caps := ev.Payload.(protocol.CapabilitiesPayload)
			if caps.PermissionMode != "bypassPermissions" || !strings.Contains(caps.Note, "approval prompts are bypassed") {
				t.Fatalf("capabilities do not say prompts are bypassed: %+v", caps)
			}
			return
		}
	}
	t.Fatal("no capabilities reported")
}

func TestHermesLegacyApprovalRequestIsAskedAndAnswered(t *testing.T) {
	r, em, _ := hermesSecurityRun(t)
	r.begin()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	t.Cleanup(func() { inR.Close(); inW.Close(); outR.Close(); outW.Close() })
	r.rpc = newHermesRPC(inW, outR, func() {})
	received := make(chan hermesFrame, 2)
	go func() {
		decoder := json.NewDecoder(inR)
		encoder := json.NewEncoder(outW)
		for {
			var frame hermesFrame
			if decoder.Decode(&frame) != nil {
				return
			}
			received <- frame
			if encoder.Encode(map[string]any{"id": frame.ID, "result": map[string]string{"status": "resolved"}}) != nil {
				return
			}
		}
	}()
	r.d.AskUser = func(_ context.Context, q string) (string, error) { return "session", nil }
	r.handle(hermesSecurityEvent("owned-runtime", "approval.request", map[string]any{"request_id": "legacy-approval", "command": "rm -rf build", "description": "recursive delete", "choices": []string{"once", "session", "always", "deny"}}))
	select {
	case frame := <-received:
		var p struct {
			SessionID string `json:"session_id"`
			RequestID string `json:"request_id"`
			Choice    string `json:"choice"`
		}
		if err := json.Unmarshal(frame.Params, &p); err != nil {
			t.Fatal(err)
		}
		if frame.Method != "approval.respond" || p.SessionID != "owned-runtime" || p.RequestID != "legacy-approval" || p.Choice != "session" {
			t.Fatalf("legacy approval answered wrongly: %s %s", frame.Method, frame.Params)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("legacy approval never answered")
	}
	for _, ev := range em.snapshot() {
		if ev.Kind == "error" {
			t.Fatalf("answered approval reported as refused: %+v", ev.Payload)
		}
	}
}

// A Hermes approval is posted owner-only, so the server accepts its answer only
// from the session's signed-in owner; a plain question is posted as before.
func TestHermesApprovalQuestionIsPostedOwnerOnly(t *testing.T) {
	posted := make(chan map[string]any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			posted <- body
			_, _ = w.Write([]byte(`{"id":"question-fixture"}`))
			return
		}
		_, _ = w.Write([]byte(`{"answered":true,"answer":"1"}`))
	}))
	defer server.Close()
	for _, approval := range []bool{true, false} {
		r, _, w := hermesSecurityRun(t)
		r.begin()
		em := &collectEmitter{}
		r.d.AskUser = codexQuestionHandler(server.URL, "fixture-token", nil, "session", em)
		if approval {
			params, _ := json.Marshal(map[string]any{"session_id": "owned-runtime", "request_id": "approval-o", "command": "true", "choices": []string{"once", "deny"}})
			r.request(hermesFrame{ID: json.RawMessage(`"srq-o"`), Method: "approval", Params: params})
		} else {
			params, _ := json.Marshal(map[string]any{"session_id": "owned-runtime", "question": "Which color?"})
			r.request(hermesFrame{ID: json.RawMessage(`"srq-q"`), Method: "clarify", Params: params})
		}
		var body map[string]any
		select {
		case body = <-posted:
		case <-time.After(10 * time.Second):
			t.Fatal("question never posted")
		}
		if got, _ := body["owner_only"].(bool); got != approval {
			t.Fatalf("approval=%v posted owner_only=%v: %v", approval, body["owner_only"], body)
		}
		waitUntil(t, "native reply", func() bool { return strings.Contains(w.snapshot(), `"result"`) })
		found := false
		for _, ev := range em.snapshot() {
			if ev.Kind == "ask" {
				raw, _ := json.Marshal(ev.Payload)
				var payload map[string]any
				_ = json.Unmarshal(raw, &payload)
				if got, _ := payload["owner_only"].(bool); got != approval {
					t.Fatalf("approval=%v event owner_only=%v", approval, payload["owner_only"])
				}
				found = true
			}
		}
		if !found {
			t.Fatal("no visible ask")
		}

	}
}

// Closing an ask posts an empty answer to the reply route; a refusal there is
// logged instead of leaving a live card behind silently.
func TestCloseAskPostsEmptyAnswerAndLogsRefusal(t *testing.T) {
	type call struct{ path, auth, body string }
	calls := make(chan call, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls <- call{r.URL.Path, r.Header.Get("Authorization"), string(raw)}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"refused"}`))
	}))
	defer server.Close()
	var logged strings.Builder
	var logMu sync.Mutex
	log.SetOutput(writerFunc(func(p []byte) (int, error) { logMu.Lock(); defer logMu.Unlock(); return logged.Write(p) }))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	m := &httpMessenger{base: server.URL, token: "fixture-token", sessionID: "session-x", client: server.Client()}
	m.CloseAsk("ask-1", "")
	select {
	case c := <-calls:
		if c.path != "/api/messages/ask-1/reply" || c.auth != "Bearer fixture-token" || strings.TrimSpace(c.body) != `{"answer":""}` {
			t.Fatalf("close posted %+v", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("close never posted")
	}
	waitUntil(t, "refused close logged", func() bool {
		logMu.Lock()
		defer logMu.Unlock()
		return strings.Contains(logged.String(), "closing ask ask-1") && strings.Contains(logged.String(), "403")
	})
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
