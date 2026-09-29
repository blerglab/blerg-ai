package daemon

import (
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// tmuxSessionPrefix namespaces blerg-managed tmux sessions so we can find them
// again after a daemon restart without colliding with the user's own sessions.
const tmuxSessionPrefix = "blerg-"

// tmuxHistoryLimit is the per-pane scrollback depth (lines) for blerg-runner
// sessions, set as the server-global default before each pane is created.
// 50000 ≈ a full day of dense agent output without the 2000-line wall.
const tmuxHistoryLimit = "50000"

func tmuxSessionName(sessionID string) string {
	return tmuxSessionPrefix + sessionID
}

// tmuxSocketArgs is prepended to every host tmux invocation. Empty in
// production (the default socket: docs and tooling rely on `tmux -t
// blerg-<id>`); tests point it at a private server (`-L blerg-test-…`)
// through setTmuxSocketArgs. Guarded so a parallel test or a concurrent
// tmux call never races the override.
var (
	tmuxSocketMu   sync.RWMutex
	tmuxSocketArgs []string
)

// setTmuxSocketArgs replaces the socket args and returns a restore func.
func setTmuxSocketArgs(args []string) (restore func()) {
	tmuxSocketMu.Lock()
	prev := tmuxSocketArgs
	tmuxSocketArgs = append([]string(nil), args...)
	tmuxSocketMu.Unlock()
	return func() {
		tmuxSocketMu.Lock()
		tmuxSocketArgs = prev
		tmuxSocketMu.Unlock()
	}
}

// tmuxCmd returns an exec.Cmd for a host tmux subcommand on the configured
// socket.
func tmuxCmd(args ...string) *exec.Cmd {
	tmuxSocketMu.RLock()
	full := append(append([]string{}, tmuxSocketArgs...), args...)
	tmuxSocketMu.RUnlock()
	return exec.Command("tmux", full...) //nolint:gosec,noctx // literal tmux binary; args assembled by the daemon (no shell); noctx: short-lived tmux helper invoked from synchronous daemon code without a context
}

// tmuxInnerWithEnv wraps inner as `env KEY=VAL … <inner>` carrying every
// BLERG_RUNNER_* entry of env (and nothing else new). tmux seeds its
// server-global environment from the FIRST new-session only — the cmd.Env of
// a later new-session is ignored and its pane inherits the server's env — so
// per-session vars (SESSION_ID, SESSION_TOKEN, SERVER_HTTP, BOARD_*) must
// ride inside the pane command or every session would run as the first one.
// The master token is excluded even if a caller passed it.
//
// This is a literal argv, not a shell string: tmux runs a multi-argument
// pane command with execvp (only a lone argument goes through `sh -c`), and
// the `env` prefix guarantees at least two arguments, so values are passed
// through byte-for-byte with no quoting and no shell in between.
func tmuxInnerWithEnv(inner, env []string) []string {
	out := make([]string, 0, len(inner)+len(env)+1)
	out = append(out, "env")
	for _, kv := range env {
		if strings.HasPrefix(kv, "BLERG_RUNNER_") && !strings.HasPrefix(kv, "BLERG_RUNNER_DAEMON_TOKEN=") {
			out = append(out, kv)
		}
	}
	return append(out, inner...)
}

// tmuxScrubbedGlobals are unset from the tmux server's global environment at
// daemon connect: a server started by an older daemon (which exported the
// master token) or from the user's shell would otherwise hand them to every
// pane created later.
var tmuxScrubbedGlobals = []string{
	"BLERG_RUNNER_DAEMON_TOKEN",
	"BLERG_RUNNER_SESSION_TOKEN",
	"BLERG_RUNNER_SESSION_ID",
	"BLERG_RUNNER_BOARD_TOKEN",
}

// scrubTmuxGlobalEnv removes tmuxScrubbedGlobals from the server's global
// env. Best-effort: "no server running" (or an unset variable) is fine.
func scrubTmuxGlobalEnv() {
	for _, k := range tmuxScrubbedGlobals {
		_ = tmuxCmd("set-environment", "-gu", k).Run()
	}
}

// createTmuxSession creates a detached, persistent tmux session named for
// sessionID running inner in startDir with the given environment, then attaches
// it via tmuxAttachCommand. Creating detached first (rather than new-session -A
// under the PTY) lets us configure the session before any client sees it, with
// no race. The session outlives the daemon: when the attached client dies, the
// session simply becomes unattached and keeps running.
func createTmuxSession(sessionID, startDir string, inner, env []string) error {
	// Raise the scrollback depth before the pane exists: tmux reads
	// history-limit only when a pane is created, so a per-session set-option
	// after new-session would be too late. There is no new-session flag for it,
	// so we set the server-global default. Default is 2000 lines, which capped
	// browser scroll-back; tmuxHistoryLimit lifts that wall. Affects any later
	// tmux session on this server too, but a larger default only costs memory.
	_ = tmuxCmd("set-option", "-g", "history-limit", tmuxHistoryLimit).Run()

	args := []string{"new-session", "-d", "-s", tmuxSessionName(sessionID)}
	if startDir != "" {
		args = append(args, "-c", startDir)
	}
	args = append(args, "--")
	// Per-session vars travel inside the pane command (see tmuxInnerWithEnv);
	// cmd.Env below only seeds the server's global env if this happens to be
	// the first session, and is already sanitised.
	args = append(args, tmuxInnerWithEnv(inner, env)...)

	cmd := tmuxCmd(args...)
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		return err
	}

	// Hide tmux's own status bar so the view reads as a plain terminal, and
	// pin destroy-unattached off so a daemon detach never reaps the session,
	// regardless of the user's global tmux config.
	name := tmuxSessionName(sessionID)
	_ = tmuxCmd("set-option", "-t", name, "status", "off").Run()
	_ = tmuxCmd("set-option", "-t", name, "destroy-unattached", "off").Run()
	// Enable mouse mode so the pane reports mouse/wheel events. xterm.js, once it
	// sees the SGR mouse-tracking request, forwards wheel events to the PTY
	// instead of translating them into cursor keys (which the inner program reads
	// as input-history navigation). tmux then drives copy-mode scrollback from the
	// wheel. Drag-to-select now goes to tmux; hold Shift to select locally in the
	// browser for clipboard copy.
	_ = tmuxCmd("set-option", "-t", name, "mouse", "on").Run()
	return nil
}

// tmuxAttachCommand builds a command that attaches to an existing tmux session,
// detaching any other client (-d) so a restarted daemon takes over cleanly.
// Transparently routes through `docker exec` for a sandboxed session (see
// sandbox.go) — the caller (NewSession, over a real PTY) doesn't need to care.
func tmuxAttachCommand(sessionID string) []string {
	name := tmuxSessionName(sessionID)
	if container, ok := sandboxContainer(sessionID); ok {
		return []string{"docker", "exec", "-it", container, "tmux", "attach-session", "-d", "-t", name}
	}
	return []string{"tmux", "attach-session", "-d", "-t", name}
}

// listBlergRunnerTmuxSessions returns the session IDs of all persistent blerg-runner
// tmux sessions currently alive. Returns an empty slice if tmux is unavailable
// or no server is running (both surface as a non-zero exit).
func listBlergRunnerTmuxSessions() []string {
	out, err := tmuxCmd("ls", "-F", "#{session_name}").Output()
	if err != nil {
		return []string{}
	}
	ids := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.TrimSpace(line)
		if strings.HasPrefix(name, tmuxSessionPrefix) {
			ids = append(ids, strings.TrimPrefix(name, tmuxSessionPrefix))
		}
	}
	return ids
}

// capturePane returns the visible contents of sessionID's tmux pane as plain
// text (no escape sequences). It is the ground-truth input to classifyScreen:
// reading the rendered screen avoids the repaint-vs-animation ambiguity of the
// raw PTY stream. An error (tmux gone, session not found) leaves classification
// to the caller, which preserves the last known state.
func capturePane(sessionID string) (string, error) {
	out, err := runTmuxFor(sessionID, "capture-pane", "-p", "-t", tmuxSessionName(sessionID)).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// captureScrollback returns the session's scrollback — the lines that have
// scrolled ABOVE the visible screen — as text including SGR escape sequences.
//
//	-p         print to stdout
//	-e         include escape sequences (colours/attributes)
//	-S -       start at the oldest history line (or -<maxLines> when bounded)
//	-E -1      end one line above the visible screen, so the live screen (which
//	           the browser renders from the PTY byte stream) is not duplicated
//
// maxLines, when > 0, bounds the capture to the last maxLines lines of history
// (tmux's -S -N means "N lines back from the visible screen") instead of
// pulling the full scrollback — a cheap incremental refresh for a browser that
// already has most of the history. maxLines == 0 keeps the old unbounded
// behavior.
//
// This is authoritative regardless of how the inner program draws: tmux
// maintains correct scrollback even for apps that redraw via absolute cursor
// positioning, where replaying the raw byte stream into xterm yields none.
func captureScrollback(sessionID string, maxLines int) (string, error) {
	start := "-"
	if maxLines > 0 {
		start = "-" + strconv.Itoa(maxLines)
	}
	out, err := runTmuxFor(sessionID, "capture-pane", "-p", "-e",
		"-S", start, "-E", "-1", "-t", tmuxSessionName(sessionID)).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// buildModeSyncSequences constructs the escape sequences that reflect a pane's
// current modes: DECCKM (application cursor keys, which the client reads via
// terminal.modes.applicationCursorKeysMode to decide how to encode arrow keys)
// and cursor visibility. An alt-screen pane (alternateOn) yields nothing —
// alt-screen apps repaint their whole screen on attach/resize, so a mode-sync
// preamble would only fight them, not help them.
func buildModeSyncSequences(alternateOn, appCursorKeys, cursorVisible bool) string {
	if alternateOn {
		return ""
	}
	var b strings.Builder
	if appCursorKeys {
		b.WriteString("\x1b[?1h")
	} else {
		b.WriteString("\x1b[?1l")
	}
	if cursorVisible {
		b.WriteString("\x1b[?25h")
	} else {
		b.WriteString("\x1b[?25l")
	}
	return b.String()
}

// modeSyncSequences queries sessionID's current pane modes via
// `tmux display-message` and returns the matching escape sequences (see
// buildModeSyncSequences). Bracketed paste is deliberately not queried here:
// tmux 3.2a has no format variable for it, so that mode is synced client-side
// instead. Best-effort: a query failure (tmux gone, session not found) yields
// "" — no mode-sync preamble, matching the old behavior of not syncing modes.
func modeSyncSequences(sessionID string) string {
	out, err := runTmuxFor(sessionID, "display-message", "-p", "-t", tmuxSessionName(sessionID),
		"-F", "#{alternate_on} #{keypad_cursor_flag} #{cursor_flag}").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) != 3 {
		return ""
	}
	return buildModeSyncSequences(fields[0] == "1", fields[1] == "1", fields[2] == "1")
}

// killTmuxSession terminates the persistent tmux session for sessionID. Any
// attached client then receives EOF, which ends the blerg-runner session
// normally. For a sandboxed session this also removes its container —
// otherwise a killed session would leak a stopped container per launch.
func killTmuxSession(sessionID string) {
	_ = runTmuxFor(sessionID, "kill-session", "-t", tmuxSessionName(sessionID)).Run()
	removeSandboxContainer(sessionID)
}

// refreshTmuxClient forces a full repaint of every tmux client attached to
// sessionID. tmux's incremental redraw after a resize can leave stale cells in
// the browser's xterm (drawn at the old geometry); refresh-client re-sends the
// whole grid at the current size, clearing that residue. Best-effort: a missing
// session or no attached clients simply yields nothing to refresh.
func refreshTmuxClient(sessionID string) {
	name := tmuxSessionName(sessionID)
	out, err := runTmuxFor(sessionID, "list-clients", "-t", name, "-F", "#{client_name}").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		client := strings.TrimSpace(line)
		if client == "" {
			continue
		}
		_ = runTmuxFor(sessionID, "refresh-client", "-t", client).Run()
	}
}

// mergeSessionIDs returns the de-duplicated union of any number of ID slices.
func mergeSessionIDs(groups ...[]string) []string {
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	seen := make(map[string]struct{}, total)
	out := make([]string, 0, total)
	for _, group := range groups {
		for _, id := range group {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}
