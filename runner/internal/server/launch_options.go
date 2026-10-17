package server

import (
	"context"
	"net/http"
	"slices"
	"sort"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/models"
)

type LaunchOptionsRequest struct {
	SessionID    string `json:"session_id,omitempty"`
	Runtime      string `json:"runtime,omitempty"`
	DaemonID     string `json:"daemon_id,omitempty"`
	Repo         string `json:"repo,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Restricted   bool   `json:"restricted,omitempty"`
	Refresh      bool   `json:"refresh,omitempty"`
	PersonalOnly bool   `json:"personal_only,omitempty"`
}

type LaunchEngine struct {
	engines.Descriptor
	Models   models.List `json:"models"`
	DaemonID string      `json:"daemon_id,omitempty"`
	Runtime  string      `json:"runtime"`
}

type LaunchOptions struct {
	Refreshing bool           `json:"refreshing,omitempty"`
	Runtime    string         `json:"runtime"`
	DaemonID   string         `json:"daemon_id,omitempty"`
	State      string         `json:"state"`
	Engines    []LaunchEngine `json:"engines"`
	Error      string         `json:"error,omitempty"`
	Targets    []LaunchTarget `json:"targets"`
}

type LaunchTarget struct {
	DaemonID string   `json:"daemon_id,omitempty"`
	Name     string   `json:"name"`
	Runtimes []string `json:"runtimes"`
}

func (a *API) authorizeLaunchOptions(w http.ResponseWriter, r *http.Request) (runnerPrincipal, bool) {
	if p, ok := a.verifyBrowserToken(a.rawBearer(r)); ok {
		return runnerPrincipalFromIdentity(p), true
	}
	return a.authRunner(w, r)
}

func (a *API) HandleLaunchOptions(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authorizeLaunchOptions(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	options, err := a.DiscoverLaunchOptions(r.Context(), p, LaunchOptionsRequest{SessionID: q.Get("session_id"), Runtime: q.Get("runtime"), DaemonID: q.Get("daemon_id"), Repo: q.Get("repo"), Kind: q.Get("kind"), Restricted: q.Get("restricted") == "1", Refresh: q.Get("refresh") == "1", PersonalOnly: q.Get("personal_only") == "1"})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, options)
}

// DiscoverLaunchOptions resolves readiness using the same principal and target
// as launch. It never substitutes another account's catalog or a host union for
// a Cluster catalog. Catalog acquisition itself remains asynchronous.
func (a *API) DiscoverLaunchOptions(parent context.Context, p RunnerPrincipal, req LaunchOptionsRequest) (LaunchOptions, *APIError) {
	if req.SessionID != "" {
		return a.discoverExistingSessionOptions(parent, p, req)
	}
	out := LaunchOptions{Runtime: req.Runtime, State: "known", Engines: []LaunchEngine{}}
	out.Targets = []LaunchTarget{}
	if a.hub.JobManager() != nil {
		out.Targets = append(out.Targets, LaunchTarget{Name: "Cluster", Runtimes: []string{runnerRuntimeCluster}})
	}
	hosts := a.hub.GetHostDaemons()
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })
	for _, host := range hosts {
		runtimes := []string{runnerRuntimeDaemon}
		if host.SandboxAvailable() {
			runtimes = append(runtimes, runnerRuntimeDocker)
		}
		out.Targets = append(out.Targets, LaunchTarget{DaemonID: host.ID, Name: host.Name, Runtimes: runtimes})
	}
	if req.Runtime != "" && req.Runtime != runnerRuntimeCluster && req.Runtime != runnerRuntimeDocker && req.Runtime != runnerRuntimeDaemon {
		return out, apiErrorf(http.StatusUnprocessableEntity, "%s", runnerRuntimeMessage)
	}
	if req.Kind != "" && req.Kind != "agent" && req.Kind != "tmux" {
		return out, apiErrorf(http.StatusUnprocessableEntity, "kind must be agent or tmux")
	}
	if req.Repo != "" {
		if err := validateRepoName(req.Repo); err != nil {
			return out, apiErrorf(http.StatusUnprocessableEntity, "invalid repository")
		}
	}
	autoHost := req.Runtime == "" && (req.DaemonID != "" || a.hub.JobManager() == nil)
	if out.Runtime == "" && req.DaemonID == "" && a.hub.JobManager() != nil {
		out.Runtime = runnerRuntimeCluster
	}
	var dc *DaemonConn
	if out.Runtime != runnerRuntimeCluster {
		if req.DaemonID != "" {
			dc = a.hub.GetHostDaemon(req.DaemonID)
			if dc == nil {
				out.Error = "The selected execution host is not connected"
				return out, nil
			}
			out.DaemonID = dc.ID
		} else if len(hosts) == 0 {
			out.Error = "No execution host is connected"
			return out, nil
		}
	} else if req.DaemonID != "" {
		return out, apiErrorf(http.StatusUnprocessableEntity, "daemon_id cannot select a Cluster runtime")
	}
	if req.Restricted && autoHost {
		out.Runtime = runnerRuntimeDocker
	}
	var clusterAvailable []string
	if out.Runtime == runnerRuntimeCluster {
		if a.hub.JobManager() == nil || req.Kind == "tmux" {
			return out, nil
		}
		var known bool
		clusterAvailable, known, out.Refreshing = a.clusterReadinessSnapshot(parent, p, req.Refresh, req.PersonalOnly)
		if !known {
			out.State = "unknown"
			if !out.Refreshing {
				out.Error = "Engine credentials could not be verified; retry or reconnect in Settings"
			}
			return out, nil
		}
	}
	for _, descriptor := range engines.All() {
		if (req.Kind == "tmux" && !descriptor.Terminal) || (req.Restricted && !descriptor.Restricted) {
			continue
		}
		runtime := out.Runtime
		target := dc
		if out.Runtime != runnerRuntimeCluster && target == nil {
			requirement := HostSelection{Engine: descriptor.ID, Runtime: runtime, Restricted: req.Restricted, Kind: req.Kind}
			if req.Repo != "" {
				target = a.hub.DaemonForRepo(req.Repo, requirement)
			} else {
				target = a.hub.DaemonForScratch(runtime, requirement)
			}
			if target == nil {
				continue
			}
		}
		if runtime == "" && target != nil {
			runtime = resolveDaemonRuntime("", target.SandboxAvailable())
		}
		if !descriptor.SupportsRuntime(runtime) {
			continue
		}
		if target != nil {
			state := hostEngineState(target, HostSelection{Engine: descriptor.ID, Runtime: runtime, Restricted: req.Restricted, Kind: req.Kind})
			if state == "unknown" {
				out.State = "unknown"
				continue
			}
			if state != "available" {
				continue
			}
		} else if !slices.Contains(clusterAvailable, descriptor.ID) {
			continue
		}
		var list models.List
		if _, native := clusterModelProviderFor(descriptor.ID); runtime == runnerRuntimeCluster && native {
			if p.Kind == runnerKeyPrincipalKind {
				list = a.operatorClusterModelSnapshot(parent, descriptor.ID, req.Refresh)
			} else {
				list = a.clusterModelSnapshot(parent, descriptor.ID, p.spawningAccountID(), p.proof(), req.Refresh)
			}
		} else {
			var err error
			targetID := ""
			if target != nil {
				targetID = target.ID
			}
			list, err = a.modelRegistry().Models(parent, descriptor.ID, targetID)
			if err != nil {
				list = models.List{Source: models.SourceNone, Error: "Model catalog is unavailable; retry"}
			}
			if runtime == runnerRuntimeCluster && list.Source == models.SourceDaemon {
				list = models.List{Source: models.SourceNone}
			}
		}
		list.Models = append([]models.Model{}, list.Models...)
		list.EngineEfforts = append([]string{}, models.EffortsFor(descriptor.ID)...)
		option := LaunchEngine{Descriptor: descriptor, Models: list, Runtime: runtime}
		if target != nil {
			option.DaemonID = target.ID
		}
		out.Engines = append(out.Engines, option)
	}
	if len(out.Engines) > 0 {
		out.State = "known"
		if out.Runtime == "" {
			common := out.Engines[0].Runtime
			for _, option := range out.Engines {
				if option.Runtime != common {
					common = ""
					break
				}
			}
			out.Runtime = common
		}
	}
	return out, nil
}

// Existing-session discovery is bound to its actual native engine and runtime,
// rather than whichever host would win a new launch today.
func (a *API) discoverExistingSessionOptions(ctx context.Context, p RunnerPrincipal, req LaunchOptionsRequest) (LaunchOptions, *APIError) {
	row, problem := a.requireSessionAccess(ctx, p, req.SessionID)
	if problem != nil {
		return LaunchOptions{}, problem
	}
	if row.Kind != "agent" || terminalSessionStatus(row.Status) || row.EndedAt != nil {
		return LaunchOptions{}, apiErrorf(http.StatusConflict, "model discovery requires an active agent conversation")
	}
	if owner := derefOrEmpty(row.SpawningAccountID); owner != "" && p.spawningAccountID() != owner {
		return LaunchOptions{}, apiErrorf(http.StatusForbidden, "session model catalogs require the owner's launch identity")
	}
	runtime := derefOrEmpty(row.Runtime)
	if runtime == "" {
		return LaunchOptions{}, apiErrorf(http.StatusConflict, "the session's execution runtime was not recorded")
	}
	scoped := LaunchOptionsRequest{Runtime: runtime, Kind: "agent", Refresh: req.Refresh}
	if runtime != runnerRuntimeCluster {
		host := a.hub.FindDaemonForSession(row.ID)
		if host == nil {
			return LaunchOptions{Runtime: runtime, State: "unknown", Engines: []LaunchEngine{}, Targets: []LaunchTarget{}, Error: "The session's execution host is not connected"}, nil
		}
		scoped.DaemonID = host.ID
	}
	options, problem := a.DiscoverLaunchOptions(ctx, p, scoped)
	if problem != nil {
		return options, problem
	}
	engine := engines.Normalize(derefOrEmpty(row.Engine))
	options.Engines = slices.DeleteFunc(options.Engines, func(option LaunchEngine) bool { return option.ID != engine })
	return options, nil
}
