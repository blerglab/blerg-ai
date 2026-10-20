package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceBrokerBindsConnectionSessionAndConsumesOnce(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := h.requestWorkspaceFile(ctx, dc, "session", "README.md"); done <- err }()
	var req protocol.ReadWorkspaceFile
	if err := json.Unmarshal(<-dc.send, &req); err != nil {
		t.Fatal(err)
	}
	reply := protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: req.RequestID, SessionID: "session", OK: true, Path: "README.md", Size: 2, ContentB64: base64.StdEncoding.EncodeToString([]byte("hi")), View: "text", ContentType: "text/plain", Mtime: time.Now().UTC().Format(time.RFC3339Nano)}
	other := &DaemonConn{ID: "other"}
	h.deliverWorkspaceFileResult(other, reply)
	wrong := reply
	wrong.SessionID = "foreign"
	h.deliverWorkspaceFileResult(dc, wrong)
	select {
	case err := <-done:
		t.Fatalf("forged reply accepted: %v", err)
	default:
	}
	h.deliverWorkspaceFileResult(dc, reply)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.deliverWorkspaceFileResult(dc, reply) // consumed request cannot be answered twice
	h.workspaceFileReqs.mu.Lock()
	defer h.workspaceFileReqs.mu.Unlock()
	if len(h.workspaceFileReqs.pending) != 0 {
		t.Fatal("pending request leaked")
	}
}

func TestWorkspaceFileOwnerOnlyAndOldDaemonTimeout(t *testing.T) {
	fx := newTopicFx(t)
	fx.api.RegisterWorkspaceFileRoutes(fx.mux)
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 8)}
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	get := func(sid, tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sid+"/workspace-file?path=README.md", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(rec, req)
		return rec
	}
	for _, tok := range []string{fx.token(topicOther, "human"), fx.token(topicOwner, "agent"), fx.token(topicOwner, "service")} {
		rec := get(fx.sess, tok)
		if rec.Code != http.StatusForbidden || len(dc.send) != 0 {
			t.Fatalf("nonowner/kind access=%d queued=%d", rec.Code, len(dc.send))
		}
	}
	fx.api.hub.SetSessionOwner(fx.sess, "missing-daemon")
	if rec := get(fx.sess, fx.token(topicOwner, "human")); rec.Code != http.StatusConflict {
		t.Fatalf("missing daemon %d", rec.Code)
	}
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.sess+"/workspace-file?path=README.md", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	ctx, cancel := context.WithTimeout(req.Context(), 10*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("old daemon=%d %s", rec.Code, rec.Body)
	}
}

func TestWorkspaceFileReplyValidationAndAccess(t *testing.T) {
	fx := newTopicFx(t)
	fx.api.RegisterWorkspaceFileRoutes(fx.mux)
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 8)}
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	fx.api.hub.SetSessionOwner(fx.privSess, dc.ID)
	run := func(sid, token, name string, modify func(*protocol.WorkspaceFileResult)) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sid+"/workspace-file?path="+name, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { defer close(done); fx.mux.ServeHTTP(rec, req) }()
		var raw []byte
		select {
		case raw = <-dc.send:
		case <-time.After(15 * time.Second):
			t.Fatal("file request not dispatched")
		}
		var q protocol.ReadWorkspaceFile
		if err := json.Unmarshal(raw, &q); err != nil {
			t.Fatal(err)
		}
		reply := protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: q.RequestID, SessionID: q.SessionID, OK: true, Path: "README.md", Size: 2, ContentB64: "aGk=", Mtime: time.Now().UTC().Format(time.RFC3339Nano), View: "html", ContentType: "text/html"}
		if modify != nil {
			modify(&reply)
		}
		fx.api.hub.deliverWorkspaceFileResult(dc, reply)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("file response did not complete")
		}
		return rec
	}
	owner := fx.token(topicOwner, "human")
	admin := fx.token(topicOwner, "human", "account.manage")
	privOwner := fx.token(topicOther, "human")
	for _, sid := range []string{fx.sess, fx.privSess} {
		tok := owner
		if sid == fx.privSess {
			tok = privOwner // a private session: its owner alone
		}
		rec := run(sid, tok, "README.md", nil)
		if rec.Code != 200 {
			t.Fatalf("allowed %d %s", rec.Code, rec.Body)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["view"] != "markdown" || got["content_b64"] != "aGk=" || got["content_type"] != "text/markdown; charset=utf-8" || got["request_id"] != nil || got["session_id"] != nil || got["type"] != nil || rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("unsafe reply %s", rec.Body)
		}
	}
	// Someone else's private session is not there for an administrator either: the same 404 an unknown
	// session gets, so the answer does not reveal that it exists.
	refused := func(sid, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sid+"/workspace-file?path=README.md", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(rec, req) // answered before any daemon is asked
		select {
		case raw := <-dc.send:
			t.Fatalf("a refused read reached the daemon: %s", raw)
		default:
		}
		return rec
	}
	for _, tok := range []string{admin, owner} {
		priv := refused(fx.privSess, tok)
		unknown := refused(newUUID(), tok)
		msg := func(r *httptest.ResponseRecorder) string {
			var b struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(r.Body.Bytes(), &b)
			return b.Error
		}
		if priv.Code != 404 || unknown.Code != 404 || msg(priv) != msg(unknown) || msg(priv) == "" {
			t.Fatalf("private session for a non-owner: %d %s; unknown: %d %s", priv.Code, priv.Body, unknown.Code, unknown.Body)
		}
	}
	rec := run(fx.sess, owner, "README.md", func(*protocol.WorkspaceFileResult) { dc.RuntimeTokenID = newUUID() })
	dc.RuntimeTokenID = ""
	if rec.Code != http.StatusConflict {
		t.Fatalf("invalid runtime claim returned content: %d %s", rec.Code, rec.Body)
	}
	longContent := strings.Repeat("x", 8192)
	rec = run(fx.sess, owner, "README.md", func(p *protocol.WorkspaceFileResult) {
		p.Size = int64(len(longContent))
		p.ContentB64 = base64.StdEncoding.EncodeToString([]byte(longContent))
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid streamed payload %d %s", rec.Code, rec.Body)
	}
	cases := []struct {
		name   string
		change func(*protocol.WorkspaceFileResult)
		status int
	}{
		{"wrong path", func(p *protocol.WorkspaceFileResult) { p.Path = "elsewhere.md" }, 409},
		{"absolute path", func(p *protocol.WorkspaceFileResult) { p.Path = "/private/README.md" }, 409},
		{"negative size", func(p *protocol.WorkspaceFileResult) { p.Size = -1 }, 409},
		{"size mismatch", func(p *protocol.WorkspaceFileResult) { p.Size = 3 }, 409},
		{"over cap", func(p *protocol.WorkspaceFileResult) { p.Size = workspaceFileMaxBytes + 1 }, 409},
		{"bad base64", func(p *protocol.WorkspaceFileResult) { p.ContentB64 = "***" }, 409},
		{"noncanonical base64", func(p *protocol.WorkspaceFileResult) { p.ContentB64 = "aGk=\n" }, 409},
		{"carriage return base64", func(p *protocol.WorkspaceFileResult) { p.ContentB64 = "aGk=\r" }, 409},
		{"invalid padding bits", func(p *protocol.WorkspaceFileResult) { p.ContentB64 = "aGl=" }, 409},
		{"trailing decoded bytes", func(p *protocol.WorkspaceFileResult) { p.ContentB64 = "aGl4" }, 409},
		{"invalid suffix", func(p *protocol.WorkspaceFileResult) { p.ContentB64 = "aGk=*" }, 409},
		{"padding followed by data", func(p *protocol.WorkspaceFileResult) { p.ContentB64 = "aGk=AAAA" }, 409},
		{"invalid suffix after head", func(p *protocol.WorkspaceFileResult) {
			p.Size = 4096
			p.ContentB64 = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 4096))) + "*"
		}, 409},
		{"extra decoded bytes after head", func(p *protocol.WorkspaceFileResult) {
			p.Size = 1024
			p.ContentB64 = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 1025)))
		}, 409},
		{"bad mtime", func(p *protocol.WorkspaceFileResult) { p.Mtime = "private/absolute" }, 409},
		{"unexpected truncation", func(p *protocol.WorkspaceFileResult) { p.Truncated = true }, 409},
		{"refused", func(p *protocol.WorkspaceFileResult) {
			p.OK = false
			p.ErrorCode = "forbidden"
			p.Error = "private absolute path and fixture-secret"
		}, 403},
		{"secret", func(p *protocol.WorkspaceFileResult) {
			p.OK = false
			p.ErrorCode = "forbidden"
			p.Error = "Blerg doesn't open files that usually hold secrets (fixture-secret)."
		}, 403},
		{"missing", func(p *protocol.WorkspaceFileResult) { p.OK = false; p.ErrorCode = "not_found" }, 404},
		{"too large", func(p *protocol.WorkspaceFileResult) { p.OK = false; p.ErrorCode = "too_large" }, 413},
		{"unknown error", func(p *protocol.WorkspaceFileResult) {
			p.OK = false
			p.ErrorCode = "fixture-secret"
			p.Error = "fixture-secret"
		}, 409},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := run(fx.sess, owner, "README.md", tc.change)
			if rec.Code != tc.status || strings.Contains(rec.Body.String(), "fixture-secret") || !strings.Contains(rec.Body.String(), "error_id") {
				t.Fatalf("refusal %d %s", rec.Code, rec.Body)
			}
		})
	}
	for _, name := range []string{"../README.md", "%2e%2e%2fREADME.md", "/etc/passwd", "~/.env", "README%00.md", `dir%5Cfile`, `README%0A.md`} {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.sess+"/workspace-file?path="+name, nil)
		req.Header.Set("Authorization", "Bearer "+owner)
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(rec, req)
		if rec.Code != 403 || len(dc.send) != 0 {
			t.Fatalf("unsafe path %q status=%d queued=%d", name, rec.Code, len(dc.send))
		}
	}
}

func TestWorkspaceBrokerLimitsDisconnectAndReplacement(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 2)
	for range 2 {
		go func() { _, err := h.requestWorkspaceFile(ctx, dc, "session", "README.md"); done <- err }()
		select {
		case <-dc.send:
		case <-time.After(15 * time.Second):
			t.Fatal("request did not queue")
		}
	}
	if _, err := h.requestWorkspaceFile(ctx, dc, "session", "README.md"); err == nil {
		t.Fatal("per-daemon pending limit ignored")
	}
	h.abandonWorkspaceFileRequests(dc)
	for range 2 {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("disconnect returned success")
			}
		case <-time.After(15 * time.Second):
			t.Fatal("disconnect did not release waiter")
		}
	}
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	go func() { _, err := h.requestWorkspaceFile(ctx2, dc, "session", "README.md"); done <- err }()
	var req protocol.ReadWorkspaceFile
	select {
	case raw := <-dc.send:
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("request did not queue")
	}
	replacement := &DaemonConn{ID: dc.ID, send: make(chan []byte, 8)}
	h.Register(replacement)
	reply := protocol.WorkspaceFileResult{RequestID: req.RequestID, SessionID: "session", OK: true}
	h.deliverWorkspaceFileResult(dc, reply)
	h.deliverWorkspaceFileResult(replacement, reply)
	select {
	case err := <-done:
		t.Fatalf("replaced socket answered predecessor request: %v", err)
	default:
	}
	cancel2()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("replacement accepted")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("canceled waiter leaked")
	}
}

type blockedWorkspaceWriter struct {
	http.ResponseWriter
	entered chan struct{}
	gate    <-chan struct{}
}

func (w *blockedWorkspaceWriter) Write(p []byte) (int, error) {
	close(w.entered)
	// Only the test can release this barrier. A watchdog must not silently
	// finish the response and release its lease before the assertion runs.
	<-w.gate
	return w.ResponseWriter.Write(p)
}

func TestWorkspaceReadLeaseHeldUntilHTTPResponseCompletes(t *testing.T) {
	fx := newTopicFx(t)
	fx.api.RegisterWorkspaceFileRoutes(fx.mux)
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 8)}
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	gate := make(chan struct{})
	var workers sync.WaitGroup
	t.Cleanup(func() {
		closeDone := make(chan struct{})
		go func() { workers.Wait(); close(closeDone) }()
		select {
		case <-closeDone:
		case <-time.After(15 * time.Second):
			t.Error("HTTP workers did not stop")
		}
	})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	token := fx.token(topicOwner, "human")
	done := make(chan struct{}, 2)
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.sess+"/workspace-file?path=README.md", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		writer := &blockedWorkspaceWriter{ResponseWriter: httptest.NewRecorder(), entered: make(chan struct{}), gate: gate}
		workers.Add(1)
		go func() { defer workers.Done(); defer func() { done <- struct{}{} }(); fx.mux.ServeHTTP(writer, req) }()
		var q protocol.ReadWorkspaceFile
		select {
		case raw := <-dc.send:
			if err := json.Unmarshal(raw, &q); err != nil {
				t.Fatal(err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("request did not queue")
		}
		fx.api.hub.deliverWorkspaceFileResult(dc, protocol.WorkspaceFileResult{RequestID: q.RequestID, SessionID: q.SessionID, OK: true, Path: "README.md", Size: 2, ContentB64: "aGk=", Mtime: time.Now().UTC().Format(time.RFC3339Nano)})
		select {
		case <-writer.entered:
		case <-time.After(15 * time.Second):
			t.Fatal("response write did not begin")
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.sess+"/workspace-file?path=README.md", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	if rec.Code != 409 || len(dc.send) != 0 {
		t.Fatalf("completed RPC released active response lease: status=%d queued=%d", rec.Code, len(dc.send))
	}
	release()
	for range 2 {
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("response write did not finish")
		}
	}
	acquire, ok := fx.api.hub.acquireWorkspaceFileRead(dc)
	if !ok {
		t.Fatal("response lease leaked")
	}
	acquire()
}

func TestWorkspaceReadLeaseGlobalAndReconnectLimits(t *testing.T) {
	h := NewHub()
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for _, id := range []string{"first", "first"} {
		release, ok := h.acquireWorkspaceFileRead(&DaemonConn{ID: id})
		if !ok {
			t.Fatal("lease refused below cap")
		}
		releases = append(releases, release)
	}
	if release, ok := h.acquireWorkspaceFileRead(&DaemonConn{ID: "fourth"}); ok {
		release()
		t.Fatal("global active response cap ignored")
	}
	if release, ok := h.acquireWorkspaceFileRead(&DaemonConn{ID: "first"}); ok {
		release()
		t.Fatal("reconnect reset active response cap")
	}
	releases[len(releases)-1]()
	releases = releases[:len(releases)-1]
	release, ok := h.acquireWorkspaceFileRead(&DaemonConn{ID: "first"})
	if !ok {
		t.Fatal("completed response did not release capacity")
	}
	releases = append(releases, release)
}

func TestWorkspaceFileTimeoutReturnsJSONOverRealHTTP(t *testing.T) {
	fx := newTopicFx(t)
	fx.api.RegisterWorkspaceFileRoutes(fx.mux)
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 8)}
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Millisecond)
		defer cancel()
		fx.mux.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer srv.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/sessions/"+fx.sess+"/workspace-file?path=README.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	client := srv.Client()
	client.Timeout = 2 * time.Second
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("expired daemon wait lost JSON response: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusConflict || !strings.Contains(string(body), `"error_id"`) {
		t.Fatalf("timeout response %d %q %v", res.StatusCode, body, err)
	}
}
