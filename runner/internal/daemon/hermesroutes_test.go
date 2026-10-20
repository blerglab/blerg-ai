package daemon

import (
	"context"
	"fmt"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHermesNamedProviderDiscovery(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credential sent")
		}
		fmt.Fprint(w, `{"data":[{"id":"same-model"}]}`)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"same-model"},{"id":"super-3.5"}]}`)
	}))
	defer b.Close()
	p := filepath.Join(t.TempDir(), "config.yaml")
	mustWrite(t, p, "model:\n  provider: custom\n  base_url: "+a.URL+"/v1\n  default: same-model\nproviders:\n  local-a:\n    base_url: "+a.URL+"/v1\n  local-super-vl:\n    base_url: "+b.URL+"/v1\n    api_mode: chat_completions\n    context_length: 65536\n")
	got, err := probeHermesModelsAt(context.Background(), p, newHermesProbeClient())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"same-model", "custom:local-super-vl/same-model", "custom:local-super-vl/super-3.5"} {
		if !slices.Contains(modelIDs(got), id) {
			t.Errorf("missing %s in %v", id, modelIDs(got))
		}
	}
}
func TestHermesNamedProvidersBudgetAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		local       bool
	}{
		{"local", "  local-b:\n    base_url: http://127.0.0.1:8001/v1\n", true},
		{"remote", "  other:\n    base_url: https://example.invalid/v1\n", false},
		{"key", "  local-b:\n    base_url: http://127.0.0.1:8001/v1\n    api_key: synthetic\n", false},
		{"disabled", "  other:\n    base_url: https://example.invalid/v1\n    enabled: false\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := t.TempDir()
			t.Setenv("HOME", h)
			mustWrite(t, filepath.Join(h, ".hermes", "config.yaml"), "model:\n  provider: custom\n  base_url: http://127.0.0.1:8000/v1\nproviders:\n"+tc.extra)
			if got := hermesLocal(); got != tc.local {
				t.Fatalf("local=%v want %v", got, tc.local)
			}
		})
	}
}
func TestHermesRouteSelector(t *testing.T) {
	for _, bad := range []string{"custom:missing/x", "custom:local-b/--provider", "custom:local-b/http://example.invalid", "custom:local-b/" + strings.Repeat("x", 128)} {
		d := newHermesDriver(AgentDriverOpts{})
		d.env = []string{"HOME=" + t.TempDir()}
		if _, err := d.resolveModel(context.Background(), bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestHermesRoutingAmbiguousYAMLAndFallbackRefused(t *testing.T) {
	for name, extra := range map[string]string{
		"quoted fallback": "'fallback_model': {provider: paid, model: other}\n",
		"fallback list":   "fallback_providers:\n  - provider: paid\n    model: other\n",
		"merge":           "common: &defaults {fallback_model: {provider: paid, model: other}}\n<<: *defaults\n",
		"duplicate":       "providers:\n  local-b:\n    base_url: http://127.0.0.1:1/v1\n",
	} {
		t.Run(name, func(t *testing.T) {
			h := t.TempDir()
			p := filepath.Join(h, ".hermes", "config.yaml")
			mustWrite(t, p, "model:\n  provider: custom\n  base_url: http://127.0.0.1:1/v1\nproviders:\n  local-b:\n    base_url: http://127.0.0.1:1/v1\n"+extra)
			d := newHermesDriver(AgentDriverOpts{})
			d.env = []string{"HOME=" + h}
			if _, err := d.resolveModel(context.Background(), "custom:local-b/model"); err == nil {
				t.Fatal("accepted fallback/ambiguous YAML")
			}
			t.Setenv("HOME", h)
			if hermesLocal() {
				t.Fatal("classified ambiguous/fallback config local")
			}
		})
	}
}
func TestHermesRouteBindingKeepsQualifiedModel(t *testing.T) {
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Emitter: em, Model: "custom:local-b/same-model"})
	r := newHermesRun(context.Background(), d, nil)
	r.binding = hermesModelRoute{Selector: "custom:local-b/same-model", Model: "same-model", Provider: "custom:local-b"}
	r.info([]byte(`{"model":"same-model","provider":"custom:local-b","reasoning_effort":"low"}`))
	if r.actualModel != "custom:local-b/same-model" || !r.routeConfirmed {
		t.Fatalf("binding lost: %q", r.actualModel)
	}
	r.info([]byte(`{"model":"same-model","provider":"custom:local-a"}`))
	if r.routeConfirmed || r.actualModel != "custom:local-b/same-model" {
		t.Fatal("foreign provider accepted or selector erased")
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, ev := range em.events {
		if ev.Kind == "model_changed" && ev.Payload.(agent.ModelChangedPayload).Model != "custom:local-b/same-model" {
			t.Fatal("bare model acknowledgment escaped")
		}
	}
}

func TestHermesLegacyAndModernProviders(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	body := "model:\n  provider: custom\n  base_url: http://127.0.0.1:8000/v1\ncustom_providers:\n  - name: Old Local\n    base_url: http://127.0.0.1:8002/v1\n    models: [cached-offline-model]\nproviders:\n  local-b:\n    base_url: http://127.0.0.1:8001/v1\n"
	mustWrite(t, p, body)
	cfg, err := readHermesRouteConfig(p)
	if err != nil || len(cfg.Providers) != 2 || cfg.Providers[0].Name != "old-local" {
		t.Fatal("legacy route normalization failed")
	}
	mustWrite(t, p, strings.Replace(body, "local-b:", "old-local:", 1))
	if _, err := readHermesRouteConfig(p); err == nil {
		t.Fatal("legacy/modern provider alias collision accepted")
	}
}
func TestHermesRoutingReloadRefusesChangedAuthority(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, `{"data":[{"id":"same-model"}]}`) }))
	defer srv.Close()
	h := t.TempDir()
	p := filepath.Join(h, ".hermes", "config.yaml")
	body := "model:\n  provider: custom\n  base_url: " + srv.URL + "/v1\nproviders:\n  local-b:\n    base_url: " + srv.URL + "/v1\n"
	mustWrite(t, p, body)
	d := newHermesDriver(AgentDriverOpts{})
	d.env = []string{"HOME=" + h}
	route, err := d.resolveModel(context.Background(), "custom:local-b/same-model")
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{"fallback_model: {provider: paid, model: model}\n", "providers: {}\n"} {
		mustWrite(t, p, body+extra)
		r := newHermesRun(context.Background(), d, nil)
		r.binding = route
		r.routeConfirmed = true
		if r.confirmRoute() {
			t.Fatal("stale authority accepted")
		}
	}
	if calls != 1 {
		t.Fatal("unsafe reload contacted provider")
	}
}
func TestHermesDefaultModelsAreBoundToConfiguredProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"openrouter:paid-model"},{"id":"qwen:latest"}]}`)
	}))
	defer srv.Close()
	h := t.TempDir()
	mustWrite(t, filepath.Join(h, ".hermes", "config.yaml"), "model:\n  provider: custom\n  base_url: "+srv.URL+"/v1\nproviders:\n  local-a:\n    base_url: "+srv.URL+"/v1\n")
	d := newHermesDriver(AgentDriverOpts{})
	d.env = []string{"HOME=" + h}
	for _, model := range []string{"openrouter:paid-model", "qwen:latest"} {
		route, err := d.resolveModel(context.Background(), model)
		if err != nil || route.Provider != "custom:local-a" || route.Model != model || route.Selector != model {
			t.Fatal("raw model escaped default provider binding")
		}
	}
}

func TestHermesNamedUsageRetainsCanonicalProvider(t *testing.T) {
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Emitter: em})
	r := newHermesRun(context.Background(), d, nil)
	r.binding = hermesModelRoute{Selector: "custom:local-b/same-model", Model: "same-model", Provider: "custom:local-b"}
	r.info([]byte(`{"model":"same-model","provider":"custom:local-b","usage":{"input":10,"output":2,"model":"same-model"}}`))
	r.begin()
	r.reportUsage([]byte(`{"input":30,"output":10,"model":"same-model"}`))
	r.finish("end_turn", "")
	if r.ambiguous {
		t.Fatal("raw native model made qualified usage ambiguous")
	}
	for _, e := range em.snapshot() {
		if e.Kind == "turn_done" {
			p := e.Payload.(agent.TurnDonePayload)
			if p.Model != "custom:local-b/same-model" || p.UsageByModel[p.Model].InputTokens != 20 {
				t.Fatal("qualified usage attribution lost")
			}
		}
	}
}
func TestHermesStaleStartupAcknowledgmentDoesNotEraseNewSelection(t *testing.T) {
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Emitter: em, Model: "custom:local-a/same-model"})
	r := newHermesRun(context.Background(), d, nil)
	r.binding = hermesModelRoute{Selector: "custom:local-b/same-model", Model: "same-model", Provider: "custom:local-b"}
	r.info([]byte(`{"model":"same-model","provider":"custom:local-b"}`))
	if countKind(em, "model_changed") != 0 || d.model != "custom:local-a/same-model" {
		t.Fatal("stale startup acknowledgment replaced newer requested provider")
	}
}

func TestHermesQualifiedTerminalSelectionRefused(t *testing.T) {
	for _, kind := range []string{"", "tmux"} {
		if spawnModelProblem(protocol.SpawnSession{Engine: "hermes", Kind: kind, Model: "custom:local-b/model"}) == "" {
			t.Fatal("qualified terminal selection accepted")
		}
	}
	if spawnModelProblem(protocol.SpawnSession{Engine: "hermes", Kind: "agent", Model: "custom:local-b/model"}) != "" {
		t.Fatal("agent selection rejected")
	}
	if spawnModelProblem(protocol.SpawnSession{Engine: "hermes", Kind: "tmux", Model: "old-model"}) != "" {
		t.Fatal("legacy terminal selection changed")
	}
}

func TestHermesRejectedSwitchNeverSendsQueuedInputOnOldProvider(t *testing.T) {
	for _, reject := range []string{"confirm", "rejection"} {
		t.Run(reject, func(t *testing.T) {
			root := fakeHermesGateway(t)
			lines := strings.Split(fakeHermesGatewaySource, "\n")
			for i, line := range lines {
				if strings.HasPrefix(line, " elif method=='session.create':") {
					lines[i] = " elif method=='session.create':result={'session_id':sid,'stored_session_id':key,'info':{'model':'old-model','provider':'custom:local-a','desktop_contract':8}}"
				}
				if line == " else:result={}" {
					response := "result={'confirm_required':True}"
					if reject == "rejection" {
						response = "send({'id':rid,'error':{'code':4002,'message':'not applied'}});continue"
					}
					lines[i] = " elif method=='config.set':" + response + "\n" + line
				}
			}
			source := strings.Join(lines, "\n")
			entry := "def main():\n    " + strings.ReplaceAll(strings.ReplaceAll(source, "global calls", "nonlocal calls"), "\n", "\n    ")
			if os.WriteFile(filepath.Join(root, "tui_gateway", "entry.py"), []byte(entry), 0600) != nil {
				t.Fatal("fixture write")
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"data":[{"id":"old-model"},{"id":"new-model"}]}`)
			}))
			defer srv.Close()
			mustWrite(t, filepath.Join(os.Getenv("HERMES_HOME"), "config.yaml"), "model:\n  provider: custom\n  default: old-model\n  base_url: "+srv.URL+"/v1\nproviders:\n  local-a:\n    base_url: "+srv.URL+"/v1\n  local-b:\n    base_url: "+srv.URL+"/v1\n")
			log := filepath.Join(t.TempDir(), "rpc.log")
			t.Setenv("FAKE_HERMES_RPC_LOG", log)
			em := &collectEmitter{}
			d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Model: "custom:local-a/old-model", Emitter: em})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); d.Run(ctx) }()
			defer func() { cancel(); <-done }()
			select {
			case err := <-d.EngineSessionLoaded():
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("startup timeout")
			}
			d.SetModel("custom:local-b/new-model", "", "")
			d.EnqueueInput(agent.Input{Text: "must not reach old provider", ClientID: "rejected-selection"})
			deadline := time.NewTimer(15 * time.Second)
			defer deadline.Stop()
			for {
				found := false
				for _, e := range em.snapshot() {
					if e.Kind == "error" && e.Payload.(agent.ErrorPayload).ClientID == "rejected-selection" {
						found = true
					}
				}
				if found {
					break
				}
				select {
				case <-deadline.C:
					t.Fatal("input rejection timeout")
				case <-time.After(10 * time.Millisecond):
				}
			}
			raw, _ := os.ReadFile(log)
			if strings.Contains(string(raw), "prompt.submit") || strings.Contains(string(raw), "session.redirect") || countKind(em, "user_message") != 0 {
				t.Fatal("rejected selection sent input on old provider")
			}
		})
	}
}

func TestHermesRouteConfigRecheckedAfterNativeBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"same-model"}]}`) }))
	defer srv.Close()
	h := t.TempDir()
	p := filepath.Join(h, ".hermes", "config.yaml")
	body := "model:\n  provider: custom\n  base_url: " + srv.URL + "/v1\nproviders:\n  local-b:\n    base_url: " + srv.URL + "/v1\n"
	mustWrite(t, p, body)
	d := newHermesDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	d.env = []string{"HOME=" + h}
	route, err := d.resolveModel(context.Background(), "custom:local-b/same-model")
	if err != nil {
		t.Fatal(err)
	}
	d.nativeID = "native-owned"
	d.onSessionID = func(string) { mustWrite(t, p, body+"fallback_model: {provider: paid, model: other}\n") }
	rpc := &hermesRPC{wake: make(chan struct{}, 1), done: make(chan struct{})}
	rpc.backlog = []hermesFrame{{Method: "event", Params: []byte(`{"type":"session.info","session_id":"runtime-owned","payload":{"model":"same-model","provider":"custom:local-b"}}`)}}
	r := newHermesRun(context.Background(), d, rpc)
	r.sid = "runtime-owned"
	r.binding = route
	if r.confirmRoute() {
		t.Fatal("configuration changed during native build was accepted")
	}
}
func TestHermesPrivateBindingChangeRefusesBeforeNativeResume(t *testing.T) {
	for _, change := range []string{"missing", "endpoint", "policy"} {
		t.Run(change, func(t *testing.T) {
			fakeHermesGateway(t)
			backend := func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"same-model"}]}`) }))
			}
			a, b := backend(), backend()
			defer a.Close()
			defer b.Close()
			p := filepath.Join(os.Getenv("HERMES_HOME"), "config.yaml")
			body := "model:\n  provider: custom\n  base_url: " + a.URL + "/v1\nproviders:\n  local-b:\n    base_url: " + a.URL + "/v1\n    context_length: 65536\n"
			mustWrite(t, p, body)
			em := &collectEmitter{}
			d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Model: "custom:local-b/same-model", Emitter: em})
			route, err := d.resolveModel(context.Background(), d.model)
			if err != nil {
				t.Fatal(err)
			}
			fingerprint := route.fingerprint
			if change == "missing" {
				fingerprint = ""
			}
			if err := d.SetEngineSession(&EngineSessionState{Version: 1, Engine: "hermes", ID: "durable-owned", RouteFingerprint: fingerprint}, nil); err != nil {
				t.Fatal(err)
			}
			if change == "endpoint" {
				mustWrite(t, p, strings.ReplaceAll(body, a.URL, b.URL))
			}
			if change == "policy" {
				mustWrite(t, p, strings.ReplaceAll(body, "65536", "131072"))
			}
			log := filepath.Join(t.TempDir(), "rpc.log")
			d.env = append(d.env, "FAKE_HERMES_RPC_LOG="+log)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			d.runGateway(ctx)
			raw, _ := os.ReadFile(log)
			if strings.Contains(string(raw), "session.resume") || strings.Contains(string(raw), "session.create") || strings.Contains(string(raw), "prompt.submit") {
				t.Fatal("changed binding reached native conversation")
			}
			if !d.permanentFailure {
				t.Fatal("binding refusal was not fail closed")
			}
		})
	}
}

func TestHermesDiscoveryDeduplicatesEquivalentAliasesOnly(t *testing.T) {
	backend := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"same-model"}]}`) }))
	}
	a, b := backend(), backend()
	defer a.Close()
	defer b.Close()
	p := filepath.Join(t.TempDir(), "config.yaml")
	body := "model:\n  provider: custom\n  base_url: " + a.URL + "/v1\n  context_length: 65536\ncustom_providers:\n  - name: Old Alias\n    base_url: " + b.URL + "/v1\nproviders:\n  local-a:\n    base_url: " + a.URL + "/v1\n    api_mode: chat_completions\n    context_length: 65536\n  local-b:\n    base_url: " + b.URL + "/v1\n    api_mode: chat_completions\n    context_length: 65536\n"
	mustWrite(t, p, body)
	list, err := probeHermesModelsAt(context.Background(), p, newHermesProbeClient())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(modelIDs(list), []string{"same-model", "custom:local-b/same-model"}) {
		t.Fatalf("aliases not deduplicated: %v", modelIDs(list))
	}
	mustWrite(t, p, body+"  local-policy:\n    base_url: "+b.URL+"/v1\n    extra_body: {seed: 1}\n")
	list, err = probeHermesModelsAt(context.Background(), p, newHermesProbeClient())
	if err != nil || !slices.Contains(modelIDs(list), "custom:local-policy/same-model") {
		t.Fatal("distinct endpoint policy collapsed")
	}
}
func TestHermesAmbiguousOrDynamicRoutingConfigRefused(t *testing.T) {
	for _, fields := range []string{"    base_url: http://127.0.0.1:8000/${TARGET}\n", "    base_url: http://user:pass@127.0.0.1:8000/v1\n", "    base_url: http://127.0.0.1:8000/v1\n    enabled: false\n    enabled: true\n", "    base_url: http://127.0.0.1:8000/v1\n    name: other-provider\n"} {
		p := filepath.Join(t.TempDir(), "config.yaml")
		mustWrite(t, p, "model:\n  provider: custom\nproviders:\n  local-b:\n"+fields)
		if _, err := readHermesRouteConfig(p); err == nil {
			t.Fatal("ambiguous/dynamic route accepted")
		}
	}
}

func TestHermesBudgetIncludesAuxiliaryAndDelegationRoutes(t *testing.T) {
	for _, tc := range []struct {
		body  string
		local bool
	}{
		{"delegation: {reasoning_effort: low, compression_threshold_tokens: 32000}\n", true},
		{"auxiliary: {compression: {provider: auto, model: '', base_url: '', api_key: '', extra_body: {}}}\n", true},
		{"delegation: {provider: openrouter, model: paid}\n", false},
		{"auxiliary: {compression: {base_url: https://example.invalid/v1}}\n", false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			h := t.TempDir()
			t.Setenv("HOME", h)
			mustWrite(t, filepath.Join(h, ".hermes", "config.yaml"), "model:\n  provider: custom\n  base_url: http://127.0.0.1:8000/v1\n"+tc.body)
			if hermesLocal() != tc.local {
				t.Fatal("auxiliary routing budget classification incorrect")
			}
		})
	}
	for _, source := range []string{"auth-store", "provider-env"} {
		t.Run(source, func(t *testing.T) {
			h := t.TempDir()
			t.Setenv("HOME", h)
			mustWrite(t, filepath.Join(h, ".hermes", "config.yaml"), "model:\n  provider: custom\n  base_url: http://127.0.0.1:8000/v1\n")
			if source == "auth-store" {
				mustWrite(t, filepath.Join(h, ".hermes", "auth.json"), `{"providers":{"synthetic":{"token":"synthetic"}}}`)
			} else {
				t.Setenv("SYNTHETIC_API_KEY", "synthetic")
			}
			if hermesLocal() {
				t.Fatal("unproven native credential scope classified local")
			}
		})
	}
}

func TestHermesCatalogCannotPublishEndpointAsModel(t *testing.T) {
	list, err := parseOpenAIModels([]byte(`{"data":[{"id":"http://example.invalid/v1"},{"id":"custom:foreign/model"},{"id":"qwen:latest"}]}`), "")
	if err != nil || !slices.Equal(modelIDs(list), []string{"qwen:latest"}) {
		t.Fatal("endpoint/routing data escaped as model IDs")
	}
}
func TestHermesNamedTerminalRecoveryRefuses(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	sender := newRecordingSender(4)
	m := &Manager{client: sender}
	m.recoverSession(SessionRecord{SessionID: "terminal-owned", Engine: "hermes", Model: "custom:local-b/model", ProjectPath: t.TempDir()})
	select {
	case msg := <-sender.ch:
		state, ok := msg.(protocol.SessionStateChanged)
		if !ok || state.Status != "error" || state.Message == nil || !strings.Contains(*state.Message, "Use agent mode") {
			t.Fatal("terminal route not refused clearly")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("terminal recovery refusal timeout")
	}
}

func TestHermesEmptyAuthMetadataIsNotAuthority(t *testing.T) {
	for _, tc := range []struct {
		body      string
		authority bool
	}{
		{`{"version":1,"providers":{},"active_provider":null,"updated_at":"fixture","credential_pool":{}}`, false},
		{`{"version":1,"providers":{},"active_provider":"paid","credential_pool":{}}`, true},
		{`{"providers":{},"credential_pool":{"paid":[{"api_key":"synthetic"}]}}`, true},
		{`{"providers":{},"credential_pool":{"paid":[]}}`, false},
		{`{"unknown_authority":{}}`, true},
	} {
		p := filepath.Join(t.TempDir(), "auth.json")
		mustWrite(t, p, tc.body)
		if hermesAuthStoreHasAuthority(p) != tc.authority {
			t.Fatal("auth metadata authority classification incorrect")
		}
	}
}

func TestHermesRoutingFingerprintIgnoresCatalogCacheOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	body := "model:\n  provider: custom\n  base_url: http://127.0.0.1:8000/v1\nproviders:\n  local-b:\n    base_url: http://127.0.0.1:8001/v1\n    context_length: 65536\n"
	mustWrite(t, p, body)
	before, err := readHermesRouteConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, p, body+"    models_discovered: true\n    models: {same-model: {context_length: 65536}}\n")
	after, err := readHermesRouteConfig(p)
	if err != nil || before.Providers[0].digest != after.Providers[0].digest {
		t.Fatal("catalog cache changes invalidate native recovery")
	}
	mustWrite(t, p, body+"    extra_body: {seed: 1}\n")
	after, err = readHermesRouteConfig(p)
	if err != nil || before.Providers[0].digest == after.Providers[0].digest {
		t.Fatal("request policy change was omitted from binding")
	}
}

func TestHermesFingerprintIncludesInheritedEntryPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"same-model"}]}`) }))
	defer srv.Close()
	h := t.TempDir()
	p := filepath.Join(h, ".hermes", "config.yaml")
	body := "model:\n  provider: custom\n  base_url: " + srv.URL + "/v1\n  context_length: 65536\nproviders:\n  local-b:\n    base_url: " + srv.URL + "/v1\n"
	mustWrite(t, p, body)
	d := newHermesDriver(AgentDriverOpts{})
	d.env = []string{"HOME=" + h}
	before, err := d.resolveModel(context.Background(), "custom:local-b/same-model")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, p, strings.Replace(body, "65536", "131072", 1))
	after, err := d.resolveModel(context.Background(), "custom:local-b/same-model")
	if err != nil || before.fingerprint == after.fingerprint {
		t.Fatal("inherited context change did not change binding")
	}
	r := newHermesRun(context.Background(), d, nil)
	r.binding = before
	if r.validRouteConfig() {
		t.Fatal("changed inherited policy accepted for saved route")
	}
}
func TestHermesRawDefaultCannotAdoptDifferentNamedPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"same-model"}]}`) }))
	defer srv.Close()
	h := t.TempDir()
	p := filepath.Join(h, ".hermes", "config.yaml")
	mustWrite(t, p, "model:\n  provider: custom\n  base_url: "+srv.URL+"/v1\n  context_length: 65536\nproviders:\n  local-a:\n    base_url: "+srv.URL+"/v1\n    context_length: 131072\n")
	d := newHermesDriver(AgentDriverOpts{})
	d.env = []string{"HOME=" + h}
	if _, err := d.resolveModel(context.Background(), "same-model"); err == nil {
		t.Fatal("raw default silently adopted another policy")
	}
	list, err := probeHermesModelsAt(context.Background(), p, newHermesProbeClient())
	if err != nil || slices.Contains(modelIDs(list), "same-model") || !slices.Contains(modelIDs(list), "custom:local-a/same-model") {
		t.Fatal("catalog and raw binding policy disagree")
	}
}

func TestHermesActiveProviderSwitchCannotMisattributeSameModelID(t *testing.T) {
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Emitter: em, Model: "custom:local-a/same-model"})
	r := newHermesRun(context.Background(), d, nil)
	r.binding = hermesModelRoute{Selector: "custom:local-a/same-model", Model: "same-model", Provider: "custom:local-a"}
	r.info([]byte(`{"model":"same-model","provider":"custom:local-a","usage":{"input":10,"output":2,"model":"same-model"}}`))
	r.begin()
	d.model = "custom:local-b/same-model"
	r.binding = hermesModelRoute{Selector: d.model, Model: "same-model", Provider: "custom:local-b"}
	r.info([]byte(`{"model":"same-model","provider":"custom:local-b"}`))
	r.reportUsage([]byte(`{"input":30,"output":10,"model":"same-model"}`))
	r.finish("end_turn", "")
	if !r.ambiguous {
		t.Fatal("active same-ID provider switch attributed mixed usage to prior provider")
	}
	if countKind(em, "turn_done") != 1 {
		t.Fatal("missing or duplicate mixed-provider completion")
	}
	for _, event := range em.snapshot() {
		if event.Kind == "turn_done" {
			turn := event.Payload.(agent.TurnDonePayload)
			if turn.Model != "" || turn.Usage.InputTokens != 20 {
				t.Fatal("mixed provider usage attribution is not explicitly unknown")
			}
		}
	}
}

func TestHermesBoundRecoveryVersionRefusesOlderAdapters(t *testing.T) {
	for _, version := range []int{1, 2} {
		if !(EngineSessionState{Version: version, Engine: "hermes", ID: "native-owned"}).valid("hermes") {
			t.Fatal("compatible private recovery version refused")
		}
	}
	if (EngineSessionState{Version: 3, Engine: "hermes", ID: "native-owned"}).valid("hermes") {
		t.Fatal("future private recovery version accepted")
	}
	d := newHermesDriver(AgentDriverOpts{Model: "custom:local-b/model"})
	if d.SetEngineSession(&EngineSessionState{Version: 2, Engine: "hermes", ID: "native-owned"}, nil) == nil {
		t.Fatal("unconfirmed version2 identity was resumable without binding")
	}
	var stored EngineSessionState
	if d.SetEngineSession(nil, func(s EngineSessionState) { stored = s }) != nil {
		t.Fatal("recovery callback")
	}
	d.onSessionID("native-owned")
	if stored.Version != 2 || !stored.valid("hermes") {
		t.Fatal("qualified lazy identity was writable as legacy version1")
	}
	d.model = "raw-model"
	d.routeFingerprint = strings.Repeat("a", 64)
	d.onSessionID("native-owned")
	if stored.Version != 2 {
		t.Fatal("bound raw identity lacks downgrade protection")
	}
	d.routeFingerprint = ""
	d.routeBound = true
	d.onSessionID("native-owned")
	if stored.Version != 2 {
		t.Fatal("raw bound lazy identity lacks downgrade protection")
	}
	d.routeBound = false
	d.onSessionID("native-owned")
	if stored.Version != 1 {
		t.Fatal("unbound raw legacy identity changed format")
	}
}

func TestHermesLazyIdentityPersistsRecoverableIntendedBinding(t *testing.T) {
	root := fakeHermesGateway(t)
	source := strings.Replace(fakeHermesGatewaySource, "'model':'fixture-model','reasoning_effort':'low','desktop_contract':8", "'model':'same-model','provider':'custom:local-b','lazy':True,'desktop_contract':8", 1)
	entry := "def main():\n    " + strings.ReplaceAll(strings.ReplaceAll(source, "global calls", "nonlocal calls"), "\n", "\n    ")
	if os.WriteFile(filepath.Join(root, "tui_gateway", "entry.py"), []byte(entry), 0600) != nil {
		t.Fatal("fixture")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"same-model"}]}`) }))
	defer srv.Close()
	mustWrite(t, filepath.Join(os.Getenv("HERMES_HOME"), "config.yaml"), "model:\n  provider: custom\n  base_url: "+srv.URL+"/v1\nproviders:\n  local-b:\n    base_url: "+srv.URL+"/v1\n")
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], Model: "custom:local-b/same-model", WorkDir: t.TempDir(), Emitter: &collectEmitter{}})
	expected, err := d.resolveModel(context.Background(), d.model)
	if err != nil {
		t.Fatal(err)
	}
	stored := make(chan EngineSessionState, 4)
	if d.SetEngineSession(nil, func(s EngineSessionState) { stored <- s }) != nil {
		t.Fatal("callback")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case state := <-stored:
		if state.Version != 2 || state.RouteFingerprint != expected.fingerprint {
			t.Fatal("lazy identity lost known intended route")
		}
		fresh := newHermesDriver(AgentDriverOpts{})
		if err := fresh.SetEngineSession(&state, nil); err != nil {
			t.Fatal("known intended lazy binding was not recoverable")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("lazy identity timeout")
	}
}
