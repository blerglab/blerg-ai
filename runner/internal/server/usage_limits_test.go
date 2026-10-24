package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func nextLimitsChanged(t *testing.T, bc *BrowserConn) protocol.DaemonLimitsChanged {
	t.Helper()
	select {
	case raw := <-bc.send:
		var m protocol.DaemonLimitsChanged
		if err := json.Unmarshal(raw, &m); err != nil || m.Type != "daemon_limits_changed" {
			t.Fatalf("browser got %s, want daemon_limits_changed", raw)
		}
		return m
	case <-time.After(15 * time.Second):
		t.Fatal("no daemon_limits_changed broadcast")
	}
	return protocol.DaemonLimitsChanged{}
}

// A daemon's usage limits are kept per provider, pushed to browsers as the
// daemon's full set, and carried in initial_state.
func TestUsageLimitsStoredBroadcastAndInInitialState(t *testing.T) {
	hub, ws, _, bc := sidebarHubFixture(t)
	reset := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	handleUsageLimits(hub, ws, protocol.UsageLimits{Type: "usage_limits", Provider: "codex", Windows: []protocol.UsageWindow{
		{ID: "5h", UsedPercent: 42, ResetsAt: reset}, {ID: "7d", UsedPercent: 7},
	}, FetchedAt: time.Now()})
	m := nextLimitsChanged(t, bc)
	if m.DaemonID != "00000000-0000-4000-8000-0000000000b1" || len(m.UsageLimits) != 1 || m.UsageLimits[0].Provider != "codex" || len(m.UsageLimits[0].Windows) != 2 {
		t.Fatalf("broadcast = %+v", m)
	}

	handleUsageLimits(hub, ws, protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{{ID: "capped", UsedPercent: 100, ResetsAt: reset}}})
	if m := nextLimitsChanged(t, bc); len(m.UsageLimits) != 2 || m.UsageLimits[0].Provider != "claude" || m.UsageLimits[1].Provider != "codex" {
		t.Fatalf("both providers = %+v", m.UsageLimits)
	}

	sendInitialState(context.Background(), bc, hub, nil)
	var st protocol.InitialState
	if err := json.Unmarshal(<-bc.send, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Daemons) != 1 || len(st.Daemons[0].UsageLimits) != 2 {
		t.Fatalf("initial_state daemons = %+v", st.Daemons)
	}
	if got := st.Daemons[0].UsageLimits[1].Windows[0]; got.ID != "5h" || got.UsedPercent != 42 || !got.ResetsAt.Equal(reset) {
		t.Fatalf("codex 5h window = %+v", got)
	}

	// An empty report withdraws the provider (Claude's cap lifted).
	handleUsageLimits(hub, ws, protocol.UsageLimits{Provider: "claude"})
	if m := nextLimitsChanged(t, bc); len(m.UsageLimits) != 1 || m.UsageLimits[0].Provider != "codex" {
		t.Fatalf("after the cap lifted = %+v", m.UsageLimits)
	}
	handleUsageLimits(hub, ws, protocol.UsageLimits{Provider: "codex"})
	if m := nextLimitsChanged(t, bc); m.UsageLimits == nil || len(m.UsageLimits) != 0 {
		t.Fatalf("nothing left must be an empty list: %+v", m.UsageLimits)
	}
}

// What a daemon reports is checked: an unknown provider or window is dropped,
// percentages are clamped, an absurd reset is forgotten, and a cluster pod's
// report is ignored.
func TestUsageLimitsSanitised(t *testing.T) {
	hub, ws, pod, bc := sidebarHubFixture(t)

	handleUsageLimits(hub, pod, protocol.UsageLimits{Provider: "codex", Windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 1}}})
	handleUsageLimits(hub, ws, protocol.UsageLimits{Provider: "gemini", Windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 1}}})
	select {
	case m := <-bc.send:
		t.Fatalf("an ignored report was broadcast: %s", m)
	case <-time.After(100 * time.Millisecond):
	}

	far := time.Now().Add(365 * 24 * time.Hour)
	handleUsageLimits(hub, ws, protocol.UsageLimits{Provider: "codex", FetchedAt: far, Windows: []protocol.UsageWindow{
		{ID: "5h", UsedPercent: 140, ResetsAt: far},
		{ID: "5h", UsedPercent: 3},
		{ID: "monthly", UsedPercent: 5},
		{ID: "7d", UsedPercent: -4},
	}})
	m := nextLimitsChanged(t, bc)
	w := m.UsageLimits[0].Windows
	if len(w) != 2 || w[0].ID != "5h" || w[0].UsedPercent != 100 || !w[0].ResetsAt.IsZero() || w[1].ID != "7d" || w[1].UsedPercent != 0 {
		t.Fatalf("sanitised windows = %+v", w)
	}
	if fa := m.UsageLimits[0].FetchedAt; fa.After(time.Now().Add(time.Minute)) {
		t.Fatalf("a fetched_at in the future was kept: %v", fa)
	}
	if m.UsageLimits[0].Type != "" {
		t.Fatalf("stored entries carry no message type: %+v", m.UsageLimits[0])
	}
}

// The spend-limit windows of a work account (no reset time) pass the server's check.
func TestUsageLimitsKeepsSpendWindows(t *testing.T) {
	hub, ws, _, bc := sidebarHubFixture(t)
	handleUsageLimits(hub, ws, protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{
		{ID: "month", UsedPercent: 0.7}, {ID: "day", UsedPercent: 12}, {ID: "spend-week", UsedPercent: 40}, {ID: "capped", UsedPercent: 100},
	}})
	w := nextLimitsChanged(t, bc).UsageLimits[0].Windows
	if len(w) != 4 || w[0].ID != "month" || w[0].UsedPercent != 0.7 || !w[0].ResetsAt.IsZero() || w[3].ID != "capped" {
		t.Fatalf("windows = %+v", w)
	}
}

// A Codex account with no window is kept as such ("no_windows"), not taken for a withdrawal, so the
// sidebar can say so; Codex's spend limit ("spend") passes; a note beside windows, or an unknown
// note, is dropped.
func TestUsageLimitsKeepsTheNoWindowsNoteAndCodexSpend(t *testing.T) {
	hub, ws, _, bc := sidebarHubFixture(t)
	handleUsageLimits(hub, ws, protocol.UsageLimits{Provider: "codex", Windows: []protocol.UsageWindow{}, Note: "no_windows"})
	m := nextLimitsChanged(t, bc)
	if len(m.UsageLimits) != 1 || m.UsageLimits[0].Provider != "codex" || m.UsageLimits[0].Note != "no_windows" || len(m.UsageLimits[0].Windows) != 0 {
		t.Fatalf("a no-window report = %+v", m.UsageLimits)
	}
	reset := time.Now().Add(20 * 24 * time.Hour).UTC().Truncate(time.Second)
	handleUsageLimits(hub, ws, protocol.UsageLimits{Provider: "codex", Windows: []protocol.UsageWindow{{ID: "spend", UsedPercent: 30, ResetsAt: reset}}, Note: "no_windows"})
	m = nextLimitsChanged(t, bc)
	if u := m.UsageLimits[0]; len(u.Windows) != 1 || u.Windows[0].ID != "spend" || !u.Windows[0].ResetsAt.Equal(reset) || u.Note != "" {
		t.Fatalf("a spend window = %+v", u)
	}
	handleUsageLimits(hub, ws, protocol.UsageLimits{Provider: "claude", Note: "not_read"})
	if m := nextLimitsChanged(t, bc); len(m.UsageLimits) != 2 || m.UsageLimits[0].Provider != "claude" || m.UsageLimits[0].Note != "not_read" {
		t.Fatalf("a not_read report = %+v", m.UsageLimits)
	}
	handleUsageLimits(hub, ws, protocol.UsageLimits{Provider: "claude"})
	nextLimitsChanged(t, bc)
	handleUsageLimits(hub, ws, protocol.UsageLimits{Provider: "codex", Note: "something else"})
	if m := nextLimitsChanged(t, bc); len(m.UsageLimits) != 0 {
		t.Fatalf("an unknown note must withdraw like an empty report: %+v", m.UsageLimits)
	}
}

// GET /api/daemons says the same limits as initial_state.
func TestGetDaemonsCarriesUsageLimits(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	coreSrv := newTestCoreServer(t, pub)
	t.Cleanup(coreSrv.Close)
	hub, ws, _, _ := sidebarHubFixture(t)
	api := NewAPI(hub, hub.desktopPool, "daemon-tok", nil, "")
	fixtureOwnDesktop(t, api.dbPool, ws, "human:test-user")
	api.SetCoreAuth(ownedCoreAuthClient(t, coreSrv.URL))
	token := mintTestToken(t, priv, "k1", identity.Claims{
		Sub: "human:test-user", Aud: "blerg-runner", Kind: "human",
		Caps: []string{"session.start"}, ExpiresAt: time.Now().Unix() + 60,
	})
	get := func() []protocol.DaemonInfo {
		req := httptest.NewRequest("GET", "/api/daemons", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.HandleGetDaemons(&reviewDeadlineWriter{ResponseRecorder: w}, req)
		var out []protocol.DaemonInfo
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("GET /api/daemons: %d %s", w.Code, w.Body.String())
		}
		return out
	}
	if d := get(); len(d) != 1 || d[0].UsageLimits != nil {
		t.Fatalf("no report yet = %+v", d)
	}
	handleUsageLimits(hub, ws, protocol.UsageLimits{Provider: "codex", Windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 42}}})
	if d := get(); len(d[0].UsageLimits) != 1 || d[0].UsageLimits[0].Windows[0].UsedPercent != 42 {
		t.Fatalf("daemons = %+v", d)
	}
}
