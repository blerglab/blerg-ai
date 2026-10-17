package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func (d *codexDriver) SetMCPInputGate(gate func(context.Context)) {
	d.mu.Lock()
	d.mcpInputGate = gate
	d.mu.Unlock()
}

func (d *codexDriver) MCPGeneration() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mcpGeneration
}

// MCPStatus uses the installed CLI's native status RPC, never an agent prompt.
// A complete list is authoritative even while individual servers are pending.
func (d *codexDriver) MCPStatus(parent context.Context) (mcpProbe, error) {
	if err := parent.Err(); err != nil {
		return mcpProbe{Generation: d.MCPGeneration()}, err
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	d.mu.Lock()
	a, generation, runtime := d.app, d.mcpGeneration, d.RestrictedRuntime
	initialized, threadReady := a != nil && a.mcpInitialized, a != nil && a.mcpThreadReady
	thread := ""
	if threadReady {
		thread = d.threadID
	}
	d.mu.Unlock()
	probe := mcpProbe{Generation: generation}
	if runtime != nil {
		probe.Ready = true
		if runtime.bridge != nil {
			counts := map[string]int{}
			for _, name := range runtime.bridge.servers {
				counts[name] = 0
			}
			for _, route := range runtime.bridge.routes {
				counts[route.server]++
			}
			if len(counts) > 256 {
				return probeWithoutInventory(generation, errors.New("codex MCP inventory exceeds safety limit"))
			}
			status := "pending"
			if threadReady {
				status = "connected"
			}
			for name, count := range counts {
				probe.Servers = append(probe.Servers, protocol.MCPServerStatus{Name: name, Status: status, Source: "gateway", Tools: count})
			}
		}
	} else {
		if !initialized {
			return probe, nil
		}
		cursor := ""
		seen := map[string]bool{}
		names := map[string]bool{}
		for page := 0; ; page++ {
			if page >= 20 {
				return probeWithoutInventory(generation, errors.New("codex MCP pagination exceeds safety limit"))
			}
			params := map[string]any{"limit": 100, "detail": "toolsAndAuthOnly"}
			if thread != "" {
				params["threadId"] = thread
			}
			if cursor != "" {
				params["cursor"] = cursor
			}
			raw, err := a.callContext(ctx, "mcpServerStatus/list", params)
			if err != nil {
				return probeWithoutInventory(generation, errors.New("codex MCP status unavailable"))
			}
			var result struct {
				Data []struct {
					Name          string                     `json:"name"`
					RuntimeStatus string                     `json:"runtimeStatus"`
					PluginID      string                     `json:"pluginId"`
					Tools         map[string]json.RawMessage `json:"tools"`
				} `json:"data"`
				NextCursor string `json:"nextCursor"`
			}
			if json.Unmarshal(raw, &result) != nil || result.Data == nil {
				return probeWithoutInventory(generation, errors.New("codex MCP status invalid"))
			}
			for _, server := range result.Data {
				if server.Name == "" || names[server.Name] || len(names) >= 256 {
					return probeWithoutInventory(generation, errors.New("codex MCP inventory invalid or exceeds safety limit"))
				}
				names[server.Name] = true
				source := "codex"
				if server.PluginID != "" {
					source = "plugin"
				}
				status := codexMCPConnectionStatus(server.RuntimeStatus)
				if !threadReady && status == "connected" {
					// Global discovery identifies configured servers early, but it
					// cannot prove the new thread's own connections are ready.
					status = "pending"
				}
				probe.Servers = append(probe.Servers, protocol.MCPServerStatus{Name: server.Name, Status: status, Source: source, Tools: len(server.Tools)})
			}
			if result.NextCursor == "" {
				break
			}
			if seen[result.NextCursor] {
				return probeWithoutInventory(generation, errors.New("codex MCP pagination repeated"))
			}
			seen[result.NextCursor] = true
			cursor = result.NextCursor
		}
		// The pre-thread list is useful for showing configured names, but even
		// an empty global inventory cannot settle a thread not yet initialized.
		probe.Ready = threadReady
	}
	d.mu.Lock()
	current := d.mcpGeneration
	same := d.app == a && current == generation
	d.mu.Unlock()
	if !same {
		return probeWithoutInventory(current, errors.New("codex MCP process restarted"))
	}
	public := protocol.SanitizeMCPStatus(protocol.MCPStatusPayload{Servers: probe.Servers}).Servers
	if len(public) != len(probe.Servers) {
		return probeWithoutInventory(generation, errors.New("codex MCP inventory contains invalid names"))
	}
	probe.Servers = public
	sort.Slice(probe.Servers, func(i, j int) bool { return probe.Servers[i].Name < probe.Servers[j].Name })
	return probe, nil
}

func probeWithoutInventory(generation uint64, err error) (mcpProbe, error) {
	return mcpProbe{Generation: generation}, err
}

func codexMCPConnectionStatus(status string) string {
	switch status {
	case "notStarted", "starting":
		return "pending"
	case "connected":
		return "connected"
	case "authenticationRequired":
		return "needs-auth"
	case "failed", "cancelled":
		return "failed"
	case "disabled":
		return "disabled"
	default:
		return "unknown"
	}
}
