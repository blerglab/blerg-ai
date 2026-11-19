package shellruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/keepertest"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestRound9BadRetirementEntriesAreQuarantined(t *testing.T) {
	for _, bad := range []string{"foreign", "mode", "directory", "root_mode"} {
		t.Run(bad, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "retirements")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			record := workspaceRetirement{1, "valid", strings.Repeat("a", 64)}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, retirementName(record.Workspace)), data, 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "foreign")
			switch bad {
			case "foreign":
				err = os.WriteFile(path, []byte("private canary"), 0600)
			case "mode":
				err = os.WriteFile(path, data, 0644)
			case "directory":
				err = os.Mkdir(path, 0750)
			case "root_mode":
				err = os.Chmod(dir, 0750)
			}
			if err != nil {
				t.Fatal(err)
			}
			s := NewService(func(RPC) error { return nil })
			defer func() { _ = s.Shutdown(context.Background()) }()
			if err = s.ConfigureHostRetirements(dir); err != nil {
				t.Fatal("one bad entry poisoned retirement store", err)
			}
			if s.hostIntents["valid"] != record.ID {
				t.Fatal("valid intent lost")
			}
			if err = s.BeginWorkspaceRetirement("stop"); err != nil {
				t.Fatal("bad entry blocked agent Stop", err)
			}
			if bad != "root_mode" {
				if _, err = os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("bad entry not quarantined")
				}
			}
		})
	}
}

func TestRound9RetirementStoreFailureStillRetiresInMemory(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	defer func() { _ = s.Shutdown(context.Background()) }()
	dir := filepath.Join(t.TempDir(), "retirements")
	if err := s.ConfigureHostRetirements(dir); err != nil {
		t.Fatal(err)
	}
	s.SetHostBridge(RefusedHostBridge())
	e, _ := round6HostEntry()
	s.launches[e.binding.Session] = e
	if err := os.Rename(dir, dir+".offline"); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginWorkspaceRetirement(e.binding.Session); err != nil {
		t.Fatal("store failure blocked Stop", err)
	}
	if !e.terminal || !e.fenced || s.hostIntents[e.binding.Session] == "" {
		t.Fatal("failed persistence lost memory retirement")
	}
	if err := s.RetireWorkspace(t.Context(), ownershell.SupervisorIdentity(e.binding, ownershell.SignedGrant{}).Launch); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.launches[e.binding.Session]; ok {
		t.Fatal("failed store leaked active launch")
	}
	if err := os.Rename(dir+".offline", dir); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginWorkspaceRetirement(e.binding.Session); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, retirementName(e.binding.Session))); err != nil {
		t.Fatal("memory retirement did not persist after repair", err)
	}
}

func TestRound9HeartbeatRequiresSustainedMisses(t *testing.T) {
	first := time.Unix(100, 0)
	m := heartbeatMisses{}
	if m.failed(first) {
		t.Fatal("one heartbeat miss disconnected shells")
	}
	if m.failed(first.Add(time.Second)) {
		t.Fatal("two heartbeat misses disconnected shells")
	}
	if !m.failed(first.Add(2 * time.Second)) {
		t.Fatal("sustained heartbeat silence not detected")
	}
	if m.observe(nil, first.Add(3*time.Second)) || m.observe(ErrKeeperUnavailable, first.Add(4*time.Second)) {
		t.Fatal("successful heartbeat did not reset misses")
	}
	compressed := heartbeatMisses{}
	for range 4 {
		if compressed.failed(first) {
			t.Fatal("miss count without elapsed seconds disconnected shells")
		}
	}
	sparse := heartbeatMisses{}
	if sparse.failed(first) || sparse.failed(first.Add(10*time.Second)) {
		t.Fatal("elapsed time without three misses disconnected shells")
	}
}

func TestRound9UnknownShellStateNeverClaimsAbsence(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	defer func() { _ = s.Shutdown(context.Background()) }()
	e, _ := round6HostEntry()
	s.launches[e.binding.Session] = e
	s.SetHostBridge(RefusedHostBridge())
	if _, known := s.WorkspaceShellsOpen("session"); known {
		t.Fatal("offline workspace shell state reported known")
	}
	if _, known := s.PrivilegedShellOpen("session"); known {
		t.Fatal("offline privileged shell state reported known")
	}
}

func TestRound9HeartbeatCannotOverwriteNewOpen(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	defer func() { _ = s.Shutdown(context.Background()) }()
	e, _ := round6HostEntry()
	s.launches[e.binding.Session] = e
	entered, release := make(chan struct{}), make(chan struct{})
	backend := &reviewBridge{invoke: func(ctx context.Context, method string, _ any, out any) error {
		if method == "heartbeat" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			*out.(*[]hostObservation) = []hostObservation{{Workspace: "session"}}
		}
		if method == "workspace_binding" {
			*out.(*ownershell.Binding) = e.binding
		}
		if method == "call" {
			out.(*RPC).OK = true
		}
		return nil
	}}
	manager := &ReconnectingHostBridge{ctx: t.Context(), ready: make(chan struct{}), current: backend, usable: true, changed: make(chan struct{}), failed: make(chan struct{}, 1)}
	s.SetHostBridge(manager)
	done := make(chan error, 1)
	go func() { done <- manager.heartbeat(backend) }()
	<-entered
	result := s.executeBridged(t.Context(), RPC{SessionID: "session", Op: "open", Grant: ownershell.SignedGrant{Grant: protocol.ShellGrant{Privileged: true}}})
	if !result.OK {
		close(release)
		<-done
		t.Fatal("open acknowledgement refused")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	state := s.hostObserved["session"]
	s.mu.Unlock()
	if !state.Open || !state.Privileged {
		t.Fatal("stale heartbeat erased an acknowledged open")
	}
}

func TestRound9HeartbeatBypassesFullRequestQueueAndSlots(t *testing.T) {
	k, b, _ := reviewRuntime(t)
	if err := b.Invoke(t.Context(), "rebind", nil, nil); err != nil {
		t.Fatal(err)
	}
	k.operation.Lock()
	locked := true
	ctx, cancel := context.WithCancel(t.Context())
	var calls sync.WaitGroup
	defer func() {
		cancel()
		if locked {
			k.operation.Unlock()
		}
		calls.Wait()
	}()
	refused := make(chan struct{}, 96)
	for range 96 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			var binding ownershell.Binding
			if err := b.Invoke(ctx, "workspace_binding", "missing", &binding); err != nil {
				refused <- struct{}{}
			}
		}()
	}
	guard, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	select {
	case <-refused:
	case <-guard.Done():
		t.Fatal("request queue did not saturate")
	}
	var observed []hostObservation
	if err := b.Invoke(guard, "heartbeat", nil, &observed); err != nil {
		t.Fatal("heartbeat queued behind saturated calls", err)
	}
	k.operation.Unlock()
	locked = false
}

func TestRound9RealStalledKeeperPreservesDeadlineCause(t *testing.T) {
	binary := keepertest.Build(t)
	state := t.TempDir()
	if err := keepertest.Run(t, binary, state, "shell-keeper", "init"); err != nil {
		t.Fatal(err)
	}
	p := keepertest.Start(t, binary, state)
	c := round8Connect(t, state, filepath.Join(t.TempDir(), "retirements"))
	c.ready(t)
	p.Pause(t)
	raw := c.bridge.snapshot()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	var observed []hostObservation
	err := raw.Invoke(ctx, "heartbeat", nil, &observed)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrClosed) {
		t.Fatal("real stalled stream hid deadline as ErrClosed", err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	result := c.service.executeBridged(ctx, RPC{Version: 2, SessionID: "unregistered", Op: "inventory"})
	if result.OK || result.StartCause != "keeper_unavailable" || result.ErrorCode != string(ownershell.ReasonRuntimeStart) {
		t.Fatal("real stall reported runtime_fenced", result.ErrorCode, result.StartCause)
	}
}

func TestRound9NewRetirementWakesAndResetsBackoff(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	defer func() { _ = s.Shutdown(context.Background()) }()
	type timer struct {
		duration time.Duration
		tick     chan time.Time
	}
	timers := make(chan timer)
	s.retirementTimer = func(d time.Duration) (<-chan time.Time, func()) {
		tick := make(chan time.Time, 1)
		select {
		case timers <- timer{d, tick}:
		case <-s.hostLife.Done():
		}
		return tick, func() {}
	}
	next := func(want time.Duration) timer {
		t.Helper()
		select {
		case got := <-timers:
			if got.duration != want {
				t.Fatalf("backoff = %v, want %v", got.duration, want)
			}
			return got
		case <-time.After(time.Second):
			t.Fatal("cleanup did not wake")
			return timer{}
		}
	}
	k := &KeeperRuntime{service: s}
	e, _ := round6HostEntry()
	s.launches[e.binding.Session] = e
	k.detachWorkspace(e.binding.Session)
	first := next(5 * time.Second)
	first.tick <- time.Now()
	_ = next(10 * time.Second)
	e2, _ := round6HostEntry()
	e2.binding.Session = "second"
	s.mu.Lock()
	s.launches[e2.binding.Session] = e2
	s.mu.Unlock()
	k.detachWorkspace(e2.binding.Session)
	_ = next(5 * time.Second)
	s.mu.Lock()
	_, pending := s.hostRetiring[e2]
	s.mu.Unlock()
	if pending {
		t.Fatal("new retirement waited behind old backoff")
	}
}

func TestRound9RetirementFailuresLogWithoutPrivateContents(t *testing.T) {
	if os.Getenv("BLERG_ROUND9_LOG_ORACLE") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRound9RetirementFailuresLogWithoutPrivateContents$")
		cmd.Env = append(os.Environ(), "BLERG_ROUND9_LOG_ORACLE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("logging oracle: %v %s", err, out)
		}
		return
	}
	var logs bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer func() { failurelog.Default = previous }()
	dir := filepath.Join(t.TempDir(), "retirements")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	const secret = "ROUND9_PRIVATE_CONTENT_CANARY"
	if err := os.WriteFile(filepath.Join(dir, secret), []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewService(func(RPC) error { return nil })
	if err := s.ConfigureHostRetirements(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginWorkspaceRetirement(secret); err != nil {
		t.Fatal("agent stop depended on store", err)
	}
	_ = s.Shutdown(context.Background())
	for _, stage := range []string{"retirement_quarantined", "retirement_write_memory_only"} {
		if !strings.Contains(logs.String(), stage) {
			t.Fatal("missing safe failure cause", stage)
		}
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), dir) {
		t.Fatal("private retirement contents logged")
	}
}

func TestRound9ManifestReplayCannotRetireUnknownOrLiveCustody(t *testing.T) {
	for _, state := range []string{"live", "unknown", "empty"} {
		t.Run(state, func(t *testing.T) {
			s := NewService(func(RPC) error { return nil })
			defer func() { _ = s.Shutdown(context.Background()) }()
			_, r := round6HostEntry()
			remote := r.Binding
			remote.ManifestDigest = "old-manifest"
			retired, registered := false, false
			s.SetHostBridge(&reviewBridge{invoke: func(_ context.Context, method string, _ any, out any) error {
				switch method {
				case "workspace_binding":
					*out.(*ownershell.Binding) = remote
				case "workspace_shells_open":
					if state == "unknown" {
						return ErrKeeperUnavailable
					}
					*out.(*bool) = state == "live"
				case "retire_workspace":
					retired = true
				case "register":
					registered = true
				}
				return nil
			}})
			err := s.reconcileWorkspaceRegistration(t.Context(), r, false)
			if state == "empty" {
				if err != nil || !retired || !registered {
					t.Fatal("empty custody not reconciled", err)
				}
			} else if err == nil || retired || registered {
				t.Fatal("manifest replay discarded possible live custody", state, err)
			}
		})
	}
}

func TestRound9FailedRetirementLoadDoesNotFenceOtherShells(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	defer func() { _ = s.Shutdown(context.Background()) }()
	dir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dir, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureHostRetirements(dir); err == nil {
		t.Fatal("invalid journal accepted")
	}
	e, _ := round6HostEntry()
	s.launches[e.binding.Session] = e
	s.SetHostBridge(&reviewBridge{invoke: func(_ context.Context, method string, _ any, out any) error {
		if method == "workspace_binding" {
			*out.(*ownershell.Binding) = e.binding
		}
		if method == "call" {
			out.(*RPC).OK = true
		}
		return nil
	}})
	if !s.hostBridgeReady() {
		t.Fatal("load error globally fenced healthy keeper")
	}
	if result := s.executeBridged(t.Context(), RPC{SessionID: e.binding.Session, Op: "open"}); !result.OK {
		t.Fatal("unrelated shell refused after journal failure", result.ErrorCode)
	}
	if err := s.BeginWorkspaceRetirement("other-workspace"); err != nil {
		t.Fatal("invalid store blocked Stop", err)
	}
}

func TestRound9OneHeartbeatMissKeepsUsableLink(t *testing.T) {
	backend := &reviewBridge{invoke: func(context.Context, string, any, any) error { return context.DeadlineExceeded }}
	b := &ReconnectingHostBridge{ctx: t.Context(), current: backend, usable: true, changed: make(chan struct{}), failed: make(chan struct{}, 1)}
	if err := b.heartbeat(backend); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("missing bounded heartbeat failure", err)
	}
	if !b.available() {
		t.Fatal("one heartbeat miss disconnected healthy link")
	}
	select {
	case <-b.failed:
		t.Fatal("one heartbeat miss requested recovery")
	default:
	}
}

func TestRound9UnobservedWorkspaceIsUnknownOnHealthyLink(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	defer func() { _ = s.Shutdown(context.Background()) }()
	e, _ := round6HostEntry()
	s.launches[e.binding.Session] = e
	s.SetHostBridge(&reviewBridge{invoke: func(context.Context, string, any, any) error { return nil }})
	if !s.hostBridgeReady() {
		t.Fatal("fixture link is not healthy")
	}
	if _, known := s.WorkspaceShellsOpen(e.binding.Session); known {
		t.Fatal("missing heartbeat observation claimed empty custody")
	}
	if _, known := s.PrivilegedShellOpen(e.binding.Session); known {
		t.Fatal("missing privileged observation claimed empty custody")
	}
}
