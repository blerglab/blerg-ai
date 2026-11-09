package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
)

type keeperGateBridge struct {
	ready                   bool
	probes                  int
	serverCaps, runtimeCaps protocol.WorkspaceCapabilities
}

func (b *keeperGateBridge) Invoke(_ context.Context, method string, in any, _ any) error {
	switch method {
	case "ready":
		if b.ready {
			return nil
		}
	case "probe":
		b.probes++
		return nil
	case "capabilities":
		raw, _ := json.Marshal(in)
		var caps struct {
			Server, Runtime protocol.WorkspaceCapabilities
		}
		if err := json.Unmarshal(raw, &caps); err != nil {
			return err
		}
		b.serverCaps, b.runtimeCaps = caps.Server, caps.Runtime
		return nil
	}
	return errors.New("keeper reconciliation pending")
}
func (b *keeperGateBridge) Metadata(method string, in, out any) error {
	return b.Invoke(context.Background(), method, in, out)
}
func (*keeperGateBridge) HostSession(string) bool                                   { return true }
func (*keeperGateBridge) SetReceiver(func(context.Context, shellruntime.RPC) error) {}
func (*keeperGateBridge) Close() error                                              { return nil }

func TestHostDriverAdmissionNeverWaitsForKeeperAdoption(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	bridge := &keeperGateBridge{}
	runtime := shellruntime.NewService(func(shellruntime.RPC) error { return nil })
	runtime.SetHostBridge(bridge)
	workspace := &LocalWorkspace{mu: new(sync.Mutex), record: filepath.Join(t.TempDir(), "workspace.json"), state: workspacePrivateManifest{Version: privateWorkspaceVersion, Session: "session", HostDriverStopped: true}}
	host := &hostWorkspace{w: workspace, service: &LocalWorkspaceService{Supervisor: &workspaceSupervisors{service: runtime}}}
	if err := host.driverStarting(); err != nil {
		t.Fatal("shell adoption blocked agent recovery", err)
	}
	if workspace.state.HostDriverStopped {
		t.Fatal("agent admission stayed paused for shells")
	}
	bridge.ready = true
	if err := host.driverStarting(); err != nil {
		t.Fatal("completed adoption remained fenced", err)
	}
	if workspace.state.HostDriverStopped {
		t.Fatal("adopted workspace did not record writer start")
	}
}

func TestCapabilityProbeUsesKeeperProfileWithoutWeakeningSleepProbe(t *testing.T) {
	bridge := &keeperGateBridge{}
	runtime := shellruntime.NewService(func(shellruntime.RPC) error { return nil })
	runtime.SetHostBridge(bridge)
	service := &LocalWorkspaceService{Supervisor: &workspaceSupervisors{service: runtime}, HostCommand: func(identity shellruntime.Identity) shellruntime.Command {
		return shellruntime.HostCommand("/nonexistent-legacy-probe", identity)
	}}
	if err := service.probePersistentStack(t.Context()); err != nil {
		t.Fatal("capability probe bypassed keeper restart profile", err)
	}
	if bridge.probes != 1 {
		t.Fatal("keeper profile not probed", bridge.probes)
	}
}

func TestHostAdmissionRefusalNamesKeeperCauseWithoutLeakingErrors(t *testing.T) {
	for _, cause := range []error{shellruntime.ErrAdoptionPending, shellruntime.ErrKeeperUnavailable} {
		if got := hostDriverStartRefusal(fmt.Errorf("private journal details: %w", cause)); got != cause.Error() {
			t.Fatal("keeper refusal described a journal failure", got)
		}
	}
	if got := hostDriverStartRefusal(errors.New("private path and terminal bytes")); got != "host lifecycle journal unavailable" {
		t.Fatal("journal error content escaped", got)
	}
}

func TestKeeperRefreshReprobesPersistenceWithoutAgentReconnect(t *testing.T) {
	caps := protocol.WorkspaceCapabilitiesFor("host", "linux")
	caps.ShellPersistence = 2
	caps.PersistentHost = true
	probes := 0
	client := NewWSClient(Config{WorkspaceCapabilitiesProvider: func(ctx context.Context) protocol.WorkspaceCapabilities {
		if _, bounded := ctx.Deadline(); !bounded {
			t.Error("keeper reprobe is unbounded")
		}
		probes++
		return caps
	}})
	service := client.OwnerShellRuntime()
	bridge := &keeperGateBridge{}
	service.SetHostBridge(bridge)
	client.shellServerCaps.Store(caps)
	old := caps
	old.ShellPersistence = 0
	old.PersistentHost = false
	service.SetCapabilities(caps, old)
	if bridge.runtimeCaps.SupportsPersistence("host") {
		t.Fatal("fixture unexpectedly advertised persistence while keeper was missing")
	}
	client.refreshKeeperCapabilities(t.Context())
	if probes != 1 || !bridge.runtimeCaps.SupportsPersistence("host") || bridge.serverCaps != caps {
		t.Fatal("keeper returned but startup capability refusal remained cached")
	}
}

func TestAgentCapabilitiesUseKeeperProofWithoutSynchronousIPC(t *testing.T) {
	runtime := shellruntime.NewService(func(shellruntime.RPC) error { return nil })
	runtime.SetHostBridge(&keeperGateBridge{})
	if err := runtime.ProbeHostKeeper(t.Context(), shellruntime.Command{}); err != nil {
		t.Fatal(err)
	}
	manager := shellruntime.NewReconnectingHostBridge(t.Context(), func(ctx context.Context) (shellruntime.HostBridge, error) { <-ctx.Done(); return nil, ctx.Err() })
	runtime.SetHostBridge(manager)
	defer func() { _ = manager.Close() }()
	probes := 0
	workspaces := &LocalWorkspaceService{Supervisor: &workspaceSupervisors{service: runtime}, HostCommand: func(i shellruntime.Identity) shellruntime.Command {
		probes++
		return shellruntime.HostCommand("/nonexistent", i)
	}}
	if !workspaces.persistentStackReady(t.Context()) || probes != 0 {
		t.Fatal("agent hello synchronously depended on keeper probe")
	}
	background := context.WithValue(t.Context(), keeperProbeContext{}, true)
	if workspaces.persistentStackReady(background) || probes != 1 {
		t.Fatal("background recovery reused a stale probe instead of re-proving it")
	}
}

func TestWelcomeKeepsRawShellOfferForBackgroundRecovery(t *testing.T) {
	offer := protocol.WorkspaceCapabilitiesFor("host", "linux")
	offer.ShellPersistence = 2
	offer.PersistentHost = true
	local := offer
	local.ShellPersistence = 0
	local.PersistentHost = false
	observed := make(chan protocol.WorkspaceCapabilities, 1)
	server := newTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		if conn.WriteJSON(protocol.DaemonWelcome{Type: "daemon_welcome", DaemonID: "fixture", WorkspaceCapabilities: offer}) != nil {
			return
		}
		if conn.WriteJSON(map[string]string{"type": "fixture_after_welcome"}) != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	client := NewWSClient(Config{ServerURL: server.wsURL(), DaemonToken: "fixture", DaemonName: "fixture", DaemonMode: "local", ReposRoot: t.TempDir(), HeartbeatInterval: time.Hour, WorkspaceCapabilities: local})
	client.OnMessage("fixture_after_welcome", func([]byte) {
		caps, _ := client.shellServerCaps.Load().(protocol.WorkspaceCapabilities)
		observed <- caps
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { client.Run(ctx, func() []string { return nil }); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case actual := <-observed:
		if actual != offer {
			t.Fatal("initial missing keeper permanently erased the server persistence offer")
		}
	case <-ctx.Done():
		t.Fatal("welcome did not complete independently of shell capability")
	}
}
