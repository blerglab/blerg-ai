package mcpgw

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
)

type realBoardExchange struct{ key ed25519.PrivateKey }

func (e realBoardExchange) Exchange(_ context.Context, p Proof, board, _ string) (BoardToken, error) {
	now := time.Now()
	claims := cid.Claims{Sub: "exchange-fixture", Aud: "blerg-board", Kind: "agent", Project: board, Caps: []string{"card.read", "card.write"}, PersonBehind: PersonBehind(p), IssuedAt: now.Unix(), ExpiresAt: now.Add(10 * time.Minute).Unix()}
	h, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": "review-key"})
	b, _ := json.Marshal(claims)
	si := cid.EncodeSigningInput(h, b)
	return BoardToken{Token: si + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(e.key, []byte(si))), Sub: claims.Sub, ExpiresAt: now.Add(10 * time.Minute)}, nil
}
func (realBoardExchange) Revoke(context.Context, string, string) error { return nil }

// This is the production gateway transport and production Board executable,
// including its migrations, signed-token verification, MCP handler and archive transaction.
func TestArchiveReviewGatewayToRealBoard(t *testing.T) {
	pool := testsupport.Blank(t, "TEST_DATABASE_URL")
	binary, buildElapsed, err := realBoardBinary.get(t.Context())
	t.Logf("real Board build (shared per package run): %s", buildElapsed)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jwks":
			_ = json.NewEncoder(w).Encode(map[string]string{"review-key": base64.RawURLEncoding.EncodeToString(pub)})
		case "/revocations":
			_, _ = w.Write([]byte("[]"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(core.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	command := exec.CommandContext(t.Context(), binary)
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "BLERG_") {
			env = append(env, value)
		}
	}
	command.Env = env
	command.Env = append(command.Env, "DATABASE_URL="+pool.Config().ConnString(), "BLERG_BOARD_ADDR="+addr, "BLERG_CORE_URL="+core.URL, "BLERG_BOARD_SERVICE_KEY=review-service-key-0123456789abcdef", "BLERG_BOARD_AUTOMATION_KEY=0123456789abcdef0123456789abcdef")
	var logs bytes.Buffer
	command.Stdout = &logs
	command.Stderr = &logs
	startupStarted := time.Now()
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	base := "http://" + addr
	client := &http.Client{Timeout: time.Second}
	ready := false
	var readinessErr error
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, e := http.NewRequestWithContext(t.Context(), "GET", base+"/api/version", nil)
		if e != nil {
			t.Fatal(e)
		}
		r, e := client.Do(req)
		readinessErr = e
		if e == nil {
			_ = r.Body.Close()
			readinessErr = fmt.Errorf("readiness status: %d", r.StatusCode)
			if r.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("real Board startup: %s", time.Since(startupStarted))
	if !ready {
		t.Fatalf("real Board did not become ready: %v", readinessErr)
	}
	request := func(method, path, token string, body any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		r, _ := http.NewRequestWithContext(t.Context(), method, base+path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		resp, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			t.Fatalf("real Board %s %s status=%d body=%s", method, path, resp.StatusCode, data)
		}
		var out map[string]any
		if e = json.Unmarshal(data, &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	board := request("POST", "/api/boards", "review-service-key-0123456789abcdef", map[string]any{"name": "Archive review", "gate_enabled": false, "require_repo": false})
	id, ok := board["id"].(string)
	if !ok || id == "" {
		t.Fatal("real Board create did not return an id")
	}
	card := request("POST", "/api/boards/"+id+"/cards", "review-service-key-0123456789abcdef", map[string]any{"title": "Archive through the gateway"})
	cardID, ok := card["id"].(string)
	if !ok || cardID == "" {
		t.Fatal("real Board card create did not return an id")
	}
	exchange := realBoardExchange{priv}
	plain, err := exchange.Exchange(t.Context(), Proof{AccountID: "review-account", TokenID: "review-agent"}, id, "review-session")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/cards/" + cardID + "/archive", "/mcp"} {
		body := []byte("{}")
		if path == "/mcp" {
			body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "blerg_card_archive", "arguments": map[string]string{"card_id": cardID}}})
		}
		req, _ := http.NewRequestWithContext(t.Context(), "POST", base+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+plain.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if path == "/mcp" {
			var result struct {
				Result struct {
					IsError bool `json:"isError"`
				}
			}
			if e = json.Unmarshal(data, &result); e != nil || !result.Result.IsError {
				t.Fatalf("plain token MCP archive allowed: status=%d", resp.StatusCode)
			}
		} else if resp.StatusCode != 403 {
			t.Fatalf("plain token REST archive=%d want403", resp.StatusCode)
		}
	}
	gw := New(Config{DB: testPool(t), Board: exchange, BoardMCPURL: base + "/mcp", Policy: netguard.Policy{AllowHTTPHosts: []string{"127.0.0.1"}, AllowPrivateHosts: []string{"127.0.0.1"}}})
	args, _ := json.Marshal(map[string]string{"card_id": cardID})
	_, err = gw.CallBoard(t.Context(), BoardCall{Proof: Proof{AccountID: "review-account", TokenID: "review-agent"}, BoardID: id, SessionRef: "review-session", Tool: "blerg_card_archive", Arguments: args})
	if !errors.Is(err, ErrBoardCallInvalid) {
		t.Fatalf("agent-token archive error=%v", err)
	}
	result, err := gw.CallBoard(t.Context(), BoardCall{Proof: Proof{AccountID: "review-account", SessionID: "review-login"}, BoardID: id, SessionRef: "review-session", Tool: "blerg_card_archive", Arguments: args})
	if err != nil {
		t.Fatalf("real gateway archive: %v", err)
	}
	var tool struct {
		IsError bool `json:"isError"`
	}
	if err = json.Unmarshal(result, &tool); err != nil || tool.IsError {
		t.Fatalf("archive tool result=%s err=%v", result, err)
	}
	var archived bool
	if err = pool.QueryRow(t.Context(), `SELECT archived_at IS NOT NULL AND column_id IS NULL FROM cards WHERE id=$1`, cardID).Scan(&archived); err != nil || !archived {
		t.Fatalf("real Board did not archive: archived=%v err=%v", archived, err)
	}
}
