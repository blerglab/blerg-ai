package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func budgetEngineOf(t *testing.T, body map[string]any, engine string) map[string]any {
	t.Helper()
	list, _ := body["engines"].([]any)
	for _, e := range list {
		if m, _ := e.(map[string]any); m["engine"] == engine {
			return m
		}
	}
	t.Fatalf("no %q in %v", engine, body["engines"])
	return nil
}

func TestBudgetFromTheParentsDaemonOnly(t *testing.T) {
	fx := newDelegFx(t)
	reset := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{
		{ID: "5h", UsedPercent: 62, ResetsAt: reset}, {ID: "7d", UsedPercent: 31}}})
	fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "codex", Windows: []protocol.UsageWindow{
		{ID: "capped", UsedPercent: 100, ResetsAt: reset}}})
	other := registerDaemon(t, fx.api, fx.hub, "00000000-0000-4000-8000-0000000000d2", "desktop", "app")
	fixtureOwnDesktop(t, fx.pool, other, "owner-1")
	other.SetAvailableEngines([]string{"claude"})
	other.SetUsageLimits(protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 5}}})

	code, body, _ := fx.do("GET", "/api/sessions/"+fx.parent+"/budget", fx.parentTok, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("code %d %v", code, body)
	}
	if body["daemon"] != "laptop" {
		t.Errorf("daemon = %v", body["daemon"])
	}
	if _, ok := body["fetched_at"].(string); !ok {
		t.Errorf("fetched_at missing: %v", body)
	}
	cl := budgetEngineOf(t, body, "claude")
	ws, _ := cl["windows"].([]any)
	if cl["available"] != true || len(ws) != 2 {
		t.Fatalf("claude = %v", cl)
	}
	w0 := ws[0].(map[string]any)
	if w0["id"] != "5h" || w0["used_percent"] != float64(62) || w0["resets_at"] != reset.Format(time.RFC3339) {
		t.Errorf("5h window = %v", w0)
	}
	if _, has := cl["capped_until"]; has {
		t.Errorf("claude is not capped: %v", cl)
	}
	cx := budgetEngineOf(t, body, "codex")
	if cx["capped_until"] != reset.Format(time.RFC3339) {
		t.Errorf("codex capped_until = %v", cx["capped_until"])
	}
	if hm := budgetEngineOf(t, body, "hermes"); hm["available"] != true {
		t.Errorf("hermes available = %v", hm["available"])
	}
	if oc := budgetEngineOf(t, body, "openclaw"); oc["available"] != false {
		t.Errorf("openclaw available = %v", oc["available"])
	}
	// The raw bytes carry the documented lowercase names.
	rec := fx.rawGet("/api/sessions/"+fx.parent+"/budget", fx.parentTok)
	var raw struct {
		Engines []map[string]json.RawMessage `json:"engines"`
	}
	if err := json.Unmarshal(rec, &raw); err != nil || len(raw.Engines) == 0 {
		t.Fatalf("raw: %v %s", err, rec)
	}
	for _, k := range []string{"engine", "available", "windows"} {
		if _, ok := raw.Engines[0][k]; !ok {
			t.Errorf("raw engine lacks %q: %s", k, rec)
		}
	}
	// A parent on the other daemon sees only that daemon's numbers.
	p2, tok2 := fx.session(sessOpts{owner: "owner-1", daemonID: other.ID})
	_, b2, _ := fx.do("GET", "/api/sessions/"+p2+"/budget", tok2, nil, nil)
	if b2["daemon"] != "desktop" {
		t.Fatalf("daemon = %v", b2["daemon"])
	}
	ws2, _ := budgetEngineOf(t, b2, "claude")["windows"].([]any)
	if len(ws2) != 1 || ws2[0].(map[string]any)["used_percent"] != float64(5) {
		t.Errorf("other daemon's claude windows = %v", ws2)
	}
	if cx2 := budgetEngineOf(t, b2, "codex"); len(cx2["windows"].([]any)) != 0 || cx2["available"] != false {
		t.Errorf("codex on the other daemon = %v", cx2)
	}
}

func (fx *delegFx) rawGet(path, tok string) []byte {
	fx.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	return rec.Body.Bytes()
}

func TestBudgetLocalDetection(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.SetAvailableEngines([]string{"claude", "hermes", "openclaw"})
	get := func() map[string]any {
		code, body, _ := fx.do("GET", "/api/sessions/"+fx.parent+"/budget", fx.parentTok, nil, nil)
		if code != http.StatusOK {
			t.Fatalf("code %d", code)
		}
		return body
	}
	b := get()
	hm := budgetEngineOf(t, b, "hermes")
	if _, local := hm["local"]; local || hm["note"] != "usage unknown (remote provider)" || len(hm["windows"].([]any)) != 0 {
		t.Errorf("remote hermes = %v", hm)
	}
	fx.dc.SetLocalEngines([]string{"hermes"})
	b = get()
	hm = budgetEngineOf(t, b, "hermes")
	if hm["local"] != true || hm["note"] != "local (runs on your hardware)" || len(hm["windows"].([]any)) != 0 {
		t.Errorf("local hermes = %v", hm)
	}
	if oc := budgetEngineOf(t, b, "openclaw"); oc["local"] != nil {
		t.Errorf("openclaw is not local: %v", oc)
	}
	// Claude and Codex are never local, whatever the daemon says.
	fx.dc.SetLocalEngines([]string{"claude", "codex"})
	if cl := budgetEngineOf(t, get(), "claude"); cl["local"] != nil {
		t.Errorf("claude local = %v", cl)
	}
}

func TestBudgetNoDaemonNote(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.UnregisterIfCurrent(fx.dc)
	code, body, _ := fx.do("GET", "/api/sessions/"+fx.parent+"/budget", fx.parentTok, nil, nil)
	if code != http.StatusOK || body["note"] != "no desktop daemon" {
		t.Fatalf("disconnected: %d %v", code, body)
	}
	if list, ok := body["engines"].([]any); !ok || len(list) != 0 {
		t.Errorf("engines = %v (must be an empty array)", body["engines"])
	}
	cl, ctok := fx.session(sessOpts{owner: "owner-1", runtime: runnerRuntimeCluster})
	code, body, _ = fx.do("GET", "/api/sessions/"+cl+"/budget", ctok, nil, nil)
	if code != http.StatusOK || body["note"] != "no desktop daemon" {
		t.Fatalf("cluster: %d %v", code, body)
	}
}

func TestBudgetAccess(t *testing.T) {
	fx := newDelegFx(t)
	path := "/api/sessions/" + fx.parent + "/budget"
	_, otherTok := fx.session(sessOpts{owner: "owner-1"})
	priv, _ := fx.session(sessOpts{owner: "owner-1", private: true})
	for name, c := range map[string]struct {
		tok  string
		path string
		want int
	}{
		"own session token":      {fx.parentTok, path, 200},
		"owner browser":          {fx.human("owner-1"), path, 200},
		"another session token":  {otherTok, path, 404},
		"another person":         {fx.human("owner-2"), path, 404},
		"agent token":            {fx.agentToken("owner-1"), path, 404},
		"assist token":           {fx.assistToken(fx.parent), path, 404},
		"daemon master token":    {delegDaemonToken, path, 404},
		"no credential":          {"", path, 404},
		"garbage":                {"zz", path, 404},
		"unknown session":        {fx.parentTok, "/api/sessions/" + newUUID() + "/budget", 404},
		"private, another human": {fx.human("owner-2"), "/api/sessions/" + priv + "/budget", 404},
	} {
		code, body, _ := fx.do("GET", c.path, c.tok, nil, nil)
		if code != c.want {
			t.Errorf("%s: %d %v, want %d", name, code, body, c.want)
		}
		if c.want == 404 && body["error"] != "not found" {
			t.Errorf("%s: not the uniform 404: %v", name, body)
		}
	}
}

func TestHeartbeatUpdatesLocalEngines(t *testing.T) {
	fx := newDelegFx(t)
	send := func(hb protocol.DaemonHeartbeat) {
		hb.Type = "daemon_heartbeat"
		raw, err := json.Marshal(hb)
		if err != nil {
			t.Fatal(err)
		}
		if !handleDaemonFrame(t.Context(), fx.hub, fx.dc, fx.pool, raw, envelope{Type: "daemon_heartbeat"}) {
			t.Fatal("heartbeat ended the connection")
		}
	}
	if fx.dc.LocalEngine("hermes") {
		t.Fatal("local before any report")
	}
	send(protocol.DaemonHeartbeat{LocalEngines: []string{"hermes"}})
	if !fx.dc.LocalEngine("hermes") || fx.dc.LocalEngine("openclaw") {
		t.Fatal("heartbeat did not set local_engines")
	}
	// The field is omitted when empty, so a heartbeat without it clears the list.
	send(protocol.DaemonHeartbeat{})
	if fx.dc.LocalEngine("hermes") {
		t.Fatal("an empty heartbeat did not clear local_engines")
	}
}

func TestLocalEnginesWireForm(t *testing.T) {
	has := func(v any) bool {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		_, ok := m["local_engines"]
		return ok
	}
	if !has(protocol.DaemonHello{LocalEngines: []string{"hermes"}}) || !has(protocol.DaemonHeartbeat{LocalEngines: []string{"hermes"}}) {
		t.Error("local_engines is not in the marshaled hello or heartbeat")
	}
	if has(protocol.DaemonHello{}) || has(protocol.DaemonHeartbeat{}) {
		t.Error("an empty local_engines must be omitted")
	}
}

func TestHelloWithoutLocalEnginesLeavesLocalFalse(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.SetLocalEngines([]string{"hermes"})
	// An older daemon's hello has no local_engines key; the hello path applies hello.LocalEngines.
	var hello protocol.DaemonHello
	if err := json.Unmarshal([]byte(`{"type":"daemon_hello","available_engines":["claude","hermes"]}`), &hello); err != nil {
		t.Fatal(err)
	}
	fx.dc.SetLocalEngines(hello.LocalEngines)
	if fx.dc.LocalEngine("hermes") || fx.dc.LocalEngine("openclaw") {
		t.Fatal("a hello without local_engines left an engine local")
	}
}

func TestBudgetExpiresWindowsWithoutAnotherReport(t *testing.T) {
	now := time.Date(2030, time.October, 15, 15, 0, 0, 0, time.UTC)
	dc := &DaemonConn{}
	for _, provider := range []string{"claude", "codex"} {
		dc.SetUsageLimits(protocol.UsageLimits{Provider: provider, Windows: []protocol.UsageWindow{
			{ID: "5h", UsedPercent: 100, ResetsAt: now},
			{ID: "7d", UsedPercent: 35, ResetsAt: now.Add(time.Hour)},
			{ID: "capped", UsedPercent: 100, ResetsAt: now},
		}})
		before := budgetEngineFor(dc, provider, now.Add(-time.Second))
		if len(before.Windows) != 3 || before.CappedUntil == nil {
			t.Fatalf("before: %+v", before)
		}
		after := budgetEngineFor(dc, provider, now)
		if len(after.Windows) != 1 || after.Windows[0].ID != "7d" || after.CappedUntil != nil {
			t.Errorf("%s expired windows still shown at limit: %+v", provider, after)
		}
	}
}

func TestClusterBudgetExpiresWindowsWithoutAnotherReport(t *testing.T) {
	now := time.Date(2030, time.October, 15, 15, 0, 0, 0, time.UTC)
	dc := &DaemonConn{}
	for _, provider := range []string{"claude", "codex"} {
		dc.SetUsageLimits(protocol.UsageLimits{Provider: provider, Windows: []protocol.UsageWindow{
			{ID: "5h", UsedPercent: 100, ResetsAt: now},
			{ID: "7d", UsedPercent: 35, ResetsAt: now.Add(time.Hour)},
		}})
	}
	got := budgetForCluster(dc, []string{"claude", "codex"}, true, nil, now)
	for _, b := range got.Engines {
		if b.Engine != "claude" && b.Engine != "codex" {
			continue
		}
		if len(b.Windows) != 1 || b.Windows[0].ID != "7d" {
			t.Errorf("%s expired cluster budget window retained: %+v", b.Engine, b)
		}
	}
}
