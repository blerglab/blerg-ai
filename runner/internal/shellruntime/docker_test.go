//go:build linux

package shellruntime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestRealDockerSupervisor(t *testing.T) {
	binary := os.Getenv("BLERG_SHELL_HELPER_TEST_BINARY")
	if binary == "" {
		t.Skip("explicit real Docker helper binary not supplied")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	label := hex.EncodeToString(random[:])
	docker := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("owned Docker operation failed: %v", err)
		}
		return out
	}
	id := strings.TrimSpace(string(docker("run", "-d", "--label", "blerg-p5-shell-test="+label, "--user", "1000:1000", "--pids-limit", "160", "--memory", "768m", "--mount", "type=bind,src="+binary+",dst="+FixedContainerSupervisor+",readonly", "--tmpfs", "/workspace:rw,uid=1000,gid=1000", "blerg-runner-sandbox:latest", "sleep", "infinity")))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		raw, e := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Config.Labels}}", id).Output()
		if e != nil {
			t.Error("cannot verify owned container before cleanup")
			return
		}
		var labels map[string]string
		if json.Unmarshal(raw, &labels) != nil || labels["blerg-p5-shell-test"] != label {
			t.Error("owned-container label mismatch; refused removal")
			return
		}
		if exec.CommandContext(ctx, "docker", "rm", "-f", id).Run() != nil {
			t.Error("owned-container cleanup failed")
		}
	})
	var device, inode uint64
	if _, err = fmt.Sscanf(string(docker("exec", id, "stat", "-c", "%d %i", "/workspace")), "%d %d", &device, &inode); err != nil {
		t.Fatal(err)
	}
	b := ownershell.Binding{RuntimeGeneration: 1, Root: protocol.WorkspaceRoot{Device: device, Inode: inode}, ManifestDigest: "digest", Session: "docker-session", Owner: "owner", Launch: "launch", Connection: "connection", Audience: ownershell.RuntimeAudience("connection", 1), Runtime: "sandbox", Live: true}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	command := SandboxCommand(id, "1000:1000", Identity{Cwd: "/workspace"})
	fresh := true
	command.Recovery = func(context.Context, protocol.SupervisorLaunch) error {
		if fresh {
			fresh = false
			return nil
		}
		return ErrClosed
	}
	command.Policy = protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}
	c, err := NewCoordinator(new(sync.Mutex), b, command, pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.RecoverLaunch(t.Context(), c.launch()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = c.CloseLaunch(ctx, c.launch(), "test_cleanup")
	})
	now := time.Now().Unix()
	g := ownershell.SignGrant(key, protocol.ShellGrant{Version: 1, Audience: b.Audience, OwnerID: b.Owner, LoginID: "login", SessionID: b.Session, LaunchGeneration: b.Launch, ConnectionID: b.Connection, ShellGeneration: "shell", Runtime: b.Runtime, PolicyRevision: 1, IssuedAt: now, ExpiresAt: now + 30})
	out, err := c.Open(t.Context(), b, g, pub)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Input(t.Context(), g, 1, []byte("setsid /bin/sh -c 'echo $$ > /workspace/detached.pid; while :; do sleep 1; done' </dev/null >/dev/null 2>&1 &\nprintf '\\104\\117\\103\\113\\105\\122'\n")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, out, "DOCKER")
	// Confirm the detached process really exists before requesting cleanup.
	pid := strings.TrimSpace(string(docker("exec", id, "/bin/sh", "-c", "while [ ! -s /workspace/detached.pid ]; do sleep 0.01; done; cat /workspace/detached.pid")))
	proof, err := c.CloseLaunch(t.Context(), c.launch(), "disable")
	if err != nil {
		t.Fatal(err)
	}
	if err = proof.ValidateFor(c.launch(), ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, "docker", "exec", id, "kill", "-0", pid).Run() == nil {
		t.Fatal("in-container descendant survived cleanup acknowledgment")
	}
	if _, err = c.RecoverLaunch(t.Context(), c.launch()); err != nil {
		t.Fatal(err)
	}
	lost := g
	lost.Grant.ShellGeneration = "lost-client"
	lost.Grant.IssuedAt = time.Now().Unix()
	lost.Grant.ExpiresAt = lost.Grant.IssuedAt + 2
	lost = ownershell.SignGrant(key, lost.Grant)
	stream, err := c.Open(t.Context(), b, lost, pub)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Input(t.Context(), lost, 1, []byte("echo $$ > /workspace/lost.pid; printf '\\114\\117\\123\\124'\n")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, stream, "LOST")
	rootPID := strings.TrimSpace(string(docker("exec", id, "cat", "/workspace/lost.pid")))
	c.sessionMu.Lock()
	peer := c.peers[lost.Grant.ShellGeneration]
	c.sessionMu.Unlock()
	if err = peer.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	} // exactly this test's docker-exec client
	select {
	case <-peer.done:
	case <-time.After(3 * time.Second):
		t.Fatal("owned exec client did not exit")
	}
	if _, err = c.CloseLaunch(t.Context(), c.launch(), "client_lost"); err == nil {
		t.Fatal("host docker-client death mistaken for in-container cleanup proof")
	}
	until := time.NewTimer(4 * time.Second)
	defer until.Stop()
	for {
		probe, stop := context.WithTimeout(t.Context(), time.Second)
		alive := exec.CommandContext(probe, "docker", "exec", id, "kill", "-0", rootPID).Run() == nil
		stop()
		if !alive {
			break
		}
		select {
		case <-until.C:
			t.Fatal("in-container writer outlived its disconnected local lease")
		case <-time.After(10 * time.Millisecond):
		}
	}

}
