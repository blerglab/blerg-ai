package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func claudeMCPFixture(t *testing.T) (*claudeCodeDriver, *ccProc) {
	t.Helper()
	d := newClaudeCodeDriver(t.TempDir(), "fixture", "", &collectEmitter{}, nil)
	d.steer.legacy = false
	p := &ccProc{gen: 7, writeCh: make(chan []byte, 8), done: make(chan struct{}), closeReq: make(chan struct{})}
	d.steer.proc = p
	t.Cleanup(func() { close(p.done) })
	return d, p
}

func claudeMCPRequest(t *testing.T, p *ccProc) string {
	t.Helper()
	select {
	case line := <-p.writeCh:
		var req struct {
			Type    string `json:"type"`
			ID      string `json:"request_id"`
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			t.Fatal(err)
		}
		if req.Type != "control_request" || req.Request.Subtype != "mcp_status" || req.ID == "" {
			t.Fatalf("not a read-only MCP control request: %s", line)
		}
		return req.ID
	case <-time.After(15 * time.Second):
		t.Fatal("no MCP control request")
		return ""
	}
}

func claudeMCPResponse(d *claudeCodeDriver, p *ccProc, id, payload string) {
	d.onLine(p, []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"`+id+`","response":`+payload+`}}`))
}

func TestClaudeMCPLivePendingThenConnected(t *testing.T) {
	d, p := claudeMCPFixture(t)
	d.onLine(p, []byte(`{"type":"system","subtype":"init","mcp_servers":[{"name":"jira","status":"pending","source":"project"}]}`))
	for _, status := range []string{"pending", "connected"} {
		result := make(chan mcpProbe, 1)
		errs := make(chan error, 1)
		go func() { v, e := d.MCPStatus(context.Background()); result <- v; errs <- e }()
		id := claudeMCPRequest(t, p)
		claudeMCPResponse(d, p, id, `{"mcpServers":[{"name":"jira","status":"`+status+`","source":"project","tools":[{"name":"search"},{"name":"read"}],"config":{"url":"https://invalid.example/private","headers":{"Authorization":"fixture-private"}},"error":"fixture-private"}]}`)
		select {
		case got := <-result:
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
			if !got.Ready || got.Generation != 7 || len(got.Servers) != 1 || got.Servers[0].Status != status || got.Servers[0].Tools != 2 || got.Servers[0].Source != "project" {
				t.Fatalf("probe = %+v", got)
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "fixture-private") {
				t.Fatal("private configuration leaked")
			}
		case <-time.After(15 * time.Second):
			t.Fatal("MCP status response not delivered")
		}
	}
}

func TestClaudeMCPKnownEmptyAndCancellation(t *testing.T) {
	d, p := claudeMCPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := d.MCPStatus(ctx); done <- e }()
	id := claudeMCPRequest(t, p)
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("cancel=%v", e)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cancellation blocked")
	}
	// A late response is consumed without marking an unrelated interrupt acknowledged.
	d.sm.Lock()
	d.steer.killTimer = time.AfterFunc(time.Hour, func() {})
	timer := d.steer.killTimer
	d.sm.Unlock()
	t.Cleanup(func() { d.sm.Lock(); d.stopTimerLocked(&d.steer.killTimer); d.sm.Unlock() })
	claudeMCPResponse(d, p, id, `{"mcpServers":[]}`)
	d.sm.Lock()
	same := d.steer.killTimer == timer
	d.sm.Unlock()
	if !same {
		t.Fatal("late MCP response acknowledged an interrupt")
	}
	result := make(chan mcpProbe, 1)
	go func() { v, _ := d.MCPStatus(context.Background()); result <- v }()
	id = claudeMCPRequest(t, p)
	claudeMCPResponse(d, p, id, `{"mcpServers":[]}`)
	select {
	case got := <-result:
		if !got.Ready || got.Servers == nil || len(got.Servers) != 0 {
			t.Fatalf("known empty=%+v", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("known-empty response blocked")
	}
}

func TestClaudeMCPInputGateKeepsFIFOAndAllowsStatusRPC(t *testing.T) {
	d, p := claudeMCPFixture(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.SetMCPInputGate(func(ctx context.Context) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	d.Enqueue("first", "chat")
	d.Enqueue("second", "chat")
	go func() { defer close(done); d.deliverOne(ctx) }()
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("input gate was not called")
	}
	select {
	case line := <-p.writeCh:
		t.Fatalf("input bypassed hold: %s", line)
	default:
	}
	result := make(chan error, 1)
	go func() { _, err := d.MCPStatus(ctx); result <- err }()
	id := claudeMCPRequest(t, p)
	claudeMCPResponse(d, p, id, `{"mcpServers":[]}`)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("gate blocked status RPC")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("gate did not release")
	}
	select {
	case line := <-p.writeCh:
		var input struct {
			Type    string `json:"type"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &input); err != nil {
			t.Fatal(err)
		}
		if input.Type != "user" || input.Message.Content != "first" {
			t.Fatalf("FIFO input=%s", line)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no input after release")
	}
	d.sm.Lock()
	queued := len(d.steer.sq)
	next := d.steer.sq[0].Text
	d.sm.Unlock()
	if queued != 1 || next != "second" {
		t.Fatalf("remaining queue=%d %q", queued, next)
	}
}

func TestClaudeMCPRequestPairingAndGeneration(t *testing.T) {
	d, p := claudeMCPFixture(t)
	first, second := make(chan mcpProbe, 1), make(chan mcpProbe, 1)
	go func() { v, _ := d.MCPStatus(context.Background()); first <- v }()
	id1 := claudeMCPRequest(t, p)
	go func() { v, _ := d.MCPStatus(context.Background()); second <- v }()
	id2 := claudeMCPRequest(t, p)
	if id1 == id2 {
		t.Fatal("duplicate request IDs")
	}
	// The same ID from another process must not satisfy either waiter.
	other := &ccProc{gen: 8}
	claudeMCPResponse(d, other, id1, `{"mcpServers":[{"name":"foreign","status":"connected"}]}`)
	claudeMCPResponse(d, p, id2, `{"mcpServers":[{"name":"second","status":"connected"}]}`)
	claudeMCPResponse(d, p, id1, `{"mcpServers":[{"name":"first","status":"connected"}]}`)
	for _, tc := range []struct {
		ch   chan mcpProbe
		name string
	}{{first, "first"}, {second, "second"}} {
		select {
		case got := <-tc.ch:
			if len(got.Servers) != 1 || got.Servers[0].Name != tc.name {
				t.Fatalf("response pairing=%+v want %s", got, tc.name)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("paired reply missing")
		}
	}
	if got := d.MCPGeneration(); got != 7 {
		t.Fatalf("generation=%d", got)
	}
	d.sm.Lock()
	d.steer.proc = nil
	d.steer.nextGen = 7
	d.sm.Unlock()
	if got := d.MCPGeneration(); got != 7 {
		t.Fatalf("idle generation=%d", got)
	}
}

func TestClaudeMCPInitFallbackAndInvalidResponse(t *testing.T) {
	for _, tc := range []struct {
		name, init string
		ready      bool
	}{{"pending", `[{"name":"jira","status":"pending","source":"user"}]`, true}, {"empty", `[]`, true}, {"absent", `null`, false}} {
		t.Run(tc.name, func(t *testing.T) {
			d, p := claudeMCPFixture(t)
			d.onLine(p, []byte(`{"type":"system","subtype":"init","mcp_servers":`+tc.init+`}`))
			done := make(chan struct {
				v mcpProbe
				e error
			}, 1)
			go func() {
				v, e := d.MCPStatus(context.Background())
				done <- struct {
					v mcpProbe
					e error
				}{v, e}
			}()
			id := claudeMCPRequest(t, p)
			d.onLine(p, []byte(`{"type":"control_response","response":{"subtype":"error","request_id":"`+id+`","error":"fixture-private"}}`))
			select {
			case got := <-done:
				if got.v.Ready != tc.ready || (got.e == nil) != tc.ready {
					t.Fatalf("fallback=%+v err=%v", got.v, got.e)
				}
				if got.e != nil && strings.Contains(got.e.Error(), "fixture-private") {
					t.Fatal("native error leaked")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("fallback blocked")
			}
		})
	}
}

func TestClaudeMCPGatewayAndPublicFieldsOnly(t *testing.T) {
	d, _ := claudeMCPFixture(t)
	d.mcpFile = newMCPConfigFile(&protocol.MCPGatewayConfig{Servers: []protocol.MCPGatewayServer{{Name: "docs", Token: "fixture-private"}}}, nil)
	raw := json.RawMessage(`[{"name":"docs","status":"connected","source":"user","tools":[{},{}],"config":{"url":"https://invalid.example/private","headers":{"Authorization":"fixture-private"}}},{"name":"other","status":"surprise","scope":"claudeai","tools":[]}]`)
	got, ok := d.claudeMCPServers(raw)
	if !ok || len(got) != 2 || got[0].Source != "gateway" || got[0].Tools != 2 || got[1].Status != "unknown" || got[1].Source != "claude.ai" {
		t.Fatalf("public metadata=%+v", got)
	}
	encoded, _ := json.Marshal(got)
	for _, private := range []string{"fixture-private", "https://", "Authorization", "config"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private metadata %q leaked", private)
		}
	}
	for _, bad := range []string{"null", "{}", `{"mcpServers":[]}`, `[{"name":"bad","status":3}]`} {
		if _, ok := d.claudeMCPServers(json.RawMessage(bad)); ok {
			t.Fatalf("accepted malformed inventory %s", bad)
		}
	}
}

func TestClaudeMCPShortDeadlineAndLegacy(t *testing.T) {
	d, p := claudeMCPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := d.MCPStatus(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline=%v", err)
	}
	_ = claudeMCPRequest(t, p)
	d.sm.Lock()
	waiting := len(p.mcpWait)
	d.steer.legacy = true
	d.sm.Unlock()
	if waiting != 0 {
		t.Fatalf("cancelled waiter leaked: %d", waiting)
	}
	if got, e := d.MCPStatus(context.Background()); e == nil || got.Ready {
		t.Fatalf("legacy claimed live status: %+v %v", got, e)
	}
}

// This helper stands in for docker exec, including its trailing sh/claude argv.
// It implements only the read-only status RPC, never a user/model request.
func TestHelperClaudeMCPNative(t *testing.T) {
	if os.Getenv("GO_FAKE_MCP_NATIVE") != "1" {
		t.Skip("helper process")
	}
	log, err := os.OpenFile(os.Getenv("MCP_NATIVE_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(log, strings.Join(os.Args, " "))
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req struct {
			Type    string `json:"type"`
			ID      string `json:"request_id"`
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.Type != "control_request" || req.Request.Subtype != "mcp_status" {
			_, _ = fmt.Fprintln(log, "UNEXPECTED_INPUT")
			os.Exit(3)
		}
		_, _ = fmt.Fprintln(log, "mcp_status")
		b, _ := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": req.ID, "response": map[string]any{"mcpServers": []any{map[string]any{"name": "jira", "status": "pending", "source": "project"}}}}})
		_, _ = os.Stdout.Write(append(b, '\n'))
	}
	_ = log.Close()
	os.Exit(0)
}

func TestClaudeMCPPreparesNativeProcessThroughSandboxPrefixWithoutPrompt(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "native.log")
	d := newClaudeCodeDriver(t.TempDir(), "fixture", "", &collectEmitter{}, []string{"GO_FAKE_MCP_NATIVE=1", "MCP_NATIVE_LOG=" + log})
	d.steer.legacy = false
	d.prefix = sandboxExec{bin, "-test.run=^TestHelperClaudeMCPNative$", "--"}
	d.flags = &ccCLIFlags{}
	cleared := make(chan struct{}, 1)
	originalDocker := dockerRun
	dockerRun = func(args ...string) error {
		if len(args) > 2 && args[2] == "rm" {
			select {
			case cleared <- struct{}{}:
			default:
			}
		}
		return nil
	}
	t.Cleanup(func() { dockerRun = originalDocker })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		d.sm.Lock()
		p := d.steer.proc
		d.sm.Unlock()
		d.shutdown()
		cancel()
		if p != nil {
			select {
			case <-p.done:
			case <-time.After(15 * time.Second):
				t.Error("native fake did not exit")
			}
			select {
			case <-cleared:
			case <-time.After(15 * time.Second):
				t.Error("native fake cleanup did not finish")
			}
		}
	})
	got, err := d.MCPStatus(ctx)
	if err != nil || !got.Ready || len(got.Servers) != 1 || got.Servers[0].Status != "pending" {
		t.Fatalf("startup status=%+v err=%v", got, err)
	}
	again, err := d.MCPStatus(ctx)
	if err != nil || again.Generation != got.Generation {
		t.Fatalf("second status restarted process: %+v %v", again, err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 || lines[1] != "mcp_status" || lines[2] != "mcp_status" || !strings.Contains(lines[0], " sh -c ") || !strings.Contains(lines[0], " sh claude ") || strings.Contains(string(data), "UNEXPECTED_INPUT") {
		t.Fatalf("native prefix/RPC log=%s", data)
	}
	d.sm.Lock()
	running := d.steer.running
	queued := len(d.steer.awaiting)
	d.sm.Unlock()
	if running || queued != 0 {
		t.Fatalf("status probe started a turn: running=%v awaiting=%d", running, queued)
	}
}

func TestClaudeMCPRejectsReplyAfterProcessReplacement(t *testing.T) {
	d, p := claudeMCPFixture(t)
	done := make(chan error, 1)
	go func() { _, err := d.MCPStatus(context.Background()); done <- err }()
	id := claudeMCPRequest(t, p)
	d.sm.Lock()
	d.steer.proc = &ccProc{gen: 8}
	d.sm.Unlock()
	claudeMCPResponse(d, p, id, `{"mcpServers":[]}`)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("old process response described replacement")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("old response blocked")
	}
}

func TestClaudeMCPRejectsIncompleteInventory(t *testing.T) {
	d, _ := claudeMCPFixture(t)
	for _, body := range []string{`[{"name":"a","status":"connected"},{"name":"a","status":"pending"}]`, `[{"name":"a","status":"connected"},{"name":"","status":"pending"}]`, `[{"name":"https://invalid.example","status":"pending"}]`} {
		if _, ok := d.claudeMCPServers(json.RawMessage(body)); ok {
			t.Fatalf("partial inventory accepted: %s", body)
		}
	}
	rows := make([]map[string]string, 257)
	for i := range rows {
		rows[i] = map[string]string{"name": fmt.Sprintf("server-%d", i), "status": "connected"}
	}
	rows[256]["status"] = "pending"
	raw, _ := json.Marshal(rows)
	if _, ok := d.claudeMCPServers(raw); ok {
		t.Fatal("oversized inventory hides pending tail")
	}
}

func TestClaudeMCPPreparationDoesNotRestartClosedOrLegacyStream(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		d, p := claudeMCPFixture(t)
		d.sm.Lock()
		d.steer.closed = !legacy
		d.steer.legacy = legacy
		d.sm.Unlock()
		got, err := d.ensureProc(context.Background())
		if got != nil || err == nil {
			t.Fatalf("returned unavailable stream (legacy=%v): %v %v", legacy, got, err)
		}
		// An engine that gave way to legacy must retain messages for Run's transfer,
		// rather than dequeueing them as failed native process preparation.
		d.sm.Lock()
		d.steer.sq = []steerQueued{{Input: agent.Input{Text: "retained", Source: "chat"}}}
		d.sm.Unlock()
		d.deliverOne(context.Background())
		d.sm.Lock()
		queued := len(d.steer.sq)
		d.sm.Unlock()
		if queued != 1 || len(p.writeCh) != 0 {
			t.Fatal("preparation bypassed shutdown/legacy transition")
		}
	}
	if got := claudeMCPGeneration(-1); got != 0 {
		t.Fatalf("negative generation=%d", got)
	}
}
