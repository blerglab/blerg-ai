package daemon

// The Codex child gets dynamic tool definitions, never gateway tokens or
// connection configuration. Calls cross the same runner gateway/proposal
// boundary as Claude; only this daemon holds and spends the grant.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type codexMCPRoute struct{ url, token, tool, server string }
type codexMCPBridge struct {
	client  *http.Client
	routes  map[string]codexMCPRoute
	specs   []map[string]any
	servers []string
}

func newCodexMCPBridge(ctx context.Context, cfg *protocol.MCPGatewayConfig) (*codexMCPBridge, error) {
	if err := ValidateMCPGateway(cfg); err != nil {
		return nil, err
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport is not supported")
	}
	transport := defaultTransport.Clone()
	transport.Proxy = nil
	b := &codexMCPBridge{client: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("gateway redirects are not allowed") }}, routes: map[string]codexMCPRoute{}}
	success := false
	defer func() {
		if !success {
			b.Close()
		}
	}()
	for _, server := range cfg.Servers {
		b.servers = append(b.servers, server.Name)
		route := codexMCPRoute{url: strings.TrimRight(cfg.BaseURL, "/") + "/mcp-gw/" + server.Name, token: server.Token, server: server.Name}
		cursor := ""
		for page := 0; page < 20; page++ {
			params := map[string]any{}
			if cursor != "" {
				params["cursor"] = cursor
			}
			raw, err := b.request(ctx, route, "tools/list", params)
			if err != nil {
				return nil, err
			}
			var list struct {
				Tools []struct {
					Name, Description string
					InputSchema       json.RawMessage `json:"inputSchema"`
				}
				NextCursor string `json:"nextCursor"`
			}
			if json.Unmarshal(raw, &list) != nil {
				return nil, errors.New("gateway returned an invalid tool catalog")
			}
			for _, t := range list.Tools {
				if len(b.specs) >= 500 {
					return nil, errors.New("gateway tool catalog exceeds the restricted tool limit")
				}
				if t.Name == "" || len(t.Name) > 256 || len(t.Description) > 4096 || len(t.InputSchema) > 64<<10 || !json.Valid(t.InputSchema) {
					return nil, errors.New("gateway returned an invalid tool descriptor")
				}
				name := codexDynamicMCPName(server.Name, t.Name)
				if _, exists := b.routes[name]; exists {
					return nil, errors.New("gateway tool names collide")
				}
				r := route
				r.tool = t.Name
				b.routes[name] = r
				b.specs = append(b.specs, map[string]any{"type": "function", "name": name, "description": t.Description, "inputSchema": t.InputSchema})
			}
			if list.NextCursor == "" {
				break
			}
			if list.NextCursor == cursor || page == 19 {
				return nil, errors.New("gateway tool pagination exceeds the restricted tool limit")
			}
			cursor = list.NextCursor
		}
	}
	sort.Slice(b.specs, func(i, j int) bool {
		left, _ := b.specs[i]["name"].(string)
		right, _ := b.specs[j]["name"].(string)
		return left < right
	})
	success = true
	return b, nil
}
func codexDynamicMCPName(server, tool string) string {
	name := "blerg_" + mcpToolPrefix(server) + mcpNameUnsafe.ReplaceAllString(tool, "_")
	if len(name) <= 64 && tool == mcpNameUnsafe.ReplaceAllString(tool, "_") {
		return name
	}
	hash := sha256.Sum256([]byte(server + "\x00" + tool))
	if len(name) > 47 {
		name = name[:47]
	}
	return name + "_" + hex.EncodeToString(hash[:8])
}
func (b *codexMCPBridge) Close()                  { b.client.CloseIdleConnections() }
func (b *codexMCPBridge) Specs() []map[string]any { return append([]map[string]any(nil), b.specs...) }
func (b *codexMCPBridge) request(ctx context.Context, route codexMCPRoute, method string, params any) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return nil, errors.New("invalid gateway request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, route.url, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("invalid gateway endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+route.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	response, err := b.client.Do(req)
	if err != nil {
		return nil, errors.New("MCP gateway request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("MCP gateway refused request (HTTP %d)", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
	if err != nil || len(body) > 4<<20 {
		return nil, errors.New("MCP gateway response exceeds the restricted tool limit")
	}
	var result struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &result) != nil || result.ID != 1 {
		return nil, errors.New("MCP gateway returned an invalid response")
	}
	if len(result.Error) > 0 && string(result.Error) != "null" {
		return nil, errors.New("MCP gateway refused the tool request")
	}
	return result.Result, nil
}
func (b *codexMCPBridge) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	route, ok := b.routes[name]
	if !ok {
		return "", errors.New("MCP tool is not in this session's grant")
	}
	if len(args) > 1<<20 || !json.Valid(args) {
		return "", errors.New("invalid MCP tool arguments")
	}
	raw, err := b.request(ctx, route, "tools/call", map[string]any{"name": route.tool, "arguments": args})
	if err != nil {
		return "", err
	}
	var result struct {
		Content []struct{ Type, Text string }
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return "", errors.New("MCP gateway returned an invalid tool result")
	}
	out := []string{}
	for _, item := range result.Content {
		if item.Type == "text" {
			out = append(out, item.Text)
		}
	}
	text := strings.Join(out, "\n")
	if len(text) > 128<<10 {
		text = text[:128<<10] + "\n[truncated]"
	}
	if result.IsError {
		return text, errors.New("MCP tool reported an error")
	}
	return text, nil
}

func (b *codexMCPBridge) capabilityItems() []protocol.CapabilityItem {
	counts := map[string]int{}
	for _, name := range b.servers {
		counts[name] = 0
	}
	for _, route := range b.routes {
		counts[route.server]++
	}
	items := []protocol.CapabilityItem{}
	for name, count := range counts {
		items = append(items, protocol.CapabilityItem{Name: name, Status: "connected", Source: "session grant", Detail: fmt.Sprintf("%d tools", count)})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}
