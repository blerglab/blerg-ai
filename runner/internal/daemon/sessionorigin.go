package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

var sessionOriginReads = make(chan struct{}, 2)

// sessionOriginBusy holds the sessions with an origin read in flight.
var sessionOriginBusy sync.Map
var errOriginOutput = errors.New("git output exceeds origin limit")

type originOutput struct{ data []byte }

func (w *originOutput) Write(p []byte) (int, error) {
	if len(w.data)+len(p) > 100 {
		return 0, errOriginOutput
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

// Origin output is a small in-memory buffer; copying never blocks on I/O.
func (w *originOutput) WriteContext(_ context.Context, p []byte) (int, error) {
	return w.Write(p)
}

// readWorkspaceOrigin pins cwd by descriptor: a retargeted workspace name cannot select
// another repository between the identity check and Git starting. No user command is executed:
// git runs through the shared hardened runner (workspacegit.go, internal/hostgit), and only in the
// workspace's own repository: a .git that points elsewhere (a link, a foreign gitdir, a commondir,
// borrowed objects) reads as no verifiable HEAD.
func readWorkspaceOrigin(parent context.Context, binding workspaceFileBinding, afterGit func()) (string, bool) {
	if binding.live == nil || binding.info == nil || !binding.live() {
		return "", false
	}
	dir, ok := pinGitDir(binding.root, binding.info)
	if !ok {
		return "", false
	}
	defer dir.close()
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	output := &originOutput{}
	err := errOriginNoRepository
	if dir.vet(ctx, false) {
		err = dir.gitWith(ctx, output, nil, "rev-parse", "--verify", "HEAD")
	}
	if afterGit != nil {
		afterGit()
	}
	if ctx.Err() != nil || !binding.live() || !workspaceRootUnchanged(dir.root, dir.resolved, binding.info) {
		return "", false
	}
	// Also reject retargeting an alias used by the live launch itself.
	final, statErr := os.Stat(binding.root)
	if statErr != nil || !os.SameFile(binding.info, final) {
		return "", false
	}
	if errors.Is(err, errOriginNoRepository) {
		return "", true // not a repository of its own: no verifiable HEAD
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

var errOriginNoRepository = errors.New("the workspace has no repository of its own")

// readSandboxOrigin is readWorkspaceOrigin for a sandboxed session's clone: its HEAD is read as
// plain files (readCloneHead), never by host Git, whose configuration in the clone the session
// controls.
func readSandboxOrigin(binding workspaceFileBinding) (string, bool) {
	if binding.live == nil || binding.info == nil || !binding.live() {
		return "", false
	}
	if info, err := os.Stat(binding.root); err != nil || !os.SameFile(binding.info, info) {
		return "", false
	}
	commit, ok := readCloneHead(binding.root)
	if !ok || !binding.live() || (commit != "" && !protocol.ValidSessionOriginCommit(commit)) {
		return "", false
	}
	return commit, true
}

func queueSessionOrigin(ctx context.Context, sender Sender, req protocol.ReadSessionOrigin, binding workspaceFileBinding) {
	queueSessionOriginUsing(ctx, sender, req, func(ctx context.Context) protocol.CommitResult {
		return readLegacyGit(ctx, protocol.ReadCommit{SessionID: req.SessionID, WorkspaceGitRef: req.WorkspaceGitRef}, true, binding)
	})
}
func queueSessionOriginUsing(ctx context.Context, sender Sender, req protocol.ReadSessionOrigin, read func(context.Context) protocol.CommitResult) {
	if !validSessionOriginID(req.RequestID) || !validSessionOriginID(req.SessionID) {
		return
	}
	// One read at a time per session, so no session can take the daemon's shared slots.
	if _, busy := sessionOriginBusy.LoadOrStore(req.SessionID, struct{}{}); busy {
		return
	}
	select {
	case sessionOriginReads <- struct{}{}:
	default:
		sessionOriginBusy.Delete(req.SessionID)
		return
	}
	go func() {
		defer func() { <-sessionOriginReads; sessionOriginBusy.Delete(req.SessionID) }()
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		res := read(bounded)
		commit := ""
		if res.Commit != nil {
			commit = res.Commit.SHA
		}
		result := protocol.SessionOriginResult{WorkspaceGitRef: req.WorkspaceGitRef, Type: "session_origin_result", RequestID: req.RequestID, SessionID: req.SessionID, OK: res.OK, Commit: commit}
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
	queueSessionOriginUsing(ctx, h.sender, req, func(ctx context.Context) protocol.CommitResult {
		q := protocol.ReadCommit{SessionID: req.SessionID, RequestID: req.RequestID, WorkspaceGitRef: req.WorkspaceGitRef}
		if result, handled := h.explorer.readGit(ctx, q, true); handled {
			return result
		}
		return readLegacyGit(ctx, q, true, h.workspaceFileBinding(req.SessionID))
	})
}
func (m *Manager) handleReadSessionOrigin(raw []byte) {
	var req protocol.ReadSessionOrigin
	if len(raw) > 2048 || json.Unmarshal(raw, &req) != nil {
		return
	}
	if m.agents != nil && m.agents.get(req.SessionID) != nil {
		m.agents.ReadSessionOrigin(context.Background(), req)
		return
	}
	queueSessionOriginUsing(context.Background(), m.client, req, func(ctx context.Context) protocol.CommitResult {
		q := protocol.ReadCommit{SessionID: req.SessionID, RequestID: req.RequestID, WorkspaceGitRef: req.WorkspaceGitRef}
		if result, handled := m.explorer.readGit(ctx, q, true); handled {
			return result
		}
		return readLegacyGit(ctx, q, true, m.workspaceFileBinding(req.SessionID))
	})
}
func (c *WSClient) SendSessionOriginResult(result protocol.SessionOriginResult) error {
	return c.sendBoundedWorkspaceReply(result, time.Second)
}
