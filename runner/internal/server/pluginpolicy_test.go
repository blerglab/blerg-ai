package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestMarketplacePolicyRefreshReachesServerAndEveryDaemon(t *testing.T) {
	fallback, _ := pluginspec.ParseAllowlist("example-org/fallback")
	defaults, _ := pluginspec.ParseAllowlist("")
	policy := pluginspec.PolicyFromAllowlist(defaults)
	policy.UpdatedBy = "private-admin-fixture"
	var mu sync.Mutex
	unavailable := false
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/plugins/marketplace-policy" || r.Header.Get("X-Internal-Key") != "policy-fixture" {
			t.Error("unverified policy request")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if unavailable {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(policy)
	}))
	defer core.Close()
	hub := NewHub()
	t.Setenv("BLERG_RUNNER_DATA_DIR", t.TempDir())
	a := NewAPI(hub, nil, "", nil, "")
	a.pluginAllow = fallback
	a.SetCoreCredentials(core.URL, "policy-fixture")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := a.StartPluginPolicy(ctx); err != nil {
		t.Fatal(err)
	}
	cancel() // Further refreshes are driven explicitly below.
	daemon := &DaemonConn{ID: "desktop", PluginPolicy: true, send: make(chan []byte, 8)}
	pod := &DaemonConn{ID: "pod", PluginPolicy: true, BoundSessionID: "pod-session", send: make(chan []byte, 8)}
	legacy := &DaemonConn{ID: "legacy", send: make(chan []byte, 8)}
	for _, dc := range []*DaemonConn{daemon, pod, legacy} {
		hub.Register(dc)
	}
	receive := func(dc *DaemonConn, revision int64, available bool) protocol.PluginMarketplacePolicy {
		t.Helper()
		deadline := time.NewTimer(8 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case raw := <-dc.send:
				var msg protocol.PluginMarketplacePolicy
				if json.Unmarshal(raw, &msg) != nil || msg.Type != "plugin_marketplace_policy" || msg.SessionID != dc.BoundSessionID {
					t.Fatalf("wrong daemon policy frame %s", raw)
				}
				if msg.Policy != nil && msg.Policy.UpdatedBy != "" {
					t.Fatal("daemon received administrator ID")
				}
				if available && msg.Policy != nil && msg.Policy.Revision == revision || !available && msg.Policy == nil {
					return msg
				}
			case <-deadline.C:
				t.Fatal("policy update never reached daemon")
				return protocol.PluginMarketplacePolicy{}
			}
		}
	}
	receive(daemon, 1, true)
	receive(pod, 1, true)
	if !a.currentPluginAllow().Allows("codex:openai-curated-remote") || a.currentPluginAllow().Allows("example-org/fallback") {
		t.Fatal("server intersected Core with stale environment")
	}
	jm := &JobManager{PluginAllow: fallback, MarketplacePolicy: a.marketplacePolicy}
	env := jm.pluginEnv(SessionJobSpec{Plugins: []pluginspec.Entry{{Marketplace: "codex:openai-curated-remote", Plugin: "selected"}}})
	if len(env) != 2 || env[1]["value"] != pluginspec.DefaultMarketplaces+"," {
		t.Fatal("cluster did not receive Core policy", env)
	}
	mu.Lock()
	policy.Revision++
	for i := range policy.Marketplaces {
		policy.Marketplaces[i].Enabled = false
	}
	mu.Unlock()
	a.refreshPluginPolicy(t.Context())
	receive(daemon, 2, true)
	receive(pod, 2, true)
	if a.currentPluginAllow().Allows("codex:openai-curated-remote") || jm.currentPluginAllow().Allows("codex:openai-curated") {
		t.Fatal("policy toggle did not take effect")
	}
	env = jm.pluginEnv(SessionJobSpec{Plugins: []pluginspec.Entry{{Marketplace: "example-org/old", Plugin: "selected"}}})
	if env[1]["value"] != "," {
		t.Fatal("deny-all must not restore pod defaults", env)
	}

	for _, revision := range []int64{1, 2} {
		mu.Lock()
		policy.Revision = revision
		for i := range policy.Marketplaces {
			policy.Marketplaces[i].Enabled = true
		}
		mu.Unlock()
		a.refreshPluginPolicy(t.Context())
		receive(daemon, 2, true)
		receive(pod, 2, true)
		if a.currentPluginAllow().Allows("codex:openai-curated-remote") {
			t.Fatalf("Core revision %d restored revoked permission", revision)
		}
	}
	mu.Lock()
	unavailable = true
	mu.Unlock()
	a.refreshPluginPolicy(t.Context())
	receive(daemon, 2, true)
	receive(pod, 2, true)
	if a.currentPluginAllow().Allows("example-org/fallback") {
		t.Fatal("Core failure restored a broader environment fallback")
	}
	restarted := NewAPI(NewHub(), nil, "", nil, "")
	restarted.pluginAllow = fallback
	if err := restarted.StartPluginPolicy(t.Context()); err != nil {
		t.Fatal(err)
	}
	if restarted.currentPluginAllow().Allows("example-org/fallback") || restarted.currentPluginAllow().Allows("codex:openai-curated") {
		t.Fatal("server restart lost revoke while Core unreachable")
	}
	restarted.SetCoreCredentials(core.URL, "policy-fixture")
	restarted.refreshPluginPolicy(t.Context())
	if restarted.currentPluginAllow().Allows("example-org/fallback") {
		t.Fatal("server restart/outage restored fallback")
	}
	for _, revision := range []int64{1, 2} {
		mu.Lock()
		unavailable = false
		policy.Revision = revision
		mu.Unlock()
		restarted.refreshPluginPolicy(t.Context())
		if restarted.currentPluginAllow().Allows("codex:openai-curated") {
			t.Fatal("server reconnect accepted stale/replayed revision", revision)
		}
	}
	mu.Lock()
	policy.Revision = 3
	mu.Unlock()
	restarted.refreshPluginPolicy(t.Context())
	if !restarted.currentPluginAllow().Allows("codex:openai-curated") {
		t.Fatal("server rejected newer revision after restart")
	}
	select {
	case raw := <-legacy.send:
		t.Fatalf("legacy daemon received new frame %s", raw)
	default:
	}
}

func TestMarketplacePolicyTransportRejectsUntrustedResponses(t *testing.T) {
	a, _ := pluginspec.ParseAllowlist("")
	p := pluginspec.PolicyFromAllowlist(a)
	for _, mode := range []string{"version", "missing-default", "oversize", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("followed policy redirect with internal key") }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next := p
				switch mode {
				case "version":
					next.Version = 99
				case "missing-default":
					next.Marketplaces = p.Marketplaces[1:]
				case "oversize":
					_, _ = w.Write(make([]byte, 33<<10))
					return
				case "redirect":
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				}
				_ = json.NewEncoder(w).Encode(next)
			}))
			defer server.Close()
			c := httpSessionPluginCore{url: server.URL, key: "fixture", client: coreHTTPClient(nil)}
			if _, err := c.marketplacePolicy(context.Background()); err == nil {
				t.Fatal("accepted invalid authority", mode)
			}
		})
	}
}

func TestPodPluginPolicyBootstrapUsesCurrentCoreAuthority(t *testing.T) {
	for _, mode := range []string{"current", "unavailable", "missing-core", "messaging", "daemon", "missing-token", "foreign-session", "replaced", "replace-during-core", "stop-during-core"} {
		t.Run(mode, func(t *testing.T) {
			a, _, dc, pool := spawnTokenFixture(t)
			ctx := t.Context()
			const sid = "00000000-0000-4000-8000-000000000923"
			if err := db.InsertClusterSession(ctx, pool, sid, dc.ID, "starting", "/repos/fixture", "fixture", "", ""); err != nil {
				t.Fatal(err)
			}
			runtime, messaging, err := db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			replace := func() {
				if _, _, err := db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour); err != nil {
					t.Error(err)
				}
			}
			allow, _ := pluginspec.ParseAllowlist("*")
			// The replica still has an enabling revision. Bootstrap must bypass it.
			a.marketplacePolicy = &pluginspec.LivePolicy{}
			if err := a.marketplacePolicy.Apply(pluginspec.PolicyFromAllowlist(allow), 15*time.Second); err != nil {
				t.Fatal(err)
			}
			revoke := pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{})
			revoke.Revision = 3
			revoke.UpdatedBy = "private-admin-fixture"
			var calls atomic.Int32
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/internal/plugins/marketplace-policy" || r.Header.Get("X-Internal-Key") != "bootstrap-fixture" {
					t.Error("unauthenticated Core bootstrap")
				}
				switch mode {
				case "unavailable":
					http.Error(w, "fixture-private-detail", http.StatusServiceUnavailable)
					return
				case "replace-during-core":
					replace()
				case "stop-during-core":
					if _, err := pool.Exec(ctx, `UPDATE sessions SET ended_at=now() WHERE id=$1`, sid); err != nil {
						t.Error(err)
					}
				}
				_ = json.NewEncoder(w).Encode(revoke)
			}))
			defer core.Close()
			a.SetCoreCredentials(core.URL, "bootstrap-fixture")
			sessionID := sid
			switch mode {
			case "missing-core":
				a.SetCoreCredentials("", "")
			case "messaging":
				runtime = messaging
			case "daemon":
				runtime = spawnTokenDaemonTok
			case "missing-token":
				runtime = ""
			case "foreign-session":
				sessionID = "00000000-0000-4000-8000-000000000924"
			case "replaced":
				replace()
			}
			req := httptest.NewRequest(http.MethodGet, "/api/session/plugin-marketplace-policy?session_id="+sessionID, nil)
			req.Header.Set("Authorization", "Bearer "+runtime)
			rec := httptest.NewRecorder()
			a.HandlePodPluginPolicy(rec, req)
			if mode == "current" {
				var msg protocol.PluginMarketplacePolicy
				if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &msg) != nil || msg.SessionID != sid || msg.Policy == nil || msg.Policy.Revision != 3 || msg.Policy.UpdatedBy != "" || msg.Policy.Allowlist().Allows("codex:openai-curated") {
					t.Fatal("replacement bootstrapped from stale replica policy", rec.Code, rec.Body.String())
				}
			} else if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "fixture-private-detail") {
				t.Fatal("bootstrap disclosed unavailable or unauthorized policy", rec.Code)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("bootstrap can be cached")
			}
			switch mode {
			case "current", "unavailable", "replace-during-core", "stop-during-core":
				if calls.Load() != 1 {
					t.Fatal("bootstrap did not read Core exactly once")
				}
			default:
				if calls.Load() != 0 {
					t.Fatal("unauthorized bootstrap reached Core")
				}
			}
		})
	}
}

func TestPodReplacementPolicyWithGeneratedStorageLayout(t *testing.T) {
	f := &fakeK8s{}
	j := newTestJobManager(t, f)
	plan := clusterPlanFixture()
	allow, _ := pluginspec.ParseAllowlist("*")
	j.PluginAllow = allow
	if err := j.createSessionJob(SessionJobSpec{SessionID: plan.SessionID, workspacePlan: &plan, WorkspaceMembers: plan.Members, workspaceClaim: "fixture-workspace", Engine: "claude", Plugins: []pluginspec.Entry{{Marketplace: "anthropics/claude-plugins-official", Plugin: "selected"}}}); err != nil {
		t.Fatal(err)
	}
	pod := f.created[0]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	container := pod["containers"].([]any)[0].(map[string]any)
	var home string
	for _, value := range container["env"].([]any) {
		env := value.(map[string]any)
		if env["name"] == "HOME" {
			home, _ = env["value"].(string)
		}
	}
	if home == "" {
		// The image supplies HOME unless the Job explicitly overrides it.
		image, err := os.ReadFile("../../Dockerfile.devcontainer")
		if err != nil || !strings.Contains(string(image), "ENV HOME=/workspace") {
			t.Fatal("image HOME changed", err)
		}
		home = "/workspace"
	}
	retained := map[string]string{}
	ephemeral := map[string]bool{}
	for _, value := range pod["volumes"].([]any) {
		volume := value.(map[string]any)
		name := volume["name"].(string)
		if volume["persistentVolumeClaim"] != nil {
			retained[name] = t.TempDir()
		}
		if volume["emptyDir"] != nil {
			ephemeral[name] = true
		}
	}
	var mounts []map[string]any
	for _, value := range container["volumeMounts"].([]any) {
		mounts = append(mounts, value.(map[string]any))
	}
	foundHome, foundProjects := false, false
	for _, mount := range mounts {
		foundHome = foundHome || mount["mountPath"] == home && ephemeral[mount["name"].(string)]
		foundProjects = foundProjects || mount["mountPath"] == home+"/projects" && retained[mount["name"].(string)] != ""
	}
	if !foundHome || !foundProjects {
		t.Fatal("fixture no longer models ephemeral HOME with retained projects")
	}
	// Materialize the actual Job mounts: only PVC-backed paths survive replacement.
	replacementHome := func() string {
		root := t.TempDir()
		for _, mount := range mounts {
			target := mount["mountPath"].(string)
			if source := retained[mount["name"].(string)]; source != "" && strings.HasPrefix(target, home+"/") {
				dst := filepath.Join(root, strings.TrimPrefix(target, home+"/"))
				if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(source, dst); err != nil {
					t.Fatal(err)
				}
			}
		}
		return root
	}
	t.Setenv("BLERG_RUNNER_PLUGIN_MARKETPLACES", "*")
	originalHome := replacementHome()
	original := daemon.NewPodPluginWorkshop(filepath.Join(originalHome, ".blerg"))
	enabled := pluginspec.PolicyFromAllowlist(allow)
	disabled := pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{})
	disabled.Revision = 3
	if err := original.BootstrapPolicy(enabled); err != nil {
		t.Fatal(err)
	}
	receive := func(w interface{ ReceivePolicy([]byte, string) }, p pluginspec.Policy) {
		raw, _ := json.Marshal(protocol.PluginMarketplacePolicy{Type: "plugin_marketplace_policy", SessionID: plan.SessionID, Policy: &p, LeaseSeconds: 15})
		w.ReceivePolicy(raw, plan.SessionID)
	}
	receive(original, disabled)
	if original.CurrentAllow().Allows("anthropics/claude-plugins-official") {
		t.Fatal("original pod ignored revoke")
	}
	if err := os.WriteFile(filepath.Join(originalHome, "projects", "retained"), []byte("workspace"), 0600); err != nil {
		t.Fatal(err)
	}
	nextHome := replacementHome()
	if _, err := os.Stat(filepath.Join(nextHome, "projects", "retained")); err != nil {
		t.Fatal("projects did not survive replacement", err)
	}
	if _, err := os.Stat(filepath.Join(nextHome, ".blerg")); !os.IsNotExist(err) {
		t.Fatal("replacement incorrectly retained policy HOME")
	}
	replacement := daemon.NewPodPluginWorkshop(filepath.Join(nextHome, ".blerg"))
	receive(replacement, enabled)
	if replacement.CurrentAllow().Allows("anthropics/claude-plugins-official") {
		t.Fatal("Core outage plus stale replica restored revoked permission after pod replacement")
	}
	if err := replacement.BootstrapPolicy(disabled); err != nil {
		t.Fatal(err)
	}
	receive(replacement, enabled)
	if replacement.CurrentAllow().Allows("anthropics/claude-plugins-official") {
		t.Fatal("replacement accepted a revision below current Core policy")
	}
}

func TestMarketplacePolicyServerRestoreThenRevoke(t *testing.T) {
	allow, _ := pluginspec.ParseAllowlist("*")
	policy := pluginspec.PolicyFromAllowlist(allow)
	policy.PolicyID, policy.Revision = "00000000-0000-4000-8000-000000000001", 7
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(policy) }))
	defer core.Close()
	live, err := pluginspec.NewLivePolicy(filepath.Join(t.TempDir(), "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := NewAPI(NewHub(), nil, "", nil, "")
	a.marketplacePolicy = live
	a.hub.marketplacePolicy = live
	a.SetCoreCredentials(core.URL, "fixture")
	a.refreshPluginPolicy(t.Context())
	policy = pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{})
	policy.PolicyID, policy.Revision = "00000000-0000-4000-8000-000000000002", 4
	a.refreshPluginPolicy(t.Context())
	got, ok := live.Snapshot()
	if !ok || got.PolicyID != policy.PolicyID || got.Revision != 4 || a.currentPluginAllow().Allows("codex:openai-curated") {
		t.Fatal("restore froze revoke", got)
	}
}

func TestMarketplacePolicyServerLogsPersistenceFailure(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	policy := pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{})
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(policy) }))
	defer core.Close()
	dir := t.TempDir()
	live, err := pluginspec.NewLivePolicy(filepath.Join(dir, "blocked", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blocked"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	a := NewAPI(NewHub(), nil, "", nil, "")
	a.marketplacePolicy = live
	a.SetCoreCredentials(core.URL, "fixture")
	a.refreshPluginPolicy(t.Context())
	if !strings.Contains(output.String(), "marketplace policy rejected") {
		t.Fatal("silent persistence failure", output.String())
	}
}
