//go:build linux || darwin

package hostgit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDarwinGitDiscoveryCommand(t *testing.T) {
	cmd := darwinGitDiscoveryCommand()
	if cmd.Path != "/usr/bin/xcrun" || cmd.Dir != "/" || !reflect.DeepEqual(cmd.Args, []string{"/usr/bin/xcrun", "--find", "git"}) ||
		!reflect.DeepEqual(cmd.Env, []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LC_ALL=C"}) || !cmd.SysProcAttr.Setpgid || cmd.WaitDelay <= 0 {
		t.Fatalf("unsafe discovery command: %+v", cmd)
	}
}

func TestDarwinGitDiscoveryBounds(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		good         bool
	}{
		{"path", "printf '/Library/Developer/CommandLineTools/usr/bin/git\\n'", true},
		{"failed", "exit 2", false},
		{"stderr", "printf warning >&2; printf '/Library/Developer/usr/bin/git'", false},
		{"empty", "exit 0", false},
		{"overflow", "i=0; while [ $i -lt 1000 ]; do printf 'xxxxxxxx'; i=$((i+1)); done", false},
		{"blocked", "exec /bin/sleep 30", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "-c", tc.script)
			cmd.SysProcAttr = darwinGitDiscoveryCommand().SysProcAttr
			cmd.WaitDelay = 100 * time.Millisecond
			started := time.Now()
			path, err := runDarwinGitDiscoveryTimeout(cmd, 200*time.Millisecond)
			if (err == nil) != tc.good || tc.good && path != "/Library/Developer/CommandLineTools/usr/bin/git" || time.Since(started) > 5*time.Second {
				t.Fatalf("path=%q err=%v elapsed=%v", path, err, time.Since(started))
			}
			if tc.name == "blocked" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("discovery did not time out: %v", err)
			}
		})
	}
}

func TestDarwinGitResolverCachesAndRevalidates(t *testing.T) {
	const path = "/Library/Developer/CommandLineTools/usr/bin/git"
	var calls atomic.Int32
	var uid atomic.Uint32
	lookup := cachedDarwinGit(func() (string, error) { calls.Add(1); return path, nil }, func(p string) (string, error) { return p, nil }, func(p string) (os.FileInfo, error) { return darwinGitTestInfo(p, uid.Load()), nil }, 501)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			got, err := lookup()
			if err != nil || got != path {
				t.Errorf("cached lookup: %q %v", got, err)
			}
		})
	}
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("discovery ran %d times", calls.Load())
	}
	uid.Store(502)
	if _, err := lookup(); !errors.Is(err, ErrNoGit) {
		t.Fatalf("cached executable replacement not refused: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("unsafe replacement triggered rediscovery")
	}
}

func TestDarwinGitResolverRefusalsStayClosed(t *testing.T) {
	for _, reason := range []string{"discovery", "escape", "stat", "untrusted", "alias change"} {
		t.Run(reason, func(t *testing.T) {
			var calls int
			canonicalPath := "/Library/Developer/CommandLineTools/usr/bin/git"
			lookup := cachedDarwinGit(func() (string, error) {
				calls++
				if reason == "discovery" {
					return "", errors.New("developer tools unavailable")
				}
				return canonicalPath, nil
			},
				func(p string) (string, error) {
					if reason == "escape" {
						return "/usr/bin/git", nil
					}
					return canonicalPath, nil
				},
				func(p string) (os.FileInfo, error) {
					if reason == "stat" {
						return nil, os.ErrNotExist
					}
					uid := uint32(0)
					if reason == "untrusted" {
						uid = 502
					}
					return darwinGitTestInfo(p, uid), nil
				}, 501)
			if reason == "alias change" {
				if _, err := lookup(); err != nil {
					t.Fatal(err)
				}
				canonicalPath = "/Library/Developer/Other/usr/bin/git"
			}
			for range 2 {
				got, err := lookup()
				if got != "" || !errors.Is(err, ErrNoGit) || !strings.Contains(err.Error(), "macOS") {
					t.Fatalf("unsafe result: %q %v", got, err)
				}
			}
			if calls != 1 {
				t.Fatalf("failed discovery retried %d times", calls)
			}
		})
	}
}

func darwinGitTestInfo(path string, uid uint32) os.FileInfo {
	mode := os.FileMode(0755)
	if filepath.Base(path) != "git" {
		mode |= os.ModeDir
	}
	return gitValidationInfo{mode: mode, uid: uid}
}

func TestDarwinGitResolverRevalidatesAncestors(t *testing.T) {
	const path = "/Applications/Xcode.app/Contents/Developer/usr/bin/git"
	for _, parent := range []string{"/Applications/Xcode.app", "/Applications", "/"} {
		t.Run(parent, func(t *testing.T) {
			var calls int
			mode := os.FileMode(0755)
			lookup := cachedDarwinGit(func() (string, error) { calls++; return path, nil },
				func(p string) (string, error) { return p, nil },
				func(p string) (os.FileInfo, error) {
					info := darwinGitTestInfo(p, 501).(gitValidationInfo)
					if p == parent {
						info.mode = os.ModeDir | mode
					}
					return info, nil
				}, 501)
			if got, err := lookup(); err != nil || got != path {
				t.Fatalf("same-UID installation: %q %v", got, err)
			}
			mode = 0775
			if got, err := lookup(); got != "" || !errors.Is(err, ErrNoGit) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), parent) || !strings.Contains(err.Error(), "group/world write") {
				t.Fatalf("unsafe cached ancestor not refused: %q %v", got, err)
			}
			if calls != 1 {
				t.Fatalf("ancestor change triggered discovery: %d", calls)
			}
		})
	}
}

func TestDarwinDeveloperDirectoryCommand(t *testing.T) {
	t.Setenv("DEVELOPER_DIR", "/untrusted")
	cmd := darwinDeveloperDirectoryCommand()
	if cmd.Path != "/usr/bin/xcode-select" || !reflect.DeepEqual(cmd.Args, []string{"/usr/bin/xcode-select", "-p"}) || cmd.Dir != "/" ||
		!reflect.DeepEqual(cmd.Env, []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LC_ALL=C"}) || !cmd.SysProcAttr.Setpgid || cmd.WaitDelay <= 0 {
		t.Fatalf("unsafe developer-directory command: %+v", cmd)
	}
	if gitDiscoveryTimeout != 10*time.Second {
		t.Fatalf("startup discovery timeout=%v", gitDiscoveryTimeout)
	}
}

func TestDarwinDiscoveryPrefersValidatedDeveloperDirectory(t *testing.T) {
	const selected = "/Applications/Xcode Beta.app/Contents/Developer"
	const fallback = "/Library/Developer/CommandLineTools/usr/bin/git"
	for _, reason := range []string{"selected", "selection failure", "relative", "multiline", "outside roots", "canonical escape", "unsafe metadata", "fallback failure"} {
		t.Run(reason, func(t *testing.T) {
			var calls []string
			run := func(cmd *exec.Cmd) (string, error) {
				calls = append(calls, cmd.Path)
				if cmd.Path == "/usr/bin/xcrun" {
					if reason == "fallback failure" {
						return "", context.DeadlineExceeded
					}
					return fallback, nil
				}
				switch reason {
				case "selection failure", "fallback failure":
					return "", errors.New("selection failed")
				case "relative":
					return "Applications/Xcode.app", nil
				case "multiline":
					return selected + "\n/another", nil
				case "outside roots":
					return "/untrusted", nil
				default:
					return selected, nil
				}
			}
			canonical := func(p string) (string, error) {
				if reason == "canonical escape" && strings.HasPrefix(p, selected) {
					return "/untrusted/git", nil
				}
				return p, nil
			}
			stat := func(p string) (os.FileInfo, error) {
				uid := uint32(0)
				if reason == "unsafe metadata" && strings.HasPrefix(p, selected) {
					uid = 502
				}
				return darwinGitTestInfo(p, uid), nil
			}
			// Use the production cache and final validation around discovery, so neither
			// selected-directory success nor fallback is retried on subsequent requests.
			lookup := cachedDarwinGit(func() (string, error) { return discoverDarwinGit(run, canonical, stat, 501) }, canonical, stat, 501)
			for range 2 {
				path, err := lookup()
				if reason == "fallback failure" {
					if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "selection failed") {
						t.Fatalf("missing discovery reasons: %v", err)
					}
				} else {
					want := fallback
					if reason == "selected" {
						want = selected + "/usr/bin/git"
					}
					if err != nil || path != want {
						t.Fatalf("got=%q err=%v want=%q", path, err, want)
					}
				}
			}
			wantCalls := []string{"/usr/bin/xcode-select"}
			if reason != "selected" {
				wantCalls = append(wantCalls, "/usr/bin/xcrun")
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("commands=%v, want=%v", calls, wantCalls)
			}
		})
	}
}

func TestDarwinDiscoveryAllowsColdStartup(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 2.1; printf '/Library/Developer/CommandLineTools/usr/bin/git\\n'")
	cmd.SysProcAttr = darwinGitDiscoveryCommand().SysProcAttr
	cmd.WaitDelay = 100 * time.Millisecond
	if path, err := runDarwinGitDiscovery(cmd); err != nil || path != "/Library/Developer/CommandLineTools/usr/bin/git" {
		t.Fatalf("cold startup: %q %v", path, err)
	}
}
