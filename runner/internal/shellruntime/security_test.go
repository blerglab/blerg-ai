//go:build linux

package shellruntime

import (
	"context"
	"crypto/ed25519"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type authority struct {
	mu      sync.Mutex
	policy  protocol.ShellPolicy
	revoked bool
}

func (a *authority) Authorize(context.Context, ownershell.Actor) (protocol.ShellPolicy, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revoked {
		return a.policy, ErrClosed
	}
	return a.policy, nil
}

type bindingResolver struct{ b ownershell.Binding }

func (r bindingResolver) Resolve(context.Context, string) (ownershell.Binding, error) {
	return r.b, nil
}

type realRuntime struct {
	mu      sync.Mutex
	command Command
	binding ownershell.Binding
	c       *Coordinator
	offline bool
}

func (r *realRuntime) Open(ctx context.Context, b ownershell.Binding, g ownershell.SignedGrant, key ed25519.PublicKey) (<-chan []byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.c == nil {
		command := r.command
		first := true
		command.Recovery = func(context.Context, protocol.SupervisorLaunch) error {
			if first {
				first = false
				return nil
			}
			return ErrClosed
		}
		command.Policy = protocol.ShellPolicy{Version: 1, Revision: g.Grant.PolicyRevision, Enabled: true}
		c, err := NewCoordinator(new(sync.Mutex), b, command, key)
		if err != nil {
			return nil, err
		}
		if _, err = c.RecoverLaunch(ctx, c.launch()); err != nil {
			return nil, err
		}
		r.c = c
	}
	return r.c.Open(ctx, b, g, key)
}
func (r *realRuntime) Renew(ctx context.Context, g ownershell.SignedGrant) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.offline {
		return ErrClosed
	}
	return r.c.Renew(ctx, g)
}
func (r *realRuntime) Input(ctx context.Context, g ownershell.SignedGrant, seq uint64, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.offline {
		return ErrClosed
	}
	return r.c.Input(ctx, g, seq, data)
}
func (r *realRuntime) Resize(ctx context.Context, g ownershell.SignedGrant, seq uint64, x, y uint16) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.offline {
		return ErrClosed
	}
	return r.c.Resize(ctx, g, seq, x, y)
}
func (r *realRuntime) Close(ctx context.Context, g ownershell.SignedGrant) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.offline {
		return ErrClosed
	}
	return r.c.Close(ctx, g)
}
func TestRealHostConnectedRevocationMatrix(t *testing.T) {
	for _, reason := range []string{"logout", "account", "disable"} {
		t.Run(reason, func(t *testing.T) {
			base, b, _, _ := fixture(t)
			b.ServerCapabilities = protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1}
			b.RuntimeCapabilities = b.ServerCapabilities
			runtime := &realRuntime{command: base.command, binding: b}
			t.Cleanup(func() {
				if runtime.c != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
					defer cancel()
					_, _ = runtime.c.CloseLaunch(ctx, runtime.c.launch(), "test_cleanup")
				}
			})
			a := &authority{policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}}
			manager, err := ownershell.New(a, bindingResolver{b}, runtime, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			manager.SetPolicy(t.Context(), a.policy)
			actor := ownershell.Actor{Kind: "human", Owner: b.Owner, Login: "login"}
			var infos []ownershell.Info
			for range 4 {
				info, err := manager.Open(t.Context(), actor, b.Session)
				if err != nil {
					t.Fatal(err)
				}
				infos = append(infos, info)
			}
			if _, err = manager.Open(t.Context(), actor, b.Session); err == nil {
				t.Fatal("fifth supervisor escaped session budget")
			}
			a.mu.Lock()
			if reason == "disable" {
				a.policy = protocol.ShellPolicy{Version: 1, Revision: 2, Enabled: false}
			} else {
				a.revoked = true
			}
			policy := a.policy
			a.mu.Unlock()
			switch reason {
			case "disable":
				manager.SetPolicy(t.Context(), policy)
			case "account":
				manager.Revoke(t.Context(), actor.Owner, "", "")
			default:
				manager.Revoke(t.Context(), "", actor.Login, "")
			}
			runtime.c.sessionMu.Lock()
			remaining := len(runtime.c.peers)
			runtime.c.sessionMu.Unlock()
			if remaining != 0 {
				t.Fatal("real supervisors survived revocation")
			}
			for _, info := range infos {
				if _, err := manager.Attach(t.Context(), actor, info.Handle, true); err == nil {
					t.Fatal("revoked handle resurrected")
				}
			}
			proof, err := runtime.c.CloseLaunch(t.Context(), runtime.c.launch(), "verify_no_writers")
			if err != nil || proof.ValidateFor(runtime.c.launch(), "") != nil {
				t.Fatal("all-shell cleanup proof missing")
			}
		})
	}
}
func TestRealHostFullThirtySecondOfflineGrant(t *testing.T) {
	base, b, _, _ := fixture(t)
	b.ServerCapabilities = protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1}
	b.RuntimeCapabilities = b.ServerCapabilities
	runtime := &realRuntime{command: base.command, binding: b}
	t.Cleanup(func() {
		if runtime.c != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			_, _ = runtime.c.CloseLaunch(ctx, runtime.c.launch(), "test_cleanup")
		}
	})
	a := &authority{policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}}
	manager, err := ownershell.New(a, bindingResolver{b}, runtime, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetPolicy(t.Context(), a.policy)
	actor := ownershell.Actor{Kind: "human", Owner: b.Owner, Login: "login"}
	info, err := manager.Open(t.Context(), actor, b.Session)
	if err != nil {
		t.Fatal(err)
	}
	runtime.c.sessionMu.Lock()
	peer := runtime.c.peers[info.Shell]
	runtime.c.sessionMu.Unlock()
	pidfile := filepath.Join(runtime.command.Identity.Cwd, "offline-root.pid")
	if err = runtime.c.Input(t.Context(), runtime.c.grants[info.Shell], 1, []byte("echo $$ > "+pidfile+"\n")); err != nil {
		t.Fatal(err)
	}
	var pid int
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		data, readErr := os.ReadFile(pidfile)
		if readErr == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("runtime did not record its owned shell PID")
	}
	runtime.mu.Lock()
	runtime.offline = true
	runtime.mu.Unlock()
	// Core disconnect cannot contact the runtime. Its local timer must still kill.
	manager.Unavailable(t.Context())
	deadline := time.Now().Add(31 * time.Second)
	for unix.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if unix.Kill(pid, 0) == nil {
		t.Fatal("real disconnected shell exceeded 30-second grant plus scheduler allowance")
	}
	// Go's race detector sleeps for one second at helper process exit, after
	// physical cleanup. Do not count that test-only delay as shell lifetime.
	select {
	case <-peer.done:
	case <-time.After(2 * time.Second):
		t.Fatal("reaped runtime helper did not exit")
	}
	proof, err := runtime.c.RecoverLaunch(t.Context(), runtime.c.launch())
	if err != nil || proof.ValidateFor(runtime.c.launch(), "") != nil {
		t.Fatal("offline supervisor cleanup unproven")
	}
}
func TestHostIdentityAndRootReplacementRefused(t *testing.T) {
	c, b, g, key := fixture(t)
	changed := c.command
	changed.Identity.UID++
	if _, err := NewCoordinator(new(sync.Mutex), b, changed, key.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("actual account mismatch accepted")
	}
	b.Root.Inode++
	if _, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("prepared root replacement accepted")
	}
}
