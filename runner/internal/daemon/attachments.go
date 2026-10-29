package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const attachmentsDirEnv = "BLERG_RUNNER_ATTACHMENTS_DIR"
const attachmentsSweepGrace = 24 * time.Hour
const attachmentsSweepLimit = 128

// A pod provides its private home explicitly; host sessions use daemon state.
func attachmentsStateDir(override string) string {
	if override != "" {
		return override
	}
	return DaemonStateDir()
}

func sessionAttachmentsPath(state, id string) string {
	if state == "" || id == "" || safeFileName(id) != id || id == "." || id == ".." {
		return ""
	}
	path, err := filepath.Abs(filepath.Join(state, "sessions", id, "attachments"))
	if err != nil {
		return ""
	}
	return path
}

// removeSessionAttachments opens the sessions root before inspecting/deleting.
// Root operations stay confined even if an agent replaces a directory with a
// symlink during cleanup. RemoveAll unlinks nested symlinks without following them.
// Only our attachments child is removed; explicit fetch --out paths are unknown here.
func removeSessionAttachments(state, id string) {
	path := sessionAttachmentsPath(state, id)
	if path == "" {
		return
	}
	rootPath := filepath.Dir(filepath.Dir(path))
	info, err := os.Lstat(rootPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(info, pinned) {
		return
	}
	sessionInfo, err := root.Lstat(id)
	if err != nil || !sessionInfo.IsDir() || sessionInfo.Mode()&os.ModeSymlink != 0 {
		return
	}
	sessionRoot, err := root.OpenRoot(id)
	if err != nil {
		return
	}
	defer func() { _ = sessionRoot.Close() }()
	pinned, err = sessionRoot.Stat(".")
	if err != nil || !os.SameFile(sessionInfo, pinned) {
		return
	}
	child, err := sessionRoot.Lstat("attachments")
	if err != nil || !child.IsDir() || child.Mode()&os.ModeSymlink != 0 {
		return
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != filepath.Join(resolvedRoot, id, "attachments") {
		return
	}
	if err := sessionRoot.RemoveAll("attachments"); err != nil {
		log.Printf("attachments: private session cleanup failed: %v", err)
		return
	}
	// Drop only an empty session directory, keeping unrelated session state.
	_ = root.Remove(id)
}

// Keep the directory stream between bounded batches so retained or fresh
// entries cannot starve later sessions. Run closes it on shutdown; EOF starts
// a new pass on the next sweep, including folders skipped on earlier passes.
type attachmentSweepCursor struct {
	dir  *os.File
	info os.FileInfo
}

func (c *attachmentSweepCursor) close() {
	if c.dir != nil {
		_ = c.dir.Close()
	}
	c.dir, c.info = nil, nil
}

// attachmentSweepCandidates bounds directory scanning and each server request.
// Errors, fresh directories and symlinked directories are retained.
func attachmentSweepCandidates(state string, now time.Time, cursor *attachmentSweepCursor) []string {
	if cursor == nil {
		cursor = &attachmentSweepCursor{}
		defer cursor.close()
	}
	rootPath := filepath.Join(state, "sessions")
	info, err := os.Lstat(rootPath)
	if state == "" || err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		cursor.close()
		return nil
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		cursor.close()
		return nil
	}
	defer func() { _ = root.Close() }()
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(info, pinned) {
		cursor.close()
		return nil
	}
	if cursor.dir != nil && !os.SameFile(info, cursor.info) {
		cursor.close()
	}
	if cursor.dir == nil {
		cursor.dir, err = root.Open(".")
		if err != nil {
			return nil
		}
		cursor.info = info
	}
	entries, err := cursor.dir.ReadDir(attachmentsSweepLimit)
	if err != nil {
		cursor.close()
		if !errors.Is(err, io.EOF) {
			return nil
		}
	}
	var ids []string
	for _, entry := range entries {
		id := entry.Name()
		if entry.IsDir() && sessionAttachmentsPath(state, id) != "" && attachmentPastGrace(root, id, now) {
			ids = append(ids, id)
		}
	}
	return ids
}

func attachmentPastGrace(root *os.Root, id string, now time.Time) bool {
	session, err := root.Lstat(id)
	if err != nil || !session.IsDir() || session.Mode()&os.ModeSymlink != 0 {
		return false
	}
	info, err := root.Lstat(filepath.Join(id, "attachments"))
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && now.Sub(info.ModTime()) >= attachmentsSweepGrace
}

// AttachmentCleaner asks the authenticated server which old local folders may go.
// A missing reply (older/offline server) never grants permission to delete.
type AttachmentCleaner struct {
	client     *WSClient
	active     func() []string
	state      string
	mu         sync.Mutex
	request    string
	candidates []string
	cursor     attachmentSweepCursor
}

func NewAttachmentCleaner(client *WSClient, active func() []string, state string) *AttachmentCleaner {
	c := &AttachmentCleaner{client: client, active: active, state: state}
	client.OnMessage("attachment_cleanup_result", c.handleResult)
	return c
}

// Run continues startup cleanup even on a stable connection. Each tick advances
// one batch, and a disconnected/older server never authorizes deletion.
func (c *AttachmentCleaner) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	defer c.close()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sweep(ctx)
		}
	}
}

func (c *AttachmentCleaner) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cursor.close()
}

func (c *AttachmentCleaner) Sweep() { c.sweep(context.Background()) }

func (c *AttachmentCleaner) sweep(ctx context.Context) {
	c.mu.Lock()
	ids := attachmentSweepCandidates(c.state, time.Now(), &c.cursor)
	if len(ids) == 0 {
		c.mu.Unlock()
		return
	}
	c.request, c.candidates = ccUUID(), ids
	request := protocol.AttachmentCleanupRequest{Type: "attachment_cleanup", RequestID: c.request, SessionIDs: ids}
	c.mu.Unlock()
	if err := c.client.SendContext(ctx, request); err != nil {
		log.Printf("attachments: startup sweep deferred: %v", err)
	}
}

func (c *AttachmentCleaner) handleResult(raw []byte) {
	var result protocol.AttachmentCleanupResult
	if len(raw) > 32768 || json.Unmarshal(raw, &result) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.request == "" || result.RequestID != c.request {
		return
	}
	active := c.active()
	// Recheck the grace period at deletion time, including attachments fetched
	// while the server was answering. Never touch a locally active/resumed session.
	rootPath := filepath.Join(c.state, "sessions")
	info, err := os.Lstat(rootPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(info, pinned) {
		return
	}
	now := time.Now()
	for _, id := range result.Removable {
		if slices.Contains(c.candidates, id) && attachmentPastGrace(root, id, now) && !slices.Contains(active, id) {
			removeSessionAttachments(c.state, id)
		}
	}
	c.request, c.candidates = "", nil
}

func sandboxAttachmentsEnv(env []string) string {
	for _, kv := range env {
		if id, ok := strings.CutPrefix(kv, "BLERG_RUNNER_SESSION_ID="); ok {
			return sessionAttachmentsPath("/home/agent/.blerg-runner-daemon", id)
		}
	}
	return ""
}
