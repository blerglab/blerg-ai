package server

// Child models (0.4.3). An agent names a child's model by hand, so the name is resolved against the
// engine's catalog on the host the child runs on before anything is stored or launched: an exact
// routing id, a display name, or a unique suffix or substring of an id. Anything else is refused
// with the models that are available, rather than accepted and left to fail in the child (a made-up
// Hermes route makes the pod fail with "model route is unavailable or unsafe").

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/models"
)

// claudeAlias: Claude Code resolves its own family aliases ("sonnet", "opus[1m]", "default"), so
// they pass unchanged.
var claudeAlias = regexp.MustCompile(`^(default|best|opusplan|opus|sonnet|haiku|fable)(\[1m\])?$`)

// childCatalogWait bounds how long a child start waits for a cluster catalog that is still being
// fetched (a cold cache); after it the name is resolved against whatever is known.
const childCatalogWait = 5 * time.Second

// resolveModelName resolves in against catalog for engine. It returns the routing id to store, or
// a problem for the 422. An empty name stays empty (the engine default); with no catalog known the
// name goes through as given and the engine rules still apply.
func resolveModelName(engine, in string, catalog []models.Model) (string, string) {
	in = strings.TrimSpace(in)
	if in == "" || len(catalog) == 0 {
		return in, ""
	}
	engine = models.NormalizeEngine(engine)
	if engine == models.ClaudeEngine && claudeAlias.MatchString(in) {
		return in, ""
	}
	for _, m := range catalog {
		if m.ID == in {
			return m.ID, ""
		}
	}
	if engine == models.ClaudeEngine {
		if base, ok := strings.CutSuffix(in, "[1m]"); ok {
			for _, m := range catalog {
				if m.ID == base {
					return in, ""
				}
			}
		}
	}
	lower := strings.ToLower(in)
	stages := []func(models.Model) bool{
		func(m models.Model) bool { return strings.ToLower(m.Name) == lower },
		func(m models.Model) bool { return strings.ToLower(m.ID) == lower },
		func(m models.Model) bool { return strings.HasSuffix(strings.ToLower(m.ID), lower) },
		func(m models.Model) bool {
			return strings.Contains(strings.ToLower(m.ID), lower) || strings.Contains(strings.ToLower(m.Name), lower)
		},
	}
	for _, match := range stages {
		var found []models.Model
		for _, m := range catalog {
			if match(m) {
				found = append(found, m)
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0].ID, ""
		default:
			return "", "ambiguous model " + in + " for " + engineLabel(engine) + "; available: " + availableModels(catalog)
		}
	}
	return "", "unknown model " + in + " for " + engineLabel(engine) + "; available: " + availableModels(catalog)
}

// availableModels is "<display name> (<id>), …" for the error and the CLI.
func availableModels(catalog []models.Model) string {
	parts := make([]string, 0, len(catalog))
	for _, m := range catalog {
		if m.Name != "" && m.Name != m.ID {
			parts = append(parts, m.Name+" ("+m.ID+")")
		} else {
			parts = append(parts, m.ID)
		}
	}
	return strings.Join(parts, ", ")
}

// childModelCatalog is engine's model catalog on the child host: the cluster's catalog for the
// owner (the shared Hermes providers, the owner's Codex catalog, …; waiting up to childCatalogWait
// for a cold cache) or the desktop daemon's reported or built-in list. nil when none is known.
func (a *API) childModelCatalog(ctx context.Context, engine string, host childHost, p runnerPrincipal) []models.Model {
	engine = models.NormalizeEngine(engine)
	if host.cluster {
		jm := a.hub.JobManager()
		if jm == nil {
			return nil
		}
		if _, native := clusterModelProviderOn(engine, jm); native {
			snapshot := func() models.List {
				if p.proof().valid() && p.spawningAccountID() != "" {
					return a.clusterModelSnapshot(ctx, engine, p.spawningAccountID(), p.proof(), false)
				}
				return a.operatorClusterModelSnapshot(ctx, engine, false)
			}
			list := snapshot()
			for deadline := time.Now().Add(childCatalogWait); len(list.Models) == 0 && list.Refreshing && time.Now().Before(deadline); {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(100 * time.Millisecond):
				}
				list = snapshot()
			}
			return list.Models
		}
		list, err := a.modelRegistry().Models(ctx, engine, "")
		if err != nil || list.Source == models.SourceDaemon {
			return nil
		}
		return list.Models
	}
	if host.dc == nil {
		return nil
	}
	list, err := a.modelRegistry().Models(ctx, engine, host.dc.ID)
	if err != nil || (list.Source == models.SourceDaemon && list.DaemonID != host.dc.ID) {
		return nil
	}
	return list.Models
}

// resolveChildModel resolves a child's model name on host (a 422 when it names no single model).
func (a *API) resolveChildModel(ctx context.Context, engine, model string, host childHost, p runnerPrincipal) (string, *APIError) {
	if strings.TrimSpace(model) == "" {
		return "", nil
	}
	id, problem := resolveModelName(engine, model, a.childModelCatalog(ctx, engine, host, p))
	if problem != "" {
		return "", apiErrorf(http.StatusUnprocessableEntity, "%s", problem)
	}
	return id, nil
}

// childHostOfChild is where an existing child runs, for its model changes: the cluster for a
// cluster child, else its desktop daemon (nil while disconnected).
func (a *API) childHostOfChild(ctx context.Context, row *db.SessionRow) childHost {
	if onClusterParent(row) {
		return childHost{cluster: true}
	}
	return childHost{dc: a.hub.ownedHostDaemon(ctx, derefOrEmpty(row.SpawningAccountID), row.DaemonID)}
}

type childModelsEngine struct {
	Engine  string         `json:"engine"`
	Runtime string         `json:"runtime"`
	Models  []models.Model `json:"models"`
}

// HandleGetChildModels is GET /api/sessions/{id}/children/models?engine=&runtime= for the parent's
// own token: per engine its children can run, the models they may be started on (display name and
// routing id), from the same catalog a start resolves against. `blerg-runner models` prints it.
func (a *API) HandleGetChildModels(w http.ResponseWriter, r *http.Request) {
	parent, dc, ok := a.delegateParent(w, r, delegateOpts{AllowDisconnected: true})
	if !ok {
		return
	}
	ctx := r.Context()
	host := a.parentHost(parent, dc)
	want := models.NormalizeEngine(r.URL.Query().Get("engine"))
	runtime := r.URL.Query().Get("runtime")
	if runtime == runnerRuntimeCluster && !host.cluster {
		// A desktop parent's cluster children, when the install has a cluster.
		if !host.clusterOn {
			writeError(w, http.StatusUnprocessableEntity, msgNoClusterRuntime)
			return
		}
		host = host.forRuntime(runtime)
	}
	delegate := a.delegateFor(ctx, parent, runtime, "")
	if runtime == "" {
		runtime = runnerRuntimeDocker
		if host.cluster {
			runtime = runnerRuntimeCluster
		} else if dc != nil && !dc.SandboxAvailable() {
			runtime = runnerRuntimeDaemon
		}
	}
	if host.cluster != (runtime == runnerRuntimeCluster) {
		writeError(w, http.StatusUnprocessableEntity, "this session's children run "+map[bool]string{true: `on the cluster (runtime "cluster")`, false: `on its desktop (runtime "docker" or "daemon")`}[host.cluster])
		return
	}
	out := struct {
		Engines []childModelsEngine `json:"engines"`
	}{Engines: []childModelsEngine{}}
	var clusterEngines []string
	if host.cluster {
		clusterEngines, _ = a.clusterAvailableEngines(ctx, delegate)
	}
	for _, d := range engines.All() {
		if want != "" && d.ID != want {
			continue
		}
		if !d.SupportsRuntime(runtime) {
			continue
		}
		if host.cluster {
			if len(clusterEngines) > 0 && !containsString(clusterEngines, d.ID) {
				continue
			}
		} else if dc == nil || !containsString(dc.AvailableEngines(), d.ID) {
			continue
		}
		list := a.childModelCatalog(ctx, d.ID, host, delegate)
		if list == nil {
			list = []models.Model{}
		}
		out.Engines = append(out.Engines, childModelsEngine{Engine: d.ID, Runtime: runtime, Models: list})
	}
	writeJSON(w, http.StatusOK, out)
}
