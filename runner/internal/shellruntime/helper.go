package shellruntime

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
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
	Cols, Rows uint16
	OK         bool `json:"ok,omitempty"`
}

// ServeHelper must run in its own process: kernel tracing and orphan reaping
// cannot share the agent/daemon's child-reaper namespace or output writer.
func ServeHelper(in io.Reader, out io.Writer) error { return serveHelper(in, out, ResolveIdentity) }

// ServeClusterHelper is a trusted image entry point, never selected by RPC.
// Its tracer stays at the supervisor UID; the PTY executes as the agent UID.
func ServeClusterHelper(in io.Reader, out io.Writer) error {
	return serveHelper(in, out, func(cwd string) (Identity, error) { return resolveAccountIdentity(cwd, 1000) })
}

func serveHelper(in io.Reader, out io.Writer, resolve func(string) (Identity, error)) (result error) {
	var identityChanged atomic.Bool
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), 65536)
	decode := func(f *HelperFrame) error {
		if !scan.Scan() {
			return ErrClosed
		}
		return json.Unmarshal(scan.Bytes(), f)
	}
	var boot HelperFrame
	if err := decode(&boot); err != nil {
		return ErrClosed
	}
	if boot.Op != "bootstrap" || len(boot.Key) != ed25519.PublicKeySize {
		return ErrClosed
	}
	publicRoot := boot.PublicRoot
	if publicRoot == "" {
		publicRoot = boot.Identity.Cwd
	}
	publicRoot, rootErr := filepath.EvalSymlinks(publicRoot)
	if rootErr != nil {
		return ErrClosed
	}
	actual, err := resolve(boot.Identity.Cwd)
	if err != nil || (boot.Expected.Runtime == "host" && actual != boot.Identity) || actual.Cwd != boot.Identity.Cwd || !matchesRoot(publicRoot, boot.Root) {
		return ErrClosed
	}
	expected := boot.Expected
	if ownershell.VerifyGrant(boot.Key, boot.Grant, expected, expected.PolicyRevision, time.Now()) != nil {
		return ErrClosed
	}
	process, err := startTraced(actual, boot.Root, publicRoot)
	if err != nil {
		return ErrClosed
	}
	stop := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return process.stop(ctx)
	}
	defer func() {
		if stop() == nil {
			result = nil
			if identityChanged.Load() {
				result = ErrIdentityChanged
			}
		} else {
			result = ErrClosed
		}
	}()
	lease, err := ownershell.NewRuntimeLease(boot.Key, boot.Grant, expected, protocol.ShellPolicy{Version: 1, Revision: expected.PolicyRevision, Enabled: true}, nil, stop)
	if err != nil {
		return ErrClosed
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controls := make(chan HelperFrame, 16)
	outputs := make(chan HelperFrame, 8)
	writeDone := make(chan struct{})
	written := make(chan uint64, 1)
	identityWritten := make(chan struct{}, 1)
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
				case <-ctx.Done():
					return
				}
			}
			if enc.Encode(frame) != nil {
				cancel()
				return
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
	if !sendControl(HelperFrame{Op: "ready", ID: boot.ID, OK: true}) {
		return ErrClosed
	}
	go func() {
		for {
			select {
			case data, ok := <-process.output:
				if !ok {
					if !closing.Load() {
						cancel()
					}
					return
				}
				if lease.Check() != nil {
					if !closing.Load() {
						cancel()
					}
					return
				}
				select {
				case outputs <- HelperFrame{Op: "output", Data: data}:
				default:
					_ = lease.Close()
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
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
			return ErrClosed
		case f, ok := <-requests:
			if !ok {
				return ErrClosed
			}
			if f.Op == "close" {
				// Revocation remains usable after expiry, but only for this signed identity.
				valid := ownershell.VerifyGrant(boot.Key, f.Grant, expected, expected.PolicyRevision, time.Unix(f.Grant.Grant.IssuedAt, 0)) == nil
				if !valid {
					return ErrClosed
				}
				closing.Store(true)
				err = lease.Close()
				if !sendControl(HelperFrame{Op: "closed", ID: f.ID, OK: err == nil}) {
					return ErrClosed
				}
				// Wait for the actual control ack write, never an arbitrary sleep.
				for {
					select {
					case id := <-written:
						if id == f.ID {
							return err
						}
					case <-writeDone:
						return ErrClosed
					case <-ctx.Done():
						return ErrClosed
					}
				}
			}
			if lease.Check() != nil {
				return ErrClosed
			}
			valid := ownershell.VerifyGrant(boot.Key, f.Grant, expected, expected.PolicyRevision, time.Now()) == nil
			if !valid {
				return ErrClosed
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
					return ErrClosed
				}
				err = process.input(f.Data)
				if err == nil {
					inputSeq = f.Seq
				}
			case "resize":
				if f.Seq != resizeSeq+1 {
					return ErrClosed
				}
				err = process.resize(f.Cols, f.Rows)
				if err == nil {
					resizeSeq = f.Seq
				}
			default:
				return ErrClosed
			}
			if !sendControl(HelperFrame{Op: "ack", ID: f.ID, OK: err == nil}) {
				return ErrClosed
			}
			if err != nil {
				return ErrClosed
			}
		}
	}
}
