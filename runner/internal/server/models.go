package server

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// NewModelSources is the registry of per-engine model lists the server runs
// with — the one place a list is attached to an engine. claude is the Claude
// Code catalog (nil = the built-in list only). Every other engine with rules
// in the models table is served from what connected daemons reported for it
// (their EngineSpec.ListModels probes — codex and hermes today), so an engine
// that gains a daemon probe gets a picker with no change here. hub nil (tests
// that need no daemon) leaves those engines with no source.
func NewModelSources(claude models.Source, hub *Hub) *models.Registry {
	if claude == nil {
		claude = models.BuiltinClaudeSource
	}
	r := models.NewRegistry()
	r.Register(models.ClaudeEngine, claude)
	if hub != nil {
		for _, engine := range models.Engines() {
			if engine == models.ClaudeEngine {
				continue
			}
			r.Register(engine, models.SourceFunc(func(_ context.Context, daemonID string) (models.List, error) {
				return reportedModelList(hub, engine, daemonID), nil
			}))
		}
	}
	return r
}

// reportedModelList is an engine's daemon-reported list (source "daemon"):
//
//   - daemonID given: that daemon's list, or none when it is not connected or
//     reported nothing for the engine (never another daemon's list);
//   - daemonID empty and exactly one connected daemon reported a list: that
//     one, with its daemon_id;
//   - daemonID empty and several did: their union, daemons taken in daemon-id
//     order and the lowest id winning a model both list, with no daemon_id
//     and the oldest fetched_at among them.
//
// No list anywhere is source "none" with no models.
func reportedModelList(hub *Hub, engine, daemonID string) models.List {
	engine = models.NormalizeEngine(engine)
	none := models.List{Models: []models.Model{}, Source: models.SourceNone}
	if daemonID != "" {
		dc := hub.GetDaemon(daemonID)
		if dc == nil {
			return none
		}
		rep, ok := dc.EngineModels(engine)
		if !ok {
			return none
		}
		t := rep.FetchedAt
		return models.List{Models: rep.Models, Source: models.SourceDaemon, FetchedAt: &t, DaemonID: dc.ID}
	}
	daemons := hub.GetAllDaemons()
	sort.Slice(daemons, func(i, j int) bool { return daemons[i].ID < daemons[j].ID })
	type reported struct {
		id  string
		rep models.Report
	}
	var found []reported
	for _, dc := range daemons {
		if rep, ok := dc.EngineModels(engine); ok {
			found = append(found, reported{dc.ID, rep})
		}
	}
	switch len(found) {
	case 0:
		return none
	case 1:
		t := found[0].rep.FetchedAt
		return models.List{Models: found[0].rep.Models, Source: models.SourceDaemon, FetchedAt: &t, DaemonID: found[0].id}
	}
	out := []models.Model{}
	seen := map[string]bool{}
	oldest := found[0].rep.FetchedAt
	for _, f := range found {
		if f.rep.FetchedAt.Before(oldest) {
			oldest = f.rep.FetchedAt
		}
		for _, m := range f.rep.Models {
			if seen[m.ID] || len(out) >= models.MaxReportedModels {
				continue
			}
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	return models.List{Models: out, Source: models.SourceDaemon, FetchedAt: &oldest}
}

// SetModelSources wires the registry GET /api/models/{engine} serves and the
// per-model effort check reads. Without one, NewModelSources(nil, hub) is used.
func (a *API) SetModelSources(r *models.Registry) { a.modelSources = r }

func (a *API) modelRegistry() *models.Registry {
	if a.modelSources != nil {
		return a.modelSources
	}
	return NewModelSources(nil, a.hub)
}

// HandleGetModels is GET /api/models/{engine}: the models a session on that
// engine can be launched on, each with its effort levels and default. An
// engine with no registered source answers 200 with an empty list and
// source "none" — no picker, not an error — so adding an engine is
// registering a source, never a new route. ?daemon_id= is passed to the
// source for engines whose list is reported per daemon (see
// reportedModelList for how it picks without one). Readable by anyone
// who may start a session: a signed-in browser or a runner-contract
// credential.
func (a *API) HandleGetModels(w http.ResponseWriter, r *http.Request) {
	if !a.authBrowserOrRunner(w, r) {
		return
	}
	engine := r.PathValue("engine")
	list, err := a.modelRegistry().Models(r.Context(), engine, r.URL.Query().Get("daemon_id"))
	if err != nil {
		log.Printf("models %s: %v", engine, err)
		writeError(w, http.StatusBadGateway, "model list unavailable")
		return
	}
	if list.Models == nil {
		list.Models = []models.Model{}
	}
	list.EngineEfforts = models.EffortsFor(engine)
	if list.EngineEfforts == nil {
		list.EngineEfforts = []string{}
	}
	writeJSON(w, http.StatusOK, list)
}

// authBrowserOrRunner accepts either a browser session token or any
// credential authRunner accepts (the static runner key, or a core token with
// the runner capability). It writes 401 and returns false otherwise.
func (a *API) authBrowserOrRunner(w http.ResponseWriter, r *http.Request) bool {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if _, ok := a.verifyBrowserToken(raw); ok {
		return true
	}
	if a.runnerKey != "" && raw != "" && subtle.ConstantTimeCompare([]byte(raw), []byte(a.runnerKey)) == 1 {
		return true
	}
	if raw != "" && a.coreAuth != nil {
		if p, err := identity.Verify(raw, coreAuthAudience, a.coreAuth.KeySet(), a.coreAuth, coreAuthSensitiveCaps); err == nil && p.Has(coreAuthRunnerCap) {
			return true
		}
	}
	writeError(w, http.StatusUnauthorized, "unauthorized")
	return false
}

// modelEffortProblem returns why a requested model/effort cannot be used on
// engine, or "" when both are acceptable: the engine's rules first
// (modelRuleProblem), then — when the model is one the engine's list knows —
// that model's own effort levels. A model the list does not know (an alias
// like "sonnet", a free-typed name, or an engine with no list) gets the
// engine rules only. A daemon-reported list is per workstation, so it is
// consulted only when the session's daemon is known (daemonID) and it is
// that daemon's own list; otherwise the rules alone decide.
func (a *API) modelEffortProblem(ctx context.Context, engine, model, effort, daemonID string) string {
	if msg := modelRuleProblem(engine, model, effort); msg != "" || model == "" || effort == "" {
		return msg
	}
	list, err := a.modelRegistry().Models(ctx, engine, daemonID)
	if err != nil {
		return ""
	}
	if list.Source == models.SourceDaemon && (daemonID == "" || list.DaemonID != daemonID) {
		return ""
	}
	for _, m := range list.Models {
		if m.ID != model {
			continue
		}
		for _, e := range m.Efforts {
			if e == effort {
				return ""
			}
		}
		if len(m.Efforts) == 0 {
			return "model " + model + " takes no effort"
		}
		return "effort for model " + model + " must be one of " + strings.Join(m.Efforts, ", ")
	}
	return ""
}

// modelRuleProblem is the engine-rules half of modelEffortProblem. Both values
// become engine CLI arguments, so a value with whitespace, shell
// metacharacters or a leading dash never gets past it. The rules are the
// engine's own row in the shared internal/models table.
func modelRuleProblem(engine, model, effort string) string {
	if !models.ValidModelFor(engine, model) {
		if models.NormalizeEngine(engine) == models.ClaudeEngine {
			return "model must be a model name: lowercase letters, digits, '.', '-', '[' and ']', at most 64 characters"
		}
		return "model must be a model name: letters, digits and . _ : / @ - [ ], not starting with '-', at most 128 characters"
	}
	if !models.ValidEffortFor(engine, effort) {
		if levels := models.EffortsFor(engine); len(levels) > 0 {
			return "effort must be one of " + strings.Join(levels, ", ")
		}
		return "engine " + models.NormalizeEngine(engine) + " takes no effort"
	}
	return ""
}

// sanitizeSetSessionModel checks a browser's in-session model/effort switch
// before it is forwarded to the daemon, where both can become engine CLI
// arguments. The session's engine is not known here, so both get the
// engine-agnostic check (the daemon's driver applies its own engine's rules
// on top). ok is false when either is invalid or both are empty — nothing
// worth forwarding.
func sanitizeSetSessionModel(msg protocol.SetSessionModel) (protocol.SetSessionModel, bool) {
	if msg.Model == "" && msg.Effort == "" {
		return msg, false
	}
	if !models.ValidAnyModel(msg.Model) || !models.ValidAnyEffort(msg.Effort) {
		return msg, false
	}
	return protocol.SetSessionModel{
		Type: "set_session_model", SessionID: msg.SessionID, Model: msg.Model, Effort: msg.Effort,
	}, true
}
