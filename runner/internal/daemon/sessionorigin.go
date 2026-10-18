package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

var sessionOriginReads = make(chan struct{}, 2)
var errOriginOutput = errors.New("git output exceeds origin limit")

type originOutput struct{ data []byte }

func (w *originOutput) Write(p []byte) (int, error) {
	if len(w.data)+len(p) > 100 {
		return 0, errOriginOutput
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

// readWorkspaceOrigin pins cwd by descriptor: a retargeted workspace name cannot select
// another repository between the identity check and Git starting. No user command is executed.
func readWorkspaceOrigin(parent context.Context, binding workspaceFileBinding, afterGit func()) (string, bool) {
	if binding.live == nil || binding.info == nil || !binding.live() {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(binding.root)
	if err != nil {
		return "", false
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return "", false
	}
	defer func() { _ = root.Close() }()
	if !workspaceRootUnchanged(root, resolved, binding.info) {
		return "", false
	}
	directory, err := root.Open(".")
	if err != nil {
		return "", false
	}
	defer func() { _ = directory.Close() }()
	info, err := directory.Stat()
	if err != nil || !os.SameFile(binding.info, info) {
		return "", false
	}
	cwd := "/proc/self/fd/" + strconv.FormatUint(uint64(directory.Fd()), 10)
	if runtime.GOOS == "darwin" {
		cwd = "/dev/fd/" + strconv.FormatUint(uint64(directory.Fd()), 10)
	} else if runtime.GOOS != "linux" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "safe.directory="+resolved, "rev-parse", "--verify", "HEAD") //nolint:gosec // G204: fixed Git command and flags, no shell; safe.directory names the exact descriptor-pinned authorized workspace.
	// Go changes cwd before remapping ExtraFiles, so name the inherited parent descriptor.
	cmd.Dir = cwd
	cmd.ExtraFiles = []*os.File{directory}
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C", "GIT_CEILING_DIRECTORIES=" + filepath.Dir(resolved)}
	output := &originOutput{}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	err = cmd.Run()
	if afterGit != nil {
		afterGit()
	}
	if ctx.Err() != nil || !binding.live() || !workspaceRootUnchanged(root, resolved, binding.info) {
		return "", false
	}
	// Also reject retargeting an alias used by the live launch itself.
	final, statErr := os.Stat(binding.root)
	if statErr != nil || !os.SameFile(binding.info, final) {
		return "", false
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && (exit.ExitCode() == 1 || exit.ExitCode() == 128) && len(output.data) == 0 {
			return "", true
		}
		return "", false
	}
	commit := strings.TrimSpace(string(output.data))
	if !protocol.ValidSessionOriginCommit(commit) {
		return "", false
	}
	return commit, true
}
func queueSessionOrigin(ctx context.Context, sender Sender, req protocol.ReadSessionOrigin, binding workspaceFileBinding) {
	if !validSessionOriginID(req.RequestID) || !validSessionOriginID(req.SessionID) {
		return
	}
	select {
	case sessionOriginReads <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-sessionOriginReads }()
		commit, ok := readWorkspaceOrigin(ctx, binding, nil)
		result := protocol.SessionOriginResult{Type: "session_origin_result", RequestID: req.RequestID, SessionID: req.SessionID, OK: ok, Commit: commit}
		if bounded, ok := sender.(interface {
			SendSessionOriginResult(protocol.SessionOriginResult) error
		}); ok {
			_ = bounded.SendSessionOriginResult(result)
		} else {
			_ = sender.Send(result)
		}
	}()
}
func validSessionOriginID(id string) bool {
	return len(id) > 0 && len(id) <= 100 && !strings.ContainsAny(id, "\r\n\x00")
}
func (h *AgentHost) ReadSessionOrigin(ctx context.Context, req protocol.ReadSessionOrigin) {
	queueSessionOrigin(ctx, h.sender, req, h.workspaceFileBinding(req.SessionID))
}
func (m *Manager) handleReadSessionOrigin(raw []byte) {
	var req protocol.ReadSessionOrigin
	if len(raw) > 512 || json.Unmarshal(raw, &req) != nil {
		return
	}
	queueSessionOrigin(context.Background(), m.client, req, m.workspaceFileBinding(req.SessionID))
}
func (c *WSClient) SendSessionOriginResult(result protocol.SessionOriginResult) error {
	return c.sendBoundedWorkspaceReply(result, time.Second)
}
