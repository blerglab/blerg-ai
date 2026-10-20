package server

// A child's --model is resolved against the engine's catalog on the child's host (0.4.3): an exact
// routing id, a display name, or a unique suffix or substring of an id; anything else is refused
// with the available models, instead of a pod that fails on a made-up route.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/models"
)

func fixtureCatalog() []models.Model {
	return []models.Model{
		{ID: "nvidia/nemotron-3-super", Name: "Nemotron Super", Efforts: []string{}},
		{ID: "nvidia/nemotron-3-super-vl", Name: "Nemotron Super VL", Efforts: []string{}},
		{ID: "qwen/qwen3-coder", Name: "Qwen3 Coder", Efforts: []string{}},
	}
}

func TestResolveModelName(t *testing.T) {
	for name, c := range map[string]struct {
		engine, in, want, problem string
	}{
		"exact id":                {"hermes", "nvidia/nemotron-3-super-vl", "nvidia/nemotron-3-super-vl", ""},
		"display name":            {"hermes", "Nemotron Super VL", "nvidia/nemotron-3-super-vl", ""},
		"display name, any case":  {"hermes", "nemotron super", "nvidia/nemotron-3-super", ""},
		"id, any case":            {"hermes", "QWEN/Qwen3-Coder", "qwen/qwen3-coder", ""},
		"unique suffix":           {"hermes", "super-vl", "nvidia/nemotron-3-super-vl", ""},
		"unique substring":        {"hermes", "qwen3", "qwen/qwen3-coder", ""},
		"ambiguous substring":     {"hermes", "nemotron", "", "ambiguous model nemotron for Hermes; available: Nemotron Super (nvidia/nemotron-3-super), Nemotron Super VL (nvidia/nemotron-3-super-vl), Qwen3 Coder (qwen/qwen3-coder)"},
		"made-up name":            {"hermes", "nvidia/nemotron-3.5-super-vl", "", "unknown model nvidia/nemotron-3.5-super-vl for Hermes; available: Nemotron Super (nvidia/nemotron-3-super), Nemotron Super VL (nvidia/nemotron-3-super-vl), Qwen3 Coder (qwen/qwen3-coder)"},
		"empty keeps the default": {"hermes", "", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			got, problem := resolveModelName(c.engine, c.in, fixtureCatalog())
			if got != c.want || problem != c.problem {
				t.Fatalf("resolve(%q) = %q %q, want %q %q", c.in, got, problem, c.want, c.problem)
			}
		})
	}
	// Claude Code resolves its own family aliases ("sonnet", "opus[1m]"): they pass unchanged, as
	// does a catalog id with the 1M-context suffix.
	claude := []models.Model{{ID: "claude-sonnet-5", Name: "Sonnet 5"}, {ID: "claude-sonnet-4-6", Name: "Sonnet 4.6"}}
	for in, want := range map[string]string{"sonnet": "sonnet", "opus[1m]": "opus[1m]", "claude-sonnet-5[1m]": "claude-sonnet-5[1m]", "Sonnet 5": "claude-sonnet-5"} {
		if got, problem := resolveModelName("claude", in, claude); got != want || problem != "" {
			t.Errorf("claude %q = %q %q, want %q", in, got, problem, want)
		}
	}
	// No catalog known: the name goes through as given (the engine rules still apply).
	if got, problem := resolveModelName("hermes", "anything", nil); got != "anything" || problem != "" {
		t.Errorf("no catalog = %q %q", got, problem)
	}
}

func (fx *delegFx) reportModels(engine string, list []models.Model) {
	fx.dc.SetEngineModels(map[string]models.Report{engine: {Models: list, FetchedAt: time.Now()}})
}

func TestDesktopChildStartResolvesTheModel(t *testing.T) {
	fx := newDelegFx(t)
	fx.reportModels("codex", []models.Model{{ID: "gpt-fixture-5", Name: "GPT Fixture 5", Efforts: []string{"low", "high"}}, {ID: "gpt-fixture-5-mini", Name: "GPT Fixture 5 Mini", Efforts: []string{"low"}}})
	code, out, _ := fx.postChild(fx.parent, fx.parentTok, map[string]any{"engine": "codex", "repo": "app", "prompt": "x", "model": "gpt-fixture-6"}, "k-unknown")
	if code != http.StatusUnprocessableEntity || asString(out["error"]) != "unknown model gpt-fixture-6 for Codex; available: GPT Fixture 5 (gpt-fixture-5), GPT Fixture 5 Mini (gpt-fixture-5-mini)" {
		t.Fatalf("unknown model = %d %v", code, out)
	}
	fx.storedNothing(fx.parent)
	id := fx.ask(fx.parent, fx.parentTok, map[string]any{"engine": "codex", "repo": "app", "prompt": "x", "model": "GPT Fixture 5 Mini"})
	var stored string
	if err := fx.pool.QueryRow(context.Background(), `SELECT request->>'model' FROM delegation_requests WHERE id = $1::uuid`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "gpt-fixture-5-mini" {
		t.Fatalf("stored model = %q, want the routing id", stored)
	}
}

func TestClusterChildStartResolvesTheModel(t *testing.T) {
	cx := newClusterDelegFx(t)
	cx.api.clusterModels.probe = func(_ context.Context, engine, _ string, _ coreProof) (models.List, error) {
		now := time.Now()
		return models.List{Models: []models.Model{{ID: "gpt-cluster-5", Name: "GPT Cluster 5", Efforts: []string{"low", "high"}}}, Source: models.SourceLive, FetchedAt: &now}, nil
	}
	cx.clusterRule("codex")
	code, out, _ := cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"engine": "codex", "repo": "app", "prompt": "x", "model": "gpt-cluster-9"}, newUUID())
	if code != http.StatusUnprocessableEntity || !strings.HasPrefix(asString(out["error"]), "unknown model gpt-cluster-9 for Codex; available: GPT Cluster 5 (gpt-cluster-5)") {
		t.Fatalf("unknown model = %d %v", code, out)
	}
	code, out, _ = cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"engine": "codex", "repo": "app", "prompt": "x", "model": "GPT Cluster 5"}, newUUID())
	if code != http.StatusCreated {
		t.Fatalf("display name = %d %v", code, out)
	}
	child, _ := out["child"].(map[string]any)
	if child["model"] != "gpt-cluster-5" || derefOrEmpty(cx.row(asString(child["id"])).Model) != "gpt-cluster-5" {
		t.Fatalf("child model = %v / %q, want the routing id", child["model"], derefOrEmpty(cx.row(asString(child["id"])).Model))
	}
}

func TestChildModelChangeResolvesTheModel(t *testing.T) {
	fx := newDelegFx(t)
	fx.reportModels("codex", []models.Model{{ID: "gpt-fixture-5", Name: "GPT Fixture 5", Efforts: []string{"low", "high"}}})
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if err := db.SetSessionEngine(context.Background(), fx.pool, child, "codex"); err != nil {
		t.Fatal(err)
	}
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	code, out, _ := fx.do("POST", childRoute(fx.parent, child, "/model"), fx.parentTok, map[string]any{"model": "gpt-fixture-4"}, nil)
	if code != http.StatusUnprocessableEntity || !strings.HasPrefix(asString(out["error"]), "unknown model gpt-fixture-4 for Codex; available: GPT Fixture 5 (gpt-fixture-5)") {
		t.Fatalf("unknown = %d %v", code, out)
	}
	code, out, _ = fx.do("POST", childRoute(fx.parent, child, "/model"), fx.parentTok, map[string]any{"model": "GPT Fixture 5"}, nil)
	if code != http.StatusAccepted || out["model"] != "gpt-fixture-5" {
		t.Fatalf("display name = %d %v", code, out)
	}
}

func TestChildModelsListsTheCatalogForTheChildHost(t *testing.T) {
	fx := newDelegFx(t)
	fx.reportModels("hermes", fixtureCatalog())
	code, out, _ := fx.do("GET", "/api/sessions/"+fx.parent+"/children/models?engine=hermes", fx.parentTok, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("models = %d %v", code, out)
	}
	raw, _ := json.Marshal(out["engines"])
	var engines []struct {
		Engine string `json:"engine"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &engines); err != nil || len(engines) != 1 || engines[0].Engine != "hermes" || len(engines[0].Models) != 3 ||
		engines[0].Models[1].ID != "nvidia/nemotron-3-super-vl" || engines[0].Models[1].Name != "Nemotron Super VL" {
		t.Fatalf("engines = %s %v", raw, err)
	}
	if code, _, _ := fx.do("GET", "/api/sessions/"+fx.parent+"/children/models", "", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("no credential = %d", code)
	}
}
