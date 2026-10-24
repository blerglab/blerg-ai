package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCodexGrantTargetsRequireExplicitDaemonSupport(t *testing.T) {
	a := &API{hub: NewHub(), dbPool: &pgxpool.Pool{}}
	for _, modern := range []bool{false, true} {
		dc := &DaemonConn{}
		dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
		dc.SetMCPGateway(true)
		dc.SetRestrictTools(true)
		dc.SetCodexRestricted(modern)
		err := a.checkGrantTarget(grantTarget{Runtime: runnerRuntimeDocker, Engine: "codex", Kind: "agent", Daemon: dc})
		if !modern {
			if err == nil || !strings.Contains(err.Message, "codex_restricted") {
				t.Fatalf("legacy daemon did not get the explicit Codex capability refusal: %v", err)
			}
		} else if err == nil || err.Message != msgMCPNoGateway {
			t.Fatalf("modern daemon did not pass placement validation: %v", err)
		}
	}
	err := a.checkGrantTarget(grantTarget{Runtime: runnerRuntimeCluster, Engine: "codex", Kind: "agent"})
	if err == nil || err.Message != msgMCPNoGateway {
		t.Fatalf("cluster Codex placement was refused: %v", err)
	}
}

func TestCodexCronStartRules(t *testing.T) {
	req := RunnerStartRequest{CronID: "cron", Runtime: runnerRuntimeCluster, Engine: "codex", NoRepo: true, AutoStop: true, NoOperatorFallback: true}
	if problem := cronStartProblem(req); problem != "" {
		t.Fatalf("well-contained Codex cron refused: %s", problem)
	}
	req.Engine = "hermes"
	if cronStartProblem(req) == "" {
		t.Fatal("unsupported engine admitted to a restricted cron")
	}
}

func TestCodexPluginsRequireNativeCapability(t *testing.T) {
	if !pluginsWanted(SessionJobSpec{Engine: "codex", SpawningAccountID: "account"}) {
		t.Fatal("unrestricted Codex sessions must resolve their native plugin list")
	}
	if pluginsWanted(SessionJobSpec{Engine: "codex", SpawningAccountID: "account", RestrictTools: true}) {
		t.Fatal("restricted Codex session admitted plugins")
	}
	dc := &DaemonConn{}
	dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	dc.SetPlugins(true)
	if dc.CanPluginsFor("codex") || !dc.CanPluginsFor("claude") {
		t.Fatal("legacy plugin capability was widened beyond Claude")
	}
	dc.SetPluginEngines([]string{"codex"})
	if !dc.CanPluginsFor("codex") || dc.CanPluginsFor("hermes") {
		t.Fatal("explicit native plugin capability was not honored")
	}
}

func TestCodexPluginFetchNamesTheSelectedEngine(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request internalPluginsRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Engine != "codex" || request.SessionID != testSID {
			t.Error("plugin fetch did not preserve engine and liveness proof")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"plugins":[]}`))
	}))
	defer core.Close()
	if _, err := fetchCorePlugins(context.Background(), core.Client(), core.URL, "fixture-internal-key", "account", coreProof{SessionID: testSID}, "codex"); err != nil {
		t.Fatal("Codex plugin list could not be fetched")
	}
}

func TestCodexCronEnginePersistsAcrossCreateAndPatch(t *testing.T) {
	fx := newCronsFx(t)
	c, row := fx.create(func(body map[string]any) {
		body["engine"], body["model"], body["effort"] = "codex", "gpt-5-codex", "ultra"
	})
	if row.Engine != "codex" {
		t.Fatal("Codex cron creation did not persist its engine")
	}
	response := fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(), `{"engine":"claude"}`)
	var updated struct{ Engine string }
	_ = json.Unmarshal(response.Body.Bytes(), &updated)
	if response.Code != http.StatusOK || updated.Engine != "claude" {
		t.Fatalf("engine patch failed: status %d", response.Code)
	}
	stored, err := db.GetCron(context.Background(), fx.pool, c.ID)
	if err != nil || stored.Model != nil || stored.Effort != nil {
		t.Fatal("engine switch retained the prior engine's model or effort")
	}
	response = fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(), `{"effort":"ultra"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatal("invalid effort was saved for the selected engine")
	}
}

func TestCodexCronSelectsOnlyCapableDaemons(t *testing.T) {
	pool := connectSrvTestDB(t)
	h := NewHub()
	api := NewAPI(h, pool, "", nil, "")
	old := &DaemonConn{ID: newUUID(), Name: "legacy"}
	old.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	modern := &DaemonConn{ID: newUUID(), Name: "modern"}
	modern.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	for _, dc := range []*DaemonConn{old, modern} {
		dc.SetSandboxAvailable(true)
		dc.SetRestrictTools(true)
		fixtureOwnDesktop(t, pool, dc, "user-1")
		h.Register(dc)
	}
	modern.SetCodexRestricted(true)
	s := &CronService{api: api}
	chosen, err := s.pickOwnedDaemon(t.Context(), "user-1", nil, false, "codex")
	if err != nil || chosen != modern {
		t.Fatalf("Codex selected incapable daemon: %v", err)
	}
	if _, err := s.pickOwnedDaemon(t.Context(), "user-1", &old.ID, false, "codex"); err == nil {
		t.Fatal("explicit legacy daemon pin bypassed the Codex capability")
	}
	if checkRestrictTarget(runnerRuntimeDocker, old, "codex") == nil || checkRestrictTarget(runnerRuntimeDocker, modern, "codex") != nil {
		t.Fatal("restricted launch did not enforce engine-specific support")
	}
}

func TestCodexCronStartsWithPersonalCredentialAndRestriction(t *testing.T) {
	cf := newCronFx(t)
	cf.k8s.credentialFetchResponses[mcpAcct+":codex"] = []byte(`{"tokens":{"access_token":"invented-codex-fixture"}}`)
	c := cf.newCron(func(c *db.Cron) { c.Engine = "codex" })
	id, err := cf.svc.Start(context.Background(), c, cf.run(c))
	if err != nil {
		t.Fatalf("Codex cron start: %v", err)
	}
	row := cf.session(id)
	if row.Engine == nil || *row.Engine != "codex" || !row.Private || !row.AutoStop || !db.GetSessionRestrictTools(context.Background(), cf.pool, id) {
		t.Fatal("Codex cron lost its engine or containment settings")
	}
}
