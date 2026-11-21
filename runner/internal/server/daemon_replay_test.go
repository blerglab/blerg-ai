package server

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func TestBLG768ReplaySessionBatchRotates(t *testing.T) {
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = fmt.Sprintf("session-%02d", i)
	}
	for cursor := uint64(0); cursor < 40; cursor++ {
		batch := daemonReplaySessionIDs(ids, cursor)
		if len(batch) != 20 || batch[0] != ids[cursor%20] {
			t.Fatalf("cursor %d: %v", cursor, batch)
		}
	}
	if len(daemonReplaySessionIDs(nil, 0)) != 0 {
		t.Fatal("empty inventory has work")
	}
	if got := daemonReplaySessionIDs([]string{"b", "a"}, 1); len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("small batch: %v", got)
	}
}
func TestBLG768ReplayPacesRestartAndHonorsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		defer cancel()
		start := time.Now()
		for range 2 {
			before := time.Now()
			if !waitDaemonReplay(ctx, nil) || time.Since(before) != 100*time.Millisecond {
				t.Fatal("reattach batch was not paced")
			}
		}
		if waitDaemonReplay(ctx, nil) || time.Since(start) != 250*time.Millisecond {
			t.Fatal("deadline did not stop replay pacing")
		}
		disconnected := make(chan struct{})
		close(disconnected)
		if waitDaemonReplay(t.Context(), disconnected) {
			t.Fatal("disconnected daemon replayed")
		}
	})
}

func TestBLG768ReplayPreparationUsesDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewHub()
		dc := &DaemonConn{ID: "fixture", send: make(chan []byte, 1)}
		h.Register(dc)
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		h.planInputContext = func(parent context.Context, _ *DaemonConn, raw []byte) ([]byte, func(bool)) {
			if parent != ctx {
				t.Fatal("input preparation lost replay context")
			}
			<-parent.Done()
			return raw, nil
		}
		if h.trySendToDaemonContext(ctx, dc, []byte(`{}`)) {
			t.Fatal("expired replay was queued after preparation")
		}
		if len(dc.send) != 0 {
			t.Fatal("expired replay reached daemon")
		}
	})
}
