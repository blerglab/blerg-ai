package server

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A slow but progressing consumer throttles the runtime through server credit:
// the daemon (simulated here, honoring its window) sends only what the server
// has returned, so the queue never overflows and every byte arrives in order.
func TestServerCreditPacesASlowProgressingConsumer(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8), shellControl: make(chan []byte, 32)}
	hub.Register(dc)
	hub.SetSessionOwner("session", dc.ID)
	remote := newRemoteShell(NewAPI(hub, nil, "", nil, ""))
	entry, out := newShellOutput(dc, "session", "shell")
	hub.shellRPC.outputs = map[string]shellOutput{"shell": entry}
	t.Cleanup(entry.pipe.Abort)
	entry.credit.window.Store(ownershell.ServerOutputWindow)
	g := ownershell.SignedGrant{}
	g.Grant.ShellGeneration = "shell"

	const total = 4 << 20 // 4 MiB: sixteen windows
	stream := make([]byte, total)
	for i := range stream {
		stream[i] = byte('a' + i%23)
	}
	producerDone := make(chan int64, 1)
	go func() { // the daemon: never more than its window outstanding
		available, sent, maxQueued := int64(ownershell.ServerOutputWindow), 0, int64(0)
		for sent < total {
			for available <= 0 {
				var credit shellruntime.RPC
				if json.Unmarshal(<-dc.shellControl, &credit) != nil || credit.Op != "credit" || credit.Shell != "shell" {
					t.Error("unexpected control frame")
					return
				}
				available += int64(credit.Seq)
			}
			n := min(ownershell.MaxFrame, total-sent, int(available))
			hub.deliverShell(dc, shellruntime.RPC{Version: 1, Type: "owner_shell_output", SessionID: "session", Shell: "shell", Data: stream[sent : sent+n]})
			sent += n
			available -= int64(n)
			maxQueued = max(maxQueued, int64(entry.pipe.Len()))
		}
		producerDone <- maxQueued
	}()
	got := make([]byte, 0, total)
	for len(got) < total { // the manager: slower than the daemon, but always progressing
		select {
		case data, ok := <-out:
			if !ok {
				t.Fatalf("stream ended after %d of %d bytes", len(got), total)
			}
			got = append(got, data...)
			time.Sleep(time.Millisecond)
			remote.Consumed(g, len(data))
		case <-time.After(10 * time.Second):
			t.Fatalf("output stalled after %d of %d bytes", len(got), total)
		}
	}
	if string(got) != string(stream) {
		t.Fatal("output changed or reordered")
	}
	if maxQueued := <-producerDone; maxQueued > ownershell.ServerOutputWindow {
		t.Fatalf("server queued %d bytes, over the %d byte window", maxQueued, ownershell.ServerOutputWindow)
	}
	hub.shellRPC.mu.Lock()
	_, kept := hub.shellRPC.outputs["shell"]
	hub.shellRPC.mu.Unlock()
	if !kept {
		t.Fatal("a progressing consumer lost its stream")
	}
}

// Every path that removes an output entry releases its pump (even one holding
// a frame for a manager that stopped reading), its credit loop and its bytes.
func TestOutputEntryRemovalReleasesPumpAndCredit(t *testing.T) {
	for _, path := range []string{"disconnect", "overflow", "close"} {
		t.Run(path, func(t *testing.T) {
			hub := NewHub()
			dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8), shellControl: make(chan []byte, 32)}
			hub.Register(dc)
			hub.SetSessionOwner("session", dc.ID)
			remote := newRemoteShell(NewAPI(hub, nil, "", nil, ""))
			baseline := runtime.NumGoroutine()
			entry, out := newShellOutput(dc, "session", "shell")
			hub.shellRPC.outputs = map[string]shellOutput{"shell": entry}
			frame := shellruntime.RPC{Version: 1, Type: "owner_shell_output", SessionID: "session", Shell: "shell", Data: []byte("private-terminal-bytes")}
			hub.deliverShell(dc, frame)
			// The pump took the frame and now waits on a manager that never reads.
			waitUntil(t, "pump did not take the frame", func() bool { return entry.pipe.Len() == 0 })
			hub.deliverShell(dc, frame)
			switch path {
			case "disconnect":
				hub.abandonShellRequests(dc)
			case "overflow":
				big := frame
				big.Data = make([]byte, ownershell.MaxFrame)
				for range remoteOutputQueue/ownershell.MaxFrame + 1 {
					hub.deliverShell(dc, big)
				}
			case "close":
				g := ownershell.SignedGrant{}
				g.Grant.ShellGeneration = "shell"
				remote.live["shell"] = shellTestBinding()
				remote.peers["shell"] = dc
				ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
				_ = remote.Close(ctx, g) // the daemon never answers; the pump is released first
				cancel()
			}
			select {
			case _, ok := <-out:
				for ok {
					_, ok = <-out
				}
			case <-time.After(3 * time.Second):
				t.Fatal("pump stranded")
			}
			if entry.pipe.Len() != 0 {
				t.Fatal("queued output retained after removal")
			}
			waitUntil(t, "pump or credit goroutine leaked", func() bool { return runtime.NumGoroutine() <= baseline })
		})
	}
}
