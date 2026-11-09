package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
)

// The host fetch's upload-pack is only a duplex byte relay. Docker is started by the daemon,
// outside Git's address-space limit (the Go Docker client cannot run under that limit).
// Descriptors 3 and 4 connect directly to that client's stdout and stdin. No clone path or
// configuration reaches a host Git serving process. Closing unused descriptors is essential:
// otherwise a relay keeps the other direction alive after the server exits. Hostgit owns
// and kills the relay process group before reaping; never signal a possibly reused child PID.
const sandboxPackRelay = `exec 5<&0
cat <&3 3<&- 4>&- 5<&- & reader=$!
cat <&5 >&4 3<&- 4>&- 5<&- &
exec 3<&- 4>&- 5<&-
wait "$reader"; result=$?
exit "$result"`

func packShellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func sandboxPackArgs(path string, c *sandboxClone, name string, timeout time.Duration) []string {
	args := []string{"run", "--rm", "-i", "--pull=never", "--name", name, "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--init", "--memory=512m", "--memory-swap=512m", "--pids-limit=64", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--label", "blerg.runner.pack-server=1", "--mount", sandboxBindMount(path, path) + ",readonly"}
	if !c.OwnObjects {
		args = append(args, "--mount", sandboxBindMount(c.Objects, c.Objects)+",readonly")
	}
	for _, label := range sandboxTestLabels {
		args = append(args, "--label", label)
	}
	// The server-side timeout also bounds a container whose Docker client was killed or lost.
	seconds := max(1, int(timeout.Round(time.Second)/time.Second))
	args = append(args, "--entrypoint", "/usr/bin/timeout", sandboxImage(), "--signal=KILL", strconv.Itoa(seconds)+"s", "/usr/bin/env", "-i", "PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=/dev/null", "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=none", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "git", "--no-replace-objects")
	args = append(args, hostgit.Config(hostgit.Options{Untrusted: true, MemoryLimit: true, Config: []string{"pack.threads=1"}})...)
	return append(args, "upload-pack", "--strict", filepath.Join(path, ".git"))
}

// fetchSandboxPack keeps the host limited to receiving and strictly indexing the pack. Both
// clone metadata and object parsing on the serving side happen in a throwaway container with
// no network, no credentials, no writable host mounts, and an aggregate memory/process limit.
func fetchSandboxPack(parent context.Context, timeout time.Duration, path, sessionID string, c *sandboxClone, branch, target string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	bin, err := exec.LookPath("docker")
	if err != nil {
		return "", errors.New("pack serving requires Docker")
	}
	name := "blerg-pack-" + ccUUID()
	container := dockerContainerName(sessionID)
	if registered, ok := sandboxContainer(sessionID); ok {
		container = registered
	}
	prefix := pinnedDockerArgs(container)
	args := append(append([]string{}, prefix...), sandboxPackArgs(path, c, name, timeout)...)
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // daemon-resolved Docker executable and fixed isolation flags; validated private clone paths are individual argv elements
	cmd.Env = sandboxPackDockerEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	// Never return Docker/serving diagnostics: they may quote untrusted repository data.
	cmd.Stderr = io.Discard
	input, writeEnd, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer func() { _ = input.Close(); _ = writeEnd.Close() }()
	readEnd, output, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer func() { _ = readEnd.Close(); _ = output.Close() }()
	cmd.Stdin = input
	cmd.Stdout = output
	if err := cmd.Start(); err != nil {
		return "", errors.New("could not start isolated pack server")
	}
	_ = input.Close()
	_ = output.Close()
	// Cleanup uses its own small budget even when the fetch ran out of time. A random name and
	// the same pinned Docker context ensure it can remove only the server created for this call.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(parent), sandboxDockerTimeout)
		defer cancel()
		cleanupArgs := append(append([]string{}, prefix...), "rm", "-f", name)
		rm := exec.CommandContext(cleanup, bin, cleanupArgs...) //nolint:gosec // same daemon-resolved Docker binary; name is a freshly generated UUID, not session data
		rm.Env = sandboxPackDockerEnv()
		rm.WaitDelay = time.Second
		_ = rm.Run() // --rm may already have removed it; the in-container timeout remains the final bound
	}()
	relay := "/bin/sh -c " + packShellQuote(sandboxPackRelay)
	out, fetchErr := hostgit.Output(ctx, hostgit.Options{Dir: c.Repo, Protocols: []string{"file"}, ExtraFiles: []*os.File{readEnd, writeEnd}, Config: []string{"fetch.fsckObjects=true", "transfer.fsckObjects=true", "fetch.unpackLimit=0"}, MemoryLimit: true, LimitKB: sandboxFetchMemoryKB, Untrusted: true, Timeout: timeout},
		"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-maintenance", "--no-write-commit-graph", "--upload-pack="+relay, "file:///blerg-pack.git", "+"+branch+":"+target)
	// Close the daemon's copies before joining the client; all protocol bytes have been consumed.
	_ = readEnd.Close()
	_ = writeEnd.Close()
	if fetchErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if fetchErr != nil {
		return out, fetchErr
	}
	if waitErr != nil {
		return "", errors.New("isolated pack server failed")
	}
	return out, nil
}

// Docker needs only its host connection and client configuration. Engine credentials and
// session tokens are never inherited by the client, and its container gets an empty env.
func sandboxPackDockerEnv() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "USER", "TMPDIR", "XDG_RUNTIME_DIR", "SSH_AUTH_SOCK", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_API_VERSION", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
