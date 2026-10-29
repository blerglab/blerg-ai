package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A daemon's report is bounded and only facts survive: unknown engines and
// values, unsafe paths and any text the daemon wrote for the browser are dropped.
func TestSanitizeEngineStatuses(t *testing.T) {
	if sanitizeEngineStatuses(nil) != nil {
		t.Fatal("an older daemon's missing report became known")
	}
	got := sanitizeEngineStatuses([]protocol.EngineStatus{
		{Engine: "codex", Status: "not_installed", Detail: "found_elsewhere", FoundAt: "~/.nvm/versions/node/v22.1.0/bin/codex",
			SearchedPath: []string{"/usr/bin", "relative", "/a b", "~/.local/bin", `/x"<script>`}, Reason: "<b>trust me</b>", Fix: "rm -rf /", Action: "evil"},
		{Engine: "codex", Status: "available"},
		{Engine: "nonesuch", Status: "available"},
		{Engine: "hermes", Status: "on_fire"},
		{Engine: "hermes", Status: "outdated", Version: "0.1.0; rm", MinVersion: "1.2", Detail: "made-up"},
	})
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	c := got[0]
	if c.Reason != "" || c.Fix != "" || c.Action != "" || c.FoundAt != "~/.nvm/versions/node/v22.1.0/bin/codex" || !slices.Equal(c.SearchedPath, []string{"/usr/bin", "~/.local/bin"}) {
		t.Fatalf("codex %+v", c)
	}
	if h := got[1]; h.Version != "" || h.MinVersion != "1.2" || h.Detail != "" {
		t.Fatalf("hermes %+v", h)
	}
}

func TestDescribeEngineStatusGivesReasonAndExactFix(t *testing.T) {
	cases := []struct {
		st                  protocol.EngineStatus
		reason, fix, action string
	}{
		{protocol.EngineStatus{Engine: "codex", Status: "not_logged_in"}, "Codex is installed on blerg but not logged in.", "Run `codex login` on blerg, then Check again.", "check_again"},
		{protocol.EngineStatus{Engine: "codex", Status: "not_installed", Detail: "found_elsewhere", FoundAt: "~/.nvm/versions/node/v22.1.0/bin/codex"},
			"Codex is installed at ~/.nvm/versions/node/v22.1.0/bin/codex on blerg, but that folder isn't on the daemon's PATH.", "Re-detect PATH", "redetect_path"},
		{protocol.EngineStatus{Engine: "codex", Status: "not_installed"}, "Codex isn't on the daemon's PATH on blerg.", "`npm install -g @openai/codex`", "redetect_path"},
		{protocol.EngineStatus{Engine: "hermes", Status: "misconfigured", Detail: "no_provider"}, "Hermes on blerg has no model provider set up.", "Run `hermes setup` on blerg", "check_again"},
		{protocol.EngineStatus{Engine: "openclaw", Status: "misconfigured", Detail: "not_onboarded"}, "OpenClaw on blerg isn't set up yet.", "Run `openclaw onboard` on blerg", "check_again"},
		{protocol.EngineStatus{Engine: "claude", Status: "not_logged_in"}, "Claude Code is installed on blerg but not logged in.", "Run `claude` on blerg and log in", "check_again"},
		{protocol.EngineStatus{Engine: "codex", Status: "outdated", Version: "0.9.0", MinVersion: "1.2"}, "Codex 0.9.0 on blerg is older than 1.2", "Update Codex on blerg", "check_again"},
		{protocol.EngineStatus{Engine: "codex", Status: "available"}, "", "", ""},
	}
	for _, c := range cases {
		got := describeEngineStatus(c.st, "blerg")
		if !strings.Contains(got.Reason, c.reason) || !strings.Contains(got.Fix, c.fix) || got.Action != c.action || (c.reason == "" && got.Reason != "") {
			t.Errorf("%s/%s: %+v", c.st.Engine, c.st.Status, got)
		}
	}
}

func TestLaunchOptionsListUnavailableEnginesWithReasons(t *testing.T) {
	h := NewHub()
	dc := reportingDaemon(h, "00000000-0000-4000-8000-0000000000e1", nil)
	dc.SetHostClaude(true, false, nil)
	dc.SetAvailableEngines([]string{"claude"})
	dc.SetEngineStatuses([]protocol.EngineStatus{
		{Engine: "claude", Status: "available"},
		{Engine: "codex", Status: "not_logged_in", FoundAt: "~/.local/bin/codex"},
		{Engine: "hermes", Status: "not_installed", Detail: "found_elsewhere", FoundAt: "~/.nvm/versions/node/v22.1.0/bin/hermes", SearchedPath: []string{"/usr/bin", "/bin"}},
		{Engine: "openclaw", Status: "misconfigured", Detail: "not_onboarded"},
	})
	a := ownedInventoryAPI(t, h, "fixture-daemon", nil, "")
	options, err := a.DiscoverLaunchOptions(context.Background(), workspaceFixturePrincipal(), LaunchOptionsRequest{Runtime: "daemon", DaemonID: dc.ID, Kind: "agent"})
	if err != nil || len(options.Engines) != 1 || options.Engines[0].ID != "claude" {
		t.Fatalf("available: %+v %v", options, err)
	}
	byID := map[string]UnavailableEngine{}
	for _, u := range options.Unavailable {
		byID[u.ID] = u
	}
	if len(byID) != 3 {
		t.Fatalf("every other known engine must be listed: %+v", options.Unavailable)
	}
	if c := byID["codex"]; c.Status != "not_logged_in" || !strings.Contains(c.Fix, "codex login") || c.DaemonID != dc.ID || c.Action != "check_again" {
		t.Fatalf("codex %+v", c)
	}
	if hm := byID["hermes"]; hm.Action != "redetect_path" || hm.FoundAt == "" || !slices.Equal(hm.SearchedPath, []string{"/usr/bin", "/bin"}) {
		t.Fatalf("hermes %+v", hm)
	}
	if o := byID["openclaw"]; !strings.Contains(o.Fix, "openclaw onboard") {
		t.Fatalf("openclaw %+v", o)
	}

	// The person logs in to Codex: the next report makes it available, no restart.
	dc.SetAvailableEngines([]string{"claude", "codex"})
	dc.SetEngineStatuses([]protocol.EngineStatus{{Engine: "claude", Status: "available"}, {Engine: "codex", Status: "available"}})
	options, _ = a.DiscoverLaunchOptions(context.Background(), workspaceFixturePrincipal(), LaunchOptionsRequest{Runtime: "daemon", DaemonID: dc.ID, Kind: "agent"})
	if !slices.ContainsFunc(options.Engines, func(e LaunchEngine) bool { return e.ID == "codex" }) ||
		slices.ContainsFunc(options.Unavailable, func(u UnavailableEngine) bool { return u.ID == "codex" }) {
		t.Fatalf("after login: %+v", options)
	}

	// An older daemon (no statuses) still gets every engine listed, with a generic reason.
	dc.SetEngineStatuses(nil)
	options, _ = a.DiscoverLaunchOptions(context.Background(), workspaceFixturePrincipal(), LaunchOptionsRequest{Runtime: "daemon", DaemonID: dc.ID, Kind: "agent"})
	if len(options.Unavailable) != 2 || !strings.Contains(options.Unavailable[0].Reason, "doesn't report") {
		t.Fatalf("older daemon: %+v", options.Unavailable)
	}

	// The sandbox: OpenClaw can never run there, and says so.
	dc.SetSandboxAvailable(true)
	options, _ = a.DiscoverLaunchOptions(context.Background(), workspaceFixturePrincipal(), LaunchOptionsRequest{Runtime: "docker", DaemonID: dc.ID, Kind: "agent"})
	if !slices.ContainsFunc(options.Unavailable, func(u UnavailableEngine) bool {
		return u.ID == "openclaw" && u.Status == "unsupported" && strings.Contains(u.Reason, "Local sandbox")
	}) {
		t.Fatalf("sandbox: %+v", options.Unavailable)
	}
}

func TestLaunchOptionsUnpinnedHostExplainsMissingEngine(t *testing.T) {
	h := NewHub()
	dc := reportingDaemon(h, "00000000-0000-4000-8000-0000000000e2", nil)
	dc.SetAvailableEngines([]string{})
	dc.SetEngineStatuses([]protocol.EngineStatus{{Engine: "codex", Status: "not_logged_in"}})
	a := ownedInventoryAPI(t, h, "fixture-daemon", nil, "")
	options, err := a.DiscoverLaunchOptions(context.Background(), workspaceFixturePrincipal(), LaunchOptionsRequest{Runtime: "daemon", Kind: "agent"})
	if err != nil || !slices.ContainsFunc(options.Unavailable, func(u UnavailableEngine) bool {
		return u.ID == "codex" && u.DaemonID == dc.ID && strings.Contains(u.Fix, "codex login on") == false && strings.Contains(u.Fix, "`codex login` on "+dc.Name)
	}) {
		t.Fatalf("%+v %v", options.Unavailable, err)
	}
}

func TestLaunchOptionsClusterExplainsUnconnectedEngine(t *testing.T) {
	f := &fakeK8s{secrets: map[string]map[string]string{"blerg-runner-agent": {"CLAUDE_CODE_OAUTH_TOKEN": b64("fixture-only")}}}
	h := NewHub()
	h.SetJobManager(newTestJobManager(t, f))
	a := ownedInventoryAPI(t, h, "fixture-daemon", nil, "")
	var options LaunchOptions
	deadline := time.Now().Add(10 * time.Second)
	for {
		options, _ = a.DiscoverLaunchOptions(context.Background(), workspaceFixturePrincipal(), LaunchOptionsRequest{Runtime: "cluster", Kind: "agent"})
		if options.State == "known" || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	byID := map[string]UnavailableEngine{}
	for _, u := range options.Unavailable {
		byID[u.ID] = u
	}
	if c := byID["codex"]; c.Status != "not_connected" || c.Reason != "Codex isn't connected for the Cluster." || c.Action != "settings" {
		t.Fatalf("codex: %+v (all %+v)", c, options)
	}
	if o := byID["openclaw"]; o.Status != "unsupported" || !strings.Contains(o.Reason, "Cluster") {
		t.Fatalf("openclaw: %+v", o)
	}
}

// engineCheckFixture is a connected desktop the test answers for.
func answerEngineCheck(t *testing.T, h *Hub, dc *DaemonConn, reply func(protocol.EngineCheck) protocol.EngineCheckResult) <-chan protocol.EngineCheck {
	t.Helper()
	got := make(chan protocol.EngineCheck, 1)
	ctx := t.Context()
	go func() {
		select {
		case raw := <-dc.send:
			var msg protocol.EngineCheck
			if json.Unmarshal(raw, &msg) != nil || msg.Type != "engine_check" {
				return
			}
			got <- msg
			res := reply(msg)
			res.Type, res.RequestID = "engine_check_result", msg.RequestID
			h.handleEngineCheckResult(dc, res)
		case <-ctx.Done():
		}
	}()
	return got
}

func TestEngineCheckEndpoint(t *testing.T) {
	f := newReposRootFixture(t)
	f.mux.HandleFunc("POST /api/daemons/{id}/engine-check", f.api.HandleEngineCheck)
	post := func(id, body string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/daemons/"+id+"/engine-check", bytes.NewBufferString(body))
		if auth {
			req.Header.Set("Authorization", "Bearer "+f.token)
		}
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(&reviewDeadlineWriter{ResponseRecorder: rec}, req)
		return rec
	}
	if rec := post(f.dc.ID, `{}`, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	if rec := post("00000000-0000-4000-8000-0000000000ff", `{}`, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown desktop: %d", rec.Code)
	}

	sent := answerEngineCheck(t, f.hub, f.dc, func(m protocol.EngineCheck) protocol.EngineCheckResult {
		return protocol.EngineCheckResult{AvailableEngines: []string{"codex"}, PathAdded: []string{"~/.nvm/versions/node/v22.1.0/bin", "bad path"},
			Statuses: []protocol.EngineStatus{{Engine: "codex", Status: "available"}, {Engine: "hermes", Status: "misconfigured", Detail: "no_provider"}}}
	})
	rec := post(f.dc.ID, `{"redetect_path":true}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if msg := <-sent; !msg.RedetectPath || msg.RequestID == "" {
		t.Fatalf("asked %+v", msg)
	}
	var body engineCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(body.AvailableEngines, []string{"codex"}) || !slices.Equal(body.PathAdded, []string{"~/.nvm/versions/node/v22.1.0/bin"}) ||
		len(body.EngineStatuses) != 2 || !strings.Contains(body.EngineStatuses[1].Fix, "hermes setup") {
		t.Fatalf("body %+v", body)
	}
	// Applied to the connection: the launch sheet's next read sees it.
	if !slices.Equal(f.dc.AvailableEngines(), []string{"codex"}) || len(f.dc.EngineStatuses()) != 2 {
		t.Fatalf("not applied: %v %+v", f.dc.AvailableEngines(), f.dc.EngineStatuses())
	}
	// /api/daemons carries the described list for Settings.
	req := httptest.NewRequest(http.MethodGet, "/api/daemons", nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	list := httptest.NewRecorder()
	f.mux.ServeHTTP(&reviewDeadlineWriter{ResponseRecorder: list}, req)
	var daemons []protocol.DaemonInfo
	if err := json.Unmarshal(list.Body.Bytes(), &daemons); err != nil || len(daemons) != 1 || len(daemons[0].EngineStatuses) != 2 || daemons[0].EngineStatuses[1].Reason == "" {
		t.Fatalf("daemons %s", list.Body.String())
	}

	// An older daemon never answers.
	prev := engineCheckTimeout
	engineCheckTimeout = 50 * time.Millisecond
	t.Cleanup(func() { engineCheckTimeout = prev })
	go func() { <-f.dc.send }()
	if rec := post(f.dc.ID, ``, true); rec.Code != http.StatusGatewayTimeout || !strings.Contains(errorBody(t, rec), "update its daemon") {
		t.Fatalf("timeout: %d %s", rec.Code, rec.Body.String())
	}
}

// An answer from another connection, or one nobody waits for, changes only
// its own connection's report.
func TestEngineCheckResultOnlyFromTheAskedDaemon(t *testing.T) {
	h := NewHub()
	asked := reportingDaemon(h, "00000000-0000-4000-8000-0000000000e3", nil)
	other := reportingDaemon(h, "00000000-0000-4000-8000-0000000000e4", nil)
	done := make(chan error, 1)
	go func() {
		_, err := h.requestEngineCheck(context.Background(), asked, false)
		done <- err
	}()
	var msg protocol.EngineCheck
	_ = json.Unmarshal(<-asked.send, &msg)
	h.handleEngineCheckResult(other, protocol.EngineCheckResult{RequestID: msg.RequestID, AvailableEngines: []string{"codex"}})
	if asked.AvailableEngines() != nil {
		t.Fatal("another daemon's answer was applied to the asked one")
	}
	h.abandonEngineChecks(asked)
	if err := <-done; !errors.Is(err, errEngineCheckGone) {
		t.Fatalf("err %v", err)
	}
}

func engineCheckPost(ctx context.Context, f *reposRootFixture, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/daemons/"+f.dc.ID+"/engine-check", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(&reviewDeadlineWriter{ResponseRecorder: rec}, req)
	return rec
}

// A transfer that commits while the daemon answers: the former owner gets a
// 409, never the CLI locations or the PATH searched; the cached report is dropped.
func TestEngineCheckFencesOwnershipAtDelivery(t *testing.T) {
	f := newReposRootFixture(t)
	f.mux.HandleFunc("POST /api/daemons/{id}/engine-check", f.api.HandleEngineCheck)
	answerEngineCheck(t, f.hub, f.dc, func(protocol.EngineCheck) protocol.EngineCheckResult {
		if _, err := f.pool.Exec(context.Background(), `UPDATE daemons SET desktop_owner_id='human:someone-else' WHERE id=$1`, f.dc.ID); err != nil {
			t.Error(err)
		}
		return protocol.EngineCheckResult{AvailableEngines: []string{}, PathAdded: []string{"~/private-project/bin"},
			Statuses: []protocol.EngineStatus{{Engine: "codex", Status: "not_installed", SearchedPath: []string{"~/private-project/bin"}}}}
	})
	rec := engineCheckPost(context.Background(), f, `{"redetect_path":true}`)
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "private-project") {
		t.Fatalf("former owner got %d %s", rec.Code, rec.Body.String())
	}
	f.api.desktopOwnershipChanged(f.dc.ID, "human:test-user", "human:someone-else", 2)
	if f.dc.EngineStatuses() != nil {
		t.Fatal("the cached engine details outlived the transfer")
	}
}

// Requests that stop waiting still count until the daemon answers: canceling
// cannot push more checks at a desktop than its limit.
func TestCanceledEngineChecksCannotBypassTheLimit(t *testing.T) {
	f := newReposRootFixture(t)
	f.mux.HandleFunc("POST /api/daemons/{id}/engine-check", f.api.HandleEngineCheck)
	f.dc.send = make(chan []byte, 64)
	for range 12 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		engineCheckPost(ctx, f, `{"redetect_path":true}`)
		cancel()
	}
	if n := len(f.dc.send); n != engineChecksPerDesktop {
		t.Fatalf("%d checks reached the daemon, limit %d", n, engineChecksPerDesktop)
	}
	if rec := engineCheckPost(context.Background(), f, `{}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over the limit: %d", rec.Code)
	}
	// The daemon answering frees its slot.
	var msg protocol.EngineCheck
	_ = json.Unmarshal(<-f.dc.send, &msg)
	f.hub.handleEngineCheckResult(f.dc, protocol.EngineCheckResult{RequestID: msg.RequestID, Busy: true})
	sent := make(chan struct{})
	go func() { <-f.dc.send; <-f.dc.send; close(sent) }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	engineCheckPost(ctx, f, `{}`)
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("a freed slot was not reused")
	}
	// ...and so does the work bound passing without an answer.
	f.hub.engineCheckReqs.mu.Lock()
	for id, w := range f.hub.engineCheckReqs.pending {
		w.until = time.Now().Add(-time.Second)
		f.hub.engineCheckReqs.pending[id] = w
	}
	f.hub.engineCheckReqs.mu.Unlock()
	go func() { <-f.dc.send }()
	if _, err := f.hub.requestEngineCheck(ctx, f.dc, false); errors.Is(err, errEngineCheckLimit) {
		t.Fatal("expired checks still counted")
	}
}

// A daemon that reports its Claude as not logged in gets no Claude launch:
// the launch sheet lists it unavailable, with the fix.
func TestNegativeClaudeStatusGatesLaunch(t *testing.T) {
	h := NewHub()
	dc := reportingDaemon(h, "00000000-0000-4000-8000-0000000000e5", nil)
	dc.SetHostClaude(true, false, nil)
	dc.SetAvailableEngines([]string{})
	a := ownedInventoryAPI(t, h, "fixture-daemon", nil, "")
	if hostEngineState(dc, HostSelection{Engine: "claude", Runtime: "daemon", Kind: "agent"}) != "available" {
		t.Fatal("an older daemon's CLI no longer counts")
	}
	dc.SetEngineStatuses([]protocol.EngineStatus{{Engine: "claude", Status: "not_logged_in"}})
	if hostEngineState(dc, HostSelection{Engine: "claude", Runtime: "daemon", Kind: "agent"}) != "unavailable" {
		t.Fatal("explicit not_logged_in was ignored")
	}
	options, _ := a.DiscoverLaunchOptions(context.Background(), workspaceFixturePrincipal(), LaunchOptionsRequest{Runtime: "daemon", DaemonID: dc.ID, Kind: "agent"})
	if slices.ContainsFunc(options.Engines, func(e LaunchEngine) bool { return e.ID == "claude" }) ||
		!slices.ContainsFunc(options.Unavailable, func(u UnavailableEngine) bool {
			return u.ID == "claude" && u.Status == "not_logged_in" && strings.Contains(u.Fix, "`claude`")
		}) {
		t.Fatalf("%+v", options)
	}
}
