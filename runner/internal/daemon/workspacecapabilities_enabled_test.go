//go:build linux

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

func TestWorkspaceEnabledCapabilitiesVerifyInstalledConfiguration(t *testing.T) {
	binary := os.Getenv("BLERG_HOST_HELPER_TEST_BINARY")
	if binary == "" {
		t.Skip("requires the production daemon helper and owned sandbox image")
	}
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	client := NewWSClient(Config{})
	var manager *Manager
	service := NewIntegratedWorkspaceService(client, func() *Manager { return manager })
	service.HostExecutable = binary
	service.HostCommand = func(i shellruntime.Identity) shellruntime.Command { return shellruntime.HostCommand(binary, i) }
	if got := service.Capabilities(t.Context()); got != (protocol.WorkspaceCapabilities{}) {
		t.Fatal("uninstalled manager advertised", got)
	}
	manager = NewManager(client, ManagerConfig{ReposRoot: t.TempDir(), Workspaces: service})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.OwnerShellRuntime().Shutdown(ctx)
	})
	if got := service.Capabilities(t.Context()); !got.Supports("host", true) || !got.Supports("sandbox", true) {
		t.Fatal("complete actual configuration not advertised", got)
	}
	check := func(name string, runtime string) {
		t.Helper()
		if got := service.Capabilities(t.Context()); (runtime == "" && got != (protocol.WorkspaceCapabilities{})) || (runtime != "" && got.Supports(runtime, true)) {
			t.Fatalf("%s advertised unsupported runtime: %+v", name, got)
		}
	}
	trusted := service.TrustedLaunch
	service.TrustedLaunch = nil
	check("missing trusted selection", "")
	service.TrustedLaunch = trusted
	reader := service.CloseStatus
	service.CloseStatus = nil
	check("missing reader cleanup", "")
	service.CloseStatus = reader
	supervisor := service.Supervisor
	service.Supervisor = nil
	check("missing P5 registration", "")
	service.Supervisor = supervisor
	storage := service.Storage
	service.Storage = &testWorkspaceStorage{}
	check("unverified storage backend", "sandbox")
	service.Storage = storage
	service.HostExecutable = "/bin/true"
	check("wrong host helper", "host")
	service.HostExecutable = binary
	command := service.HostCommand
	service.HostCommand = func(shellruntime.Identity) shellruntime.Command {
		return shellruntime.Command{Executable: "/bin/false"}
	}
	check("wrong P5 helper", "host")
	service.HostCommand = command
	baseImage := sandboxImage()
	for _, kind := range []string{"root", "wrong-explorer"} {
		label := "p6-capability-" + ccUUID()
		image := label + ":fixture"
		dir := t.TempDir()
		dockerfile := "FROM " + baseImage + "\nUSER root\n"
		if kind == "wrong-explorer" {
			dockerfile += "RUN printf '#!/bin/sh\\nprintf invalid-helper\\n' > /usr/local/bin/blerg-workspace-explorer\nUSER agent\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
			t.Fatal(err)
		}
		buildCtx, buildCancel := context.WithTimeout(t.Context(), 2*time.Minute)
		build := exec.CommandContext(buildCtx, "docker", "build", "--pull=false", "--label", "blerg-test-run="+label, "-t", image, dir)
		output, buildErr := build.CombinedOutput()
		buildCancel()
		if buildErr != nil {
			t.Fatalf("owned capability fixture build: %v: %s", buildErr, output)
		}
		t.Cleanup(func() {
			if capabilityDockerCleanup(t, "image", "inspect", "--format", `{{index .Config.Labels "blerg-test-run"}}`, image) != label {
				t.Error("image ownership changed")
				return
			}
			capabilityDockerCleanup(t, "image", "rm", image)
		})
		t.Setenv("BLERG_RUNNER_SANDBOX_IMAGE", image)
		check("unsafe image "+kind, "sandbox")
	}
	t.Setenv("BLERG_RUNNER_SANDBOX_IMAGE", "postgres:16")
	check("image lacks P4/P5 helpers", "sandbox")
}

func TestWorkspaceEnabledSelectionRequiresNegotiatedServer(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var dispatched atomic.Bool
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err = conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteJSON(protocol.DaemonWelcome{Type: "daemon_welcome"}) // legacy server, no workspace support
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req protocol.LocalWorkspaceControl
			if json.Unmarshal(raw, &req) == nil && req.Type == "local_workspace_control" {
				dispatched.Store(true)
				_ = conn.WriteJSON(protocol.LocalWorkspaceControlResult{Type: "local_workspace_control_result", SessionID: req.SessionID, RequestID: req.RequestID})
			}
		}
	}))
	defer endpoint.Close()
	client := NewWSClient(Config{ServerURL: "ws" + strings.TrimPrefix(endpoint.URL, "http"), ReposRoot: t.TempDir(), WorkspaceCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true, Sandbox: true, Cluster: true}})
	var manager *Manager
	service := NewIntegratedWorkspaceService(client, func() *Manager { return manager })
	manager = NewManager(client, ManagerConfig{ReposRoot: t.TempDir(), Workspaces: service})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); client.Run(ctx, func() []string { return nil }) }()
	defer func() { cancel(); <-done }()
	for !client.workspaceNegotiated.Load() {
		select {
		case <-ctx.Done():
			t.Fatal("legacy welcome not received")
		case <-time.After(time.Millisecond):
		}
	}
	_, _, err := service.Resolve(ctx, protocol.SpawnSession{SessionID: "session", Workspace: &protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "host"}})
	if err == nil || !strings.Contains(err.Error(), "server does not support workspace v2") || dispatched.Load() {
		t.Fatal("UNNEGOTIATED_SELECTION: selection authority RPC escaped without peer support", err)
	}
}

// testing cancels t.Context before Cleanup; ownership verification and removal
// must still join our own image after a failed assertion.
func capabilityDockerCleanup(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("owned capability fixture cleanup: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}
