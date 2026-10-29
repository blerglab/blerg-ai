//go:build linux

package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
)

// Opt in on Linux hosts with a systemd user manager. No installed unit, token or
// state is read or changed. Each service and all writable directories are owned
// by this test. The properties below match the installer service template.
func TestSystemdHostShell(t *testing.T) {
	for _, test := range []struct{ name, syscall, cause string }{
		{"installer_properties", "", ""},
		{"helper_launch_denied", "prctl", "helper_start"},
		{"shell_tracing_denied", "ptrace", "exec_shell"},
	} {
		t.Run(test.name, func(t *testing.T) { testSystemdHostShell(t, test.syscall, test.cause) })
	}
}

func testSystemdHostShell(t *testing.T, deniedSyscall, expectedCause string) {
	t.Helper()
	binary := os.Getenv("BLERG_SHELL_TEST_BINARY")
	if binary == "" {
		t.Skip("set BLERG_SHELL_TEST_BINARY to a built daemon")
	}
	socketRoot, err := os.MkdirTemp(filepath.Dir(binary), "mx-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	root := t.TempDir()
	home, state := filepath.Join(root, "home"), filepath.Join(root, "state")
	for _, dir := range []string{home, state, filepath.Join(home, ".local", "bin"), filepath.Join(root, "project")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".local", "bin", "claude"), []byte("#!/bin/sh\nexec /bin/cat\n"), 0700); err != nil {
		t.Fatal(err)
	}
	init := exec.CommandContext(t.Context(), "git", "init", "-q", filepath.Join(root, "project"))
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("fixture project: %v: %s", err, out)
	}
	const sessionID = "6cd92706-07de-4012-8ac3-d447d8d17753"
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", Runtime: "host", OwnerID: "fixture-owner", LaunchGeneration: "legacy-" + sessionID, Digest: "fixture-digest", Members: []protocol.WorkspaceMember{{ID: "repo", Name: "repository", Path: ".", SourceIdentity: "fixture-source", CommonDirectoryIdentity: "fixture-common", Destination: "fixture-source"}}}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	upgrade := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !websocket.IsWebSocketUpgrade(r) {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrade.Upgrade(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		var hello protocol.DaemonHello
		if err := conn.ReadJSON(&hello); err != nil {
			result <- err
			return
		}
		if hello.Token != "systemd-fixture-credential" {
			result <- fmt.Errorf("unexpected disposable credential")
			return
		}
		caps := protocol.WorkspaceCapabilitiesFor("host", "linux")
		if err := conn.WriteJSON(protocol.DaemonWelcome{Type: "daemon_welcome", DaemonID: "fixture-connection", WorkspaceCapabilities: caps}); err != nil {
			result <- err
			return
		}
		if err := conn.WriteJSON(protocol.SpawnSession{Type: "spawn_session", Kind: "agent", SessionID: sessionID, Repo: "project", ProjectPath: filepath.Join(root, "project"), Cols: 80, Rows: 24}); err != nil {
			result <- err
			return
		}
		for {
			var event struct {
				Type string `json:"type"`
			}
			if err := conn.ReadJSON(&event); err != nil {
				result <- err
				return
			}
			if event.Type == "session_started" {
				break
			}
			if event.Type == "session_error" {
				result <- fmt.Errorf("fixture session could not start")
				return
			}
		}
		var output string
		rpc := func(req shellruntime.RPC) (shellruntime.RPC, error) {
			req.Type, req.Version, req.SessionID, req.RequestID = "owner_shell_request", 1, sessionID, req.Op
			if req.Op == "attach_workspace" {
				req.Type = "attach_session_workspace"
			}
			if err := conn.WriteJSON(req); err != nil {
				return shellruntime.RPC{}, err
			}
			for {
				var raw json.RawMessage
				if err := conn.ReadJSON(&raw); err != nil {
					return shellruntime.RPC{}, err
				}
				var envelope struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal(raw, &envelope)
				if envelope.Type == "local_workspace_control" {
					var control protocol.LocalWorkspaceControl
					if err := json.Unmarshal(raw, &control); err != nil {
						return shellruntime.RPC{}, err
					}
					binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: 1}, ManifestDigest: manifest.Digest, Root: control.Binding.Root}
					if err := conn.WriteJSON(protocol.LocalWorkspaceControlResult{Type: "local_workspace_control_result", RequestID: control.RequestID, SessionID: sessionID, OK: true, Manifest: &manifest, Binding: binding, ShellEligible: true, Launch: protocol.SupervisorLaunch{SessionID: sessionID, LaunchGeneration: manifest.LaunchGeneration, RuntimeGeneration: 1, Runtime: "host", ConnectionID: "fixture-connection"}}); err != nil {
						return shellruntime.RPC{}, err
					}
					continue
				}
				var reply shellruntime.RPC
				if err := json.Unmarshal(raw, &reply); err != nil {
					return reply, err
				}
				if reply.Type == "owner_shell_output" {
					output += string(reply.Data)
				}
				if reply.RequestID == req.RequestID {
					if !reply.OK {
						return reply, fmt.Errorf("%s refused: %s / %s", req.Op, reply.ErrorCode, reply.StartCause)
					}
					return reply, nil
				}
			}
		}
		if _, err := rpc(shellruntime.RPC{Op: "attach_workspace"}); err != nil {
			result <- err
			return
		}
		description, err := rpc(shellruntime.RPC{Op: "describe"})
		if err != nil {
			result <- err
			return
		}
		b, i := description.Binding, description.Identity
		consent := &protocol.HostShellConsent{ID: "fixture-consent", OwnerID: b.Owner, LaunchGeneration: b.Launch, MachineID: i.Machine, Account: i.Account, UID: i.UID, Shell: i.Shell, Cwd: i.Cwd, ChallengeID: "fixture-challenge"}
		b.Consent, b.HostConsentValid = consent.ID, true
		configured, err := rpc(shellruntime.RPC{Op: "configure", Binding: b, Key: pub, Consent: consent, Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}})
		if err != nil {
			result <- err
			return
		}
		b = configured.Binding
		now := time.Now().Unix()
		grant := ownershell.SignGrant(key, protocol.ShellGrant{Version: 1, Audience: b.Audience, OwnerID: b.Owner, LoginID: "fixture-login", SessionID: b.Session, LaunchGeneration: b.Launch, ConnectionID: b.Connection, ShellGeneration: "fixture-shell", Runtime: b.Runtime, ConsentID: b.Consent, PolicyRevision: 1, IssuedAt: now, ExpiresAt: now + 30})
		opened, err := rpc(shellruntime.RPC{Op: "open", Grant: grant})
		if expectedCause != "" {
			if err == nil || opened.StartCause != expectedCause {
				result <- fmt.Errorf("containment denial lost: %s / %s", opened.ErrorCode, opened.StartCause)
				return
			}
			result <- nil
			return
		}
		if err != nil {
			result <- err
			return
		}
		// The input contains no output marker; only execution can produce it.
		if _, err = rpc(shellruntime.RPC{Op: "input", Grant: grant, Seq: 1, Data: []byte("printf '\\123\\131\\123\\124\\105\\115\\104\\137\\120\\124\\131'\n")}); err != nil {
			result <- err
			return
		}
		for !strings.Contains(output, "SYSTEMD_PTY") {
			var frame shellruntime.RPC
			if err = conn.ReadJSON(&frame); err != nil {
				result <- err
				return
			}
			if frame.Type == "owner_shell_output" {
				output += string(frame.Data)
			}
		}
		if _, err = rpc(shellruntime.RPC{Op: "resize", Grant: grant, Seq: 1, Cols: 120, Rows: 40}); err != nil {
			result <- err
			return
		}
		launch := ownershell.SupervisorIdentity(b, grant).Launch
		closed, err := rpc(shellruntime.RPC{Op: "close_launch", Launch: launch})
		if err != nil {
			result <- err
			return
		}
		if closed.Cleanup == nil || closed.Cleanup.ValidateFor(launch, "") != nil {
			result <- fmt.Errorf("missing exact cleanup proof")
			return
		}
		result <- nil
	}))
	defer server.Close()
	unit := fmt.Sprintf("blerg-shell-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	logPath := filepath.Join(root, "service.log")
	args := []string{"--user", "--unit=" + unit, "--collect", "-p", "Type=simple", "-p", "Restart=on-failure", "-p", "RestartSec=15", "-p", "KillMode=mixed", "-p", "StartLimitIntervalSec=0", "-p", "StandardOutput=append:" + logPath, "-p", "StandardError=inherit"}
	for _, value := range []string{"HOME=" + home, "TMPDIR=" + root, "BLERG_RUNNER_DAEMON_STATE_DIR=" + state, "BLERG_RUNNER_DAEMON_TOKEN=systemd-fixture-credential", "BLERG_RUNNER_SERVER_URL=ws" + strings.TrimPrefix(server.URL, "http"), "BLERG_RUNNER_REPOS_ROOT=" + root, "TMUX_TMPDIR=" + socketRoot, "BLERG_RUNNER_CLAUDE_USAGE=0", "GORACE=atexit_sleep_ms=0"} {
		args = append(args, "--setenv="+value)
	}
	if deniedSyscall != "" {
		args = append(args, "-p", "SystemCallFilter=~"+deniedSyscall, "-p", "SystemCallErrorNumber=EPERM")
	}
	args = append(args, binary)
	cmd := exec.CommandContext(t.Context(), "systemd-run", args...)
	// Some minimal user managers expose only their private socket, not a bus.
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "bus")); os.IsNotExist(err) {
		cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "systemd/private"))
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start disposable service: %v: %s", err, output)
	}
	t.Cleanup(func() {
		// Only this test's exact transient unit can be stopped, including a retry
		// after a failed assertion. Never target the installed daemon.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "systemctl", "--user", "stop", unit+".service").Run()
	})
	select {
	case err := <-result:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(25 * time.Second):
		t.Error("disposable service did not complete shell flow")
	}
	if t.Failed() {
		logData, _ := os.ReadFile(logPath)
		t.Logf("disposable daemon log: %s", logData)
	}
}
