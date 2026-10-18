package daemon

import (
	"encoding/json"
	"log"
	"os"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Includes disk reads AND sending their large replies. A disconnected peer or
// slow filesystem must not create an unbounded number of goroutines or buffers.
// The server also bounds pending requests. Saturated commands time out there.
var workspaceFileReads = make(chan struct{}, 2)

type workspaceFileBinding struct {
	root string
	info os.FileInfo
	live func() bool
}

func (h *AgentHost) workspaceFileBinding(id string) workspaceFileBinding {
	sess := h.get(id)
	if sess == nil || sess.workspaceCtx == nil || sess.workspace == "" || sess.workspaceInfo == nil || sess.runDone == nil || sess.loop == nil {
		return workspaceFileBinding{}
	}
	return workspaceFileBinding{root: sess.workspace, info: sess.workspaceInfo, live: func() bool {
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
	queueWorkspaceFileRead(h.sender, req, h.workspaceFileBinding(req.SessionID))
}
func (m *Manager) handleReadWorkspaceFile(raw []byte) {
	var req protocol.ReadWorkspaceFile
	if json.Unmarshal(raw, &req) != nil {
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
			binding = workspaceFileBinding{root: sess.workspace, info: sess.workspaceInfo, live: func() bool {
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
	select {
	case workspaceFileReads <- struct{}{}:
	default:
		log.Printf("workspace_file session=%q path=%q size=0 outcome=busy", req.SessionID, "")
		return
	}
	go func() {
		defer func() { <-workspaceFileReads }()
		res := protocol.WorkspaceFileResult{ErrorCode: "unavailable", Error: "Session has no live workspace."}
		if binding.live != nil && binding.info != nil && binding.live() {
			limits := parseWorkspaceFileLimits()
			limits.expectedRoot = binding.info
			res = readWorkspaceFile(binding.root, req.Path, limits)
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
			SendWorkspaceFileResult(protocol.WorkspaceFileResult) error
		}); ok {
			_ = bounded.SendWorkspaceFileResult(res)
		} else {
			_ = sender.Send(res)
		}
	}()
}
