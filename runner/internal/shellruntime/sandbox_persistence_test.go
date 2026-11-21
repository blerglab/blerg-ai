package shellruntime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestPersistentSandboxRefusedByDaemonAndHelper(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	identity := Identity{MemberRoot: protocol.WorkspaceRoot{Device: 1, Inode: 2}}
	custody := protocol.ShellCustody{Version: 2, Owner: "owner", Login: "login", Session: "session", Launch: "launch", Runtime: "sandbox", Container: "container", RuntimeGeneration: 1, Root: identity.MemberRoot, Manifest: "manifest", Desktop: "desktop", Credential: "credential", OwnershipRevision: 1, Runner: "runner", Daemon: "daemon", Shell: "shell", Nonce: "nonce", Identity: RuntimeIdentityDigest(identity), Created: now.Unix(), LifetimeUntil: now.Add(time.Hour).Unix()}
	if err := custody.Validate(); err != nil {
		t.Fatal(err)
	}
	g := protocol.ShellGrant{Version: 2, OwnerID: custody.Owner, LoginID: custody.Login, SessionID: custody.Session, LaunchGeneration: custody.Launch, Runtime: custody.Runtime, ShellGeneration: custody.Shell, CustodyID: custody.Digest(), AccessEpoch: 1, PolicyRevision: 1, IssuedAt: now.Unix(), ExpiresAt: now.Add(30 * time.Second).Unix()}
	caps := protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, Sandbox: true, ShellPersistence: 2, PersistentSandbox: true}
	b := ownershell.Binding{Owner: custody.Owner, Runtime: "sandbox", Live: true, Root: custody.Root, ServerCapabilities: caps, RuntimeCapabilities: caps, ShellLink: protocol.ShellLinkWelcome{Version: 2, Runner: "runner", Daemon: "daemon", Challenge: "challenge", Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: "owner", TokenID: "credential", Revision: 1, ExpiresAt: now.Add(time.Hour)}}}
	command := Command{Container: custody.Container, Custody: &custody, Identity: identity, Root: custody.Root, Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}}
	if _, err := newRetainedCoordinator(new(sync.Mutex), b, command, pub, ownershell.NewCustodyRegistry(), time.Now, func(context.Context, RPC) error { return nil }); err == nil {
		t.Fatal("daemon accepted sandbox v2")
	}
	signed := ownershell.SignGrant(key, g)
	if _, err := startPeer(t.Context(), command, signed, pub); err == nil {
		t.Fatal("daemon started sandbox v2 helper")
	}
	cleanup, err := newCleanupAuthority(custody.Digest(), "helper")
	if err != nil {
		t.Fatal(err)
	}
	clockCalled := false
	started := false
	boot := HelperFrame{Custody: &custody, Expected: g, Root: custody.Root, Key: pub, Grant: signed, HelperID: "helper", CleanupKey: cleanup.bootstrapKey()}
	err = servePersistentHelper(boot, identity, "", func(*HelperFrame) error { return io.EOF }, io.Discard, nil, func() (ownershell.LeaseClock, error) { clockCalled = true; return nil, ErrClosed }, &started)
	if err == nil || clockCalled || started {
		t.Fatal("helper did not refuse sandbox before clock/process allocation")
	}
}

func TestRetainedRPCRefusesNonHostEntryWithClaimedWelcome(t *testing.T) {
	for _, runtime := range []string{"sandbox", "cluster", "unknown"} {
		t.Run(runtime, func(t *testing.T) {
			s := NewService(func(RPC) error { return nil })
			b := ownershell.Binding{Runtime: runtime, Session: "session", Owner: "owner", Live: true, ShellLink: protocol.ShellLinkWelcome{Version: 2}}
			entry := &registered{binding: b, persistentIdentity: &Identity{}}
			result := s.executeRetained(t.Context(), entry, RPC{Version: 2, Op: "describe", SessionID: b.Session})
			if result.OK || result.ErrorCode != string(ownershell.ReasonRuntimeBinding) {
				t.Fatal("non-host entry claimed persistent access", result)
			}
			if entry.binding != b {
				t.Fatal("v1 binding was changed by v2 refusal")
			}
		})
	}
}
