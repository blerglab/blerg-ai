package api_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// rawCall is one request the chat proxy passed through to the runner.
type rawCall struct {
	Method, Path, Body string
	Headers            map[string]string
}

// rawRunner is fakeRunner plus the pass-through the chat proxy needs: it records
// every request and answers with a canned body.
type rawRunner struct {
	*fakeRunner
	rmu     sync.Mutex
	calls   []rawCall
	respond func(c rawCall) (status int, contentType, body string)
}

func (f *rawRunner) Raw(_ context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	var b []byte
	if body != nil {
		b, _ = io.ReadAll(body)
	}
	c := rawCall{Method: method, Path: path, Body: string(b), Headers: headers}
	f.rmu.Lock()
	f.calls = append(f.calls, c)
	respond := f.respond
	f.rmu.Unlock()
	status, ct, out := http.StatusOK, "application/json", `{"ok":true}`
	if respond != nil {
		status, ct, out = respond(c)
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {ct}, "Set-Cookie": {"runner=secret"}},
		Body:       io.NopCloser(strings.NewReader(out)),
	}, nil
}

func (f *rawRunner) seen() []rawCall {
	f.rmu.Lock()
	defer f.rmu.Unlock()
	return append([]rawCall(nil), f.calls...)
}

// chatFixture spawns one session on a card of board A and returns its board id,
// the board's runner-session id, and the runner.
func chatFixture(t *testing.T) (srvURL func(method, path, token string, body any) *http.Response, boardB string, rsID string, fake *rawRunner) {
	t.Helper()
	srv, pool := testServer(t)
	fake = &rawRunner{fakeRunner: &fakeRunner{lifecycle: "running"}}
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})
	admin := adminToken(t)

	var a, b db.Board
	decodeBody(t, request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "chat-a", "repos": []string{"blerg-board"}}), &a)
	decodeBody(t, request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "chat-b"}), &b)
	card, err := db.CreateCard(context.Background(), pool, a.ID, db.CardParams{
		Title: strPtr("chat me"), Repos: &[]string{"blerg-board"},
	}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	var spawned struct {
		RunnerSessionID string `json:"runner_session_id"`
	}
	resp := request(t, srv, "POST", "/api/cards/"+card.Card.ID+"/spawn", admin, nil, map[string]any{})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("spawn = %d", resp.StatusCode)
	}
	decodeBody(t, resp, &spawned)
	return func(method, path, token string, body any) *http.Response {
		return request(t, srv, method, path, token, nil, body)
	}, b.ID, spawned.RunnerSessionID, fake
}

// The chat's requests reach the runner for the RUNNER's session id (the board
// names sessions by its own row id), with the body and query intact, and
// nothing of the runner's but the status, type and body comes back.
func TestChatProxyPassesThroughToTheRunnersSession(t *testing.T) {
	call, _, rsID, fake := chatFixture(t)
	member := memberToken(t)
	base := "/api/chat/sessions/" + rsID

	fake.respond = func(c rawCall) (int, string, string) {
		if strings.HasSuffix(c.Path, "/events/live?after_seq=3&tail=1") {
			return 200, "text/event-stream", "event: replay_done\ndata: {\"last_seq\":3}\n\n"
		}
		return 200, "application/json", `{"lifecycle":"running"}`
	}

	resp := call("GET", base+"/events/live?after_seq=3&tail=1", member, nil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "event: replay_done") {
		t.Fatalf("live stream = %d %q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	if resp.Header.Get("Set-Cookie") != "" {
		t.Fatal("the runner's Set-Cookie reached the browser")
	}

	for _, rt := range []struct{ method, path, want string }{
		{"GET", base, "/api/runner/sessions/ext-123"},
		{"GET", base + "/events?before_seq=9&limit=5", "/api/runner/sessions/ext-123/events?before_seq=9&limit=5"},
		{"POST", base + "/interrupt", "/api/runner/sessions/ext-123/interrupt"},
		{"POST", base + "/stop", "/api/runner/sessions/ext-123/stop"},
		{"GET", base + "/artifacts", "/api/runner/sessions/ext-123/artifacts"},
		{"GET", base + "/artifacts/a9/raw", "/api/runner/sessions/ext-123/artifacts/a9/raw"},
		{"GET", base + "/artifacts/a9/download", "/api/runner/sessions/ext-123/artifacts/a9/download"},
		{"DELETE", base + "/artifacts/a9", "/api/runner/sessions/ext-123/artifacts/a9"},
	} {
		before := len(fake.seen())
		resp := call(rt.method, rt.path, member, nil)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s %s = %d, want 200", rt.method, rt.path, resp.StatusCode)
			continue
		}
		got := fake.seen()
		if len(got) != before+1 || got[len(got)-1].Path != rt.want || got[len(got)-1].Method != rt.method {
			t.Errorf("%s %s reached the runner as %+v, want %s %s", rt.method, rt.path, got[before:], rt.method, rt.want)
		}
	}
	// The browser's own credential never travels to the runner.
	for _, c := range fake.seen() {
		for h := range c.Headers {
			if strings.EqualFold(h, "Authorization") || strings.EqualFold(h, "Cookie") {
				t.Fatalf("the proxy asked the runner client to send %s", h)
			}
		}
	}
}

// What a person types in the chat is recorded by the runner as a person's, and
// only the text travels: a browser cannot choose its own source.
func TestChatProxyMessageIsAPersons(t *testing.T) {
	call, _, rsID, fake := chatFixture(t)
	resp := call("POST", "/api/chat/sessions/"+rsID+"/messages", memberToken(t), map[string]any{"text": "do it", "source": "system"})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("message = %d, want 200", resp.StatusCode)
	}
	got := fake.seen()
	last := got[len(got)-1]
	if last.Path != "/api/runner/sessions/ext-123/message" || last.Body != `{"source":"human","text":"do it"}` {
		t.Fatalf("runner got %s %s", last.Path, last.Body)
	}
}

// Who may do what: another board's token is refused, a read-only person reads
// but changes nothing, and a person's acts are refused to the service key.
func TestChatProxyAuthorization(t *testing.T) {
	call, boardB, rsID, fake := chatFixture(t)
	admin := adminToken(t)
	base := "/api/chat/sessions/" + rsID

	var mintedB struct {
		Secret string `json:"secret"`
	}
	r := call("POST", "/api/tokens", admin, map[string]any{
		"label": "b-only", "board_id": boardB, "capabilities": []string{"card.read", "card.write"},
	})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("mint = %d", r.StatusCode)
	}
	decodeBody(t, r, &mintedB)

	reads := []struct{ method, path string }{
		{"GET", base}, {"GET", base + "/events/live"}, {"GET", base + "/events"},
		{"GET", base + "/artifacts"}, {"GET", base + "/artifacts/a1/raw"}, {"GET", base + "/artifacts/a1/download"},
	}
	writes := []struct{ method, path string }{
		{"POST", base + "/stop"}, {"POST", base + "/interrupt"}, {"DELETE", base + "/artifacts/a1"},
	}
	persons := []struct{ method, path string }{
		{"POST", base + "/messages"}, {"POST", base + "/uploads"},
	}
	all := append(append(append([]struct{ method, path string }{}, reads...), writes...), persons...)

	status := func(method, path, token string) int {
		t.Helper()
		resp := call(method, path, token, map[string]any{"text": "x"})
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	// No credential: 401, so the app renews its sign-in rather than showing a refusal.
	before := len(fake.seen())
	for _, rt := range all {
		if got := status(rt.method, rt.path, ""); got != http.StatusUnauthorized {
			t.Errorf("no token %s %s = %d, want 401", rt.method, rt.path, got)
		}
	}
	// A token for another board: 403 everywhere.
	for _, rt := range all {
		if got := status(rt.method, rt.path, mintedB.Secret); got != http.StatusForbidden {
			t.Errorf("board-B token %s %s = %d, want 403", rt.method, rt.path, got)
		}
	}
	// A person who can only read: reads pass, nothing else does.
	readOnly := testHumanToken(t, []string{"card.read"})
	for _, rt := range append(append([]struct{ method, path string }{}, writes...), persons...) {
		if got := status(rt.method, rt.path, readOnly); got != http.StatusForbidden {
			t.Errorf("read-only %s %s = %d, want 403", rt.method, rt.path, got)
		}
	}
	// The service key is not a person: it may stop and interrupt, not speak or attach.
	for _, rt := range persons {
		if got := status(rt.method, rt.path, "svc-key-123"); got != http.StatusForbidden {
			t.Errorf("service key %s %s = %d, want 403", rt.method, rt.path, got)
		}
	}
	if n := len(fake.seen()); n != before {
		t.Fatalf("the runner was contacted %d times by refused requests", n-before)
	}
	for _, rt := range reads {
		if got := status(rt.method, rt.path, readOnly); got != http.StatusOK {
			t.Errorf("read-only %s %s = %d, want 200", rt.method, rt.path, got)
		}
	}
	for _, rt := range writes {
		if got := status(rt.method, rt.path, "svc-key-123"); got != http.StatusOK {
			t.Errorf("service key %s %s = %d, want 200", rt.method, rt.path, got)
		}
	}
}

// An id the board does not know answers 404 for everyone, and the runner is
// not asked about it.
func TestChatProxyUnknownSession404(t *testing.T) {
	call, _, _, fake := chatFixture(t)
	before := len(fake.seen())
	for _, path := range []string{"", "/events/live", "/artifacts"} {
		resp := call("GET", "/api/chat/sessions/00000000-0000-0000-0000-000000000000"+path, adminToken(t), nil)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("unknown session %q = %d, want 404", path, resp.StatusCode)
		}
	}
	if n := len(fake.seen()); n != before {
		t.Fatalf("the runner was asked about an unknown session %d times", n-before)
	}
}

// A driver that cannot pass a request through (an older one) is a bad gateway,
// not a crash.
func TestChatProxyWithoutAPassThroughDriver(t *testing.T) {
	call, _, rsID, fake := chatFixture(t)
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake.fakeRunner})
	resp := call("GET", "/api/chat/sessions/"+rsID, adminToken(t), nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestChatModelAndDurableInputControls(t *testing.T) {
	call, _, id, fake := chatFixture(t)
	person := memberToken(t)
	for _, body := range []map[string]string{{"model": "codex-model", "effort": "high"}, {"text": "hello", "client_id": "stable-turn"}} {
		suffix := "/model"
		if body["text"] != "" {
			suffix = "/messages"
		}
		resp := call("POST", "/api/chat/sessions/"+id+suffix, person, body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("control HTTP %d", resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	calls := fake.seen()
	if len(calls) != 2 || !strings.Contains(calls[0].Body, `"effort":"high"`) || !strings.Contains(calls[1].Body, `"client_id":"stable-turn"`) {
		t.Fatalf("lost control settings: %+v", calls)
	}
	// An agent with board write authority must still use the self-scoped model
	// API rather than bypassing its neighbour restriction through chat.
	resp := call("POST", "/api/chat/sessions/"+id+"/model", "svc-key-123", map[string]string{"model": "other"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("nonhuman model proxy HTTP %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestChatControlWithNoRunnerIsUnavailable(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	person := humanToken(t, srv)
	b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "no runner"})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO runner_sessions(board_id,runner,external_session_id,role) VALUES($1,'test','ext','board') RETURNING id`, b.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	resp := request(t, srv, "POST", "/api/chat/sessions/"+id+"/messages", person, nil, map[string]string{"text": "hello"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured runner HTTP %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// The person's code colours come from the runner through the chat proxy: read-only, for the account
// of the signed-in person, and never for anything else.
func TestChatProxyReadsThePersonsPreferences(t *testing.T) {
	call, _, _, fake := chatFixture(t)
	fake.respond = func(c rawCall) (int, string, string) {
		return 200, "application/json", `{"code_colours":{"dark":"nord","light":"blerg","overrides":{"dark":{},"light":{}}}}`
	}
	before := len(fake.seen())
	resp := call("GET", "/api/chat/preferences", memberToken(t), nil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"dark":"nord"`) {
		t.Fatalf("preferences = %d %s", resp.StatusCode, body)
	}
	seen := fake.seen()
	if len(seen) != before+1 || seen[len(seen)-1].Method != "GET" || seen[len(seen)-1].Path != "/api/runner/preferences?account=test-human" {
		t.Fatalf("runner saw %+v", seen[before:])
	}
	if resp := call("GET", "/api/chat/preferences", "", nil); resp.StatusCode != 401 {
		t.Fatalf("anonymous = %d", resp.StatusCode)
	}
	if resp := call("PUT", "/api/chat/preferences", memberToken(t), map[string]any{}); resp.StatusCode == 200 {
		t.Fatal("the board's chat can change preferences")
	}
	// An agent (a service, not a person) is given the defaults; the runner is not asked.
	n := len(fake.seen())
	svc := testCoreToken(t, "service", []string{"card.read"})
	resp = call("GET", "/api/chat/preferences", svc, nil)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || strings.TrimSpace(string(body)) != "{}" || len(fake.seen()) != n {
		t.Fatalf("service = %d %s (runner calls %d -> %d)", resp.StatusCode, body, n, len(fake.seen()))
	}
}

// #231 review, finding 1: the board reaches the runner with its shared key, which has no account,
// so a file deleted from the board's chat was always refused. The board now names the signed-in
// person it has let change the session (and whether they are an administrator) on the file
// routes, and the runner decides for them. A principal that is not a person names nobody.
func TestChatProxyFileRoutesNameThePersonForTheRunner(t *testing.T) {
	call, _, rsID, fake := chatFixture(t)
	base := "/api/chat/sessions/" + rsID
	last := func() rawCall {
		seen := fake.seen()
		return seen[len(seen)-1]
	}
	for _, rt := range []struct{ method, path string }{
		{"GET", base + "/artifacts"},
		{"DELETE", base + "/artifacts/a9"},
		{"POST", base + "/storage"},
	} {
		resp := call(rt.method, rt.path, memberToken(t), nil)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s = %d", rt.method, rt.path, resp.StatusCode)
		}
		if c := last(); c.Headers["X-Blerg-Acting-Account"] != "test-human" || c.Headers["X-Blerg-Acting-Admin"] != "" {
			t.Errorf("member %s %s: runner saw %+v", rt.method, rt.path, c.Headers)
		}
		resp = call(rt.method, rt.path, adminToken(t), nil)
		_ = resp.Body.Close()
		if c := last(); c.Headers["X-Blerg-Acting-Account"] != "test-human" || c.Headers["X-Blerg-Acting-Admin"] != "1" {
			t.Errorf("admin %s %s: runner saw %+v", rt.method, rt.path, c.Headers)
		}
	}
	// (A browser cannot name anyone itself: the proxy forwards only the headers it builds;
	// packages/chat/proxy/go's TestFileRoutesNameTheActingPerson proves it.)
	// A service principal with card.write names nobody: the runner then refuses the delete.
	before := len(fake.seen())
	svc := testCoreToken(t, "service", []string{"card.read", "card.write"})
	resp := call("DELETE", base+"/artifacts/a9", svc, nil)
	_ = resp.Body.Close()
	if seen := fake.seen(); len(seen) == before+1 {
		if _, named := seen[len(seen)-1].Headers["X-Blerg-Acting-Account"]; named {
			t.Fatal("a service principal was named as a person")
		}
	}
	// Raising storage is a person's act.
	resp = call("POST", base+"/storage", svc, nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("service storage raise = %d", resp.StatusCode)
	}
}
