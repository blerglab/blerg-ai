//go:build linux

package shellkeeper

import (
	"archive/tar"
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestGateRequiresDifferentUIDFixture(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRealDifferentUIDPeersBothDirections$")
	cmd.Env = append(os.Environ(), "PATH=", "BLERG_KEEPER_UID_REQUIRED=1")
	out, err := cmd.CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("gate requires real different-UID peers")) {
		t.Fatalf("gate silently skipped unavailable UID proof: %v %s", err, out)
	}
}

// Docker supplies the privilege to make a real UID 65534 peer even when the
// session itself has no_new_privs. The fixture mounts only disposable test data.
func TestRealDifferentUIDPeersBothDirections(t *testing.T) {
	image := os.Getenv("BLERG_KEEPER_UID_IMAGE")
	if out, err := exec.CommandContext(t.Context(), "docker", "info", "--format", "{{.OSType}}").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "linux" {
		if os.Getenv("BLERG_KEEPER_UID_REQUIRED") == "1" {
			t.Fatalf("gate requires real different-UID peers: %v %s", err, out)
		}
		t.Skip("Linux Docker unavailable; pre-merge gate requires this proof")
	}
	bin := filepath.Join(t.TempDir(), "uidpeer")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./testdata/uidpeer")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, out)
	}
	if err := os.Chmod(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if image == "" {
		id, err := nonce()
		if err != nil {
			t.Fatal(err)
		}
		image = "blerg-keeper-uid-test:" + id[:12]
		// Import an empty filesystem: no registry, shell, network or build cache.
		// The static Go fixture is bind-mounted below; remove the image always.
		var empty bytes.Buffer
		if err = tar.NewWriter(&empty).Close(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", image).Run() })
		importImage := exec.CommandContext(t.Context(), "docker", "import", "-", image)
		importImage.Stdin = &empty
		if out, err := importImage.CombinedOutput(); err != nil {
			t.Fatalf("UID fixture image: %v %s", err, out)
		}
	}
	for _, direction := range []string{"server_rejects_client", "client_rejects_server", "stale_socket_other_uid"} {
		t.Run(direction, func(t *testing.T) {
			dir := provision(t)
			a, err := AcquireDaemon(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = a.Close() }()
			var server *Server
			if direction == "server_rejects_client" {
				server, err = Listen(t.Context(), dir)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = server.Close() }()
				data, err := json.Marshal(clientHello(t, a.Epoch))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, "request.json"), data, 0644); err != nil {
					t.Fatal(err)
				}
				// Deliberately expose only this fixture's socket, to isolate the
				// SO_PEERCRED check from the normal private-directory protection.
				if err = os.Chmod(filepath.Join(dir, "keeper.sock"), 0666); err != nil {
					t.Fatal(err)
				}
			}
			if err = os.Chmod(dir, 0777); err != nil {
				t.Fatal(err)
			}
			id, err := nonce()
			if err != nil {
				t.Fatal(err)
			}
			name := "blerg-keeper-uid-" + id[:12]
			args := []string{"run", "--rm", "--pull=never", "--name", name, "--network=none", "--read-only", "--cap-drop=ALL", "--mount", "type=bind,src=" + bin + ",dst=/peer,readonly", "--mount", "type=bind,src=" + dir + ",dst=/fixture", "--entrypoint", "/peer"}
			if direction == "server_rejects_client" {
				args = append(args, "--user", "65534:65534", image, "client")
			} else {
				owner := os.Getuid()
				if direction == "stale_socket_other_uid" {
					owner = 65534
				}
				args = append(args, "--user", "0:0", "--cap-add=SETUID", "--cap-add=SETGID", "--cap-add=CHOWN", image, "server", strconv.Itoa(owner))
			}
			cmd := exec.CommandContext(t.Context(), "docker", args...)
			defer func() { _ = exec.Command("docker", "rm", "--force", name).Run() }()
			if direction == "server_rejects_client" {
				out, err := cmd.CombinedOutput()
				if err != nil || strings.TrimSpace(string(out)) != "refused" {
					t.Fatalf("different-UID client received challenge: %v %s", err, out)
				}
			} else {
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
				reader := bufio.NewReader(stdout)
				if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
					t.Fatalf("UID server readiness: %v %s", err, stderr.String())
				}
				// Restore the client directory proof. The fixture root chowned
				// the socket to us; its listening peer still has real UID 65534.
				if err = os.Chmod(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if direction == "stale_socket_other_uid" {
					if server, err := Listen(t.Context(), dir); err == nil {
						_ = server.Close()
						t.Fatal("foreign UID socket removed under flock")
					}
					return
				}
				if _, err = Inspect(t.Context(), dir, a.Epoch); err == nil {
					t.Fatal("different-UID keeper accepted")
				}
				out, err := io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				if err = cmd.Wait(); err != nil || strings.TrimSpace(string(out)) != "refused" {
					t.Fatalf("daemon released hello to different-UID keeper: %v %s %s", err, out, stderr.String())
				}
			}
		})
	}
}
