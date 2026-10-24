package server

import "context"
import "net/http"
import "time"
import "github.com/blerglab/blerg-ai/runner/internal/shellruntime"
import "github.com/blerglab/blerg-ai/runner/internal/protocol"

// RuntimeShellGateVerified stays false until all real-runtime lifecycle and
// transport security gates have passed. There is deliberately no environment
// override or administrator bypass for an incomplete implementation.
const RuntimeShellGateVerified = false

func (a *API) StartRuntimeShellChannel(ctx context.Context) error {
	if !protocol.RuntimeGateEnabled(RuntimeShellGateVerified) {
		return a.StartShellChannel(ctx, nil, nil)
	}
	runtime := newRemoteShell(a)
	a.shellRuntime = runtime
	if err := a.StartShellChannel(ctx, runtime, runtime); err != nil {
		return err
	}
	a.hub.runtimeShellReady.Store(true)
	phases := make(chan shellruntime.RPC, 64)
	a.hub.shellPhase = func(frame shellruntime.RPC) bool {
		select {
		case phases <- frame:
			return true
		default:
			return false
		}
	}
	go func() {
		for {
			select {
			case event := <-phases:
				call, cancel := context.WithTimeout(ctx, 3*time.Second)
				a.shells.NotifyRuntimePhase(call, event.Launch, string(event.Phase))
				cancel()
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}

// HandleShellCapability exposes availability only to the exact signed-in owner.
// It is a UI hint; every shell operation still performs live authorization.
func (a *API) HandleShellCapability(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := a.shellActor(w, r)
	if !ok {
		return
	}
	available := false
	if protocol.RuntimeGateEnabled(RuntimeShellGateVerified) && a.shellRuntime != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		b, err := (shellResolver{a, a.shellRuntime}).Resolve(ctx, r.PathValue("id"))
		if err == nil && b.Owner == actor.Owner && b.Live && !b.Child && !b.Cron && b.ServerCapabilities.ShellVersion == 1 && b.RuntimeCapabilities.ShellVersion == 1 {
			policy, err := (shellAuthority{a.coreURL, a.coreInternalKey, &http.Client{Timeout: 3 * time.Second}}).Authorize(ctx, actor)
			available = err == nil && policy.Enabled
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"available": available})
}

// Startup installs the concrete adapter before any hello may advertise shells.
func (h *Hub) workspaceCapabilities() protocol.WorkspaceCapabilities {
	caps := protocol.VerifiedWorkspaceCapabilities()
	if caps.ManifestVersion == protocol.WorkspaceVersion && protocol.RuntimeGateEnabled(RuntimeShellGateVerified) && h.runtimeShellReady.Load() {
		caps.ShellVersion = 1
	}
	return caps
}
