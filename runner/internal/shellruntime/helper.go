package shellruntime

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// HelperFrame is private, framed stdio to a fixed executable, never an agent
// socket. Bootstrap is provisioned only by the authenticated runtime coordinator.
type HelperFrame struct {
	StartStage string                 `json:"start_stage,omitempty"`
	StartCause string                 `json:"start_cause,omitempty"`
	PublicRoot string                 `json:"public_root"`
	Root       protocol.WorkspaceRoot `json:"root"`
	Op         string                 `json:"op"`
	ID         uint64                 `json:"id,omitempty"`
	Grant      ownershell.SignedGrant `json:"grant,omitempty"`
	Key        ed25519.PublicKey      `json:"key,omitempty"`
	Expected   protocol.ShellGrant    `json:"expected,omitempty"`
	Identity   Identity               `json:"identity,omitempty"`
	Data       []byte                 `json:"data,omitempty"`
	Seq        uint64                 `json:"seq,omitempty"`
	// Window is output flow control. On bootstrap the daemon offers a credit
	// window; a helper that supports it echoes the window on ready and then never
	// has more than that many output bytes unacknowledged. "credit" frames return
	// consumed bytes. A helper or daemon without it ignores the field.
	Window     uint32 `json:"window,omitempty"`
	Cols, Rows uint16
	OK         bool `json:"ok,omitempty"`
}

// maxHelperWindow bounds a requested window; output memory stays bounded even
// if a caller asks for more.
const maxHelperWindow = 1 << 20

// outputCredit is the helper's side of output flow control.
type outputCredit struct {
	mu        sync.Mutex
	available int
	window    int
	ready     chan struct{}
}

func newOutputCredit(window int) *outputCredit {
	return &outputCredit{available: window, window: window, ready: make(chan struct{}, 1)}
}

// add returns consumed bytes. Credit is capped at the window, so a repeated
// or forged grant cannot let output memory grow past it.
func (c *outputCredit) add(n uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n > maxHelperWindow {
		n = maxHelperWindow
	}
	c.available = min(c.window, c.available+int(n))
	select {
	case c.ready <- struct{}{}:
	default:
	}
}

// take waits until some credit is available and takes up to want bytes. It
// returns 0 once ctx ends; the lease timer cancels ctx independently of output.
func (c *outputCredit) take(ctx context.Context, want int) int {
	for {
		c.mu.Lock()
		if c.available > 0 {
			n := min(want, c.available)
			c.available -= n
			c.mu.Unlock()
			return n
		}
		c.mu.Unlock()
		select {
		case <-c.ready:
		case <-ctx.Done():
			return 0
		}
	}
}

// ServeHelper must run in its own process: kernel tracing and orphan reaping
// cannot share the agent/daemon's child-reaper namespace or output writer.
func ServeHelper(in io.Reader, out io.Writer) error { return serveHelper(in, out, ResolveIdentity) }

// ServeClusterHelper is a trusted image entry point, never selected by RPC.
// Its tracer stays at the supervisor UID; the PTY executes as the agent UID.
func ServeClusterHelper(in io.Reader, out io.Writer) error {
	return serveHelper(in, out, func(cwd string) (Identity, error) { return resolveAccountIdentity(cwd, 1000) })
}

func serveHelper(in io.Reader, out io.Writer, resolve func(string) (Identity, error)) error {
	return serveHelperWithClock(in, out, resolve, time.Now)
}

func serveHelperWithClock(in io.Reader, out io.Writer, resolve func(string) (Identity, error), now func() time.Time) (result error) {
	var identityChanged atomic.Bool
	var boot HelperFrame
	started := false
	// All bootstrap refusals use the same bounded, redacted frame, including
	// failures before a PTY exists. Never forward a decoder or OS error string.
	defer func() {
		if result != nil && !started {
			_, cause := startDiagnostic(result)
			stage := ""
			var failure *startError
			if errors.As(result, &failure) {
				if safe := ownershell.SafeStartStage(failure.stage); safe != "" {
					stage = failure.stage
					_, cause = startDiagnostic(failure.err)
				}
			}
			_ = json.NewEncoder(out).Encode(HelperFrame{Op: "start_failed", ID: boot.ID, StartStage: stage, StartCause: cause})
		}
	}()
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), 65536)
	decode := func(f *HelperFrame) error {
		if !scan.Scan() {
			if err := scan.Err(); err != nil {
				return err
			}
			return io.EOF
		}
		return json.Unmarshal(scan.Bytes(), f)
	}
	if err := decode(&boot); err != nil {
		return stageError("bootstrap_read", err)
	}
	if boot.Op != "bootstrap" || len(boot.Key) != ed25519.PublicKeySize {
		return stageError("bootstrap_validate", nil)
	}
	publicRoot := boot.PublicRoot
	if publicRoot == "" {
		publicRoot = boot.Identity.Cwd
	}
	publicRoot, rootErr := filepath.EvalSymlinks(publicRoot)
	if rootErr != nil {
		return stageError("resolve_root", rootErr)
	}
	actual, err := resolve(boot.Identity.Cwd)
	if err != nil {
		return stageError("resolve_identity", err)
	}
	if boot.Expected.Runtime == "host" && actual != boot.Identity {
		return stageError("verify_identity", nil)
	}
	if actual.Cwd != boot.Identity.Cwd {
		return stageError("verify_cwd", nil)
	}
	if !matchesRoot(publicRoot, boot.Root) {
		return stageError("verify_root", nil)
	}
	expected := boot.Expected
	if err := ownershell.VerifyGrant(boot.Key, boot.Grant, expected, expected.PolicyRevision, now()); err != nil {
		if errors.Is(err, ownershell.ErrGrantClock) {
			return stageError("grant_clock", nil)
		}
		if errors.Is(err, ownershell.ErrGrantExpired) {
			return stageError("lease_create", nil)
		}
		return stageError("verify_grant", nil)
	}
	process, err := startTraced(actual, boot.Root, publicRoot)
	if err != nil {
		return err
	}
	stop := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return process.stop(ctx)
	}
	var reportFailure func(error)
	var stopWriter func()
	defer func() {
		failure := result
		if stop() == nil {
			if started {
				result = nil
			}
			if identityChanged.Load() {
				result = ErrIdentityChanged
			}
		} else {
			result = stageError("cleanup_shell", nil)
			failure = result
		}
		if started && failure != nil && !identityChanged.Load() && reportFailure != nil {
			reportFailure(failure)
		}
		if stopWriter != nil {
			stopWriter()
		}
	}()
	lease, err := ownershell.NewRuntimeLease(boot.Key, boot.Grant, expected, protocol.ShellPolicy{Version: 1, Revision: expected.PolicyRevision, Enabled: true}, now, stop)
	if err != nil {
		return stageError("lease_create", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controls := make(chan HelperFrame, 16)
	outputs := make(chan HelperFrame, 8)
	writeDone := make(chan struct{})
	written := make(chan uint64, 1)
	identityWritten := make(chan struct{}, 1)
	failureWritten := make(chan struct{}, 1)
	writerContext, writerCancel := context.WithCancel(context.Background())
	stopWriter = writerCancel
	var requestID uint64
	reportFailure = func(failure error) {
		_, cause := startDiagnostic(failure)
		frame := HelperFrame{Op: "control_failed", ID: requestID, StartStage: ownershell.SafeStartStage(cause), StartCause: cause}
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case controls <- frame:
		case <-writeDone:
			return
		case <-timer.C:
			return
		}
		select {
		case <-failureWritten:
		case <-writeDone:
		case <-timer.C:
		}
	}
	var closing atomic.Bool
	// Independent bounded writer; blocked output cannot hold the expiry/reaper path.
	go func() {
		defer close(writeDone)
		enc := json.NewEncoder(out)
		for {
			var frame HelperFrame
			select {
			case frame = <-controls:
			default:
				select {
				case frame = <-controls:
				case frame = <-outputs:
				case <-writerContext.Done():
					return
				}
			}
			if enc.Encode(frame) != nil {
				cancel()
				return
			}
			if frame.Op == "control_failed" {
				failureWritten <- struct{}{}
			}
			if frame.Op == "identity_changed" {
				identityWritten <- struct{}{}
			}
			if frame.Op == "closed" {
				written <- frame.ID
			}
		}
	}()
	sendControl := func(f HelperFrame) bool {
		select {
		case controls <- f:
			return true
		case <-ctx.Done():
			return false
		default:
			cancel()
			return false
		}
	}
	// Flow control only when the daemon offered it; an older daemon keeps the
	// unwindowed stream (it still gets backpressure through the pipe).
	var credit *outputCredit
	if boot.Window > 0 {
		credit = newOutputCredit(int(min(boot.Window, maxHelperWindow)))
	}
	ready := HelperFrame{Op: "ready", ID: boot.ID, OK: true}
	if credit != nil {
		ready.Window = uint32(credit.window) //nolint:gosec // bounded by maxHelperWindow
	}
	if !sendControl(ready) {
		return stageError("helper_ready", nil)
	}
	started = true
	go relayOutput(ctx, process.output, outputs, credit, lease.Check, func() {
		if !closing.Load() {
			cancel()
		}
	})
	// Account database resolution may use NSS. It must never hold the expiry
	// timer, so identity monitoring has its own goroutine and process lifetime.
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				current, err := resolve(actual.Cwd)
				if err != nil || current != actual {
					identityChanged.Store(true)
					closing.Store(true)
					_ = lease.Close()
					if sendControl(HelperFrame{Op: "identity_changed"}) {
						select {
						case <-identityWritten:
						case <-time.After(100 * time.Millisecond):
						}
					}
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	var timerWG sync.WaitGroup
	timerWG.Add(1)
	go func() {
		defer timerWG.Done()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				lease.Tick()
				if lease.Check() != nil {
					if !closing.Load() {
						cancel()
					}
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); timerWG.Wait() }()
	// Decoder is independent of writer and timer. The process owning stdin closes
	// it on parent shutdown. A dead Docker client is never considered cleanup proof.
	requests := make(chan HelperFrame, 1)
	go func() {
		defer close(requests)
		for {
			var f HelperFrame
			if decode(&f) != nil {
				return
			}
			if len(f.Data) > 16384 {
				return
			}
			// Credit only lets this helper send the daemon more of its own
			// output; it carries no authority, so it skips the request loop
			// and can never wait behind a slow control operation.
			if f.Op == "credit" {
				if credit != nil {
					credit.add(f.Seq)
				}
				continue
			}
			select {
			case requests <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	var inputSeq, resizeSeq uint64
	for {
		select {
		case <-ctx.Done():
			if lease.Check() != nil && !closing.Load() {
				return stageError("lease_check", nil)
			}
			return stageError("helper_stopped", nil)
		case f, ok := <-requests:
			if !ok {
				return stageError("control_read", nil)
			}
			requestID = f.ID
			if f.Op == "close" {
				// Revocation remains usable after expiry, but only for this signed identity.
				valid := ownershell.VerifyGrant(boot.Key, f.Grant, expected, expected.PolicyRevision, time.Unix(f.Grant.Grant.IssuedAt, 0)) == nil
				if !valid {
					return stageError("control_grant", nil)
				}
				closing.Store(true)
				err = lease.Close()
				if !sendControl(HelperFrame{Op: "closed", ID: f.ID, OK: err == nil}) {
					return stageError("control_write", nil)
				}
				// Wait for the actual control ack write, never an arbitrary sleep.
				for {
					select {
					case id := <-written:
						if id == f.ID {
							return err
						}
					case <-writeDone:
						return stageError("control_write", nil)
					case <-ctx.Done():
						return stageError("helper_stopped", nil)
					}
				}
			}
			if lease.Check() != nil {
				return stageError("lease_check", nil)
			}
			valid := ownershell.VerifyGrant(boot.Key, f.Grant, expected, expected.PolicyRevision, now()) == nil
			if !valid {
				return stageError("control_grant", nil)
			}
			switch f.Op {
			case "freeze", "thaw":
				call, cancel := context.WithTimeout(ctx, 2*time.Second)
				if f.Op == "freeze" {
					err = process.freeze(call)
				} else {
					err = process.thaw(call)
				}
				cancel()
			case "renew":
				err = lease.Renew(f.Grant)
			case "input":
				if f.Seq != inputSeq+1 {
					return stageError("input_sequence", nil)
				}
				err = process.input(f.Data)
				if err == nil {
					inputSeq = f.Seq
				}
			case "resize":
				if f.Seq != resizeSeq+1 {
					return stageError("resize_sequence", nil)
				}
				err = process.resize(f.Cols, f.Rows)
				if err == nil {
					resizeSeq = f.Seq
				}
			default:
				return stageError("control_operation", nil)
			}
			if err != nil {
				return stageError("control_apply", nil)
			}
			if !sendControl(HelperFrame{Op: "ack", ID: f.ID, OK: true}) {
				return stageError("control_write", nil)
			}
		}
	}
}

// relayOutput moves PTY reads to the framed writer. Reads queued behind a busy
// writer are coalesced into frames of up to ownershell.MaxFrame bytes, and every
// wait (writer, credit) also selects on ctx, which the lease timer and identity
// monitor cancel on their own goroutines. Blocked output therefore only blocks
// the PTY reader, which blocks the program's writes. stop runs once the PTY
// stream ends or the lease no longer holds.
func relayOutput(ctx context.Context, in <-chan []byte, outputs chan<- HelperFrame, credit *outputCredit, check func() error, stop func()) {
	pending := make([]byte, 0, ownershell.MaxFrame+4096)
	eof := false
	receive := func(data []byte, ok bool) bool {
		if !ok {
			eof = true
			return false
		}
		if check() != nil {
			stop()
			return false
		}
		pending = append(pending, data...)
		return true
	}
	for {
		if len(pending) == 0 {
			if eof {
				stop()
				return
			}
			select {
			case data, ok := <-in:
				if !receive(data, ok) && !eof {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	fill:
		for !eof && len(pending) < ownershell.MaxFrame {
			select {
			case data, ok := <-in:
				if !receive(data, ok) {
					if !eof {
						return
					}
					break fill
				}
			default:
				break fill
			}
		}
		if len(pending) == 0 {
			continue
		}
		n := min(len(pending), ownershell.MaxFrame)
		if credit != nil {
			if n = credit.take(ctx, n); n == 0 {
				return
			}
		}
		frame := HelperFrame{Op: "output", Data: append([]byte(nil), pending[:n]...)}
		pending = pending[:copy(pending, pending[n:])]
		select {
		case outputs <- frame:
		case <-ctx.Done():
			return
		}
	}
}
