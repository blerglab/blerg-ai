package daemon

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// pretendForeign makes ownedByDaemon report "another user" for any file
// whose base name is in names (everything else keeps the real answer).
func pretendForeign(t *testing.T, names ...string) {
	t.Helper()
	orig := ownedByDaemon
	t.Cleanup(func() { ownedByDaemon = orig })
	ownedByDaemon = func(info fs.FileInfo) bool {
		for _, n := range names {
			if info.Name() == n {
				return false
			}
		}
		return orig(info)
	}
}

// mkdirMode creates p with exactly mode (chmod after mkdir: umask).
func mkdirMode(t *testing.T, p string, mode fs.FileMode) {
	t.Helper()
	mustMkdir(t, p)
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func permOf(t *testing.T, p string) fs.FileMode {
	t.Helper()
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestPrepareReposRootRefusesFoldersOthersControl(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		name    string
		mode    fs.FileMode
		foreign bool
		wantErr string // "" = accepted
	}{
		{name: "another user's world-writable folder (a /tmp-like scratch dir)", mode: 0o777, foreign: true, wantErr: "belongs to another user"},
		{name: "another user's sticky world-writable folder (/tmp itself)", mode: 0o777 | fs.ModeSticky, foreign: true, wantErr: "belongs to another user"},
		{name: "another user's group-writable folder", mode: 0o775, foreign: true, wantErr: "belongs to another user"},
		{name: "own world-writable folder without sticky bit", mode: 0o777, wantErr: "any user"},
		{name: "own world-writable sticky folder", mode: 0o777 | fs.ModeSticky},
		{name: "own group-writable folder (user-private groups)", mode: 0o775},
		{name: "own private folder", mode: 0o700},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "root" + string(rune('a'+i))
			dir := filepath.Join(base, name)
			mkdirMode(t, dir, tc.mode)
			if tc.foreign {
				pretendForeign(t, name)
			}
			got, err := PrepareReposRoot(dir)
			if tc.wantErr == "" {
				if err != nil || got != dir {
					t.Fatalf("PrepareReposRoot = (%q, %v), want it accepted", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("PrepareReposRoot = (%q, %v), want an error containing %q", got, err, tc.wantErr)
			}
		})
	}
}

// A .blerg-runner directory already in the chosen folder is checked before
// the folder is accepted: someone else's is refused, never adopted; a
// symlink is refused; one of ours that is too open is tightened.
func TestPrepareReposRootChecksExistingStateDir(t *testing.T) {
	t.Run("another user's .blerg-runner is refused", func(t *testing.T) {
		root := t.TempDir()
		mkdirMode(t, filepath.Join(root, ".blerg-runner"), 0o777)
		pretendForeign(t, ".blerg-runner")
		if _, err := PrepareReposRoot(root); err == nil || !strings.Contains(err.Error(), "owned by another user") {
			t.Fatalf("err = %v, want a refusal naming the foreign owner", err)
		}
	})
	t.Run("a .blerg-runner symlink is refused", func(t *testing.T) {
		root := t.TempDir()
		elsewhere := t.TempDir()
		mustSymlink(t, elsewhere, filepath.Join(root, ".blerg-runner"))
		if _, err := PrepareReposRoot(root); err == nil || !strings.Contains(err.Error(), "not a plain directory") {
			t.Fatalf("err = %v, want a refusal", err)
		}
	})
	t.Run("another user's sessions dir inside ours is refused", func(t *testing.T) {
		root := t.TempDir()
		mkdirMode(t, filepath.Join(root, ".blerg-runner"), 0o700)
		mkdirMode(t, filepath.Join(root, ".blerg-runner", "sessions"), 0o777)
		pretendForeign(t, "sessions")
		if _, err := PrepareReposRoot(root); err == nil || !strings.Contains(err.Error(), "owned by another user") {
			t.Fatalf("err = %v, want a refusal", err)
		}
	})
	t.Run("our own too-open .blerg-runner is tightened to 0700", func(t *testing.T) {
		root := t.TempDir()
		state := filepath.Join(root, ".blerg-runner")
		mkdirMode(t, state, 0o777)
		mkdirMode(t, filepath.Join(state, "sessions"), 0o755)
		if _, err := PrepareReposRoot(root); err != nil {
			t.Fatal(err)
		}
		for _, d := range []string{state, filepath.Join(state, "sessions")} {
			if p := permOf(t, d); p != 0o700 {
				t.Errorf("%s is %o, want 700", d, p)
			}
		}
	})
}

// Record I/O never trusts a records directory — or a record — that is not
// the daemon user's own, and never relies on MkdirAll's silence about an
// existing directory.
func TestSessionRecordsRefuseForeignStateDir(t *testing.T) {
	root := t.TempDir()
	mkdirMode(t, filepath.Join(root, ".blerg-runner"), 0o777)
	mkdirMode(t, filepath.Join(root, ".blerg-runner", "sessions"), 0o777)
	// A record planted there (as the other user would have).
	planted := SessionRecord{Version: recordSchemaVersion, SessionID: "planted", ProjectPath: "/", DangerouslySkipPermissions: true, InitialPrompt: "exfiltrate"}
	data, _ := json.Marshal(planted)
	if err := os.WriteFile(recordPath(root, "planted"), data, 0o666); err != nil {
		t.Fatal(err)
	}
	pretendForeign(t, ".blerg-runner")

	if err := writeSessionRecord(root, SessionRecord{Version: recordSchemaVersion, SessionID: "mine", SessionToken: "secret"}); err == nil ||
		!strings.Contains(err.Error(), "owned by another user") {
		t.Errorf("write into a foreign state dir: err = %v, want a refusal", err)
	}
	if _, err := os.Stat(recordPath(root, "mine")); !os.IsNotExist(err) {
		t.Error("a record carrying a token was written into another user's directory")
	}
	if recs := listSessionRecords(root); len(recs) != 0 {
		t.Errorf("records read from a foreign state dir: %+v", recs)
	}
}

func TestReadSessionRecordRefusesForeignFile(t *testing.T) {
	root := t.TempDir()
	if err := writeSessionRecord(root, SessionRecord{Version: recordSchemaVersion, SessionID: "theirs"}); err != nil {
		t.Fatal(err)
	}
	pretendForeign(t, "theirs.json")
	if _, err := readSessionRecord(root, "theirs"); err == nil {
		t.Error("a record owned by another user was read")
	}
	if recs := listSessionRecords(root); len(recs) != 0 {
		t.Errorf("listSessionRecords returned %+v", recs)
	}
}

// Our own records directory left too open (an older daemon, a manual chmod)
// is tightened before it is used, not used as is.
func TestSessionRecordsTightenOwnOpenStateDir(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, ".blerg-runner")
	mkdirMode(t, state, 0o777)
	mkdirMode(t, filepath.Join(state, "sessions"), 0o777)
	if err := writeSessionRecord(root, SessionRecord{Version: recordSchemaVersion, SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{state, filepath.Join(state, "sessions")} {
		if p := permOf(t, d); p != 0o700 {
			t.Errorf("%s is %o after a write, want 700", d, p)
		}
	}
}

// The exact interleaving from review: a kill that lands after the new root
// is live but before the records have moved must leave the session dead —
// its record must not be moved into the new root for recovery to revive.
func TestKillDuringRepoRootChangeStaysDead(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	if err := writeSessionRecord(oldRoot, SessionRecord{Version: recordSchemaVersion, SessionID: "victim", ProjectPath: oldRoot}); err != nil {
		t.Fatal(err)
	}
	mgr := newManagerWithSender(newRecordingSender(64), ManagerConfig{
		ReposRoot:        oldRoot,
		ReposRootSetting: NewReposRootSetting(oldRoot, filepath.Join(t.TempDir(), daemonSettingsFile)),
		Command:          []string{"unused"},
	})

	killDone := make(chan struct{})
	mgr.beforeRecordMigration = func() {
		go func() {
			defer close(killDone)
			raw, _ := json.Marshal(protocol.KillSession{Type: "kill_session", SessionID: "victim"})
			mgr.handleKillSession(raw)
		}()
		// Give the kill every chance to run inside the window.
		time.Sleep(100 * time.Millisecond)
	}
	res := mgr.setReposRoot(protocol.SetReposRoot{Type: "set_repos_root", RequestID: "r", ReposRoot: newRoot})
	if !res.OK {
		t.Fatalf("change refused: %+v", res)
	}
	select {
	case <-killDone:
	case <-time.After(5 * time.Second):
		t.Fatal("kill never finished")
	}
	for _, r := range []string{oldRoot, newRoot} {
		if _, err := os.Stat(recordPath(r, "victim")); !os.IsNotExist(err) {
			t.Errorf("killed session's record survives under %s (err=%v): a reattach would revive it", r, err)
		}
	}
	if recs := listSessionRecords(newRoot); len(recs) != 0 {
		t.Errorf("recovery would find %+v", recs)
	}
}

// Two changes at once: each moves records from the root that was current
// when IT applied, so every record ends up under the final root.
func TestConcurrentRepoRootChangesKeepRecordsWithTheFinalRoot(t *testing.T) {
	start := t.TempDir()
	a, b := t.TempDir(), t.TempDir()
	for _, id := range []string{"s1", "s2"} {
		if err := writeSessionRecord(start, SessionRecord{Version: recordSchemaVersion, SessionID: id}); err != nil {
			t.Fatal(err)
		}
	}
	mgr := newManagerWithSender(newRecordingSender(64), ManagerConfig{
		ReposRoot:        start,
		ReposRootSetting: NewReposRootSetting(start, filepath.Join(t.TempDir(), daemonSettingsFile)),
		Command:          []string{"unused"},
	})
	var wg sync.WaitGroup
	for _, r := range []string{a, b} {
		wg.Add(1)
		go func(r string) {
			defer wg.Done()
			if res := mgr.setReposRoot(protocol.SetReposRoot{Type: "set_repos_root", ReposRoot: r}); !res.OK {
				t.Errorf("change to %s refused: %s", r, res.Error)
			}
		}(r)
	}
	wg.Wait()
	final := mgr.reposRoot()
	for _, r := range []string{start, a, b} {
		n := len(listSessionRecords(r))
		if r == final && n != 2 {
			t.Errorf("final root %s holds %d records, want 2", r, n)
		}
		if r != final && n != 0 {
			t.Errorf("stale root %s still holds %d records", r, n)
		}
	}
}
