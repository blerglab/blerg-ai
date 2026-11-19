package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/keepertest"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

func TestRound10NeverOpenedMembershipRealKeeperUnavailable(t *testing.T) {
	binary := keepertest.Build(t)
	incompatible := keepertest.BuildProtocol(t, shellkeeper.Version+1)
	for _, state := range []string{"never_started", "incompatible", "down"} {
		t.Run(state, func(t *testing.T) {
			var service *shellruntime.Service
			if state == "down" {
				r := round8Attach(t, binary)
				r.transition(t, "down")
				service = r.service
			} else {
				root := t.TempDir()
				if err := keepertest.Run(t, binary, root, "shell-keeper", "init"); err != nil {
					t.Fatal(err)
				}
				if state == "incompatible" {
					keepertest.Start(t, incompatible, root)
				}
				dir := filepath.Join(root, "shell-keeper")
				authority, err := shellkeeper.AcquireDaemon(dir)
				if err != nil {
					t.Fatal(err)
				}
				service = shellruntime.NewService(func(shellruntime.RPC) error { return nil })
				bridge := shellruntime.NewReconnectingHostBridge(t.Context(), func(ctx context.Context) (shellruntime.HostBridge, error) {
					view, err := shellkeeper.InspectSnapshot(ctx, dir, authority.Epoch)
					if err != nil {
						return nil, err
					}
					b, err := shellruntime.ConnectKeeper(ctx, dir, authority.Epoch, view.Generation)
					if err != nil {
						return nil, err
					}
					return b, nil
				})
				service.SetHostBridge(bridge)
				t.Cleanup(func() { _ = service.Shutdown(context.Background()); _ = bridge.Close(); _ = authority.Close() })
			}
			f := membershipSetup(t)
			f.service.Supervisor = newWorkspaceSupervisors(service)
			f.service.membershipShells = nil
			f.w.authorize = func(context.Context) error { return nil }
			f.w.state.HostWorkDir = f.w.state.Root
			f.w.state.HostKeeperNeverOpened = true
			if err := f.w.save(); err != nil {
				t.Fatal(err)
			}
			if err := f.service.Supervisor.(*workspaceSupervisors).registerHost(t.Context(), f.w.host); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(100, 0)
			f.service.membershipClock = func() time.Time { return now }
			f.service.ApplyMembership(t.Context(), f.msg)
			if len(f.server.reports) != 1 || f.server.reports[0].Waiting != protocol.MembershipWaitKeeper {
				t.Fatal("unknown state misreported", f.server.reports)
			}
			now = now.Add(6 * time.Second)
			f.service.ApplyMembership(t.Context(), f.msg)
			if f.server.installed != 1 || f.saved().Binding.ManifestDigest != f.msg.Proposed.Digest {
				t.Fatal("agent membership wedged on keeper", state, f.server.reports)
			}
		})
	}
}
