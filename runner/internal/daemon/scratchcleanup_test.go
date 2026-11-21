package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestScratchRemovedOnlyWhenSessionEndsForGood(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "work"}[keep], func(t *testing.T) {
			host, sender, project := newTestHost(t, &scriptedAgentProvider{}, "")
			path := filepath.Join(filepath.Dir(project), ".scratch-test")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			host.Spawn(protocol.SpawnSession{SessionID: "scratch", Repo: filepath.Base(path), ProjectPath: path, NoRepo: true, Kind: "agent"})
			if !host.Has("scratch") {
				t.Fatal("session did not start")
			}
			mustExist(t, path)
			if keep {
				if err := os.WriteFile(filepath.Join(path, "notes"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			host.Kill("scratch")
			if !keep {
				mustNotExist(t, path)
				return
			}
			mustExist(t, filepath.Join(path, "notes"))
			sender.mu.Lock()
			defer sender.mu.Unlock()
			for _, msg := range sender.msgs {
				if end, ok := msg.(protocol.SessionEnded); ok && strings.Contains(end.Message, path) {
					return
				}
			}
			t.Fatal("retained scratch path was not reported")
		})
	}
}

func TestScratchCleanupKeepsEveryKindOfWork(t *testing.T) {
	for _, kind := range []string{"empty", "unborn", "commit", "other-ref", "dangling", "staged", "untracked", "ignored", "custom-hook", "custom-metadata-type", "git-link", "replaced"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".scratch-test")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			folder := bindScratchFolder(root, path, filepath.Base(path))
			if kind != "empty" && kind != "replaced" {
				childRepoGit(t, path, "init", "-q", "-b", "main")
			}
			write := func(name string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(path, name), []byte("work\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "commit", "other-ref", "dangling":
				childRepoGit(t, path, "commit", "--allow-empty", "-qm", "work")
				if kind != "commit" {
					if kind == "other-ref" {
						childRepoGit(t, path, "branch", "saved")
					}
					childRepoGit(t, path, "update-ref", "-d", "refs/heads/main")
					if kind == "dangling" {
						if err := os.RemoveAll(filepath.Join(path, ".git", "logs")); err != nil {
							t.Fatal(err)
						}
					}
				}
			case "staged":
				write("note")
				childRepoGit(t, path, "add", "note")
				if err := os.Remove(filepath.Join(path, "note")); err != nil {
					t.Fatal(err)
				}
			case "untracked":
				write("note")
			case "ignored":
				if err := os.WriteFile(filepath.Join(path, ".git", "info", "exclude"), []byte("note\n"), 0600); err != nil {
					t.Fatal(err)
				}
				write("note")
			case "custom-hook":
				write(".git/hooks/custom")
			case "custom-metadata-type":
				if err := os.RemoveAll(filepath.Join(path, ".git", "branches")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, ".git", "branches"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "git-link":
				if err := os.Rename(filepath.Join(path, ".git"), filepath.Join(root, "git")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "git"), filepath.Join(path, ".git")); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				if err := os.Rename(path, path+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			keep := kind != "empty" && kind != "unborn"
			var before map[string][]byte
			if keep && kind != "git-link" {
				var err error
				before, err = scratchMetadata(os.DirFS(path))
				if err != nil {
					t.Fatal(err)
				}
			}
			// Inspection has no effect, whether or not removal would be safe.
			_ = folder.inspect(false)
			mustExist(t, path)
			report := folder.remove()
			if !keep {
				if report != "" {
					t.Fatal(report)
				}
				mustNotExist(t, path)
				return
			}
			if !strings.Contains(report, path) {
				t.Fatalf("missing retained path: %q", report)
			}
			mustExist(t, path)
			if before != nil {
				after, err := scratchMetadata(os.DirFS(path))
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatal("retained work changed")
				}
			}
		})
	}
}

func TestScratchBindingRefusesLinksAndOtherFolders(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	path := filepath.Join(root, ".scratch-link")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if bindScratchFolder(root, path, filepath.Base(path)) != nil {
		t.Fatal("bound a symlink")
	}
	if bindScratchFolder(root, target, ".scratch-outside") != nil {
		t.Fatal("bound an outside path")
	}
	if bindScratchFolder(filepath.Dir(target), target, filepath.Base(target)) != nil {
		t.Fatal("bound an ordinary folder")
	}
}

func TestScratchMaintenanceRefusesLiveAndUncertainSessions(t *testing.T) {
	for _, kind := range []string{"dry-run", "apply", "daemon", "record", "corrupt-record", "allocation", "container", "open-file", "unknown-processes"} {
		t.Run(kind, func(t *testing.T) {
			root, state := t.TempDir(), t.TempDir()
			path := filepath.Join(root, ".scratch-old")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			tools := t.TempDir()
			docker := "exit 0"
			lsof := "exit 1"
			if kind == "container" {
				docker = "echo blerg-live"
			}
			if kind == "open-file" {
				lsof = "echo live-process; exit 0"
			}
			if kind == "unknown-processes" {
				lsof = "echo unavailable >&2; exit 1"
			}
			for name, body := range map[string]string{"docker": docker, "tmux": "exit 0", "lsof": lsof} {
				if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			if kind == "daemon" {
				dir := AgentRecordsDir(state)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lock.Close() }()
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "record" || kind == "corrupt-record" || kind == "allocation" {
				dir := AgentRecordsDir(state)
				body := []byte("unreadable or unresolved")
				if kind == "record" {
					dir = recordsDir(root)
					var err error
					body, err = json.Marshal(SessionRecord{Version: recordSchemaVersion, SessionID: "live", Repo: filepath.Base(path), ProjectPath: path})
					if err != nil {
						t.Fatal(err)
					}
				}
				if kind == "allocation" {
					dir = filepath.Join(state, sandboxCloneDirName)
				}
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "live.json"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out strings.Builder
			err := CleanupScratchFolders(root, state, kind != "dry-run", &out)
			switch kind {
			case "dry-run":
				if err != nil {
					t.Fatal(err)
				}
				mustExist(t, path)
				if !strings.Contains(out.String(), "Would remove") {
					t.Fatal(out.String())
				}
			case "apply":
				if err != nil {
					t.Fatal(err)
				}
				mustNotExist(t, path)
			default:
				mustExist(t, path)
				if err == nil && !strings.Contains(out.String(), "kept") {
					t.Fatal("unsafe cleanup was not refused")
				}
			}
		})
	}
}

func TestScratchCleanupKeepsAnIdleSiblingSession(t *testing.T) {
	host, _, project := newTestHost(t, &scriptedAgentProvider{}, "")
	path := filepath.Join(filepath.Dir(project), ".scratch-shared")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		host.Spawn(protocol.SpawnSession{SessionID: id, Repo: filepath.Base(path), ProjectPath: path, NoRepo: true, Kind: "agent"})
	}
	host.Kill("one")
	mustExist(t, path)
	if !host.Has("two") {
		t.Fatal("sibling session ended")
	}
	host.Kill("two")
	mustNotExist(t, path)
}

func TestScratchCleanupWaitsForTheDriver(t *testing.T) {
	host, _, project := newTestHost(t, &scriptedAgentProvider{}, "")
	path := filepath.Join(filepath.Dir(project), ".scratch-unresolved")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	host.Spawn(protocol.SpawnSession{SessionID: "scratch", Repo: filepath.Base(path), ProjectPath: path, NoRepo: true, Kind: "agent"})
	actualDone := host.get("scratch").runDone
	host.get("scratch").runDone = make(chan struct{})
	old := agentDriverStopTimeout
	agentDriverStopTimeout = time.Millisecond
	t.Cleanup(func() { agentDriverStopTimeout = old })
	host.Kill("scratch")
	<-actualDone
	mustExist(t, path)
}

func TestDiscardPausedRecoveryCleansEmptyScratch(t *testing.T) {
	host, _, project := newTestHost(t, &scriptedAgentProvider{}, "")
	path := filepath.Join(filepath.Dir(project), ".scratch-paused")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "lsof"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	host.adopting["paused"] = &adoptingSession{rec: AgentRecord{SessionID: "paused", Repo: filepath.Base(path), WorkDir: path}, failed: "recovery paused"}
	host.DropRecord("paused")
	mustNotExist(t, path)
}

func TestSandboxScratchWaitsForReturnedCommits(t *testing.T) {
	for _, work := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "commit"}[work], func(t *testing.T) {
			engine := fakeClaude(t)
			host, _, _, home := sandboxHost(t, engine, AgentHostConfig{ClaudeCode: true})
			writeClaudeLogin(t, home)
			stubCloneProbe(t, true, true)
			path := filepath.Join(host.cfg.reposRoot(), ".scratch-sandbox")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			host.Spawn(protocol.SpawnSession{SessionID: "scratch-child", Repo: filepath.Base(path), ProjectPath: path, NoRepo: true, Kind: "agent", Sandbox: true})
			sess := host.get("scratch-child")
			if sess == nil {
				t.Fatal("sandbox did not start")
			}
			if work {
				childCommit(t, sess.workspace, "work.txt")
			}
			host.Kill("scratch-child")
			if !work {
				mustNotExist(t, path)
				return
			}
			mustExist(t, path)
			if got := childRepoGit(t, path, "log", "-1", "--format=%s", "wip/scratch-child"); got != "child work.txt" {
				t.Fatalf("returned commit: %q", got)
			}
		})
	}
}

func TestScratchMaintenanceSeesLiveProcess(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof is not installed")
	}
	path := t.TempDir()
	cmd := exec.Command("sleep", "60")
	cmd.Dir = path
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if err := scratchNoOpenFiles(path); err == nil || !strings.Contains(err.Error(), "a process still uses it") {
		t.Fatalf("live process was not detected: %v", err)
	}
}
