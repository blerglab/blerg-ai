//go:build linux

package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestP6EngineWaitsForMessage(t *testing.T) {
	root := t.TempDir()
	for _, member := range []string{"one", "two"} {
		dir := filepath.Join(root, member)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		p6EnabledGit(t, dir, "init", "-q", "-b", "main")
		p6EnabledGit(t, dir, "-c", "user.name=Example", "-c", "user.email=developer@example.com", "commit", "--allow-empty", "-qm", "initial")
	}
	binary := filepath.Join(root, "claude")
	if err := os.WriteFile(binary, []byte(p6EnabledEngine), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary)
	cmd.Dir = root
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	reader := bufio.NewReader(out)
	if line, err := reader.ReadString('\n'); err != nil || !strings.Contains(line, `"subtype":"init"`) {
		t.Fatalf("fixture did not initialize: %q %v", line, err)
	}
	for _, member := range []string{"one", "two"} {
		if got := p6EnabledGit(t, filepath.Join(root, member), "log", "-1", "--format=%s"); got != "initial" {
			t.Fatalf("fixture committed before receiving a message: %s", got)
		}
	}
	if _, err := io.WriteString(in, "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"commit both members\"}}\n"); err != nil {
		t.Fatal(err)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, `"type":"result"`) {
			break
		}
	}
	for _, member := range []string{"one", "two"} {
		if got := p6EnabledGit(t, filepath.Join(root, member), "log", "-1", "--format=%s"); got != "enabled" {
			t.Fatalf("message did not commit %s: %s", member, got)
		}
	}
}

func TestP6SandboxEngineFixture(t *testing.T) {
	if os.Getenv("BLERG_HOST_HELPER_TEST_BINARY") == "" {
		t.Skip("build the production daemon and supply BLERG_HOST_HELPER_TEST_BINARY")
	}
	image := os.Getenv("BLERG_RUNNER_SANDBOX_IMAGE")
	if image == "" {
		image = "blerg-runner-sandbox:latest"
	}
	image = p6SandboxImage(t, image)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--pull=never", "--network=none", "--entrypoint", "sha256sum", image, "/usr/local/bin/claude").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != fmt.Sprintf("%x  /usr/local/bin/claude", sha256.Sum256([]byte(p6EnabledEngine))) {
		t.Fatalf("sandbox must run the scripted engine fixture: %v: %s", err, out)
	}
}

// Keep production runtime setup, but own the engine and trusted helper versions.
// Host PATH does not replace an image's CLI, and a prebuilt helper can lag this
// checkout even when its protocol probe still passes.
func p6SandboxImage(t *testing.T, base string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(binary string, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("prepare sandbox fixture (%s): %v: %s", binary, err, out)
		}
		return out
	}
	// Resolve the local base to an immutable ID; never pull or replace its tag.
	base = strings.TrimSpace(string(run("docker", "image", "inspect", "--format", "{{.Id}}", base)))
	image := "blerg-p6-test:" + newUUID()
	baseTag := image + "-base"
	run("docker", "image", "tag", base, baseTag)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "docker", "image", "rm", baseTag).CombinedOutput(); err != nil {
			t.Errorf("remove owned sandbox base tag: %v: %s", err, out)
		}
	})
	for _, helper := range []string{"shell-supervisor", "workspace-explorer"} {
		run("go", "build", "-o", filepath.Join(dir, "blerg-"+helper), "../../cmd/"+helper)
	}
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(p6EnabledEngine), 0700); err != nil {
		t.Fatal(err)
	}
	dockerfile := fmt.Sprintf(`FROM %s
LABEL blerg.test.p6=%q
USER root
RUN rm -f /usr/local/bin/claude
COPY --chmod=0755 claude blerg-shell-supervisor blerg-workspace-explorer /usr/local/bin/
USER %d:%d
`, baseTag, image, os.Getuid(), os.Getgid())
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
		t.Fatal(err)
	}
	run("docker", "build", "--pull=false", "--network=none", "-t", image, dir)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// A refused preparation can retain quota keepers before the session is
		// registered. These resources belong solely to this disposable image.
		out, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--no-trunc", "--filter", "label=blerg.test.p6="+image).Output()
		if err != nil {
			t.Errorf("list owned fixture containers: %v", err)
		}
		var tokens []string
		for _, id := range strings.Fields(string(out)) {
			raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "blerg.workspace.storage"}}`, id).Output()
			if err != nil {
				t.Errorf("inspect owned fixture container: %v", err)
				continue
			}
			if token := strings.TrimSpace(string(raw)); token != "" && token != "<no value>" {
				tokens = append(tokens, token)
			}
			if out, err := exec.CommandContext(ctx, "docker", "rm", "-f", id).CombinedOutput(); err != nil {
				t.Errorf("remove owned fixture container: %v: %s", err, out)
			}
		}
		for _, token := range tokens {
			raw, err := exec.CommandContext(ctx, "docker", "volume", "ls", "-q", "--filter", "label=blerg.workspace.storage="+token).Output()
			if err != nil {
				t.Errorf("list owned fixture volumes: %v", err)
			}
			for _, volume := range strings.Fields(string(raw)) {
				if out, err := exec.CommandContext(ctx, "docker", "volume", "rm", volume).CombinedOutput(); err != nil {
					t.Errorf("remove owned fixture volume: %v: %s", err, out)
				}
			}
		}
		if out, err := exec.CommandContext(ctx, "docker", "image", "rm", image).CombinedOutput(); err != nil {
			t.Errorf("remove owned sandbox fixture image: %v: %s", err, out)
		}
	})
	return image
}
