package daemon

// A spawn that fails before its session starts must not leave behind a folder
// it created: an empty folder shows up in the launch sheet's repo picker.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func mustExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); err != nil {
		t.Errorf("%s should still exist: %v", p, err)
	}
}

func mustNotExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("%s should be gone (err=%v)", p, err)
	}
}

// failingSpawn runs a spawn that passes workspace preparation and then fails
// the engine preflight (nothing on PATH), and returns the sender's messages.
func failingSpawn(t *testing.T, root string, msg protocol.SpawnSession) []string {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root})
	msg.Type, msg.SessionID = "spawn_session", "s-fail"
	raw, _ := json.Marshal(msg)
	mgr.handleSpawnSession(raw)
	msgs := sender.messages()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1], `"status":"error"`) {
		t.Fatalf("want the spawn to fail with an error, got %v", msgs)
	}
	return msgs
}

func TestFailedSpawnRemovesTheNewFolderItCreated(t *testing.T) {
	root := t.TempDir()
	failingSpawn(t, root, protocol.SpawnSession{Repo: "fresh", NewRepo: true})
	mustNotExist(t, filepath.Join(root, "fresh"))
	mustExist(t, root)
}

func TestFailedSpawnRemovesTheScratchFolderItCreated(t *testing.T) {
	root := t.TempDir()
	failingSpawn(t, root, protocol.SpawnSession{Repo: ".scratch-ember-01ab", NoRepo: true, NewRepo: true})
	mustNotExist(t, filepath.Join(root, ".scratch-ember-01ab"))
}

func TestFailedSpawnRemovesTheOrgParentItCreatedToo(t *testing.T) {
	root := t.TempDir()
	failingSpawn(t, root, protocol.SpawnSession{Repo: "acme/widget", NewRepo: true})
	mustNotExist(t, filepath.Join(root, "acme"))
}

func TestFailedSpawnKeepsAnOrgParentThatExisted(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "acme", "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	failingSpawn(t, root, protocol.SpawnSession{Repo: "acme/widget", NewRepo: true})
	mustNotExist(t, filepath.Join(root, "acme", "widget"))
	mustExist(t, filepath.Join(root, "acme", "other"))
}

func TestFailedSpawnLeavesAPreexistingEmptyFolderAlone(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	failingSpawn(t, root, protocol.SpawnSession{Repo: "mine", NewRepo: true})
	mustExist(t, filepath.Join(root, "mine"))
}

func TestFailedSpawnLeavesAFolderWithContentAlone(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "app")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	failingSpawn(t, root, protocol.SpawnSession{Repo: "app"})
	mustExist(t, filepath.Join(dir, "main.go"))
}

// If something writes into the folder between its creation and the failure,
// it is no longer empty and is kept.
func TestRemoveCreatedWorkspaceKeepsANonEmptyFolder(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "made")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeCreatedWorkspace(root, createdDirs{leaf: dir, top: dir})
	mustExist(t, filepath.Join(dir, "f"))
}

func TestRemoveCreatedWorkspaceNeverTouchesPathsOutsideTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repos")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]createdDirs{
		"outside":            {leaf: outside, top: outside},
		"the root":           {leaf: root, top: root},
		"dotdot":             {leaf: filepath.Join(root, "..", "elsewhere"), top: filepath.Join(root, "..", "elsewhere")},
		"sibling prefix":     {leaf: root + "-x", top: root + "-x"},
		"top not above leaf": {leaf: filepath.Join(root, "a"), top: filepath.Join(root, "b")},
	} {
		if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
			t.Fatal(err)
		}
		removeCreatedWorkspace(root, c)
		mustExist(t, outside)
		mustExist(t, root)
		mustExist(t, filepath.Join(root, "a"))
		_ = name
	}
}

func TestRemoveCreatedWorkspaceDoesNotFollowASymlink(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir() // empty, outside the root
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	removeCreatedWorkspace(root, createdDirs{leaf: link, top: link})
	mustExist(t, link)
	mustExist(t, target)
}

func TestRemoveCreatedWorkspaceZeroValueDoesNothing(t *testing.T) {
	root := t.TempDir()
	removeCreatedWorkspace(root, createdDirs{})
	mustExist(t, root)
}
