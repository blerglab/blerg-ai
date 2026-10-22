package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/models"
)

func TestLaunchOptionsUsesTargetAvailability(t *testing.T) {
	hub := NewHub()
	dc := reportingDaemon(hub, "fixture-host", map[string]models.Report{"codex": rep(time.Now(), mdl("gpt-host", "low", "high"))})
	dc.SetAvailableEngines([]string{"codex"})
	a := NewAPI(hub, nil, "fixture-daemon", nil, "")
	a.SetModelSources(NewModelSources(nil, hub))
	options, err := a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Runtime: "daemon", DaemonID: dc.ID})
	if err != nil || options.State != "known" || len(options.Engines) != 1 || options.Engines[0].ID != "codex" || options.Engines[0].Models.Models[0].ID != "gpt-host" {
		t.Fatalf("wrong target discovery: %+v %v", options, err)
	}
	options, err = a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Runtime: "docker", DaemonID: dc.ID})
	if err != nil || len(options.Engines) != 0 {
		t.Fatal("advertised missing sandbox")
	}
	options, err = a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Runtime: "daemon", DaemonID: "missing"})
	if err != nil || len(options.Engines) != 0 || options.Error == "" {
		t.Fatal("substituted another target")
	}
}

func TestLaunchOptionsRegisteredEngineNeedsNoSelectorBranch(t *testing.T) {
	t.Cleanup(engines.Register(engines.Descriptor{ID: "fixture-engine", Name: "Fixture Engine", Runtimes: []string{"daemon"}}))
	t.Cleanup(models.RegisterEngineRules("fixture-engine", models.EngineRules{}))
	h := NewHub()
	dc := reportingDaemon(h, "fixture-host", nil)
	dc.SetAvailableEngines([]string{"fixture-engine"})
	a := NewAPI(h, nil, "fixture-daemon", nil, "")
	options, err := a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Runtime: "daemon", DaemonID: dc.ID})
	if err != nil || len(options.Engines) != 1 || options.Engines[0].ID != "fixture-engine" || options.Engines[0].Models.Models == nil {
		t.Fatalf("new descriptor not discovered: %+v %v", options, err)
	}
	options, err = a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Runtime: "daemon", DaemonID: dc.ID, Restricted: true})
	if err != nil || len(options.Engines) != 0 {
		t.Fatal("unverified engine inherited confinement support")
	}
}

func TestLaunchOptionsOperatorCatalogCannotReadAccountCache(t *testing.T) {
	f := &fakeK8s{secrets: map[string]map[string]string{"blerg-runner-agent": {"CODEX_AUTH_JSON": b64(`{"OPENAI_API_KEY":"fixture-only"}`)}}}
	h := NewHub()
	j := newTestJobManager(t, f)
	h.SetJobManager(j)
	a := NewAPI(h, nil, "fixture-daemon", nil, "")
	a.SetRunnerKey("fixture-operator")
	a.clusterModels.entries = map[clusterModelCacheKey]*clusterModelCacheEntry{{"private-account", "codex", j.Image}: {list: cacheFixture(time.Now(), "gpt-private"), loaded: true}}
	started := make(chan struct{})
	release := make(chan struct{})
	a.clusterModels.probe = func(_ context.Context, engine, account string, proof coreProof) (models.List, error) {
		if engine != "codex" || account != "" || proof.valid() {
			t.Error("operator discovery selected personal scope")
		}
		close(started)
		<-release
		return cacheFixture(time.Now(), "gpt-operator"), nil
	}
	defer close(release)
	req := httptest.NewRequest(http.MethodGet, "/api/runner/launch-options?runtime=cluster", nil)
	req.Header.Set("Authorization", "Bearer fixture-operator")
	w := httptest.NewRecorder()
	a.HandleLaunchOptions(w, req)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "gpt-private") || !strings.Contains(w.Body.String(), `"refreshing":true`) {
		t.Fatalf("scope leak or failed operator snapshot: %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		options, problem := a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Runtime: "cluster"})
		if problem != nil {
			t.Fatal(problem)
		}
		if len(options.Engines) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("operator probe not started")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.credentialFetchCalls != 0 {
		t.Fatal("operator discovery fetched a personal credential")
	}
}

func TestLaunchOptionsRejectsUnknownRuntimeAndUnauthenticated(t *testing.T) {
	a := NewAPI(NewHub(), nil, "fixture-daemon", nil, "")
	a.SetRunnerKey("fixture-operator")
	w := httptest.NewRecorder()
	a.HandleLaunchOptions(w, httptest.NewRequest(http.MethodGet, "/api/runner/launch-options", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatal("unprotected discovery")
	}
	if _, err := a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{}, LaunchOptionsRequest{Runtime: "anything"}); err == nil {
		t.Fatal("invalid runtime accepted")
	}
}

func TestLaunchOptionsColdReadinessDoesNotWaitForKubernetes(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	kubernetes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer kubernetes.Close()
	defer close(release)
	h := NewHub()
	h.SetJobManager(&JobManager{Image: "fixture-image", BaseURL: kubernetes.URL, Namespace: "fixture", SecretName: "fixture", Client: kubernetes.Client()})
	a := NewAPI(h, nil, "fixture-daemon", nil, "")
	done := make(chan LaunchOptions, 1)
	go func() {
		options, _ := a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Runtime: "cluster"})
		done <- options
	}()
	select {
	case options := <-done:
		if !options.Refreshing || options.State != "unknown" || len(options.Engines) != 0 {
			t.Fatalf("invalid cold readiness %+v", options)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("discovery blocked on Kubernetes")
	}
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("readiness refresh never started")
	}
}

func TestLaunchOptionsUnpinnedHostsResolvePerEngine(t *testing.T) {
	h := NewHub()
	first := reportingDaemon(h, "a-no-sandbox", nil)
	first.SetAvailableEngines([]string{"claude"})
	second := reportingDaemon(h, "b-sandbox", map[string]models.Report{"codex": rep(time.Now(), mdl("gpt-b-host"))})
	second.SetAvailableEngines([]string{"codex"})
	second.SetSandboxAvailable(true)
	third := reportingDaemon(h, "c-sandbox", nil)
	third.SetAvailableEngines([]string{"claude"})
	third.SetSandboxAvailable(true)
	a := NewAPI(h, nil, "fixture-daemon", nil, "")
	options, err := a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Runtime: "docker"})
	if err != nil || len(options.Engines) != 2 || options.DaemonID != "" {
		t.Fatalf("invalid multi-host discovery %+v %v", options, err)
	}
	for _, option := range options.Engines {
		if option.ID == "codex" && (option.DaemonID != second.ID || option.Models.Models[0].ID != "gpt-b-host") {
			t.Fatal("Codex catalog came from another host")
		}
		if option.ID == "claude" && option.DaemonID != third.ID {
			t.Fatal("selected host without sandbox")
		}
	}
}

func TestLaunchOptionsTerminalRequiresCLIAndCannotBeRestricted(t *testing.T) {
	h := NewHub()
	host := reportingDaemon(h, "fixture-host", nil)
	host.SetHostClaude(false, true, nil)
	a := NewAPI(h, nil, "fixture-daemon", nil, "")
	options, _ := a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Runtime: "daemon", DaemonID: host.ID, Kind: "agent"})
	if len(options.Engines) != 1 || options.Engines[0].ID != "claude" {
		t.Fatal("API-backed agent not available")
	}
	options, _ = a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Runtime: "daemon", DaemonID: host.ID, Kind: "tmux"})
	if len(options.Engines) != 0 {
		t.Fatal("API key advertised as terminal executable")
	}
	host.SetSandboxAvailable(true)
	host.SetAvailableEngines([]string{"claude", "codex"})
	options, _ = a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Runtime: "docker", DaemonID: host.ID, Kind: "tmux", Restricted: true})
	if len(options.Engines) != 0 {
		t.Fatal("restricted terminal advertised")
	}
}

func TestLaunchOptionsAutoRuntimeResolvesForEachEngine(t *testing.T) {
	h := NewHub()
	host := reportingDaemon(h, "a-codex-host", map[string]models.Report{"codex": rep(time.Now(), mdl("gpt-host"))})
	host.SetAvailableEngines([]string{"codex"})
	other := reportingDaemon(h, "b-empty-sandbox", nil)
	other.SetAvailableEngines([]string{})
	other.SetSandboxAvailable(true)
	a := NewAPI(h, nil, "fixture-daemon", nil, "")
	options, problem := a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{})
	if problem != nil || len(options.Engines) != 1 || options.Engines[0].ID != "codex" || options.Engines[0].DaemonID != host.ID || options.Engines[0].Runtime != "daemon" {
		t.Fatalf("auto discovery lost eligible engine: %+v %v", options, problem)
	}
	if _, problem := a.StartSession(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, runnerStartRequest{NoRepo: true, Engine: "codex"}, ""); problem != nil {
		t.Fatal(problem)
	}
	select {
	case <-host.send:
	default:
		t.Fatal("auto launch disagreed with discovery")
	}
	options, problem = a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{DaemonID: host.ID})
	if problem != nil || len(options.Engines) != 1 || options.Engines[0].Runtime != "daemon" {
		t.Fatal("auto pinned host discovery failed")
	}
}

func TestLaunchReadinessTransportFailureIsUnknown(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("fixture-sensitive-response"))
	}))
	defer backend.Close()
	h := NewHub()
	h.SetJobManager(&JobManager{Image: "fixture", BaseURL: backend.URL, Client: backend.Client(), Namespace: "fixture", SecretName: "fixture"})
	a := NewAPI(h, nil, "fixture-daemon", nil, "")
	deadline := time.Now().Add(15 * time.Second)
	for {
		options, problem := a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Runtime: "cluster"})
		if problem != nil {
			t.Fatal(problem)
		}
		if !options.Refreshing {
			if options.State != "unknown" || options.Error == "" || len(options.Engines) != 0 || strings.Contains(options.Error, "fixture-sensitive-response") {
				t.Fatalf("failure became authoritative absence: %+v", options)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("readiness did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSecretReadinessHonorsContextDeadline(t *testing.T) {
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer backend.Close()
	defer close(release)
	j := &JobManager{BaseURL: backend.URL, Client: backend.Client(), Namespace: "fixture", SecretName: "fixture"}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	done := make(chan bool, 1)
	go func() { _, known := j.discoverCredentialEngines(ctx); done <- known }()
	select {
	case known := <-done:
		if known {
			t.Fatal("timeout became authoritative absence")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("credential discovery ignored deadline")
	}
}

func TestLaunchReadinessPersonalOnlyExcludesOperatorAndHasSeparateCache(t *testing.T) {
	var operatorReads atomic.Int32
	kubernetes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		operatorReads.Add(1)
		_, _ = w.Write([]byte(`{"data":{"CLAUDE_CODE_OAUTH_TOKEN":"Zml4dHVyZQ=="}}`))
	}))
	defer kubernetes.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req internalCredentialListRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.AccountID != "fixture-owner" || req.TokenID != "fixture-token" {
			t.Error("lost account-only credential proof")
		}
		_, _ = w.Write([]byte(`{"engines":["codex"]}`))
	}))
	defer core.Close()
	hub := NewHub()
	hub.SetJobManager(&JobManager{Image: "fixture", BaseURL: kubernetes.URL, Client: kubernetes.Client(), SecretName: "fixture", Namespace: "fixture"})
	api := NewAPI(hub, nil, "fixture-daemon", nil, "")
	api.SetCoreCredentials(core.URL, "fixture-key")
	principal := runnerPrincipal{Kind: "agent", Sub: "fixture-token", OnBehalfOf: "fixture-owner"}
	read := func(personal bool) []string {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			got, known, refreshing := api.clusterReadinessSnapshot(t.Context(), principal, false, personal)
			if known && !refreshing {
				return got
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("readiness never completed")
		return nil
	}
	both := read(false)
	if !slices.Contains(both, "claude") || !slices.Contains(both, "codex") {
		t.Fatal("normal launch lost credential sources")
	}
	before := operatorReads.Load()
	personal := read(true)
	if len(personal) != 1 || personal[0] != "codex" {
		t.Fatal("account-only discovery inherited operator engine")
	}
	if operatorReads.Load() != before {
		t.Fatal("account-only discovery queried operator credentials")
	}
	if !slices.Contains(read(false), "claude") {
		t.Fatal("account-only result replaced ordinary cache")
	}
}

// A desktop target says whether it has the requested repo (by folder or by its reported origin),
// so the Board can pick the one desktop with a board's repository.
func TestLaunchOptionsTargetsReportRepoPresence(t *testing.T) {
	hub := NewHub()
	a1 := &DaemonConn{ID: "desktop-a", Name: "A", send: make(chan []byte, 8)}
	a1.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	a1.SetCheckedOutRepos([]string{"app"})
	a1.SetRepoRemotes(map[string]string{"app": "example-org/app"})
	hub.Register(a1)
	b := &DaemonConn{ID: "desktop-b", Name: "B", send: make(chan []byte, 8)}
	b.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	b.SetCheckedOutRepos([]string{"other"})
	hub.Register(b)
	a := NewAPI(hub, nil, "fixture-daemon", nil, "")
	has := func(repo string) map[string]any {
		options, err := a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Repo: repo, Kind: "agent"})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]any{}
		for _, target := range options.Targets {
			if target.HasRepo == nil {
				out[target.DaemonID] = nil
			} else {
				out[target.DaemonID] = *target.HasRepo
			}
		}
		return out
	}
	for _, repo := range []string{"app", "example-org/app"} {
		if got := has(repo); got["desktop-a"] != true || got["desktop-b"] != false {
			t.Fatalf("%s: %v", repo, got)
		}
	}
	if got := has(""); got["desktop-a"] != nil || got["desktop-b"] != nil {
		t.Fatalf("no repo asked: %v", got)
	}
}

// A bare folder's remote is reported only for the desktop the request names, never another
// desktop's: desktops have no owner, so the Board resolves a board repo to owner/name against the
// one desktop the person chose. A folder with no remote reports none.
func TestLaunchOptionsReportOnlyTheNamedDesktopsRemote(t *testing.T) {
	hub := NewHub()
	mine := &DaemonConn{ID: "desktop-a", Name: "A", send: make(chan []byte, 8)}
	mine.SetSandboxClones(true)
	mine.SetCheckedOutRepos([]string{"app", "scratch"})
	mine.SetRepoRemotes(map[string]string{"app": "example-org/app"})
	hub.Register(mine)
	other := &DaemonConn{ID: "desktop-b", Name: "B", send: make(chan []byte, 8)}
	other.SetSandboxClones(true)
	other.SetCheckedOutRepos([]string{"app", "scratch"})
	other.SetRepoRemotes(map[string]string{"app": "someone/app", "scratch": "someone/scratch"})
	hub.Register(other)
	jm := newTestJobManager(t, &fakeK8s{})
	jm.GitHubOrg, jm.ExplicitGitURLBase = "", ""
	hub.SetJobManager(jm)
	a := NewAPI(hub, nil, "fixture-daemon", nil, "")
	origins := func(repo, daemon string) (map[string]string, LaunchTarget) {
		options, err := a.DiscoverLaunchOptions(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, LaunchOptionsRequest{Repo: repo, DaemonID: daemon, Runtime: "daemon", Kind: "agent"})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		var cluster LaunchTarget
		for _, target := range options.Targets {
			if target.DaemonID == "" {
				cluster = target
			}
			if target.RepoOrigin != nil {
				out[target.DaemonID] = target.RepoOrigin.Provider + ":" + target.RepoOrigin.FullName
			}
		}
		return out, cluster
	}
	got, cluster := origins("app", "desktop-a")
	if len(got) != 1 || got["desktop-a"] != "github:example-org/app" {
		t.Fatalf("named desktop: %v", got)
	}
	if cluster.ResolvesBareRepos {
		t.Fatal("no default org or git base, yet the cluster says it resolves bare names")
	}
	if got, _ := origins("scratch", "desktop-a"); len(got) != 0 {
		t.Fatalf("a folder with no remote on the named desktop borrowed another desktop's: %v", got)
	}
	if got, _ := origins("app", ""); len(got) != 0 {
		t.Fatalf("no desktop named, yet a remote was reported: %v", got)
	}
	if got, _ := origins("example-org/app", "desktop-a"); len(got) != 0 {
		t.Fatalf("owner/name needs no resolving: %v", got)
	}
	jm.GitHubOrg = "example-org"
	if _, cluster := origins("app", "desktop-a"); !cluster.ResolvesBareRepos {
		t.Fatal("a default org resolves bare names on the cluster")
	}
}
