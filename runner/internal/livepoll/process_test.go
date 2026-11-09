package livepoll

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// The stub is a separate real OS process. Its provider is visibly simulated;
// there is no network access outside its ephemeral loopback listener.
func runMarketStub() int {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 1
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		var request struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request) != nil {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		reply := func(result json.RawMessage) {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
		switch request.Method {
		case "initialize":
			reply(json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"simulated market MCP","version":"1"}}`))
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			reply(json.RawMessage(`{"tools":[{"name":"quotes","description":"Simulated market quotes","inputSchema":{"type":"object","additionalProperties":false,"properties":{"symbol":{"type":"string"}},"required":["symbol"]},"annotations":{"readOnlyHint":true}}]}`))
		case "tools/call":
			if request.Params.Name != "quotes" || string(request.Params.Arguments) != `{"symbol":"NVDA"}` {
				w.WriteHeader(403)
				return
			}
			reply(json.RawMessage(`{"content":[{"type":"text","text":"Simulated NVDA quote"}],"structuredContent":{"quotes":[{"timestamp":"2026-11-02T14:30:00Z","close":185.25}],"provider":"simulated market MCP"},"isError":false}`))
		default:
			w.WriteHeader(400)
		}
	})
	fmt.Println("http://" + listener.Addr().String() + "/mcp")
	server := http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	if server.Serve(listener) != nil {
		return 1
	}
	return 0
}

type marketCredentials struct {
	url   string
	calls int
}

func (c *marketCredentials) ProviderCredential(_ context.Context, r ld.ProviderRequest) (ld.ProviderCredential, error) {
	if r.Token == "" || r.Attempt == "" || (r.Stage != "list" && r.Stage != "call") {
		return ld.ProviderCredential{}, ld.ErrDenied
	}
	c.calls++
	return ld.ProviderCredential{URL: c.url}, nil
}

func TestVerifierRealProcessMarket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, executable, "-test.run=^$")
	cmd.Env = append(os.Environ(), "BLERG_LIVE_MARKET_STUB=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal("start stub")
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("stub did not report ready")
	}
	s := scope()
	s.Hash = mcpgw.ToolHash("quotes", "Simulated market quotes", json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"symbol":{"type":"string"}},"required":["symbol"]}`))
	v, a, attempts, _, token := fixture(t, s)
	credentials := &marketCredentials{url: scanner.Text()}
	gateway := mcpgw.New(mcpgw.Config{Policy: netguard.Policy{AllowHTTPHosts: []string{"127.0.0.1"}, AllowPrivateHosts: []string{"127.0.0.1"}}})
	provider := gateway.LiveProvider(credentials)
	v.Provider = provider
	result, err := v.Execute(ctx, token, "market-attempt")
	if err != nil || !bytes.Contains(result, []byte("185.25")) {
		t.Fatal("simulated market call failed", err)
	}
	if !attempts.finished || a.calls != 3 || credentials.calls != 2 {
		t.Fatal("missing audit or fresh authority")
	}
	a.calls = 0
	a.revokeAt = 2
	beforeCredentials := credentials.calls
	if result, err := v.Execute(ctx, token, "unused-list"); err == nil || result != nil {
		t.Fatal("revoked listing reached provider")
	}
	callCtx := ld.WithProviderRequest(ctx, ld.ProviderRequest{Token: token, Attempt: "unused-list", Stage: "call"})
	if result, err := provider.Call(callCtx, a.snapshot.Grant.Scope, a.snapshot.Grant.Arguments); err == nil || result != nil || credentials.calls != beforeCredentials+1 {
		t.Fatal("abandoned real-process listing retained a credential exchange")
	}
	a.denied = true
	if result, err := v.Execute(ctx, token, "new-attempt"); err == nil || result != nil {
		t.Fatal("revoked authority reached market")
	}
}
