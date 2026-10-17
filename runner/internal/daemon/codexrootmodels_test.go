package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestCodexRootRerouteRefreshesSettingsAndCapabilities(t *testing.T) {
	for _, mode := range []string{"reroute", "settings"} {
		t.Run(mode, func(t *testing.T) {
			bin := fakeCodex(t)
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_ROOT_REROUTE", mode)
			em := &collectEmitter{}
			d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stopDriver := startTestCodexDriver(ctx, t, d)
			defer stopDriver()
			d.Enqueue("Review fixture", "chat")
			want := "fixture-root-rerouted"
			if mode == "settings" {
				want = "fixture-root-settings"
			}
			// Capability discovery is asynchronous and may first finish after
			// the short synthetic turn. Wait for its observable snapshot too.
			waitUntil(t, "rerouted root metadata settled", func() bool {
				if countKind(em, "turn_done") != 1 {
					return false
				}
				em.mu.Lock()
				defer em.mu.Unlock()
				for _, ev := range em.events {
					if p, ok := ev.Payload.(protocol.CapabilitiesPayload); ok && p.Model == want {
						return true
					}
				}
				return false
			})
			d.mu.Lock()
			got := d.model
			d.mu.Unlock()
			if got != want {
				t.Fatalf("settings model=%q want=%q", got, want)
			}
			em.mu.Lock()
			defer em.mu.Unlock()
			changed, caps, done := "", "", ""
			for _, ev := range em.events {
				switch p := ev.Payload.(type) {
				case agent.ModelChangedPayload:
					changed = p.Model
				case protocol.CapabilitiesPayload:
					caps = p.Model
				case agent.TurnDonePayload:
					done = p.Model
				}
			}
			if changed != want || caps != want || done != want {
				t.Fatalf("effective metadata differs: changed=%q caps=%q done=%q want=%q", changed, caps, done, want)
			}
		})
	}
}

func TestCodexRootReroutePreservesManualNextTurnSelection(t *testing.T) {
	for _, manual := range []struct{ name, model, effort string }{{"model", "fixture-manual-next", "medium"}, {"effort", "", "medium"}, {"same-value", "gpt-5-codex", "high"}, {"aba", "gpt-5-codex", "high"}} {
		t.Run(manual.name, func(t *testing.T) {
			bin := fakeCodex(t)
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_ROOT_REROUTE", "settings")
			gate := filepath.Join(t.TempDir(), "continue")
			t.Setenv("FAKE_CODEX_ROOT_REROUTE_GATE", gate)
			em := &collectEmitter{}
			d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stopDriver := startTestCodexDriver(ctx, t, d)
			defer stopDriver()
			d.Enqueue("Review fixture", "chat")
			waitUntil(t, "fixture awaiting manual selection", func() bool {
				em.mu.Lock()
				defer em.mu.Unlock()
				for _, ev := range em.events {
					if p, ok := ev.Payload.(agent.AssistantTextPayload); ok && p.Text == "Fixture waiting before reroute" {
						return true
					}
				}
				return false
			})
			if manual.name == "aba" {
				d.SetModel("fixture-other-next", "medium", "ui")
			}
			d.SetModel(manual.model, manual.effort, "ui")
			if err := os.WriteFile(gate, nil, 0600); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, "rerouted root completed", func() bool { return countKind(em, "turn_done") == 1 })
			wantModel := manual.model
			if wantModel == "" {
				wantModel = "gpt-5-codex"
			}
			d.mu.Lock()
			gotModel, gotEffort := d.model, d.effort
			d.mu.Unlock()
			if gotModel != wantModel || gotEffort != manual.effort {
				t.Fatalf("native report clobbered next turn: model=%q effort=%q", gotModel, gotEffort)
			}
			em.mu.Lock()
			defer em.mu.Unlock()
			changed := ""
			for _, ev := range em.events {
				if p, ok := ev.Payload.(agent.ModelChangedPayload); ok {
					changed = p.Model
				}
				if p, ok := ev.Payload.(agent.TurnDonePayload); ok && p.Model != "fixture-root-settings" {
					t.Fatalf("completed turn lost effective model: %q", p.Model)
				}
			}
			if changed != wantModel {
				t.Fatalf("native reports overwrote manual settings metadata: %q", changed)
			}
		})
	}
}

func TestCodexOmittedInitialSettingsRetainRequestedValues(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_OMIT_INITIAL_SETTINGS", "1")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Model: "fixture-requested", Effort: "medium", Emitter: em})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("Review fixture", "chat")
	waitUntil(t, "root completed", func() bool { return countKind(em, "turn_done") == 1 })
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.model != "fixture-requested" || d.effort != "medium" {
		t.Fatalf("omitted settings discarded selected values: %q/%q", d.model, d.effort)
	}
}

func TestCodexRootRerouteClearsKnownUnsupportedEffort(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_ROOT_REROUTE", "reroute")
	t.Setenv("FAKE_CODEX_MODELS", `{"data":[{"id":"fixture-root-rerouted","supportedReasoningEfforts":[]}],"nextCursor":null}`)
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Effort: "high", Emitter: em})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("Review fixture", "chat")
	waitUntil(t, "root completed", func() bool { return countKind(em, "turn_done") == 1 })
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.model != "fixture-root-rerouted" || d.effort != "" {
		t.Fatalf("reroute retained unsupported effort: %q/%q", d.model, d.effort)
	}
}

type blockingNativeSettingsEmitter struct {
	collectEmitter
	entered chan struct{}
	release chan struct{}
	kind    string
	once    sync.Once
}

func (e *blockingNativeSettingsEmitter) Emit(ev agent.Event) {
	model := ""
	switch p := ev.Payload.(type) {
	case agent.ModelChangedPayload:
		model = p.Model
	case protocol.CapabilitiesPayload:
		model = p.Model
	}
	if ev.Kind == e.kind && model == "fixture-native" {
		e.once.Do(func() { close(e.entered); <-e.release })
	}
	e.collectEmitter.Emit(ev)
}
func TestCodexNativeAndManualSettingsPublicationIsOrdered(t *testing.T) {
	for _, kind := range []string{"model_changed", "capabilities"} {
		t.Run(kind, func(t *testing.T) {
			em := &blockingNativeSettingsEmitter{entered: make(chan struct{}), release: make(chan struct{}), kind: kind}
			d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Model: "fixture-original", Effort: "high", Emitter: em})
			d.runtimeCaps = &protocol.CapabilitiesPayload{Engine: "codex", Model: "fixture-original"}
			nativeDone := make(chan struct{})
			go func() { d.observeNativeSettings(&codexSettingsFence{}, "fixture-native", "low"); close(nativeDone) }()
			<-em.entered
			manualDone := make(chan struct{})
			go func() { d.SetModel("fixture-manual", "medium", "ui"); close(manualDone) }()
			// Before serialization the newer manual event passes the blocked native
			// publisher. With serialization it must wait until native publication ends.
			select {
			case <-manualDone:
			case <-time.After(100 * time.Millisecond):
			}
			close(em.release)
			<-nativeDone
			<-manualDone
			em.mu.Lock()
			defer em.mu.Unlock()
			changed, caps := "", ""
			for _, ev := range em.events {
				switch p := ev.Payload.(type) {
				case agent.ModelChangedPayload:
					changed = p.Model
				case protocol.CapabilitiesPayload:
					caps = p.Model
				}
			}
			if changed != "fixture-manual" || caps != "fixture-manual" {
				t.Fatalf("older native publication replaced manual settings: changed=%q caps=%q", changed, caps)
			}
		})
	}
}
