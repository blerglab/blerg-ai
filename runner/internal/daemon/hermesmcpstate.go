package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"sort"
	"time"
)

func (d *hermesDriver) SetMCPInputGate(fn func(context.Context)) {
	d.mu.Lock()
	d.mcpInputGate = fn
	d.mu.Unlock()
}
func (d *hermesDriver) MCPGeneration() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mcpGeneration
}
func (d *hermesDriver) MCPStatus(parent context.Context) (mcpProbe, error) {
	d.mu.Lock()
	rpc, generation, ready := d.rpc, d.mcpGeneration, d.runtimeID != ""
	d.mu.Unlock()
	if err := parent.Err(); err != nil {
		return mcpProbe{Generation: generation}, err
	}
	if rpc == nil {
		return mcpProbe{Generation: generation}, nil
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	raw, err := rpc.call(ctx, "mcp.servers.status", map[string]any{})
	if err != nil {
		return mcpProbe{Generation: generation}, errors.New("hermes MCP status unavailable")
	}
	d.mu.Lock()
	same := rpc == d.rpc && generation == d.mcpGeneration
	current := d.mcpGeneration
	d.mu.Unlock()
	if !same {
		return mcpProbe{Generation: current}, errors.New("hermes gateway restarted")
	}
	return hermesMCPRows(raw, generation, ready)
}
func hermesMCPRows(raw json.RawMessage, generation uint64, ready bool) (mcpProbe, error) {
	probe := mcpProbe{Generation: generation}
	var p struct {
		Servers []struct {
			Name     string `json:"name"`
			Status   string `json:"status"`
			Source   string `json:"source"`
			Tools    int    `json:"tools"`
			Disabled bool   `json:"disabled"`
		}
	}
	if len(raw) > 1<<20 || json.Unmarshal(raw, &p) != nil || p.Servers == nil || len(p.Servers) > 256 {
		return probe, errors.New("hermes MCP inventory invalid")
	}
	seen := map[string]bool{}
	for _, s := range p.Servers {
		if s.Name == "" || seen[s.Name] {
			return mcpProbe{Generation: generation}, errors.New("hermes MCP inventory invalid")
		}
		seen[s.Name] = true
		status := "unknown"
		switch s.Status {
		case "connected", "failed", "lazy", "disabled":
			status = s.Status
		case "connecting", "configured":
			status = "pending"
		}
		if s.Disabled {
			status = "disabled"
		}
		source := "hermes"
		if s.Source == "plugin" {
			source = "plugin"
		}
		probe.Servers = append(probe.Servers, protocol.MCPServerStatus{Name: s.Name, Status: status, Source: source, Tools: s.Tools})
	}
	public := protocol.SanitizeMCPStatus(protocol.MCPStatusPayload{Servers: probe.Servers}).Servers
	if len(public) != len(probe.Servers) {
		return mcpProbe{Generation: generation}, errors.New("hermes MCP inventory invalid")
	}
	probe.Servers = public
	probe.Ready = ready
	sort.Slice(probe.Servers, func(i, j int) bool { return probe.Servers[i].Name < probe.Servers[j].Name })
	return probe, nil
}
