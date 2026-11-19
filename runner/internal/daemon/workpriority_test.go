package daemon

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorkerPriorityBackends(t *testing.T) {
	for _, tc := range []struct {
		os   string
		want []string
	}{
		{"linux", []string{"/bin/systemd-run", "--user", "--scope", "--quiet", "--collect", "--description=Blerg agent work", "-p", "CPUWeight=10", "-p", "IOWeight=10", "/bin/nice", "-n", "5", "/bin/ionice", "-c", "3"}},
		{"darwin", []string{"/bin/taskpolicy", "-b", "/bin/nice", "-n", "5"}},
		{"other", nil},
	} {
		t.Run(tc.os, func(t *testing.T) {
			got := detectWorkerPriority(tc.os, func(s string) (string, error) { return "/bin/" + s, nil }, func([]string) error { return nil })
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	t.Run("no user systemd", func(t *testing.T) {
		got := detectWorkerPriority("linux", func(s string) (string, error) { return "/bin/" + s, nil }, func(args []string) error {
			if strings.HasSuffix(args[0], "systemd-run") {
				return errors.New("fixture unavailable")
			}
			return nil
		})
		if !reflect.DeepEqual(got, []string{"/bin/nice", "-n", "5", "/bin/ionice", "-c", "3"}) {
			t.Fatal(got)
		}
	})
	t.Run("CPU-only fallback", func(t *testing.T) {
		got := detectWorkerPriority("linux", func(s string) (string, error) {
			if s != "nice" {
				return "", exec.ErrNotFound
			}
			return "/bin/nice", nil
		}, func([]string) error { return nil })
		if !reflect.DeepEqual(got, []string{"/bin/nice", "-n", "5"}) {
			t.Fatal(got)
		}
	})
}

func TestWorkerPrioritySettings(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	p := NewWorkerPriority(file)
	if !p.Enabled() {
		t.Fatal("must default on")
	}
	if err := saveReposRoot(file, "/srv/repos"); err != nil {
		t.Fatal(err)
	}
	if err := p.Set(false); err != nil {
		t.Fatal(err)
	}
	if p.Enabled() || NewWorkerPriority(file).Enabled() {
		t.Fatal("disabled setting lost")
	}
	if root, err := readSavedReposRoot(file); err != nil || root != "/srv/repos" {
		t.Fatal("repos setting lost", root, err)
	}
	if err := saveReposRoot(file, "/srv/next"); err != nil {
		t.Fatal(err)
	}
	if NewWorkerPriority(file).Enabled() {
		t.Fatal("repos save overwrote priority")
	}
	p.stateFile = ""
	if err := p.Set(true); err == nil || p.Enabled() {
		t.Fatal("failed persistence changed live value")
	}
}

func TestWorkerPrioritySandboxBoundary(t *testing.T) {
	cmd := sandboxExecPrefix("fixture").command(context.Background(), "/workspace", "engine", "literal arg")
	wrapSandboxWorkerCommand(cmd)
	want := []string{"docker", "exec", "-i", "-w", "/workspace", "fixture", "nice", "-n", "5", "ionice", "-c", "3", "sh"}
	if !reflect.DeepEqual(cmd.Args[:len(want)], want) {
		t.Fatal(cmd.Args)
	}
	if cmd.Args[len(cmd.Args)-1] != "literal arg" {
		t.Fatal("payload arguments changed")
	}
	low := sandboxRunArgs("fixture", "/srv/repo", "", nil, sandboxRunExtras{yieldToPerson: true})
	normal := sandboxRunArgs("fixture", "/srv/repo", "", nil, sandboxRunExtras{})
	if !strings.Contains(strings.Join(low, " "), "--cpu-shares 128 --blkio-weight 100") {
		t.Fatal("container worker cgroup weights missing")
	}
	if strings.Contains(strings.Join(normal, " "), "--cpu-shares") {
		t.Fatal("cluster/default sandbox modified")
	}
}
