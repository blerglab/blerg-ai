package api

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/core/internal/plugins"
	"net/http"
)

// This is static integration metadata, never login or target readiness. Plugin
// support additionally requires Core's actual validator, not a descriptor flag.
func (d Deps) handleEngineDescriptors(w http.ResponseWriter, _ *http.Request) {
	pluginEngines := []string{}
	for _, id := range plugins.EngineIDs() {
		if descriptor, ok := engines.Lookup(id); ok && descriptor.Plugins {
			pluginEngines = append(pluginEngines, id)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Engines       []engines.Descriptor `json:"engines"`
		PluginEngines []string             `json:"plugin_engines"`
	}{engines.All(), pluginEngines})
}
