package desktopupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/contracts/releasegate"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelectPublishedStable(t *testing.T) {
	releases := []Release{{Tag: "v0.9.0", Published: "today"}, {Tag: "v0.10.0", Published: "today"}, {Tag: "v2.0.0", Draft: true, Published: "today"}, {Tag: "v3.0.0-rc.1", Published: "today"}, {Tag: "v4.0.0", Prerelease: true, Published: "today"}, {Tag: "v5.0.0"}}
	r, e := Select(releases, "")
	if e != nil || r.Tag != "v0.10.0" {
		t.Fatalf("selected %+v: %v", r, e)
	}
	for _, tag := range []string{"v2.0.0", "v3.0.0-rc.1", "v4.0.0", "v5.0.0", "main", "v01.0.0"} {
		if _, e = Select(releases, tag); e == nil {
			t.Fatalf("accepted %s", tag)
		}
	}
}
func TestDirtyCheckoutRefusesBeforeBackup(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	u := Upgrader{Root: root, State: state, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "git" {
			t.Fatalf("ran %s before clean checkout validation", name)
		}
		if strings.Join(args, " ") == "status --porcelain --untracked-files=all" {
			return []byte("?? local.txt"), nil
		}
		return nil, nil
	}}
	if e := u.Upgrade(t.Context(), ""); e == nil {
		t.Fatal("dirty checkout upgraded")
	}
}
func TestExclusiveLockAndPrivateRecord(t *testing.T) {
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	u := Upgrader{State: state}
	unlock, e := u.lock()
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	if _, e = u.lock(); e == nil {
		t.Fatal("second worker admitted")
	}
	op := Operation{Phase: "backing_up", OldCommit: "old", Tag: "v0.6.0"}
	if e = u.save(op); e != nil {
		t.Fatal(e)
	}
	f, e := os.Stat(filepath.Join(state, "operation.json"))
	if e != nil || f.Mode().Perm() != 0600 {
		t.Fatal("record not private", e)
	}
}

func TestUpgradeWithRealGitAndFakeServices(t *testing.T) {
	failures := []string{"", "backup", "active", "moved", "installer", "preinstall", "cancel", "truncated", "ancestry", "health", "stale"}
	if os.Getenv("BLERG_UPGRADE_DRILL") == "1" {
		failures = append(failures, "drill")
	}
	for _, failure := range failures {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			home := t.TempDir()
			state := t.TempDir()
			if e := os.Chmod(state, 0700); e != nil {
				t.Fatal(e)
			}
			daemonState := filepath.Join(home, ".blerg-runner-daemon")
			if e := os.Mkdir(daemonState, 0700); e != nil {
				t.Fatal(e)
			}
			write := func(path, s string) {
				t.Helper()
				if e := os.WriteFile(path, []byte(s), 0600); e != nil {
					t.Fatal(e)
				}
			}
			git := func(args ...string) string {
				t.Helper()
				b, e := releasegate.Exec(t.Context(), "git", append([]string{"-C", root}, args...)...)
				if e != nil {
					t.Fatal(e)
				}
				return strings.TrimSpace(string(b))
			}
			git("init")
			git("config", "user.name", "Test")
			git("config", "user.email", "test@example.com")
			write(filepath.Join(root, ".gitignore"), "install/desktop/.env\n")
			if e := os.MkdirAll(filepath.Join(root, "install/desktop"), 0700); e != nil {
				t.Fatal(e)
			}
			write(filepath.Join(root, "install/desktop/.env"), "private-test-settings")
			write(filepath.Join(root, "VERSION"), "0.5.0")
			write(filepath.Join(root, "app"), "old")
			git("add", ".")
			git("commit", "-m", "old")
			old := git("rev-parse", "HEAD")
			write(filepath.Join(root, "install/desktop/blerg-up.sh"), "# BLERG_UPGRADE_MIGRATION_MARKER")
			write(filepath.Join(root, "app"), "new")
			git("add", ".")
			git("commit", "-m", "new")
			target := git("rev-parse", "HEAD")
			if failure == "ancestry" {
				old, target = target, old
			}
			remote := filepath.Join(t.TempDir(), "upstream.git")
			git("clone", "--bare", root, remote)
			git("--git-dir="+remote, "update-ref", "refs/tags/v0.6.0", target)
			git("checkout", "--detach", old)
			git("remote", "add", "origin", remote)
			// A locally shadowed stable tag must neither select the old tree nor be overwritten.
			git("update-ref", "refs/tags/v0.6.0", old)
			write(filepath.Join(daemonState, "connection.json"), `{"state":"connected","pid":122}`)
			write(filepath.Join(daemonState, "desktop-credential.json"), "private-test-credential")
			upgradeCtx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			var journal []string
			calls := 0
			u := Upgrader{Root: root, Home: home, State: state, DaemonState: daemonState, Verify: func(context.Context, Operation) error { return nil }}
			u.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if ctx.Err() != nil {
					t.Fatal("recovery reused canceled context")
				}
				s := strings.Join(args, " ")
				journal = append(journal, name+" "+s)
				if name == "git" {
					return releasegate.Exec(ctx, "git", append([]string{"-C", root}, args...)...)
				}
				if name == "docker" {
					if strings.Contains(s, "SELECT count") {
						if failure == "active" {
							return []byte("1"), nil
						}
						return []byte("0"), nil
					}
					if strings.Contains(s, "ps --format") {
						if failure == "health" {
							return []byte(`[]`), nil
						}
						return []byte(`[{"Service":"blerg-core","State":"running"},{"Service":"blerg-board","State":"running"},{"Service":"blerg-runner","State":"running"}]`), nil
					}
				}
				if name == "env" {
					if failure == "cancel" {
						cancel()
						return nil, context.Canceled
					}
					if failure == "preinstall" {
						return nil, errors.New("build failed")
					}
					for _, arg := range args {
						if strings.HasPrefix(arg, "BLERG_UPGRADE_MIGRATION_MARKER=") {
							write(strings.TrimPrefix(arg, "BLERG_UPGRADE_MIGRATION_MARKER="), "started")
						}
					}
					if failure == "installer" {
						return nil, errors.New("installer failed")
					}
					if failure != "stale" {
						write(filepath.Join(daemonState, "connection.json"), `{"state":"connected","pid":123}`)
					}
				}
				return nil, nil
			}
			u.Releases = func(context.Context) ([]Release, error) {
				calls++
				if calls == 2 && failure == "moved" {
					git("--git-dir="+remote, "update-ref", "refs/tags/v0.6.0", old)
				}
				return []Release{{Tag: "v0.6.0", Published: "2026-01-01"}}, nil
			}
			u.Dump = func(_ context.Context, _ string, w io.Writer) error {
				if failure == "backup" {
					return errors.New("backup failed")
				}
				if failure == "truncated" {
					_, e := io.WriteString(w, "partial dump")
					return e
				}
				_, e := io.WriteString(w, "complete database dump\n-- PostgreSQL database dump complete\n")
				return e
			}
			var restore func(Operation)
			if failure == "drill" {
				u.Dump, restore = disposableDatabaseDrill(t, root, daemonState, &u)
			}
			err := u.Upgrade(upgradeCtx, "")
			if failure == "" || failure == "drill" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("upgrade succeeded after injected failure")
			}
			got := git("rev-parse", "HEAD")
			mutated := failure == "" || failure == "drill" || failure == "installer" || failure == "health" || failure == "stale"
			if mutated && got != target || !mutated && got != old {
				t.Fatalf("unexpected checkout %s", got)
			}
			if git("rev-parse", "refs/tags/v0.6.0") != old {
				t.Fatal("overwrote local tag")
			}
			raw, e := os.ReadFile(filepath.Join(state, "operation.json"))
			if e != nil {
				t.Fatal(e)
			}
			var op Operation
			if e = json.Unmarshal(raw, &op); e != nil {
				t.Fatal(e)
			}
			if restore != nil {
				restore(op)
			}
			if failure == "preinstall" || failure == "cancel" {
				if op.Phase != "failed" || !strings.Contains(strings.Join(journal, "\n"), "compose start blerg-core") {
					t.Fatalf("pre-migration recovery failed: %+v", op)
				}
			}
			if failure == "installer" || failure == "health" || failure == "stale" {
				if op.Phase != "recovery_required" {
					t.Fatalf("unsafe failure state: %s", raw)
				}
			}
			if mutated {
				for _, name := range []string{"complete.json", "env", "desktop-credential.json", "blerg_core.sql", "blerg_board.sql", "blerg_runner.sql"} {
					info, e := os.Stat(filepath.Join(op.Backup, name))
					if e != nil || info.Mode().Perm() != 0600 {
						t.Fatalf("missing/private backup %s: %v", name, e)
					}
				}
				if op.OldCommit != old || op.NewCommit != target {
					t.Fatal("rollback identifiers lost")
				}
			}
			if failure == "backup" || failure == "truncated" {
				if _, e = os.Stat(filepath.Join(op.Backup, "complete.json")); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("incomplete backup marked complete")
				}
				if !strings.Contains(strings.Join(journal, "\n"), "compose start blerg-core") {
					t.Fatal("old services not restarted")
				}
			}
		})
	}
}

func TestVerifyRunningVersions(t *testing.T) {
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/agents" {
					_, _ = io.WriteString(w, `{"version":"0.6.0","commit":"1234567890abcdef"}`)
					return
				}
				commit := "1234567890abcdef"
				if bad {
					commit = "oldcommit"
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"release": "0.6.0", "commit": commit})
			}))
			defer server.Close()
			_, port, e := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
			if e != nil {
				t.Fatal(e)
			}
			run := func(context.Context, string, ...string) ([]byte, error) { return []byte("127.0.0.1:" + port), nil }
			err := VerifyServices(t.Context(), run, Operation{Tag: "v0.6.0", NewCommit: "1234567890abcdef"})
			if (err != nil) != bad {
				t.Fatalf("verification: %v", err)
			}
		})
	}
}

func TestConfiguredDaemonStateAndInstalledExecutable(t *testing.T) {
	root := t.TempDir()
	if e := os.MkdirAll(filepath.Join(root, "install/desktop"), 0700); e != nil {
		t.Fatal(e)
	}
	want := filepath.Join(t.TempDir(), "private-state")
	if e := os.WriteFile(filepath.Join(root, "install/desktop/.env"), []byte("BLERG_RUNNER_DAEMON_STATE_DIR=\""+want+"\"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	got, e := DaemonDirectory(root, t.TempDir(), "")
	if e != nil || got != want {
		t.Fatalf("state location: %q, %v", got, e)
	}
	u := Upgrader{Root: root, Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "docker" {
			return []byte("0"), nil
		}
		if name != filepath.Join(root, "bin/blerg-runner-daemon") {
			t.Fatalf("wrong daemon executable %q", name)
		}
		return nil, nil
	}}
	if e = u.noWork(t.Context()); e != nil {
		t.Fatal(e)
	}
}

func TestInterruptedUpgradeRefusesRetry(t *testing.T) {
	state := t.TempDir()
	if e := os.Chmod(state, 0700); e != nil {
		t.Fatal(e)
	}
	u := Upgrader{State: state, Root: t.TempDir(), Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "rev-parse" {
			return []byte("absent-git-state"), nil
		}
		return nil, nil
	}, Releases: func(context.Context) ([]Release, error) { t.Fatal("retried uncertain installation"); return nil, nil }}
	if e := u.save(Operation{Phase: "installing"}); e != nil {
		t.Fatal(e)
	}
	if e := u.Upgrade(t.Context(), ""); e == nil || !strings.Contains(e.Error(), "requires recovery") {
		t.Fatalf("uncertain upgrade retried: %v", e)
	}
}

func TestNoWorkRejectsEveryNonterminalStatus(t *testing.T) {
	u := Upgrader{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "docker" {
			query := args[len(args)-1]
			if !strings.Contains(query, "status NOT IN ('ended','stopped','error')") || strings.Contains(query, "ended_at") {
				t.Fatalf("unsafe active work query: %s", query)
			}
			return []byte("1"), nil
		}
		t.Fatal("daemon consulted after live session")
		return nil, nil
	}}
	if u.noWork(t.Context()) == nil {
		t.Fatal("live work admitted")
	}
}

func TestVerifyRunnerCommit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agents" {
			_, _ = io.WriteString(w, `{"version":"0.6.0","commit":"oldcommit"}`)
			return
		}
		_, _ = io.WriteString(w, `{"release":"0.6.0","commit":"1234567890abcdef"}`)
	}))
	defer server.Close()
	run := func(context.Context, string, ...string) ([]byte, error) {
		return []byte(strings.TrimPrefix(server.URL, "http://")), nil
	}
	if VerifyServices(t.Context(), run, Operation{Tag: "v0.6.0", NewCommit: "1234567890abcdef"}) == nil {
		t.Fatal("accepted old runner commit")
	}
}

func TestDumpTrailer(t *testing.T) {
	for _, tail := range []string{"-- PostgreSQL database dump complete\n--\n", "-- PostgreSQL database dump complete\n--\n\n\\unrestrict randomtoken\n"} {
		if !completeDump([]byte(tail)) {
			t.Fatal("rejected PostgreSQL trailer")
		}
	}
	for _, tail := range []string{"partial dump", "-- PostgreSQL database dump complete\nSELECT 1;"} {
		if completeDump([]byte(tail)) {
			t.Fatal("accepted incomplete dump")
		}
	}
}

func TestPrivateInstallLog(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "install/desktop"), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "install.log")
	run := Executor(root, t.TempDir())
	_, err := run(t.Context(), "env", "BLERG_UPGRADE_INSTALL_LOG="+log, "sh", "-c", "echo private-test-secret; echo private-test-secret >&2; exit 1")
	if err == nil {
		t.Fatal("failed command succeeded")
	}
	b, e := os.ReadFile(log)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "private-test-secret") || !strings.Contains(string(b), "failed") {
		t.Fatal("unsafe or missing log")
	}
	info, e := os.Stat(log)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("log not private")
	}
}
func TestPruneCompleteBackupsOnly(t *testing.T) {
	state := t.TempDir()
	u := Upgrader{State: state}
	for i := 0; i < 5; i++ {
		dir := filepath.Join(state, fmt.Sprintf("backup-%d", i))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if i < 4 {
			if err := privateWrite(filepath.Join(dir, "complete.json"), []byte(`{"old_commit":"old"}`)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < 5; i++ {
		stamp := time.Unix(int64(i+1), 0)
		if err := os.Chtimes(filepath.Join(state, fmt.Sprintf("backup-%d", i)), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.pruneBackups(filepath.Join(state, "backup-3")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "backup-0")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oldest complete backup retained")
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 4 {
		t.Fatalf("retention: %v %d", err, len(entries))
	}
	if _, err := os.Stat(filepath.Join(state, "backup-4")); err != nil {
		t.Fatal("incomplete backup pruned")
	}
}

func TestVerifyBindAddress(t *testing.T) {
	host := net.IPv4(127, 0, 0, 2).String()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"version":"0.6.0","release":"0.6.0","commit":"1234567890abcdef"}`)
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BLERG_BIND_ADDR", host)
	run := func(context.Context, string, ...string) ([]byte, error) {
		return []byte(net.JoinHostPort("0.0.0.0", port)), nil
	}
	if err := VerifyServices(t.Context(), run, Operation{Tag: "v0.6.0", NewCommit: "1234567890abcdef"}); err != nil {
		t.Fatal(err)
	}
}
