package daemon

// Claude Code's native stream-json control protocol is read-only here: the SDK
// mcpServerStatus call sends subtype:mcp_status and reads response.mcpServers.
// No user message, model turn, separate CLI, or host-side MCP config probe is used.
import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const ccMCPRequestPrefix = "blerg-mcp-"
const ccMCPQueryTimeout = 2 * time.Second

type ccControlResponse struct {
	Response struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Body      json.RawMessage `json:"response"`
	} `json:"response"`
}
type ccMCPReply struct {
	body json.RawMessage
	ok   bool
}

// MCPGeneration does not start a process. Keep the latest native generation even
// while its idle process is absent, so idle shutdown cannot look like a restart.
func (d *claudeCodeDriver) MCPGeneration() uint64 {
	d.sm.Lock()
	defer d.sm.Unlock()
	if d.steer.proc != nil {
		return claudeMCPGeneration(d.steer.proc.gen)
	}
	return claudeMCPGeneration(d.steer.nextGen)
}

// MCPStatus may prepare the existing steering process. ctx owns that process's
// session lifetime; only the status RPC receives the independent two-second cap.
func (d *claudeCodeDriver) MCPStatus(ctx context.Context) (mcpProbe, error) {
	if err := ctx.Err(); err != nil {
		return mcpProbe{}, err
	}
	d.sm.Lock()
	legacy, closed := d.steer.legacy, d.steer.closed
	d.sm.Unlock()
	if legacy || closed {
		return mcpProbe{}, errors.New("claude live MCP status is unavailable")
	}
	p, err := d.ensureProc(ctx)
	if err != nil {
		return mcpProbe{}, errors.New("claude MCP process preparation failed")
	}
	rpcCtx, cancel := context.WithTimeout(ctx, ccMCPQueryTimeout)
	defer cancel()
	id := ccMCPRequestPrefix + ccUUID()
	response := make(chan ccMCPReply, 1)
	d.sm.Lock()
	if d.steer.closed || d.steer.proc != p || p.dying || p.stdinClosed {
		d.sm.Unlock()
		return mcpProbe{}, errors.New("claude MCP process changed")
	}
	if len(p.mcpWait) >= 8 {
		d.sm.Unlock()
		return mcpProbe{}, errors.New("claude MCP status query capacity reached")
	}
	if p.mcpWait == nil {
		p.mcpWait = make(map[string]chan ccMCPReply)
	}
	p.mcpWait[id] = response
	d.sm.Unlock()
	defer func() { d.sm.Lock(); delete(p.mcpWait, id); d.sm.Unlock() }()
	line, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": "mcp_status"}})
	select {
	case p.writeCh <- append(line, '\n'):
	case <-p.done:
		return mcpProbe{}, errors.New("claude MCP process exited")
	case <-rpcCtx.Done():
		return mcpProbe{}, rpcCtx.Err()
	}
	select {
	case reply := <-response:
		if reply.ok {
			var body struct {
				Servers json.RawMessage `json:"mcpServers"`
			}
			if json.Unmarshal(reply.body, &body) == nil {
				if servers, ok := d.claudeMCPServers(body.Servers); ok {
					return d.currentMCPProbe(p, mcpProbe{Servers: servers, Generation: claudeMCPGeneration(p.gen), Ready: true})
				}
			}
			return mcpProbe{Generation: claudeMCPGeneration(p.gen)}, errors.New("claude MCP status inventory invalid")
		}
		// Older CLIs may reject this control request. Their explicit init inventory
		// remains useful, including pending servers, but absence is never ready-empty.
		d.sm.Lock()
		fallback := p.mcpInit
		d.sm.Unlock()
		if fallback.Ready {
			return d.currentMCPProbe(p, fallback)
		}
		return mcpProbe{Generation: claudeMCPGeneration(p.gen)}, errors.New("claude MCP status response unavailable")
	case <-p.done:
		return mcpProbe{Generation: claudeMCPGeneration(p.gen)}, errors.New("claude MCP process exited")
	case <-rpcCtx.Done():
		return mcpProbe{Generation: claudeMCPGeneration(p.gen)}, rpcCtx.Err()
	}
}

func (d *claudeCodeDriver) routeMCPControlResponse(p *ccProc, line []byte) bool {
	var reply ccControlResponse
	if json.Unmarshal(line, &reply) != nil || !strings.HasPrefix(reply.Response.RequestID, ccMCPRequestPrefix) {
		return false
	}
	d.sm.Lock()
	waiting := p.mcpWait[reply.Response.RequestID]
	d.sm.Unlock()
	if waiting != nil {
		select {
		case waiting <- ccMCPReply{body: reply.Response.Body, ok: reply.Response.Subtype == "success"}:
		default:
		}
	}
	// Late/cancelled/foreign-generation replies must never acknowledge an interrupt.
	return true
}

func (d *claudeCodeDriver) observeMCPInit(p *ccProc, line []byte) {
	var init struct {
		Servers json.RawMessage `json:"mcp_servers"`
	}
	if json.Unmarshal(line, &init) != nil {
		return
	}
	servers, ok := d.claudeMCPServers(init.Servers)
	if !ok {
		return
	}
	d.sm.Lock()
	p.mcpInit = mcpProbe{Servers: servers, Generation: claudeMCPGeneration(p.gen), Ready: true}
	d.sm.Unlock()
}

// Decode only public inventory fields; config, endpoints, errors, tool names and
// descriptions are deliberately never decoded into the resulting public type.
func (d *claudeCodeDriver) claudeMCPServers(raw json.RawMessage) ([]protocol.MCPServerStatus, bool) {
	var rows []struct {
		Name   string            `json:"name"`
		Status string            `json:"status"`
		Source string            `json:"source"`
		Scope  string            `json:"scope"`
		Tools  []json.RawMessage `json:"tools"`
	}
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &rows) != nil {
		return nil, false
	}
	if len(rows) > 256 {
		return nil, false
	}
	out := make([]protocol.MCPServerStatus, 0, len(rows))
	for _, row := range rows {
		if row.Name == "" {
			return nil, false
		}
		status := row.Status
		if !mcpStatuses[status] {
			status = "unknown"
		}
		source := row.Source
		if source == "" {
			source = row.Scope
		}
		switch source {
		case "user", "project", "local", "plugin", "sdk", "managed", "enterprise", "dynamic", "agent", "gateway", "claude.ai":
		case "claudeai":
			source = "claude.ai"
		default:
			source = "unknown"
		}
		if d.mcpFile != nil && d.mcpFile.cfg != nil {
			for _, s := range d.mcpFile.cfg.Servers {
				if row.Name == s.Name {
					source = "gateway"
					break
				}
			}
		}
		out = append(out, protocol.MCPServerStatus{Name: row.Name, Status: status, Source: source, Tools: len(row.Tools)})
	}
	clean := protocol.SanitizeMCPStatus(protocol.MCPStatusPayload{Servers: out}).Servers
	if len(clean) != len(rows) {
		return nil, false
	}
	return clean, true
}

// SetMCPInputGate installs the shared first-prompt hold. The hook runs after the
// native process exists and outside driver locks, retaining the original FIFO.
func (d *claudeCodeDriver) SetMCPInputGate(gate func(context.Context)) {
	d.sm.Lock()
	d.steer.mcpInputGate = gate
	d.sm.Unlock()
}

// A response racing engine replacement cannot describe the replacement process.
func (d *claudeCodeDriver) currentMCPProbe(p *ccProc, probe mcpProbe) (mcpProbe, error) {
	d.sm.Lock()
	defer d.sm.Unlock()
	if d.steer.closed || d.steer.proc != p || p.dying || p.stdinClosed {
		return mcpProbe{Generation: claudeMCPGeneration(p.gen)}, errors.New("claude MCP process changed")
	}
	return probe, nil
}

func claudeMCPGeneration(gen int) uint64 {
	if gen < 0 {
		return 0
	}
	return uint64(gen)
}

// ccMCPAuthTimeout bounds mcp_authenticate: the engine discovers the server's authorization
// server (and may register a client) before it answers with the URL.
var ccMCPAuthTimeout = 30 * time.Second

// ccMCPAuthReply is the part of Claude Code's mcp_authenticate answer Blerg uses. state and
// callbackPort are deliberately not decoded: the engine keeps its own flow.
type ccMCPAuthReply struct {
	AuthURL            string `json:"authUrl"`
	RequiresUserAction bool   `json:"requiresUserAction"`
	CallbackExpected   bool   `json:"callbackExpected"`
}

// mcpAuthenticate sends Claude Code's own stream-json control request mcp_authenticate (the SDK's
// mcpAuthenticate) for one server: the engine starts its OAuth flow without opening a browser,
// listens for the callback on localhost and answers with the authorization URL. The token it gets
// stays in the engine's own credential store; the engine reconnects the server itself.
func (d *claudeCodeDriver) mcpAuthenticate(ctx context.Context, server string) (ccMCPAuthReply, bool, error) {
	d.sm.Lock()
	legacy, closed := d.steer.legacy, d.steer.closed
	d.sm.Unlock()
	if legacy || closed {
		return ccMCPAuthReply{}, false, errors.New("claude control requests are unavailable")
	}
	p, err := d.ensureProc(ctx)
	if err != nil {
		return ccMCPAuthReply{}, false, errors.New("claude process preparation failed")
	}
	rpcCtx, cancel := context.WithTimeout(ctx, ccMCPAuthTimeout)
	defer cancel()
	id := ccMCPRequestPrefix + ccUUID()
	response := make(chan ccMCPReply, 1)
	d.sm.Lock()
	if d.steer.closed || d.steer.proc != p || p.dying || p.stdinClosed {
		d.sm.Unlock()
		return ccMCPAuthReply{}, false, errors.New("claude process changed")
	}
	if len(p.mcpWait) >= 8 {
		d.sm.Unlock()
		return ccMCPAuthReply{}, false, errors.New("claude MCP query capacity reached")
	}
	if p.mcpWait == nil {
		p.mcpWait = make(map[string]chan ccMCPReply)
	}
	p.mcpWait[id] = response
	d.sm.Unlock()
	defer func() { d.sm.Lock(); delete(p.mcpWait, id); d.sm.Unlock() }()
	line, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": id,
		"request": map[string]any{"subtype": "mcp_authenticate", "serverName": server}})
	select {
	case p.writeCh <- append(line, '\n'):
	case <-p.done:
		return ccMCPAuthReply{}, false, errors.New("claude process exited")
	case <-rpcCtx.Done():
		return ccMCPAuthReply{}, false, rpcCtx.Err()
	}
	select {
	case reply := <-response:
		if !reply.ok {
			// The engine's error text is not passed on: it can name paths and endpoints.
			return ccMCPAuthReply{}, false, nil
		}
		var out ccMCPAuthReply
		if len(reply.body) > 64<<10 || json.Unmarshal(reply.body, &out) != nil {
			return ccMCPAuthReply{}, false, errors.New("claude mcp_authenticate reply invalid")
		}
		return out, true, nil
	case <-p.done:
		return ccMCPAuthReply{}, false, errors.New("claude process exited")
	case <-rpcCtx.Done():
		return ccMCPAuthReply{}, false, rpcCtx.Err()
	}
}
