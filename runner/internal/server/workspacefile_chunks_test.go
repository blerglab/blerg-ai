package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type chunkedRead struct {
	t       *testing.T
	h       *Hub
	dc      *DaemonConn
	request protocol.ReadWorkspaceFile
	done    chan protocol.WorkspaceFileResult
}

func startChunkedRead(t *testing.T) chunkedRead {
	t.Helper()
	h := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	dc.SetSandboxClones(true)
	dc.SetWorkBranches(true)
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	done := make(chan protocol.WorkspaceFileResult, 1)
	go func() {
		res, err := h.requestWorkspaceFile(ctx, dc, "session", "image.png")
		if err != nil {
			res = protocol.WorkspaceFileResult{ErrorCode: "request failed"}
		}
		done <- res
	}()
	var request protocol.ReadWorkspaceFile
	if err := json.Unmarshal(<-dc.send, &request); err != nil {
		t.Fatal(err)
	}
	return chunkedRead{t: t, h: h, dc: dc, request: request, done: done}
}

func (r chunkedRead) chunk(seq int, data string) protocol.WorkspaceFileChunk {
	return protocol.WorkspaceFileChunk{Type: "workspace_file_chunk", RequestID: r.request.RequestID, SessionID: "session", Seq: seq, Data: data}
}

func (r chunkedRead) closing(chunks int) protocol.WorkspaceFileResult {
	return protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: r.request.RequestID, SessionID: "session", OK: true, Path: "image.png", Size: 9, Mtime: time.Now().UTC().Format(time.RFC3339Nano), Chunks: chunks}
}

func (r chunkedRead) result() protocol.WorkspaceFileResult {
	r.t.Helper()
	select {
	case res := <-r.done:
		return res
	case <-time.After(5 * time.Second):
		r.t.Fatal("no result")
		return protocol.WorkspaceFileResult{}
	}
}

func TestWorkspaceChunksRebuildTheReplyContent(t *testing.T) {
	r := startChunkedRead(t)
	content := base64.StdEncoding.EncodeToString([]byte("nine byte"))
	r.h.collectWorkspaceFileChunk(r.dc, r.chunk(0, content[:5]))
	r.h.collectWorkspaceFileChunk(r.dc, r.chunk(1, content[5:]))
	r.h.deliverWorkspaceFileResult(r.dc, r.closing(2))
	if res := r.result(); !res.OK || res.ContentB64 != content {
		t.Fatalf("result: ok=%v content=%q code=%q", res.OK, res.ContentB64, res.ErrorCode)
	}
}

// Anything but the exact chunks in order fails the read: no partial or
// reordered content reaches the person.
func TestWorkspaceChunksRefuseBrokenSequences(t *testing.T) {
	for _, tt := range []struct {
		name  string
		send  func(chunkedRead)
		count int
	}{
		{"gap", func(r chunkedRead) { r.h.collectWorkspaceFileChunk(r.dc, r.chunk(1, "QUJD")) }, 2},
		{"repeat", func(r chunkedRead) {
			r.h.collectWorkspaceFileChunk(r.dc, r.chunk(0, "QUJD"))
			r.h.collectWorkspaceFileChunk(r.dc, r.chunk(0, "QUJD"))
		}, 2},
		{"short count", func(r chunkedRead) { r.h.collectWorkspaceFileChunk(r.dc, r.chunk(0, "QUJD")) }, 2},
		{"long count", func(r chunkedRead) {
			r.h.collectWorkspaceFileChunk(r.dc, r.chunk(0, "QUJD"))
			r.h.collectWorkspaceFileChunk(r.dc, r.chunk(1, "QUJD"))
		}, 1},
		{"over the cap", func(r chunkedRead) {
			huge := strings.Repeat("A", workspaceFileMaxContent)
			r.h.collectWorkspaceFileChunk(r.dc, r.chunk(0, huge))
			r.h.collectWorkspaceFileChunk(r.dc, r.chunk(1, "QUJD"))
		}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := startChunkedRead(t)
			tt.send(r)
			r.h.deliverWorkspaceFileResult(r.dc, r.closing(tt.count))
			if res := r.result(); res.OK || res.ContentB64 != "" {
				t.Fatalf("broken chunks accepted: ok=%v content=%d bytes", res.OK, len(res.ContentB64))
			}
		})
	}
}

// A closing frame that also carries inline content cannot smuggle it past the
// assembled chunks.
func TestWorkspaceChunksRefuseInlineContentAlongside(t *testing.T) {
	r := startChunkedRead(t)
	r.h.collectWorkspaceFileChunk(r.dc, r.chunk(0, "QUJD"))
	closing := r.closing(1)
	closing.ContentB64 = "REVG"
	r.h.deliverWorkspaceFileResult(r.dc, closing)
	if res := r.result(); res.OK {
		t.Fatal("inline content accepted alongside chunks")
	}
}

// Chunks have the same authority as the reply: another connection or session
// cannot add to a read, and the bytes do not outlive it.
func TestWorkspaceChunksBindToTheRequestAndAreReleased(t *testing.T) {
	r := startChunkedRead(t)
	other := &DaemonConn{ID: "other"}
	other.SetSandboxClones(true)
	other.SetWorkBranches(true)
	r.h.collectWorkspaceFileChunk(other, r.chunk(0, "QUJD"))
	foreign := r.chunk(0, "QUJD")
	foreign.SessionID = "foreign"
	r.h.collectWorkspaceFileChunk(r.dc, foreign)
	unknown := r.chunk(0, "QUJD")
	unknown.RequestID = "unknown"
	r.h.collectWorkspaceFileChunk(r.dc, unknown)
	r.h.workspaceFileReqs.mu.Lock()
	held := r.h.workspaceFileReqs.pending[r.request.RequestID].content.parts
	r.h.workspaceFileReqs.mu.Unlock()
	if held != 0 {
		t.Fatalf("forged chunks were kept: %d", held)
	}
	r.h.collectWorkspaceFileChunk(r.dc, r.chunk(0, "bmluZSBi"))
	r.h.collectWorkspaceFileChunk(r.dc, r.chunk(1, "eXRl"))
	r.h.deliverWorkspaceFileResult(r.dc, r.closing(2))
	if res := r.result(); !res.OK || res.ContentB64 != "bmluZSBieXRl" {
		t.Fatalf("result: %+v", res)
	}
	r.h.collectWorkspaceFileChunk(r.dc, r.chunk(2, "QUJD")) // a late chunk finds nothing
	r.h.workspaceFileReqs.mu.Lock()
	defer r.h.workspaceFileReqs.mu.Unlock()
	if len(r.h.workspaceFileReqs.pending) != 0 {
		t.Fatal("request outlived its reply")
	}
}

// The real path: a real daemon client, the real daemon socket and read pump. The
// welcome offers chunks, the daemon sends them and the broker rebuilds the file.
func TestRealDaemonDeliversALargeFileAsChunks(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(hub.ServeDaemon("tok-1234567890", nil))
	defer srv.Close()
	client := daemon.NewWSClient(daemon.Config{ServerURL: "ws" + strings.TrimPrefix(srv.URL, "http"), DaemonToken: "tok-1234567890", DaemonName: "box", DaemonMode: "local", ReposRoot: t.TempDir(), HeartbeatInterval: time.Hour, ReconnectInitial: 20 * time.Millisecond, ReconnectMax: 40 * time.Millisecond, ConnectionFile: filepath.Join(t.TempDir(), "connection.json"), UnconfirmedAfter: time.Minute})
	content := base64.StdEncoding.EncodeToString(make([]byte, 20<<20))
	client.OnMessage("read_workspace_file", func(raw []byte) {
		var request protocol.ReadWorkspaceFile
		if json.Unmarshal(raw, &request) != nil {
			return
		}
		go func() {
			_ = client.SendWorkspaceFileResultBy(protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: request.RequestID, SessionID: request.SessionID, OK: true, Path: "image.png", Size: 20 << 20, ContentB64: content}, time.Now().Add(8*time.Second))
		}()
	})
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() { defer close(stopped); client.Run(ctx, func() []string { return nil }) }()
	defer func() { cancel(); <-stopped }()
	deadline := time.Now().Add(15 * time.Second)
	for len(hub.GetAllDaemons()) == 0 || !client.WorkspaceFileChunks() {
		if time.Now().After(deadline) {
			t.Fatal("the daemon never connected, or the welcome offered no chunks")
		}
		time.Sleep(5 * time.Millisecond)
	}
	dc := hub.GetAllDaemons()[0]
	hub.SetSessionOwner("session", dc.ID)
	done := make(chan protocol.WorkspaceFileResult, 1)
	go func() {
		res, _ := hub.requestWorkspaceFile(ctx, dc, "session", "image.png")
		done <- res
	}()
	sawChunks := false
	for {
		select {
		case res := <-done:
			if !sawChunks {
				t.Error("the reply did not arrive as chunks")
			}
			if !res.OK || res.ContentB64 != content {
				t.Fatalf("result: ok=%v, %d bytes of %d", res.OK, len(res.ContentB64), len(content))
			}
			return
		case <-time.After(100 * time.Microsecond):
			hub.workspaceFileReqs.mu.Lock()
			for _, waiter := range hub.workspaceFileReqs.pending {
				sawChunks = sawChunks || waiter.content.parts > 0
			}
			hub.workspaceFileReqs.mu.Unlock()
		}
	}
}

// Session pods answer through an allow-list of frames, and an oversized chunk
// is refused before it is decoded.
func TestWorkspaceChunksPassThePodFrameAllowList(t *testing.T) {
	fx := newWorkFx(t)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='cluster' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	runtimeRaw, _, err := db.MintRuntimeLaunch(t.Context(), fx.pool, fx.sess, fx.sessTok, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	runtimeToken, err := db.ValidateRuntimeToken(t.Context(), fx.pool, runtimeRaw)
	if err != nil {
		t.Fatal(err)
	}
	dc := &DaemonConn{ID: topicDaemonID, RuntimeTokenID: runtimeToken.ID, BoundSessionID: fx.sess, send: make(chan []byte, 8)}
	dc.SetSandboxClones(true)
	dc.SetWorkBranches(true)
	hub := fx.api.hub
	hub.Register(dc)
	hub.SetSessionOwner(fx.sess, dc.ID)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	done := make(chan protocol.WorkspaceFileResult, 1)
	go func() {
		res, _ := hub.requestWorkspaceFile(ctx, dc, fx.sess, "image.png")
		done <- res
	}()
	var request protocol.ReadWorkspaceFile
	if err := json.Unmarshal(<-dc.send, &request); err != nil {
		t.Fatal(err)
	}
	frame := func(value any, kind string) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if !handleDaemonFrame(ctx, hub, dc, fx.pool, raw, envelope{Type: kind, SessionID: fx.sess}) {
			t.Fatalf("%s ended the link", kind)
		}
	}
	chunk := protocol.WorkspaceFileChunk{Type: "workspace_file_chunk", RequestID: request.RequestID, SessionID: fx.sess, Data: "bmluZSBi"}
	frame(chunk, chunk.Type)
	oversized := chunk
	oversized.Seq, oversized.Data = 1, strings.Repeat("A", workspaceFileMaxChunkFrame)
	frame(oversized, oversized.Type) // refused, so the sequence stays intact
	chunk.Seq, chunk.Data = 1, "eXRl"
	frame(chunk, chunk.Type)
	frame(protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: request.RequestID, SessionID: fx.sess, OK: true, Chunks: 2}, "workspace_file_result")
	select {
	case res := <-done:
		if !res.OK || res.ContentB64 != "bmluZSBieXRl" {
			t.Fatalf("result: %+v", res)
		}
	case <-ctx.Done():
		t.Fatal("pod reply never arrived")
	}
}
