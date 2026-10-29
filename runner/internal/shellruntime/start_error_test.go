//go:build linux

package shellruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

func TestServiceLogsRedactedUnderlyingStartError(t *testing.T) {
	for _, reserved := range []bool{false, true} {
		t.Run(map[bool]string{true: "reserved", false: "open"}[reserved], func(t *testing.T) {
			base, b, g, key := fixture(t)
			missing := filepath.Join(t.TempDir(), "token=fixture-secret")
			service := NewService(func(RPC) error { return nil })
			if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = service.Shutdown(ctx)
			})
			service.launches[b.Session].command.Executable = missing
			b, _ = service.Binding(b.Session)
			if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy}).OK {
				t.Fatal("configure")
			}
			g.Grant.ConnectionID = b.Connection
			g.Grant.Audience = b.Audience
			g = ownershell.SignGrant(key, g.Grant)
			if reserved && !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "admit", Launch: ownershell.SupervisorIdentity(b, g).Launch, Shell: g.Grant.ShellGeneration}).OK {
				t.Fatal("reserve")
			}
			var output bytes.Buffer
			old := log.Writer()
			log.SetOutput(&output)
			defer log.SetOutput(old)
			reply := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g})
			if reply.OK || reply.StartCause != "helper_start" {
				t.Fatalf("missing safe cause: code=%s cause=%s", reply.ErrorCode, reply.StartCause)
			}
			line := output.String()
			if !strings.Contains(line, "no such file or directory") || !strings.Contains(line, "helper_start") || !strings.Contains(line, "runtime_start_refused") {
				t.Fatalf("missing underlying error: %s", line)
			}
			if strings.Contains(line, "fixture-secret") || strings.Contains(line, missing) {
				t.Fatal("secret leaked")
			}
		})
	}
}
func TestStartDiagnosticNeverLogsArbitraryErrors(t *testing.T) {
	for _, err := range []error{errors.New("Bearer fixture-secret\nforged"), &os.PathError{Op: "open", Path: "token=fixture-secret", Err: os.ErrPermission}} {
		detail, _ := startDiagnostic(err)
		if strings.Contains(detail, "fixture-secret") || strings.Contains(detail, "forged") {
			t.Fatal("secret leaked")
		}
	}
}
func TestDisposableDaemonHostShell(t *testing.T) {
	binary := os.Getenv("BLERG_CHROME_TEST_DAEMON")
	if binary == "" {
		t.Skip("set BLERG_CHROME_TEST_DAEMON to a disposable daemon binary")
	}
	c, b, g, key := fixture(t)
	command := c.command
	c.command = HostCommand(binary, command.Identity)
	c.command.Root = b.Root
	c.command.Policy = command.Policy
	out, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		detail, _ := startDiagnostic(err)
		t.Fatal(detail)
	}
	if err = c.Input(t.Context(), g, 1, []byte("printf chrome-host-ok\\n\n")); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	for {
		select {
		case data, ok := <-out:
			if !ok {
				t.Fatal("shell closed")
			}
			if bytes.Contains(data, []byte("chrome-host-ok")) {
				return
			}
		case <-timer.C:
			t.Fatal("no shell output")
		}
	}
}

func TestHelperReportsSafeShellStartCause(t *testing.T) {
	c, b, grant, key := fixture(t)
	identity := c.command.Identity
	identity.Shell = filepath.Join(t.TempDir(), "token=fixture-secret")
	boot := HelperFrame{Op: "bootstrap", ID: 1, Root: b.Root, Identity: identity, Expected: grant.Grant, Grant: grant, Key: key.Public().(ed25519.PublicKey)}
	input, err := json.Marshal(boot)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = serveHelper(bytes.NewReader(input), &output, func(string) (Identity, error) { return identity, nil })
	if err == nil {
		t.Fatal("missing shell unexpectedly started")
	}
	var reply HelperFrame
	if json.Unmarshal(output.Bytes(), &reply) != nil || reply.Op != "start_failed" || reply.StartCause != ownershell.StartCauseMissing || reply.StartStage != "exec_shell" {
		t.Fatal("helper discarded the underlying shell start cause")
	}
	if strings.Contains(output.String(), "fixture-secret") {
		t.Fatal("shell path leaked")
	}
	detail, cause := startDiagnostic(&startError{stage: "bootstrap helper", err: &helperStartError{cause: reply.StartCause, stage: reply.StartStage}})
	if cause != "exec_shell" || !strings.Contains(detail, "exec_shell") {
		t.Fatal("helper cause lost before log")
	}
}
