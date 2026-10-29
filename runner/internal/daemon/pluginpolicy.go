package daemon

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func (w *pluginWorkshop) currentAllow() pluginspec.Allowlist {
	if w.requireBootstrap && !w.bootstrapped.Load() {
		return pluginspec.Allowlist{}
	}
	return w.policy.Allowlist(w.allow)
}

// NewPodPluginWorkshop denies installs until the current launch authenticates a
// fresh Core policy. Pod HOME and its local revision may disappear on replacement.
func NewPodPluginWorkshop(stateDir string) *pluginWorkshop {
	w := NewPluginWorkshop(stateDir)
	if w != nil {
		w.requireBootstrap = true
	}
	return w
}

// BootstrapPolicy receives fresh authority from the runtime-authenticated HTTP
// bootstrap, never a replica's cached socket frame. Persist before opening the
// gate; an existing revision must agree with current Core authority.
func (w *pluginWorkshop) BootstrapPolicy(p pluginspec.Policy) error {
	err := w.policy.Apply(p, 15*time.Second)
	if err != nil {
		if !errors.Is(err, pluginspec.ErrPolicyRevision) {
			w.policy.LogApplyError(p, err)
			return err
		}
		saved, ok := w.policy.Snapshot()
		savedSources, savedAny := saved.Allowlist().List()
		sources, anySource := p.Allowlist().List()
		if !ok || saved.PolicyID != p.PolicyID || saved.Revision != p.Revision || savedAny != anySource || !slices.Equal(savedSources, sources) {
			w.policy.LogApplyError(p, errors.New("marketplace policy bootstrap conflicts with saved authority"))
			return err
		}
	}
	w.bootstrapped.Store(true)
	return nil
}

func (m *Manager) handlePluginMarketplacePolicy(raw []byte) {
	if m.agents == nil || m.agents.cfg.Plugins == nil {
		return
	}
	m.agents.cfg.Plugins.ReceivePolicy(raw, "")
}

// CurrentAllow is shared by the pod startup installer and live workshop.
func (w *pluginWorkshop) CurrentAllow() pluginspec.Allowlist { return w.currentAllow() }

// ReceivePolicy validates the authenticated connection's session binding before
// applying an update. Desktop frames are unbound; pod frames must name that pod.
func (w *pluginWorkshop) ReceivePolicy(raw []byte, boundSession string) {
	if w.requireBootstrap && !w.bootstrapped.Load() {
		return
	}
	var msg protocol.PluginMarketplacePolicy
	if json.Unmarshal(raw, &msg) != nil || msg.Type != "plugin_marketplace_policy" || msg.SessionID != boundSession {
		w.policy.LogApplyError(pluginspec.Policy{}, errors.New("invalid marketplace policy frame or session binding"))
		return
	}
	if msg.Policy == nil {
		return
	}
	// Invalid, stale and replayed messages cannot change authority.
	err := w.policy.Apply(*msg.Policy, time.Duration(msg.LeaseSeconds)*time.Second)
	w.policy.LogApplyError(*msg.Policy, err)
}
