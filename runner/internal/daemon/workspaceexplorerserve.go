package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// WorkspaceExplorerStart is private launcher-to-helper configuration, sent on
// an inherited pipe. It must never be accepted from agent files/browser input.
// The path is an explicit trusted public root, never HOME or a guessed parent.
type WorkspaceExplorerStart struct {
	Binding    *protocol.WorkspaceRuntimeBinding `json:"binding,omitempty"`
	Expected   *helperPrepared                   `json:"expected,omitempty"`
	GitTargets map[string]string                 `json:"git_targets,omitempty"`
	GitPins    map[string]WorkspaceGitPin        `json:"git_pins,omitempty"`
	SessionID  string                            `json:"session_id"`
	Manifest   protocol.WorkspaceManifest        `json:"manifest"`
	Runtime    protocol.WorkspaceRuntimeRef      `json:"runtime"`
}

// ServeWorkspaceExplorer runs inside a sandbox, or is embedded by a pod runner.
// P2/P3 own creation/containment of the helper and its private pipes. The first
// reply reports the actual root after creation; subsequent requests cannot
// change configuration. P6 packages cmd/workspace-explorer in runtime images.
func ServeWorkspaceExplorer(ctx context.Context, publicRoot string, input io.Reader, output *os.File) (result error) {
	ctx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	output, err := workspacePollablePipe(output)
	if err != nil {
		return err
	}
	defer func() { _ = output.Close() }()
	if file, ok := input.(*os.File); ok {
		pollable, err := workspacePollablePipe(file)
		if err != nil {
			return err
		}
		defer func() { _ = pollable.Close() }()
		stop := context.AfterFunc(ctx, func() { _ = pollable.Close() })
		defer stop()
		input = pollable
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), protocol.WorkspaceExplorerReplyBytes)
	if !scanner.Scan() {
		return errors.New("missing explorer launcher configuration")
	}
	var start WorkspaceExplorerStart
	if json.Unmarshal(scanner.Bytes(), &start) != nil || start.SessionID == "" || len(start.SessionID) > 100 || start.Manifest.Validate() != nil || start.Runtime.Generation <= 0 || start.Runtime.LaunchGeneration != start.Manifest.LaunchGeneration {
		return errors.New("invalid explorer launcher configuration")
	}
	info, err := os.Lstat(publicRoot) //nolint:gosec // fixed launcher root, never an agent path; the prepared identity is checked before traversal
	if err != nil || !info.IsDir() {
		return errors.New("public root unavailable")
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: start.Runtime, ManifestDigest: start.Manifest.Digest, Root: workspaceRootIdentity(info)}
	if start.Binding != nil && binding != *start.Binding {
		return errors.New("runtime binding changed")
	}
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	engine, err := NewLocalExplorerWithGitPins(publicRoot, start.Manifest, binding, func() bool { return lifetime.Err() == nil }, start.GitPins)
	if err != nil {
		return errors.New("public root unavailable")
	}
	defer func() { result = errors.Join(result, engine.Close()) }()
	prepared := helperPrepared{WorkspaceRuntimePrepared: protocol.WorkspaceRuntimePrepared{Type: "workspace_runtime_prepared", SessionID: start.SessionID, Binding: binding}, Members: engine.memberRoots, GitRoots: engine.gitRoots}
	if start.Expected != nil && (binding != start.Expected.Binding || !equalWorkspaceRoots(prepared.Members, start.Expected.Members) || !equalWorkspaceRoots(prepared.GitRoots, start.Expected.GitRoots)) {
		return errors.New("runtime root changed")
	}
	for id, target := range start.GitTargets {
		info, err := os.Lstat(target)
		if err != nil || !info.IsDir() || engine.gitRoots[id].Inode == 0 {
			return errors.New("private Git target unavailable")
		}
		if engine.gitPins == nil {
			engine.gitPins = map[string]WorkspaceGitPin{}
		}
		pin := WorkspaceGitPin{GitRoot: engine.gitRoots[id], ObjectRoot: workspaceRootIdentity(info), Alternates: target + "\n"}
		if expected, exists := engine.gitPins[id]; exists && expected != pin {
			return errors.New("private object root changed")
		}
		engine.gitPins[id] = pin
	}
	prepared.Pins = engine.gitPins
	sender := &explorerStreamSender{out: output, ctx: lifetime, fail: cancelServe}
	if err := sender.Send(prepared); err != nil {
		return err
	}
	var router WorkspaceExplorerRouter
	router.Bind(start.SessionID, engine)
	defer func() { cancel(); router.Close() }()
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) > 16384 {
			return errors.New("explorer request too large")
		}
		var envelope struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.SessionID != start.SessionID {
			continue
		}
		switch envelope.Type {
		case "workspace_explorer", "cancel_workspace_explorer":
			router.Handle(lifetime, sender, raw)
		case "read_workspace_file", "cancel_workspace_file":
			var req protocol.ReadWorkspaceFile
			if json.Unmarshal(raw, &req) == nil {
				router.HandleFile(lifetime, sender, req)
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return scanner.Err()
}

type explorerStreamSender struct {
	queue workspaceOutputQueue
	out   *os.File
	ctx   context.Context
	fail  context.CancelFunc
}

func (s *explorerStreamSender) send(ctx context.Context, value any, viewer bool, until time.Time) error {
	until = workspaceOutputDeadline(ctx, until)
	ctx, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	return s.queue.send(ctx, viewer, func() error {
		deadline := until
		timeout := 2 * time.Second
		if viewer {
			timeout = workspaceReplyTTL
		}
		if writeLimit := time.Now().Add(timeout); writeLimit.Before(deadline) {
			deadline = writeLimit
		}
		if err := s.out.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer func() { _ = s.out.SetWriteDeadline(time.Time{}) }()
		interrupted := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { _ = s.out.SetWriteDeadline(time.Now()); close(interrupted) })
		defer func() {
			if !stop() {
				<-interrupted
			}
		}()
		err := json.NewEncoder(s.out).Encode(value)
		if err != nil {
			_ = s.out.Close()
			if s.fail != nil {
				s.fail()
			}
		} // A partial JSON frame poisons this stream.
		return err
	})
}
func (s *explorerStreamSender) Send(value any) error { return s.send(s.ctx, value, false, time.Time{}) }
func (s *explorerStreamSender) SendWorkspaceExplorerResultContext(ctx context.Context, res protocol.WorkspaceExplorerResult) error {
	return s.send(ctx, res, false, time.Time{})
}
func (s *explorerStreamSender) SendWorkspaceFileResultContext(ctx context.Context, res protocol.WorkspaceFileResult, until time.Time) error {
	return s.send(ctx, res, true, until)
}
func (s *explorerStreamSender) SendWorkspaceExplorerResult(res protocol.WorkspaceExplorerResult) error {
	return s.send(s.ctx, res, false, time.Time{})
}
func (s *explorerStreamSender) SendWorkspaceFileResultBy(res protocol.WorkspaceFileResult, until time.Time) error {
	return s.send(s.ctx, res, true, until)
}
