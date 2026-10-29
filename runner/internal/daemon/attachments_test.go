package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func attachmentFixture(t *testing.T, state, id string, old bool) string {
	t.Helper()
	path := sessionAttachmentsPath(state, id)
	if err := os.MkdirAll(filepath.Join(path, ".reviews"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "screenshot.png"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if old {
		when := time.Now().Add(-48 * time.Hour)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	return path
}
func assertAttachmentsGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("private attachments retained: %v", err)
	}
}

func TestSessionAttachmentsEnvIsPerSession(t *testing.T) {
	state := t.TempDir()
	t.Setenv(DaemonStateDirEnv, state)
	t.Setenv(attachmentsDirEnv, "/inherited/other-session")
	for _, id := range []string{"first", "second"} {
		env := buildSessionEnv(sessionEnvOpts{SessionID: id, Extra: map[string]string{attachmentsDirEnv: "/forged"}})
		want := attachmentsDirEnv + "=" + filepath.Join(state, "sessions", id, "attachments")
		if !slices.Contains(env, want) {
			t.Fatal("missing session attachment path")
		}
		if strings.Count(strings.Join(env, "\n"), attachmentsDirEnv+"=") != 1 {
			t.Fatal("inherited path leaked")
		}
		args := sandboxEnvArgs(env)
		if !slices.Contains(args, attachmentsDirEnv+"=/home/agent/.blerg-runner-daemon/sessions/"+id+"/attachments") {
			t.Fatal("sandbox inherited host attachment path")
		}
	}
}

func TestRemoveSessionAttachmentsConfined(t *testing.T) {
	state := t.TempDir()
	gone := attachmentFixture(t, state, "ended", false)
	running := attachmentFixture(t, state, "running", false)
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(gone, "link")); err != nil {
		t.Fatal(err)
	}
	removeSessionAttachments(state, "ended")
	assertAttachmentsGone(t, gone)
	if _, err := os.Stat(running); err != nil {
		t.Fatal("running session touched", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("nested symlink followed", err)
	}
	for _, id := range []string{"../escape", ".", "..", ""} {
		removeSessionAttachments(state, id)
	}
	// A symlink replacing the session or its attachments must not be traversed.
	if err := os.Symlink(outside, filepath.Join(state, "sessions", "linked")); err != nil {
		t.Fatal(err)
	}
	removeSessionAttachments(state, "linked")
	if err := os.Mkdir(filepath.Join(state, "sessions", "linked-child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(state, "sessions", "linked-child", "attachments")); err != nil {
		t.Fatal(err)
	}
	removeSessionAttachments(state, "linked-child")
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("symlink followed", err)
	}
	// Replacing the sessions root itself must not grant deletion authority.
	otherState := t.TempDir()
	other := attachmentFixture(t, otherState, "ended", false)
	linkedState := t.TempDir()
	if err := os.Symlink(filepath.Join(otherState, "sessions"), filepath.Join(linkedState, "sessions")); err != nil {
		t.Fatal(err)
	}
	removeSessionAttachments(linkedState, "ended")
	if _, err := os.Stat(other); err != nil {
		t.Fatal("symlinked sessions root followed", err)
	}
}

func TestAttachmentStartupSweepOnlyServerAuthorizedOldFolders(t *testing.T) {
	state := t.TempDir()
	paths := map[string]string{}
	for _, id := range []string{"ended", "discarded", "unknown", "running", "local-running", "server-error", "fresh"} {
		paths[id] = attachmentFixture(t, state, id, id != "fresh")
	}
	c := &AttachmentCleaner{state: state, active: func() []string { return []string{"local-running"} }, request: "request-1", candidates: attachmentSweepCandidates(state, time.Now(), nil)}
	raw, _ := json.Marshal(protocol.AttachmentCleanupResult{RequestID: "wrong", Removable: []string{"ended"}})
	c.handleResult(raw)
	if _, err := os.Stat(paths["ended"]); err != nil {
		t.Fatal("uncorrelated reply deleted files")
	}
	raw, _ = json.Marshal(protocol.AttachmentCleanupResult{RequestID: "request-1", Removable: []string{"ended", "discarded", "unknown", "local-running", "fresh", "../escape"}})
	c.handleResult(raw)
	for _, id := range []string{"ended", "discarded", "unknown"} {
		assertAttachmentsGone(t, paths[id])
	}
	for _, id := range []string{"running", "local-running", "server-error", "fresh"} {
		if _, err := os.Stat(paths[id]); err != nil {
			t.Fatalf("%s touched: %v", id, err)
		}
	}
}

func TestAttachmentStartupSweepBounded(t *testing.T) {
	state := t.TempDir()
	for i := range attachmentsSweepLimit + 5 {
		attachmentFixture(t, state, fmt.Sprintf("session-%d", i), true)
	}
	if got := len(attachmentSweepCandidates(state, time.Now(), nil)); got > attachmentsSweepLimit {
		t.Fatalf("unbounded scan: %d", got)
	}
}

// The server delivers Discard through kill_session, including for a session
// that ended before this daemon reconnected (no in-memory runtime remains).
func TestDiscardKillDeletesPrivateAttachments(t *testing.T) {
	state := t.TempDir()
	t.Setenv(DaemonStateDirEnv, state)
	discarded := attachmentFixture(t, state, "discarded", false)
	running := attachmentFixture(t, state, "running", false)
	explicit := filepath.Join(state, "chosen-output")
	if err := os.Mkdir(explicit, 0o700); err != nil {
		t.Fatal(err)
	}
	manager := newManagerWithSender(newRecordingSender(8), ManagerConfig{ReposRoot: t.TempDir(), Command: []string{"true"}})
	raw, _ := json.Marshal(protocol.KillSession{Type: "kill_session", SessionID: "discarded"})
	manager.handleKillSession(raw)
	waitManagerLifecycle(t, manager, "discarded")
	assertAttachmentsGone(t, discarded)
	for _, path := range []string{running, explicit} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("unrelated attachment folder touched", err)
		}
	}
}

func TestAttachmentStartupSweepDisconnectedRetainsFolders(t *testing.T) {
	state := t.TempDir()
	path := attachmentFixture(t, state, "old", true)
	client := NewWSClient(Config{})
	cleaner := NewAttachmentCleaner(client, func() []string { return nil }, state)
	t.Cleanup(cleaner.close)
	cleaner.Sweep() // Disconnected: a send failure must not authorize local cleanup.
	if _, err := os.Stat(path); err != nil {
		t.Fatal("offline sweep deleted attachments", err)
	}
	if !slices.Contains(cleaner.candidates, "old") || cleaner.request == "" {
		t.Fatal("startup sweep did not request server state")
	}
}

func TestClusterSessionAttachmentsStayOutsideWorkspace(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, "/workspace/.blerg-runner-daemon")
	env := buildSessionEnv(sessionEnvOpts{SessionID: "pod-session", AttachmentsStateDir: "/home/agent/.blerg-runner-daemon"})
	want := attachmentsDirEnv + "=/home/agent/.blerg-runner-daemon/sessions/pod-session/attachments"
	if !slices.Contains(env, want) {
		t.Fatal("pod attachment path reverted to workspace HOME")
	}
}

// A retained first batch must not starve old ended/unknown sessions behind it.
func TestAttachmentStartupSweepProgressPastRetainedBatch(t *testing.T) {
	state := t.TempDir()
	for i := range attachmentsSweepLimit + 5 {
		attachmentFixture(t, state, fmt.Sprintf("session-%d", i), true)
	}
	client := NewWSClient(Config{})
	cleaner := NewAttachmentCleaner(client, func() []string { return nil }, state)
	t.Cleanup(cleaner.close)
	cleaner.Sweep()
	first := slices.Clone(cleaner.candidates)
	if len(first) != attachmentsSweepLimit {
		t.Fatalf("first batch: %d", len(first))
	}
	var tail string
	for i := range attachmentsSweepLimit + 5 {
		id := fmt.Sprintf("session-%d", i)
		if !slices.Contains(first, id) {
			tail = id
			break
		}
	}
	// Simulate a server retaining every session in the first batch.
	raw, _ := json.Marshal(protocol.AttachmentCleanupResult{RequestID: cleaner.request})
	cleaner.handleResult(raw)
	for range 4 {
		cleaner.Sweep()
		if len(cleaner.candidates) > attachmentsSweepLimit {
			t.Fatal("unbounded batch")
		}
		raw, _ = json.Marshal(protocol.AttachmentCleanupResult{RequestID: cleaner.request, Removable: []string{tail}})
		cleaner.handleResult(raw)
		if _, err := os.Stat(sessionAttachmentsPath(state, tail)); os.IsNotExist(err) {
			for _, id := range first {
				if _, err := os.Stat(sessionAttachmentsPath(state, id)); err != nil {
					t.Fatal("retained folder removed", err)
				}
			}
			return
		}
	}
	t.Fatalf("old eligible folder %s starved behind retained batch", tail)
}

func TestAttachmentSweepRechecksLaterBatchAndRetriesSkippedFolders(t *testing.T) {
	state := t.TempDir()
	for i := range attachmentsSweepLimit + 5 {
		attachmentFixture(t, state, fmt.Sprintf("session-%d", i), true)
	}
	active := []string{}
	cleaner := NewAttachmentCleaner(NewWSClient(Config{}), func() []string { return active }, state)
	t.Cleanup(cleaner.close)
	cleaner.Sweep()
	cleaner.Sweep()
	later := slices.Clone(cleaner.candidates)
	if len(later) != 5 {
		t.Fatalf("later batch: %v", later)
	}
	// The server authorizes all five, but current local activity/age and links
	// must still protect files fetched/resumed/replaced while it was answering.
	active = []string{later[0]}
	fresh := sessionAttachmentsPath(state, later[1])
	now := time.Now()
	if err := os.Chtimes(fresh, now, now); err != nil {
		t.Fatal(err)
	}
	linked := sessionAttachmentsPath(state, later[2])
	outside := t.TempDir()
	if err := os.RemoveAll(linked); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(protocol.AttachmentCleanupResult{RequestID: cleaner.request, Removable: later[:4]})
	cleaner.handleResult(raw)
	for _, id := range []string{later[0], later[1], later[2], later[4]} {
		if _, err := os.Lstat(sessionAttachmentsPath(state, id)); err != nil {
			t.Fatalf("protected %s removed: %v", id, err)
		}
	}
	assertAttachmentsGone(t, sessionAttachmentsPath(state, later[3]))
	// After EOF, later passes must retry entries previously retained by age.
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(fresh, old, old); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		cleaner.Sweep()
		if slices.Contains(cleaner.candidates, later[1]) {
			raw, _ = json.Marshal(protocol.AttachmentCleanupResult{RequestID: cleaner.request, Removable: []string{later[1]}})
			cleaner.handleResult(raw)
			assertAttachmentsGone(t, fresh)
			return
		}
	}
	t.Fatal("previously fresh folder was not retried after EOF")
}

func TestAttachmentSweepCursorRejectsReplacedRootAndClosesOnShutdown(t *testing.T) {
	state := t.TempDir()
	attachmentFixture(t, state, "first", true)
	cleaner := NewAttachmentCleaner(NewWSClient(Config{}), func() []string { return nil }, state)
	t.Cleanup(cleaner.close)
	cleaner.Sweep()
	if cleaner.cursor.dir == nil {
		t.Fatal("no directory cursor")
	}
	original := cleaner.cursor.dir
	outside := t.TempDir()
	saved := attachmentFixture(t, outside, "keep", true)
	if err := os.Rename(filepath.Join(state, "sessions"), filepath.Join(state, "old-sessions")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "sessions"), filepath.Join(state, "sessions")); err != nil {
		t.Fatal(err)
	}
	cleaner.Sweep()
	if cleaner.cursor.dir != nil {
		t.Fatal("symlinked root retained cursor")
	}
	if _, err := original.Stat(); err == nil {
		t.Fatal("replaced root cursor leaked")
	}
	raw, _ := json.Marshal(protocol.AttachmentCleanupResult{RequestID: cleaner.request, Removable: []string{"keep"}})
	cleaner.handleResult(raw)
	if _, err := os.Stat(saved); err != nil {
		t.Fatal("symlinked root followed", err)
	}
	if err := os.Remove(filepath.Join(state, "sessions")); err != nil {
		t.Fatal(err)
	}
	attachmentFixture(t, state, "replacement", true)
	cleaner.Sweep()
	if !slices.Contains(cleaner.candidates, "replacement") {
		t.Fatal("replacement root not scanned")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cleaner.Run(ctx)
	if cleaner.cursor.dir != nil {
		t.Fatal("shutdown leaked cursor")
	}
}

// Virtual time proves cleanup keeps advancing on a stable connection without
// wall-clock waits or repeated reconnect callbacks.
func TestAttachmentSweepContinuesWithoutReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := t.TempDir()
		for i := range attachmentsSweepLimit + 5 {
			attachmentFixture(t, state, fmt.Sprintf("session-%d", i), true)
		}
		cleaner := NewAttachmentCleaner(NewWSClient(Config{}), func() []string { return nil }, state)
		cleaner.Sweep()
		first := slices.Clone(cleaner.candidates)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); cleaner.Run(ctx) }()
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()
		cleaner.mu.Lock()
		later := slices.Clone(cleaner.candidates)
		cleaner.mu.Unlock()
		if len(later) != 5 {
			t.Fatalf("periodic batch: %v", later)
		}
		for _, id := range later {
			if slices.Contains(first, id) {
				t.Fatal("periodic sweep repeated retained prefix")
			}
		}
		cancel()
		<-done
		if cleaner.cursor.dir != nil {
			t.Fatal("periodic worker leaked cursor")
		}
	})
}

// A daemon started from inside a session must not pass that session's identity, token or
// attachments folder on to the sessions it starts.
func TestSessionEnvDropsInheritedSessionIdentity(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	t.Setenv("BLERG_RUNNER_SESSION_ID", "inherited-session")
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "inherited-token")
	t.Setenv(attachmentsDirEnv, "/inherited/attachments")
	env := strings.Join(buildSessionEnv(sessionEnvOpts{SessionID: "fresh", SessionToken: "fresh-token"}), "\n")
	for _, leaked := range []string{"inherited-session", "inherited-token", "/inherited/attachments"} {
		if strings.Contains(env, leaked) {
			t.Fatalf("inherited value %q reached the new session", leaked)
		}
	}
	if strings.Count(env, "BLERG_RUNNER_SESSION_ID=") != 1 || !strings.Contains(env, "BLERG_RUNNER_SESSION_ID=fresh") {
		t.Fatal("the new session's id is missing or duplicated")
	}
}
