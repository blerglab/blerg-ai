package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/keepertest"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

type round8Host struct {
	service       *shellruntime.Service
	bridge        *shellruntime.ReconnectingHostBridge
	authority     *shellkeeper.DaemonAuthority
	process       *keepertest.Process
	state, binary string
	hold          atomic.Bool
	pending       chan protocol.ShellLinkHello
}

func round8Attach(t *testing.T, binary string) *round8Host {
	t.Helper()
	r := &round8Host{binary: binary, state: t.TempDir(), pending: make(chan protocol.ShellLinkHello, 4)}
	if err := keepertest.Run(t, binary, r.state, "shell-keeper", "init"); err != nil {
		t.Fatal(err)
	}
	r.process = keepertest.Start(t, binary, r.state)
	var err error
	r.authority, err = shellkeeper.AcquireDaemon(filepath.Join(r.state, "shell-keeper"))
	if err != nil {
		t.Fatal(err)
	}
	r.service = shellruntime.NewService(func(frame shellruntime.RPC) error {
		if frame.Type == "daemon_shell_hello" && frame.ShellHello != nil {
			if r.hold.Load() {
				r.pending <- *frame.ShellHello
				return nil
			}
			return r.welcome(t, *frame.ShellHello)
		}
		return nil
	})
	if err = r.service.ConfigureHostRetirements(filepath.Join(r.state, "retirements")); err != nil {
		t.Fatal(err)
	}
	r.bridge = shellruntime.NewReconnectingHostBridge(t.Context(), func(ctx context.Context) (shellruntime.HostBridge, error) {
		view, err := shellkeeper.InspectSnapshot(ctx, filepath.Join(r.state, "shell-keeper"), r.authority.Epoch)
		if err != nil {
			return nil, err
		}
		b, err := shellruntime.ConnectKeeper(ctx, filepath.Join(r.state, "shell-keeper"), r.authority.Epoch, view.Generation)
		if err != nil {
			return nil, err
		}
		return b, nil
	})
	r.service.SetHostBridge(r.bridge)
	t.Cleanup(func() { _ = r.service.Shutdown(context.Background()); _ = r.bridge.Close(); _ = r.authority.Close() })
	guard, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err = r.bridge.WaitReady(guard); err != nil {
		t.Fatal("real keeper adoption", err)
	}
	return r
}
func (r *round8Host) welcome(t *testing.T, hello protocol.ShellLinkHello) error {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	welcome := protocol.ShellLinkWelcome{Version: 2, Runner: "runner", Daemon: hello.Daemon, Challenge: hello.Challenge, Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: "owner", TokenID: "credential", Revision: 1, ExpiresAt: time.Now().Add(time.Hour)}}
	return r.service.AcceptShellLink(ctx, hello, &welcome)
}
func (r *round8Host) transition(t *testing.T, state string) {
	t.Helper()
	guard, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	switch state {
	case "stalled":
		r.process.Signal(t, syscall.SIGSTOP)
		if err := r.bridge.WaitUnavailable(guard); err != nil {
			t.Fatal("heartbeat missed real stopped keeper", err)
		}
	case "down":
		r.process.Close(t)
		if err := r.bridge.WaitUnavailable(guard); err != nil {
			t.Fatal(err)
		}
	case "restarting":
		r.process.Close(t)
		if err := r.bridge.WaitUnavailable(guard); err != nil {
			t.Fatal(err)
		}
		r.hold.Store(true)
		r.process = keepertest.Start(t, r.binary, r.state)
		select {
		case <-r.pending:
		case <-guard.Done():
			t.Fatal("new keeper never reached adoption")
		}
	}
}

// Every keeper is a real bundled daemon child. Stalled is SIGSTOP with an
// established socket; down has no process; restarting has a new, listening
// process with an actual fresh adoption transaction intentionally pending.
func TestRound8AgentLifecycleRealKeeperMatrix(t *testing.T) {
	binary := keepertest.Build(t)
	next := keepertest.DifferentImage(t, binary)
	for _, state := range []string{"healthy", "stalled", "down", "restarting"} {
		for _, op := range []string{"start", "sync", "membership", "Stop", "reconnect", "install"} {
			t.Run(state+"/"+op, func(t *testing.T) {
				r := round8Attach(t, binary)
				if op == "membership" {
					f := membershipSetup(t)
					f.w.authorize = func(context.Context) error { return nil }
					f.w.state.HostWorkDir = f.w.state.Root
					registry := newWorkspaceSupervisors(r.service)
					f.service.Supervisor = registry
					f.service.membershipShells = nil
					if err := registry.registerHost(t.Context(), f.w.host); err != nil {
						t.Fatal(err)
					}
					if _, err := r.service.Binding(f.w.state.Session); err != nil {
						t.Fatal("real registration", err)
					}
					if state == "healthy" {
						r.bridge.RequestRecovery()
						guard, cancel := context.WithTimeout(t.Context(), 10*time.Second)
						defer cancel()
						if err := r.bridge.WaitReady(guard); err != nil {
							t.Fatal("observed registration", err)
						}
					}
					r.transition(t, state)
					if state != "healthy" && !f.service.shellsOpen(f.w.state.Session) {
						t.Fatal("unknown keeper occupancy admitted a membership change")
					}
					f.service.ApplyMembership(t.Context(), f.msg)
					if state == "healthy" {
						if f.saved().Binding.ManifestDigest != f.msg.Proposed.Digest || f.saved().Membership != nil || f.server.installed != 1 {
							t.Fatal("healthy membership failed", f.server.reports)
						}
					} else if f.server.commits != 0 || f.server.installed != 0 || len(f.server.reports) != 1 || f.server.reports[0].Waiting != protocol.MembershipWaitKeeper {
						t.Fatal("unknown shell state did not pause membership safely", f.server.reports)
					}
					if _, err := f.w.Stop(t.Context()); err != nil || !f.w.state.Stopped {
						t.Fatal("membership then Stop", err)
					}
					return
				}
				svc, w, h, l, head := round7HostWorkspace(t)
				registry := newWorkspaceSupervisors(r.service)
				svc.Supervisor = registry
				if err := registry.registerHost(t.Context(), h); err != nil {
					t.Fatal(err)
				}
				if _, err := r.service.Binding(l.SessionID); err != nil {
					t.Fatal("real registration", err)
				}
				r.transition(t, state)
				switch op {
				case "start":
					w.state.Session = "new-agent"
					w.state.SupervisorLaunch.SessionID = w.state.Session
					if err := registry.registerHost(t.Context(), h); err != nil {
						t.Fatal("agent start", err)
					}
				case "sync":
					resumed := false
					svc.ResumeStatus = func(context.Context, string) error { resumed = true; return nil }
					results, err := w.Sync(t.Context())
					if err != nil || len(results) != 1 || results[0].Head != head || results[0].Pending || !resumed {
						t.Fatal("agent sync/status", err, resumed)
					}
				case "Stop":
					if _, err := w.Stop(t.Context()); err != nil || !w.state.Stopped {
						t.Fatal("agent Stop", err)
					}
				case "reconnect":
					if err := h.reconnect(t.Context(), func(protocol.SessionEnded) error { return nil }); err != nil {
						t.Fatal("agent reconnect", err)
					}
				case "install":
					destination := filepath.Join(t.TempDir(), "installed-daemon")
					if err := os.WriteFile(destination, []byte("older daemon"), 0755); err != nil {
						t.Fatal(err)
					}
					if err := keepertest.Run(t, next, r.state, "shell-keeper", "install", destination); err != nil {
						t.Fatal("different-image install depended on keeper", err)
					}
					installed, err := os.ReadFile(destination)
					if err != nil {
						t.Fatal(err)
					}
					image, err := os.ReadFile(next)
					if err != nil || string(installed) != string(image) {
						t.Fatal("daemon image not replaced", err)
					}
				}
			})
		}
	}
}

func TestRound8StopPersistsRetirementBeforeSideEffects(t *testing.T) {
	for _, op := range []string{"Stop", "RequestStop", "legacy_close", "Kill"} {
		t.Run(op, func(t *testing.T) {
			svc, w, h, _, _ := round7HostWorkspace(t)
			service := svc.Supervisor.(*workspaceSupervisors).service
			dir := filepath.Join(t.TempDir(), "retirements")
			if err := service.ConfigureHostRetirements(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(dir, dir+".unavailable"); err != nil {
				t.Fatal(err)
			}
			switch op {
			case "Stop":
				if _, err := h.stop(t.Context()); err != nil || !w.state.Stopped {
					t.Fatal("retirement store blocked Stop", err)
				}
			case "RequestStop":
				if err := w.RequestStop(protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session}); err != nil || !w.state.StopRequested {
					t.Fatal("retirement store blocked stop delivery", err)
				}
			case "legacy_close":
				legacy := &legacySessionWorkspace{session: w.state.Session, shells: service, shellRegistered: true}
				if err := legacy.closeContext(t.Context()); err != nil || !legacy.closed {
					t.Fatal("retirement store blocked legacy close", err)
				}
			case "Kill":
				sender := &agentTestSender{}
				canceled := false
				sess := &agentSession{localWorkspace: w, loop: &fakeRefreshDriver{}, cancel: func() { canceled = true }, emitter: newWSEmitter(sender, w.state.Session, nil)}
				host := &AgentHost{sender: sender, sessions: map[string]*agentSession{w.state.Session: sess}, cfg: AgentHostConfig{Workspaces: svc}}
				host.Kill(w.state.Session)
				if !canceled || host.get(w.state.Session) != nil {
					t.Fatal("retirement store prevented agent kill")
				}
			}
		})
	}
}
