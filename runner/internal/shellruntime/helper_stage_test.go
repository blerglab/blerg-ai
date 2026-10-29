//go:build linux

package shellruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

func TestHelperBootstrapStages(t *testing.T) {
	for _, stage := range []string{"bootstrap_read", "bootstrap_validate", "resolve_root", "resolve_identity", "verify_identity", "verify_cwd", "verify_root", "verify_grant"} {
		t.Run(stage, func(t *testing.T) {
			c, b, g, key := fixture(t)
			boot := HelperFrame{Op: "bootstrap", ID: 17, Identity: c.command.Identity, Root: b.Root, Grant: g, Expected: g.Grant, Key: key.Public().(ed25519.PublicKey)}
			resolve := func(string) (Identity, error) { return c.command.Identity, nil }
			switch stage {
			case "bootstrap_validate":
				boot.Key = nil
			case "resolve_root":
				boot.PublicRoot = filepath.Join(t.TempDir(), "token=fixture-secret")
			case "resolve_identity":
				resolve = func(string) (Identity, error) { return Identity{}, errors.New("Bearer fixture-secret") }
			case "verify_identity":
				boot.Identity.Account = "fixture-secret"
			case "verify_cwd":
				boot.Expected.Runtime = "sandbox"
				boot.Identity.Cwd = filepath.Dir(boot.Identity.Cwd)
			case "verify_root":
				boot.Root.Inode++
			case "verify_grant":
				boot.Grant.Signature = "fixture-secret"
			}
			raw, err := json.Marshal(boot)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "bootstrap_read" {
				raw = []byte("{token=fixture-secret}")
			}
			var out bytes.Buffer
			err = serveHelper(bytes.NewReader(raw), &out, resolve)
			if err == nil {
				t.Fatal("invalid bootstrap admitted")
			}
			var frame HelperFrame
			if err = json.Unmarshal(out.Bytes(), &frame); err != nil {
				t.Fatalf("no diagnostic frame: %v", err)
			}
			if frame.Op != "start_failed" || frame.StartStage != stage || (stage != "bootstrap_read" && frame.ID != boot.ID) {
				t.Fatalf("wrong diagnostic: op=%s stage=%s", frame.Op, frame.StartStage)
			}
			detail, cause := startDiagnostic(&startError{stage: "bootstrap helper", err: &helperStartError{cause: frame.StartCause, stage: frame.StartStage}})
			if cause != stage || !strings.Contains(detail, stage) {
				t.Fatalf("stage lost: %s / %s", detail, cause)
			}
			if strings.Contains(out.String(), "fixture-secret") || strings.Contains(detail, "fixture-secret") {
				t.Fatal("private data leaked")
			}
		})
	}
}

func TestTracedLaunchStages(t *testing.T) {
	for _, stage := range []string{"open_root", "verify_root", "verify_cwd", "open_cwd", "verify_uid", "exec_shell"} {
		t.Run(stage, func(t *testing.T) {
			c, b, _, _ := fixture(t)
			identity, root := c.command.Identity, c.command.Identity.Cwd
			switch stage {
			case "open_root":
				root = filepath.Join(t.TempDir(), "fixture-secret")
			case "verify_root":
				b.Root.Inode++
			case "verify_cwd":
				identity.Cwd = filepath.Dir(root)
			case "open_cwd":
				identity.Cwd = filepath.Join(root, "fixture-secret")
			case "verify_uid":
				identity.UID = 1 << 32
			case "exec_shell":
				identity.Shell = filepath.Join(t.TempDir(), "fixture-secret")
			}
			p, err := startTraced(identity, b.Root, root)
			if p != nil || err == nil {
				t.Fatal("invalid process launch admitted")
			}
			detail, cause := startDiagnostic(err)
			if cause != stage || !strings.Contains(detail, stage) {
				t.Fatalf("stage lost: %s / %s", detail, cause)
			}
			if strings.Contains(detail, "fixture-secret") {
				t.Fatal("path leaked")
			}
		})
	}
}

func TestCoordinatorAdmissionStages(t *testing.T) {
	for _, stage := range []string{"admit_shell", "verify_binding", "verify_reservation", "verify_policy"} {
		t.Run(stage, func(t *testing.T) {
			c, b, g, key := fixture(t)
			shell := ownershell.SupervisorIdentity(b, g)
			if err := c.AdmitShell(t.Context(), shell); err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "admit_shell":
				c.fenced = true
			case "verify_binding":
				b.Owner = "fixture-secret"
			case "verify_reservation":
				delete(c.reserved, shell.ShellGeneration)
			case "verify_policy":
				g.Signature = "fixture-secret"
			}
			_, err := c.OpenReserved(t.Context(), shell, b, g, key.Public().(ed25519.PublicKey))
			if err == nil {
				t.Fatal("invalid admission succeeded")
			}
			detail, cause := startDiagnostic(err)
			if cause != stage || !strings.Contains(detail, stage) {
				t.Fatalf("stage lost: %s / %s", detail, cause)
			}
			if strings.Contains(detail, "fixture-secret") {
				t.Fatal("private data leaked")
			}
		})
	}
}

func TestHelperStageCannotInjectLogText(t *testing.T) {
	for _, err := range []error{
		&startError{stage: "Bearer fixture-secret\nforged", err: os.ErrPermission},
		&helperStartError{stage: "Bearer fixture-secret\nforged", cause: "Bearer fixture-secret\nforged"},
		stageError("exec_shell", &os.PathError{Op: "exec", Path: "fixture-secret", Err: os.ErrPermission}),
		stageError("helper_ready", context.DeadlineExceeded),
	} {
		detail, cause := startDiagnostic(err)
		if strings.Contains(detail, "fixture-secret") || strings.Contains(detail, "forged") || strings.Contains(cause, "fixture-secret") {
			t.Fatal("untrusted diagnostic escaped allowlist")
		}
	}
}

func TestWorkspaceAdmissionFailureHasSafeStage(t *testing.T) {
	base, b, g, key := fixture(t)
	service := NewService(func(RPC) error { return nil })
	if err := service.RegisterWithAdmission(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }, func(context.Context, func() error) error { return errors.New("Bearer fixture-secret") }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	b, _ = service.Binding(b.Session)
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy}).OK {
		t.Fatal("configure")
	}
	reply := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g})
	if reply.OK || reply.ErrorCode != string(ownershell.ReasonRuntimeStart) || reply.StartCause != "workspace_admission" {
		t.Fatalf("admission cause lost: code=%s cause=%s", reply.ErrorCode, reply.StartCause)
	}
}

func TestDiagnosticHelperProcess(t *testing.T) {
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "diagnostic-helper" {
		var request HelperFrame
		if json.NewDecoder(os.Stdin).Decode(&request) != nil {
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(HelperFrame{Op: "start_failed", StartStage: "bootstrap_read", StartCause: ownershell.StartCauseBootstrap})
		os.Exit(1)
	}
}

func TestBootstrapDecoderStageReachesCallerWithoutRequestID(t *testing.T) {
	c, _, g, key := fixture(t)
	command := c.command
	command.Executable = os.Args[0]
	command.Args = []string{"-test.run=^TestDiagnosticHelperProcess$", "--", "diagnostic-helper"}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	p, err := startPeer(ctx, command, g, key.Public().(ed25519.PublicKey))
	if p != nil {
		<-p.done
	}
	detail, cause := startDiagnostic(err)
	if cause != "bootstrap_read" || !strings.Contains(detail, "bootstrap_read") {
		t.Fatalf("decoder cause lost: %s / %s", detail, cause)
	}
}

func TestPTYAllocationAndConfigurationStages(t *testing.T) {
	for _, stage := range []string{"pty_open", "pty_configure"} {
		t.Run(stage, func(t *testing.T) {
			open := func() (*os.File, *os.File, error) {
				if stage == "pty_open" {
					return nil, nil, &os.PathError{Op: "open", Path: "fixture-secret", Err: os.ErrPermission}
				}
				master, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				slave, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
				if err != nil {
					_ = master.Close()
					t.Fatal(err)
				}
				return master, slave, nil
			}
			p := new(tracedProcess)
			err := p.startPTY(exec.CommandContext(t.Context(), "/bin/sh"), new(syscall.SysProcAttr), open)
			if err == nil {
				t.Fatal("invalid terminal admitted")
			}
			detail, cause := startDiagnostic(err)
			if cause != stage || strings.Contains(detail, "fixture-secret") {
				t.Fatalf("PTY cause lost or leaked: %s / %s", detail, cause)
			}
		})
	}
}

func TestLeaseExpiryDuringStartIsNotErasedBySuccessfulCleanup(t *testing.T) {
	c, b, g, key := fixture(t)
	boot := HelperFrame{Op: "bootstrap", ID: 1, Identity: c.command.Identity, Root: b.Root, Grant: g, Expected: g.Grant, Key: key.Public().(ed25519.PublicKey)}
	raw, err := json.Marshal(boot)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	now := func() time.Time {
		calls++
		if calls == 1 {
			return time.Unix(g.Grant.IssuedAt, 0)
		}
		return time.Unix(g.Grant.ExpiresAt, 0)
	}
	var out bytes.Buffer
	err = serveHelperWithClock(bytes.NewReader(raw), &out, func(string) (Identity, error) { return c.command.Identity, nil }, now)
	if err == nil {
		t.Fatal("successful cleanup erased startup failure")
	}
	var reply HelperFrame
	if json.Unmarshal(out.Bytes(), &reply) != nil || reply.Op != "start_failed" || reply.StartStage != "lease_create" {
		t.Fatal("expired startup lease did not report its stage")
	}
}

// A grant that is authentic but outside the clock window gets its own stage, never
// verify_policy's "authorization changed" (#242: a desktop clock ~1.5 s behind refused
// every shell). Within the tolerance the same grant opens.
func TestCoordinatorClockSkewHasItsOwnStage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		shift int64
		stage string
	}{
		{"desktop 1.5 s behind", 2, ""},
		{"desktop 5 s behind", 5, ""},
		{"desktop 10 s behind", 10, "grant_clock"},
		// Synchronized clocks, a grant that simply expired: not a clock problem.
		{"grant expired six seconds ago", -36, "lease_create"},
		{"grant expired long ago", -400, "lease_create"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, b, g, key := fixture(t)
			// The runner's clock is ahead of this one by shift seconds.
			g.Grant.IssuedAt += tc.shift
			g.Grant.ExpiresAt += tc.shift
			g = ownershell.SignGrant(key, g.Grant)
			shell := ownershell.SupervisorIdentity(b, g)
			if err := c.AdmitShell(t.Context(), shell); err != nil {
				t.Fatal(err)
			}
			if tc.stage == "" {
				// Stop before a helper starts: only the grant check is under test.
				if err := c.verify(g); err != nil {
					t.Fatalf("skew inside the tolerance refused: %v", err)
				}
				return
			}
			_, err := c.OpenReserved(t.Context(), shell, b, g, key.Public().(ed25519.PublicKey))
			_, cause := startDiagnostic(err)
			if err == nil || cause != tc.stage {
				t.Fatalf("got %v / %q, want %q", err, cause, tc.stage)
			}
			if ownershell.StartStageMessage(cause) == ownershell.StartStageMessage("verify_policy") {
				t.Fatal("a time-window refusal reuses the authorization-changed text")
			}
		})
	}
}

func TestHelperBootstrapClockSkewHasItsOwnStage(t *testing.T) {
	c, b, g, key := fixture(t)
	boot := HelperFrame{Op: "bootstrap", ID: 3, Identity: c.command.Identity, Root: b.Root, Grant: g, Expected: g.Grant, Key: key.Public().(ed25519.PublicKey)}
	raw, err := json.Marshal(boot)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	skewed := func() time.Time { return time.Unix(g.Grant.IssuedAt-10, 0) }
	if serveHelperWithClock(bytes.NewReader(raw), &out, func(string) (Identity, error) { return c.command.Identity, nil }, skewed) == nil {
		t.Fatal("grant from 10 s in the future admitted")
	}
	var reply HelperFrame
	if json.Unmarshal(out.Bytes(), &reply) != nil || reply.Op != "start_failed" || reply.StartStage != "grant_clock" {
		t.Fatalf("clock skew lost its stage: %+v", reply)
	}
}

func TestHelperBootstrapExpiredGrantIsNotClockSkew(t *testing.T) {
	c, b, g, key := fixture(t)
	boot := HelperFrame{Op: "bootstrap", ID: 4, Identity: c.command.Identity, Root: b.Root, Grant: g, Expected: g.Grant, Key: key.Public().(ed25519.PublicKey)}
	raw, err := json.Marshal(boot)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	expired := func() time.Time { return time.Unix(g.Grant.ExpiresAt+1, 0) }
	if serveHelperWithClock(bytes.NewReader(raw), &out, func(string) (Identity, error) { return c.command.Identity, nil }, expired) == nil {
		t.Fatal("expired grant admitted")
	}
	var reply HelperFrame
	if json.Unmarshal(out.Bytes(), &reply) != nil || reply.Op != "start_failed" || reply.StartStage != "lease_create" {
		t.Fatalf("expired grant mislabeled: %+v", reply)
	}
}
