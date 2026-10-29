//go:build linux

package shellruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

// collectUntil reads runtime output until marker appears, failing if the channel
// closes (the shell ended) or the deadline passes. It returns everything read.
func collectUntil(t *testing.T, out <-chan []byte, marker string, within time.Duration) []byte {
	t.Helper()
	timer := time.NewTimer(within)
	defer timer.Stop()
	var data []byte
	for {
		select {
		case chunk, ok := <-out:
			if !ok {
				t.Fatalf("shell output ended after %d bytes before %q", len(data), marker)
			}
			if len(chunk) > ownershell.MaxFrame {
				t.Fatalf("frame of %d bytes exceeds the %d byte frame cap", len(chunk), ownershell.MaxFrame)
			}
			data = append(data, chunk...)
			if bytes.Contains(data, []byte(marker)) {
				return data
			}
		case <-timer.C:
			t.Fatalf("no %q within %s (%d bytes read)", marker, within, len(data))
		}
	}
}

// expectLines checks that want[first..last] appear once each, in order, in the output.
func expectLines(t *testing.T, data []byte, prefix string, first, last int) {
	t.Helper()
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	at := 0
	for i := first; i <= last; i++ {
		line := fmt.Sprintf("%s%d\n", prefix, i)
		next := strings.Index(text[at:], line)
		if next < 0 {
			t.Fatalf("line %q missing or out of order", strings.TrimSpace(line))
		}
		at += next + len(line)
	}
}

func shellAlive(t *testing.T, c *Coordinator, g ownershell.SignedGrant, out <-chan []byte, seq uint64) {
	t.Helper()
	if err := c.Input(t.Context(), g, seq, []byte("printf '\\101\\114\\111\\126\\105\\n'\n")); err != nil {
		t.Fatal("shell refused input after the burst:", err)
	}
	collectUntil(t, out, "ALIVE", 10*time.Second)
}

// The owner's report: one command writes many short stderr lines in a burst
// (each a separate small PTY read). The shell must stay up and every line must
// arrive in order.
func TestBurstOfSmallWritesKeepsShellUp(t *testing.T) {
	c, b, g, key := fixture(t)
	out, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	command := "i=1; while [ $i -le 600 ]; do printf 'ls: cannot access missing-%d\\n' $i >&2; i=$((i+1)); done; printf '\\104\\117\\116\\105\\n'\n"
	if err = c.Input(t.Context(), g, 1, []byte(command)); err != nil {
		t.Fatal(err)
	}
	data := collectUntil(t, out, "DONE", 20*time.Second)
	expectLines(t, data, "ls: cannot access missing-", 1, 600)
	shellAlive(t, c, g, out, 2)
}

// A consumer that stops reading for a while (a slow browser or link) applies
// backpressure; it must not end the shell or lose bytes.
func TestStalledConsumerBackpressuresInsteadOfClosing(t *testing.T) {
	c, b, g, key := fixture(t)
	out, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	command := "i=1; while [ $i -le 3000 ]; do echo row-$i; i=$((i+1)); done; printf '\\104\\117\\116\\105\\n'\n"
	if err = c.Input(t.Context(), g, 1, []byte(command)); err != nil {
		t.Fatal(err)
	}
	// Nobody reads for two seconds while the loop writes.
	time.Sleep(2 * time.Second)
	c.sessionMu.Lock()
	peer := c.peers[g.Grant.ShellGeneration]
	c.sessionMu.Unlock()
	select {
	case <-peer.done:
		t.Fatal("a stalled consumer ended the shell")
	default:
	}
	if peer.window.Load() != ownershell.OutputWindow {
		t.Fatal("the helper did not accept output flow control")
	}
	if peer.output.Len() > ownershell.OutputWindow {
		t.Fatalf("daemon output queue holds %d bytes, over the %d byte window", peer.output.Len(), ownershell.OutputWindow)
	}
	data := collectUntil(t, out, "DONE", 30*time.Second)
	expectLines(t, data, "row-", 1, 3000)
	shellAlive(t, c, g, out, 2)
}

// Sustained output is throttled to the output budget but arrives complete.
func TestSustainedOutputIsThrottledAndComplete(t *testing.T) {
	c, b, g, key := fixture(t)
	out, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	// seq 1 30000 writes 168,894 bytes: well over one second of the output budget.
	if err = c.Input(t.Context(), g, 1, []byte("seq 1 30000; printf '\\104\\117\\116\\105\\n'\n")); err != nil {
		t.Fatal(err)
	}
	frames := 0
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	var data []byte
	for !bytes.Contains(data, []byte("DONE")) {
		select {
		case chunk, ok := <-out:
			if !ok {
				t.Fatal("sustained output ended the shell")
			}
			frames++
			data = append(data, chunk...)
		case <-timer.C:
			t.Fatal("sustained output did not finish")
		}
	}
	elapsed := time.Since(started)
	expectLines(t, data, "", 1, 30000)
	if elapsed < 2*time.Second {
		t.Fatalf("%d bytes arrived in %s, faster than the %d byte/s budget", len(data), elapsed, ownershell.OutputRate)
	}
	if perSecond := float64(frames) / elapsed.Seconds(); perSecond > OutputFramesPerSecond+1 {
		t.Fatalf("%.0f frames/s over the %d frame budget", perSecond, OutputFramesPerSecond)
	}
	shellAlive(t, c, g, out, 2)
}

// Output blocked end to end must not hold the helper's own expiry timer.
func TestLeaseExpiresOnTimeWhileOutputIsBlocked(t *testing.T) {
	c, b, g, key := fixture(t)
	g.Grant.ExpiresAt = time.Now().Unix() + 3
	g = ownershell.SignGrant(key, g.Grant)
	_, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	c.sessionMu.Lock()
	peer := c.peers[g.Grant.ShellGeneration]
	c.sessionMu.Unlock()
	// yes floods the PTY; nobody reads the output channel, so every stage fills.
	if err = c.Input(t.Context(), g, 1, []byte("yes\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	select {
	case <-peer.done:
	case <-deadline.C:
		t.Fatal("blocked output held the lease expiry")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	if _, err = c.CloseLaunch(ctx, c.launch(), "expired"); err != nil {
		t.Fatal("cleanup proof after expiry:", err)
	}
}

// Both sides of a version skew (a daemon or a sandbox image helper without
// flow control) fall back to the unwindowed stream, which still survives a burst.
func TestUnwindowedHelperStillSurvivesABurst(t *testing.T) {
	c, b, g, key := fixture(t)
	c.command.Args[len(c.command.Args)-1] = "p5-unwindowed-helper"
	out, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	c.sessionMu.Lock()
	peer := c.peers[g.Grant.ShellGeneration]
	c.sessionMu.Unlock()
	if peer.window.Load() != 0 {
		t.Fatal("a helper without flow control was treated as windowed")
	}
	command := "i=1; while [ $i -le 600 ]; do printf 'ls: cannot access missing-%d\\n' $i >&2; i=$((i+1)); done; printf '\\104\\117\\116\\105\\n'\n"
	if err = c.Input(t.Context(), g, 1, []byte(command)); err != nil {
		t.Fatal(err)
	}
	data := collectUntil(t, out, "DONE", 20*time.Second)
	expectLines(t, data, "ls: cannot access missing-", 1, 600)
	shellAlive(t, c, g, out, 2)
}

func serviceShell(t *testing.T, send func(RPC) error, window uint32) (*Service, ownershell.Binding, ownershell.SignedGrant, RPC) {
	t.Helper()
	base, b, g, key := fixture(t)
	service := NewService(send)
	if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	b, _ = service.Binding(b.Session)
	g.Grant.ConnectionID = b.Connection
	g.Grant.Audience = b.Audience
	g = ownershell.SignGrant(key, g.Grant)
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy}).OK {
		t.Fatal("configure")
	}
	opened := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g, Window: window})
	if !opened.OK {
		t.Fatal("open")
	}
	return service, b, g, opened
}

func creditFrame(t *testing.T, session, shell string, n int) []byte {
	t.Helper()
	raw, err := json.Marshal(RPC{Version: 1, Type: "owner_shell_request", SessionID: session, Op: "credit", Shell: shell, Seq: uint64(n)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// End to end through the daemon: a server consuming slower than the output
// budget, but always progressing, throttles the program through credit. The
// daemon never has more than the window unacknowledged, every byte arrives in
// order, and the shell stays up.
func TestServiceOutputFollowsServerCredit(t *testing.T) {
	const window = 32 << 10
	frames := make(chan []byte, 1024)
	service, b, g, opened := serviceShell(t, func(r RPC) error {
		if r.Type == "owner_shell_output" && len(r.Data) > 0 {
			frames <- r.Data
		}
		return nil
	}, window)
	if opened.Window != window {
		t.Fatalf("daemon did not accept the server window: %d", opened.Window)
	}
	shell := g.Grant.ShellGeneration
	// seq 1 40000 writes 228,894 bytes: seven windows.
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "input", Grant: g, Seq: 1, Data: []byte("seq 1 40000; printf '\\104\\117\\116\\105\\n'\n")}).OK {
		t.Fatal("input")
	}
	var got []byte
	credited, peak := 0, 0
	deadline := time.After(40 * time.Second)
	for !bytes.Contains(got, []byte("DONE")) {
		select {
		case data := <-frames:
			got = append(got, data...)
			peak = max(peak, len(got)-credited)
			// A slow consumer: about 40 KiB/s, below the 64 KiB/s budget.
			time.Sleep(time.Duration(len(data)) * time.Second / (40 << 10))
			credited += len(data)
			service.HandleContext(t.Context(), creditFrame(t, b.Session, shell, len(data)))
		case <-deadline:
			t.Fatalf("output stalled after %d bytes", len(got))
		}
	}
	if peak > window {
		t.Fatalf("%d bytes outstanding over the %d byte window", peak, window)
	}
	expectLines(t, got, "", 1, 40000)
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "input", Grant: g, Seq: 2, Data: []byte("printf '\\101\\114\\111\\126\\105\\n'\n")}).OK {
		t.Fatal("shell refused input after throttled output")
	}
	for !bytes.Contains(got, []byte("ALIVE")) {
		select {
		case data := <-frames:
			got = append(got, data...)
			service.HandleContext(t.Context(), creditFrame(t, b.Session, shell, len(data)))
		case <-time.After(10 * time.Second):
			t.Fatal("shell did not answer after throttled output")
		}
	}
}

// A shell that ends while its transport is slow and frames are buffered must
// not hold the launch's output join: close_launch proves cleanup within its
// own deadline instead of sending every buffered frame first.
func TestFinishedShellOutputJoinIsPrompt(t *testing.T) {
	var started atomic.Int32
	service, b, g, _ := serviceShell(t, func(r RPC) error {
		if r.Type == "owner_shell_output" {
			started.Add(1)
			time.Sleep(500 * time.Millisecond) // a busy link that ignores cancellation
			return errors.New("daemon writer busy")
		}
		return nil
	}, 0)
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "input", Grant: g, Seq: 1, Data: []byte("yes | head -c 400000\n")}).OK {
		t.Fatal("input")
	}
	time.Sleep(time.Second) // output queues behind the slow link
	before := time.Now()
	// The production worker gives each command two seconds.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	reply := service.execute(ctx, RPC{SessionID: b.Session, Op: "close_launch", Launch: ownershell.SupervisorIdentity(b, g).Launch})
	if !reply.OK {
		t.Fatalf("output join held cleanup past its deadline (%s, %d sends)", time.Since(before), started.Load())
	}
	sends := started.Load()
	time.Sleep(time.Second)
	if started.Load() != sends {
		t.Fatal("the finished shell kept sending buffered frames")
	}
}

// committedOutput is a Service with a real helper whose first output frame is
// a committed transport write: it ignores cancellation and takes 600 ms.
type committedOutput struct {
	service     *Service
	b           ownershell.Binding
	g           ownershell.SignedGrant
	entered     chan struct{}
	mu          sync.Mutex
	finished    time.Time
	startsAfter []time.Time
}

func newCommittedOutput(t *testing.T) *committedOutput {
	t.Helper()
	co := &committedOutput{entered: make(chan struct{})}
	var once sync.Once
	committed := func(_ context.Context, r RPC) error {
		if r.Type != "owner_shell_output" {
			return nil
		}
		co.mu.Lock()
		co.startsAfter = append(co.startsAfter, time.Now())
		co.mu.Unlock()
		first := false
		once.Do(func() { first = true; close(co.entered) })
		if first {
			time.Sleep(600 * time.Millisecond)
			co.mu.Lock()
			co.finished = time.Now()
			co.mu.Unlock()
		}
		return nil
	}
	base, b, g, key := fixture(t)
	service := NewService(func(RPC) error { return nil })
	service.SetOutputSender(committed)
	if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	b, _ = service.Binding(b.Session)
	g.Grant.ConnectionID = b.Connection
	g.Grant.Audience = b.Audience
	g = ownershell.SignGrant(key, g.Grant)
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy}).OK {
		t.Fatal("configure")
	}
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g}).OK {
		t.Fatal("open")
	}
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "input", Grant: g, Seq: 1, Data: []byte("seq 1 100000\n")}).OK {
		t.Fatal("input")
	}
	select {
	case <-co.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no output write began")
	}
	co.service, co.b, co.g = service, b, g
	return co
}

// prove requests a single-shell cleanup proof by path, under the production
// two-second command deadline, and reports whether it was granted.
func (co *committedOutput) prove(t *testing.T, path string) bool {
	t.Helper()
	identity := ownershell.SupervisorIdentity(co.b, co.g)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	switch path {
	case "close":
		return co.service.execute(ctx, RPC{SessionID: co.b.Session, Op: "close", Grant: co.g}).OK
	case "close_shell":
		return co.service.execute(ctx, RPC{SessionID: co.b.Session, Op: "close_shell", Launch: identity.Launch, Shell: identity.ShellGeneration}).OK
	default:
		proof, err := co.service.Lifecycle(co.b.Session).CloseShell(ctx, identity, "test")
		return err == nil && proof.ValidateFor(identity.Launch, identity.ShellGeneration) == nil
	}
}

// checkJoined asserts the proof followed the committed write and nothing
// started afterwards.
func (co *committedOutput) checkJoined(t *testing.T, proved time.Time) {
	t.Helper()
	co.mu.Lock()
	done := co.finished
	co.mu.Unlock()
	if done.IsZero() || done.After(proved) {
		t.Fatal("cleanup proof published before the committed output write finished")
	}
	time.Sleep(300 * time.Millisecond)
	co.mu.Lock()
	defer co.mu.Unlock()
	for _, start := range co.startsAfter {
		if start.After(proved) {
			t.Fatal("an output frame started after the cleanup proof")
		}
	}
}

// A single-shell cleanup proof, on the wire or through the native lifecycle,
// must follow that shell's committed output write: the sender is stopped and
// joined first, no further frame starts, and the proof stays within its deadline.
func TestSingleShellProofJoinsCommittedOutputWrite(t *testing.T) {
	for _, path := range []string{"close", "close_shell", "native"} {
		t.Run(path, func(t *testing.T) {
			co := newCommittedOutput(t)
			if !co.prove(t, path) {
				t.Fatal("no cleanup proof within the command deadline")
			}
			co.checkJoined(t, time.Now())
		})
	}
}

// A cleanup that ran out of time for the output join (here Rebind with a short
// deadline: the helper is gone, its proof cached, the sender still writing)
// must not let a retry return the cached proof early, on any path.
func TestCachedProofRetryJoinsCommittedOutputWrite(t *testing.T) {
	for _, path := range []string{"close", "close_shell", "native"} {
		t.Run(path, func(t *testing.T) {
			co := newCommittedOutput(t)
			short, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			err := co.service.Rebind(short)
			cancel()
			if err == nil {
				t.Fatal("Rebind proved cleanup before the committed write finished")
			}
			if !co.prove(t, path) {
				t.Fatal("no cleanup proof on retry within the command deadline")
			}
			co.checkJoined(t, time.Now())
		})
	}
}
