package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestMarketplacePolicyDaemonEnforcesUpdatesWithoutUninstall(t *testing.T) {
	w, _ := newTestWorkshop(t)
	old, res := w.prepare(t.Context(), "old", []pluginspec.Entry{wsEntry("existing")})
	if res.Installed != 1 {
		t.Fatal("fixture", res)
	}
	defaults, _ := pluginspec.ParseAllowlist("")
	p := pluginspec.PolicyFromAllowlist(defaults)
	for i := range p.Marketplaces {
		p.Marketplaces[i].Enabled = false
	}
	receive := func(p *pluginspec.Policy) {
		raw, _ := json.Marshal(protocol.PluginMarketplacePolicy{Type: "plugin_marketplace_policy", Policy: p, LeaseSeconds: 15})
		w.ReceivePolicy(raw, "")
	}
	receive(&p)
	if w.currentAllow().Allows(wsOfficial) {
		t.Fatal("daemon ignored Core disable")
	}
	_, res = w.prepare(t.Context(), "new", []pluginspec.Entry{wsEntry("existing")})
	if res.Installed != 0 || len(res.Skipped) != 1 {
		t.Fatal("disabled source installed", res)
	}
	if _, err := os.Stat(filepath.Join(old[0], ".claude-plugin", "plugin.json")); err != nil {
		t.Fatal("policy silently removed running snapshot", err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{Plugins: w})
	host.sessions["session"] = &agentSession{loop: newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)}
	host.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "disabled", SessionID: "session", Plugins: []pluginspec.Entry{wsEntry("selected")}})
	pluginResultFor(t, sender, "disabled", "failed")
	// Codex startup checks the same local authority before any installer command.
	_, _, res = w.prepareCodex(t.Context(), "codex", []pluginspec.Entry{{Marketplace: "codex:openai-curated-remote", Plugin: "selected"}}, t.TempDir())
	if res.Installed != 0 || len(res.Skipped) != 1 {
		t.Fatal("native startup bypassed Core disable", res)
	}
	p.Revision++
	for i := range p.Marketplaces {
		p.Marketplaces[i].Enabled = true
	}
	receive(&p)
	_, res = w.prepare(t.Context(), "enabled", []pluginspec.Entry{wsEntry("selected")})
	if res.Installed != 1 {
		t.Fatal("toggle required daemon restart", res)
	}
	receive(nil)
	if !w.currentAllow().Allows(wsOfficial) {
		t.Fatal("daemon lost last applied policy")
	}
	// Concurrent reads/install-option snapshots and policy delivery must be race-free.
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				receive(&p)
				_ = w.currentAllow().Allows(wsOfficial)
				_ = w.options()
			}
		})
	}
	wg.Wait()
}

func TestMarketplacePolicyDefaultAllowsNativeCodexHostInstall(t *testing.T) {
	h, d, sender, em, _ := codexMarketplaceHost(t)
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(raw), "selected@market", "selected@openai-curated-remote")
	script = strings.ReplaceAll(script, `"market"`, `"openai-curated-remote"`)
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	defaults, _ := pluginspec.ParseAllowlist("")
	p := pluginspec.PolicyFromAllowlist(defaults)
	frame, _ := json.Marshal(protocol.PluginMarketplacePolicy{Type: "plugin_marketplace_policy", Policy: &p, LeaseSeconds: 15})
	h.cfg.Plugins.ReceivePolicy(frame, "")
	stop := startTestCodexDriver(t.Context(), t, d)
	defer stop()
	d.Enqueue("before", "chat")
	waitUntil(t, "first turn", func() bool { return countKind(em, "turn_done") == 1 })
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{RequestID: "official-default", SessionID: "session", Plugins: []pluginspec.Entry{{Marketplace: "codex:openai-curated-remote", Plugin: "selected"}}})
	waitCodexPluginResult(t, sender, "official-default", "applied")
}

func TestMarketplacePolicyDaemonRejectsReconnectReplayAndOutOfOrder(t *testing.T) {
	w, _ := newTestWorkshop(t)
	fallback, _ := pluginspec.ParseAllowlist("*")
	enabled := pluginspec.PolicyFromAllowlist(fallback)
	disabled := pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{})
	disabled.Revision = 3
	receive := func(p *pluginspec.Policy) {
		raw, _ := json.Marshal(protocol.PluginMarketplacePolicy{Type: "plugin_marketplace_policy", Policy: p, LeaseSeconds: 15})
		w.ReceivePolicy(raw, "")
	}
	receive(&disabled)
	for _, revision := range []int64{1, 3, 2} {
		enabled.Revision = revision
		receive(&enabled)
		if w.currentAllow().Allows(wsOfficial) {
			t.Fatalf("reconnect revision %d restored revoked permission", revision)
		}
	}
	receive(nil)
	if w.currentAllow().Allows(wsOfficial) {
		t.Fatal("Core outage restored revoked permission")
	}
	restarted := newPluginWorkshop(w.dir, w.home, fallback)
	for _, revision := range []int64{1, 3, 2} {
		enabled.Revision = revision
		raw, _ := json.Marshal(protocol.PluginMarketplacePolicy{Type: "plugin_marketplace_policy", Policy: &enabled, LeaseSeconds: 15})
		restarted.ReceivePolicy(raw, "")
		if restarted.currentAllow().Allows(wsOfficial) {
			t.Fatal("restart/reconnect restored revoked permission", revision)
		}
	}
}

func TestPodWorkshopRequiresBootstrapAfterReplacement(t *testing.T) {
	t.Setenv("BLERG_RUNNER_PLUGIN_MARKETPLACES", "*")
	allow, _ := pluginspec.ParseAllowlist("*")
	enabled := pluginspec.PolicyFromAllowlist(allow)
	disabled := pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{})
	disabled.Revision = 3
	receive := func(w *pluginWorkshop, p pluginspec.Policy) {
		raw, _ := json.Marshal(protocol.PluginMarketplacePolicy{Type: "plugin_marketplace_policy", SessionID: "pod", Policy: &p, LeaseSeconds: 15})
		w.ReceivePolicy(raw, "pod")
	}
	original := NewPodPluginWorkshop(t.TempDir())
	if err := original.BootstrapPolicy(enabled); err != nil {
		t.Fatal(err)
	}
	receive(original, disabled)
	if original.CurrentAllow().Allows(wsOfficial) {
		t.Fatal("original pod ignored revoke")
	}
	// The generated pod retains only projects; policy HOME is a new emptyDir.
	for _, stateDir := range []string{t.TempDir(), filepath.Dir(filepath.Dir(original.dir))} {
		replacement := NewPodPluginWorkshop(stateDir)
		if replacement.CurrentAllow().Allows(wsOfficial) {
			t.Fatal("unbootstrapped replacement used environment or cached policy")
		}
		receive(replacement, enabled)
		if replacement.CurrentAllow().Allows(wsOfficial) {
			t.Fatal("stale replica established replacement authority")
		}
		replacement.run = func(context.Context, string, ...string) (string, error) {
			t.Error("unbootstrapped pod ran plugin command")
			return "", nil
		}
		_, res := replacement.prepare(t.Context(), "replacement", []pluginspec.Entry{wsEntry("selected")})
		if res.Installed != 0 || len(res.Skipped) != 1 {
			t.Fatal("unbootstrapped workshop allowed startup/live installation", res)
		}
		_, _, res = replacement.prepareCodex(t.Context(), "replacement-codex", []pluginspec.Entry{{Marketplace: "codex:openai-curated", Plugin: "selected"}}, t.TempDir())
		if res.Installed != 0 || len(res.Skipped) != 1 {
			t.Fatal("unbootstrapped workshop allowed Codex installation", res)
		}
		if err := replacement.BootstrapPolicy(disabled); err != nil {
			t.Fatal(err)
		}
		receive(replacement, enabled)
		if replacement.CurrentAllow().Allows(wsOfficial) {
			t.Fatal("replacement lost bootstrapped revision floor")
		}
		enabled.Revision = 4
		receive(replacement, enabled)
		if !replacement.CurrentAllow().Allows(wsOfficial) {
			t.Fatal("replacement refused newer enabling policy")
		}
		enabled.Revision = 1
	}
}

func TestPodBootstrapCannotTrustConflictingCachedAuthority(t *testing.T) {
	for _, mode := range []string{"same", "conflicting", "newer-local"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			cached := NewPluginWorkshop(dir)
			p := pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{})
			p.Revision = 3
			current := p
			if mode != "same" {
				allow, _ := pluginspec.ParseAllowlist("*")
				p = pluginspec.PolicyFromAllowlist(allow)
				p.Revision = 3
			}
			if mode == "newer-local" {
				p.Revision = 4
			}
			if err := cached.policy.Apply(p, 15*time.Second); err != nil {
				t.Fatal(err)
			}
			pod := NewPodPluginWorkshop(dir)
			err := pod.BootstrapPolicy(current)
			if (err == nil) != (mode == "same") {
				t.Fatal("bootstrap trusted a conflicting local policy", err)
			}
			if pod.CurrentAllow().Allows(wsOfficial) {
				t.Fatal("cached permissions bypassed current Core authority")
			}
		})
	}
}

func TestMarketplacePolicyHoldersRestoreThenRevoke(t *testing.T) {
	for _, pod := range []bool{false, true} {
		t.Run(map[bool]string{false: "desktop", true: "pod"}[pod], func(t *testing.T) {
			dir := t.TempDir()
			allow, _ := pluginspec.ParseAllowlist("*")
			old := pluginspec.PolicyFromAllowlist(allow)
			old.PolicyID, old.Revision = "00000000-0000-4000-8000-000000000001", 7
			cached := NewPluginWorkshop(dir)
			if err := cached.policy.Apply(old, time.Second); err != nil {
				t.Fatal(err)
			}
			revoke := pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{})
			revoke.PolicyID, revoke.Revision = "00000000-0000-4000-8000-000000000002", 4
			var holder *pluginWorkshop
			if pod {
				holder = NewPodPluginWorkshop(dir)
				if err := holder.BootstrapPolicy(revoke); err != nil {
					t.Fatal("restored Core blocked pod bootstrap", err)
				}
			} else {
				holder = NewPluginWorkshop(dir)
				raw, _ := json.Marshal(protocol.PluginMarketplacePolicy{Type: "plugin_marketplace_policy", Policy: &revoke, LeaseSeconds: 15})
				holder.ReceivePolicy(raw, "")
			}
			if holder.CurrentAllow().Allows(wsOfficial) {
				t.Fatal("restore froze revocation")
			}
			got, ok := holder.policy.Snapshot()
			if !ok || got.PolicyID != revoke.PolicyID || got.Revision != 4 {
				t.Fatal("restore lost policy identity", got)
			}
		})
	}
}

func TestMarketplacePolicyDaemonLogsPersistenceFailure(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	w, _ := newTestWorkshop(t)
	if err := os.MkdirAll(filepath.Join(w.dir, "marketplace-policy.json"), 0700); err != nil {
		t.Fatal(err)
	}
	p := pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{})
	raw, _ := json.Marshal(protocol.PluginMarketplacePolicy{Type: "plugin_marketplace_policy", Policy: &p, LeaseSeconds: 15})
	w.ReceivePolicy(raw, "")
	if !strings.Contains(output.String(), "marketplace policy rejected") {
		t.Fatal("silent daemon persistence failure", output.String())
	}
}
