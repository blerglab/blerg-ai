package shellruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// RuntimeIdentityDigest excludes the display hostname. The enrolled desktop and
// runtime incarnation, not a mutable name, provide machine identity.
func RuntimeIdentityDigest(identity Identity) string {
	identity.Machine = ""
	raw, _ := json.Marshal(identity)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func newHelperID() (string, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", ErrClosed
	}
	return hex.EncodeToString(nonce[:]), nil
}

func servePersistentHelper(boot HelperFrame, actual Identity, publicRoot string, decode func(*HelperFrame) error, out io.Writer, resolve func(string) (Identity, error), clockFactory func() (ownershell.LeaseClock, error), started *bool) (result error) {
	custody := boot.Custody
	if custody == nil || custody.Runtime != "host" || boot.Expected.Runtime != "host" || custody.Container != "" || custody.Validate() != nil || custody.Digest() != boot.Expected.CustodyID || custody.Shell != boot.Expected.ShellGeneration || custody.Owner != boot.Expected.OwnerID || custody.Login != boot.Expected.LoginID || custody.Session != boot.Expected.SessionID || custody.Launch != boot.Expected.LaunchGeneration || custody.Privileged != boot.Expected.Privileged || custody.Runtime != boot.Expected.Runtime || custody.Root != boot.Root || custody.Consent != boot.Expected.ConsentID || custody.Identity != RuntimeIdentityDigest(actual) {
		return stageError("verify_binding", nil)
	}
	cleanup, err := cleanupAuthorityFromBootstrap(custody.Digest(), boot.HelperID, boot.CleanupKey)
	if err != nil {
		return stageError("bootstrap_validate", nil)
	}
	clock, err := clockFactory()
	if err != nil {
		return stageError("lease_create", nil)
	}
	policy := protocol.ShellPolicy{Version: 1, Revision: boot.Expected.PolicyRevision, Enabled: true}
	if _, err = ownershell.NewPersistentLease(boot.Key, boot.Grant, boot.Expected, policy, time.Unix(custody.LifetimeUntil, 0), clock, func() error { return nil }); err != nil {
		return stageError("lease_create", nil)
	}
	process, err := startShell(actual, boot.Root, publicRoot, boot.ShellCgroup)
	if err != nil {
		return err
	}
	stop := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return process.stop(ctx)
	}
	defer func() {
		if stop() != nil {
			result = stageError("cleanup_shell", nil)
		}
	}()
	lease, err := ownershell.NewPersistentLease(boot.Key, boot.Grant, boot.Expected, policy, time.Unix(custody.LifetimeUntil, 0), clock, stop)
	if err != nil {
		return stageError("lease_create", nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	history := ownershell.NewHistory(ownershell.ScrollbackBytes)
	var sendMu sync.Mutex
	epoch := boot.Expected.AccessEpoch
	var controlSeq uint64
	cursor := uint64(0)
	negotiatedWindow := min(boot.Window, maxHelperWindow)
	if negotiatedWindow == 0 {
		negotiatedWindow = ownershell.OutputWindow
	}
	window := uint64(negotiatedWindow)
	credit := ownershell.NewStreamCredit(window, 0)
	controls := make(chan HelperFrame, 16)
	outputs := make(chan HelperFrame, 8)
	written := make(chan uint64, 16)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		enc := json.NewEncoder(out)
		for {
			var f HelperFrame
			select {
			case f = <-controls:
			default:
				select {
				case f = <-controls:
				case f = <-outputs:
				case <-ctx.Done():
					return
				}
			}
			if f.Op == "output" {
				sendMu.Lock()
				allowed := lease.CheckEpoch(f.Epoch) == nil && f.Epoch == epoch && credit.Committed(f.Offset, len(f.Data)) == nil
				if allowed {
					_ = history.Sent(f.Offset + uint64(len(f.Data)))
				}
				sendMu.Unlock()
				if !allowed {
					continue
				}
			}
			if enc.Encode(f) != nil {
				cancel()
				return
			}
			if f.Op == "closed" || f.Op == "ended" {
				select {
				case written <- f.ID:
				default:
				}
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
			lease.Suspend()
			return false
		}
	}
	finish := func(f HelperFrame) error {
		if !sendControl(f) {
			return stageError("control_write", nil)
		}
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			select {
			case id := <-written:
				if id == f.ID {
					return nil
				}
			case <-writerDone:
				return stageError("control_write", nil)
			case <-timer.C:
				return stageError("control_write", nil)
			}
		}
	}
	changed := make(chan struct{}, 1)
	notify := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	captureDone := make(chan struct{})
	streamEnded := (<-chan struct{})(captureDone)
	go func() {
		defer close(captureDone)
		for {
			select {
			case data, ok := <-process.output:
				if !ok {
					return
				}
				if history.Append(data) != nil {
					_ = lease.Close()
					return
				}
				notify()
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-changed:
			case <-tick.C:
			case <-ctx.Done():
				return
			}
			sendMu.Lock()
			if lease.CheckEpoch(epoch) != nil || credit.Available() == 0 {
				sendMu.Unlock()
				continue
			}
			frame, readErr := history.Read(cursor, int(min(credit.Available(), uint64(ownershell.MaxFrame))))
			if readErr != nil || len(frame.Data) == 0 {
				sendMu.Unlock()
				continue
			}
			f := HelperFrame{Op: "output", Epoch: epoch, Offset: frame.Offset, End: frame.End, Gap: frame.Gap, Data: frame.Data}
			if len(outputs) < cap(outputs) && credit.Reserve(frame.Offset, len(frame.Data), frame.Gap) {
				outputs <- f
				cursor = frame.Offset + uint64(len(frame.Data))
			}
			sendMu.Unlock()
		}
	}()
	terminal := make(chan struct{}, 1)
	go func() {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				lease.Tick()
				if lease.Closed() {
					select {
					case terminal <- struct{}{}:
					default:
					}
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Reported before the lease closes: a closed lease lets the helper finish, and a report queued
	// after "ended" is never written. Every access operation checks, so once is enough, and a full
	// queue leaves it to the next check.
	var identityMu sync.Mutex
	identityReported := false
	reportIdentityChange := func() {
		identityMu.Lock()
		defer identityMu.Unlock()
		if !identityReported {
			identityReported = sendControl(HelperFrame{Op: "identity_changed"})
		}
	}
	verifyIdentity := func() error {
		rootErr := persistentRootError(publicRoot, boot.Root)
		memberErr := persistentRootError(actual.Cwd, actual.MemberRoot)
		knownChange := (rootErr != nil && !ownershell.Unknown(rootErr)) || (memberErr != nil && !ownershell.Unknown(memberErr))
		current, resolveErr := resolve(actual.Cwd)
		if !knownChange && (ownershell.Unknown(rootErr) || ownershell.Unknown(memberErr) || resolveErr != nil) {
			lease.Suspend()
			return ownershell.ErrUnknown
		}
		if knownChange || RuntimeIdentityDigest(current) != RuntimeIdentityDigest(actual) {
			reportIdentityChange()
			_ = lease.Close()
			return ownershell.ErrDenied
		}
		return nil
	}
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				if err := verifyIdentity(); err != nil && lease.Closed() {
					select {
					case terminal <- struct{}{}:
					default:
					}
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	requests := make(chan HelperFrame, 1)
	go func() {
		defer close(requests)
		for {
			var f HelperFrame
			if decode(&f) != nil {
				return
			}
			if len(f.Data) > ownershell.MaxFrame {
				continue
			}
			select {
			case requests <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	if !sendControl(HelperFrame{Op: "ready", ID: boot.ID, OK: true, Window: negotiatedWindow}) {
		return stageError("helper_ready", nil)
	}
	*started = true
	var inputSeq, resizeSeq uint64
	var prepared HelperFrame
	var committed string
	naturalEnded := false
	drainTick := time.NewTicker(50 * time.Millisecond)
	defer drainTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return stageError("control_write", nil)
		case <-terminal:
			if lease.Close() != nil {
				return stageError("cleanup_shell", nil)
			}
			return finish(HelperFrame{Op: "ended", OK: true})
		case <-streamEnded:
			// Reap all descendants now, while keeping access authorization and
			// volatile history available for the final private-buffer handoff.
			if stop() != nil {
				return stageError("cleanup_shell", nil)
			}
			naturalEnded = true
			streamEnded = nil
		case <-drainTick.C:
			if !naturalEnded {
				continue
			}
			sendMu.Lock()
			_, end := history.Bounds()
			drained := credit.Acknowledged() >= end
			sendMu.Unlock()
			if !drained {
				continue
			}
			if lease.Close() != nil {
				return stageError("cleanup_shell", nil)
			}
			return finish(HelperFrame{Op: "ended", OK: true})
		case f, ok := <-requests:
			if !ok {
				return stageError("control_read", nil)
			}
			var operationErr error
			if !cleanup.verifyControl(f) || f.ControlSeq <= controlSeq {
				operationErr = ownershell.ErrDenied
			} else {
				controlSeq = f.ControlSeq
			}

			inspected, accessActive := false, false
			accessOp := f.Op == "inspect" || f.Op == "prepare" || f.Op == "commit" || f.Op == "renew" || f.Op == "input" || f.Op == "resize" || f.Op == "credit" || f.Op == "ack_output"
			if accessOp && operationErr == nil {
				operationErr = verifyIdentity()
			}
			if operationErr == nil {
				switch f.Op {
				case "inspect":
					inspected = true
					accessActive = lease.Check() == nil
					if lease.Closed() {
						operationErr = ownershell.ErrDenied
					}

				case "close":
					if !cleanup.verify(f.Cleanup) {
						operationErr = ownershell.ErrDenied
						break
					}
					operationErr = lease.Close()
					if operationErr != nil {
						return stageError("cleanup_shell", nil)
					}
					return finish(HelperFrame{Op: "closed", ID: f.ID, OK: true})
				case "sync":
					if !custody.Privileged {
						operationErr = ownershell.ErrDenied
						break
					}
					operationErr = lease.FreezeUntil(time.Unix(f.SyncUntil, 0))
				case "freeze":
					operationErr = lease.FreezeUntil(time.Unix(f.SyncUntil, 0))
					if operationErr == nil {
						call, stopCall := context.WithTimeout(ctx, time.Second)
						operationErr = process.freeze(call)
						stopCall()
						if operationErr != nil {
							_ = lease.Close()
						}
					}
				case "thaw":
					call, stopCall := context.WithTimeout(ctx, time.Second)
					operationErr = process.thaw(call)
					stopCall()
					if operationErr == nil {
						operationErr = lease.Thawed()
					} else {
						_ = lease.Close()
					}
				case "suspend":
					sendMu.Lock()
					if f.Epoch == epoch {
						lease.Suspend()
					} else {
						operationErr = ownershell.ErrDenied
					}
					sendMu.Unlock()
				case "prepare":
					operationErr = lease.Prepare(f.Transaction, f.Key, f.Grant)
					if operationErr == nil {
						prepared = f
					}
				case "commit":
					sendMu.Lock()
					operationErr = lease.Commit(f.Transaction)
					if operationErr == nil && committed != f.Transaction {
						epoch = prepared.Grant.Grant.AccessEpoch
						cursor = history.Acknowledged()
						credit = ownershell.NewStreamCredit(window, cursor)
						inputSeq = 0
						resizeSeq = 0
						committed = f.Transaction
						notify()
					}
					sendMu.Unlock()
				case "renew":
					operationErr = lease.Renew(f.Grant)
				case "credit":
					sendMu.Lock()
					if lease.CheckEpoch(f.Epoch) != nil || credit.Acknowledge(f.Offset) != nil {
						operationErr = ownershell.ErrDenied
					} else {
						notify()
					}
					sendMu.Unlock()
				case "ack_output":
					// A capacity return proves daemon receipt only. Retention/replay
					// advances only on this separate server-committed receipt.
					if lease.CheckEpoch(f.Epoch) != nil {
						operationErr = ownershell.ErrDenied
					} else {
						operationErr = history.Acknowledge(f.Offset)
					}
				case "input":
					if f.Seq != inputSeq+1 || len(f.Data) == 0 {
						operationErr = ownershell.ErrDenied
						break
					}
					operationErr = lease.Access(f.Grant, func() error { inputSeq++; return nil })
					if operationErr == nil {
						deadline := time.Now().Add(time.Second)
						for data := f.Data; len(data) > 0; {
							count := 0
							operationErr = lease.Access(f.Grant, func() error { var writeErr error; count, writeErr = process.tryInput(data); return writeErr })
							if count > 0 {
								data = data[count:]
							}
							if len(data) == 0 {
								break
							}
							if time.Now().After(deadline) {
								operationErr = ownershell.ErrDenied
								break
							}
							if errors.Is(operationErr, syscall.EAGAIN) || operationErr == nil {
								select {
								case <-time.After(5 * time.Millisecond):
									continue
								case <-ctx.Done():
									operationErr = ownershell.ErrDenied
								}
							}
							break
						}
					}
				case "resize":
					if f.Seq != resizeSeq+1 {
						operationErr = ownershell.ErrDenied
						break
					}
					operationErr = lease.Access(f.Grant, func() error { resizeSeq++; return process.resize(f.Cols, f.Rows) })
				default:
					operationErr = ownershell.ErrDenied
				}
			}
			if !sendControl(HelperFrame{Op: "result", ID: f.ID, OK: operationErr == nil, Inspected: inspected, AccessActive: accessActive, Epoch: epoch}) && ctx.Err() != nil {
				return stageError("control_write", nil)
			}
		}
	}
}
