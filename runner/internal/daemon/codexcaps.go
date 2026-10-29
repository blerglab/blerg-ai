package daemon

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Native inventory changes may arrive in bursts while MCP servers start.
// At most one worker reads inventory; a burst schedules one further snapshot.
// The app loop never waits on its own RPC responses.
func (a *codexApp) requestCapabilities() {
	a.mu.Lock()
	if a.capRefreshing {
		a.capRefreshAgain = true
		a.mu.Unlock()
		return
	}
	a.capRefreshing = true
	a.mu.Unlock()
	go func() {
		for {
			a.d.mu.Lock()
			model := a.d.model
			a.d.mu.Unlock()
			a.capabilities(model)
			a.mu.Lock()
			again := a.capRefreshAgain && a.ctx.Err() == nil
			a.capRefreshAgain = false
			if !again {
				a.capRefreshing = false
			}
			a.mu.Unlock()
			if !again {
				return
			}
		}
	}()
}

// Inventory endpoints return much more than display data. Decode only the
// whitelisted metadata below; never include config, paths, headers or URLs.
func (a *codexApp) capabilities(model string) {
	p := protocol.CapabilitiesPayload{ToolSchemaRefresh: protocol.ToolSchemaRefreshNewSession, Engine: "codex", EngineName: "Codex", Model: model, PermissionMode: "never", SandboxMode: "danger-full-access", Note: "Reported by the running Codex app-server. Skill and plugin inventory shows availability; tools appear as they are used."}
	if raw, err := a.call("skills/list", map[string]any{"cwds": []string{a.d.runtimeWorkDir()}}); err == nil {
		var r struct {
			Data []struct {
				Skills []struct {
					Name, Description, Scope, Path string
					Enabled                        bool
				}
			}
		}
		if json.Unmarshal(raw, &r) == nil {
			g := protocol.CapabilityGroup{ID: protocol.CapGroupSkills, Label: "Skills", Items: []protocol.CapabilityItem{}}
			for _, e := range r.Data {
				for _, s := range e.Skills {
					status := "disabled"
					if s.Enabled {
						status = "enabled"

					}
					g.Items = append(g.Items, protocol.CapabilityItem{Name: s.Name, Description: s.Description, Source: s.Scope, Status: status})
				}
			}
			p.Groups = append(p.Groups, g)
		}
	}
	if raw, err := a.call("plugin/installed", map[string]any{"cwds": []string{a.d.runtimeWorkDir()}}); err == nil {
		var r struct {
			Marketplaces []struct {
				Plugins []struct {
					Name, Version      string
					Installed, Enabled bool
				}
			}
		}
		if json.Unmarshal(raw, &r) == nil {
			g := protocol.CapabilityGroup{ID: protocol.CapGroupPlugins, Label: "Plugins", Items: []protocol.CapabilityItem{}}
			for _, m := range r.Marketplaces {
				for _, s := range m.Plugins {
					if !s.Installed {
						continue
					}
					status := "disabled"
					if s.Enabled {
						status = "enabled"

					}
					g.Items = append(g.Items, protocol.CapabilityItem{Name: s.Name, Detail: s.Version, Status: status})
				}
			}
			p.Groups = append(p.Groups, g)
		}
	}
	g := protocol.CapabilityGroup{ID: protocol.CapGroupMCP, Label: "MCP servers", Items: []protocol.CapabilityItem{}}
	cursor := ""
	complete := false
	for page := 0; page < 20; page++ {
		params := map[string]any{"threadId": a.threadID(), "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := a.call("mcpServerStatus/list", params)
		if err != nil {
			break
		}
		var r struct {
			Data []struct {
				Name, RuntimeStatus string
				Tools               map[string]json.RawMessage
			}
			NextCursor string
		}
		if json.Unmarshal(raw, &r) != nil || r.Data == nil {
			break
		}
		for _, s := range r.Data {
			status := s.RuntimeStatus
			if status == "authenticationRequired" {
				status = "needs-auth"
			}
			g.Items = append(g.Items, protocol.CapabilityItem{Name: s.Name, Status: status, Detail: fmt.Sprintf("%d tools", len(s.Tools))})
		}
		if r.NextCursor == "" {
			complete = true
			break
		}
		if r.NextCursor == cursor {
			break
		}
		cursor = r.NextCursor
	}
	if !complete {
		// A failed or partial read cannot establish an authoritative inventory.
		g.Items = nil
	}
	bridgeReported := a.d.RestrictedRuntime != nil && a.d.RestrictedRuntime.bridge != nil
	if bridgeReported {
		g.Items = append(g.Items, a.d.RestrictedRuntime.bridge.capabilityItems()...)
		g.Note = "Includes the explicitly granted gateway tools for this session."
		if !complete {
			g.Note = "Explicitly granted gateway tools only; native MCP inventory is unavailable."
		}
	}
	if a.d.RestrictedRuntime != nil && a.d.RestrictedRuntime.filing != nil {
		g.Items = append(g.Items, protocol.CapabilityItem{Name: sessionFilingMCPName, Status: "connected", Source: "Blerg session", Detail: fmt.Sprintf("%d approval-gated session tools", len(sessionBuiltinTools))})
		bridgeReported = true
	}
	if complete || bridgeReported {
		p.Groups = append(p.Groups, g)
	}
	for i := range p.Groups {
		sort.Slice(p.Groups[i].Items, func(j, k int) bool { return p.Groups[i].Items[j].Name < p.Groups[i].Items[k].Name })
	}
	a.d.mu.Lock()
	a.d.runtimeCaps = &p
	a.d.mu.Unlock()
	a.d.emitRuntimeCaps()
}

// A null thread effort means the model's default, not an invented fixed
// effort. Resolve that default from this runtime's own model catalog.
func (a *codexApp) defaultEffort(model string) string {
	a.d.mu.Lock()
	defer a.d.mu.Unlock()
	return a.d.modelCatalog[model].DefaultEffort
}

func (d *codexDriver) observeTool(name string) {
	d.mu.Lock()
	if d.observedTools == nil {
		d.observedTools = map[string]bool{}
	}
	changed := !d.observedTools[name]
	d.observedTools[name] = true
	d.mu.Unlock()
	if changed {
		d.emitRuntimeCaps()
	}
}
func (d *codexDriver) emitRuntimeCaps() {
	d.settingsMu.Lock()
	defer d.settingsMu.Unlock()
	d.emitRuntimeCapsLocked()
}

// emitRuntimeCapsLocked publishes a snapshot while settingsMu prevents a newer
// settings mutation/publication from overtaking it. Driver mu is released before Emit.
func (d *codexDriver) emitRuntimeCapsLocked() {
	d.mu.Lock()
	if d.codexToolsRestricted() {
		p := d.RestrictedRuntime.capabilities(d.model)
		d.mu.Unlock()
		d.caps.emit(d.emitter, p)
		return
	}
	if d.runtimeCaps == nil {
		d.mu.Unlock()
		return
	}
	p := *d.runtimeCaps
	p.Model = d.model
	p.Groups = append([]protocol.CapabilityGroup(nil), p.Groups...)
	g := protocol.CapabilityGroup{ID: protocol.CapGroupTools, Label: "Tools", Items: []protocol.CapabilityItem{}, Note: "Tools observed in this session; the app-server does not enumerate all built-in tools."}
	for name := range d.observedTools {
		g.Items = append(g.Items, protocol.CapabilityItem{Name: name})
	}
	sort.Slice(g.Items, func(i, j int) bool { return g.Items[i].Name < g.Items[j].Name })
	p.Groups = append(p.Groups, g)
	d.mu.Unlock()
	d.caps.emit(d.emitter, p)
}
