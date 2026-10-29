//go:build linux

package daemon

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

func TestResumedLegacyHostConsentAndOpen(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	host, _, project := newTestHost(t, &scriptedAgentProvider{}, "")
	initChildRepo(t, project)
	const id = "resumed-host"
	host.spawn(protocol.SpawnSession{SessionID: id, Kind: "agent", ProjectPath: project, Repo: "project"}, nil, "", &adoptRequest{})
	defer host.Kill(id)
	rig := newCoordRig(t)
	service := hostLifecycleTestService(t, nil)
	manager := newManagerWithSender(newRecordingSender(16), ManagerConfig{ReposRoot: filepath.Dir(project), Workspaces: service})
	manager.agents = host
	source := manager.workspaceFileBinding(id)
	authority := legacyTestAuthority(id, source)
	authorize := func(ctx context.Context, root protocol.WorkspaceRoot) (protocol.LocalWorkspaceControlResult, error) {
		reply, err := authority(ctx, root)
		reply.ShellEligible = true
		return reply, err
	}
	caps := protocol.WorkspaceCapabilitiesFor("host", "linux")
	caps.ShellVersion = 0
	if err := manager.attachLegacyWorkspaceWith(t.Context(), id, caps, rig.service, authorize); err != nil {
		t.Fatal(err)
	}
	caps.ShellVersion = 1
	if err := rig.service.Rebind(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := manager.attachLegacyWorkspaceWith(t.Context(), id, caps, rig.service, authorize); err != nil {
		t.Fatal(err)
	}
	description, err := rig.call(t.Context(), shellruntime.RPC{SessionID: id, Op: "describe"})
	if err != nil {
		t.Fatal("describe", err)
	}
	b, i := description.Binding, description.Identity
	config := shellruntime.RPC{SessionID: id, Op: "configure", Binding: b, Key: rig.key.Public().(ed25519.PublicKey), Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}}
	if reply, _ := rig.call(t.Context(), config); reply.OK {
		t.Fatal("host shell configured without consent")
	}
	config.Consent = &protocol.HostShellConsent{ID: "consent", OwnerID: b.Owner, LaunchGeneration: b.Launch, MachineID: i.Machine, Account: i.Account, UID: i.UID, Shell: i.Shell, Cwd: i.Cwd, ChallengeID: "challenge"}
	config.Binding.Consent, config.Binding.HostConsentValid = "consent", true
	configured, err := rig.call(t.Context(), config)
	if err != nil {
		t.Fatalf("configure after consent: %s", configured.ErrorCode)
	}
	rig.binding = configured.Binding
	rig.open(t, "resumed-shell", time.Now().Unix()+30)
	rig.input(t, 1, "printf '\\122\\105\\123\\125\\115\\105\\104'\n")
	rig.wait(t, "RESUMED")
	// A completed helper invalidates its host identity observation. The next
	// describe must rotate consent without stranding this live legacy session.
	rig.input(t, 2, "exit\n")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		next, e := rig.call(ctx, shellruntime.RPC{SessionID: id, Op: "describe"})
		if e == nil && next.Binding.Connection != rig.binding.Connection {
			// The server must be able to retire the old handle after describe
			// rotated identity, using only its already completed cleanup receipt.
			closed, e := rig.call(ctx, shellruntime.RPC{SessionID: id, Op: "close", Grant: rig.grant})
			oldLaunch := ownershell.SupervisorIdentity(rig.binding, rig.grant).Launch
			if e != nil || closed.Cleanup == nil || closed.Cleanup.ValidateFor(oldLaunch, rig.grant.Grant.ShellGeneration) != nil {
				t.Fatal("old handle lost its cleanup proof after identity rotation")
			}
			forged := rig.grant
			forged.Grant.OwnerID = "foreign-owner"
			if receipt, _ := rig.call(ctx, shellruntime.RPC{SessionID: id, Op: "close", Grant: forged}); receipt.OK {
				t.Fatal("cleanup receipt crossed owner identity")
			}
			forged = rig.grant
			forged.Grant.ShellGeneration = "unrecorded-shell"
			if receipt, _ := rig.call(ctx, shellruntime.RPC{SessionID: id, Op: "close", Grant: forged}); receipt.OK {
				t.Fatal("cleanup proof invented for unknown shell")
			}
			if stale, _ := rig.call(ctx, shellruntime.RPC{SessionID: id, Op: "input", Grant: rig.grant, Seq: 3, Data: []byte("stale")}); stale.OK {
				t.Fatal("old grant regained execution")
			}
			config.Binding = next.Binding
			config.Consent.ID = "fresh-consent"
			config.Consent.ChallengeID = "fresh-challenge"
			config.Binding.Consent, config.Binding.HostConsentValid = config.Consent.ID, true
			result, e := rig.call(ctx, config)
			if e != nil {
				t.Fatalf("fresh consent after helper exit refused: %s", result.ErrorCode)
			}
			rig.binding = result.Binding
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("helper exit did not rotate consent")
		case <-time.After(10 * time.Millisecond):
		}
	}
	rig.open(t, "fresh-shell", time.Now().Unix()+30)
	proof, err := rig.service.Lifecycle(id).CloseLaunch(context.Background(), ownershell.SupervisorIdentity(rig.binding, rig.grant).Launch, "test")
	if err != nil || !proof.ContainmentEmpty {
		t.Fatal("cleanup", err)
	}
}

// #257 review P1: shell liveness is verified with describe, not a new attachment,
// so describe itself must notice a retargeted project alias (direct or through an
// ancestor symlink) while the shell is already running, and stop its input.
func TestLegacyHostShellFencesRetargetedAliasOnDescribe(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		t.Run(fmt.Sprintf("ancestor=%v", ancestor), func(t *testing.T) {
			t.Setenv(DaemonStateDirEnv, t.TempDir())
			host, _, project := newTestHost(t, &scriptedAgentProvider{}, "")
			initChildRepo(t, project)
			link := filepath.Join(t.TempDir(), "project-alias")
			alias, target := link, project
			if ancestor {
				target = filepath.Dir(project)
				alias = filepath.Join(link, filepath.Base(project))
			}
			workspaceLink(t, target, link)
			const id = "alias-host"
			host.spawn(protocol.SpawnSession{SessionID: id, Kind: "agent", ProjectPath: alias, Repo: "proj"}, nil, "", &adoptRequest{})
			defer host.Kill(id)
			rig := newCoordRig(t)
			service := hostLifecycleTestService(t, nil)
			manager := newManagerWithSender(newRecordingSender(16), ManagerConfig{ReposRoot: filepath.Dir(project), Workspaces: service})
			manager.agents = host
			source := manager.workspaceFileBinding(id)
			authority := legacyTestAuthority(id, source)
			authorize := func(ctx context.Context, root protocol.WorkspaceRoot) (protocol.LocalWorkspaceControlResult, error) {
				reply, err := authority(ctx, root)
				reply.ShellEligible = true
				return reply, err
			}
			caps := protocol.WorkspaceCapabilitiesFor("host", "linux")
			if err := manager.attachLegacyWorkspaceWith(t.Context(), id, caps, rig.service, authorize); err != nil {
				t.Fatal(err)
			}
			defer host.explorer.Unbind(id)
			description, err := rig.call(t.Context(), shellruntime.RPC{SessionID: id, Op: "describe"})
			if err != nil {
				t.Fatal("describe", err)
			}
			b, i := description.Binding, description.Identity
			consent := &protocol.HostShellConsent{ID: "consent", OwnerID: b.Owner, LaunchGeneration: b.Launch, MachineID: i.Machine, Account: i.Account, UID: i.UID, Shell: i.Shell, Cwd: i.Cwd, ChallengeID: "challenge"}
			config := shellruntime.RPC{SessionID: id, Op: "configure", Binding: b, Key: rig.key.Public().(ed25519.PublicKey), Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}, Consent: consent}
			config.Binding.Consent, config.Binding.HostConsentValid = consent.ID, true
			configured, err := rig.call(t.Context(), config)
			if err != nil {
				t.Fatalf("configure: %s", configured.ErrorCode)
			}
			rig.binding = configured.Binding
			rig.open(t, "alias-shell", time.Now().Unix()+30)
			rig.input(t, 1, "printf '\\101\\114\\111\\126\\105'\n")
			rig.wait(t, "ALIVE")
			if _, err := rig.call(t.Context(), shellruntime.RPC{SessionID: id, Op: "describe"}); err != nil {
				t.Fatal("unchanged alias refused on describe", err)
			}
			// Retarget while the original project still exists.
			other := t.TempDir()
			if ancestor {
				workspaceFixture(t, filepath.Join(other, filepath.Base(project)), "README.md", "other")
			}
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			workspaceLink(t, other, link)
			if reply, err := rig.call(t.Context(), shellruntime.RPC{SessionID: id, Op: "describe"}); err == nil {
				t.Fatalf("describe still verified a retargeted alias: %+v", reply.Binding)
			}
			if reply, err := rig.call(t.Context(), shellruntime.RPC{SessionID: id, Op: "input", Grant: rig.grant, Seq: 2, Data: []byte("printf STILL_LIVE\n")}); err == nil {
				t.Fatal("retargeted alias still accepted shell input", reply)
			}
			if reply, err := rig.call(t.Context(), shellruntime.RPC{SessionID: id, Op: "renew", Grant: rig.grant}); err == nil {
				t.Fatal("retargeted alias renewed the shell", reply)
			}
			// Restoring the alias does not restore the old authority: the next
			// describe rotates the connection, so the old grant stays dead.
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			workspaceLink(t, target, link)
			restored, err := rig.call(t.Context(), shellruntime.RPC{SessionID: id, Op: "describe"})
			if err != nil || restored.Binding.Connection == rig.binding.Connection {
				t.Fatal("restored alias kept the old connection", err)
			}
			if _, err := rig.call(t.Context(), shellruntime.RPC{SessionID: id, Op: "input", Grant: rig.grant, Seq: 3, Data: []byte("printf STILL_LIVE\n")}); err == nil {
				t.Fatal("old grant regained execution after the alias returned")
			}
			// Cleanup stays available for the server to retire the old handle.
			if closed, err := rig.call(t.Context(), shellruntime.RPC{SessionID: id, Op: "close", Grant: rig.grant}); err != nil || closed.Cleanup == nil {
				t.Fatal("old handle lost its cleanup proof", err)
			}
		})
	}
}
