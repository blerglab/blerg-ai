package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func rep(t time.Time, ms ...models.Model) models.Report {
	return models.Report{Models: ms, FetchedAt: t}
}

func mdl(id string, efforts ...string) models.Model {
	if efforts == nil {
		efforts = []string{}
	}
	return models.Model{ID: id, Name: id, Section: "main", Efforts: efforts}
}

func reportingDaemon(hub *Hub, id string, reports map[string]models.Report) *DaemonConn {
	dc := &DaemonConn{ID: id, Name: "host-" + id, send: make(chan []byte, 8)}
	dc.SetCheckedOutRepos([]string{"app"})
	dc.SetEngineModels(reports)
	hub.Register(dc)
	return dc
}

// A daemon's report is re-validated on arrival, whatever the daemon did.
func TestDaemonConnSanitizesReportedModels(t *testing.T) {
	dc := &DaemonConn{ID: "d1"}
	dc.SetEngineModels(map[string]models.Report{
		"codex": {Models: []models.Model{
			{ID: "--dangerously-bypass-approvals-and-sandbox", Efforts: []string{"high"}},
			{ID: "gpt-ok", Name: "GPT\u202eOK\x07", Efforts: []string{"ultra", "none", "turbo", "low"}, DefaultEffort: "turbo", EffortKind: "Reasoning!"},
			{ID: "gpt-ok"},
		}},
		"no-such-engine": {Models: []models.Model{mdl("x")}},
	})
	r, ok := dc.EngineModels("codex")
	if !ok || len(r.Models) != 1 {
		t.Fatalf("codex = %+v %v", r, ok)
	}
	m := r.Models[0]
	if m.ID != "gpt-ok" || m.Name != "GPTOK" || !slices.Equal(m.Efforts, []string{"none", "low", "ultra"}) || m.DefaultEffort != "" || m.EffortKind != "" || m.Section != "overflow" {
		t.Errorf("sanitized = %+v", m)
	}
	if r.FetchedAt.IsZero() {
		t.Error("a missing fetched_at should become the receipt time")
	}
	if _, ok := dc.EngineModels("no-such-engine"); ok {
		t.Error("an engine without rules was kept")
	}
	// An empty report is "no list".
	dc.SetEngineModels(map[string]models.Report{"codex": {Models: []models.Model{}}})
	if _, ok := dc.EngineModels("codex"); ok {
		t.Error("an empty report should read as no list")
	}
}

func TestReportedModelListDaemonSelection(t *testing.T) {
	hub := NewHub()
	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	recent := old.Add(time.Hour)
	reportingDaemon(hub, "d2", map[string]models.Report{
		"codex": rep(recent, mdl("gpt-b", "high"), mdl("gpt-shared", "low")),
	})
	reportingDaemon(hub, "d1", map[string]models.Report{
		"codex":  rep(old, mdl("gpt-a", "low"), mdl("gpt-shared", "max")),
		"hermes": rep(old, mdl("qwen3-30b", "none", "ultra")),
	})
	reportingDaemon(hub, "d3", map[string]models.Report{"codex": rep(recent)}) // probed, nothing

	// Named daemon: its own list, never another's.
	l := reportedModelList(hub, "codex", "d2")
	if l.Source != models.SourceDaemon || l.DaemonID != "d2" || !slices.Equal(ids(l.Models), []string{"gpt-b", "gpt-shared"}) || !l.FetchedAt.Equal(recent) {
		t.Errorf("d2 = %+v", l)
	}
	for _, d := range []string{"d3", "gone"} {
		if l := reportedModelList(hub, "codex", d); l.Source != models.SourceNone || len(l.Models) != 0 || l.Models == nil {
			t.Errorf("%s = %+v", d, l)
		}
	}
	// One reporter: that daemon's list, named.
	if l := reportedModelList(hub, "hermes", ""); l.DaemonID != "d1" || len(l.Models) != 1 {
		t.Errorf("hermes = %+v", l)
	}
	// Several: the union in daemon-id order, the lowest id winning a model
	// both list, no daemon named, the oldest fetch.
	l = reportedModelList(hub, "codex", "")
	if l.DaemonID != "" || !slices.Equal(ids(l.Models), []string{"gpt-a", "gpt-shared", "gpt-b"}) || !l.FetchedAt.Equal(old) {
		t.Errorf("union = %+v", l)
	}
	if l.Models[1].Efforts[0] != "max" {
		t.Errorf("d1's gpt-shared should win: %+v", l.Models[1])
	}
	// Nobody reports openclaw.
	if l := reportedModelList(hub, "openclaw", ""); l.Source != models.SourceNone {
		t.Errorf("openclaw = %+v", l)
	}
}

func ids(ms []models.Model) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

// GET /api/models/{engine}?daemon_id= serves a daemon's report through the
// real registry, and Claude keeps its own catalog source.
func TestModelsEndpointServesDaemonReports(t *testing.T) {
	hub := NewHub()
	fetched := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	reportingDaemon(hub, "d1", map[string]models.Report{
		"hermes": rep(fetched, models.Model{ID: "qwen3-30b", Name: "qwen3-30b", Section: "main",
			Efforts: models.EffortsFor("hermes"), EffortKind: "reasoning"}),
	})
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	api.SetRunnerKey("runner-key")
	api.SetModelSources(NewModelSources(nil, hub))
	get := func(path string) (int, string) {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer runner-key")
		rec := httptest.NewRecorder()
		mux := http.NewServeMux()
		RegisterRunnerContractRoutes(mux, api)
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body := get("/api/models/hermes?daemon_id=d1")
	var l models.List
	_ = json.Unmarshal([]byte(body), &l)
	if code != http.StatusOK || l.Source != "daemon" || l.DaemonID != "d1" || l.FetchedAt == nil || !l.FetchedAt.Equal(fetched) ||
		len(l.Models) != 1 || l.Models[0].DefaultEffort != "" || len(l.Models[0].Efforts) != 8 {
		t.Errorf("hermes d1 = %d %s", code, body)
	}
	if !strings.Contains(body, `"daemon_id":"d1"`) || strings.Contains(body, "default_effort") {
		t.Errorf("wire shape: %s", body)
	}
	// The engine's allowlist rides along (for a model typed by hand), from
	// the rules table whatever the source — [] for an engine with none.
	if !strings.Contains(body, `"engine_efforts":["none","minimal","low","medium","high","xhigh","max","ultra"]`) {
		t.Errorf("hermes engine_efforts: %s", body)
	}
	if _, body := get("/api/models/openclaw"); !strings.Contains(body, `"engine_efforts":[]`) {
		t.Errorf("openclaw engine_efforts: %s", body)
	}
	if code, body := get("/api/models/hermes?daemon_id=d9"); code != http.StatusOK || !strings.Contains(body, `"source":"none"`) || !strings.Contains(body, `"models":[]`) {
		t.Errorf("unknown daemon = %d %s", code, body)
	}
	if _, body := get("/api/models/codex"); !strings.Contains(body, `"source":"none"`) {
		t.Errorf("codex unreported = %s", body)
	}
	if _, body := get("/api/models/claude?daemon_id=d1"); !strings.Contains(body, `"source":"builtin"`) {
		t.Errorf("claude = %s", body)
	}
}

// A model a daemon reported is checked against its own levels for a session
// on that daemon; with no daemon known (the v1 start picks one later, or a
// cluster session) only the engine rules apply.
func TestEffortCheckedAgainstTheDaemonsReportedList(t *testing.T) {
	hub := NewHub()
	dc := reportingDaemon(hub, "d1", map[string]models.Report{
		"codex": rep(time.Now(), mdl("gpt-5.5", "low", "medium", "high", "xhigh")),
	})
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	api.SetModelSources(NewModelSources(nil, hub))
	tok := enableBrowserAuth(t, api)("session.start")
	post := func(body map[string]any) int {
		body["daemon_id"], body["repo"], body["runtime"], body["engine"] = "d1", "app", "docker", "codex"
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		api.HandlePostSessions(rec, req)
		select {
		case <-dc.send:
		default:
		}
		return rec.Code
	}
	for _, tc := range []struct {
		model, effort string
		want          int
	}{
		{"gpt-5.5", "ultra", http.StatusUnprocessableEntity}, // allowlisted, but not this model's
		{"gpt-5.5", "xhigh", http.StatusAccepted},
		{"gpt-future", "ultra", http.StatusAccepted}, // not in the list: rules only
		{"gpt-5.5", "", http.StatusAccepted},
	} {
		if got := post(map[string]any{"model": tc.model, "effort": tc.effort}); got != tc.want {
			t.Errorf("%s/%s = %d, want %d", tc.model, tc.effort, got, tc.want)
		}
	}
	// No daemon named: rules only.
	if msg := api.modelEffortProblem(t.Context(), "codex", "gpt-5.5", "ultra", ""); msg != "" {
		t.Errorf("without a daemon: %q", msg)
	}
	if msg := api.modelEffortProblem(t.Context(), "codex", "gpt-5.5", "ultra", "d1"); !strings.Contains(msg, "must be one of low, medium, high, xhigh") {
		t.Errorf("on d1: %q", msg)
	}
}

// The daemon socket: a hello's lists are stored, a heartbeat without them
// leaves them alone, and one with them replaces them.
func TestDaemonSocketCarriesEngineModels(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(hub.ServeDaemon("tok-1234567890", nil))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send := func(v any) {
		t.Helper()
		if err := conn.WriteJSON(v); err != nil {
			t.Fatal(err)
		}
	}
	send(protocol.DaemonHello{Type: "daemon_hello", Name: "box", Token: "tok-1234567890", ProtocolVersion: "1",
		EngineModels: map[string]models.Report{"codex": rep(time.Now(), mdl("gpt-a", "low"))}})
	waitCodex := func(want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			var got string
			if ds := hub.GetAllDaemons(); len(ds) == 1 {
				if r, ok := ds[0].EngineModels("codex"); ok {
					got = r.Models[0].ID
				}
			}
			if got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("codex list = %q, want %q", got, want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitCodex("gpt-a")
	send(protocol.DaemonHeartbeat{Type: "daemon_heartbeat", ActiveSessions: []string{}})
	send(protocol.DaemonHeartbeat{Type: "daemon_heartbeat", ActiveSessions: []string{},
		EngineModels: map[string]models.Report{"codex": rep(time.Now(), mdl("gpt-b"))}})
	waitCodex("gpt-b")
	send(protocol.DaemonHeartbeat{Type: "daemon_heartbeat", ActiveSessions: []string{}})
	time.Sleep(50 * time.Millisecond)
	waitCodex("gpt-b")
}
