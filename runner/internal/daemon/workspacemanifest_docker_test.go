package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func p2Docker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v: %s", args[0], err, out)
	}
	return strings.TrimSpace(string(out))
}

func p2DockerWorkspace(t *testing.T) (*LocalWorkspace, *DockerWorkspaceContainer) {
	t.Helper()
	if os.Getenv("BLERG_P2_DOCKER_TEST") != "1" {
		t.Skip("set BLERG_P2_DOCKER_TEST=1 with the sandbox image")
	}
	previousContext := currentDockerContext
	currentDockerContext = realDockerContext
	t.Cleanup(func() { currentDockerContext = previousContext })
	w, _, _, _ := localWorkspaceFixture(t)
	label := "p2-" + ccUUID()
	plan, err := w.DockerMountPlan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	owner := sandboxOwner()
	entryPolicy, err := workspaceEntrySecurityOption()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--tmpfs", workspaceExplorerTempMount, "--security-opt", entryPolicy, "-d", "--pull=never", "--network=none", "--init", "--user", "1000:1000", "--label", "blerg-test-run=" + label}
	for _, label := range plan.Labels {
		args = append(args, "--label", label)
	}
	for _, mount := range plan.Mounts {
		args = append(args, "--mount", mount)
	}
	args = append(args, "--entrypoint", "/bin/sh", sandboxImage(), "-c", "sleep 600")
	id := p2Docker(t, args...)
	t.Cleanup(func() {
		// Exact ID and unique label: never clean up by a shared sandbox prefix.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "blerg-test-run"}}`, id).Output()
		if err == nil && strings.TrimSpace(string(raw)) == label {
			_ = exec.CommandContext(ctx, "docker", "rm", "-f", id).Run()
		}
	})
	d := &DockerWorkspaceContainer{Context: currentDockerContext(), ID: id, Owner: owner, Session: w.state.Session, Launch: w.state.Launch.LaunchGeneration, Paths: map[string]bool{}, CancelStatus: func(context.Context) error { return nil }, FenceWriters: func(context.Context) error { return nil }}
	for _, m := range w.state.Members {
		d.Paths[m.Member.Path] = true
	}
	w.runtime = nil
	w.state.RuntimeIdentity = ""
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.BindRuntime(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if err := d.Identity(t.Context(), d.Session, d.Launch); err != nil {
		t.Fatal(err)
	}
	return w, d
}

type observingP2Container struct {
	*DockerWorkspaceContainer
	t              *testing.T
	files          []string
	paused         bool
	freezes, thaws int
}

func (d *observingP2Container) Head(ctx context.Context, path string) (string, error) {
	if d.paused {
		return "", fmt.Errorf("exec while paused")
	}
	return d.DockerWorkspaceContainer.Head(ctx, path)
}
func (d *observingP2Container) Freeze(ctx context.Context) error {
	if err := d.DockerWorkspaceContainer.Freeze(ctx); err != nil {
		return err
	}
	d.paused = true
	d.freezes++
	before := make([][]byte, len(d.files))
	for i, file := range d.files {
		before[i], _ = os.ReadFile(file)
	}
	// A real pause must stop every independent writer, not just the agent's
	// docker-exec client. The process itself supplies the readiness barrier.
	time.Sleep(60 * time.Millisecond)
	for i, file := range d.files {
		after, _ := os.ReadFile(file)
		if string(after) != string(before[i]) {
			d.t.Error("container writer ran during session freeze")
		}
	}
	return nil
}
func (d *observingP2Container) Thaw(ctx context.Context) error {
	d.thaws++
	err := d.DockerWorkspaceContainer.Thaw(ctx)
	if err == nil {
		d.paused = false
	}
	return err
}

func TestP2DockerSessionFreezeIncludesIndependentShellWriters(t *testing.T) {
	w, d := p2DockerWorkspace(t)
	observer := &observingP2Container{DockerWorkspaceContainer: d, t: t}
	w.runtime = observer
	for i, m := range w.state.Members {
		file := filepath.Join(m.Clone, "writer.txt")
		observer.files = append(observer.files, file)
		// Each independent exec forks a detached writer like a shell background
		// descendant. No dependency on another package's shell implementation.
		code := `import os,time
if os.fork(): os._exit(0)
os.setsid()
with open("writer.txt","ab",buffering=0) as f:
 while True:
  f.write(b"x")
  time.sleep(.002)
`
		p2Docker(t, "exec", "-d", "-w", "/workspace/"+m.Member.Path, d.ID, "python3", "-c", code)
		deadline := time.Now().Add(5 * time.Second)
		for {
			if st, err := os.Stat(file); err == nil && st.Size() > 10 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("writer %d did not start", i)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	childCommit(t, w.state.Members[0].Clone, "change.txt")
	results, err := w.Sync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if observer.freezes != 1 || observer.thaws != 1 || observer.paused {
		t.Fatal("freeze/thaw not session-wide")
	}
	if results[0].Pending || results[0].SyncError != "" {
		t.Fatalf("fetch failed: %+v", results)
	}
	if _, err := w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if observer.freezes != 1 {
		t.Fatal("unchanged real runtime froze again")
	}
}

func TestP2DockerPartialRefusalAndRestart(t *testing.T) {
	w, d := p2DockerWorkspace(t)
	for _, m := range w.state.Members {
		childCommit(t, m.Clone, "change.txt")
	}
	bad := filepath.Join(w.state.Members[0].Clone, ".git", "config")
	f, err := os.OpenFile(bad, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n[include]\npath=/forbidden\n")
	_ = f.Close()
	recovered, err := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, w.storage, d)
	if err != nil {
		t.Fatal(err)
	}
	results, err := recovered.Sync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].SyncError == "" || results[1].SyncError != "" || results[1].Pending {
		t.Fatalf("partial refusal: %+v", results)
	}
	// Stop proves all writers gone before any post-stop fetch/release.
	results, err = recovered.Stop(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].RecoveryLocation == "" || results[0].SyncError == "" {
		t.Fatal("refused member lost recovery state")
	}
	if !sandboxContainerGone(d.ID) {
		t.Fatal("runtime removal not proved")
	}
}

func TestP2DockerTmpfsByteAndInodeQuotaExhaustion(t *testing.T) {
	if os.Getenv("BLERG_P2_DOCKER_TEST") != "1" {
		t.Skip("set BLERG_P2_DOCKER_TEST=1")
	}
	label := "p2-quota-" + ccUUID()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		raw, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=blerg-test-run="+label).Output()
		if err == nil {
			for _, id := range strings.Fields(string(raw)) {
				_ = exec.CommandContext(ctx, "docker", "rm", "-f", id).Run()
			}
		}
	})
	// Capability test only: a bind mount does not inherit this tmpfs quota.
	// The production storage adapter must expose an equivalently enforced,
	// persistent allocation to the host hardened clone/fetch path.
	p2Docker(t, "run", "--rm", "--pull=never", "--network=none", "--label", "blerg-test-run="+label,
		"--tmpfs", "/limited:rw,size=1048576,nr_inodes=32", "--entrypoint", "python3", sandboxImage(), "-c", `import errno,os
try:
 with open('/limited/large','wb') as f: f.write(b'x'*(2*1024*1024))
 raise AssertionError('byte quota not enforced')
except OSError as e:
 assert e.errno == errno.ENOSPC
os.unlink('/limited/large')
try:
 for n in range(100): open('/limited/'+str(n),'w').close()
 raise AssertionError('inode quota not enforced')
except OSError as e:
 assert e.errno == errno.ENOSPC
`)
}

func TestP2DockerCrossMemberMetadata(t *testing.T) {
	for _, attack := range []string{"index-link", "git-link", "alternates-reset", "source-pruned"} {
		t.Run(attack, func(t *testing.T) {
			w, _ := p2DockerWorkspace(t)
			for _, m := range w.state.Members {
				childCommit(t, m.Clone, "change.txt")
			}
			bad, good := w.state.Members[0], w.state.Members[1]
			switch attack {
			case "index-link":
				p := filepath.Join(bad.Clone, ".git", "index")
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(good.Clone, ".git", "index"), p); err != nil {
					t.Fatal(err)
				}
			case "git-link":
				p := filepath.Join(bad.Clone, ".git")
				if err := os.Rename(p, p+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(good.Clone, ".git"), p); err != nil {
					t.Fatal(err)
				}
			case "alternates-reset":
				p := filepath.Join(bad.Clone, ".git", "objects", "info", "alternates")
				if err := os.WriteFile(p, []byte(filepath.Join(good.Clone, ".git", "objects")+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "source-pruned":
				// The exact object directory stays mounted and pinned; remove borrowed
				// objects in place, as a host-side pruning operation can do.
				entries, err := os.ReadDir(bad.Source.Objects)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Name() != "info" {
						if err := os.RemoveAll(filepath.Join(bad.Source.Objects, entry.Name())); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			results, err := w.Sync(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if results[1].SyncError != "" || results[1].Pending {
				t.Fatalf("unaffected member blocked: %+v", results)
			}
			if attack == "alternates-reset" {
				if results[0].SyncError != "" || results[0].Pending {
					t.Fatalf("safe alternates reset failed: %+v", results)
				}
				raw, err := os.ReadFile(filepath.Join(bad.Clone, ".git", "objects", "info", "alternates"))
				if err != nil || string(raw) != bad.Source.Objects+"\n" {
					t.Fatal("cross-member alternates were not reset")
				}
			} else if results[0].SyncError == "" || !results[0].Pending {
				t.Fatalf("tampered member accepted: %+v", results)
			}
		})
	}
}

type failingP2DockerContainer struct {
	*DockerWorkspaceContainer
	failure string
}

func (d *failingP2DockerContainer) Freeze(ctx context.Context) error {
	err := d.DockerWorkspaceContainer.Freeze(ctx)
	if err == nil && d.failure == "pause" {
		return fmt.Errorf("pause acknowledgement lost")
	}
	return err
}
func (d *failingP2DockerContainer) Thaw(ctx context.Context) error {
	if d.failure == "unpause" {
		return fmt.Errorf("unpause unavailable")
	}
	return d.DockerWorkspaceContainer.Thaw(ctx)
}
func (d *failingP2DockerContainer) Remove(ctx context.Context) error {
	if d.failure == "remove" {
		return fmt.Errorf("removal unavailable")
	}
	return d.DockerWorkspaceContainer.Remove(ctx)
}

func TestP2DockerLifecycleFailuresRetainWork(t *testing.T) {
	for _, failure := range []string{"pause", "unpause", "remove"} {
		t.Run(failure, func(t *testing.T) {
			w, d := p2DockerWorkspace(t)
			childCommit(t, w.state.Members[0].Clone, "change.txt")
			w.runtime = &failingP2DockerContainer{DockerWorkspaceContainer: d, failure: failure}
			var err error
			if failure == "remove" {
				_, err = w.Stop(t.Context())
			} else {
				_, err = w.Sync(t.Context())
			}
			if err == nil {
				t.Fatal("runtime failure accepted")
			}
			if _, err := os.Stat(w.state.Members[0].Clone); err != nil {
				t.Fatal("work deleted on runtime failure")
			}
			if failure != "unpause" {
				if _, err := childRepoGitErr(w.state.Members[0].Source.Repo, "rev-parse", "--verify", "refs/heads/wip/p2-session"); err == nil {
					t.Fatal("fetched without a proved writer fence")
				}
			} else if !w.state.Unavailable {
				t.Fatal("failed real unpause did not fence session")
			}
			if failure == "unpause" {
				recovered, recoveryErr := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, w.storage, d)
				if recoveryErr != nil {
					t.Fatal(recoveryErr)
				}
				if _, err := recovered.Sync(t.Context()); err != nil {
					t.Fatal("recovered frozen runtime cannot sync:", err)
				}
			}
		})
	}
}

func TestP2DockerConcurrentSyncStop(t *testing.T) {
	w, d := p2DockerWorkspace(t)
	childCommit(t, w.state.Members[0].Clone, "change.txt")
	observer := &blockingP2DockerContainer{DockerWorkspaceContainer: d, entered: make(chan struct{}), release: make(chan struct{})}
	w.runtime = observer
	synced := make(chan error, 1)
	stopped := make(chan error, 1)
	go func() { _, err := w.Sync(context.Background()); synced <- err }()
	<-observer.entered
	go func() { _, err := w.Stop(context.Background()); stopped <- err }()
	close(observer.release)
	if err := <-synced; err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if !w.state.Stopped || !sandboxContainerGone(d.ID) {
		t.Fatal("concurrent lifecycle did not finish safely")
	}
}

type blockingP2DockerContainer struct {
	*DockerWorkspaceContainer
	entered, release chan struct{}
}

func (d *blockingP2DockerContainer) Freeze(ctx context.Context) error {
	if err := d.DockerWorkspaceContainer.Freeze(ctx); err != nil {
		return err
	}
	close(d.entered)
	select {
	case <-d.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestP2DockerHeadIgnoresBlockingGitConfiguration(t *testing.T) {
	w, d := p2DockerWorkspace(t)
	m := w.state.Members[0]
	p2Docker(t, "exec", "-w", "/workspace/"+m.Member.Path, d.ID, "sh", "-c", "mkfifo include-fifo; printf '\\n[include]\\npath=../include-fifo\\n' >> .git/config")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	head, err := d.Head(ctx, m.Member.Path)
	if err != nil || head != m.Source.Base {
		t.Fatalf("head parsed hostile config: %q %v", head, err)
	}
	top := p2Docker(t, "top", d.ID, "-eo", "pid,args")
	if strings.Contains(top, "git --no-replace-objects") {
		t.Fatal("head probe leaked Git worker")
	}
}
