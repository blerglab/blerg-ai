package server

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
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
	// Unavailable is every other known engine, with why it can't run on this
	// target and the exact fix (engine discovery after install). Absent when
	// every engine is offered or nothing is known.
	Unavailable []UnavailableEngine `json:"unavailable,omitempty"`
	Error       string              `json:"error,omitempty"`
	Targets     []LaunchTarget      `json:"targets"`
}

// UnavailableEngine is a known engine this target can't run now. Status is a
// protocol.EngineStatus value, or "unsupported" (this runtime or session kind
// can never run it) or "not_connected" (no Cluster credential). Action is the
// one-click fix: "check_again", "redetect_path", "settings" or "".
type UnavailableEngine struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	Reason       string   `json:"reason"`
	Fix          string   `json:"fix,omitempty"`
	Action       string   `json:"action,omitempty"`
	DaemonID     string   `json:"daemon_id,omitempty"`
	FoundAt      string   `json:"found_at,omitempty"`
	SearchedPath []string `json:"searched_path,omitempty"`
}

type LaunchTarget struct {
	DaemonID string   `json:"daemon_id,omitempty"`
	Name     string   `json:"name"`
	Runtimes []string `json:"runtimes"`
	// HasRepo (a desktop, when the request names a repo): whether that desktop has a folder for
	// it, matched the way a start pinned to it finds one (repoFolderOn). Absent otherwise.
	HasRepo *bool `json:"has_repo,omitempty"`
	// RepoOrigin (only the desktop the request names, for a bare folder name it has): the hosted
	// repository that folder's origin remote points at, or absent when it has none the desktop
	// recognised. Only the exact persisted owner can consult a desktop, and a
	// caller resolving a folder name consults only the desktop it chose.
	RepoOrigin *protocol.RepoOrigin `json:"repo_origin,omitempty"`
	// ResolvesBareRepos (the cluster): the install has a default organization or git base, so a
	// cluster start may name a bare folder and the cluster resolves it itself.
	ResolvesBareRepos bool `json:"resolves_bare_repos,omitempty"`
}

func (a *API) authorizeLaunchOptions(w http.ResponseWriter, r *http.Request) (runnerPrincipal, bool) {
	if p, ok := a.verifyBrowserToken(a.rawBearer(r)); ok {
		*r = *r.WithContext(withDesktopViewer(r.Context(), a.desktopRecipient(r, runnerPrincipalFromIdentity(p).spawningAccountID())))
		return runnerPrincipalFromIdentity(p), true
	}
	return a.authRunner(w, r)
}

func (a *API) HandleLaunchOptions(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authorizeLaunchOptions(w, r)
	if !ok {
		return
	}
	snapshot := a.hub.ownedHostDaemons(r.Context(), p.spawningAccountID())
	ctx := withDesktopReadSnapshot(r.Context(), snapshot)
	q := r.URL.Query()
	options, err := a.DiscoverLaunchOptions(ctx, p, LaunchOptionsRequest{SessionID: q.Get("session_id"), Runtime: q.Get("runtime"), DaemonID: q.Get("daemon_id"), Repo: q.Get("repo"), Kind: q.Get("kind"), Restricted: q.Get("restricted") == "1", Refresh: q.Get("refresh") == "1", PersonalOnly: q.Get("personal_only") == "1"})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if err := a.hub.deliverDesktopSnapshot(ctx, p.spawningAccountID(), snapshot, func() { writeJSON(w, http.StatusOK, options) }); err != nil {
		writeError(w, http.StatusConflict, "desktop changed; refresh launch options")
	}
}

// folderOrigin is the hosted repository dc reports for its folder, normalized the way a cluster
// start resolves a bare name (resolveClusterRepoName), or nil when it reports none.
func folderOrigin(dc *DaemonConn, folder string) *protocol.RepoOrigin {
	origin, ok := dc.RepoOrigin(folder)
	if !ok {
		return nil
	}
	ref, ok := gitprovider.Default.ParseFullName(origin.Provider, origin.FullName)
	if !ok {
		return nil
	}
	return &protocol.RepoOrigin{Provider: origin.Provider, FullName: ref.FullName()}
}

// DiscoverLaunchOptions resolves readiness using the same principal and target
// as launch. It never substitutes another account's catalog or a host union for
// a Cluster catalog. Catalog acquisition itself remains asynchronous.
func (a *API) DiscoverLaunchOptions(parent context.Context, p RunnerPrincipal, req LaunchOptionsRequest) (LaunchOptions, *APIError) {
	parent = withDesktopViewer(parent, desktopViewer(parent, p.spawningAccountID()))
	if req.SessionID != "" {
		return a.discoverExistingSessionOptions(parent, p, req)
	}
	out := LaunchOptions{Runtime: req.Runtime, State: "known", Engines: []LaunchEngine{}}
	out.Targets = []LaunchTarget{}
	if jm := a.hub.JobManager(); jm != nil {
		out.Targets = append(out.Targets, LaunchTarget{Name: "Cluster", Runtimes: []string{runnerRuntimeCluster},
			ResolvesBareRepos: jm.GitHubOrg != "" || jm.ExplicitGitURLBase != ""})
	}
	hosts := a.hub.ownedHostDaemons(parent, p.spawningAccountID())
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })
	for _, host := range hosts {
		runtimes := []string{runnerRuntimeDaemon}
		if host.SandboxAvailable() {
			runtimes = append(runtimes, runnerRuntimeDocker)
		}
		target := LaunchTarget{DaemonID: host.ID, Name: host.Name, Runtimes: runtimes}
		if req.Repo != "" && validateRepoName(req.Repo) == nil {
			folder := repoFolderOn(host, req.Repo, "")
			has := slices.Contains(host.CheckedOutRepos(), folder)
			target.HasRepo = &has
			if has && req.DaemonID != "" && host.ID == req.DaemonID && !strings.Contains(req.Repo, "/") {
				target.RepoOrigin = folderOrigin(host, folder)
			}
		}
		out.Targets = append(out.Targets, target)
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
			dc = a.hub.ownedHostDaemon(parent, p.spawningAccountID(), req.DaemonID)
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
		if req.Kind == "tmux" && !descriptor.Terminal {
			out.Unavailable = append(out.Unavailable, unsupportedEngine(descriptor, descriptor.Name+" has no terminal mode: start an Agent session instead."))
			continue
		}
		if req.Restricted && !descriptor.Restricted {
			out.Unavailable = append(out.Unavailable, unsupportedEngine(descriptor, descriptor.Name+" can't run with restricted tools."))
			continue
		}
		runtime := out.Runtime
		target := dc
		if out.Runtime != runnerRuntimeCluster && target == nil {
			requirement := HostSelection{Engine: descriptor.ID, Runtime: runtime, Restricted: req.Restricted, Kind: req.Kind}
			if req.Repo != "" {
				target = a.hub.ownedDaemonForRepo(parent, p.spawningAccountID(), req.Repo, requirement)
			} else {
				target = a.hub.ownedDaemonForScratch(parent, p.spawningAccountID(), runtime, requirement)
			}
			if target == nil {
				// No desktop can run it: say why on the first one that reports a reason.
				if why, ok := unavailableOnHosts(descriptor, hosts, runtime, req); ok {
					out.Unavailable = append(out.Unavailable, why)
				}
				continue
			}
		}
		if runtime == "" && target != nil {
			runtime = resolveDaemonRuntime("", target.SandboxAvailable())
		}
		if !descriptor.SupportsRuntime(runtime) {
			out.Unavailable = append(out.Unavailable, unsupportedEngine(descriptor, descriptor.Name+" doesn't run "+runtimePlace(runtime)+"."))
			continue
		}
		if target != nil {
			state := hostEngineState(target, HostSelection{Engine: descriptor.ID, Runtime: runtime, Restricted: req.Restricted, Kind: req.Kind})
			if state == "unknown" {
				out.State = "unknown"
				continue
			}
			if state != "available" {
				out.Unavailable = append(out.Unavailable, unavailableOnHost(descriptor, target, runtime, req))
				continue
			}
		} else if !slices.Contains(clusterAvailable, descriptor.ID) {
			out.Unavailable = append(out.Unavailable, UnavailableEngine{ID: descriptor.ID, Name: descriptor.Name, Status: "not_connected",
				Reason: descriptor.Name + " isn't connected for the Cluster.", Fix: "Connect " + descriptor.Name + " in Settings, then Check again.", Action: "settings"})
			continue
		}
		var list models.List
		if _, native := clusterModelProviderOn(descriptor.ID, a.hub.JobManager()); runtime == runnerRuntimeCluster && native {
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
		host := a.hub.ownedHostDaemon(ctx, p.spawningAccountID(), row.DaemonID)
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
	options.Unavailable = nil
	return options, nil
}

func unsupportedEngine(d engines.Descriptor, reason string) UnavailableEngine {
	return UnavailableEngine{ID: d.ID, Name: d.Name, Status: "unsupported", Reason: reason}
}

func runtimePlace(runtime string) string {
	switch runtime {
	case runnerRuntimeCluster:
		return "on the Cluster"
	case runnerRuntimeDocker:
		return "in the Local sandbox"
	}
	return "on This machine"
}

// unavailableOnHost is why d can't run on dc: the daemon's own status when it
// reported one that says so, else what the server knows (the sandbox has no
// Claude credential, or an older daemon that only lists what works).
func unavailableOnHost(d engines.Descriptor, dc *DaemonConn, runtime string, req LaunchOptionsRequest) UnavailableEngine {
	out := UnavailableEngine{ID: d.ID, Name: d.Name, DaemonID: dc.ID, Action: "check_again"}
	switch {
	case req.Restricted && (runtime != runnerRuntimeDocker || !dc.CanRestrictTools() || (d.ID == "codex" && !dc.CanCodexRestricted())):
		out.Status, out.Reason, out.Action = "unsupported", dc.Name+" can't run "+d.Name+" with restricted tools; update its daemon.", ""
		return out
	case runtime == runnerRuntimeDocker && !dc.SandboxAvailable():
		out.Status, out.Reason = "unsupported", "The Local sandbox isn't available on "+dc.Name+"."
		out.Fix = "Build the sandbox image there (daemon/install.sh install), or run on This machine."
		return out
	case runtime == runnerRuntimeDocker && d.ID == "claude":
		if credential := dc.SandboxClaudeCredential(); credential != nil && !*credential {
			out.Status, out.Reason = protocol.EngineNotLoggedIn, dc.Name+" has no Claude login or API key to give the Local sandbox."
			out.Fix = "Log in with `claude` on " + dc.Name + ", or set CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY for its daemon, then Check again."
			return out
		}
	}
	if st, ok := engineStatusOn(dc, d.ID); ok && st.Status != protocol.EngineAvailable {
		out.Status, out.Reason, out.Fix, out.Action = st.Status, st.Reason, st.Fix, st.Action
		out.FoundAt, out.SearchedPath = st.FoundAt, st.SearchedPath
		return out
	}
	if d.ID == "claude" && req.Kind == "tmux" && runtime == runnerRuntimeDaemon {
		out.Status, out.Reason = protocol.EngineNotInstalled, "A Claude terminal needs the `claude` CLI on "+dc.Name+"."
		out.Fix, out.Action = "Install Claude Code there, or Re-detect PATH if your shell already finds it.", "redetect_path"
		return out
	}
	out.Status, out.Reason = protocol.EngineNotInstalled, dc.Name+" doesn't report "+d.Name+" as set up."
	out.Fix = "Set " + d.Name + " up on " + dc.Name + ", then Check again. Update the daemon there to see exactly what's missing."
	return out
}

// unavailableOnHosts is unavailableOnHost on the first owned desktop (by id)
// that could otherwise run runtime; false when there is none to ask.
func unavailableOnHosts(d engines.Descriptor, hosts []*DaemonConn, runtime string, req LaunchOptionsRequest) (UnavailableEngine, bool) {
	for _, host := range hosts {
		rt := runtime
		if rt == "" {
			rt = resolveDaemonRuntime("", host.SandboxAvailable())
		}
		if !d.SupportsRuntime(rt) {
			return unsupportedEngine(d, d.Name+" doesn't run "+runtimePlace(rt)+"."), true
		}
		if hostEngineState(host, HostSelection{Engine: d.ID, Runtime: rt, Restricted: req.Restricted, Kind: req.Kind}) == "unknown" {
			continue
		}
		return unavailableOnHost(d, host, rt, req), true
	}
	return UnavailableEngine{}, false
}
