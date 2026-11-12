package server

import (
	"context"
	"testing"
	"time"
)

type statusWaitContext struct {
	context.Context
	entered chan struct{}
}

func (c statusWaitContext) Done() <-chan struct{} {
	select {
	case <-c.entered:
	default:
		close(c.entered)
	}
	return c.Context.Done()
}

func TestWorkspaceStatusWaitsForExplorer(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon"}
	release, ok := h.acquireWorkspaceExplorer(dc, "owner", "session", false)
	if !ok {
		t.Fatal("list refused")
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan func(), 1)
	entered := make(chan struct{})
	go func() {
		next, _ := h.waitWorkspaceStatus(statusWaitContext{ctx, entered}, dc, "owner", "session")
		done <- next
	}()
	// A second explorer still cannot bypass the read limit.
	if next, ok := h.acquireWorkspaceExplorer(dc, "owner", "session", false); ok {
		next()
		t.Fatal("read limit bypassed")
	}
	select {
	case <-entered:
	case next := <-done:
		if next != nil {
			next()
		}
		t.Fatal("status failed rather than waiting for the explorer slot")
	case <-time.After(time.Second):
		t.Fatal("status never queued")
	}
	release()
	release = nil
	select {
	case next := <-done:
		if next == nil {
			t.Fatal("status failed rather than waiting for the explorer slot")
		}
		next()
	case <-time.After(time.Second):
		t.Fatal("status did not resume")
	}
}

func TestWorkspaceStatusWaitCancellation(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon"}
	release, _ := h.acquireWorkspaceExplorer(dc, "owner", "session", false)
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if next, ok := h.waitWorkspaceStatus(ctx, dc, "owner", "session"); ok {
		next()
		t.Fatal("cancelled status acquired a lease")
	}
}

func TestWorkspaceStatusQueuedCancellationReleasesWaiter(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon"}
	release, _ := h.acquireWorkspaceExplorer(dc, "owner", "session", false)
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan bool, 1)
	go func() {
		next, ok := h.waitWorkspaceStatus(statusWaitContext{ctx, entered}, dc, "owner", "session")
		if next != nil {
			next()
		}
		done <- ok
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("cancelled waiter acquired")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not finish")
	}
	h.explorerReqs.mu.Lock()
	waiting := h.explorerReqs.waitingStatus
	h.explorerReqs.mu.Unlock()
	if waiting != 0 {
		t.Fatalf("leaked %d waiters", waiting)
	}
}

func TestWorkspaceStatusWaitsForViewer(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon"}
	first, _ := h.acquireWorkspaceFileRead(dc, "owner")
	defer first()
	second, _ := h.acquireWorkspaceFileRead(dc, "owner")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan func(), 1)
	go func() {
		next, _ := h.waitWorkspaceStatus(statusWaitContext{ctx, entered}, dc, "owner", "session")
		done <- next
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		second()
		t.Fatal("not queued")
	}
	second()
	select {
	case next := <-done:
		if next == nil {
			t.Fatal("viewer release lost")
		}
		next()
	case <-time.After(time.Second):
		t.Fatal("viewer release did not wake status")
	}
}

func TestWorkspaceStatusQueueBound(t *testing.T) {
	h := NewHub()
	h.explorerReqs.waitingStatus = 32
	if next, ok := h.waitWorkspaceStatus(t.Context(), &DaemonConn{ID: "daemon"}, "owner", "session"); ok {
		next()
		t.Fatal("queue bound bypassed")
	}
}
