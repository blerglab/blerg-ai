package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

func TestRound10NeverOpenedMembershipWaitIsBounded(t *testing.T) {
	for _, fixture := range []struct {
		name              string
		never, unobserved bool
	}{
		{"never_opened", true, false},
		{"old_or_possible_custody", false, false},
		{"registration_absent_never_opened", true, true},
		{"registration_absent_possible_custody", false, true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			never := fixture.never
			f := membershipSetup(t)
			runtime := shellruntime.NewService(func(shellruntime.RPC) error { return nil })
			defer func() { _ = runtime.Shutdown(context.Background()) }()
			runtime.SetHostBridge(shellruntime.RefusedHostBridge())
			if fixture.unobserved {
				runtime.SetHostBridge(round10UnobservedBridge{})
			}
			registry := newWorkspaceSupervisors(runtime)
			f.service.Supervisor = registry
			f.service.membershipShells = nil
			f.w.state.HostWorkDir = f.w.state.Root
			f.w.state.HostKeeperNeverOpened = never
			f.w.authorize = func(context.Context) error { return nil }
			if err := f.w.save(); err != nil {
				t.Fatal(err)
			}
			if err := registry.registerHost(t.Context(), f.w.host); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(100, 0)
			f.service.membershipClock = func() time.Time { return now }
			f.service.ApplyMembership(t.Context(), f.msg)
			if f.server.installed != 0 || len(f.server.reports) != 1 || f.server.reports[0].Waiting != protocol.MembershipWaitKeeper {
				t.Fatal("keeper wait claimed an open shell", f.server.reports)
			}
			now = now.Add(6 * time.Second)
			f.service.ApplyMembership(t.Context(), f.msg)
			if never {
				if f.server.installed != 1 {
					t.Fatal("never-opened workspace stuck behind missing keeper", f.server.reports)
				}
			} else if f.server.commits != 0 {
				t.Fatal("unknown old custody bypassed fencing")
			}
		})
	}
}

// An answering transport with a refused registration has no occupancy entry.
// Missing observations must use the same durable history rule as an outage.
type round10UnobservedBridge struct{}

func (round10UnobservedBridge) Invoke(context.Context, string, any, any) error {
	return shellruntime.ErrClosed
}
func (round10UnobservedBridge) Close() error                                              { return nil }
func (round10UnobservedBridge) HostSession(string) bool                                   { return false }
func (round10UnobservedBridge) Metadata(string, any, any) error                           { return shellruntime.ErrClosed }
func (round10UnobservedBridge) SetReceiver(func(context.Context, shellruntime.RPC) error) {}

func TestRound10HostOpenSharesMembershipAdmission(t *testing.T) {
	f := membershipSetup(t)
	lock := workspaceAdmissionLock(f.w.state.Session)
	lock.Lock()
	forwarded := false
	err := f.w.host.admitHostShell(t.Context(), func() error { forwarded = true; return nil })
	lock.Unlock()
	if err == nil || forwarded {
		t.Fatal("host open bypassed membership admission", err, forwarded)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = f.w.host.admitHostShell(ctx, func() error { forwarded = true; return nil })
	if err == nil || forwarded {
		t.Fatal("cancelled open forwarded", err, forwarded)
	}
}

func TestRound10HostOpenHistoryPrecedesForwardAndSurvivesRestart(t *testing.T) {
	f := membershipSetup(t)
	f.w.state.HostKeeperNeverOpened = true
	if err := f.w.save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := readPrivateWorkspace(f.w.record)
	if err != nil || !loaded.HostKeeperNeverOpened {
		t.Fatal("fresh proof missing", err)
	}
	if err = f.w.host.beforeHostShell(); err != nil {
		t.Fatal(err)
	}
	loaded, err = readPrivateWorkspace(f.w.record)
	if err != nil || loaded.HostKeeperNeverOpened {
		t.Fatal("restart could forget a possible open", err)
	}
	// Reading an old signed manifest has no affirmative negative proof.
	if (workspacePrivateManifest{}).HostKeeperNeverOpened {
		t.Fatal("old records defaulted to empty")
	}
}

func TestRound10HostOpenHistoryWriteFailureRefusesForward(t *testing.T) {
	f := membershipSetup(t)
	f.w.state.HostKeeperNeverOpened = true
	if err := os.Rename(f.w.record, f.w.record+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.w.record, 0700); err != nil {
		t.Fatal(err)
	}
	if err := f.w.host.beforeHostShell(); err == nil {
		t.Fatal("open allowed without durable history")
	}
	if !f.w.state.HostKeeperNeverOpened {
		t.Fatal("refused unsent open lost negative proof")
	}
}

func TestRound10FreshHostIntentIsTheOnlyNeverOpenedProof(t *testing.T) {
	f := membershipSetup(t)
	service := shellruntime.NewService(func(shellruntime.RPC) error { return nil })
	defer func() { _ = service.Shutdown(context.Background()) }()
	f.service.Supervisor = newWorkspaceSupervisors(service)
	dir, err := os.Open(f.w.state.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	common, err := os.Open(filepath.Join(f.w.state.Root, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = common.Close() }()
	m := f.old
	pins := &discovery.BoundPins{Manifest: m, Anchor: dir, AnchorPath: f.w.state.Root, Sources: []discovery.SourcePin{{Member: m.Members[0], Directory: dir, CommonDirectory: common, CanonicalPath: f.w.state.Root, CommonDirectoryPath: filepath.Join(f.w.state.Root, ".git")}}}
	msg := protocol.SpawnSession{SessionID: "fresh-host", Workspace: &m}
	w, err := f.service.beginHostIntent(msg, pins, "fresh-token", false)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := readPrivateWorkspace(w.record)
	if err != nil || !saved.HostKeeperNeverOpened {
		t.Fatal("fresh never-opened proof not durable", err)
	}
	if _, err = f.service.beginHostIntent(msg, pins, "replacement", false); err == nil {
		t.Fatal("existing journal can regain never-opened proof")
	}
}
