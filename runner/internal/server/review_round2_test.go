package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestRereviewNewOwnerMustNotReceiveEarlierOwnerSources(t *testing.T) {
	for _, unknownOwner := range []bool{false, true} {
		for _, endpoint := range []string{"result", "status", "initial_state", "queued_snapshot", "polling_sse", "live_sse"} {
			t.Run(fmt.Sprintf("%s/unknown_owner=%v", endpoint, unknownOwner), func(t *testing.T) {
				f := newPrivFixture(t)
				if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='docker',repo='private-old-repo',project_path='/private-old-owner',error_reason='private-old-failure',git_url='https://github.com/private-old-owner/private-repo.git' WHERE id=$1`, f.pubSession); err != nil {
					t.Fatal(err)
				}
				if unknownOwner {
					if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=NULL WHERE id=$1`, f.pubSession); err != nil {
						t.Fatal(err)
					}
				}
				// Include a payload collected under the previous scope to exercise
				// the final queued-message filter independently of the collector.
				queuedBefore, _ := json.Marshal(map[string]any{"type": "initial_state", "sessions": []map[string]any{{"id": f.pubSession, "daemon_id": f.dc.ID, "repo": "private-old-repo", "project_path": "/private-old-owner", "git_url": "https://github.com/private-old-owner/private-repo.git", "branch": "private-old-branch"}}})
				token := f.tok(t, "human", privAcctB, "session.start")
				if !unknownOwner {
					owner := privDo(f.api.HandleRunnerResult, "GET", "/", f.pubSession, f.tok(t, "human", privAcctA, "session.start"), "")
					if owner.Code != 200 || !strings.Contains(owner.Body.String(), "private-old-repo") {
						t.Fatal("original owner lost its own sources before assignment")
					}
				}
				before := privDo(f.api.HandleRunnerResult, "GET", "/", f.pubSession, token, "")
				if before.Code != 200 || strings.Contains(before.Body.String(), "private-old") {
					t.Fatal("foreign baseline wrong")
				}
				if err := db.WithDesktopFence(t.Context(), f.pool, f.dc.ID, func(ctx context.Context) error {
					_, err := db.AssignDesktop(ctx, f.pool, f.dc.ID, privAcctB, 1)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				f.api.desktopOwnershipChanged(f.dc.ID, privAcctA, privAcctB, 2)
				var raw string
				switch endpoint {
				case "result", "status":
					handler := f.api.HandleRunnerResult
					if endpoint == "status" {
						handler = f.api.HandleRunnerStatus
					}
					w := privDo(handler, "GET", "/", f.pubSession, token, "")
					if w.Code != 200 {
						t.Fatalf("HTTP %d", w.Code)
					}
					raw = w.Body.String()
				case "polling_sse", "live_sse":
					now := time.Now()
					if err := db.EndSessionStatus(t.Context(), f.pool, f.pubSession, "stopped", &now, systemEnd(db.EndReasonProcessExited)); err != nil {
						t.Fatal(err)
					}
					handler := f.api.HandleRunnerEventStream
					if endpoint == "live_sse" {
						handler = f.api.HandleRunnerEventsLive
					}
					w := privDo(handler, "GET", "/", f.pubSession, token, "")
					if w.Code != 200 || !strings.Contains(w.Body.String(), "event: end") {
						t.Fatalf("stream did not deliver terminal result: %d %s", w.Code, w.Body.String())
					}
					raw = w.Body.String()
				case "queued_snapshot":
					if err := f.hub.deliverDesktopPayload(t.Context(), privAcctB, queuedBefore, func(sent []byte) error { raw = string(sent); return nil }); err != nil {
						t.Fatal(err)
					}
				default:
					bc := &BrowserConn{AccountID: privAcctB, send: make(chan []byte, 256), ctx: t.Context()}
					sendInitialState(t.Context(), bc, f.hub, f.pool)
					queued := <-bc.send
					if !strings.Contains(string(queued), "public question") {
						t.Fatal("shared conversation prose was removed")
					}
					if err := f.hub.deliverDesktopPayload(t.Context(), privAcctB, queued, func(sent []byte) error { raw = string(sent); return nil }); err != nil {
						t.Fatal(err)
					}
				}
				var body any
				if endpoint != "polling_sse" && endpoint != "live_sse" && json.Unmarshal([]byte(raw), &body) != nil {
					t.Fatal("bad response")
				}
				if strings.Contains(raw, "private-old") || strings.Contains(raw, f.dc.ID) || strings.Contains(raw, `"branch":"wip/`) {
					t.Fatalf("new desktop owner received earlier owner's source metadata: %s", raw)
				}
			})
		}
	}
}

// net.Pipe's peer never reads: Write blocks until the installed socket deadline.
// This exercises a real blocked write rather than trusting a recorded deadline.
type round2StalledWriter struct {
	header   http.Header
	conn     net.Conn
	entered  chan struct{}
	once     sync.Once
	deadline time.Time
	started  time.Time
	writes   int
}

func (w *round2StalledWriter) FlushError() error   { return nil }
func (w *round2StalledWriter) Header() http.Header { return w.header }
func (w *round2StalledWriter) WriteHeader(int)     {}
func (w *round2StalledWriter) SetWriteDeadline(d time.Time) error {
	w.deadline = d
	return w.conn.SetWriteDeadline(d)
}
func (w *round2StalledWriter) Write(p []byte) (int, error) {
	w.writes++
	w.once.Do(func() { w.started = time.Now(); close(w.entered) })
	return w.conn.Write(p)
}

func TestRound2DiscoveryStalledWriteReleasesAssignmentFence(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(fmt.Sprintf("preview=%v", preview), func(t *testing.T) {
			f := newPrivFixture(t)
			dc := workspaceOwnedDesktop(t, f.hub, f.pool, privAcctA, "desktop")
			token := f.tok(t, "human", privAcctA, "session.start")
			selection := protocol.WorkspaceSelectionRequest{DaemonID: dc.ID, OwnershipRevision: 1, ConnectionGeneration: dc.DesktopGeneration, RootIdentity: "root", Members: []protocol.SelectionRef{{Path: "repo", SourceIdentity: "source"}}}
			r := httptest.NewRequest("GET", "/api/workspace/discovery?daemon_id="+dc.ID+"&path=.", nil)
			handler := f.api.HandleWorkspaceDiscovery
			if preview {
				raw, _ := json.Marshal(map[string]any{"runtime": "host", "selection": selection})
				r = httptest.NewRequest("POST", "/api/workspace/preview", bytes.NewReader(raw))
				handler = f.api.HandleWorkspacePreview
			}
			r.Header.Set("Authorization", "Bearer "+token)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
			socket, peer := net.Pipe()
			defer socket.Close()
			defer peer.Close()
			w := &round2StalledWriter{header: make(http.Header), conn: socket, entered: make(chan struct{})}
			done := make(chan struct{})
			go func() { handler(w, r); close(done) }()
			var req protocol.DiscoverWorkspace
			select {
			case raw := <-dc.send:
				if err := json.Unmarshal(raw, &req); err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no discovery RPC")
			}
			result := protocol.WorkspaceDiscoveryResult{Type: "workspace_discovery_result", RequestID: req.RequestID, OK: true}
			if preview {
				result.Resolved = &protocol.DiscoveryResolved{RootIdentity: "root", SelectionIdentity: "selection", Members: []protocol.DiscoverySource{{ID: "repo", Name: "repo", Path: "repo", SourceIdentity: "source", CommonDirectoryIdentity: "common"}}}
			} else {
				result.Page = &protocol.DiscoveryPage{RootIdentity: "root", Root: protocol.DiscoveryEntry{Path: "."}}
			}
			f.hub.deliverWorkspaceDiscovery(dc, result)
			select {
			case <-w.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("handler never wrote")
			}
			probe, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var acquired bool
			err = probe.QueryRow(t.Context(), `SELECT pg_try_advisory_xact_lock(hashtextextended($1::uuid::text,876434))`, dc.ID).Scan(&acquired)
			_ = probe.Rollback(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if acquired {
				t.Fatal("final write was not fenced")
			}
			assigned := make(chan error, 1)
			go func() {
				assigned <- db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error {
					_, err := db.AssignDesktop(ctx, f.pool, dc.ID, privAcctB, 1)
					return err
				})
			}()
			select {
			case <-done:
			case <-time.After(1500 * time.Millisecond):
				_ = peer.Close()
				<-done
				t.Fatal("stalled response retained desktop fence without a short socket deadline")
			}
			if w.deadline.IsZero() || w.deadline.After(w.started.Add(time.Second)) {
				t.Fatalf("unbounded write deadline: %v", w.deadline)
			}
			if until, _ := ctx.Deadline(); w.deadline.After(until) {
				t.Fatal("write extended request deadline")
			}
			if w.writes != 1 {
				t.Fatalf("write failure retried %d times", w.writes)
			}
			select {
			case err := <-assigned:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("assignment did not acquire released fence")
			}
		})
	}
}

func TestRound2DiscoveryWriteDeadlineIsCappedAndRequired(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	w := &reviewDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	started, err := writeDiscoverySnapshot(ctx, w, map[string]bool{"ok": true})
	if !started || err != nil {
		t.Fatalf("write failed: %v", err)
	}
	until, _ := ctx.Deadline()
	if w.deadline.IsZero() || w.deadline.After(until) {
		t.Fatalf("write extends fence context: %v > %v", w.deadline, until)
	}
	unsupported := httptest.NewRecorder()
	started, err = writeDiscoverySnapshot(t.Context(), unsupported, map[string]string{"source": "secret"})
	if started || err == nil || unsupported.Body.Len() != 0 {
		t.Fatal("source write without a socket deadline")
	}
}
