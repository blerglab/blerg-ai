package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWriteReadSessionRecord(t *testing.T) {
	root := t.TempDir()
	rec := SessionRecord{
		Version:                    recordSchemaVersion,
		SessionID:                  "abc123",
		Repo:                       "myrepo",
		ProjectPath:                "/home/user/myrepo",
		Model:                      "claude-sonnet",
		DangerouslySkipPermissions: true,
		Title:                      "Test session",
		InitialPrompt:              "Hello world",
		CreatedAt:                  "2026-06-12T10:00:00Z",
		UpdatedAt:                  "2026-06-12T10:01:00Z",
	}

	if err := writeSessionRecord(root, rec); err != nil {
		t.Fatalf("writeSessionRecord: %v", err)
	}

	got, err := readSessionRecord(root, "abc123")
	if err != nil {
		t.Fatalf("readSessionRecord: %v", err)
	}

	if !reflect.DeepEqual(got, rec) {
		t.Errorf("round-trip mismatch\ngot:  %+v\nwant: %+v", got, rec)
	}
}

func TestWriteSessionRecordCreatesDir(t *testing.T) {
	root := t.TempDir()
	// Ensure no .blerg-runner dir yet.
	os.RemoveAll(filepath.Join(root, ".blerg-runner"))

	rec := SessionRecord{
		Version:   recordSchemaVersion,
		SessionID: "newid",
		Repo:      "r",
		CreatedAt: "2026-06-12T00:00:00Z",
		UpdatedAt: "2026-06-12T00:00:00Z",
	}
	if err := writeSessionRecord(root, rec); err != nil {
		t.Fatalf("writeSessionRecord: %v", err)
	}

	want := recordPath(root, "newid")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected file %s to exist, got: %v", want, err)
	}
}

func TestDeleteSessionRecord(t *testing.T) {
	root := t.TempDir()
	rec := SessionRecord{
		Version:   recordSchemaVersion,
		SessionID: "todelete",
		Repo:      "r",
		CreatedAt: "2026-06-12T00:00:00Z",
		UpdatedAt: "2026-06-12T00:00:00Z",
	}
	if err := writeSessionRecord(root, rec); err != nil {
		t.Fatalf("writeSessionRecord: %v", err)
	}

	deleteSessionRecord(root, "todelete")

	p := recordPath(root, "todelete")
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected file to be gone, got stat err: %v", err)
	}

	// Deleting a missing record should be a no-op (no panic, no error surfaced).
	deleteSessionRecord(root, "todelete")
	deleteSessionRecord(root, "doesnotexist")
}

func TestListSessionRecordsSkipsCorrupt(t *testing.T) {
	root := t.TempDir()

	valid := SessionRecord{
		Version:   recordSchemaVersion,
		SessionID: "validid",
		Repo:      "r",
		CreatedAt: "2026-06-12T00:00:00Z",
		UpdatedAt: "2026-06-12T00:00:00Z",
	}
	if err := writeSessionRecord(root, valid); err != nil {
		t.Fatalf("writeSessionRecord: %v", err)
	}

	// Write a corrupt file directly.
	corruptPath := filepath.Join(recordsDir(root), "corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("not json"), 0o600); err != nil {
		t.Fatalf("writing corrupt file: %v", err)
	}

	recs := listSessionRecords(root)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d: %v", len(recs), recs)
	}
	if recs[0].SessionID != "validid" {
		t.Errorf("expected sessionID validid, got %q", recs[0].SessionID)
	}
}

func TestRecordedSessionIDs(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"id1", "id2"} {
		rec := SessionRecord{
			Version:   recordSchemaVersion,
			SessionID: id,
			Repo:      "r",
			CreatedAt: "2026-06-12T00:00:00Z",
			UpdatedAt: "2026-06-12T00:00:00Z",
		}
		if err := writeSessionRecord(root, rec); err != nil {
			t.Fatalf("writeSessionRecord(%s): %v", id, err)
		}
	}

	ids := recordedSessionIDs(root)
	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d: %v", len(ids), ids)
	}
	idSet := make(map[string]struct{})
	for _, id := range ids {
		idSet[id] = struct{}{}
	}
	for _, want := range []string{"id1", "id2"} {
		if _, ok := idSet[want]; !ok {
			t.Errorf("expected ID %q in result %v", want, ids)
		}
	}
}

func TestListSessionRecordsMissingDir(t *testing.T) {
	root := t.TempDir()
	// No .blerg-runner dir at all.
	recs := listSessionRecords(root)
	if len(recs) != 0 {
		t.Errorf("expected empty slice, got %v", recs)
	}
}

// TestTranscriptExistsTrue places a transcript under a project directory whose
// name does NOT match any "/"→"-" escaping of the project path, proving the glob
// finds it by session ID regardless of Claude's directory-naming scheme.
func TestTranscriptExistsTrue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionID := "sess42"
	// Deliberately use an opaque dir name unrelated to any project path.
	dir := filepath.Join(home, ".claude", "projects", "-some-escaped_name.with.dots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if !transcriptExists(sessionID) {
		t.Error("transcriptExists returned false, expected true")
	}
}

func TestTranscriptExistsFalse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if transcriptExists("nosuchid") {
		t.Error("transcriptExists returned true, expected false")
	}
}

// TestSessionRecordAssistRoundTrip verifies that Assist fields survive a
// write/read cycle and that a recovered Assist session rebuilds the correct env
// (board vars present, no BLERG_RUNNER_DAEMON_TOKEN).
func TestSessionRecordAssistRoundTrip(t *testing.T) {
	root := t.TempDir()

	rec := SessionRecord{
		Version:     recordSchemaVersion,
		SessionID:   "assist-sess",
		Repo:        "myrepo",
		ProjectPath: "/home/user/myrepo",
		Assist:      true,
		BoardID:     "board-42",
		TicketID:    "ticket-7",
		BoardToken:  "btok-secret",
		// Per-session messaging token and caller env survive a restart too,
		// so a recovered session can still `blerg-runner ask`.
		SessionToken: "sess-tok-recovered",
		ExtraEnv:     map[string]string{"BLERG_BOARD_URL": "http://localhost:8082"},
		CreatedAt:    "2026-06-30T10:00:00Z",
		UpdatedAt:    "2026-06-30T10:01:00Z",
	}

	if err := writeSessionRecord(root, rec); err != nil {
		t.Fatalf("writeSessionRecord: %v", err)
	}

	got, err := readSessionRecord(root, "assist-sess")
	if err != nil {
		t.Fatalf("readSessionRecord: %v", err)
	}

	if !reflect.DeepEqual(got, rec) {
		t.Errorf("round-trip mismatch\ngot:  %+v\nwant: %+v", got, rec)
	}

	// Simulate daemon process having BLERG_RUNNER_DAEMON_TOKEN in env.
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "ambient-daemon-tok")

	mgr := newManagerWithSender(newRecordingSender(4), ManagerConfig{
		DaemonToken: "config-daemon-tok",
	})

	// Build env using the recovered record's Assist fields.
	env := mgr.sessionEnv(got.SessionID, got.Assist, got.BoardID, got.BoardToken, got.SessionToken, got.ExtraEnv)

	// Board vars must be present.
	for _, needle := range []string{"BLERG_RUNNER_BOARD_ID=board-42", "BLERG_RUNNER_BOARD_TOKEN=btok-secret"} {
		found := false
		for _, e := range env {
			if e == needle {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("recovered assist env missing %q; env = %v", needle, env)
		}
	}

	// BLERG_RUNNER_DAEMON_TOKEN must be absent.
	for _, e := range env {
		if strings.HasPrefix(e, "BLERG_RUNNER_DAEMON_TOKEN=") {
			t.Errorf("recovered assist env must not contain BLERG_RUNNER_DAEMON_TOKEN; got %q", e)
		}
	}
	// The recovered session token and extra env are reinjected.
	for _, needle := range []string{"BLERG_RUNNER_SESSION_TOKEN=sess-tok-recovered", "BLERG_BOARD_URL=http://localhost:8082"} {
		if !hasEntry(env, needle) {
			t.Errorf("recovered env missing %q; env = %v", needle, env)
		}
	}
}

func TestRecoverableRecords(t *testing.T) {
	records := []SessionRecord{
		{SessionID: "a"},
		{SessionID: "b"},
		{SessionID: "c"},
	}
	liveIDs := []string{"b"}
	managedIDs := []string{"c"}

	got := recoverableRecords(records, liveIDs, managedIDs)
	if len(got) != 1 {
		t.Fatalf("expected 1 recoverable record, got %d: %v", len(got), got)
	}
	if got[0].SessionID != "a" {
		t.Errorf("expected sessionID a, got %q", got[0].SessionID)
	}
}
