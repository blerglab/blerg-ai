package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/coreauth"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// reposRootFixture is an API with one connected workstation daemon whose
// outbound messages the test reads (standing in for the daemon's end).
type reposRootFixture struct {
	hub   *Hub
	api   *API
	mux   *http.ServeMux
	dc    *DaemonConn
	token string
}

func newReposRootFixture(t *testing.T) *reposRootFixture {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	core := newTestCoreServer(t, pub)
	t.Cleanup(core.Close)

	hub := NewHub()
	dc := &DaemonConn{ID: "d1", Name: "workstation", Mode: "local", ReposRoot: "/home/dev/repos", send: make(chan []byte, 8)}
	dc.SetCheckedOutRepos([]string{"old-repo"})
	dc.SetRepoRemotes(map[string]string{"old-repo": "example-org/old-repo"})
	hub.Register(dc)

	api := NewAPI(hub, nil, "", nil, "")
	api.SetCoreAuth(coreauth.New(core.URL))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/daemons", api.HandleGetDaemons)
	mux.HandleFunc("PUT /api/daemons/{id}/repos-root", api.HandlePutDaemonReposRoot)

	token := mintTestToken(t, priv, "k1", identity.Claims{
		Sub: "human:test-user", Aud: "blerg-runner", Kind: "human",
		Caps: []string{"session.start"}, ExpiresAt: time.Now().Unix() + 60,
	})
	return &reposRootFixture{hub: hub, api: api, mux: mux, dc: dc, token: token}
}

func (f *reposRootFixture) put(t *testing.T, daemonID, body string, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/daemons/"+daemonID+"/repos-root", bytes.NewBufferString(body))
	if auth {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

// answer plays the daemon: it reads the next set_repos_root sent to dc and
// answers it with reply(msg) through the same path the read pump uses.
func (f *reposRootFixture) answer(t *testing.T, dc *DaemonConn, reply func(protocol.SetReposRoot) protocol.ReposRootResult) <-chan protocol.SetReposRoot {
	t.Helper()
	got := make(chan protocol.SetReposRoot, 1)
	go func() {
		select {
		case raw := <-dc.send:
			var msg protocol.SetReposRoot
			if json.Unmarshal(raw, &msg) != nil || msg.Type != "set_repos_root" {
				return
			}
			got <- msg
			res := reply(msg)
			res.Type = "repos_root_result"
			handleReposRootResult(context.Background(), f.hub, nil, dc, res)
		case <-time.After(5 * time.Second):
		}
	}()
	return got
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body["error"]
}

func TestPutReposRootRequiresBrowserAuth(t *testing.T) {
	f := newReposRootFixture(t)
	if rec := f.put(t, "d1", `{"repos_root":"/srv/repos"}`, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	select {
	case <-f.dc.send:
		t.Error("an unauthenticated request reached the daemon")
	default:
	}
}

func TestPutReposRootAppliesDaemonAnswer(t *testing.T) {
	f := newReposRootFixture(t)
	sent := f.answer(t, f.dc, func(m protocol.SetReposRoot) protocol.ReposRootResult {
		return protocol.ReposRootResult{RequestID: m.RequestID, OK: true, ReposRoot: "/srv/repos", CheckedOutRepos: []string{"new-repo"}}
	})

	rec := f.put(t, "d1", `{"repos_root":"/srv/repos/"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var body putReposRootResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.ReposRoot != "/srv/repos" {
		t.Errorf("body %s, want the root the daemon applied", rec.Body.String())
	}
	msg := <-sent
	if msg.ReposRoot != "/srv/repos/" || msg.RequestID == "" {
		t.Errorf("daemon was asked %+v", msg)
	}

	if got := f.dc.CurrentReposRoot(); got != "/srv/repos" {
		t.Errorf("CurrentReposRoot = %q", got)
	}
	if got := f.dc.CheckedOutRepos(); !slices.Equal(got, []string{"new-repo"}) {
		t.Errorf("checked-out repos %v, want the new root's", got)
	}
	if _, ok := f.dc.RepoRemote("old-repo"); ok {
		t.Error("the old root's remotes survived the change")
	}

	// GET /api/daemons shows the new root.
	req := httptest.NewRequest(http.MethodGet, "/api/daemons", nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	list := httptest.NewRecorder()
	f.mux.ServeHTTP(list, req)
	var daemons []protocol.DaemonInfo
	_ = json.Unmarshal(list.Body.Bytes(), &daemons)
	if len(daemons) != 1 || daemons[0].ReposRoot != "/srv/repos" {
		t.Errorf("GET /api/daemons = %s", list.Body.String())
	}
}

func TestPutReposRootRelaysRefusal(t *testing.T) {
	f := newReposRootFixture(t)
	f.answer(t, f.dc, func(m protocol.SetReposRoot) protocol.ReposRootResult {
		return protocol.ReposRootResult{RequestID: m.RequestID, OK: false, ReposRoot: "/home/dev/repos", Error: "the daemon can't write to /srv/ro: permission denied"}
	})
	rec := f.put(t, "d1", `{"repos_root":"/srv/ro"}`, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(errorBody(t, rec), "permission denied") {
		t.Fatalf("status %d body %s, want 422 with the daemon's reason", rec.Code, rec.Body.String())
	}
	if got := f.dc.CurrentReposRoot(); got != "/home/dev/repos" {
		t.Errorf("root changed to %q on a refusal", got)
	}
	if got := f.dc.CheckedOutRepos(); !slices.Equal(got, []string{"old-repo"}) {
		t.Errorf("repos changed to %v on a refusal", got)
	}
}

func TestPutReposRootServerSideRefusals(t *testing.T) {
	f := newReposRootFixture(t)
	cases := []struct {
		name, id, body string
		want           int
	}{
		{"bad json", "d1", `{`, http.StatusBadRequest},
		{"empty", "d1", `{"repos_root":""}`, http.StatusUnprocessableEntity},
		{"relative", "d1", `{"repos_root":"repos"}`, http.StatusUnprocessableEntity},
		{"control chars", "d1", `{"repos_root":"/srv/a\nb"}`, http.StatusUnprocessableEntity},
		{"too long", "d1", `{"repos_root":"/` + strings.Repeat("a", maxReposRootLen) + `"}`, http.StatusUnprocessableEntity},
		{"unknown daemon", "nope", `{"repos_root":"/srv/repos"}`, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := f.put(t, tc.id, tc.body, true); rec.Code != tc.want {
				t.Errorf("status %d (%s), want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
	select {
	case raw := <-f.dc.send:
		t.Errorf("a refused request reached the daemon: %s", raw)
	default:
	}
}

// A cluster session pod connects as a per-session "daemon"; it has no repos
// root anyone configures.
func TestPutReposRootRefusesRunnerPods(t *testing.T) {
	f := newReposRootFixture(t)
	pod := &DaemonConn{ID: "pod1", Name: "runner-x", Mode: "runner", send: make(chan []byte, 1)}
	f.hub.Register(pod)
	if rec := f.put(t, "pod1", `{"repos_root":"/srv/repos"}`, true); rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
	if len(pod.send) != 0 {
		t.Error("the pod was asked")
	}
}

func TestPutReposRootTimesOutOnSilentDaemon(t *testing.T) {
	f := newReposRootFixture(t)
	prev := reposRootTimeout
	reposRootTimeout = 50 * time.Millisecond
	t.Cleanup(func() { reposRootTimeout = prev })

	rec := f.put(t, "d1", `{"repos_root":"/srv/repos"}`, true)
	if rec.Code != http.StatusGatewayTimeout || !strings.Contains(errorBody(t, rec), "older version") {
		t.Errorf("status %d body %s, want 504 naming an older daemon", rec.Code, rec.Body.String())
	}
	if f.dc.CurrentReposRoot() != "/home/dev/repos" {
		t.Error("root changed without an answer")
	}
}

// An answer is accepted only from the connection that was asked: another
// daemon (or a reconnected one) cannot complete the request.
func TestPutReposRootIgnoresAnswerFromAnotherDaemon(t *testing.T) {
	f := newReposRootFixture(t)
	prev := reposRootTimeout
	reposRootTimeout = 200 * time.Millisecond
	t.Cleanup(func() { reposRootTimeout = prev })

	other := &DaemonConn{ID: "d2", Name: "other", Mode: "local", ReposRoot: "/other", send: make(chan []byte, 1)}
	f.hub.Register(other)
	go func() {
		raw := <-f.dc.send
		var msg protocol.SetReposRoot
		_ = json.Unmarshal(raw, &msg)
		handleReposRootResult(context.Background(), f.hub, nil, other, protocol.ReposRootResult{
			Type: "repos_root_result", RequestID: msg.RequestID, OK: true, ReposRoot: "/hijacked",
		})
	}()
	rec := f.put(t, "d1", `{"repos_root":"/srv/repos"}`, true)
	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("status %d, want 504 (the foreign answer must be ignored)", rec.Code)
	}
	if f.dc.CurrentReposRoot() != "/home/dev/repos" {
		t.Errorf("asked daemon's root changed to %q", f.dc.CurrentReposRoot())
	}
}

func TestPutReposRootDaemonDisconnectsMidRequest(t *testing.T) {
	f := newReposRootFixture(t)
	go func() {
		<-f.dc.send
		handleDaemonDisconnect(f.hub, f.dc, nil)
	}()
	start := time.Now()
	rec := f.put(t, "d1", `{"repos_root":"/srv/repos"}`, true)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status %d (%s), want 502", rec.Code, rec.Body.String())
	}
	if time.Since(start) > reposRootTimeout/2 {
		t.Error("the disconnect was only noticed at the timeout")
	}
}

func TestPutReposRootSendBufferFull(t *testing.T) {
	f := newReposRootFixture(t)
	full := &DaemonConn{ID: "d3", Name: "busy", Mode: "local", ReposRoot: "/r", send: make(chan []byte)} // unbuffered, nobody reading
	f.hub.Register(full)
	if rec := f.put(t, "d3", `{"repos_root":"/srv/repos"}`, true); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
}

// A heartbeat that reports a different root (a change made while no one
// was waiting, or after a request timed out) updates the server's view; an
// implausible one is ignored.
func TestApplyReposRootFromDaemonReport(t *testing.T) {
	dc := &DaemonConn{ID: "d1", ReposRoot: "/a"}
	applyReposRoot(context.Background(), nil, dc, "/b")
	if dc.CurrentReposRoot() != "/b" {
		t.Errorf("root %q, want /b", dc.CurrentReposRoot())
	}
	for _, bad := range []string{"relative", "/x\ny", ""} {
		applyReposRoot(context.Background(), nil, dc, bad)
		if dc.CurrentReposRoot() != "/b" {
			t.Errorf("reported %q changed the root to %q", bad, dc.CurrentReposRoot())
		}
	}
}
