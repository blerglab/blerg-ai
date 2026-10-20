// Package engines describes coding-engine capabilities shared by Blerg components.
// Descriptors advertise implemented integration, not current installation, login,
// catalog availability or authority. Those are resolved for each launch target.
package engines

import (
	"slices"
	"sync"
)

type Descriptor struct {
	Steering         bool     `json:"steering"`
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Runtimes         []string `json:"runtimes"`
	Terminal         bool     `json:"terminal"`
	Restricted       bool     `json:"restricted"`
	Plugins          bool     `json:"plugins"`
	CredentialImport bool     `json:"credential_import"`
	LimitResume      bool     `json:"limit_resume"`
	UsageWindows     []string `json:"usage_windows,omitempty"`
	// CredentialEnvKeys is adapter wiring, never credential values. It is not
	// browser presentation or evidence that a credential is currently valid.
	CredentialEnvKeys []string `json:"-"`
}

func (d Descriptor) SupportsRuntime(runtime string) bool { return slices.Contains(d.Runtimes, runtime) }

var (
	mu       sync.RWMutex
	registry = map[string]Descriptor{
		"claude":   {ID: "claude", Steering: true, Name: "Claude Code", Runtimes: []string{"daemon", "docker", "cluster"}, Terminal: true, Restricted: true, Plugins: true, CredentialImport: true, LimitResume: true, UsageWindows: []string{"5h", "7d", "month", "day", "spend-week", "capped"}, CredentialEnvKeys: []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}},
		"codex":    {ID: "codex", Steering: true, Name: "Codex", Runtimes: []string{"daemon", "docker", "cluster"}, Terminal: true, Restricted: true, Plugins: true, CredentialImport: true, LimitResume: true, UsageWindows: []string{"5h", "7d", "capped"}, CredentialEnvKeys: []string{"CODEX_AUTH_JSON"}},
		"hermes":   {ID: "hermes", Steering: true, Name: "Hermes", Runtimes: []string{"daemon", "docker", "cluster"}, Terminal: true, CredentialImport: true, CredentialEnvKeys: []string{"HERMES_ENV_CONTENTS"}},
		"openclaw": {ID: "openclaw", Name: "OpenClaw", Runtimes: []string{"daemon"}, Terminal: true},
	}
)

func Normalize(id string) string {
	if id == "" {
		return "claude"
	}
	return id
}

func Lookup(id string) (Descriptor, bool) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := registry[Normalize(id)]
	d.UsageWindows = slices.Clone(d.UsageWindows)
	d.Runtimes = slices.Clone(d.Runtimes)
	d.CredentialEnvKeys = slices.Clone(d.CredentialEnvKeys)
	return d, ok
}

// All returns independent copies in deterministic presentation order.
func All() []Descriptor {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Descriptor, 0, len(registry))
	for _, d := range registry {
		d.UsageWindows = slices.Clone(d.UsageWindows)
		d.Runtimes = slices.Clone(d.Runtimes)
		d.CredentialEnvKeys = slices.Clone(d.CredentialEnvKeys)
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Descriptor) int {
		if a.ID == b.ID {
			return 0
		}
		if a.ID == "claude" {
			return -1
		}
		if b.ID == "claude" {
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		return 1
	})
	return out
}

// Register installs a component's implemented descriptor. The returned function
// restores the prior registration, allowing isolated synthetic-engine tests.
// Registration is trusted application wiring, never client-provided metadata.
func Register(d Descriptor) func() {
	mu.Lock()
	previous, existed := registry[d.ID]
	d.UsageWindows = slices.Clone(d.UsageWindows)
	d.Runtimes = slices.Clone(d.Runtimes)
	d.CredentialEnvKeys = slices.Clone(d.CredentialEnvKeys)
	registry[d.ID] = d
	mu.Unlock()
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if existed {
			registry[d.ID] = previous
		} else {
			delete(registry, d.ID)
		}
	}
}
