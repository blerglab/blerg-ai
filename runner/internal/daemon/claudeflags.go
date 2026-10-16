package daemon

import (
	"bytes"
	"context"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

// ccForwardSubagentFlag makes Claude Code stream a subagent's own text and thinking (as assistant
// lines with parent_tool_use_id), not only its tool calls. A CLI that lacks it refuses to start
// with "unknown option", so it is passed only when the CLI's --help lists it.
const ccForwardSubagentFlag = "--forward-subagent-text"

// ccPartialMessagesFlag makes Claude Code write the API's stream events as they arrive (stream_event
// lines), besides the whole assistant lines. The driver reads only when a thinking block opens and
// closes (claudethinking.go), so the chat can say "Thinking…" while a long think runs. Probed like
// ccForwardSubagentFlag: a CLI that lacks it refuses to start.
const ccPartialMessagesFlag = "--include-partial-messages"

// ccCLIFlags is what a CLI's --help said about the optional flags the driver passes.
type ccCLIFlags struct {
	FwdSub  bool // ccForwardSubagentFlag
	Partial bool // ccPartialMessagesFlag
}

// ccHelpPIDFile is the in-container PID file of the --help probe: never a turn's, so the probe
// cannot overwrite the record Interrupt reads.
const ccHelpPIDFile = "/tmp/blerg-help.pid"

// ccHelpProbeTimeout bounds the one --help run; a CLI that does not answer in time is treated as
// lacking the flag.
const ccHelpProbeTimeout = 20 * time.Second

// ccHelpText runs `claude --help` the way the driver runs a turn (on the host, or in the session's
// container) and returns its output. A var so the tests never start a real or fake CLI for it.
var ccHelpText = func(ctx context.Context, prefix sandboxExec, workDir string, env []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ccHelpProbeTimeout)
	defer cancel()
	cmd := prefix.commandPID(ctx, workDir, "claude", ccHelpPIDFile, "--help")
	cmd.Env = env
	return cmd.CombinedOutput()
}

// ccHostFlags caches the host CLI's answer per binary: the `claude` the daemon's PATH resolves
// to, and its modification time, so an upgraded CLI is asked again without a daemon restart. A
// sandboxed session's CLI is the one in its own container, so the driver keeps that answer itself
// (claudeCodeDriver.flags). Only an answer from a --help that ran to the end is kept: one that
// failed or timed out is asked again next time.
var ccHostFlags struct {
	mu    sync.Mutex
	flags map[string]ccCLIFlags
}

// ccHostBinaryKey names the host CLI the next turn runs: its path and modification time ("" when
// it cannot be found, which is never cached).
func ccHostBinaryKey() string {
	path, err := exec.LookPath("claude")
	if err != nil {
		return ""
	}
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return path + "@" + fi.ModTime().UTC().Format(time.RFC3339Nano)
}

// cliFlags reports which optional flags this session's CLI takes, asking it once per host binary
// (or once per sandboxed session) and saying so in the log when it lacks one.
func (d *claudeCodeDriver) cliFlags(ctx context.Context) ccCLIFlags {
	probe := func() (flags ccCLIFlags, answered bool) {
		out, err := ccHelpText(ctx, d.prefix, d.workDir, d.env)
		if err != nil {
			log.Printf("claude-code: could not read the CLI's --help (%v); %s and %s are not passed this time", err, ccForwardSubagentFlag, ccPartialMessagesFlag)
			return ccCLIFlags{}, false
		}
		flags.FwdSub = bytes.Contains(out, []byte(ccForwardSubagentFlag))
		flags.Partial = bytes.Contains(out, []byte(ccPartialMessagesFlag))
		if !flags.FwdSub {
			log.Printf("claude-code: the CLI does not list %s; a subagent's text and thinking are not streamed", ccForwardSubagentFlag)
		}
		if !flags.Partial {
			log.Printf("claude-code: the CLI does not list %s; the chat cannot tell when the model is thinking", ccPartialMessagesFlag)
		}
		return flags, true
	}
	if !d.prefix.enabled() {
		key := ccHostBinaryKey()
		ccHostFlags.mu.Lock()
		defer ccHostFlags.mu.Unlock()
		if v, ok := ccHostFlags.flags[key]; ok && key != "" {
			return v
		}
		v, answered := probe()
		if answered && key != "" {
			if ccHostFlags.flags == nil {
				ccHostFlags.flags = map[string]ccCLIFlags{}
			}
			ccHostFlags.flags[key] = v
		}
		return v
	}
	d.flagsMu.Lock()
	defer d.flagsMu.Unlock()
	if d.flags != nil {
		return *d.flags
	}
	v, answered := probe()
	if answered {
		d.flags = &v
	}
	return v
}

// withForwardSubagentText adds ccForwardSubagentFlag to the arguments.
func withForwardSubagentText() ccOption {
	return func(o *ccOptions) { o.ForwardSubagentText = true }
}

// withPartialMessages adds ccPartialMessagesFlag to the arguments.
func withPartialMessages() ccOption {
	return func(o *ccOptions) { o.PartialMessages = true }
}

// withCLIFlags adds the optional flags the CLI takes.
func withCLIFlags(f ccCLIFlags) []ccOption {
	var opts []ccOption
	if f.FwdSub {
		opts = append(opts, withForwardSubagentText())
	}
	if f.Partial {
		opts = append(opts, withPartialMessages())
	}
	return opts
}
