package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// stubHelp makes the --help probe answer out (or fail with err) and counts the runs; the host's
// cached answer is forgotten before and after the test.
func stubHelp(t *testing.T, out string, err error) *int {
	t.Helper()
	runs := 0
	old := ccHelpText
	ccHelpText = func(context.Context, sandboxExec, string, []string) ([]byte, error) {
		runs++
		return []byte(out), err
	}
	forget := func() {
		ccHostFlags.mu.Lock()
		ccHostFlags.flags = nil
		ccHostFlags.mu.Unlock()
	}
	forget()
	t.Cleanup(func() {
		ccHelpText = old
		forget()
	})
	return &runs
}

const helpWithFlag = "Options:\n  --forward-subagent-text  Forward subagent text and thinking blocks\n  --from-pr [value]\n"

func TestForwardSubagentFlagOnlyWithTheOption(t *testing.T) {
	for name, args := range map[string][]string{
		"turn":    ccTurnArgs("do it", "", "", ""),
		"session": ccSessionArgs("", "", ""),
	} {
		if slices.Contains(args, ccForwardSubagentFlag) {
			t.Errorf("%s args carry %s without the option: %v", name, ccForwardSubagentFlag, args)
		}
	}
	for name, args := range map[string][]string{
		"turn":    ccTurnArgs("do it", "", "", "", withForwardSubagentText()),
		"session": ccSessionArgs("", "", "", withForwardSubagentText()),
	} {
		if !slices.Contains(args, ccForwardSubagentFlag) {
			t.Errorf("%s args lack %s with the option: %v", name, ccForwardSubagentFlag, args)
		}
	}
}

// fakeHostClaude puts an executable `claude` first on PATH (the probe keys its cache on it) and
// returns its path.
func fakeHostClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return path
}

func TestForwardSubagentProbeReadsHelpOncePerHost(t *testing.T) {
	fakeHostClaude(t)
	runs := stubHelp(t, helpWithFlag, nil)
	a := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	b := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	if !a.cliFlags(context.Background()).FwdSub || !b.cliFlags(context.Background()).FwdSub {
		t.Fatal("a CLI whose --help lists the flag must get it")
	}
	if *runs != 1 {
		t.Fatalf("--help runs = %d, want 1 for every host session", *runs)
	}
}

func TestForwardSubagentProbeSaysNoForAnOlderCLI(t *testing.T) {
	for name, c := range map[string]struct {
		out string
		err error
	}{
		"not listed": {out: "Options:\n  --verbose\n"},
		"help fails": {out: helpWithFlag, err: errors.New("exit status 1")},
	} {
		t.Run(name, func(t *testing.T) {
			stubHelp(t, c.out, c.err)
			d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
			if d.cliFlags(context.Background()).FwdSub {
				t.Fatal("the flag must not be passed")
			}
		})
	}
}

func TestForwardSubagentProbeIsPerSessionInASandbox(t *testing.T) {
	runs := stubHelp(t, helpWithFlag, nil)
	for i := 0; i < 2; i++ {
		d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
		d.useSandbox(sandboxExecPrefix("blerg-sess-x"))
		first, second := d.cliFlags(context.Background()), d.cliFlags(context.Background())
		if !first.FwdSub || !second.FwdSub {
			t.Fatal("want the flag")
		}
	}
	if *runs != 2 {
		t.Fatalf("--help runs = %d, want one per sandboxed session", *runs)
	}
}

// The process the driver starts carries the flag when the CLI has it, and not otherwise.
func TestStreamingProcessForwardsSubagentTextWhenSupported(t *testing.T) {
	for _, supported := range []bool{true, false} {
		help := "Options:\n  --verbose\n"
		if supported {
			help = helpWithFlag
		}
		stubHelp(t, help, nil)
		d, em, logPath := newSteeringDriver(t)
		d.Enqueue("one", "chat")
		waitIdle(t, em, 1)
		args := starts(logLines(t, logPath))[0]
		if got := strings.Contains(args, ccForwardSubagentFlag); got != supported {
			t.Fatalf("supported=%v: flag passed=%v in %s", supported, got, args)
		}
	}
}

// A --help that failed is not remembered: the next turn asks again, and a good answer then sticks.
func TestForwardSubagentProbeAsksAgainAfterAFailedHelp(t *testing.T) {
	fakeHostClaude(t)
	runs := stubHelp(t, "", errors.New("signal: killed"))
	d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	if d.cliFlags(context.Background()).FwdSub {
		t.Fatal("a failed --help must not pass the flag")
	}
	ccHelpText = func(context.Context, sandboxExec, string, []string) ([]byte, error) {
		*runs++
		return []byte(helpWithFlag), nil
	}
	first, second := d.cliFlags(context.Background()), d.cliFlags(context.Background())
	if !first.FwdSub || !second.FwdSub {
		t.Fatal("the second probe found the flag")
	}
	if *runs != 2 {
		t.Fatalf("--help runs = %d, want 2 (the failure, then one good answer kept)", *runs)
	}

	// The same in a sandbox, per session.
	*runs = 0
	ccHelpText = func(context.Context, sandboxExec, string, []string) ([]byte, error) {
		*runs++
		if *runs == 1 {
			return nil, errors.New("timeout")
		}
		return []byte(helpWithFlag), nil
	}
	s := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	s.useSandbox(sandboxExecPrefix("blerg-sess-y"))
	if s.cliFlags(context.Background()).FwdSub || !s.cliFlags(context.Background()).FwdSub || !s.cliFlags(context.Background()).FwdSub {
		t.Fatal("want no, then yes, then the kept yes")
	}
	if *runs != 2 {
		t.Fatalf("sandbox --help runs = %d, want 2", *runs)
	}
}

// An upgraded CLI (another modification time) is asked again without a daemon restart.
func TestForwardSubagentProbeAsksAgainForAnUpgradedCLI(t *testing.T) {
	path := fakeHostClaude(t)
	runs := stubHelp(t, "Options:\n  --verbose\n", nil)
	d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	first, second := d.cliFlags(context.Background()), d.cliFlags(context.Background())
	if first.FwdSub || second.FwdSub {
		t.Fatal("the old CLI lacks the flag")
	}
	if *runs != 1 {
		t.Fatalf("--help runs = %d, want 1", *runs)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	ccHelpText = func(context.Context, sandboxExec, string, []string) ([]byte, error) {
		*runs++
		return []byte(helpWithFlag), nil
	}
	if !d.cliFlags(context.Background()).FwdSub {
		t.Fatal("the upgraded CLI lists the flag")
	}
	if *runs != 2 {
		t.Fatalf("--help runs = %d, want 2", *runs)
	}
}

// --include-partial-messages is probed from the same --help and passed, on both kinds of process,
// only when the CLI lists it.
func TestPartialMessagesFlagFollowsTheHelp(t *testing.T) {
	for name, args := range map[string][]string{
		"turn":    ccTurnArgs("do it", "", "", ""),
		"session": ccSessionArgs("", "", ""),
	} {
		if slices.Contains(args, ccPartialMessagesFlag) {
			t.Errorf("%s args carry %s without the option: %v", name, ccPartialMessagesFlag, args)
		}
	}
	for name, args := range map[string][]string{
		"turn":    ccTurnArgs("do it", "", "", "", withCLIFlags(ccCLIFlags{Partial: true})...),
		"session": ccSessionArgs("", "", "", withCLIFlags(ccCLIFlags{Partial: true})...),
	} {
		if !slices.Contains(args, ccPartialMessagesFlag) || slices.Contains(args, ccForwardSubagentFlag) {
			t.Errorf("%s args with only the partial option: %v", name, args)
		}
	}
	for help, want := range map[string]ccCLIFlags{
		helpWithFlag: {FwdSub: true},
		helpWithFlag + "  --include-partial-messages  Include partial message chunks\n": {FwdSub: true, Partial: true},
		"Options:\n  --include-partial-messages\n":                                      {Partial: true},
	} {
		stubHelp(t, help, nil)
		d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
		d.useSandbox(sandboxExecPrefix("blerg-sess-x"))
		if got := d.cliFlags(context.Background()); got != want {
			t.Errorf("help %q: flags = %+v, want %+v", help, got, want)
		}
	}
}
