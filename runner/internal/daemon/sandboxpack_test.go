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
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
)

func TestSandboxFetchServesPackOnlyInContainer(t *testing.T) {
	repo, clone := cloneFixture(t, "pack-boundary")
	childCommit(t, clone, "work.txt")
	log := filepath.Join(t.TempDir(), "host-trace")
	git, err := hostgit.Binary()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nexport GIT_TRACE=" + packShellQuote(log) + "\nexec " + packShellQuote(git) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	restore := hostgit.SetBinaryForTest(wrapper)
	defer restore()
	if _, err := syncSandboxBranch(clone, "pack-boundary", ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(raw)
	if strings.Contains(trace, "git upload-pack --strict "+clone) || strings.Contains(trace, "built-in: git upload-pack") {
		t.Fatal("host served the sandbox pack")
	}
	if !strings.Contains(trace, "index-pack --stdin") || !strings.Contains(trace, "--strict") {
		t.Fatal("host did not strictly index the received pack")
	}
	if got := childRepoGit(t, repo, "show", "wip/pack-boundary:work.txt"); got != "work.txt" {
		t.Fatal("commit did not arrive")
	}
}

func TestSandboxPackServerIsolationArgs(t *testing.T) {
	path := "/srv/sessions/clone,'quoted'"
	c := &sandboxClone{Repo: "/srv/repos/project", Objects: "/srv/repos/project/.git/objects"}
	args := sandboxPackArgs(path, c, "blerg-pack-fixture", time.Minute)
	joined := " " + strings.Join(args, " ") + " "
	for _, flag := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=512m", "--memory-swap=512m", "--pids-limit=64", "--pull=never", "--rm", "GIT_CONFIG_NOSYSTEM=1", "GIT_ALLOW_PROTOCOL=none", "upload-pack --strict"} {
		if !strings.Contains(joined, " "+flag+" ") {
			t.Fatalf("missing isolation: %s", flag)
		}
	}
	mounts := []string{}
	for i, arg := range args {
		if arg == "--mount" {
			mounts = append(mounts, args[i+1])
		}
	}
	if len(mounts) != 2 || mounts[0] != sandboxBindMount(path, path)+",readonly" || mounts[1] != sandboxBindMount(c.Objects, c.Objects)+",readonly" {
		t.Fatal("unexpected host mount")
	}
	if strings.Contains(joined, "docker.sock") || strings.Contains(joined, "--privileged") || strings.Contains(joined, "--network=host") {
		t.Fatal("container escaped isolation")
	}
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "secret-fixture")
	t.Setenv("ANTHROPIC_API_KEY", "secret-fixture")
	t.Setenv("OPENAI_API_KEY", "secret-fixture")
	if strings.Contains(strings.Join(sandboxPackDockerEnv(), " "), "secret-fixture") {
		t.Fatal("client inherited a credential")
	}
}

func TestSandboxPackServerFailureAndCleanup(t *testing.T) {
	for _, mode := range []string{"refused", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			_, clone := cloneFixture(t, "pack-failure")
			childCommit(t, clone, "work.txt")
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + packShellQuote(log) + "\nif [ \"$1\" = run ]; then\n"
			if mode == "timeout" {
				script += "sleep 30\n"
			} else {
				script += "echo private-serving-secret >&2\nexit 1\n"
			}
			script += "fi\n"
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			c, err := loadSandboxClone("pack-failure")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			started := time.Now()
			out, err := fetchSandboxPack(ctx, time.Second, clone, "pack-failure", c, "refs/heads/wip/pack-failure", "refs/heads/wip/pack-failure")
			if err == nil || strings.Contains(out+err.Error(), "private-serving-secret") {
				t.Fatal("unsafe server failure")
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("server failure exceeded deadline")
			}
			calls, err := os.ReadFile(log)
			if err != nil || !strings.Contains(string(calls), "rm -f blerg-pack-") {
				t.Fatal("server cleanup was not attempted")
			}
			if _, err := os.Stat(clone); err != nil {
				t.Fatal("failed fetch discarded the clone")
			}
		})
	}
}

func TestSandboxPackServerInDocker(t *testing.T) {
	if os.Getenv("BLERG_SANDBOX_CHILD_TEST") != "1" {
		t.Skip("set BLERG_SANDBOX_CHILD_TEST=1 with a built sandbox image")
	}
	labelTestContainers(t)
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	repo := filepath.Join(t.TempDir(), "repo")
	initChildRepo(t, repo)
	id := "pack-real-" + ccUUID()
	clone, err := createSandboxClone(repo, id, "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := loadSandboxClone(id)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect exactly the production container configuration before serving any repository.
	name := "blerg-pack-inspect-" + ccUUID()
	args := sandboxPackArgs(clone, c, name, time.Minute)
	args[0] = "create"
	dockerTest(t, args...)
	raw := dockerTest(t, "inspect", name)
	var views []struct {
		HostConfig struct {
			NetworkMode    string
			ReadonlyRootfs bool
			Privileged     bool
			Memory         int64
			PidsLimit      int64
			CapDrop        []string
			SecurityOpt    []string
		}
		Mounts []struct {
			Source string
			RW     bool
		}
	}
	if err := json.Unmarshal([]byte(raw), &views); err != nil || len(views) != 1 {
		t.Fatal("could not inspect pack container")
	}
	view := views[0]
	if view.HostConfig.NetworkMode != "none" || !view.HostConfig.ReadonlyRootfs || view.HostConfig.Privileged || view.HostConfig.Memory != 512<<20 || view.HostConfig.PidsLimit != 64 {
		t.Fatal("container isolation mismatch")
	}
	if !slices.Contains(view.HostConfig.CapDrop, "ALL") || !slices.Contains(view.HostConfig.SecurityOpt, "no-new-privileges") {
		t.Fatal("container privilege mismatch")
	}
	if len(view.Mounts) != 2 {
		t.Fatal("unexpected container mount")
	}
	for _, mount := range view.Mounts {
		if mount.RW || (mount.Source != clone && mount.Source != c.Objects) {
			t.Fatal("unsafe container mount")
		}
	}
	dockerTest(t, "rm", name)
	writer := "blerg-pack-writer-" + ccUUID()
	dockerTest(t, "run", "-d", "--name", writer, "--network=none", "--label", sandboxTestLabels[len(sandboxTestLabels)-1], "--mount", sandboxBindMount(clone, clone), "--entrypoint", "sleep", sandboxImage(), "infinity")
	var elapsed []time.Duration
	for i := 0; i < 3; i++ {
		childCommit(t, clone, fmt.Sprintf("turn-%d.txt", i))
		before := time.Now()
		changed, err := syncSandboxBranch(clone, id, writer)
		elapsed = append(elapsed, time.Since(before))
		if err != nil || !changed {
			t.Fatalf("turn %d: changed=%v error=%v", i, changed, err)
		}
		if state := dockerTest(t, "inspect", "--format", "{{.State.Paused}}", writer); state != "false" {
			t.Fatal("session was left paused")
		}
	}
	t.Logf("isolated turn-end durations (pause/check/serve/strict receive/cleanup/unpause): %v", elapsed)
	if got := childRepoGit(t, repo, "show", "wip/"+id+":turn-2.txt"); got != "turn-2.txt" {
		t.Fatal("last turn missing")
	}
	// No disposable server may remain after a completed fetch.
	if remaining := dockerTest(t, "ps", "-aq", "--filter", "label="+sandboxTestLabels[len(sandboxTestLabels)-1], "--filter", "label=blerg.runner.pack-server=1"); remaining != "" {
		t.Fatal("pack server outlived fetch")
	}
}

func TestSandboxStopKeepsContextThroughFinalFetch(t *testing.T) {
	const id = "pack-stop-context"
	repo, clone := cloneFixture(t, id)
	childCommit(t, clone, "stop-work.txt")
	container := dockerContainerName(id)
	registerSandbox(id, container)
	pinSandboxContext(container, "fixture-original")
	oldGone, oldRun := sandboxContainerGone, dockerRun
	dockerRun = func(...string) error { return nil }
	defer func() {
		sandboxContainerGone, dockerRun = oldGone, oldRun
		unregisterSandbox(id)
		forgetSandboxDeath(container)
	}()
	stubCloneProbe(t, true, true)
	sandboxContainerGone = func(name string) bool {
		if got := pinnedDockerArgs(name); !slices.Equal(got, []string{"--context", "fixture-original"}) {
			t.Errorf("stop-time context lost: %v", got)
		}
		return true
	}
	finishSandbox(id)
	raw, err := os.ReadFile(os.Getenv("FAKE_DOCKER_LOG"))
	if err != nil {
		t.Fatal(err)
	}
	served := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "blerg.runner.pack-server=1") {
			served = true
			if !strings.HasPrefix(line, "--context fixture-original run ") {
				t.Fatalf("pack server used wrong context: %s", line)
			}
		}
	}
	if !served {
		t.Fatal("stop did not serve the final pack")
	}
	if got := childRepoGit(t, repo, "show", "wip/"+id+":stop-work.txt"); got != "stop-work.txt" {
		t.Fatal("stop-time commit lost")
	}
	if len(pinnedDockerArgs(container)) != 0 {
		t.Fatal("context retained after finish")
	}
}

func TestSandboxGitCommandsUsePinnedContext(t *testing.T) {
	const id = "pack-git-context"
	_, clone := cloneFixture(t, id)
	container := dockerContainerName(id)
	registerSandbox(id, container)
	pinSandboxContext(container, "fixture-original")
	defer func() { unregisterSandbox(id); forgetSandboxDeath(container) }()
	t.Setenv("FAKE_DOCKER_EXEC_NOOP", "1")
	if clean, known := sandboxCloneSettle(container, id); !clean || !known {
		t.Fatal("settle failed")
	}
	if _, err := syncSandboxBranch(clone, id, container); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(os.Getenv("FAKE_DOCKER_LOG"))
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"exec", "pause", "unpause"} {
		found := false
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if strings.HasPrefix(line, operation+" ") || strings.HasPrefix(line, "--context fixture-original "+operation+" ") {
				found = true
				if !strings.HasPrefix(line, "--context fixture-original ") {
					t.Errorf("Git lifecycle command lost its context: %s", line)
				}
			}
		}
		if !found {
			t.Errorf("missing lifecycle command: %s", operation)
		}
	}
}

// Exercise the caller that chooses the serving boundary: no Docker must never
// become permission to fetch directly from the sandbox clone on the host.
func TestSandboxCloneFetchDoesNotFallBackWithoutDocker(t *testing.T) {
	for _, mode := range []string{"failing", "absent"} {
		t.Run(mode, func(t *testing.T) {
			const id = "pack-no-fallback"
			repo, clone := cloneFixture(t, id)
			childCommit(t, clone, "work.txt")
			c, err := loadSandboxClone(id)
			if err != nil {
				t.Fatal(err)
			}
			git, err := hostgit.Binary()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			tracePath := filepath.Join(dir, "trace")
			wrapper := filepath.Join(dir, "git")
			writeExe(t, wrapper, "export GIT_TRACE="+packShellQuote(tracePath)+"\nexec "+packShellQuote(git)+" \"$@\"")
			t.Cleanup(hostgit.SetBinaryForTest(wrapper))
			if mode == "failing" {
				writeExe(t, filepath.Join(dir, "docker"), "exit 1")
			}
			t.Setenv("PATH", dir)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if changed, err := fetchSandboxCloneContext(ctx, clone, id, c); err == nil || changed {
				t.Fatalf("Docker %s: changed=%v err=%v", mode, changed, err)
			}
			raw, err := os.ReadFile(tracePath)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(string(raw), "\n") {
				if (strings.Contains(line, "built-in: git upload-pack") || strings.Contains(line, "git-upload-pack")) || (strings.Contains(line, "fetch ") && strings.Contains(line, clone)) {
					t.Fatalf("host attempted local sandbox serving: %s", line)
				}
			}
			// Use the fixed binary after restoring a usable PATH for the fixture helper.
			t.Setenv("PATH", filepath.Dir(git)+":/usr/bin:/bin")
			if got := childRepoGit(t, repo, "branch", "--list", "wip/"+id); got != "" {
				t.Fatalf("failed fetch imported a branch: %s", got)
			}
		})
	}
}
