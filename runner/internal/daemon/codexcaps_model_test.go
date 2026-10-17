package daemon

import (
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestCodexCapabilitiesFollowEffectiveModel(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		name := "native"
		if restricted {
			name = "restricted"
		}
		t.Run(name, func(t *testing.T) {
			em := &collectEmitter{}
			d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Model: "gpt-5-codex", Emitter: em})
			if restricted {
				d.RestrictedRuntime = &codexRestrictedRuntime{Restricted: true}
			} else {
				d.runtimeCaps = &protocol.CapabilitiesPayload{Engine: "codex", Model: "gpt-5-codex"}
			}
			d.SetModel("gpt-6.1-sol", "medium", "ui")
			em.mu.Lock()
			defer em.mu.Unlock()
			seen := false
			for _, ev := range em.events {
				if p, ok := ev.Payload.(protocol.CapabilitiesPayload); ok {
					seen = true
					if p.Model != "gpt-6.1-sol" {
						t.Fatalf("capabilities model %q is stale", p.Model)
					}
				}
			}
			if !seen {
				t.Fatal("successful model change did not refresh capabilities")
			}
		})
	}
}
