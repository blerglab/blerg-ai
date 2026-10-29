package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

const WorkspaceRuntimeProbeVersion = "workspace-runtime-v2"
const WorkspaceExplorerProbeVersion = "workspace-explorer-v2"

// Capabilities describes the installed local stack, not discovery or a claimed
// wire version. Host containment/helper probes and sandbox image/storage probes
// establish independent support. A missing backend never disables another one.
func (s *LocalWorkspaceService) Capabilities(ctx context.Context) protocol.WorkspaceCapabilities {
	return s.capabilitiesForPlatform(ctx, runtime.GOOS, s.probeWorkspaceHost, probeWorkspaceImage)
}

// Probes are injected only by package tests. Production always probes the
// concrete platform, fixed helper commands and immutable local Docker image.
func (s *LocalWorkspaceService) capabilitiesForPlatform(ctx context.Context, platform string, hostProbe, imageProbe func(context.Context) error) protocol.WorkspaceCapabilities {
	empty := protocol.WorkspaceCapabilities{}
	if s == nil ||
		s.capabilityReady == nil || !s.capabilityReady() || s.Authority == nil || s.Supervisor == nil || s.Resolve == nil || s.TrustedLaunch == nil ||
		s.Results == nil || s.CommitFinal == nil || s.CancelStatus == nil || s.ResumeStatus == nil || s.CloseStatus == nil {
		return empty
	}
	if registry, ok := s.Supervisor.(*workspaceSupervisors); !ok || registry.service == nil {
		return empty
	}
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	caps := empty
	if protocol.WorkspaceRuntimeEnabled("host", platform) && hostProbe(call) == nil {
		caps = protocol.WorkspaceCapabilitiesFor("host", platform)
	}
	_, dockerStorage := s.Storage.(*DockerWorkspaceStorage)
	if protocol.WorkspaceRuntimeEnabled("sandbox", platform) && (s.Storage == nil || dockerStorage) && imageProbe(call) == nil {
		caps.Sandbox = true
		caps.ManifestVersion = protocol.WorkspaceVersion
		caps.ShellVersion = 1
	}
	return caps
}
func (s *LocalWorkspaceService) probeWorkspaceHost(ctx context.Context) error {
	if err := workspacePlatformProbe(); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	writerExecutable := executable
	if s.HostExecutable != "" {
		writerExecutable = s.HostExecutable
	}
	if err := ProbeWorkspaceHelper(ctx, writerExecutable, []string{"workspace-runtime", "--probe"}, WorkspaceRuntimeProbeVersion); err != nil {
		return err
	}
	command := shellruntime.HostCommand(executable, shellruntime.Identity{})
	if s.HostCommand != nil {
		command = s.HostCommand(shellruntime.Identity{})
	}
	return shellruntime.ProbeCommand(ctx, command)
}

// ProbeWorkspaceHelper executes only an image/configuration-owned fixed helper,
// with no engine credentials and bounded time/output. No request supplies argv.
func ProbeWorkspaceHelper(ctx context.Context, executable string, args []string, version string) error {
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(call, executable, args...) //nolint:gosec // trusted runtime/image configuration, never browser fields
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	out := &workspaceBoundedBuffer{limit: 128}
	cmd.Stdout = out
	if cmd.Run() != nil || strings.TrimSpace(out.String()) != version {
		return errors.New("workspace helper unavailable")
	}
	return nil
}
func ProbeWorkspaceRuntime(out io.Writer) error {
	if err := workspacePlatformProbe(); err != nil {
		return err
	}
	_, err := io.WriteString(out, WorkspaceRuntimeProbeVersion+"\n")
	return err
}
func ProbeWorkspaceExplorer(out io.Writer) error {
	if err := workspacePlatformProbe(); err != nil {
		return err
	}
	_, err := io.WriteString(out, WorkspaceExplorerProbeVersion+"\n")
	return err
}

// Probe an immutable local image ID on the captured Docker context. The empty
// container has no network or host mounts. Cleanup verifies our random label;
// a failed probe can never target an existing sandbox or another builder's work.
func probeWorkspaceImage(ctx context.Context) error {
	raw, err := workspaceDockerOutput(ctx, "context", "show")
	if err != nil {
		return err
	}
	dockerContext := strings.TrimSpace(string(raw))
	if dockerContext == "" || strings.HasPrefix(dockerContext, "-") || strings.ContainsAny(dockerContext, " \t\n/") {
		return errors.New("docker context unavailable")
	}
	args := []string{"--context", dockerContext}
	run := func(ctx context.Context, tail ...string) ([]byte, error) {
		return workspaceDockerOutput(ctx, append(append([]string(nil), args...), tail...)...)
	}
	raw, err = run(ctx, "image", "inspect", "--format", "{{.Id}}", sandboxImage())
	if err != nil {
		return err
	}
	image := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(image, "sha256:") || len(image) != 71 {
		return errors.New("workspace image identity unavailable")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(nonce[:])
	name := "blerg-workspace-probe-" + token
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		label, e := run(cleanup, "inspect", "--format", `{{index .Config.Labels "blerg-workspace-probe"}}`, name)
		if e == nil && strings.TrimSpace(string(label)) == token {
			_, _ = run(cleanup, "rm", "-f", name)
		}
	}()
	// The shell program is a literal image probe, with neither user input nor credentials.
	raw, err = run(ctx, "create", "--name", name, "--label", "blerg-workspace-probe="+token, "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=128m", "--pids-limit=16", "--entrypoint", "/bin/sh", image, "-ec", `test "$(id -u)" != 0
 test "$(awk '/^CapEff:/{print $2}' /proc/self/status)" = 0000000000000000
 test "$(stat -c '%u %g %a' /usr/local/bin/blerg-workspace-explorer)" = '0 0 755'
 test "$(stat -c '%u %g %a' /usr/local/bin/blerg-shell-supervisor)" = '0 0 755'
 /usr/local/bin/blerg-workspace-explorer --probe
 /usr/local/bin/blerg-shell-supervisor --probe`)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(string(raw))
	if len(id) != 64 {
		return errors.New("workspace probe container identity unavailable")
	}
	raw, err = run(ctx, "start", "--attach", id)
	if err != nil || strings.TrimSpace(string(raw)) != WorkspaceExplorerProbeVersion+"\n"+shellruntime.ProbeVersion {
		return errors.New("workspace image helpers unavailable")
	}
	return nil
}
