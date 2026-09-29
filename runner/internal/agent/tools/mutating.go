package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// WriteFile returns the "write_file" tool.
func WriteFile(workDir string) agent.Tool {
	return fnTool{
		def: agent.ToolDef{
			Name:        "write_file",
			Description: "Write a file in the workspace, creating parent directories. Overwrites existing content.",
			InputSchema: schema(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`),
		},
		mutating: true,
		run: func(_ context.Context, in json.RawMessage) (string, error) {
			var args struct{ Path, Content string }
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			p, err := resolve(workDir, args.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // workspace working-tree directory (git-tracked source, not secret state); path confined to the session workspace by resolve() (symlink-aware containment check)
				return "", err
			}
			if err := os.WriteFile(p, []byte(args.Content), 0o644); err != nil { //nolint:gosec // workspace source file, not a secret: must stay readable by the user's editor and git; path confined to the session workspace by resolve() (symlink-aware containment check)
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), nil
		},
	}
}

// EditFile returns the "edit_file" tool (unique-match string replacement).
func EditFile(workDir string) agent.Tool {
	return fnTool{
		def: agent.ToolDef{
			Name:        "edit_file",
			Description: "Replace old_string with new_string in a file. old_string must appear exactly once.",
			InputSchema: schema(`{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"}},"required":["path","old_string","new_string"]}`),
		},
		mutating: true,
		run: func(_ context.Context, in json.RawMessage) (string, error) {
			var args struct {
				Path      string `json:"path"`
				OldString string `json:"old_string"`
				NewString string `json:"new_string"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			p, err := resolve(workDir, args.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(p) //nolint:gosec // path confined to the session workspace by resolve() (symlink-aware containment check)
			if err != nil {
				return "", err
			}
			n := strings.Count(string(data), args.OldString)
			if n == 0 {
				return "", fmt.Errorf("old_string not found in %s", args.Path)
			}
			if n > 1 {
				return "", fmt.Errorf("old_string has %d matches in %s; provide more context to make it unique", n, args.Path)
			}
			out := strings.Replace(string(data), args.OldString, args.NewString, 1)
			if err := os.WriteFile(p, []byte(out), 0o644); err != nil { //nolint:gosec // rewrite of an existing workspace file (its mode is kept); path confined to the session workspace by resolve() (symlink-aware containment check)
				return "", err
			}
			return fmt.Sprintf("edited %s", args.Path), nil
		},
	}
}

const (
	bashDefaultTimeout = 120 * time.Second
	bashMaxTimeout     = 600 * time.Second
)

// Bash returns the "bash" tool. Commands run in their own process group so
// timeouts and cancellation kill the whole tree.
func Bash(workDir string) agent.Tool { return BashEnv(workDir, nil) }

// BashEnv is Bash with an explicit environment for the spawned shell. nil
// inherits the host process env (Bash's behaviour); a hosting daemon passes
// its sanitised session env so the model's shell never sees credentials the
// daemon holds for itself.
func BashEnv(workDir string, env []string) agent.Tool {
	return fnTool{
		def: agent.ToolDef{
			Name:        "bash",
			Description: "Run a bash command in the workspace. Default timeout 120s (timeout_seconds, max 600). stdout+stderr combined.",
			InputSchema: schema(`{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer"}},"required":["command"]}`),
		},
		mutating: true,
		run: func(ctx context.Context, in json.RawMessage) (string, error) {
			var args struct {
				Command        string `json:"command"`
				TimeoutSeconds int    `json:"timeout_seconds"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			timeout := bashDefaultTimeout
			if args.TimeoutSeconds > 0 {
				timeout = time.Duration(args.TimeoutSeconds) * time.Second
				if timeout > bashMaxTimeout {
					timeout = bashMaxTimeout
				}
			}
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			cmd := exec.Command("bash", "-c", args.Command) //nolint:gosec,noctx // the bash tool: executing the model's command is its purpose; runs in the session's own sandbox/pod with a timeout, own process group and filtered env; noctx: the timeout is enforced by the select below, which kills the whole process group; CommandContext would only signal bash
			cmd.Dir = workDir
			cmd.Env = env
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			var buf strings.Builder
			cmd.Stdout = &buf
			cmd.Stderr = &buf
			if err := cmd.Start(); err != nil {
				return "", err
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-cctx.Done():
				// Kill the whole process group (negative pid).
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				reason := "timed out"
				if ctx.Err() != nil {
					reason = "cancelled"
				}
				// A descendant that escaped the process group (setsid) can
				// hold the output pipe open and stall Wait forever — never
				// let that wedge the session. Only read buf after Wait
				// returns (the exec copier goroutine writes it until then).
				select {
				case <-done:
					return capOutput(buf.String() + "\n[command " + reason + "]"), nil
				case <-time.After(2 * time.Second):
					return "[command " + reason + "; output withheld — orphaned child processes still hold the output pipe]", nil
				}
			case err := <-done:
				out := buf.String()
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					out += fmt.Sprintf("\n[exit status %d]", exitErr.ExitCode())
				} else if err != nil {
					return "", err
				}
				return capOutput(out), nil
			}
		},
	}
}
