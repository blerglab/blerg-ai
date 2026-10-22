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
	"slices"
	"strings"
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
// windows' percentages and reset times are kept (and, for a usage-based work
// plan's spend limit, the share left and its reset). Nothing else is decoded.

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

// codexSpendLimit is a snapshot's individualLimit: the spend control of a usage-based work plan.
// Only its share left and reset are decoded; its used and limit amounts never are.
type codexSpendLimit struct {
	RemainingPercent *float64 `json:"remainingPercent"`
	ResetsAt         int64    `json:"resetsAt"`
}

// codexRateSnapshot is one bucket of the answer: its two windows and, on a usage-based work
// plan (which has no windows), its spend limit.
type codexRateSnapshot struct {
	Primary         *codexRateWindow `json:"primary"`
	Secondary       *codexRateWindow `json:"secondary"`
	IndividualLimit *codexSpendLimit `json:"individualLimit"`
}

type codexRPCLine struct {
	ID     *int   `json:"id"`
	Method string `json:"method"` // set on the server's own requests and notifications
	Result *struct {
		// RateLimits is the single-bucket view every version sends; RateLimitsByLimitId (newer
		// versions) is keyed by the metered limit ("codex"), read when the single view is empty.
		RateLimits          *codexRateSnapshot           `json:"rateLimits"`
		RateLimitsByLimitID map[string]codexRateSnapshot `json:"rateLimitsByLimitId"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// errNoLimitWindows: Codex answered, and the account it is signed in to has no limit window to
// show (an API key, or a work plan with neither windows nor a spend limit). The tracker reports
// that (UsageLimits.Note "no_windows"), so the sidebar says so instead of showing nothing.
var errNoLimitWindows = errors.New("the account reports no limit windows")

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

	return codexLimitsConversationContext(ctx, stdin, stdout)
}

// codexLimitsConversationContext applies the deadline independently of process
// startup. Closing the transport after cancellation releases the reader.
func codexLimitsConversationContext(ctx context.Context, in io.Writer, out io.Reader) ([]protocol.UsageWindow, error) {

	type outcome struct {
		windows []protocol.UsageWindow
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		w, err := codexLimitsConversation(in, out)
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
	if l.Result == nil || (l.Result.RateLimits == nil && l.Result.RateLimitsByLimitID == nil) {
		return nil, errors.New("codex app-server: no rate limits in the answer")
	}
	var windows []protocol.UsageWindow
	if l.Result.RateLimits != nil {
		windows = codexSnapshotWindows(*l.Result.RateLimits)
	}
	if len(windows) == 0 {
		// The single view mirrors one bucket (and may be absent); the per-limit view may have the
		// windows it lacks:
		// Codex's own bucket first, then the others by name.
		ids := make([]string, 0, len(l.Result.RateLimitsByLimitID))
		for id := range l.Result.RateLimitsByLimitID {
			ids = append(ids, id)
		}
		slices.SortFunc(ids, func(a, b string) int {
			if (a == "codex") != (b == "codex") {
				if a == "codex" {
					return -1
				}
				return 1
			}
			return strings.Compare(a, b)
		})
		for _, id := range ids {
			if windows = codexSnapshotWindows(l.Result.RateLimitsByLimitID[id]); len(windows) > 0 {
				break
			}
		}
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("codex app-server: %w", errNoLimitWindows)
	}
	return windows, nil
}

// codexSnapshotWindows maps one bucket to its windows: the five-hour and weekly ones, and a
// spend limit ("spend", the share used and its reset; never the amounts).
func codexSnapshotWindows(snap codexRateSnapshot) []protocol.UsageWindow {
	var windows []protocol.UsageWindow
	for _, w := range []struct {
		win      *codexRateWindow
		fallback string
	}{{snap.Primary, "5h"}, {snap.Secondary, "7d"}} {
		if w.win == nil || w.win.UsedPercent == nil {
			continue
		}
		uw := protocol.UsageWindow{ID: codexWindowID(w.win.WindowDurationMins, w.fallback), UsedPercent: usedPercent(*w.win.UsedPercent)}
		if w.win.ResetsAt > 0 {
			uw.ResetsAt = time.Unix(w.win.ResetsAt, 0).UTC()
		}
		windows = append(windows, uw)
	}
	if l := snap.IndividualLimit; l != nil && l.RemainingPercent != nil {
		uw := protocol.UsageWindow{ID: "spend", UsedPercent: usedPercent(100 - *l.RemainingPercent)}
		if l.ResetsAt > 0 {
			uw.ResetsAt = time.Unix(l.ResetsAt, 0).UTC()
		}
		windows = append(windows, uw)
	}
	return windows
}
