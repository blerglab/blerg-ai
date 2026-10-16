package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Codex's usage limits, read from `codex app-server` (JSON-RPC over stdio, one
// JSON object per line, no "jsonrpc" member): initialize → initialized →
// account/rateLimits/read. One process per read, killed (with its process
// group) as soon as the answer is in or the deadline passes — simpler than
// keeping a long-lived server healthy, and a read is rare (usage_limits.go).
//
// The answer also names the account, its plan and its credits; only the
// windows' percentages and reset times are kept. Nothing else is decoded.

const (
	codexLimitsTimeout = 10 * time.Second
	// codexLimitsMaxLine bounds one line of the app-server's output.
	codexLimitsMaxLine = 1 << 20
	// The ids of our two requests: far from the small numbers the server gives its own requests.
	codexInitID  = 1001
	codexLimitID = 1002
)

type codexRateWindow struct {
	UsedPercent        *float64 `json:"usedPercent"`
	WindowDurationMins int      `json:"windowDurationMins"`
	ResetsAt           int64    `json:"resetsAt"`
}

type codexRPCLine struct {
	ID     *int   `json:"id"`
	Method string `json:"method"` // set on the server's own requests and notifications
	Result *struct {
		RateLimits *struct {
			Primary   *codexRateWindow `json:"primary"`
			Secondary *codexRateWindow `json:"secondary"`
		} `json:"rateLimits"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// codexWindowID names a window by its length: five hours or a week. A length
// Codex has not used so far falls back to the window's position.
func codexWindowID(mins int, fallback string) string {
	switch mins {
	case 300:
		return "5h"
	case 10080:
		return "7d"
	}
	return fallback
}

// usedPercent clamps a reported percentage to 0–100 (NaN reads as 0).
func usedPercent(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	return math.Min(v, 100)
}

// readCodexRateLimits runs bin as `codex app-server` and returns the account's
// limit windows. Any failure — no binary, not logged in, an error answer, no
// window, the deadline — is an error and the caller reports nothing.
func readCodexRateLimits(ctx context.Context, bin string, timeout time.Duration) ([]protocol.UsageWindow, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "app-server")
	cmd.Env = probeEnv()
	if h := homeDir(); h != "" {
		cmd.Dir = h
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		cancel() // kills the process group
		_ = cmd.Wait()
	}()

	type outcome struct {
		windows []protocol.UsageWindow
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		w, err := codexLimitsConversation(stdin, stdout)
		done <- outcome{w, err}
	}()
	select {
	case o := <-done:
		return o.windows, o.err
	case <-ctx.Done():
		return nil, fmt.Errorf("codex app-server: %w", ctx.Err())
	}
}

// codexLimitsConversation is the exchange itself, over the process's pipes.
func codexLimitsConversation(in io.Writer, out io.Reader) ([]protocol.UsageWindow, error) {
	lines := bufio.NewScanner(out)
	lines.Buffer(make([]byte, 0, 64<<10), codexLimitsMaxLine)
	send := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = in.Write(append(b, '\n'))
		return err
	}
	// await reads lines until the answer to id; notifications and the
	// server's own requests in between are skipped.
	await := func(id int) (*codexRPCLine, error) {
		for lines.Scan() {
			var l codexRPCLine
			if json.Unmarshal(lines.Bytes(), &l) != nil || l.ID == nil || *l.ID != id || l.Method != "" {
				continue
			}
			if l.Error != nil {
				return nil, fmt.Errorf("codex app-server: error %d", l.Error.Code)
			}
			return &l, nil
		}
		if err := lines.Err(); err != nil {
			return nil, fmt.Errorf("codex app-server: %w", err)
		}
		return nil, errors.New("codex app-server: ended without an answer")
	}

	if err := send(map[string]any{"id": codexInitID, "method": "initialize", "params": map[string]any{
		"clientInfo": map[string]string{"name": "blerg-runner", "title": "Blerg", "version": "1"},
	}}); err != nil {
		return nil, err
	}
	if _, err := await(codexInitID); err != nil {
		return nil, err
	}
	if err := send(map[string]any{"method": "initialized"}); err != nil {
		return nil, err
	}
	if err := send(map[string]any{"id": codexLimitID, "method": "account/rateLimits/read", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	l, err := await(codexLimitID)
	if err != nil {
		return nil, err
	}
	if l.Result == nil || l.Result.RateLimits == nil {
		return nil, errors.New("codex app-server: no rate limits in the answer")
	}
	var windows []protocol.UsageWindow
	for _, w := range []struct {
		win      *codexRateWindow
		fallback string
	}{{l.Result.RateLimits.Primary, "5h"}, {l.Result.RateLimits.Secondary, "7d"}} {
		if w.win == nil || w.win.UsedPercent == nil {
			continue
		}
		uw := protocol.UsageWindow{ID: codexWindowID(w.win.WindowDurationMins, w.fallback), UsedPercent: usedPercent(*w.win.UsedPercent)}
		if w.win.ResetsAt > 0 {
			uw.ResetsAt = time.Unix(w.win.ResetsAt, 0).UTC()
		}
		windows = append(windows, uw)
	}
	if len(windows) == 0 {
		return nil, errors.New("codex app-server: no limit window")
	}
	return windows, nil
}
