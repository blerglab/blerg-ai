package server

import (
	"context"
	"crypto/ed25519"
	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

// All mutations, including channel close, hold shellRPC.mu. A full queue
// refuses access without blocking the shared daemon reader or killing custody.
type retainedRemoteOutput struct {
	dc             *DaemonConn
	session, shell string
	epoch          uint64
	output         chan ownershell.PersistentFrame
	closed         bool
}

func (h *Hub) deliverRetainedOutput(dc *DaemonConn, r shellruntime.RPC) {
	state := &h.shellRPC
	state.mu.Lock()
	defer state.mu.Unlock()
	if r.Custody == nil || r.Custody.Validate() != nil || r.Custody.Session != r.SessionID || r.Custody.Shell != r.Shell {
		return
	}
	entry := state.retained[r.Custody.Digest()]
	if entry == nil || entry.closed || entry.dc != dc || entry.session != r.SessionID || entry.shell != r.Shell || entry.epoch != r.Epoch {
		return
	}
	if len(r.Data) > ownershell.MaxFrame {
		close(entry.output)
		entry.closed = true
		return
	}
	frame := ownershell.PersistentFrame{HistoryFrame: ownershell.HistoryFrame{Offset: r.Offset, End: r.End, Gap: r.Gap, Data: append([]byte(nil), r.Data...)}, Epoch: r.Epoch, Ended: r.Ended, Cause: ownershell.SafeStartCause(r.StartCause)}
	select {
	case entry.output <- frame:
	default:
		close(entry.output)
		entry.closed = true
	}
}
func (r *remoteShell) installRetainedOutput(dc *DaemonConn, b ownershell.Binding, g ownershell.SignedGrant) <-chan ownershell.PersistentFrame {
	state := &r.api.hub.shellRPC
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.retained == nil {
		state.retained = map[string]*retainedRemoteOutput{}
	}
	if old := state.retained[g.Grant.CustodyID]; old != nil && !old.closed {
		close(old.output)
		old.closed = true
	}
	entry := &retainedRemoteOutput{dc: dc, session: b.Session, shell: g.Grant.ShellGeneration, epoch: g.Grant.AccessEpoch, output: make(chan ownershell.PersistentFrame, ownershell.ServerOutputWindow/ownershell.MaxFrame)}
	state.retained[g.Grant.CustodyID] = entry
	return entry.output
}
func (r *remoteShell) persistentCall(ctx context.Context, b ownershell.Binding, request shellruntime.RPC) (shellruntime.RPC, error) {
	dc := r.api.hub.FindDaemonForSession(b.Session)
	if dc == nil {
		return shellruntime.RPC{}, ownershell.ErrUnknown
	}
	request.Version = 2
	request.SessionID = b.Session
	reply, err := r.api.hub.shellCall(ctx, dc, request)
	if err != nil && (reply.Type != "owner_shell_result" || reply.Version != 2 || reply.ErrorCode == "" || ownershell.RuntimeReason(reply.ErrorCode) == ownershell.ReasonUnavailable) {
		return reply, ownershell.ErrUnknown
	}
	return reply, err
}
func (r *remoteShell) OpenCustody(ctx context.Context, b ownershell.Binding, c protocol.ShellCustody, g ownershell.SignedGrant, key ed25519.PublicKey) (<-chan ownershell.PersistentFrame, error) {
	dc := r.api.hub.FindDaemonForSession(b.Session)
	if dc == nil {
		return nil, ownershell.ErrUnknown
	}
	if c.Runtime == "host" {
		r.mu.Lock()
		consent := r.consents[c.Consent]
		valid := consent != nil && consent.accepted && consent.consumed && !consent.withdrawn && consent.value.Allows(c)
		r.mu.Unlock()
		if !valid {
			return nil, ownershell.Refuse(ownershell.ReasonRuntimeConsent)
		}
	}
	r.mu.Lock()
	if len(r.live) >= 1024 {
		r.mu.Unlock()
		return nil, ownershell.ErrLimit
	}
	r.live[c.Shell] = b
	r.peers[c.Shell] = dc
	r.mu.Unlock()
	_, err := r.persistentCall(ctx, b, shellruntime.RPC{Op: "configure", Binding: b, Key: key, Policy: protocol.ShellPolicy{Version: 1, Enabled: true, Revision: g.Grant.PolicyRevision}})
	if err != nil {
		return nil, err
	}
	out := r.installRetainedOutput(dc, b, g)
	reply, err := r.persistentCall(ctx, b, shellruntime.RPC{Op: "open", Custody: &c, Grant: g, Window: ownershell.ServerOutputWindow})
	if err != nil && reply.Type == "owner_shell_result" && reply.Version == 2 && reply.Op == "open" && reply.SessionID == b.Session && !reply.OK && reply.ErrorCode == string(ownershell.ReasonRuntimeBinding) && reply.AllocationRefused {
		state := &r.api.hub.shellRPC
		state.mu.Lock()
		if entry := state.retained[c.Digest()]; entry != nil && !entry.closed {
			close(entry.output)
			entry.closed = true
		}
		delete(state.retained, c.Digest())
		state.mu.Unlock()
		r.mu.Lock()
		delete(r.live, c.Shell)
		delete(r.peers, c.Shell)
		r.mu.Unlock()
		return nil, ownershell.RefuseBeforeAllocation(err)
	}
	return out, err
}
func (r *remoteShell) SuspendCustody(ctx context.Context, b ownershell.Binding) error {
	state := &r.api.hub.shellRPC
	state.mu.Lock()
	for _, entry := range state.retained {
		if entry.session == b.Session && !entry.closed {
			close(entry.output)
			entry.closed = true
		}
	}
	state.mu.Unlock()
	_, err := r.persistentCall(ctx, b, shellruntime.RPC{Op: "suspend"})
	return err
}
func (r *remoteShell) InventoryCustody(ctx context.Context, b ownershell.Binding, v ownershell.CustodyInventory) (ownershell.CustodyInventory, error) {
	reply, err := r.persistentCall(ctx, b, shellruntime.RPC{Op: "inventory", Inventory: &v})
	if err != nil {
		return ownershell.CustodyInventory{}, err
	}
	if reply.Inventory == nil {
		return ownershell.CustodyInventory{}, ownershell.ErrUnknown
	}
	return *reply.Inventory, nil
}
func (r *remoteShell) PrepareCustody(ctx context.Context, b ownershell.Binding, tx string, v ownershell.CustodyInventory, key ed25519.PublicKey, gs []ownershell.SignedGrant) error {
	for _, g := range gs {
		if b.Runtime != "host" {
			continue
		}
		r.mu.Lock()
		consent := r.consents[g.Grant.ConsentID]
		valid := consent != nil && consent.accepted && consent.consumed && !consent.withdrawn && consentMatchesBinding(consent.value, ownershell.Actor{Kind: "human", Owner: g.Grant.OwnerID, Login: g.Grant.LoginID}, b)
		r.mu.Unlock()
		if !valid {
			return ownershell.ErrDenied
		}
		stored, err := db.ReadPersistentShellConsent(ctx, r.api.dbPool, g.Grant.OwnerID, g.Grant.LoginID, b.Session, g.Grant.ConsentID)
		if err != nil {
			return persistentConsentReadError(err)
		}
		if stored != consent.value {
			return ownershell.ErrDenied
		}
	}
	_, err := r.persistentCall(ctx, b, shellruntime.RPC{Op: "prepare", Binding: b, Transaction: tx, Inventory: &v, Key: key, Grants: gs})
	return err
}
func (r *remoteShell) CommitCustody(ctx context.Context, b ownershell.Binding, tx string, gs []ownershell.SignedGrant) (map[string]<-chan ownershell.PersistentFrame, error) {
	dc := r.api.hub.FindDaemonForSession(b.Session)
	if dc == nil {
		return nil, ownershell.ErrUnknown
	}
	streams := map[string]<-chan ownershell.PersistentFrame{}
	for _, g := range gs {
		streams[g.Grant.CustodyID] = r.installRetainedOutput(dc, b, g)
	}
	_, err := r.persistentCall(ctx, b, shellruntime.RPC{Op: "commit", Transaction: tx})
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	for _, g := range gs {
		r.live[g.Grant.ShellGeneration] = b
		r.peers[g.Grant.ShellGeneration] = dc
	}
	r.mu.Unlock()
	return streams, nil
}
func (r *remoteShell) CloseCustody(ctx context.Context, c protocol.ShellCustody, tx string) (protocol.CustodyCleanup, error) {
	// The stable custody, not an expired grant or a replacement connection nonce,
	// authorizes cleanup. The runtime must return the exact physical proof.
	reply, err := r.persistentCall(ctx, ownershell.Binding{Session: c.Session}, shellruntime.RPC{Op: "close", Custody: &c, Transaction: tx})
	if err != nil {
		return protocol.CustodyCleanup{}, err
	}
	dc := r.api.hub.FindDaemonForSession(c.Session)
	responder := c.Daemon
	var link *protocol.ShellLinkWelcome
	if dc != nil {
		link = dc.shellLink()
	}
	if link != nil && link.Desktop.OwnerID == c.Owner && link.Desktop.DesktopID == c.Desktop {
		responder = link.Daemon
	}
	if reply.CustodyCleanup == nil || reply.CustodyCleanup.ValidateFor(c, tx, responder) != nil {
		return protocol.CustodyCleanup{}, ownershell.ErrUnknown
	}
	state := &r.api.hub.shellRPC
	state.mu.Lock()
	if entry := state.retained[c.Digest()]; entry != nil && !entry.closed {
		close(entry.output)
		entry.closed = true
	}
	delete(state.retained, c.Digest())
	state.mu.Unlock()
	r.mu.Lock()
	if consent := r.consents[c.Consent]; consent != nil && consent.value.Allows(c) {
		consent.cleanupComplete = true
	}
	delete(r.live, c.Shell)
	delete(r.peers, c.Shell)
	r.mu.Unlock()
	return *reply.CustodyCleanup, nil
}
func (r *remoteShell) ReceiptCustody(ctx context.Context, g ownershell.SignedGrant, offset uint64) error {
	if _, err := r.call(ctx, g, shellruntime.RPC{Op: "credit", Offset: offset}); err != nil {
		return err
	}
	_, err := r.call(ctx, g, shellruntime.RPC{Op: "receipt", Offset: offset})
	return err
}

// A daemon claim never supplies enrollment or the runner's process incarnation.
func (h *Hub) personalShellWelcome(hello *protocol.ShellLinkHello, enrollment *desktopauth.Binding) *protocol.ShellLinkWelcome {
	h.mu.RLock()
	runner := h.shellCustodian
	h.mu.RUnlock()
	if hello == nil || enrollment == nil || runner == "" {
		return nil
	}
	welcome := &protocol.ShellLinkWelcome{Version: 2, Runner: runner, Daemon: hello.Daemon, Challenge: hello.Challenge, Desktop: *enrollment}
	if !welcome.Matches(*hello, time.Now()) {
		return nil
	}
	return welcome
}

func (dc *DaemonConn) shellLink() *protocol.ShellLinkWelcome {
	if link := dc.refreshedShellLink.Load(); link != nil {
		return link
	}
	return dc.ShellLink
}
