package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workbranch"
)

// Branch records live beside the daemon-owned clone records, outside agent
// mounts. Recovery and all Git operations consult the same immutable value.
func recordWorkBranch(msg protocol.SpawnSession) error {
	if msg.WorkBranch == "" || (!msg.Sandbox && msg.Workspace == nil) {
		return nil
	}
	if !sandboxCloneIDPattern.MatchString(msg.SessionID) || !workbranch.Valid(msg.WorkBranch) {
		return errors.New("invalid session work branch")
	}
	root := sandboxCloneRoot()
	if root == "" {
		return errors.New("cannot record session work branch without daemon state")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	name := filepath.Join(root, msg.SessionID+".branch")
	// Publish only a complete record; Link refuses to replace another launch's
	// name, including a concurrent writer's. No recovery reader sees a partial file.
	f, err := os.CreateTemp(root, ".branch-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer func() { _ = os.Remove(temporary) }()
	if _, err = f.WriteString(msg.WorkBranch); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	err = os.Link(temporary, name)
	if errors.Is(err, os.ErrExist) {
		raw, readErr := os.ReadFile(name) //nolint:gosec // daemon-private record derived from a validated session ID
		if readErr != nil || string(raw) != msg.WorkBranch {
			return errors.New("recorded session work branch differs from launch")
		}
		return nil
	}
	return err
}

// sandboxBranch reads the immutable record, retaining the pre-template default.
func sandboxBranch(sessionID string) string {
	if root := sandboxCloneRoot(); root != "" && sandboxCloneIDPattern.MatchString(sessionID) {
		raw, err := os.ReadFile(filepath.Join(root, sessionID+".branch")) //nolint:gosec // validated ID under daemon-private state
		if err == nil {
			if workbranch.Valid(string(raw)) {
				return string(raw)
			}
			return ""
		}
		if !errors.Is(err, os.ErrNotExist) {
			return ""
		}
	}
	return workbranch.Effective(sessionID, "")
}

func shellBranch(sessionID string) string {
	return "'refs/heads/" + strings.ReplaceAll(sandboxBranch(sessionID), "'", "'\"'\"'") + "'"
}
