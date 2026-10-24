package daemon

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Includes disk reads AND sending their large replies. A disconnected peer or
// slow filesystem must not create an unbounded number of goroutines or buffers.
// The server also bounds pending requests. Saturated commands time out there.
var workspaceFileReads = make(chan struct{}, 2)

type workspaceFileBinding struct {
	inRuntimeOnly bool
	root          string
	alias         string
	info          os.FileInfo
	live          func() bool
}

func (h *AgentHost) workspaceFileBinding(id string) workspaceFileBinding {
	sess := h.get(id)
	if sess == nil || sess.workspaceCtx == nil || sess.workspace == "" || sess.workspaceInfo == nil || sess.runDone == nil || sess.loop == nil {
		return workspaceFileBinding{}
	}
	alias := ""
	if sess.sandboxed {
		alias = sandboxWorkdir
	}
	return workspaceFileBinding{inRuntimeOnly: sess.sandboxed, root: sess.workspace, alias: alias, info: sess.workspaceInfo, live: func() bool {
		if h.get(id) != sess || sess.workspaceCtx.Err() != nil {
			return false
		}
		select {
		case <-sess.runDone:
			return false
		default:
			return true
		}
	}}
}

// ReadWorkspaceFile reads from an existing launch, without preparing an engine.
func (h *AgentHost) ReadWorkspaceFile(req protocol.ReadWorkspaceFile) {
	h.ReadWorkspaceFileContext(context.Background(), req)
}
func (h *AgentHost) ReadWorkspaceFileContext(ctx context.Context, req protocol.ReadWorkspaceFile) {
	if sess := h.get(req.SessionID); sess != nil && sess.workspaceV2 || req.LaunchGeneration != "" || req.Generation != 0 || h.workspaceFileBinding(req.SessionID).inRuntimeOnly {
		h.explorer.HandleFile(ctx, h.sender, req)
		return
	}
	queueWorkspaceFileRead(h.sender, req, h.workspaceFileBinding(req.SessionID))
}
func (m *Manager) handleReadWorkspaceFile(raw []byte) {
	var req protocol.ReadWorkspaceFile
	if json.Unmarshal(raw, &req) != nil {
		return
	}
	if m.agents != nil && m.agents.get(req.SessionID) != nil {
		m.agents.ReadWorkspaceFileContext(context.Background(), req)
		return
	}
	if m.explorer.registered(req.SessionID) || req.LaunchGeneration != "" || req.Generation != 0 || m.workspaceFileBinding(req.SessionID).inRuntimeOnly {
		m.explorer.HandleFile(context.Background(), m.client, req)
		return
	}
	queueWorkspaceFileRead(m.client, req, m.workspaceFileBinding(req.SessionID))
}
func (m *Manager) workspaceFileBinding(id string) workspaceFileBinding {
	var binding workspaceFileBinding
	if m.agents != nil {
		binding = m.agents.workspaceFileBinding(id)
	}
	if binding.live == nil {
		m.mu.Lock()
		sess := m.sessions[id]
		m.mu.Unlock()
		if sess != nil && sess.workspace != "" && sess.workspaceInfo != nil && sess.Done() != nil {
			alias := ""
			if sess.sandboxed {
				alias = sandboxWorkdir
			}
			binding = workspaceFileBinding{inRuntimeOnly: sess.sandboxed, root: sess.workspace, alias: alias, info: sess.workspaceInfo, live: func() bool {
				m.mu.Lock()
				same := m.sessions[id] == sess
				m.mu.Unlock()
				if !same {
					return false
				}
				select {
				case <-sess.Done():
					return false
				default:
					return true
				}
			}}
		}
	}
	return binding
}
func queueWorkspaceFileRead(sender Sender, req protocol.ReadWorkspaceFile, binding workspaceFileBinding) {
	if req.RequestID == "" || len(req.RequestID) > 100 || req.SessionID == "" || len(req.SessionID) > 100 {
		return
	}
	received := time.Now()
	select {
	case workspaceFileReads <- struct{}{}:
	default:
		log.Printf("workspace_file session=%q path=%q size=0 outcome=busy", req.SessionID, "")
		return
	}
	go func() {
		defer func() { <-workspaceFileReads }()
		res := protocol.WorkspaceFileResult{ErrorCode: "unavailable", Error: "Session has no live workspace."}
		if !binding.inRuntimeOnly && binding.live != nil && binding.info != nil && binding.live() {
			limits := parseWorkspaceFileLimits()
			limits.expectedRoot = binding.info
			res = readBoundWorkspaceFile(binding, req.Path, limits)
			// An engine end/replacement during I/O must not turn an old launch's
			// file into a reply from the current session runtime.
			if !binding.live() {
				res = protocol.WorkspaceFileResult{ErrorCode: "unavailable", Error: "Session has no live workspace."}
			}
		}
		res.Type = "workspace_file_result"
		res.RequestID = req.RequestID
		res.SessionID = req.SessionID
		outcome := res.ErrorCode
		if res.OK {
			outcome = "ok"
		}
		log.Printf("workspace_file session=%q path=%q size=%d outcome=%s", req.SessionID, res.Path, res.Size, outcome)
		if bounded, ok := sender.(interface {
			SendWorkspaceFileResultBy(protocol.WorkspaceFileResult, time.Time) error
		}); ok {
			_ = bounded.SendWorkspaceFileResultBy(res, received.Add(workspaceReplyTTL))
		} else {
			_ = sender.Send(res)
		}
	}()
}

// Absolute references are names inside this launch's root, never authority to open
// another host path. Only a sandbox launch has the /workspace mount alias.
func readBoundWorkspaceFile(binding workspaceFileBinding, name string, limits workspaceFileLimits) (result protocol.WorkspaceFileResult) {
	// #165 accepts a launch alias, but traversal starts only from its canonical,
	// pinned directory. No descendant link is followed and delivery rechecks the
	// launch alias, including replacement while the file is being read.
	launchRoot := binding.root
	canonical, err := filepath.EvalSymlinks(launchRoot)
	if err != nil {
		return workspaceFileError("", err)
	}
	info, err := os.Lstat(canonical)
	if err != nil || binding.info == nil || !info.IsDir() || !os.SameFile(info, binding.info) {
		return workspaceFileError("", os.ErrPermission)
	}
	binding.root = canonical
	defer func() {
		info, err := os.Stat(launchRoot)
		if err != nil || !os.SameFile(info, binding.info) {
			result = workspaceFileError("", os.ErrPermission)
		}
	}()

	if !filepath.IsAbs(name) {
		return readWorkspaceFile(binding.root, name, limits)
	}
	if strings.HasPrefix(name, "//") {
		return workspaceFileError("", os.ErrPermission)
	}
	if _, ok := workspaceCleanRequest(strings.TrimPrefix(name, "/")); !ok {
		return workspaceFileError("", os.ErrPermission)
	}
	root, err := filepath.Abs(binding.root)
	if err != nil || binding.root == "" {
		return workspaceFileError("", os.ErrPermission)
	}
	scopes := []string{root, launchRoot, binding.alias}
	// Physical-directory tools can spell a symlinked launch root canonically.
	// Trust that spelling only while it still identifies the launch directory.
	if canonical, err := filepath.EvalSymlinks(root); err == nil && binding.info != nil {
		if info, err := os.Stat(canonical); err == nil && info.IsDir() && os.SameFile(binding.info, info) {
			scopes = append(scopes, canonical)
		}
	}
	for _, scope := range scopes {
		if scope == "" {
			continue
		}
		prefix := strings.TrimSuffix(scope, string(filepath.Separator)) + string(filepath.Separator)
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		res := readWorkspaceFile(binding.root, strings.TrimPrefix(name, prefix), limits)
		res.RequestedPath = filepath.ToSlash(filepath.Clean(name))
		return res
	}
	return workspaceFileError("", os.ErrPermission)
}
