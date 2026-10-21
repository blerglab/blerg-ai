package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const sessionFilingMCPName = "_blerg_session"
const sessionFilingToolName = "blerg_ticket_file"
const sessionPluginRequestToolName = "blerg_request_plugin"
const sessionBoardToolName = "blerg_board"

// These are fixed own-session proposal/read tools, never arbitrary MCP grant
// names or destinations. Adding one does not enable shell or ambient servers.
var sessionBuiltinTools = []struct {
	name string
	spec func() map[string]any
}{
	{sessionFilingToolName, sessionFilingSpec},
	{sessionPluginRequestToolName, sessionPluginRequestSpec},
	{sessionBoardToolName, sessionBoardSpec},
}

func sessionBuiltinAllowed(name string) bool {
	for _, tool := range sessionBuiltinTools {
		if tool.name == name {
			return true
		}
	}
	return false
}
func sessionBuiltinSpecs() []map[string]any {
	specs := make([]map[string]any, 0, len(sessionBuiltinTools))
	for _, tool := range sessionBuiltinTools {
		specs = append(specs, tool.spec())
	}
	return specs
}
func sessionPluginRequestSpec() map[string]any {
	return map[string]any{"type": "function", "name": sessionPluginRequestToolName, "description": "Request an allowed plugin or its skills. This only queues human approval; it never installs code or changes settings. Restricted sessions keep their restrictions.", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"name", "reason"}, "properties": map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 205}, "reason": map[string]any{"type": "string", "minLength": 1, "maxLength": 2000}}}}
}

// sessionBoardSpec is the session's board tool (0.4.4): the server checks the board's level in the
// session's Permissions on every call and makes the call itself; the session holds no board
// credential. It is always offered, so a level the person gives while the engine runs works at once.
func sessionBoardSpec() map[string]any {
	str := map[string]any{"type": "string"}
	return map[string]any{"type": "function", "name": sessionBoardToolName, "description": "Read or change cards on a board your person gave this session access to in its Permissions. " +
		"Call {\"tool\":\"access\"} first to list the boards and their level. Level read: blerg_board_get, blerg_board_list, blerg_board_schema, blerg_column_list, " +
		"blerg_card_search, blerg_card_get. Level full adds blerg_card_create, blerg_card_update, blerg_card_move, blerg_card_comment, blerg_card_link. " +
		"The person can change levels while you work: if a board tool you need is not listed or a call is refused, call this tool with {\"tool\":\"access\"} " +
		"before telling the person you lack permission, and use it for anything the level allows. Without access, use blerg_ticket_file to propose a card; it asks the person.",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"tool"}, "properties": map[string]any{
			"tool": map[string]any{"type": "string", "enum": []string{"access", "blerg_board_get", "blerg_board_list", "blerg_board_schema", "blerg_column_list",
				"blerg_card_search", "blerg_card_get", "blerg_card_create", "blerg_card_update", "blerg_card_move", "blerg_card_comment", "blerg_card_link"}},
			"board": str, "arguments": map[string]any{"type": "object"}}}}
}

func sessionFilingStdio() map[string]any {
	return map[string]any{"command": "blerg-runner", "args": []string{"_session_mcp"}}
}
func codexSessionFilingStdio() map[string]any {
	c := sessionFilingStdio()
	c["env_vars"] = []string{"BLERG_RUNNER_SESSION_ID", "BLERG_RUNNER_SERVER_HTTP", "BLERG_RUNNER_SESSION_TOKEN", "BLERG_RUNNER_DAEMON_STATE_DIR", "HOME", "PATH"}
	return c
}
func sessionFilingSpec() map[string]any {
	str := map[string]any{"type": "string"}
	return map[string]any{"type": "function", "name": sessionFilingToolName, "description": "List accessible boards, search for duplicates, or propose a ticket. A person must approve before any card is written.", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action"}, "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"list_boards", "search", "propose"}}, "board": str, "query": str, "title": str, "body": str, "type": str, "sensitive": map[string]any{"type": "boolean"}, "repos": map[string]any{"type": "array", "items": str}}}}
}

// sessionFilingTool is used only by Codex's already isolated dynamic runtime.
// It grants the same approval-gated self-service API as the stdio shim, without
// enabling native MCP, a shell, user configuration or arbitrary destinations.
type sessionFilingTool struct{ server, session, initialToken, stateDir string }

func (t *sessionFilingTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	return t.CallNamed(ctx, sessionFilingToolName, args)
}

func (t *sessionFilingTool) CallNamed(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if !sessionBuiltinAllowed(name) {
		return "", errors.New("unknown session tool")
	}
	if len(args) > 256<<10 || !json.Valid(args) {
		return "", errors.New("invalid filing arguments")
	}
	token := t.initialToken
	if path := SessionTokenPath(t.stateDir, t.session); path != "" {
		if f, err := os.Open(path); err == nil { //nolint:gosec // G304: validated session filename under the daemon-owned private token directory.
			raw, e := io.ReadAll(io.LimitReader(f, 8193))
			_ = f.Close()
			if e != nil || len(raw) > 8192 {
				return "", errors.New("session credential is unavailable")
			}
			if s := strings.TrimSpace(string(raw)); s != "" {
				token = s
			}
		}
	}
	if token == "" {
		return "", errors.New("session credential is unavailable")
	}
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	endpoint := strings.TrimRight(t.server, "/") + "/api/sessions/" + url.PathEscape(t.session) + "/filing-tools"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", errors.New("session filing endpoint is unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return "", errors.New("session filing transport is unavailable")
	}
	tr := transport.Clone()
	tr.Proxy = nil
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("session filing redirects are refused") }}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("session filing is unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || resp.StatusCode != http.StatusOK {
		return "", errors.New("session filing request was refused")
	}
	var result struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &result) != nil || result.ID != 1 || len(result.Result) == 0 || (len(result.Error) > 0 && string(result.Error) != "null") {
		return "", errors.New("invalid session filing reply")
	}
	var toolResult struct {
		Content []struct{ Type, Text string }
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(result.Result, &toolResult) != nil || toolResult.Content == nil {
		return "", errors.New("invalid session filing reply")
	}
	texts := []string{}
	for _, item := range toolResult.Content {
		if item.Type == "text" {
			texts = append(texts, item.Text)
		}
	}
	text := strings.ReplaceAll(strings.Join(texts, "\n"), token, "[token]")
	if t.initialToken != "" {
		text = strings.ReplaceAll(text, t.initialToken, "[token]")
	}
	if toolResult.IsError {
		return text, errors.New("session filing tool refused the request")
	}
	return text, nil
}

// sessionFilingMCPConfigJSON merges exactly one daemon-owned server into the
// private gateway file. Its reserved name cannot occur in a gateway grant.
func sessionFilingMCPConfigJSON(gateway *protocol.MCPGatewayConfig) ([]byte, error) {
	servers := map[string]any{}
	if gateway != nil {
		raw, err := mcpConfigJSON(gateway)
		if err != nil {
			return nil, err
		}
		var cfg struct {
			Servers map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
		servers = cfg.Servers
	}
	servers[sessionFilingMCPName] = sessionFilingStdio()
	return json.Marshal(map[string]any{"mcpServers": servers})
}

// Only the fixed self-service tools may extend the existing dynamic router to
// children. Gateway tool policy stays root-only. Ownership is enrolled by native
// spawn proofs, never by caller-supplied thread metadata.
func (a *codexApp) sessionBuiltinChildAllowed(thread string) bool {
	if a.ctx == nil || a.ctx.Err() != nil {
		return false
	}
	select {
	case <-a.done:
		return false
	default:
	}
	a.d.mu.Lock()
	defer a.d.mu.Unlock()
	child := a.d.children[thread]
	return a.d.app == a && thread != a.d.threadID && child != nil && !child.done && child.callID != "" && a.d.RestrictedRuntime != nil && a.d.RestrictedRuntime.filing != nil && !a.d.codexToolsRestricted()
}
