package daemon

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"sync"
	"testing"
	"time"
)

type mcpFakeSource struct {
	mu    sync.Mutex
	probe mcpProbe
	calls int
}

func (f *mcpFakeSource) MCPStatus(context.Context) (mcpProbe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	p := f.probe
	p.Servers = append([]protocol.MCPServerStatus{}, p.Servers...)
	return p, nil
}
func (f *mcpFakeSource) MCPGeneration() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probe.Generation
}
func (f *mcpFakeSource) set(status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probe.Servers[0].Status = status
}
func mcpTestTracker(t *testing.T, wait time.Duration) (*mcpTracker, *mcpFakeSource, *collectEmitter) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	em := &collectEmitter{}
	src := &mcpFakeSource{probe: mcpProbe{Ready: true, Generation: 1, Servers: []protocol.MCPServerStatus{{Name: "fixture", Status: "pending", Source: "user"}}}}
	tr := newMCPTracker(ctx, "claude", src, em, mcpTrackerOptions{Wait: wait, Poll: 5 * time.Millisecond, Track: 100 * time.Millisecond})
	t.Cleanup(func() {
		cancel()
		select {
		case <-tr.done:
		case <-time.After(time.Second):
			t.Error("tracker did not stop")
		}
	})
	return tr, src, em
}
func mcpLatest(em *collectEmitter) protocol.MCPStatusPayload {
	es := em.snapshot()
	for i := len(es) - 1; i >= 0; i-- {
		if es[i].Kind == protocol.MCPStatusKind {
			return es[i].Payload.(protocol.MCPStatusPayload)
		}
	}
	return protocol.MCPStatusPayload{}
}
func waitMCP(t *testing.T, fn func() bool) {
	t.Helper()
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("MCP fixture condition not reached")
}
func TestMCPFirstPromptReleasesOnSettleTimeoutOrPerson(t *testing.T) {
	for _, reason := range []string{"settled", "timeout", "person"} {
		t.Run(reason, func(t *testing.T) {
			tr, src, em := mcpTestTracker(t, 40*time.Millisecond)
			waitMCP(t, func() bool { return len(mcpLatest(em).Servers) == 1 })
			done := make(chan struct{})
			go func() { defer close(done); tr.beforeFirstPrompt(context.Background()) }()
			waitMCP(t, func() bool { return mcpLatest(em).Hold != nil })
			switch reason {
			case "settled":
				src.set("connected")
			case "person":
				tr.release()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("held prompt did not release")
			}
			waitMCP(t, func() bool { p := mcpLatest(em); return p.Hold == nil && p.Released == reason })
			src.set("pending")
			tr.beforeFirstPrompt(context.Background())
			tr.release()
			if mcpLatest(em).Hold != nil {
				t.Fatal("later prompt held")
			}
		})
	}
}
func TestMCPNoServersAndDisabledWaitDoNotHold(t *testing.T) {
	for _, empty := range []bool{true, false} {
		t.Run(map[bool]string{true: "empty", false: "disabled"}[empty], func(t *testing.T) {
			wait := time.Duration(0)
			if empty {
				wait = time.Second
			}
			tr, src, em := mcpTestTracker(t, wait)
			if empty {
				src.mu.Lock()
				src.probe.Servers = []protocol.MCPServerStatus{}
				src.mu.Unlock()
			}
			tr.beforeFirstPrompt(context.Background())
			for _, ev := range em.snapshot() {
				if ev.Kind == protocol.MCPStatusKind && ev.Payload.(protocol.MCPStatusPayload).Hold != nil {
					t.Fatal("unexpected hold")
				}
			}
		})
	}
}
func TestMCPStopsPollingAfterSettleAndRestartsOnGeneration(t *testing.T) {
	_, src, em := mcpTestTracker(t, time.Second)
	src.set("connected")
	waitMCP(t, func() bool { return mcpLatest(em).Settled })
	src.mu.Lock()
	count := src.calls
	src.mu.Unlock()
	time.Sleep(25 * time.Millisecond)
	src.mu.Lock()
	if src.calls != count {
		t.Error("settled source polled")
	}
	src.probe.Generation = 2
	src.probe.Servers[0].Status = "pending"
	src.mu.Unlock()
	waitMCP(t, func() bool { return !mcpLatest(em).Settled && len(mcpLatest(em).Servers) == 1 })
	src.set("connected")
	waitMCP(t, func() bool { return mcpLatest(em).Settled })
}
func TestMCPTrackingDeadlineDoesNotInventConnectedState(t *testing.T) {
	_, _, em := mcpTestTracker(t, 0)
	waitMCP(t, func() bool { return mcpLatest(em).Settled })
	p := mcpLatest(em)
	if len(p.Servers) != 1 || p.Servers[0].Status != "pending" {
		t.Fatalf("invented readiness: %+v", p)
	}
}
func TestMCPWaitConfiguration(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{{"", 30 * time.Second}, {"0", 0}, {"5", 5 * time.Second}, {"60", 30 * time.Second}, {"-1", 30 * time.Second}, {"bad", 30 * time.Second}} {
		if got := parseMCPPromptWait(tc.raw); got != tc.want {
			t.Errorf("wait(%q)=%v want %v", tc.raw, got, tc.want)
		}
	}
}

func TestMCPCanceledFirstGateDoesNotConsumeInput(t *testing.T) {
	tr, _, em := mcpTestTracker(t, time.Second)
	waitMCP(t, func() bool { return len(mcpLatest(em).Servers) == 1 })
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() { tr.beforeFirstPrompt(ctx); close(returned) }()
	waitMCP(t, func() bool { return mcpLatest(em).Hold != nil })
	cancel()
	<-returned
	second := make(chan struct{})
	go func() { tr.beforeFirstPrompt(context.Background()); close(second) }()
	waitMCP(t, func() bool { return mcpLatest(em).Hold != nil })
	select {
	case <-second:
		t.Fatal("replacement input bypassed first hold")
	case <-time.After(10 * time.Millisecond):
	}
	tr.release()
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("replacement not released")
	}
}
func TestMCPTrackingExpiredFirstPromptHasNoInvisibleHold(t *testing.T) {
	tr, _, em := mcpTestTracker(t, time.Second)
	waitMCP(t, func() bool { return mcpLatest(em).Settled })
	done := make(chan struct{})
	go func() { tr.beforeFirstPrompt(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("tracking expired but prompt invisibly held")
	}
	if p := mcpLatest(em); p.Hold != nil || p.Released != "timeout" {
		t.Fatalf("wrong deadline state: %+v", p)
	}
}
func TestMCPFirstAlreadyUsedNeverHolds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &mcpFakeSource{probe: mcpProbe{Ready: true, Generation: 1, Servers: []protocol.MCPServerStatus{{Name: "fixture", Status: "pending"}}}}
	em := &collectEmitter{}
	tr := newMCPTracker(ctx, "codex", src, em, mcpTrackerOptions{Wait: time.Second, FirstUsed: true})
	tr.beforeFirstPrompt(context.Background())
	for _, ev := range em.snapshot() {
		if ev.Payload.(protocol.MCPStatusPayload).Hold != nil {
			t.Fatal("restored session held a later input")
		}
	}
	cancel()
	<-tr.done
}

func TestMCPSnapshotsHaveDurableDistinctIdentities(t *testing.T) {
	_, src, em := mcpTestTracker(t, 0)
	waitMCP(t, func() bool { return len(mcpLatest(em).Servers) == 1 })
	src.set("connected")
	waitMCP(t, func() bool { return mcpLatest(em).Settled })
	seen := map[string]bool{}
	for _, ev := range em.snapshot() {
		if ev.ClientEventID == "" || seen[ev.ClientEventID] || ev.Ts.IsZero() {
			t.Fatalf("invalid durable identity: %+v", ev)
		}
		seen[ev.ClientEventID] = true
	}
}

func TestMCPDiscoverySharesTheFirstPromptDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &mcpFakeSource{probe: mcpProbe{Generation: 1}}
	em := &collectEmitter{}
	tr := newMCPTracker(ctx, "codex", src, em, mcpTrackerOptions{Wait: 200 * time.Millisecond, Poll: time.Millisecond})
	done := make(chan struct{})
	go func() { tr.beforeFirstPrompt(ctx); close(done) }()
	time.Sleep(40 * time.Millisecond)
	src.mu.Lock()
	src.probe.Ready = true
	src.probe.Servers = []protocol.MCPServerStatus{{Name: "fixture", Status: "pending"}}
	src.mu.Unlock()
	waitMCP(t, func() bool { return mcpLatest(em).Hold != nil })
	hold := mcpLatest(em).Hold
	since, err := time.Parse(time.RFC3339Nano, hold.Since)
	if err != nil {
		t.Fatal(err)
	}
	until, err := time.Parse(time.RFC3339Nano, hold.Until)
	if err != nil {
		t.Fatal(err)
	}
	if until.Sub(since) > 180*time.Millisecond {
		t.Fatal("discovery restarted the full hold budget")
	}
	tr.release()
	<-done
	cancel()
	<-tr.done
}

func TestMCPInvalidSourceInventoryIsNotAuthoritativeEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &mcpFakeSource{probe: mcpProbe{Generation: 1, Ready: true, Servers: []protocol.MCPServerStatus{{Name: "https://invalid.example/private", Status: "connected"}}}}
	em := &collectEmitter{}
	tr := newMCPTracker(ctx, "fixture", src, em, mcpTrackerOptions{Poll: time.Millisecond, Track: time.Second})
	waitMCP(t, func() bool { src.mu.Lock(); defer src.mu.Unlock(); return src.calls >= 2 })
	if mcpLatest(em).Settled {
		t.Fatal("dropped invalid inventory became authoritative empty")
	}
	cancel()
	<-tr.done
}
