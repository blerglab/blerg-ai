package runner

import (
	"context"
	"io"
	"os"
	"runtime"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

// ConfigureClusterRuntime establishes the protected mount, identities, writers,
// launch consumer and services. A manifest alone is never runtime readiness.
func podWorkspaceCapabilities(ctx context.Context, cfg Config) protocol.WorkspaceCapabilities {
	if !protocol.RuntimeGateEnabled(protocol.WorkspaceRuntimeGateVerified) || runtime.GOOS != "linux" || cfg.Workspace == nil || cfg.WorkspaceErr != nil || cfg.ClusterWriters == nil || cfg.Supervisor == nil || cfg.ClusterServices == nil || cfg.ClusterAttach == nil || cfg.StartCommand == nil || cfg.ClusterPrivateRoot == "" || !podCapabilityServicesReady(cfg) {
		return protocol.WorkspaceCapabilities{}
	}
	executable, err := os.Executable()
	// P4 is in-process over the verified private/public roots. P5 uses the
	// pod executable's contained helper, not the desktop image helper.
	if err != nil || daemon.ProbeWorkspaceRuntime(io.Discard) != nil || daemon.ProbeWorkspaceHelper(ctx, executable, []string{"cluster-shell-supervisor", "--probe"}, shellruntime.ProbeVersion) != nil {
		return protocol.WorkspaceCapabilities{}
	}
	return protocol.WorkspaceCapabilities{ManifestVersion: protocol.WorkspaceVersion, ShellVersion: 1}
}

// Shared by the shipped Run path and the isolated image/transport regression.
// Capability computation is tied to this exact trusted runtime configuration.
func newPodWorkspaceClient(cfg Config, publicRoot string, unsupported func()) *daemon.WSClient {
	var required *protocol.WorkspaceManifest
	if cfg.Workspace != nil {
		required = &cfg.Workspace.Manifest
	}
	return daemon.NewWSClient(daemon.Config{
		WorkspaceCapabilitiesProvider: func(ctx context.Context) protocol.WorkspaceCapabilities { return podWorkspaceCapabilities(ctx, cfg) },
		RequiredWorkspace:             required, OnUnsupportedWorkspace: unsupported,
		ServerURL: cfg.ServerWS, DaemonToken: cfg.DaemonToken, DaemonName: "runner-" + cfg.SessionID,
		DaemonMode: "runner", ReposRoot: publicRoot, Version: cfg.Version,
	})
}
