package daemon

// Why the previous run of the daemon ended, as far as this one can tell, so the agent of a session
// the restart cut off is told the right thing (design: docs/design/agent-session-recovery.md).
//
// Two markers in the daemon's state directory, each read and removed once at start:
//
//   - restart-planned: written by the installer (install/desktop/daemon/install.sh) right before
//     it restarts a running daemon, holding the reason ("update" or "reinstall"). It is the
//     explicit reason.
//   - stopped-cleanly: written by the daemon itself when it is told to stop (SIGTERM from systemd
//     or launchd, SIGINT). A previous run that left neither marker crashed or was killed.
//
// Either marker makes the restart planned; neither makes it unplanned.
// Update recovery is same-user bookkeeping; see updaterestart.go.

import (
	"github.com/blerglab/blerg-ai/runner/internal/updatehandoff"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	plannedRestartFile = "restart-planned"
	cleanStopFile      = "stopped-cleanly"
	// plannedRestartMaxAge bounds how old an installer marker may be: one left behind by an
	// install whose restart never happened must not explain a crash much later.
	plannedRestartMaxAge = 15 * time.Minute
)

// RestartCause is why the previous run of the daemon ended.
type RestartCause struct {
	update *updatehandoff.Receipt
	// Planned: the installer said it was restarting the daemon, or the previous run was told to
	// stop rather than crashing.
	Planned bool
	// Reason is the installer's reason ("update", "reinstall"); "" when it gave none.
	Reason string
}

// ReadRestartCause reads and removes the markers in stateDir: the cause is told once, for the
// restart that just happened, never for a later one. "" (no state directory) says unplanned.
func ReadRestartCause(stateDir string) RestartCause {
	if stateDir == "" {
		return RestartCause{}
	}
	var c RestartCause
	planned := filepath.Join(stateDir, plannedRestartFile)
	if info, err := os.Lstat(planned); err == nil {
		if info.Mode().IsRegular() && time.Since(info.ModTime()) < plannedRestartMaxAge {
			c.Planned = true
			if b, err := os.ReadFile(planned); err == nil { //nolint:gosec // G304: a fixed file name in the daemon's own state directory, checked regular above
				switch r := strings.TrimSpace(string(b)); r {
				case "update", "reinstall":
					c.Reason = r
				}
			}
		}
		removeMarker(planned)
	}
	clean := filepath.Join(stateDir, cleanStopFile)
	if _, err := os.Lstat(clean); err == nil {
		c.Planned = true
		removeMarker(clean)
	}
	return c
}

func removeMarker(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("blerg-runner-daemon: removing %s: %v", path, err)
	}
}

// NoteCleanStop records that this run of the daemon was told to stop, for the next run's
// ReadRestartCause. Called first thing on a stop signal.
func NoteCleanStop(stateDir string) {
	if stateDir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(stateDir, cleanStopFile), []byte("stopped\n"), 0o600); err != nil {
		log.Printf("blerg-runner-daemon: noting a clean stop: %v", err)
	}
}

// String is the cause for the daemon's log.
func (c RestartCause) String() string {
	switch {
	case c.Reason != "":
		return "planned (" + c.Reason + ")"
	case c.Planned:
		return "planned (stopped on request)"
	}
	return "unplanned (the previous run did not stop cleanly)"
}

// restartCauseWords is what the agent of a session the restart cut off is told about why it
// happened, and how to go on. The unplanned case is the one an agent is tempted to blame on its
// own last command; it is told that ordinary commands cannot do that. A planned restart says
// nothing the agent ran caused it, unless one of its cut-off calls could have (mayHaveStopped).
func restartCauseWords(c RestartCause, calls []OpenCall) string {
	if !c.Planned {
		return restartWordsUnplanned
	}
	var b strings.Builder
	switch c.Reason {
	case "update":
		b.WriteString("The daemon was updated and restarted.")
	case "reinstall":
		b.WriteString("The daemon was reinstalled and restarted.")
	default:
		b.WriteString("The daemon was restarted.")
	}
	if !mayHaveStopped(c, calls) {
		b.WriteString(" Nothing you ran caused it.")
	}
	b.WriteString(" " + restartWordsGoOn)
	return b.String()
}

const (
	restartWordsUnplanned = "The daemon stopped unexpectedly. A test run, a build or ordinary commands cannot cause that; " +
		"only re-check a command that could affect the daemon itself (an install, a service restart) before running it again. " +
		"Continue where you left off; check the state of anything that was mid-run first."
	restartWordsGoOn = "Continue where you left off; check the state of anything that was mid-run first — " +
		"never re-run anything destructive without checking."
)

// mayHaveStopped reports whether one of the calls the restart cut off may be what stopped the
// daemon in a planned restart: the installer or a service restart or stop (daemonCommand, on the
// deny-list), or, when the daemon knows only that it was told to stop (no installer reason), a
// command that signals processes (signalCommand).
func mayHaveStopped(c RestartCause, calls []OpenCall) bool {
	for _, call := range calls {
		if !strings.EqualFold(call.Tool, "Bash") { // Codex names it "bash"
			continue
		}
		if call.DaemonCommand || (c.Reason == "" && call.SignalCommand) ||
			(call.CommandHash == "" && (daemonCommand.MatchString(call.Detail) || (c.Reason == "" && signalCommand.MatchString(call.Detail)))) {
			return true
		}
	}
	return false
}

// signalCommand is a command that sends processes a signal. Not on the deny-list (stopping a
// process of one's own is ordinary), but a stop request the daemon got may have come from it.
var signalCommand = regexp.MustCompile(`(?i)(^|[\s;&|(])(kill|pkill|killall)(\s|$)`)
