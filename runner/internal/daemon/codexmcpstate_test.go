package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type mcpStatusWriter struct {
	app     *codexApp
	calls   atomic.Int32
	respond func(int, map[string]any) codexRPC
}

func TestCodexMCPInputGateKeepsNativeRPCAlive(t *testing.T) {
	dir := fakeCodex(t)
	logPath := filepath.Join(dir, "calls.log")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_LOG", logPath)
	t.Setenv("FAKE_CODEX_MCP_STATUS", "starting")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	d.SetMCPInputGate(func(ctx context.Context) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { d.Run(ctx); close(finished) }()
	defer func() { cancel(); <-finished }()
	d.Enqueue("first fixture prompt", "chat")
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("native input gate not entered")
	}
	// The gate has no driver/app lock. A status RPC succeeds while input is held.
	got, err := d.MCPStatus(ctx)
	if err != nil || !got.Ready || len(got.Servers) != 1 || got.Servers[0].Status != "pending" {
		t.Fatalf("held native status: %+v %v", got, err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "turn/start") || countKind(em, "user_message") != 0 {
		t.Fatal("prompt sent before gate release")
	}
	close(release)
	deadline := time.After(15 * time.Second)
	for countKind(em, "turn_done") < 1 {
		select {
		case <-deadline:
			t.Fatal("released input did not run")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if d.MCPGeneration() != 1 {
		t.Fatal("native generation not incremented")
	}
	d.Enqueue("second fixture prompt", "chat")
	deadline = time.After(15 * time.Second)
	for countKind(em, "turn_done") < 2 {
		select {
		case <-deadline:
			t.Fatal("second input did not run")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if calls.Load() != 2 {
		t.Fatal("host gate must see each new queued turn")
	}
}

func (w *mcpStatusWriter) Close() error { return nil }
func (w *mcpStatusWriter) Write(raw []byte) (int, error) {
	var req struct {
		ID     int
		Method string
		Params map[string]any
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return 0, err
	}
	if req.Method != "mcpServerStatus/list" {
		return 0, errors.New("probe must only request MCP status")
	}
	n := int(w.calls.Add(1))
	reply := w.respond(n, req.Params)
	if len(reply.Result) > 0 || len(reply.Error) > 0 {
		w.app.mu.Lock()
		ch := w.app.pending[req.ID]
		w.app.mu.Unlock()
		ch <- reply
	}
	return len(raw), nil
}
func mcpFixture(result string) codexRPC { return codexRPC{Result: json.RawMessage(result)} }
func mcpDriverFixture(respond func(int, map[string]any) codexRPC) (*codexDriver, *mcpStatusWriter) {
	d := &codexDriver{mcpGeneration: 7, threadID: "fixture-thread"}
	a := &codexApp{d: d, ctx: context.Background(), pending: map[int]chan codexRPC{}, done: make(chan struct{}), mcpInitialized: true, mcpThreadReady: true}
	w := &mcpStatusWriter{app: a, respond: respond}
	a.in = w
	d.app = a
	return d, w
}

func TestCodexMCPStatusPendingToConnected(t *testing.T) {
	d, w := mcpDriverFixture(func(n int, p map[string]any) codexRPC {
		if n == 1 && p["threadId"] != nil {
			t.Error("pre-thread discovery must omit threadId")
		}
		if n == 2 && p["threadId"] != "fixture-thread" {
			t.Error("thread inventory did not reuse the native thread connection")
		}
		if p["detail"] != "toolsAndAuthOnly" {
			t.Error("probe requested sensitive/full inventory")
		}
		if n == 1 {
			return mcpFixture(`{"data":[{"name":"jira","runtimeStatus":"starting","tools":{}}],"nextCursor":null}`)
		}
		return mcpFixture(`{"data":[{"name":"jira","runtimeStatus":"connected","tools":{"search":{},"write":{}}}],"nextCursor":null}`)
	})
	d.app.mcpThreadReady = false
	for i, want := range []string{"pending", "connected"} {
		if i == 1 {
			d.app.mcpThreadReady = true
		}
		got, err := d.MCPStatus(context.Background())
		if err != nil || got.Ready != (i == 1) || got.Generation != 7 || len(got.Servers) != 1 || got.Servers[0].Status != want || got.Servers[0].Tools != i*2 {
			t.Fatalf("snapshot %+v: %v", got, err)
		}
	}
	if w.calls.Load() != 2 {
		t.Fatal("unexpected RPC count")
	}
}

func TestCodexMCPStatusPreThreadConnectedRemainsPending(t *testing.T) {
	d, _ := mcpDriverFixture(func(int, map[string]any) codexRPC {
		return mcpFixture(`{"data":[{"name":"jira","runtimeStatus":"connected","tools":{}}]}`)
	})
	d.app.mcpThreadReady = false
	got, err := d.MCPStatus(context.Background())
	if err != nil || got.Ready || len(got.Servers) != 1 || got.Servers[0].Status != "pending" {
		t.Fatalf("global status cannot settle thread inventory: %+v %v", got, err)
	}
}

func TestCodexMCPStatusCompleteInventoryRequired(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":null}`, `{"data":[]`, `{"data":[],"nextCursor":"again"}`} {
		t.Run(body, func(t *testing.T) {
			d, _ := mcpDriverFixture(func(int, map[string]any) codexRPC { return mcpFixture(body) })
			got, err := d.MCPStatus(context.Background())
			if err == nil || got.Ready || len(got.Servers) != 0 {
				t.Fatalf("incomplete response accepted: %+v %v", got, err)
			}
		})
	}
	d, _ := mcpDriverFixture(func(int, map[string]any) codexRPC { return mcpFixture(`{"data":[],"nextCursor":null}`) })
	got, err := d.MCPStatus(context.Background())
	if err != nil || !got.Ready || len(got.Servers) != 0 {
		t.Fatalf("known empty: %+v %v", got, err)
	}
	d.app.mcpThreadReady = false
	got, err = d.MCPStatus(context.Background())
	if err != nil || got.Ready {
		t.Fatalf("global empty cannot settle thread inventory: %+v %v", got, err)
	}
	d.app.mcpInitialized = false
	got, err = d.MCPStatus(context.Background())
	if err != nil || got.Ready {
		t.Fatalf("pre-initialize is not empty inventory: %+v %v", got, err)
	}
}

func TestCodexMCPStatusPaginationFailureAndBound(t *testing.T) {
	d, _ := mcpDriverFixture(func(n int, p map[string]any) codexRPC {
		if n == 1 {
			return mcpFixture(`{"data":[{"name":"jira","runtimeStatus":"starting"}],"nextCursor":"page2"}`)
		}
		if p["cursor"] != "page2" {
			t.Error("cursor omitted")
		}
		return codexRPC{Error: json.RawMessage(`{"code":-32601,"message":"private endpoint and token"}`)}
	})
	got, err := d.MCPStatus(context.Background())
	if err == nil || got.Ready || len(got.Servers) != 0 || strings.Contains(err.Error(), "private") {
		t.Fatalf("partial failure: %+v %v", got, err)
	}
	d, w := mcpDriverFixture(func(n int, _ map[string]any) codexRPC {
		b, _ := json.Marshal(map[string]any{"data": []any{}, "nextCursor": strconv.Itoa(n)})
		return codexRPC{Result: b}
	})
	_, err = d.MCPStatus(context.Background())
	if err == nil || w.calls.Load() != 20 {
		t.Fatal("pagination unbounded or malformed cursor accepted")
	}
}

func TestCodexMCPStatusInventoryBound(t *testing.T) {
	d, _ := mcpDriverFixture(func(int, map[string]any) codexRPC {
		servers := make([]map[string]any, 257)
		for i := range servers {
			servers[i] = map[string]any{"name": "fixture-" + strconv.Itoa(i), "runtimeStatus": "connected"}
		}
		raw, _ := json.Marshal(map[string]any{"data": servers})
		return codexRPC{Result: raw}
	})
	got, err := d.MCPStatus(context.Background())
	if err == nil || got.Ready || len(got.Servers) != 0 {
		t.Fatalf("oversized inventory accepted: %+v %v", got, err)
	}
}

func TestCodexMCPStatusInvalidNamesAreNotKnownEmpty(t *testing.T) {
	for _, names := range [][]string{{"https://private.invalid"}, {"duplicate", " duplicate "}, {""}} {
		d, _ := mcpDriverFixture(func(int, map[string]any) codexRPC {
			rows := make([]map[string]any, len(names))
			for i, name := range names {
				rows[i] = map[string]any{"name": name, "runtimeStatus": "connected"}
			}
			raw, _ := json.Marshal(map[string]any{"data": rows})
			return codexRPC{Result: raw}
		})
		got, err := d.MCPStatus(context.Background())
		if err == nil || got.Ready || len(got.Servers) > 0 {
			t.Fatalf("dropped names became ready inventory: %+v %v", got, err)
		}
	}
}

func TestCodexMCPInputGatePreservesInputAcrossProcessRestart(t *testing.T) {
	dir := fakeCodex(t)
	logPath := filepath.Join(dir, "calls.log")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_LOG", logPath)
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	var gates atomic.Int32
	d.SetMCPInputGate(func(ctx context.Context) {
		if gates.Add(1) == 1 {
			d.mu.Lock()
			cmd := d.cmd
			d.mu.Unlock()
			if err := cmd.Process.Kill(); err != nil {
				t.Error(err)
			}
			<-ctx.Done()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { d.Run(ctx); close(finished) }()
	defer func() { cancel(); <-finished }()
	d.Enqueue("survive held restart", "chat")
	deadline := time.After(6 * time.Second)
	for countKind(em, "turn_done") < 1 {
		select {
		case <-deadline:
			t.Fatal("held input lost across restart")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if d.MCPGeneration() != 2 || gates.Load() != 2 || countKind(em, "user_message") != 1 {
		t.Fatal("restart changed input delivery count")
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "turn/start ") != 1 {
		t.Fatal("held input sent more than once")
	}
}

func TestCodexMCPStatusMappingsAndPrivacy(t *testing.T) {
	cases := map[string]string{"notStarted": "pending", "starting": "pending", "connected": "connected", "authenticationRequired": "needs-auth", "failed": "failed", "cancelled": "failed", "disabled": "disabled", "future": "unknown", "": "unknown"}
	for native, want := range cases {
		t.Run(native, func(t *testing.T) {
			d, _ := mcpDriverFixture(func(int, map[string]any) codexRPC {
				b, _ := json.Marshal(map[string]any{"data": []any{map[string]any{"name": "fixture", "runtimeStatus": native, "pluginId": "private-plugin-value", "httpOrigin": "https://private.invalid", "toolsError": "secret", "tools": map[string]any{"private-tool": map[string]string{"description": "secret"}}}}})
				return codexRPC{Result: b}
			})
			got, err := d.MCPStatus(context.Background())
			if err != nil || !reflect.DeepEqual(got.Servers, []protocol.MCPServerStatus{{Name: "fixture", Status: want, Source: "plugin", Tools: 1}}) {
				t.Fatalf("mapping: %+v %v", got, err)
			}
			b, _ := json.Marshal(got)
			if strings.Contains(string(b), "private") || strings.Contains(string(b), "secret") {
				t.Fatal("native config escaped whitelist")
			}
		})
	}
}

func TestCodexMCPStatusContextAndGeneration(t *testing.T) {
	d, _ := mcpDriverFixture(func(int, map[string]any) codexRPC { return codexRPC{} })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	got, err := d.MCPStatus(ctx)
	if err == nil || got.Ready || time.Since(started) > time.Second {
		t.Fatalf("unbounded canceled probe: %+v %v", got, err)
	}
	d, _ = mcpDriverFixture(func(int, map[string]any) codexRPC {
		d.mu.Lock()
		d.mcpGeneration++
		d.app = nil
		d.mu.Unlock()
		return mcpFixture(`{"data":[],"nextCursor":null}`)
	})
	got, err = d.MCPStatus(context.Background())
	if got.Ready || got.Generation != 8 || err == nil || d.MCPGeneration() != 8 {
		t.Fatalf("stale process snapshot: %+v %v", got, err)
	}
}

func TestCodexMCPStatusRestrictedGatewayOnly(t *testing.T) {
	d, w := mcpDriverFixture(func(int, map[string]any) codexRPC {
		t.Error("restricted sessions must not query ambient MCP")
		return codexRPC{}
	})
	d.RestrictedRuntime = &codexRestrictedRuntime{bridge: &codexMCPBridge{servers: []string{"jira"}, routes: map[string]codexMCPRoute{"tool": {server: "jira", url: "https://private.invalid", token: "private"}}}}
	for _, ready := range []bool{false, true} {
		d.app.mcpThreadReady = ready
		got, err := d.MCPStatus(context.Background())
		status := "pending"
		if ready {
			status = "connected"
		}
		if err != nil || !got.Ready || !reflect.DeepEqual(got.Servers, []protocol.MCPServerStatus{{Name: "jira", Status: status, Source: "gateway", Tools: 1}}) {
			t.Fatalf("gateway: %+v %v", got, err)
		}
	}
	if w.calls.Load() != 0 {
		t.Fatal("ambient MCP queried")
	}
	d.RestrictedRuntime.bridge = nil
	got, err := d.MCPStatus(context.Background())
	if err != nil || !got.Ready || len(got.Servers) != 0 {
		t.Fatalf("empty grant: %+v %v", got, err)
	}
}
