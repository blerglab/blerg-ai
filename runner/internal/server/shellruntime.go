package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

type shellWaiter struct {
	dc      *DaemonConn
	session string
	reply   chan shellruntime.RPC
}
type shellOutput struct {
	dc      *DaemonConn
	session string
	out     chan []byte
}
type shellRPCState struct {
	mu      sync.Mutex
	pending map[string]shellWaiter
	outputs map[string]shellOutput
}

func shellRandom() string {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(data[:])
}
func (h *Hub) shellCall(ctx context.Context, dc *DaemonConn, r shellruntime.RPC) (shellruntime.RPC, error) {
	if ctx.Err() != nil {
		return shellruntime.RPC{}, ctx.Err()
	}
	if dc == nil || h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(r.SessionID) != dc {
		return shellruntime.RPC{}, ownershell.ErrDenied
	}
	r.Type = "owner_shell_request"
	r.Version = 1
	r.RequestID = shellRandom()
	if r.RequestID == "" {
		return shellruntime.RPC{}, ownershell.ErrDenied
	}
	state := &h.shellRPC
	state.mu.Lock()
	if state.pending == nil {
		state.pending = map[string]shellWaiter{}
	}
	if len(state.pending) >= 256 {
		state.mu.Unlock()
		return shellruntime.RPC{}, ownershell.ErrLimit
	}
	wait := make(chan shellruntime.RPC, 1)
	state.pending[r.RequestID] = shellWaiter{dc, r.SessionID, wait}
	state.mu.Unlock()
	defer func() { state.mu.Lock(); delete(state.pending, r.RequestID); state.mu.Unlock() }()
	raw, err := json.Marshal(r)
	if err != nil {
		return shellruntime.RPC{}, err
	}
	lane := dc.shellControl
	if r.Op == "input" || r.Op == "resize" {
		lane = dc.shellData
	}
	select {
	case lane <- raw:
	case <-ctx.Done():
		return shellruntime.RPC{}, ctx.Err()
	}
	select {
	case reply := <-wait:
		if reply.OK {
			return reply, nil
		}
		return reply, ownershell.ErrDenied
	case <-ctx.Done():
		return shellruntime.RPC{}, ctx.Err()
	}
}
func (h *Hub) deliverShell(dc *DaemonConn, r shellruntime.RPC) {
	if r.Version != 1 || h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(r.SessionID) != dc || (dc.BoundSessionID != "" && dc.BoundSessionID != r.SessionID) {
		return
	}
	if r.Type == "owner_shell_phase" {
		if h.shellPhase != nil {
			h.shellPhase(r)
		}
		return
	}
	state := &h.shellRPC
	state.mu.Lock()
	defer state.mu.Unlock()
	if r.Type == "owner_shell_output" {
		entry, ok := state.outputs[r.Shell]
		if !ok || entry.dc != dc || entry.session != r.SessionID {
			return
		}
		if len(r.Data) > 16384 {
			close(entry.out)
			delete(state.outputs, r.Shell)
			return
		}
		select {
		case entry.out <- r.Data:
		default:
			close(entry.out)
			delete(state.outputs, r.Shell)
		}
		return
	}
	if wait, ok := state.pending[r.RequestID]; ok && wait.dc == dc && wait.session == r.SessionID {
		delete(state.pending, r.RequestID)
		wait.reply <- r
	}
}

type remoteShell struct {
	api   *API
	mu    sync.Mutex
	live  map[string]ownershell.Binding
	peers map[string]*DaemonConn
}

func newRemoteShell(a *API) *remoteShell {
	return &remoteShell{api: a, live: map[string]ownershell.Binding{}, peers: map[string]*DaemonConn{}}
}
func (r *remoteShell) describe(ctx context.Context, id string) (shellruntime.RPC, error) {
	dc := r.api.hub.FindDaemonForSession(id)
	if dc == nil || dc.WorkspaceCapabilities.ManifestVersion != protocol.WorkspaceVersion || dc.WorkspaceCapabilities.ShellVersion != 1 {
		return shellruntime.RPC{}, ownershell.ErrDenied
	}
	return r.api.hub.shellCall(ctx, dc, shellruntime.RPC{SessionID: id, Op: "describe"})
}
func consentBinding(b ownershell.Binding, login string, i shellruntime.Identity) db.ShellConsentBinding {
	// HOME is never browser consent authority; it is independently resolved locally.
	raw, _ := json.Marshal(struct {
		Machine, Account, Shell, Cwd, Home string
		UID                                uint64
	}{i.Machine, i.Account, i.Shell, i.Cwd, i.Home, i.UID})
	sum := sha256.Sum256(raw)
	return db.ShellConsentBinding{Owner: b.Owner, Login: login, Session: b.Session, Launch: b.Launch, Connection: b.Connection, Generation: b.RuntimeGeneration, Digest: hex.EncodeToString(sum[:])}
}
func (r *remoteShell) Resolve(ctx context.Context, id string) (ownershell.Binding, error) {
	reply, err := r.describe(ctx, id)
	if err != nil {
		return ownershell.Binding{}, err
	}
	b := reply.Binding
	if b.Runtime == "host" {
		b.HostConsentValid = false
		b.Consent = ""
		consent, err := db.GetShellConsent(ctx, r.api.dbPool, consentBinding(b, "", reply.Identity))
		if err == nil {
			b.HostConsentValid = true
			b.Consent = consent.ID
		}
	}
	return b, nil
}
func (r *remoteShell) Open(ctx context.Context, b ownershell.Binding, g ownershell.SignedGrant, key ed25519.PublicKey) (<-chan []byte, error) {
	dc := r.api.hub.FindDaemonForSession(b.Session)
	if dc == nil {
		return nil, ownershell.ErrDenied
	}
	r.mu.Lock()
	if len(r.live) >= 1024 {
		r.mu.Unlock()
		return nil, ownershell.ErrLimit
	}
	r.live[g.Grant.ShellGeneration] = b
	r.peers[g.Grant.ShellGeneration] = dc
	r.mu.Unlock()
	config := shellruntime.RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key, Policy: protocol.ShellPolicy{Version: 1, Revision: g.Grant.PolicyRevision, Enabled: true}}
	if b.Runtime == "host" {
		description, err := r.describe(ctx, b.Session)
		if err != nil {
			return nil, err
		}
		consent, err := db.GetShellConsent(ctx, r.api.dbPool, consentBinding(b, g.Grant.LoginID, description.Identity))
		if err != nil || consent.ID != b.Consent {
			return nil, ownershell.ErrDenied
		}
		config.Consent = &consent
	}
	if _, err := r.api.hub.shellCall(ctx, dc, config); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	state := &r.api.hub.shellRPC
	state.mu.Lock()
	if state.outputs == nil {
		state.outputs = map[string]shellOutput{}
	}
	out := make(chan []byte, 8)
	state.outputs[g.Grant.ShellGeneration] = shellOutput{dc, b.Session, out}
	state.mu.Unlock()
	if _, err := r.api.hub.shellCall(ctx, dc, shellruntime.RPC{SessionID: b.Session, Op: "open", Grant: g}); err != nil {
		return nil, err
	}
	return out, nil
}
func (r *remoteShell) call(ctx context.Context, g ownershell.SignedGrant, frame shellruntime.RPC) (shellruntime.RPC, error) {
	r.mu.Lock()
	b, ok := r.live[g.Grant.ShellGeneration]
	dc := r.peers[g.Grant.ShellGeneration]
	r.mu.Unlock()
	if !ok {
		return shellruntime.RPC{}, ownershell.ErrDenied
	}
	frame.SessionID = b.Session
	frame.Grant = g
	return r.api.hub.shellCall(ctx, dc, frame)
}
func (r *remoteShell) Renew(ctx context.Context, g ownershell.SignedGrant) error {
	_, e := r.call(ctx, g, shellruntime.RPC{Op: "renew"})
	return e
}
func (r *remoteShell) Input(ctx context.Context, g ownershell.SignedGrant, seq uint64, data []byte) error {
	_, e := r.call(ctx, g, shellruntime.RPC{Op: "input", Seq: seq, Data: data})
	return e
}
func (r *remoteShell) Resize(ctx context.Context, g ownershell.SignedGrant, seq uint64, cols, rows uint16) error {
	_, e := r.call(ctx, g, shellruntime.RPC{Op: "resize", Seq: seq, Cols: cols, Rows: rows})
	return e
}
func (r *remoteShell) Close(ctx context.Context, g ownershell.SignedGrant) error {
	reply, err := r.call(ctx, g, shellruntime.RPC{Op: "close"})
	if err != nil {
		return err
	}
	r.mu.Lock()
	b := r.live[g.Grant.ShellGeneration]
	r.mu.Unlock()
	identity := ownershell.SupervisorIdentity(b, g)
	if reply.Cleanup == nil || reply.Cleanup.ValidateFor(identity.Launch, identity.ShellGeneration) != nil {
		return ownershell.ErrDenied
	}
	state := &r.api.hub.shellRPC
	state.mu.Lock()
	if entry, ok := state.outputs[g.Grant.ShellGeneration]; ok {
		close(entry.out)
		delete(state.outputs, g.Grant.ShellGeneration)
	}
	state.mu.Unlock()
	r.mu.Lock()
	delete(r.live, g.Grant.ShellGeneration)
	delete(r.peers, g.Grant.ShellGeneration)
	r.mu.Unlock()
	return nil
}

func (r *remoteShell) AdmitShell(ctx context.Context, s protocol.SupervisorShell) error {
	if s.Validate() != nil {
		return ownershell.ErrDenied
	}
	_, err := r.api.hub.shellCall(ctx, r.api.hub.FindDaemonForSession(s.SessionID), shellruntime.RPC{SessionID: s.SessionID, Op: "admit", Launch: s.Launch, Shell: s.ShellGeneration})
	return err
}
func (r *remoteShell) Notify(ctx context.Context, l protocol.SupervisorLaunch, p protocol.SupervisorPhase) error {
	if l.Validate() != nil || !p.Valid() {
		return ownershell.ErrDenied
	}
	if r.api.shells != nil {
		r.api.shells.NotifyRuntimePhase(ctx, l, string(p))
	}
	_, err := r.api.hub.shellCall(ctx, r.api.hub.FindDaemonForSession(l.SessionID), shellruntime.RPC{SessionID: l.SessionID, Op: "phase", Launch: l, Phase: p})
	return err
}
func (r *remoteShell) CloseShell(ctx context.Context, s protocol.SupervisorShell, _ string) (protocol.SupervisorCleanup, error) {
	if s.Validate() != nil {
		return protocol.SupervisorCleanup{}, ownershell.ErrDenied
	}
	reply, err := r.api.hub.shellCall(ctx, r.api.hub.FindDaemonForSession(s.SessionID), shellruntime.RPC{SessionID: s.SessionID, Op: "close_shell", Launch: s.Launch, Shell: s.ShellGeneration})
	if err != nil || reply.Cleanup == nil {
		return protocol.SupervisorCleanup{}, ownershell.ErrDenied
	}
	return *reply.Cleanup, reply.Cleanup.ValidateFor(s.Launch, s.ShellGeneration)
}
func (r *remoteShell) CloseLaunch(ctx context.Context, l protocol.SupervisorLaunch, _ string) (protocol.SupervisorCleanup, error) {
	return r.launchCall(ctx, l, "close_launch")
}
func (r *remoteShell) RecoverLaunch(ctx context.Context, l protocol.SupervisorLaunch) (protocol.SupervisorCleanup, error) {
	return r.launchCall(ctx, l, "recover")
}
func (r *remoteShell) launchCall(ctx context.Context, l protocol.SupervisorLaunch, op string) (protocol.SupervisorCleanup, error) {
	if l.Validate() != nil {
		return protocol.SupervisorCleanup{}, ownershell.ErrDenied
	}
	reply, err := r.api.hub.shellCall(ctx, r.api.hub.FindDaemonForSession(l.SessionID), shellruntime.RPC{SessionID: l.SessionID, Op: op, Launch: l})
	if err != nil || reply.Cleanup == nil {
		return protocol.SupervisorCleanup{}, ownershell.ErrDenied
	}
	return *reply.Cleanup, reply.Cleanup.ValidateFor(l, "")
}
func (a *API) RuntimeShellSupervisor() protocol.RuntimeSupervisor {
	if a.shellRuntime == nil {
		return nil
	}
	return a.shellRuntime
}

var _ protocol.RuntimeSupervisor = (*remoteShell)(nil)

func (h *Hub) abandonShellRequests(dc *DaemonConn) {
	state := &h.shellRPC
	state.mu.Lock()
	defer state.mu.Unlock()
	for id, wait := range state.pending {
		if wait.dc == dc {
			delete(state.pending, id)
			wait.reply <- shellruntime.RPC{SessionID: wait.session, RequestID: id}
		}
	}
	for id, entry := range state.outputs {
		if entry.dc == dc {
			close(entry.out)
			delete(state.outputs, id)
		}
	}
}
