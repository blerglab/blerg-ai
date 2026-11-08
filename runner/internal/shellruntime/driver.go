package shellruntime

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const FixedContainerSupervisor = "/usr/local/bin/blerg-shell-supervisor"

// Command is assembled by trusted daemon/runtime state only. No browser field
// supplies an executable, argv, container, account or cwd.
type Command struct {
	Container           string // exact trusted runtime container identity
	persistentConsent   func(protocol.ShellCustody) bool
	cleanupUncertain    func()                 // nonblocking physical fence notification, never identity revocation
	Custody             *protocol.ShellCustody // trusted runtime allocation, never browser configuration
	OwnedStarted        func(int) error        // trusted durable helper identity before bootstrap
	BeforeHostShell     func() error           // trusted durable history before forwarding any host open
	diagnostic          func(string, error)
	identityInvalidated func()
	PublicRoot          string
	// SourceLive is the runtime owner's check of the binding the launch was
	// attached through (a project alias pinned to its directory). It is rechecked
	// on every operation that verifies or uses a shell, never on cleanup.
	SourceLive  func() bool
	SourceState func() (same, known bool)
	Recovery    func(context.Context, protocol.SupervisorLaunch) error
	Root        protocol.WorkspaceRoot
	Consent     *protocol.HostShellConsent
	Policy      protocol.ShellPolicy
	Executable  string
	Args        []string
	Identity    Identity
}

func HostCommand(executable string, identity Identity) Command {
	return Command{Executable: executable, Args: []string{"shell-supervisor"}, Identity: identity}
}
func SandboxCommand(container, account string, identity Identity) Command {
	return Command{Container: container, Executable: "docker", Args: []string{"exec", "-i", "--user", account, container, FixedContainerSupervisor}, Identity: identity}
}

type helperPeer struct {
	controlSeq          uint64 // writeMu owns authenticated request ordering
	inspectedEpoch      atomic.Uint64
	inspectedActive     atomic.Bool
	cleanupUncertain    func()
	cleanup             *cleanupAuthority
	retained            bool
	shellCgroup         string
	stream              *retainedOutput
	expectedClose       atomic.Bool
	closeMu             sync.Mutex
	identityInvalidated func()
	failure             error
	clean               bool
	proveTreeDead       func() bool // after helper reap; before publishing done
	cmd                 *exec.Cmd
	input               *os.File
	output              *ownershell.OutputPipe
	done                chan struct{}
	// Output flow control (helper echoed a window on ready): consumed bytes not
	// yet returned to the helper as credit, and the signal for its sender.
	window      atomic.Int64
	unacked     atomic.Int64
	creditReady chan struct{}
	forwardDone chan struct{}
	writeMu     sync.Mutex
	mu          sync.Mutex
	pending     map[uint64]chan HelperFrame
	seq         atomic.Uint64
}

func startPeer(ctx context.Context, command Command, g ownershell.SignedGrant, key ed25519.PublicKey) (*helperPeer, error) {
	// Refuse an unavailable privileged mode before allocating or starting a helper.
	if g.Grant.Privileged && (g.Grant.Runtime != "host" || command.Container != "" || !hostShellCgroupEnabled()) {
		return nil, stageError("containment_setup", nil)
	}
	var cleanup *cleanupAuthority
	var helperID string
	if g.Grant.Version == 2 {
		if g.Grant.Runtime != "host" || command.Container != "" {
			return nil, stageError("verify_binding", nil)
		}
		if protectHelperProcess() != nil {
			return nil, stageError("bootstrap_validate", nil)
		}
		if command.Custody == nil || command.Custody.Digest() != g.Grant.CustodyID || command.Custody.Container != command.Container || command.Custody.Identity != RuntimeIdentityDigest(command.Identity) {
			return nil, stageError("verify_binding", nil)
		}
		var err error
		helperID, err = newHelperID()
		if err != nil {
			return nil, err
		}
		cleanup, err = newCleanupAuthority(g.Grant.CustodyID, helperID)
		if err != nil {
			return nil, err
		}
	}
	cmd := exec.Command(command.Executable, command.Args...) //nolint:gosec,noctx // fixed trusted runtime command; local grant/containment supervisor owns lifetime
	cmd.Env = transportEnvironment()
	proveTreeDead, releaseLedger, err := helperProcessLedger(context.WithoutCancel(ctx), cmd, cleanup != nil && command.Container == "" && !g.Grant.Privileged)
	if err != nil {
		return nil, stageError("helper_ledger", err)
	}
	ledgerHandedOff := false
	defer func() {
		releaseLedger()
		if !ledgerHandedOff && proveTreeDead != nil {
			_ = proveTreeDead()
		}
	}()
	stdin, write, err := helperTransport(cleanup != nil)
	if err != nil {
		return nil, stageError("helper_input_pipe", err)
	}
	read, stdout, err := helperTransport(cleanup != nil)
	if err != nil {
		_ = stdin.Close()
		_ = write.Close()
		return nil, stageError("helper_output_pipe", err)
	}
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	done := make(chan struct{})
	if err = startOwnedCommand(cmd, done); err != nil {
		_ = stdin.Close()
		_ = write.Close()
		_ = read.Close()
		_ = stdout.Close()
		return nil, stageError("helper_start", err)
	}
	_ = stdin.Close()
	_ = stdout.Close()
	if command.OwnedStarted != nil {
		if err := command.OwnedStarted(cmd.Process.Pid); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			close(done)
			_ = write.Close()
			_ = read.Close()
			return nil, stageError("helper_record", err)
		}
	}
	p := &helperPeer{identityInvalidated: command.identityInvalidated, cmd: cmd, input: write, output: ownershell.NewOutputPipe(ownershell.OutputWindow), creditReady: make(chan struct{}, 1), done: done, pending: make(map[uint64]chan HelperFrame)}
	p.cleanupUncertain = command.cleanupUncertain
	p.cleanup = cleanup
	p.retained = cleanup != nil
	p.proveTreeDead = proveTreeDead
	if g.Grant.Privileged {
		var err error
		p.shellCgroup, err = newShellCgroup()
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			close(done)
			_ = write.Close()
			_ = read.Close()
			return nil, stageError("containment_setup", err)
		}
	}
	if p.retained {
		p.stream = newRetainedOutput(g.Grant.AccessEpoch)
	}
	ledgerHandedOff = true
	go p.read(read, func(err error) { //nolint:contextcheck // Cleanup outlives whoever started this helper.
		if command.diagnostic != nil {
			command.diagnostic(g.Grant.ShellGeneration, err)
		}
	})
	bootstrap := HelperFrame{Op: "bootstrap", Grant: g, Expected: g.Grant, Key: key, Root: command.Root, PublicRoot: command.PublicRoot, Identity: command.Identity, Window: ownershell.OutputWindow, Custody: command.Custody, HelperID: helperID, ShellCgroup: p.shellCgroup}
	if cleanup != nil {
		if err = p.call(ctx, HelperFrame{Op: "protect"}); err != nil {
			_ = write.Close()
			return p, stageError("bootstrap_validate", nil)
		}
		bootstrap.CleanupKey = cleanup.bootstrapKey()
	}
	if err = p.call(ctx, bootstrap); err != nil {
		_ = write.Close()
		return p, &startError{stage: "bootstrap helper", err: err}
	}
	if p.window.Load() > 0 {
		go p.sendCredit()
	}
	return p, nil
}
func (p *helperPeer) read(in *os.File, diagnostic func(error)) {
	var ready, identityChanged bool
	defer close(p.done)
	defer p.output.Close()
	defer func() { _ = in.Close() }()
	defer func() {
		_ = p.input.Close()
		if !p.retained && p.identityInvalidated != nil && !p.expectedClose.Load() {
			p.identityInvalidated()
		}
		err := p.cmd.Wait()
		provenDead := false
		if p.proveTreeDead != nil {
			provenDead = p.proveTreeDead()
		}
		// PTRACE_O_EXITKILL has no cgroup equivalent, so a helper's death reaches its shell from here.
		var shellLeft error
		if p.shellCgroup != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			shellLeft = emptyShellCgroup(ctx, p.shellCgroup)
			cancel()
		}
		if shellLeft != nil {
			err = shellLeft
		}
		if !p.retained && p.identityInvalidated != nil && (!p.expectedClose.Load() || err != nil) {
			p.identityInvalidated()
		}
		if p.retained && p.cleanupUncertain != nil && (!p.expectedClose.Load() || err != nil) {
			p.cleanupUncertain()
		}
		p.mu.Lock()
		p.clean = shellLeft == nil && (provenDead || err == nil || (p.cmd.ProcessState != nil && p.cmd.ProcessState.ExitCode() == 3))
		reported := p.failure != nil
		failure := p.failure
		if ready && failure == nil && !identityChanged && !p.expectedClose.Load() {
			failure = stageError("helper_stopped", nil)
			p.failure = failure
		}
		p.mu.Unlock()
		if !reported && ready && failure != nil && diagnostic != nil {
			diagnostic(failure)
		}
	}()
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), 65536)
	for scan.Scan() {
		var f HelperFrame
		if json.Unmarshal(scan.Bytes(), &f) != nil {
			return
		}
		if f.Op == "ready" && f.OK {
			ready = true
			// Only a window this daemon offered; anything else stays unwindowed.
			if f.Window > 0 && f.Window <= ownershell.OutputWindow {
				p.window.Store(int64(f.Window))
			}
		}
		if f.Op == "result" && f.Inspected {
			p.inspectedEpoch.Store(f.Epoch)
			p.inspectedActive.Store(f.AccessActive)
		}
		if f.Op == "identity_changed" {
			identityChanged = true
			if p.identityInvalidated != nil {
				p.identityInvalidated()
			}
			continue
		}
		if p.retained && f.Op == "ended" && f.OK {
			p.expectedClose.Store(true)
			continue
		}
		if f.Op == "output" {
			if p.stream != nil {
				if p.stream.receive(f) == nil {
					select {
					case p.creditReady <- struct{}{}:
					default:
					}
				}
				continue
			}
			if len(f.Data) > 16384 {
				return
			}
			// This loop also carries control replies, so it never waits on output.
			// A windowed helper cannot exceed the queue; a full queue here means an
			// older helper without flow control outran the consumer.
			if !p.output.Write(f.Data) {
				return
			}
			continue
		}
		if f.Op == "control_failed" {
			failure := helperFailure(f)
			p.mu.Lock()
			p.failure = failure
			p.mu.Unlock()
			if diagnostic != nil {
				diagnostic(failure)
			}
		}
		// A bootstrap decoder failure may not have recovered the request ID.
		// There is exactly one outstanding first request before startup completes.
		if f.Op == "start_failed" && f.ID == 0 && p.seq.Load() == 1 {
			f.ID = 1
		}
		p.mu.Lock()
		reply := p.pending[f.ID]
		delete(p.pending, f.ID)
		p.mu.Unlock()
		if reply != nil {
			reply <- f
		}
	}
}
func (p *helperPeer) call(ctx context.Context, f HelperFrame) error {
	if p.stream != nil {
		if f.Op == "prepare" {
			p.stream.prepared(f.Transaction, f.Grant.Grant.AccessEpoch)
		}
		if f.Op == "suspend" || f.Op == "close" || f.Op == "freeze" || f.Op == "sync" {
			p.stream.suspend()
		}
	}
	success := func() error {
		if p.stream != nil && f.Op == "commit" {
			p.stream.committed(f.Transaction)
		}
		return nil
	}

	emptyTreeOK := f.Op == "close" || f.Op == "freeze" || f.Op == "thaw"
	if f.Op == "close" {
		p.expectedClose.Store(true)
	}
	if emptyTreeOK {
		select {
		case <-p.done:
			p.mu.Lock()
			clean := p.clean
			p.mu.Unlock()
			if clean {
				return nil
			}
			return stageError("helper_reply", nil)
		default:
		}
	}
	if !emptyTreeOK {
		p.mu.Lock()
		failure := p.failure
		p.mu.Unlock()
		if failure != nil {
			return failure
		}
	}
	f.ID = p.seq.Add(1)
	if f.Op == "close" && p.cleanup != nil {
		f.Cleanup = p.cleanup.request(strconv.FormatUint(f.ID, 10))
	}
	reply := make(chan HelperFrame, 1)
	p.mu.Lock()
	p.pending[f.ID] = reply
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, f.ID); p.mu.Unlock() }()
	p.writeMu.Lock()
	if p.cleanup != nil && f.Op != "bootstrap" && f.Op != "protect" {
		p.controlSeq++
		f.ControlSeq = p.controlSeq
		f.ControlMAC = p.cleanup.controlTag(f)
	}
	data, err := json.Marshal(f)
	if err != nil {
		p.writeMu.Unlock()
		return stageError("helper_reply", nil)
	}
	data = append(data, '\n')
	if p.stream != nil && (f.Op == "input" || f.Op == "resize" || f.Op == "renew") {
		if err := p.stream.permits(f.Grant.Grant.AccessEpoch); err != nil {
			p.writeMu.Unlock()
			return err
		}
	}
	deadline := time.Now().Add(time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = p.input.SetWriteDeadline(deadline)
	_, err = p.input.Write(data)
	p.writeMu.Unlock()
	if err != nil {
		if emptyTreeOK {
			select {
			case <-p.done:
				p.mu.Lock()
				clean := p.clean
				p.mu.Unlock()
				if clean {
					return nil
				}
			case <-ctx.Done():
			}
		}
		return stageError("helper_write", err)
	}
	select {
	case response := <-reply:
		if response.OK {
			return success()
		}
		if response.Op == "start_failed" || response.Op == "control_failed" {
			return helperFailure(response)
		}
		return stageError("helper_reply", nil)
	case <-ctx.Done():
		return stageError("helper_wait", ctx.Err())
	case <-p.done:
		select {
		case response := <-reply:
			if response.OK {
				return success()
			}
			if response.Op == "start_failed" || response.Op == "control_failed" {
				return helperFailure(response)
			}
		default:
		}
		if emptyTreeOK {
			p.mu.Lock()
			clean := p.clean
			p.mu.Unlock()
			if clean {
				return nil
			}
		}
		p.mu.Lock()
		failure := p.failure
		p.mu.Unlock()
		if failure != nil {
			return failure
		}
		return stageError("helper_stopped", nil)
	}
}

func helperFailure(f HelperFrame) error {
	cause := ownershell.SafeStartCause(f.StartCause)
	if cause == "" {
		cause = ownershell.StartCauseUnavailable
	}
	return &helperStartError{cause: cause, stage: ownershell.SafeStartStage(f.StartStage)}
}

// consumed returns n output bytes to the helper's window. Credit goes out in
// batches of a quarter window; the helper only stalls with its whole window
// unacknowledged, so at least that much is always still to be consumed.
func (p *helperPeer) consumed(n int) {
	window := p.window.Load()
	if window == 0 || n <= 0 {
		return
	}
	if p.unacked.Add(int64(n)) >= window/4 {
		select {
		case p.creditReady <- struct{}{}:
		default:
		}
	}
}

// sendCredit writes credit frames on its own goroutine, so a briefly full
// helper input pipe never holds the output forwarder. It ends with the helper.
func (p *helperPeer) sendCredit() {
	if p.stream != nil {
		p.sendRetainedCredit()
		return
	}

	window := p.window.Load()
	retry := time.NewTimer(time.Hour)
	retry.Stop()
	defer retry.Stop()
	for {
		select {
		case <-p.creditReady:
		case <-retry.C:
		case <-p.done:
			return
		}
		n := p.unacked.Load()
		if n < window/4 {
			continue
		}
		data, err := json.Marshal(HelperFrame{Op: "credit", Seq: uint64(n)}) //nolint:gosec // n is a positive byte count
		if err != nil {
			return
		}
		data = append(data, '\n')
		p.writeMu.Lock()
		_ = p.input.SetWriteDeadline(time.Now().Add(time.Second))
		_, err = p.input.Write(data)
		p.writeMu.Unlock()
		if err != nil {
			retry.Reset(50 * time.Millisecond)
			continue
		}
		p.unacked.Add(-n)
	}
}

// readRetained never flattens source positions into a legacy byte pipe. The
// caller must separately validate the current grant at final transport handoff.
func (p *helperPeer) readRetained(ctx context.Context, epoch, offset uint64) (ownershell.HistoryFrame, error) {
	if p.stream == nil {
		return ownershell.HistoryFrame{}, ownershell.ErrDenied
	}
	for {
		frame, err := p.stream.read(epoch, offset)
		if err != nil || len(frame.Data) > 0 {
			return frame, err
		}
		select {
		case <-p.stream.changed:
		case <-p.done:
			frame, err = p.stream.read(epoch, offset)
			if err != nil || len(frame.Data) > 0 {
				return frame, err
			}
			return ownershell.HistoryFrame{}, io.EOF
		case <-ctx.Done():
			return ownershell.HistoryFrame{}, ctx.Err()
		}
	}
}

func (p *helperPeer) sendRetainedCredit() {
	retry := time.NewTimer(time.Hour)
	retry.Stop()
	defer retry.Stop()
	for {
		select {
		case <-p.creditReady:
		case <-retry.C:
		case <-p.done:
			return
		}
		epoch, offset := p.stream.acknowledgment()
		if epoch == 0 {
			continue
		}
		p.writeMu.Lock()
		p.controlSeq++
		f := HelperFrame{Op: "credit", Epoch: epoch, Offset: offset, ControlSeq: p.controlSeq}
		f.ControlMAC = p.cleanup.controlTag(f)
		data, err := json.Marshal(f)
		if err != nil {
			p.writeMu.Unlock()
			return
		}
		_ = p.input.SetWriteDeadline(time.Now().Add(time.Second))
		_, err = p.input.Write(append(data, '\n'))
		p.writeMu.Unlock()
		if err != nil {
			retry.Reset(50 * time.Millisecond)
		}
	}
}

// acknowledgeRetained is called only for a correlated server receipt on the
// active authenticated access epoch, never by the local capacity-return loop.
func (p *helperPeer) acknowledgeRetained(ctx context.Context, epoch, offset uint64) error {
	if p.stream == nil {
		return ownershell.ErrDenied
	}
	receivedEpoch, received := p.stream.acknowledgment()
	if receivedEpoch != epoch || offset > received {
		return ownershell.ErrDenied
	}
	return p.call(ctx, HelperFrame{Op: "ack_output", Epoch: epoch, Offset: offset})
}
