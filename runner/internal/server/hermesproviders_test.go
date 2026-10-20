package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/models"
)

// The shared fragment as an operator writes it. The key is synthetic; the
// addresses are a ULA prefix so no real network appears in a test.
const sharedHermesFragment = `model:
  provider: custom
  base_url: http://[fd12:3456::10]:8000/v1
  default: nemotron-super
providers:
  local-super-vl:
    base_url: http://[fd12:3456::11]:8000/v1
    api_key: fixture-provider-secret
`

func hermesShared(config string) HermesSharedConfig {
	return HermesSharedConfig{
		Config: config,
		Probe: func(_ context.Context, data []byte) ([]models.Model, error) {
			return []models.Model{{ID: "nemotron-super", Name: "nemotron-super"}, {ID: "custom:local-super-vl/nemotron-nano", Name: "custom:local-super-vl / nemotron-nano"}}, nil
		},
		Local: func(data []byte) bool { return strings.Contains(string(data), "fd12:3456") },
	}
}

func createdSessionSecretData(t *testing.T, f *fakeK8s) map[string]string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.createdSecrets) != 1 {
		t.Fatalf("per-session secrets = %d, want 1", len(f.createdSecrets))
	}
	raw, _ := json.Marshal(f.createdSecrets[0]["stringData"])
	out := map[string]string{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestCreateSessionJobDeliversSharedHermesProviders(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.Hermes = hermesShared(sharedHermesFragment)
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s-hermes", Repo: "r", Engine: "hermes"}); err != nil {
		t.Fatal(err)
	}
	if got := createdSessionSecretData(t, f)[hermesProvidersEnvVar]; got != sharedHermesFragment {
		t.Fatalf("per-session secret %s = %q, want the shared fragment", hermesProvidersEnvVar, got)
	}
	raw, _ := json.Marshal(f.created[0])
	body := string(raw)
	if !strings.Contains(body, `"key":"`+hermesProvidersEnvVar+`","name":"blerg-runner-session-s-hermes"`) {
		t.Errorf("Job does not reference the fragment from the per-session Secret:\n%s", body)
	}
	if countEnvEntries(t, f.created[0], hermesProvidersEnvVar) != 1 {
		t.Error("fragment env entry missing or duplicated")
	}
	for _, leak := range []string{"fixture-provider-secret", "fd12:3456", "nemotron-super"} {
		if strings.Contains(body, leak) {
			t.Errorf("Job spec carries a fragment value %q", leak)
		}
		if strings.Contains(logs.String(), leak) {
			t.Errorf("log carries a fragment value %q", leak)
		}
	}
}

func TestCreateSessionJobSharedHermesProvidersOnlyForHermes(t *testing.T) {
	for _, engine := range []string{"", "claude", "codex"} {
		f := &fakeK8s{}
		jm := newTestJobManager(t, f)
		jm.Hermes = hermesShared(sharedHermesFragment)
		if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s-other", Repo: "r", Engine: engine}); err != nil {
			t.Fatal(err)
		}
		if _, ok := createdSessionSecretData(t, f)[hermesProvidersEnvVar]; ok {
			t.Errorf("engine %q received the Hermes fragment", engine)
		}
		if countEnvEntries(t, f.created[0], hermesProvidersEnvVar) != 0 {
			t.Errorf("engine %q Job references the Hermes fragment", engine)
		}
	}
}

// A cron's session never runs on operator-supplied material; the fragment
// may hold an operator provider key.
func TestCreateSessionJobSharedHermesProvidersWithheldFromCron(t *testing.T) {
	f := &fakeK8s{credentialFetchResponses: map[string][]byte{"acct-1:hermes": []byte("OPENROUTER_API_KEY=x\n")}}
	jm := newTestJobManager(t, f)
	jm.Hermes = hermesShared(sharedHermesFragment)
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s-cron", NoRepo: true, Engine: "hermes", SpawningAccountID: "acct-1", AuthSessionID: "auth-1", NoOperatorFallback: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := createdSessionSecretData(t, f)[hermesProvidersEnvVar]; ok {
		t.Error("a cron session received the operator's shared Hermes providers")
	}
}

// No shared configuration: a Hermes Job is exactly what it was before.
func TestCreateSessionJobWithoutSharedHermesProvidersUnchanged(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s-plain", Repo: "r", Engine: "hermes"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := createdSessionSecretData(t, f)[hermesProvidersEnvVar]; ok {
		t.Error("fragment key present with no shared configuration")
	}
	if countEnvEntries(t, f.created[0], hermesProvidersEnvVar) != 0 {
		t.Error("Job references a fragment that does not exist")
	}
}

func TestCreateSessionJobHermesProvidersKeyIsReserved(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.Hermes = hermesShared(sharedHermesFragment)
	err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s-res", Repo: "r", Engine: "hermes", ExtraEnv: map[string]string{hermesProvidersEnvVar: "model: {}"}})
	if err == nil {
		t.Fatal("a caller supplied the shared Hermes provider configuration")
	}
	if len(f.created) != 0 {
		t.Fatal("created a Job after refusing a reserved key")
	}
}

func TestSharedHermesProvidersMakeHermesAvailable(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if slices.Contains(jm.AvailableEngines(), "hermes") {
		t.Fatal("hermes available with nothing configured")
	}
	jm = newTestJobManager(t, f)
	jm.Hermes = hermesShared(sharedHermesFragment)
	if !slices.Contains(jm.AvailableEngines(), "hermes") {
		t.Errorf("AvailableEngines() = %v, want hermes", jm.AvailableEngines())
	}
	got, ok := jm.discoverCredentialEngines(context.Background())
	if !ok || !slices.Contains(got, "hermes") {
		t.Errorf("discoverCredentialEngines() = %v %v, want hermes", got, ok)
	}
}

func TestHermesSharedFromEnv(t *testing.T) {
	t.Setenv(hermesProvidersEnvVar, sharedHermesFragment)
	if got := hermesSharedFromEnv(); got.Config != sharedHermesFragment {
		t.Errorf("Config = %q", got.Config)
	}
	t.Setenv(hermesProvidersEnvVar, "  \n")
	if got := hermesSharedFromEnv(); got.Config != "" {
		t.Error("blank fragment counted as configured")
	}
	// One environment entry over 128 KiB fails execve (E2BIG): the runner
	// and every Hermes pod would not start. 64 KiB is the cap.
	t.Setenv(hermesProvidersEnvVar, "model: {}\n"+strings.Repeat("#", 64<<10))
	if got := hermesSharedFromEnv(); got.Config != "" {
		t.Error("fragment over 64 KiB accepted")
	}
	t.Setenv(hermesProvidersEnvVar, "model: {}\n"+strings.Repeat("#", (64<<10)-20))
	if got := hermesSharedFromEnv(); got.Config == "" {
		t.Error("fragment under 64 KiB refused")
	}
}

func TestClusterHermesModelsFromSharedProviders(t *testing.T) {
	api, token, core := newCredentialsAPI(t, nil)
	defer core.Close()
	var mu sync.Mutex
	var probed []string
	jm := &JobManager{Image: "fixture-agent", Hermes: hermesShared(sharedHermesFragment)}
	inner := jm.Hermes.Probe
	jm.Hermes.Probe = func(ctx context.Context, data []byte) ([]models.Model, error) {
		mu.Lock()
		probed = append(probed, string(data))
		mu.Unlock()
		return inner(ctx, data)
	}
	api.hub.SetJobManager(jm)
	w, list := getModels(t, api, "hermes?runtime=cluster", token)
	deadline := time.Now().Add(15 * time.Second)
	for (list.Refreshing || len(list.Models) == 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		w, list = getModels(t, api, "hermes?runtime=cluster", token)
	}
	if w.Code != http.StatusOK || list.Source != models.SourceLive || len(list.Models) != 2 {
		t.Fatalf("catalog: %d %+v", w.Code, list)
	}
	if list.Models[1].ID != "custom:local-super-vl/nemotron-nano" || list.Models[1].Name != "custom:local-super-vl / nemotron-nano" {
		t.Errorf("routing ID or display name changed: %+v", list.Models[1])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(probed) == 0 || probed[0] != sharedHermesFragment {
		t.Errorf("probe did not receive the shared fragment: %q", probed)
	}
	if strings.Contains(w.Body.String(), "fixture-provider-secret") || strings.Contains(w.Body.String(), "fd12:3456") {
		t.Error("catalog response carries fragment values")
	}
}

func TestLaunchOptionsOfferSharedHermesModels(t *testing.T) {
	h := NewHub()
	jm := newTestJobManager(t, &fakeK8s{})
	jm.Hermes = hermesShared(sharedHermesFragment)
	h.SetJobManager(jm)
	a := NewAPI(h, nil, "fixture-daemon", nil, "")
	p := runnerPrincipal{Kind: runnerKeyPrincipalKind}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		options, problem := a.DiscoverLaunchOptions(context.Background(), p, LaunchOptionsRequest{Runtime: "cluster"})
		if problem != nil {
			t.Fatal(problem)
		}
		for _, e := range options.Engines {
			if e.ID == "hermes" && !e.Models.Refreshing && len(e.Models.Models) == 2 {
				if e.Models.Models[1].ID != "custom:local-super-vl/nemotron-nano" {
					t.Fatalf("models = %+v", e.Models.Models)
				}
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cluster launch options never offered the shared Hermes models")
}

// A changed fragment is a different catalog: the cache key moves with it, so
// a stored list for the old providers is never served for the new ones.
func TestClusterHermesCatalogKeyFollowsConfig(t *testing.T) {
	a := &JobManager{Image: "img", Hermes: hermesShared(sharedHermesFragment)}
	b := &JobManager{Image: "img", Hermes: hermesShared(sharedHermesFragment + "# changed\n")}
	if a.catalogImageKey("hermes") == b.catalogImageKey("hermes") {
		t.Error("config change kept the catalog key")
	}
	if a.catalogImageKey("codex") != "img" || b.catalogImageKey("codex") != "img" {
		t.Error("other engines' catalog key changed")
	}
	if strings.Contains(a.catalogImageKey("hermes"), "fd12") {
		t.Error("catalog key carries fragment text")
	}
}

func TestHermesSharedLocal(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if jm.HermesSharedLocal(context.Background()) {
		t.Fatal("local with no shared configuration")
	}
	jm.Hermes = hermesShared(sharedHermesFragment)
	if !jm.HermesSharedLocal(context.Background()) {
		t.Fatal("private-LAN shared configuration with no operator .env is not local")
	}
	remote := newTestJobManager(t, f)
	remote.Hermes = hermesShared("model:\n  provider: custom\n  base_url: https://example.invalid/v1\n")
	if remote.HermesSharedLocal(context.Background()) {
		t.Error("remote shared configuration claimed local")
	}
	// An operator .env can hold provider keys the session would also get.
	withEnv := newTestJobManager(t, &fakeK8s{secrets: map[string]map[string]string{"blerg-runner-agent": {"HERMES_ENV_CONTENTS": b64("OPENROUTER_API_KEY=x")}}})
	withEnv.Hermes = hermesShared(sharedHermesFragment)
	if withEnv.HermesSharedLocal(context.Background()) {
		t.Error("local despite an operator Hermes .env")
	}
}

func TestHermesLocalOnCluster(t *testing.T) {
	jm := newTestJobManager(t, &fakeK8s{})
	jm.Hermes = hermesShared(sharedHermesFragment)
	if !hermesLocalOnCluster(context.Background(), jm, nil, "", noPersonalHermes) {
		t.Fatal("shared local providers, no personal credential: want local")
	}
	// A Hermes parent pod's report can only take "local" away: its files are
	// the agent's to edit, and a child gets the owner's saved .env, not the
	// parent's copy.
	pod := &DaemonConn{ID: "pod", Name: "runner-s", send: make(chan []byte, 1)}
	pod.SetAvailableEngines([]string{"hermes"})
	if hermesLocalOnCluster(context.Background(), jm, pod, "hermes", noPersonalHermes) {
		t.Error("pod said not local, rule says local")
	}
	pod.SetLocalEngines([]string{"hermes"})
	if !hermesLocalOnCluster(context.Background(), jm, pod, "hermes", noPersonalHermes) {
		t.Error("pod and server rule both say local, rule says not")
	}
	hasPersonal := func(context.Context) (bool, bool) { return true, true }
	if hermesLocalOnCluster(context.Background(), jm, pod, "hermes", hasPersonal) {
		t.Error("pod reports local but the owner saved a Hermes credential: claimed local")
	}
	if hermesLocalOnCluster(context.Background(), jm, pod, "hermes", nil) {
		t.Error("pod reports local with no credential lookup: claimed local")
	}
	if hermesLocalOnCluster(context.Background(), newTestJobManager(t, &fakeK8s{}), pod, "hermes", noPersonalHermes) {
		t.Error("pod reports local with no shared configuration: claimed local")
	}
	// A Claude parent's pod knows nothing about Hermes: the shared rule decides.
	if !hermesLocalOnCluster(context.Background(), jm, pod, "claude", noPersonalHermes) {
		t.Error("a non-Hermes pod's report was used")
	}
	plain := newTestJobManager(t, &fakeK8s{})
	if hermesLocalOnCluster(context.Background(), plain, nil, "", noPersonalHermes) {
		t.Error("local without a shared configuration")
	}
	reply := budgetForCluster(nil, []string{"hermes"}, true, []string{"hermes"}, time.Now())
	if hm := findBudgetEngine(reply, "hermes"); hm == nil || !hm.Local || hm.Note != budgetLocalNote {
		t.Errorf("budget hermes = %+v", hm)
	}
	reply = budgetForCluster(nil, []string{"hermes"}, true, nil, time.Now())
	if hm := findBudgetEngine(reply, "hermes"); hm.Local || hm.Note != budgetRemoteNote {
		t.Errorf("budget hermes, not local = %+v", hm)
	}
}

func noPersonalHermes(context.Context) (bool, bool) { return false, true }

// The desktop rule withholds "local" when ~/.hermes/.env holds a key line. On
// the cluster the person's own saved Hermes credential becomes that .env in
// every Hermes pod of theirs, so its presence alone withholds the claim, and
// a lookup that cannot answer withholds it too.
func TestClusterHermesPersonalCredentialIsNotLocal(t *testing.T) {
	jm := newTestJobManager(t, &fakeK8s{})
	jm.Hermes = hermesShared(sharedHermesFragment)
	for name, c := range map[string]struct {
		personal func(context.Context) (bool, bool)
		local    bool
	}{
		"no personal credential": {func(context.Context) (bool, bool) { return false, true }, true},
		"personal credential":    {func(context.Context) (bool, bool) { return true, true }, false},
		"lookup failed":          {func(context.Context) (bool, bool) { return false, false }, false},
		"no lookup":              {nil, false},
	} {
		if got := hermesLocalOnCluster(context.Background(), jm, nil, "", c.personal); got != c.local {
			t.Errorf("%s: local = %v, want %v", name, got, c.local)
		}
	}
}

// A config bundle that carries a ~/.hermes/config.yaml becomes the base every
// Hermes pod merges onto; its providers are unknown here, so not local.
func TestHermesSharedLocalRefusesBundledHermesConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BLERG_RUNNER_DATA_DIR", dir)
	jm := newTestJobManager(t, &fakeK8s{})
	jm.Hermes = hermesShared(sharedHermesFragment)
	writeBundle := func(names ...string) {
		t.Helper()
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, n := range names {
			_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o600, Size: 2, Typeflag: tar.TypeReg})
			_, _ = tw.Write([]byte("x\n"))
		}
		_ = tw.Close()
		_ = gz.Close()
		if err := os.WriteFile(filepath.Join(dir, "agent-config.tar.gz"), buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !jm.HermesSharedLocal(context.Background()) {
		t.Fatal("no bundle: want local")
	}
	writeBundle(".claude/CLAUDE.md")
	if !jm.HermesSharedLocal(context.Background()) {
		t.Fatal("bundle without Hermes config: want local")
	}
	writeBundle(".claude/CLAUDE.md", ".hermes/config.yaml")
	if jm.HermesSharedLocal(context.Background()) {
		t.Fatal("bundle with a Hermes config: claimed local")
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-config.tar.gz"), []byte("not a tarball"), 0o600); err != nil {
		t.Fatal(err)
	}
	if jm.HermesSharedLocal(context.Background()) {
		t.Fatal("unreadable bundle: claimed local")
	}
}

// Crons pick from personal credentials only: the shared fragment never makes
// Hermes a cron choice, and a cron Hermes start without the person's own
// credential is refused with the cron rule's message.
func TestSharedHermesProvidersNeverEnableCrons(t *testing.T) {
	h := NewHub()
	jm := newTestJobManager(t, &fakeK8s{})
	jm.Hermes = hermesShared(sharedHermesFragment)
	h.SetJobManager(jm)
	a := NewAPI(h, nil, "fixture-daemon", nil, "")
	a.SetCoreCredentials(jm.CoreURL, testInternalKey)
	p := runnerPrincipal{Kind: "human", Sub: "acct-1", Sid: "sid-1"}
	deadline := time.Now().Add(15 * time.Second)
	verified := false
	for time.Now().Before(deadline) && !verified {
		engines, known, _ := a.clusterReadinessSnapshot(context.Background(), p, false, true)
		if slices.Contains(engines, "hermes") {
			t.Fatal("shared providers made Hermes a cron choice")
		}
		verified = known
		time.Sleep(time.Millisecond)
	}
	if !verified {
		t.Fatal("personal-only readiness never verified")
	}
	f := &fakeK8s{}
	cron := newTestJobManager(t, f)
	cron.Hermes = hermesShared(sharedHermesFragment)
	err := cron.CreateSessionJob(SessionJobSpec{SessionID: "s-cron2", NoRepo: true, Engine: "hermes", SpawningAccountID: "acct-1", AuthSessionID: "auth-1", NoOperatorFallback: true})
	if !errors.Is(err, ErrNoPersonalCredential) || len(f.created) != 0 {
		t.Fatalf("cron Hermes without a personal credential: err=%v jobs=%d", err, len(f.created))
	}
}

func findBudgetEngine(r budgetReply, engine string) *budgetEngine {
	for i := range r.Engines {
		if r.Engines[i].Engine == engine {
			return &r.Engines[i]
		}
	}
	return nil
}

// The cluster budget and local_only rules use one rule, with the parent's recorded launch proof:
// the agent's own budget call (its session token proves nothing to core) reports Hermes local, and a
// saved personal Hermes credential withholds it.
func TestClusterBudgetHermesLocalWithLaunchProof(t *testing.T) {
	cx := newClusterDelegFx(t)
	jm := cx.hub.JobManager()
	jm.Hermes = hermesShared(sharedHermesFragment)
	get := func(session, tok string) map[string]any {
		t.Helper()
		code, body, _ := cx.do("GET", "/api/sessions/"+session+"/budget", tok, nil, nil)
		if code != http.StatusOK || body["daemon"] != clusterDaemonLabel {
			t.Fatalf("cluster budget: %d %v", code, body)
		}
		return budgetEngineOf(t, body, "hermes")
	}
	if hm := get(cx.cparent, cx.cparentTk); hm["local"] != true || hm["available"] != true {
		t.Errorf("own token, launch proof, no personal Hermes credential: hermes = %v", hm)
	}
	cx.k8s.mu.Lock()
	cx.k8s.credentialKinds = []string{"claude", "hermes"}
	cx.k8s.mu.Unlock()
	jm.expireHermesPersonalCache() // the owner's answer is cached for 30 s
	if hm := get(cx.cparent, cx.cparentTk); hm["local"] != nil {
		t.Errorf("personal Hermes credential: hermes = %v, want not local", hm)
	}
	cx.k8s.mu.Lock()
	cx.k8s.credentialKinds = nil
	cx.k8s.mu.Unlock()
	jm.expireHermesPersonalCache()
	// No recorded launch proof: core cannot be asked, so not local.
	bare, bareTok := cx.session(sessOpts{owner: "owner-1", runtime: runnerRuntimeCluster})
	if hm := get(bare, bareTok); hm["local"] != nil {
		t.Errorf("no proof: hermes = %v, want not local", hm)
	}
}

// A local_only Hermes rule on the cluster starts a Hermes child when the shared providers are local
// (fix 5 alone never matched one), and asks the person once a personal Hermes credential makes the
// claim untrue.
func TestClusterLocalOnlyHermesRule(t *testing.T) {
	cx := newClusterDelegFx(t)
	cx.hub.JobManager().Hermes = hermesShared(sharedHermesFragment)
	rule, err := db.InsertDelegationRule(context.Background(), cx.pool, db.DelegationRule{AccountID: "owner-1", Engine: "hermes",
		Runtime: runnerRuntimeCluster, DaemonID: clusterDaemonID, CreatedVia: "settings", LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	waitClusterReady(t, cx, "hermes")
	code, out, _ := cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "x", "engine": "hermes"}, newUUID())
	if code != http.StatusCreated || out["via"] != "rule:"+rule.ID {
		t.Fatalf("local Hermes under a local_only rule = %d %v", code, out)
	}
	cx.k8s.mu.Lock()
	cx.k8s.credentialKinds = []string{"hermes"}
	cx.k8s.mu.Unlock()
	cx.hub.JobManager().expireHermesPersonalCache() // the owner's answer is cached for 30 s
	code, out, _ = cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "y", "engine": "hermes"}, newUUID())
	if code != http.StatusAccepted {
		t.Fatalf("personal Hermes credential under a local_only rule = %d %v, want the person asked", code, out)
	}
}

// The owner's credential-kind answer is cached briefly: a burst of budget calls and child starts
// asks core once, and the answer still changes within the cache window's bound.
func TestClusterLocalEnginesCachesTheCoreAnswer(t *testing.T) {
	cx := newClusterDelegFx(t)
	jm := cx.hub.JobManager()
	jm.Hermes = hermesShared(sharedHermesFragment)
	row := cx.row(cx.cparent)
	p := cx.api.delegatePrincipalFor(context.Background(), row, "")
	calls := func() int {
		cx.k8s.mu.Lock()
		defer cx.k8s.mu.Unlock()
		return cx.k8s.credentialListCalls
	}
	before := calls()
	for range 5 {
		if got := cx.api.clusterLocalEngines(context.Background(), row, p); !slices.Equal(got, []string{"hermes"}) {
			t.Fatalf("local = %v", got)
		}
	}
	if n := calls() - before; n != 1 {
		t.Fatalf("core list calls = %d, want 1 within the cache window", n)
	}
	// Past the window the answer is asked again.
	jm.expireHermesPersonalCache()
	cx.k8s.mu.Lock()
	cx.k8s.credentialKinds = []string{"hermes"}
	cx.k8s.mu.Unlock()
	if got := cx.api.clusterLocalEngines(context.Background(), row, p); got != nil {
		t.Fatalf("after expiry with a saved Hermes credential: local = %v", got)
	}
}

// The approval panel shows a local-only Hermes rule as standing on the cluster when the rule holds;
// the answer is computed before the panel takes the account and parent-row locks.
func TestClusterApprovalPanelLocalOnlyHermesRule(t *testing.T) {
	cx := newClusterDelegFx(t)
	cx.hub.JobManager().Hermes = hermesShared(sharedHermesFragment)
	if _, err := db.InsertDelegationRule(context.Background(), cx.pool, db.DelegationRule{AccountID: "owner-1", Engine: "hermes",
		Runtime: runnerRuntimeCluster, DaemonID: clusterDaemonID, CreatedVia: "settings", LocalOnly: true}); err != nil {
		t.Fatal(err)
	}
	waitClusterReady(t, cx, "hermes")
	code, view := cx.approval(cx.cparent, cx.human("owner-1"))
	if code != http.StatusOK {
		t.Fatalf("panel = %d %v", code, view)
	}
	if !strings.Contains(string(mustJSON(t, view)), `"standing_rule_engines":["hermes"]`) {
		t.Fatalf("local-only Hermes rule not standing: %s", mustJSON(t, view))
	}
}

// waitClusterReady waits until the owner's cluster readiness lists engine.
func waitClusterReady(t *testing.T, cx *clusterDelegFx, engine string) {
	t.Helper()
	p := cx.api.delegatePrincipalFor(context.Background(), cx.row(cx.cparent), "")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if got, known := cx.api.clusterAvailableEngines(context.Background(), p); known && slices.Contains(got, engine) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("cluster readiness never listed %s", engine)
}

// A cron's session never runs on operator-supplied material, and neither does a cluster child it
// starts: the child's Job gets neither the shared Hermes providers nor the operator fallback.
func TestClusterChildOfCronGetsNoSharedProviders(t *testing.T) {
	cx := newClusterDelegFx(t)
	cx.hub.JobManager().Hermes = hermesShared(sharedHermesFragment)
	cx.k8s.mu.Lock()
	cx.k8s.credentialFetchResponses["owner-1:hermes"] = []byte("OPENROUTER_API_KEY=x\n")
	cx.k8s.mu.Unlock()
	start := func(parent, tok string) string {
		t.Helper()
		id := cx.ask(parent, tok, map[string]any{"no_repo": true, "prompt": "x", "engine": "hermes"})
		code, out := cx.decide(id, cx.human("owner-1"), choice("once"))
		child := asString(out["child_session_id"])
		if code != http.StatusOK || child == "" {
			t.Fatalf("decide = %d %v", code, out)
		}
		return child
	}
	secretOf := func(child string) map[string]string {
		t.Helper()
		cx.k8s.mu.Lock()
		defer cx.k8s.mu.Unlock()
		for _, sec := range cx.k8s.createdSecrets {
			if meta, _ := sec["metadata"].(map[string]any); meta != nil && meta["name"] == sessionSecretName(child) {
				raw, _ := json.Marshal(sec["stringData"])
				out := map[string]string{}
				_ = json.Unmarshal(raw, &out)
				return out
			}
		}
		t.Fatalf("no per-session Secret for %s", child)
		return nil
	}
	waitClusterReady(t, cx, "hermes")
	// Control: a person's cluster parent's Hermes child gets the shared providers.
	if _, ok := secretOf(start(cx.cparent, cx.cparentTk))[hermesProvidersEnvVar]; !ok {
		t.Fatal("control: a direct parent's Hermes child did not get the shared providers")
	}
	cron, cronTok := cx.session(sessOpts{owner: "owner-1", runtime: runnerRuntimeCluster, startedByKind: "cron"})
	if err := db.SetSessionLaunchAuthSession(context.Background(), cx.pool, cron, clusterParentSID); err != nil {
		t.Fatal(err)
	}
	child := start(cron, cronTok)
	if _, ok := secretOf(child)[hermesProvidersEnvVar]; ok {
		t.Fatal("a cron's cluster child received the operator's shared Hermes providers")
	}
	// The child's pod goes away; the cron parent's next message resumes it. The resumed pod is
	// still a cron's child: no shared providers, no operator credential.
	if _, err := cx.pool.Exec(context.Background(), `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, child); err != nil {
		t.Fatal(err)
	}
	cx.k8s.finishSessionJob(child)
	cx.k8s.mu.Lock()
	jobsBefore, secretsBefore := len(cx.k8s.created), len(cx.k8s.createdSecrets)
	cx.k8s.mu.Unlock()
	code, out, _ := cx.do("POST", childRoute(cron, child, "/message"), cronTok, map[string]any{"text": "continue"}, nil)
	if code != http.StatusAccepted || out["resumed"] != true {
		t.Fatalf("message = %d %v", code, out)
	}
	cx.k8s.mu.Lock()
	newSecrets := append([]map[string]any{}, cx.k8s.createdSecrets[secretsBefore:]...)
	newJobs := append([]map[string]any{}, cx.k8s.created[jobsBefore:]...)
	cx.k8s.mu.Unlock()
	if len(newSecrets) == 0 || len(newJobs) == 0 {
		t.Fatalf("the message did not resume the child (%d secrets, %d jobs)", len(newSecrets), len(newJobs))
	}
	raw, _ := json.Marshal(newSecrets[len(newSecrets)-1]["stringData"])
	if strings.Contains(string(raw), hermesProvidersEnvVar) {
		t.Error("the resumed cron child received the operator's shared Hermes providers")
	}
	for _, j := range newJobs {
		jraw, _ := json.Marshal(j)
		if strings.Contains(string(jraw), `"name":"blerg-runner-agent"`) {
			t.Errorf("the resumed cron child's Job reads the operator Secret: %s", jraw)
		}
	}
}
