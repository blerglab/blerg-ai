package server

import "context"
import "net/http"
import "time"
import "github.com/blerglab/blerg-ai/runner/internal/shellruntime"
import "github.com/blerglab/blerg-ai/runner/internal/protocol"
import "github.com/blerglab/blerg-ai/runner/internal/ownershell"

func (a *API) StartRuntimeShellChannel(ctx context.Context) error {
	if protocol.EnabledWorkspaceCapabilities().ManifestVersion != protocol.WorkspaceVersion {
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
	if a.shellRuntime != nil && a.ownsShellSession(r.Context(), actor, r.PathValue("id")) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		b, err := (shellResolver{a, a.shellRuntime}).Resolve(ctx, r.PathValue("id"))
		if ownershell.Busy(err) && b.Owner == actor.Owner {
			// Not a "no": the browser keeps its current shells and asks again shortly.
			shellError(w, err)
			return
		}
		if err == nil && b.Owner == actor.Owner && b.Live && !b.Child && !b.Cron && b.ServerCapabilities.Supports(b.Runtime, true) && b.RuntimeCapabilities.Supports(b.Runtime, true) {
			policy, err := (shellAuthority{a.coreURL, a.coreInternalKey, &http.Client{Timeout: 3 * time.Second}}).Authorize(ctx, actor)
			available = err == nil && policy.Enabled
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"available": available})
}

// Startup installs the concrete adapter before any hello may advertise shells.
func (h *Hub) workspaceCapabilities() protocol.WorkspaceCapabilities {
	caps := protocol.EnabledWorkspaceCapabilities()
	if !h.runtimeShellReady.Load() {
		caps.ShellVersion = 0
	}
	return caps
}
