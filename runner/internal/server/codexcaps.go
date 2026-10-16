package server

import "github.com/blerglab/blerg-ai/contracts/pluginspec"

// CanCodexRestricted is explicit opt-in: a daemon that only knows Claude's
// restrict_tools capability cannot safely host Codex grant or cron sessions.
func (dc *DaemonConn) CanCodexRestricted() bool {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return dc.codexRestricted
}

func (dc *DaemonConn) SetCodexRestricted(v bool) {
	dc.liveMu.Lock()
	dc.codexRestricted = v
	dc.liveMu.Unlock()
}

func (dc *DaemonConn) SetPluginEngines(engines []string) {
	dc.liveMu.Lock()
	dc.pluginEngines = append([]string(nil), engines...)
	dc.liveMu.Unlock()
}

func (dc *DaemonConn) PluginEngines() []string {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	if len(dc.pluginEngines) == 0 && dc.plugins {
		return []string{pluginspec.EngineClaude}
	}
	return append([]string(nil), dc.pluginEngines...)
}

func (dc *DaemonConn) CanPluginsFor(engine string) bool {
	if engine == "" {
		engine = pluginspec.EngineClaude
	}
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	if !dc.plugins {
		return false
	}
	if len(dc.pluginEngines) == 0 {
		return engine == pluginspec.EngineClaude
	}
	for _, supported := range dc.pluginEngines {
		if supported == engine && pluginspec.SupportsEngine(engine) {
			return true
		}
	}
	return false
}
