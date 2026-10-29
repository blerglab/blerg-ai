//go:build linux || darwin

package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Expand the natural gap between the inner handler returning and net/http's
// automatic buffered flush. This uses an actual TCP server/ResponseWriter.
func TestReviewBR1ExplorerFlushesBeforeAuthorityRelease(t *testing.T) {
	f, dc, reader := p6ExplorerDeliveryFixture(t)
	innerReturned := make(chan struct{})
	release := make(chan struct{})
	released := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.mux.ServeHTTP(w, r); close(innerReturned); <-release }))
	defer func() {
		if !released {
			close(release)
		}
		server.CloseClientConnections()
		server.Close()
	}()
	conn, e := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	token := f.token(topicOwner, "human", "account.manage")
	_, e = fmt.Fprintf(conn, "GET /api/sessions/%s/workspace?op=list HTTP/1.1\r\nHost: example.test\r\nAuthorization: Bearer %s\r\nConnection: close\r\n\r\n", f.sess, token)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case raw := <-dc.send:
		var q protocol.WorkspaceExplorerRequest
		if e := json.Unmarshal(raw, &q); e != nil {
			t.Fatal(e)
		}
		f.api.hub.deliverWorkspaceExplorerResult(dc, reader.Explore(t.Context(), q))
	case <-time.After(3 * time.Second):
		t.Fatal("no runtime dispatch")
	}
	select {
	case <-innerReturned:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not finish")
	}
	// Assignment can now commit. The write must already have left net/http's buffer.
	if e := db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error { _, e := db.AssignDesktop(ctx, f.pool, dc.ID, topicOther, 1); return e }); e != nil {
		t.Fatal(e)
	}
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	response, e := http.ReadResponse(bufio.NewReader(conn), nil)
	if e != nil {
		close(release)
		released = true
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		response, e = http.ReadResponse(bufio.NewReader(conn), nil)
		if e != nil {
			t.Fatal("response never delivered", e)
		}
		body, e := io.ReadAll(response.Body)
		response.Body.Close()
		if e != nil || response.StatusCode != 200 || !json.Valid(body) || !strings.Contains(string(body), "private-owner-file") {
			t.Fatalf("bad fixture: %d %s %v", response.StatusCode, body, e)
		}
		t.Fatalf("EXPLORER_FLUSH: private file metadata first reached TCP after reassignment committed (HTTP %d, %d bytes)", response.StatusCode, len(body))
	}
	body, e := bufio.NewReader(response.Body).ReadBytes('\n')
	response.Body.Close()
	if e != nil || response.StatusCode != 200 || !json.Valid(body) || !strings.Contains(string(body), "private-owner-file") {
		t.Fatalf("expected actual explorer response: status=%d body=%s err=%v", response.StatusCode, body, e)
	}
}

func reviewBR1Root(t *testing.T, path string) protocol.WorkspaceRoot {
	t.Helper()
	st, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	v := st.Sys().(*syscall.Stat_t)
	device := uint64(v.Dev) //nolint:unconvert // Darwin st_dev is signed; Linux already uses uint64.
	return protocol.WorkspaceRoot{Device: device, Inode: v.Ino}
}

func TestReviewBR1ResultFlushesBeforeAuthorityRelease(t *testing.T) {
	f := newPrivFixture(t)
	sid := f.pubSession
	if _, e := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='docker' WHERE id=$1`, sid); e != nil {
		t.Fatal(e)
	}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "sandbox", OwnerID: privAcctA, LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "private-member", Path: "one", SourceIdentity: "private-source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	if e := db.InsertSessionWorkspace(t.Context(), f.pool, sid, m); e != nil {
		t.Fatal(e)
	}
	outcomes := []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: "launch", MemberID: "one"}, Pending: true, PushError: "private-rejection", RecoveryLocation: "/private-recovery"}}
	if e := db.SetWorkspaceResults(t.Context(), f.pool, sid, "launch", outcomes); e != nil {
		t.Fatal(e)
	}
	returned := make(chan struct{})
	release := make(chan struct{})
	released := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", sid)
		f.api.HandleRunnerResult(w, r)
		close(returned)
		<-release
	}))
	defer func() {
		if !released {
			close(release)
		}
		server.CloseClientConnections()
		server.Close()
	}()
	conn, e := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	_, e = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: example.test\r\nAuthorization: Bearer %s\r\nConnection: close\r\n\r\n", f.tok(t, "human", privAcctA, "session.start"))
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not finish")
	}
	if e := db.WithDesktopFence(t.Context(), f.pool, f.dc.ID, func(ctx context.Context) error {
		_, e := db.AssignDesktop(ctx, f.pool, f.dc.ID, privAcctB, 1)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	response, e := http.ReadResponse(bufio.NewReader(conn), nil)
	if e != nil {
		close(release)
		released = true
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		response, e = http.ReadResponse(bufio.NewReader(conn), nil)
		if e != nil {
			t.Fatal("response never delivered", e)
		}
		body, e := io.ReadAll(response.Body)
		response.Body.Close()
		if e != nil || response.StatusCode != 200 || !strings.Contains(string(body), "private-recovery") {
			t.Fatalf("bad fixture: %d %s %v", response.StatusCode, body, e)
		}
		t.Fatalf("RESULT_FLUSH: private recovery metadata first reached TCP after reassignment committed (HTTP %d, %d bytes)", response.StatusCode, len(body))
	}
	body, e := bufio.NewReader(response.Body).ReadBytes('\n')
	response.Body.Close()
	if e != nil || response.StatusCode != http.StatusOK || !json.Valid(body) || !strings.Contains(string(body), "private-recovery") {
		t.Fatal("expected actual member result", e)
	}
}

func p6ExplorerDeliveryFixture(t *testing.T) (*topicFx, *DaemonConn, *daemon.LocalExplorer) {
	t.Helper()
	f := newTopicFx(t)
	f.api.RegisterWorkspaceFileRoutes(f.mux)
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "flush-desktop")
	if _, e := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2,runtime='daemon' WHERE id=$1`, f.sess, dc.ID); e != nil {
		t.Fatal(e)
	}
	f.api.hub.SetSessionOwner(f.sess, dc.ID)
	root := t.TempDir()
	if e := os.WriteFile(root+"/private-owner-file.txt", []byte("private"), 0600); e != nil {
		t.Fatal(e)
	}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "host", OwnerID: topicOwner, LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{}}
	if e := db.InsertSessionWorkspace(t.Context(), f.pool, f.sess, m); e != nil {
		t.Fatal(e)
	}
	gen, e := db.BeginWorkspaceRuntime(t.Context(), f.pool, f.sess, m.LaunchGeneration, 0)
	if e != nil {
		t.Fatal(e)
	}
	// Empty host folders are valid, and a binding need only agree with the real reader.
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: gen}, ManifestDigest: "digest", Root: protocol.WorkspaceRoot{Inode: 7}}
	// The LocalExplorer requires the real inode/device; get it through its fixture helper.
	b.Root = reviewBR1Root(t, root)
	if e := db.BindWorkspaceRuntime(t.Context(), f.pool, f.sess, b.WorkspaceRuntimeRef, b); e != nil {
		t.Fatal(e)
	}
	dc.AuthorizeWorkspaceRuntime(f.sess, b.WorkspaceRuntimeRef)
	reader, e := daemon.NewLocalExplorer(root, m, b, func() bool { return true })
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = reader.Close() })
	return f, dc, reader
}

// Both routes must use the same complete, bounded delivery primitive. The
// explorer fixture supplies a real reader; it never holds authority during RPC.
func p6HTTPDelivery(t *testing.T, endpoint string) (http.HandlerFunc, *http.Request, *pgxpool.Pool, *DaemonConn) {
	t.Helper()
	if endpoint == "result" {
		f := newPrivFixture(t)
		r := httptest.NewRequest("GET", "/", nil)
		r.SetPathValue("id", f.pubSession)
		r.Header.Set("Authorization", "Bearer "+f.tok(t, "human", privAcctA, "session.start"))
		return f.api.HandleRunnerResult, r, f.pool, f.dc
	}
	f, dc, reader := p6ExplorerDeliveryFixture(t)
	r := httptest.NewRequest("GET", "/api/sessions/"+f.sess+"/workspace?op=list", nil)
	r.Header.Set("Authorization", "Bearer "+f.token(topicOwner, "human", "account.manage"))
	handler := func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context() // authBrowser updates the request's context in place.
		done := make(chan struct{})
		go func() { defer close(done); f.mux.ServeHTTP(w, r) }()
		select {
		case raw := <-dc.send:
			var q protocol.WorkspaceExplorerRequest
			if err := json.Unmarshal(raw, &q); err != nil {
				t.Error(err)
			} else {
				f.api.hub.deliverWorkspaceExplorerResult(dc, reader.Explore(ctx, q))
			}
		case <-done:
			return
		case <-ctx.Done():
		}
		<-done
	}
	return handler, r, f.pool, dc
}

type p6DeliveryWriter struct {
	header          http.Header
	body            bytes.Buffer
	status          []int
	deadline        time.Time
	writes, flushes int
	mode            string
	conn            net.Conn
	probe           func()
}

func (w *p6DeliveryWriter) Header() http.Header    { return w.header }
func (w *p6DeliveryWriter) WriteHeader(status int) { w.probe(); w.status = append(w.status, status) }
func (w *p6DeliveryWriter) SetWriteDeadline(d time.Time) error {
	w.deadline = d
	if w.mode == "deadline-error" {
		return http.ErrNotSupported
	}
	if w.conn != nil {
		return w.conn.SetWriteDeadline(d)
	}
	return nil
}
func (w *p6DeliveryWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.mode == "write-error" {
		return 0, io.ErrClosedPipe
	}
	if w.mode == "short-write" {
		return len(p) - 1, nil
	}
	if w.mode == "stall-write" {
		return w.conn.Write(p)
	}
	return w.body.Write(p)
}
func (w *p6DeliveryWriter) FlushError() error {
	w.probe()
	w.flushes++
	if w.mode == "flush-error" {
		return io.ErrClosedPipe
	}
	if w.mode == "stall-flush" {
		_, err := w.conn.Write(w.body.Bytes())
		return err
	}
	return nil
}

// This writer supports deadlines, but cannot flush. Neither it nor a writer
// lacking deadlines may receive source bytes before capability refusal.
type p6DeadlineOnlyWriter struct{ inventoryResponse }

func (*p6DeadlineOnlyWriter) SetWriteDeadline(time.Time) error { return nil }

func TestP6PrivateHTTPDeliveryFailures(t *testing.T) {
	for _, endpoint := range []string{"explorer", "result"} {
		for _, mode := range []string{"ok", "no-deadline", "no-flush", "deadline-error", "write-error", "short-write", "flush-error", "stall-write", "stall-flush"} {
			t.Run(endpoint+"/"+mode, func(t *testing.T) {
				handler, r, pool, dc := p6HTTPDelivery(t, endpoint)
				if mode == "no-deadline" {
					w := httptest.NewRecorder()
					handler(w, r)
					if w.Code != 409 || strings.Contains(w.Body.String(), "private-owner") || strings.Contains(w.Body.String(), "workspace_results") {
						t.Fatal("private response reached writer without a deadline", w.Body)
					}
					return
				}
				if mode == "no-flush" {
					w := &p6DeadlineOnlyWriter{inventoryResponse{header: make(http.Header)}}
					handler(w, r)
					if w.status != 409 || strings.Contains(w.String(), "private-owner") || strings.Contains(w.String(), "workspace_results") {
						t.Fatal("private response reached writer without flush support", w.String())
					}
					return
				}
				w := &p6DeliveryWriter{header: make(http.Header), mode: mode}
				w.probe = func() {
					if mode == "deadline-error" {
						return
					} // only a generic error after refusal
					tx, err := pool.Begin(t.Context())
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = tx.Rollback(t.Context()) }()
					var acquired bool
					if err := tx.QueryRow(t.Context(), `SELECT pg_try_advisory_xact_lock(hashtextextended($1::uuid::text,876434))`, dc.ID).Scan(&acquired); err != nil {
						t.Error(err)
					} else if acquired {
						t.Error("response escaped desktop authority")
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 700*time.Millisecond)
				defer cancel()
				if strings.HasPrefix(mode, "stall-") {
					socket, peer := net.Pipe()
					defer socket.Close()
					defer peer.Close()
					w.conn = socket // peer never reads; real socket deadline must release it
				}
				done := make(chan struct{})
				go func() { defer close(done); handler(w, r.WithContext(ctx)) }()
				select {
				case <-done:
				case <-time.After(1500 * time.Millisecond):
					if w.conn != nil {
						_ = w.conn.Close()
					}
					<-done
					t.Fatal("stalled delivery retained authority beyond its deadline")
				}
				until, _ := ctx.Deadline()
				if mode != "deadline-error" && (w.deadline.IsZero() || w.deadline.After(until)) {
					t.Fatalf("delivery extended request deadline: %v > %v", w.deadline, until)
				}
				wantStatus := 200
				if mode == "deadline-error" {
					wantStatus = 409
				}
				if len(w.status) != 1 || w.status[0] != wantStatus || w.writes != 1 {
					t.Fatalf("response retried or refused incorrectly: status=%v writes=%d body=%s", w.status, w.writes, w.body.String())
				}
				wantFlush := 1
				if mode == "deadline-error" || mode == "write-error" || mode == "short-write" || mode == "stall-write" {
					wantFlush = 0
				}
				if w.flushes != wantFlush {
					t.Fatalf("flushes=%d, want %d", w.flushes, wantFlush)
				}
				if mode == "ok" && !json.Valid(w.body.Bytes()) {
					t.Fatal("incomplete JSON response")
				}
				// A fresh exclusive transaction can acquire authority after either
				// write or flush failure, including a client that never reads.
				probeCtx, stop := context.WithTimeout(t.Context(), time.Second)
				defer stop()
				if err := db.WithDesktopFence(probeCtx, pool, dc.ID, func(context.Context) error { return nil }); err != nil {
					t.Fatal("delivery retained authority", err)
				}
			})
		}
	}
}

func TestP6FencedResponseCanceledAndWrappedWriter(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w := &reviewDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	started, err := writeFencedResponse(ctx, w, 200, []byte("private"))
	if started || !errors.Is(err, context.Canceled) || w.Body.Len() != 0 {
		t.Fatal("canceled response sent bytes", started, err)
	}
	wrapped := p6UnwrapWriter{w}
	started, err = writeFencedResponse(t.Context(), wrapped, 200, []byte("complete"))
	if !started || err != nil || !w.Flushed || w.Body.String() != "complete" {
		t.Fatal("wrapped supported writer refused", started, err)
	}
}

type p6UnwrapWriter struct{ http.ResponseWriter }

func (w p6UnwrapWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func TestP6ResultRechecksRecipientAndConnectionAfterBuilding(t *testing.T) {
	for _, change := range []string{"recipient", "connection", "assignment"} {
		t.Run(change, func(t *testing.T) {
			f := newPrivFixture(t)
			// Pause a real builder SELECT after it captured the recipient and
			// connection, before final authority. No production test hook is used.
			tx, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(t.Context()) }()
			if _, err := tx.Exec(t.Context(), `LOCK TABLE agent_events IN ACCESS EXCLUSIVE MODE`); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/", nil)
			r.SetPathValue("id", f.pubSession)
			r.Header.Set("Authorization", "Bearer "+f.tok(t, "human", privAcctA, "session.start"))
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			w := &reviewDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
			done := make(chan struct{})
			go func() { defer close(done); f.api.HandleRunnerResult(w, r.WithContext(ctx)) }()
			defer func() { _ = tx.Rollback(t.Context()); <-done }()
			for {
				var waiting bool
				if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='agent_events'::regclass AND NOT granted)`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("builder did not reach event read")
				case <-time.After(5 * time.Millisecond):
				}
			}
			switch change {
			case "recipient":
				_, err = f.pool.Exec(ctx, `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, f.pubSession, privAcctB)
			case "connection":
				f.hub.Register(&DaemonConn{ID: f.dc.ID, DesktopBinding: f.dc.DesktopBinding, DesktopGeneration: f.dc.DesktopGeneration, send: make(chan []byte, 8)})
			case "assignment":
				err = db.WithDesktopFence(ctx, f.pool, f.dc.ID, func(fenced context.Context) error {
					_, err := db.AssignDesktop(fenced, f.pool, f.dc.ID, privAcctB, 1)
					return err
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("result handler did not finish")
			}
			if w.Code != 409 || strings.Contains(w.Body.String(), `"repo"`) || strings.Contains(w.Body.String(), `"workspace_results"`) {
				t.Fatalf("stale result delivered after %s: %d %s", change, w.Code, w.Body)
			}
		})
	}
}
