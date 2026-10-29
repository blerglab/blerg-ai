package shellruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

// withoutWindow strips the window from the bootstrap line, as a helper that
// predates flow control would ignore it.
func withoutWindow(in io.Reader) io.Reader {
	r := bufio.NewReader(in)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return io.MultiReader(bytes.NewReader(line), r)
	}
	var boot map[string]json.RawMessage
	if json.Unmarshal(line, &boot) == nil {
		delete(boot, "window")
		if encoded, e := json.Marshal(boot); e == nil {
			encoded = append(encoded, '\n')
			line = encoded
		}
	}
	return io.MultiReader(bytes.NewReader(line), r)
}

func relayHarness(t *testing.T, credit *outputCredit) (chan []byte, chan HelperFrame, context.CancelFunc, chan struct{}) {
	t.Helper()
	in := make(chan []byte, 64)
	outputs := make(chan HelperFrame, 64)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		relayOutput(ctx, in, outputs, credit, func() error { return nil }, func() {})
	}()
	t.Cleanup(func() { cancel(); <-done })
	return in, outputs, cancel, done
}

// Small reads queued behind a busy writer become one frame, and a windowed
// helper never has more output outstanding than its credit.
func TestRelayCoalescesAndNeverExceedsCredit(t *testing.T) {
	credit := newOutputCredit(100)
	in, outputs, _, _ := relayHarness(t, credit)
	var want []byte
	for n := range 60 {
		line := []byte{byte('a' + n%26), '\n'}
		want = append(want, line...)
		in <- line
	}
	var got []byte
	frames := 0
	sent := func() {
		for {
			select {
			case f := <-outputs:
				frames++
				got = append(got, f.Data...)
			case <-time.After(100 * time.Millisecond):
				return
			}
		}
	}
	sent()
	if len(got) != 100 {
		t.Fatalf("sent %d bytes with 100 bytes of credit", len(got))
	}
	credit.add(1 << 30) // a forged grant is capped at the window
	sent()
	if !bytes.Equal(got, want) {
		t.Fatalf("output reordered or lost: %d of %d bytes", len(got), len(want))
	}
	if frames >= 60 {
		t.Fatalf("%d frames for 60 reads: nothing coalesced", frames)
	}
	credit.mu.Lock()
	available := credit.available
	credit.mu.Unlock()
	if available > 100 {
		t.Fatalf("credit grew past the window to %d", available)
	}
}

// Waiting for credit or for the writer selects on the context the lease timer
// cancels, so blocked output cannot hold expiry.
func TestRelayWaitsEndWithTheLease(t *testing.T) {
	for _, tc := range []struct {
		name   string
		credit *outputCredit
		buffer int
	}{{"credit", newOutputCredit(1), 64}, {"writer", nil, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			in := make(chan []byte, 4)
			outputs := make(chan HelperFrame, tc.buffer)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() {
				defer close(done)
				relayOutput(ctx, in, outputs, tc.credit, func() error { return nil }, func() {})
			}()
			in <- []byte("blocked output")
			time.Sleep(50 * time.Millisecond)
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("blocked relay ignored lease cancellation")
			}
		})
	}
}

// The end of the PTY stream flushes what is queued before stopping.
func TestRelayFlushesBeforeStoppingAtEOF(t *testing.T) {
	in := make(chan []byte, 4)
	outputs := make(chan HelperFrame, 4)
	stopped := make(chan struct{})
	go relayOutput(t.Context(), in, outputs, nil, func() error { return nil }, func() { close(stopped) })
	in <- []byte("last words")
	close(in)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("relay did not stop at EOF")
	}
	if f := <-outputs; string(f.Data) != "last words" {
		t.Fatalf("final output lost: %q", f.Data)
	}
}

// A briefly busy connection delays a frame; only a link that refuses output
// for the whole stall loses it.
func TestSendWithinRetriesUntilTheStall(t *testing.T) {
	failures := 3
	send := func(context.Context, RPC) error {
		if failures > 0 {
			failures--
			return errors.New("daemon writer busy")
		}
		return nil
	}
	if sendWithin(t.Context(), send, RPC{}, time.Second) != outputSent {
		t.Fatal("a briefly busy writer lost output")
	}
	started := time.Now()
	if sendWithin(t.Context(), func(context.Context, RPC) error { return errors.New("down") }, RPC{}, 300*time.Millisecond) != outputLost {
		t.Fatal("a dead link was not reported lost")
	}
	if elapsed := time.Since(started); elapsed < 300*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("gave up after %s", elapsed)
	}
}

// A finished shell starts no send, cancels the one in flight through the
// transport context, never counts cancellation as delivery, and does not
// drain its buffered frames into the transport.
func TestFinishedShellStopsSendingAtOnce(t *testing.T) {
	ended, end := context.WithCancel(t.Context())
	end()
	calls := 0
	blocked := func(ctx context.Context, _ RPC) error { calls++; <-ctx.Done(); return ctx.Err() }
	if sendWithin(ended, blocked, RPC{}, ownershell.OutputStall) != outputCanceled || calls != 0 {
		t.Fatal("a finished shell started a send")
	}

	out := make(chan []byte, 8)
	for range 8 {
		out <- []byte("buffered")
	}
	life, stop := context.WithCancel(t.Context())
	entered := make(chan struct{}, 8)
	var sends int
	slow := func(ctx context.Context, _ RPC) error {
		sends++
		entered <- struct{}{}
		select { // a busy transport that honors its context
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
			return errors.New("daemon writer busy")
		}
	}
	result := make(chan outputResult, 1)
	go func() {
		result <- sendOutput(life, out, nil, func(d []byte) RPC { return RPC{Data: d} }, slow, ownershell.OutputStall)
	}()
	<-entered
	started := time.Now()
	stop()
	select {
	case r := <-result:
		if r != outputCanceled {
			t.Fatal("cancellation reported as", r)
		}
	case <-time.After(time.Second):
		t.Fatal("a finished shell kept its sender")
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("sender took %s to stop", elapsed)
	}
	if sends != 1 {
		t.Fatalf("%d frames sent after the shell finished", sends-1)
	}
}

// With a server window the sender never has more output outstanding than the
// credit the server returned, and resumes as credit arrives.
func TestSendOutputWaitsForServerCredit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		credit := newOutputCredit(100)
		out := make(chan []byte, 8)
		for range 5 {
			out <- bytes.Repeat([]byte("x"), 60)
		}
		close(out)
		var sent int
		send := func(_ context.Context, r RPC) error { sent += len(r.Data); return nil }
		result := make(chan outputResult, 1)
		go func() {
			result <- sendOutput(t.Context(), out, credit, func(d []byte) RPC { return RPC{Data: d} }, send, time.Second)
		}()
		for _, want := range []int{100, 200} {
			// Wait until the sender is blocked on exhausted credit. Returning a
			// grant before then can hit the window cap and discard credit.
			synctest.Wait()
			if sent != want {
				t.Fatalf("sent %d bytes on %d bytes of credit", sent, want)
			}
			select {
			case r := <-result:
				t.Fatalf("sender ended without credit: %v", r)
			default:
			}
			// Even a long pause cannot send another byte without a grant.
			time.Sleep(time.Hour)
			synctest.Wait()
			if sent != want {
				t.Fatalf("sent without a grant: %d bytes, want %d", sent, want)
			}
			credit.add(100)
		}
		synctest.Wait()
		select {
		case r := <-result:
			if r != outputSent {
				t.Fatal(r)
			}
		default:
			t.Fatal("credit did not resume output")
		}
		if sent != 300 {
			t.Fatalf("sent %d of 300 bytes", sent)
		}
	})
}
