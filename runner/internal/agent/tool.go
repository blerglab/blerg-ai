package agent

import (
	"context"
	"encoding/json"
)

// Tool is one callable tool exposed to the model. Mutating tools are
// serialized by the loop and gated by check-in enforcement.
type Tool interface {
	Def() ToolDef
	Mutating() bool
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}

// Registry is an ordered set of tools keyed by name.
type Registry struct {
	order  []string
	byName map[string]Tool
}

// NewRegistry builds a registry from tools; duplicate names keep the last
// tool but the original position.
func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{byName: make(map[string]Tool)}
	for _, t := range tools {
		name := t.Def().Name
		if _, dup := r.byName[name]; !dup {
			r.order = append(r.order, name)
		}
		r.byName[name] = t
	}
	return r
}

func (r *Registry) Get(name string) (Tool, bool) { t, ok := r.byName[name]; return t, ok }

// Defs returns tool definitions in registration order.
func (r *Registry) Defs() []ToolDef {
	defs := make([]ToolDef, 0, len(r.order))
	for _, n := range r.order {
		defs = append(defs, r.byName[n].Def())
	}
	return defs
}

// Without returns a copy of the registry with the named tools removed.
// Used to build subagent registries (children get no agent/ask tools).
func (r *Registry) Without(names ...string) *Registry {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	out := &Registry{byName: make(map[string]Tool)}
	for _, n := range r.order {
		if drop[n] {
			continue
		}
		out.order = append(out.order, n)
		out.byName[n] = r.byName[n]
	}
	return out
}
