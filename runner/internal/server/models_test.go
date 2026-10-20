package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func getModels(t *testing.T, api *API, engine, token string) (*httptest.ResponseRecorder, models.List) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/models/"+engine, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	// Through the contract mux, so the route registration is exercised too.
	mux := http.NewServeMux()
	RegisterRunnerContractRoutes(mux, api)
	mux.ServeHTTP(w, req)
	var out models.List
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %q: %v", w.Body.String(), err)
		}
	}
	return w, out
}

func TestClaudeModelsRequiresAuth(t *testing.T) {
	api, _, coreSrv := newCredentialsAPI(t, nil)
	defer coreSrv.Close()
	api.SetRunnerKey("runner-key")
	for _, tok := range []string{"", "wrong", "daemon-tok"} {
		if w, _ := getModels(t, api, "claude", tok); w.Code != http.StatusUnauthorized {
			t.Errorf("token %q: got %d, want 401", tok, w.Code)
		}
	}
}

func TestClaudeModelsServesBuiltinToBrowserAndRunnerKey(t *testing.T) {
	api, browserTok, coreSrv := newCredentialsAPI(t, nil)
	defer coreSrv.Close()
	api.SetRunnerKey("runner-key")
	for _, tok := range []string{browserTok, "runner-key"} {
		w, snap := getModels(t, api, "claude", tok)
		if w.Code != http.StatusOK {
			t.Fatalf("got %d: %s", w.Code, w.Body.String())
		}
		if snap.Source != models.SourceBuiltin || len(snap.Models) != len(models.Builtin()) || snap.FetchedAt != nil {
			t.Fatalf("snapshot = %+v", snap)
		}
		if snap.Models[0].Section != "main" {
			t.Errorf("main models first, got %+v", snap.Models[0])
		}
	}
}

// An engine with no registered source is "no picker": 200 with an empty list
// and source "none", never a 404 — so adding an engine needs no new route.
func TestModelsForUnsourcedEngineIsEmpty(t *testing.T) {
	api, tok, coreSrv := newCredentialsAPI(t, nil)
	defer coreSrv.Close()
	for _, engine := range []string{"codex", "hermes", "no-such-engine"} {
		w, list := getModels(t, api, engine, tok)
		if w.Code != http.StatusOK || list.Source != models.SourceNone || list.Models == nil || len(list.Models) != 0 {
			t.Errorf("%s: %d %+v", engine, w.Code, list)
		}
	}
}

// A newly registered source is served under its engine, and gets the daemon
// the caller named — for lists a daemon reports.
func TestModelsServesARegisteredSource(t *testing.T) {
	api, tok, coreSrv := newCredentialsAPI(t, nil)
	defer coreSrv.Close()
	var gotDaemon string
	reg := models.NewRegistry()
	reg.Register("codex", models.SourceFunc(func(_ context.Context, daemonID string) (models.List, error) {
		gotDaemon = daemonID
		return models.List{Source: models.SourceLive, Models: []models.Model{
			{ID: "gpt-5-codex", Name: "GPT-5 Codex", Section: "main", Efforts: []string{"low", "ultra"}, DefaultEffort: "low", EffortKind: "reasoning"},
		}}, nil
	}))
	api.SetModelSources(reg)
	req := httptest.NewRequest("GET", "/api/models/codex?daemon_id=d7", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	RegisterRunnerContractRoutes(mux, api)
	mux.ServeHTTP(w, req)
	var list models.List
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != http.StatusOK || len(list.Models) != 1 || list.Models[0].EffortKind != "reasoning" || gotDaemon != "d7" {
		t.Fatalf("%d %+v daemon=%q", w.Code, list, gotDaemon)
	}
	// Claude is not registered in this registry: empty, not the built-in.
	if _, l := getModels(t, api, "claude", tok); l.Source != models.SourceNone {
		t.Errorf("claude with no source = %+v", l)
	}
}

func TestSanitizeSetSessionModel(t *testing.T) {
	ok := []protocol.SetSessionModel{
		{SessionID: "s", Model: "claude-opus-5-5"},
		{SessionID: "s", Effort: "xhigh"},
		{SessionID: "s", Model: "sonnet[1m]", Effort: "low"},
		// In some engine's allowlist (codex, hermes); the daemon applies the
		// session engine's own rules on top.
		{SessionID: "s", Effort: "ultra"},
		{SessionID: "s", Model: "qwen3-30b", Effort: "none"},
	}
	for _, m := range ok {
		fwd, good := sanitizeSetSessionModel(m)
		if !good || fwd.Model != m.Model || fwd.Effort != m.Effort || fwd.Type != "set_session_model" {
			t.Errorf("%+v: got %+v %v", m, fwd, good)
		}
	}
	bad := []protocol.SetSessionModel{
		{SessionID: "s"},
		{SessionID: "s", Model: "--dangerously-skip-permissions"},
		{SessionID: "s", Model: "opus; id"},
		{SessionID: "s", Effort: "turbo"},
		{SessionID: "s", Model: "claude-opus-5", Effort: "$(id)"},
	}
	for _, m := range bad {
		if _, good := sanitizeSetSessionModel(m); good {
			t.Errorf("%+v should be refused", m)
		}
	}
}

func TestClaudeModelsServesLiveCatalog(t *testing.T) {
	cat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"surfaces":{"cc":{"model_selector_config":[{"id":"cc","models":[
			{"id":"claude-overflow-1","name":"Old","section":"overflow","runtime":{"effort_levels":["low","high"],"default_effort":"low"}},
			{"id":"claude-main-1","name":"New","section":"main","offered_on":["first_party"]}
		]}]}}}`))
	}))
	defer cat.Close()
	c := models.NewCatalog(cat.URL)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	api, tok, coreSrv := newCredentialsAPI(t, nil)
	defer coreSrv.Close()
	reg := models.NewRegistry()
	reg.Register("claude", c)
	api.SetModelSources(reg)
	w, snap := getModels(t, api, "claude", tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	if snap.Source != models.SourceLive || snap.FetchedAt == nil || len(snap.Models) != 2 ||
		snap.Models[0].ID != "claude-main-1" || snap.Models[1].DefaultEffort != "low" {
		t.Fatalf("snapshot = %+v", snap)
	}
	// Efforts always serialise as a list, never null.
	var raw struct {
		Models []map[string]any `json:"models"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	if _, ok := raw.Models[0]["efforts"].([]any); !ok {
		t.Errorf("efforts for a model with none must be [], got %v", raw.Models[0]["efforts"])
	}
}

func TestClusterCodexModelsDoesNotUseWorkstationCatalog(t *testing.T) {
	api, token, core := newCredentialsAPI(t, nil)
	defer core.Close()
	api.SetRunnerKey("runner-key")
	w, _ := getModels(t, api, "codex?runtime=cluster", token)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
	w, _ = getModels(t, api, "codex?runtime=cluster", "runner-key")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("shared key got %d, want 401", w.Code)
	}
}

func TestClusterCodexModelsServesAccountCatalogAndSafeFailure(t *testing.T) {
	for _, authFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(authFailure), func(t *testing.T) {
			api, token, core := newCredentialsAPI(t, nil)
			defer core.Close()
			f := &catalogFake{phase: "Succeeded", logs: clusterModelFixture}
			if authFailure {
				f.phase = "Failed"
				f.exit = 42
			}
			api.hub.SetJobManager(catalogManager(t, f))
			w, list := getModels(t, api, "codex?runtime=cluster", token)
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("private catalog response must not be shared-cached")
			}
			if w.Code != http.StatusOK || !list.Refreshing {
				t.Fatalf("first snapshot: %d %+v", w.Code, list)
			}
			deadline := time.Now().Add(15 * time.Second)
			for list.Refreshing && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
				w, list = getModels(t, api, "codex?runtime=cluster", token)
			}
			if authFailure {
				if w.Code != http.StatusOK || list.Error != ErrClusterCodexCatalogAuth.Error() || list.Refreshing {
					t.Fatalf("auth failure: %d %s", w.Code, w.Body.String())
				}
			} else if w.Code != http.StatusOK || list.Source != models.SourceLive || len(list.Models) != 2 || list.Models[1].DefaultEffort != "medium" {
				t.Fatalf("catalog: %d %+v", w.Code, list)
			}
		})
	}
}

func TestClusterCodexModelsReturnsSnapshotBeforeProbeCompletes(t *testing.T) {
	api, token, core := newCredentialsAPI(t, nil)
	defer core.Close()
	api.hub.SetJobManager(&JobManager{Image: "fixture-agent"})
	release, done := make(chan struct{}), make(chan struct{})
	api.clusterModels.probe = func(context.Context, string, string, coreProof) (models.List, error) {
		defer close(done)
		<-release
		return models.List{}, ErrClusterCodexCatalog
	}
	defer func() { close(release); <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "/api/models/codex?runtime=cluster", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	RegisterRunnerContractRoutes(mux, api)
	mux.ServeHTTP(w, req)
	var out struct {
		Refreshing bool `json:"refreshing"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusOK || !out.Refreshing {
		t.Fatalf("request waited for probe: status=%d refreshing=%v", w.Code, out.Refreshing)
	}
}

func TestClusterCodexModelsRestoredAtStartupWithoutProbe(t *testing.T) {
	pool := connectSrvTestSchemaPool(t)
	now := time.Now().UTC()
	stored := models.List{Models: []models.Model{{ID: "fixture-fast", Name: "Fast", Section: "main", Efforts: []string{"low"}, DefaultEffort: "low"}}, Source: models.SourceLive, FetchedAt: &now}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.StoreClusterModelCatalog(context.Background(), pool, "acct-1", "codex", "fixture-agent", raw, now); err != nil {
		t.Fatal(err)
	}
	api, token, core := newCredentialsAPI(t, nil)
	defer core.Close()
	api.dbPool = pool
	api.hub.SetJobManager(&JobManager{Image: "fixture-agent"})
	api.WarmClusterModelCache(context.Background())
	w, list := getModels(t, api, "codex?runtime=cluster", token)
	if w.Code != http.StatusOK || list.Refreshing || len(list.Models) != 1 || list.Models[0].ID != "fixture-fast" || list.Models[0].DefaultEffort != "low" {
		t.Fatalf("startup snapshot: %d %+v", w.Code, list)
	}
}

func TestClusterModelsWithoutNativeProviderUsesOnlyPublicCatalog(t *testing.T) {
	api, tok, coreSrv := newCredentialsAPI(t, nil)
	defer coreSrv.Close()
	reg := models.NewRegistry()
	reg.Register("claude", models.BuiltinClaudeSource)
	reg.Register("hermes", models.SourceFunc(func(_ context.Context, daemonID string) (models.List, error) {
		if daemonID != "" {
			t.Errorf("cluster lookup forwarded workstation %q", daemonID)
		}
		return models.List{Source: models.SourceDaemon, DaemonID: "workstation", Models: []models.Model{{ID: "local-only"}}}, nil
	}))
	api.SetModelSources(reg)
	for _, engine := range []string{"claude", "hermes"} {
		w, list := getModels(t, api, engine+"?runtime=cluster&daemon_id=workstation", tok)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", engine, w.Code, w.Body.String())
		}
		if engine == "claude" && (list.Source != models.SourceBuiltin || len(list.Models) == 0) {
			t.Fatalf("public catalog lost: %+v", list)
		}
		if engine == "hermes" && (list.Source != models.SourceNone || len(list.Models) != 0 || list.DaemonID != "") {
			t.Fatalf("workstation catalog leaked: %+v", list)
		}
	}
}
