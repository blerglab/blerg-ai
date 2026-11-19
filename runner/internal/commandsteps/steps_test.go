package commandsteps

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefinitionRequiresExactApproval(t *testing.T) {
	steps := []Step{{Argv: []string{"echo", "hello"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	if _, err := Approve(steps, "wrong", "person", time.Now()); err == nil {
		t.Fatal("accepted stale approval")
	}
	def, err := Approve(steps, Hash(steps), "person", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if def.Validate() != nil {
		t.Fatal("valid definition refused")
	}
	def.Steps[0].Argv[1] = "changed"
	if def.Validate() == nil {
		t.Fatal("changed command kept approval")
	}
}
func TestRunBoundsOutputAndReportsExit(t *testing.T) {
	requireLinux(t)
	steps := []Step{{Argv: []string{"/bin/sh", "-c", "printf 'untrusted instruction'; exit 7"}, TimeoutSeconds: 1, MaxOutputBytes: 8}}
	def, _ := Approve(steps, Hash(steps), "person", time.Now())
	results, err := Run(context.Background(), def, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ExitCode != 7 || !results[0].Truncated || results[0].Output != "untruste" {
		t.Fatalf("%+v", results)
	}
	if !strings.Contains(Prompt(results), "untrusted data") {
		t.Fatal("missing data boundary")
	}
}
func TestTimeoutDoesNotWaitForChildPipe(t *testing.T) {
	requireLinux(t)
	steps := []Step{{Argv: []string{"/bin/sh", "-c", "sleep 20 & wait"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	def, _ := Approve(steps, Hash(steps), "person", time.Now())
	start := time.Now()
	results, err := Run(context.Background(), def, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 4*time.Second || !results[0].TimedOut {
		t.Fatalf("timeout: %+v after %v", results, time.Since(start))
	}
}
func TestValidationLimits(t *testing.T) {
	for _, s := range []Step{{}, {Argv: []string{"echo"}, TimeoutSeconds: 0, MaxOutputBytes: 32}, {Argv: []string{"echo"}, TimeoutSeconds: 301, MaxOutputBytes: 32}, {Argv: []string{"echo\x00"}, TimeoutSeconds: 1, MaxOutputBytes: 32}, {Argv: []string{"echo"}, TimeoutSeconds: 1, MaxOutputBytes: 1048577}} {
		if _, err := Approve([]Step{s}, Hash([]Step{s}), "person", time.Now()); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}

func TestSupervisorRefusalCannotSkipFixedChecks(t *testing.T) {
	requireLinux(t)
	steps := []Step{{Argv: []string{"echo", "health"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	definition, _ := Approve(steps, Hash(steps), "person", time.Now())
	_, err := RunWithStart(context.Background(), definition, "", nil, nil, func(*exec.Cmd) error { return errors.New("supervisor refused start") })
	if err == nil {
		t.Fatal("continued after the supervisor refused the fixed command")
	}
}

func TestDefinitionFitsBoundedLaunchEnvironment(t *testing.T) {
	steps := make([]Step, 8)
	for i := range steps {
		steps[i] = Step{Argv: []string{"echo", strings.Repeat("x", 16000)}, TimeoutSeconds: 1, MaxOutputBytes: 32}
	}
	if _, err := Approve(steps, Hash(steps), "person", time.Now()); err == nil {
		t.Fatal("accepted definition larger than safe launch environment")
	}
}

func TestOwnTimeoutExitCodesAreNotTimeouts(t *testing.T) {
	requireLinux(t)
	for _, code := range []string{"124", "137"} {
		steps := []Step{{Argv: []string{"/bin/sh", "-c", "exit " + code}, TimeoutSeconds: 2, MaxOutputBytes: 32}}
		d, _ := Approve(steps, Hash(steps), "person", time.Now())
		results, err := Run(context.Background(), d, "", nil, nil)
		if err != nil || len(results) != 1 || results[0].TimedOut {
			t.Fatalf("own exit %s: %+v %v", code, results, err)
		}
	}
}
func TestPromptTotalBound(t *testing.T) {
	results := make([]Result, 8)
	for i := range results {
		results[i].Output = strings.Repeat("x", 65536)
	}
	if len(Prompt(results)) > 65536 {
		t.Fatal("prompt exceeded total output budget")
	}
}
func TestRuntimeTokensDoNotReachCommandsOrOutput(t *testing.T) {
	requireLinux(t)
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "fixture-runtime-secret")
	steps := []Step{{Argv: []string{"/bin/sh", "-c", "env; printf fixture-runtime-secret"}, TimeoutSeconds: 2, MaxOutputBytes: 4096}}
	d, _ := Approve(steps, Hash(steps), "person", time.Now())
	results, err := Run(context.Background(), d, "", []string{"PATH=/usr/bin:/bin", "BLERG_RUNNER_SESSION_TOKEN=fixture-runtime-secret"}, nil)
	if err != nil || len(results) != 1 {
		t.Fatalf("%+v %v", results, err)
	}
	if strings.Contains(results[0].Output, "fixture-runtime-secret") || strings.Contains(results[0].Output, "BLERG_RUNNER_SESSION_TOKEN=") {
		t.Fatal("runtime credential reached output")
	}
}

func TestCompletedResultsSurviveLaterAdmissionFailure(t *testing.T) {
	requireLinux(t)
	steps := []Step{{Argv: []string{"echo", "health"}, TimeoutSeconds: 1, MaxOutputBytes: 32}, {Argv: []string{"echo", "later"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	d, _ := Approve(steps, Hash(steps), "person", time.Now())
	calls := 0
	results, err := RunWithStart(context.Background(), d, "", nil, nil, func(cmd *exec.Cmd) error {
		calls++
		if calls == 2 {
			return errors.New("refused")
		}
		return cmd.Start()
	})
	if err == nil || len(results) != 1 || results[0].Output != "health\n" {
		t.Fatalf("lost completed result: %+v %v", results, err)
	}
}
func TestPlacementIsPartOfApproval(t *testing.T) {
	steps := []Step{{Argv: []string{"echo", "health"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	d, err := ApproveForRuntime(steps, "docker", "fixture-daemon", "person", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d.DaemonID = "other-daemon"
	if d.Validate() == nil {
		t.Fatal("daemon changed without approval")
	}
	d.DaemonID = "fixture-daemon"
	d.Runtime = "cluster"
	if d.Validate() == nil {
		t.Fatal("runtime changed without approval")
	}
}

func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("fixed command execution requires Linux")
	}
}

func TestEnvironmentNamesCannotReplaceApprovedCommand(t *testing.T) {
	requireLinux(t)
	steps := []Step{{Argv: []string{"/bin/echo", "approved"}, TimeoutSeconds: 1, MaxOutputBytes: 128}}
	d, _ := Approve(steps, Hash(steps), "person", time.Now())
	results, err := Run(context.Background(), d, "", []string{"--split-string=/bin/echo not-approved"}, nil)
	if err != nil || len(results) != 1 || results[0].Output != "approved\n" {
		t.Fatalf("environment replaced command: %+v %v", results, err)
	}
}

func TestHostEnvironmentNeverCrossesBoundary(t *testing.T) {
	steps := []Step{{Argv: []string{"/usr/bin/env"}, TimeoutSeconds: 2, MaxOutputBytes: 4096}}
	d, _ := Approve(steps, Hash(steps), "person", time.Now())
	env := []string{"PATH=/usr/bin:/bin", "PGPASSWORD=fixture-password", "DATABASE_URL=fixture-database", "GH_PAT=fixture-pat", "CUSTOM_AUTH=fixture-extra"}
	results, err := RunWithStart(context.Background(), d, "", env, nil, func(cmd *exec.Cmd) error {
		for _, arg := range cmd.Args {
			if strings.Contains(arg, "fixture-") {
				t.Error("host environment reached argv")
			}
		}
		return cmd.Start()
	})
	if err != nil || len(results) != 1 {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(results[0].Output, "fixture-") {
		t.Fatal("host environment reached command")
	}
}

func TestScrubBeforeLimitWithOverlappingSecrets(t *testing.T) {
	for _, output := range []string{"xxxxabcdefghijk", strings.Repeat("abcdefghijk", 30) + "abcdefghij"} {
		steps := []Step{{Argv: []string{"/usr/bin/printf", output}, TimeoutSeconds: 2, MaxOutputBytes: 12}}
		d, _ := Approve(steps, Hash(steps), "person", time.Now())
		results, err := RunWithStart(context.Background(), d, "", nil, nil, nil, "abc", "abcdefghijk", "abcdefghij")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(results[0].Output, "def") || strings.Contains(results[0].Output, "abc") {
			t.Fatalf("partial secret: %q", results[0].Output)
		}
	}
}

func TestTimeoutStartsAfterAdmission(t *testing.T) {
	steps := []Step{{Argv: []string{"/bin/true"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	d, _ := Approve(steps, Hash(steps), "person", time.Now())
	results, err := RunWithStart(context.Background(), d, "", nil, nil, func(cmd *exec.Cmd) error { time.Sleep(1100 * time.Millisecond); return cmd.Start() })
	if err != nil || results[0].TimedOut {
		t.Fatalf("admission counted as execution: %+v %v", results, err)
	}
}

func TestRuntimeTargetIsPartOfApproval(t *testing.T) {
	steps := []Step{{Argv: []string{"echo"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	d, err := ApproveForRuntime(steps, "cluster", "", "person", time.Now(), "first")
	if err != nil {
		t.Fatal(err)
	}
	d.RuntimeTargetID = "second"
	if d.Validate() == nil {
		t.Fatal("runtime target changed without approval")
	}
}

func TestScrubLongestSecretFirst(t *testing.T) {
	if got := scrubOutput("abcdefghijk", []string{"abc", "abcdefghijk"}); got != "[redacted]" {
		t.Fatalf("overlap exposed suffix: %q", got)
	}
}

func TestRedactionAcrossWritesBeforeTruncation(t *testing.T) {
	b := &boundedOutput{limit: 12, secrets: []string{"abcdefghijk"}, lookahead: 11}
	for _, chunk := range []string{"xxxxabc", "defgh", "ijk-tail"} {
		_, _ = b.Write([]byte(chunk))
	}
	b.drain(true)
	if string(b.bytes) != "xxxx[redacte" || !b.truncated {
		t.Fatalf("redaction: %q truncated=%v", b.bytes, b.truncated)
	}
}

func TestLegacyApprovalCannotAcquireTarget(t *testing.T) {
	steps := []Step{{Argv: []string{"echo"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	d, _ := Approve(steps, Hash(steps), "person", time.Now())
	d.RuntimeTargetID = "cluster"
	if d.Validate() == nil {
		t.Fatal("legacy approval acquired an unapproved target")
	}
}

func TestRepeatedLongSecretsCannotExposeCaptureTail(t *testing.T) {
	secret := strings.Repeat("private-value", 20)
	steps := []Step{{Argv: []string{"/usr/bin/printf", secret + secret}, TimeoutSeconds: 2, MaxOutputBytes: 32}}
	d, _ := Approve(steps, Hash(steps), "person", time.Now())
	results, err := RunWithStart(context.Background(), d, "", nil, nil, nil, secret)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Output != "[redacted][redacted]" {
		t.Fatalf("partial secret at capture limit: %q", results[0].Output)
	}
}

func TestRuntimeTargetWireMutationInvalidatesApproval(t *testing.T) {
	steps := []Step{{Argv: []string{"echo"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	d, _ := ApproveForRuntime(steps, "cluster", "", "person", time.Now())
	raw, _ := json.Marshal(d)
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	wire["runtime_target_id"] = "unapproved-target"
	raw, _ = json.Marshal(wire)
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Validate() == nil {
		t.Fatal("wire target changed without approval")
	}
}
