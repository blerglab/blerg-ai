package activity

import (
	"fmt"
	"github.com/blerglab/blerg-ai/runner/skills"
	"regexp"
	"strings"
	"time"
)

type Worker struct {
	Skills         skills.Report `json:"skills"`
	SessionID      string        `json:"session_id"`
	Status         string        `json:"status"`
	AgeSeconds     float64       `json:"age_seconds"`
	GateFraction   float64       `json:"gate_fraction"`
	Turns          []Turn        `json:"turns"`
	TurnCount      int           `json:"turn_count"`
	TurnsTruncated bool          `json:"turns_truncated"`
}
type Workspace struct {
	SessionID   string    `json:"session_id"`
	GeneratedAt time.Time `json:"generated_at"`
	Workers     []Worker  `json:"workers"`
	Undelivered []Notice  `json:"undelivered_turn_notices"`
	Runs        []Run     `json:"runs"`
	Machine     *Machine  `json:"machine"`
}
type Nudge struct{ Key, Text string }

// Snapshot is deliberately ASCII and bounded below 600 bytes even for large
// workspaces. Aggregate counts refer to SessionID's tree, never the whole host.
func (w Workspace) Snapshot() string {
	if len(w.Workers) == 0 || len(w.Workers) == 1 && w.Workers[0].SessionID == w.SessionID {
		return ""
	}
	counts := map[string]int{}
	var longest *Worker
	for i := range w.Workers {
		v := &w.Workers[i]
		if v.SessionID == w.SessionID {
			continue
		}
		status := v.Status
		if status == "waiting" {
			status = "blocked"
		}
		counts[status]++
		if status == "running" && (longest == nil || v.AgeSeconds > longest.AgeSeconds) {
			longest = v
		}
	}
	scope := shortRef(w.SessionID)
	line := fmt.Sprintf("Workspace %s: running=%d idle=%d blocked=%d failed=%d", scope, counts["running"], counts["idle"], counts["blocked"], counts["failed"])
	if longest != nil {
		line += fmt.Sprintf("; longest=%s %.0fs gates=%.0f%%", shortRef(longest.SessionID), min(999999999, max(0, longest.AgeSeconds)), 100*min(1, max(0, longest.GateFraction)))
	}
	runningGates := 0
	for _, r := range w.Runs {
		if r.State == "running" {
			runningGates++
		}
	}
	line += fmt.Sprintf("; gates_running=%d", runningGates)
	if w.Machine != nil && w.GeneratedAt.Sub(w.Machine.At) <= 5*time.Minute {
		line += fmt.Sprintf("; machine=%s load=%.1f/%d disk_free=%dMiB", shortRef(w.Machine.SessionID), min(9999, max(0, w.Machine.Load1)), w.Machine.CPUs, w.Machine.DiskFreeBytes/(1<<20))
	} else {
		line += "; machine load/disk=unknown"
	}
	return line + "."
}

var safeReference = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func shortRef(s string) string {
	if len(s) > 64 {
		return "unknown"
	}
	if !safeReference.MatchString(s) {
		return "unknown"
	}
	return s
}

// Nudges are static facts for the existing plan_drift outbox. Stable fact
// keys ensure elapsed-time boundaries cannot repeat or invalidate a notice.
func (w Workspace) Nudges(now time.Time) []Nudge {
	var out []Nudge
	for _, v := range w.Workers {
		if v.SessionID == w.SessionID || (v.Status != "running" && v.Status != "idle" && v.Status != "blocked" && v.Status != "waiting") {
			continue
		}
		ref := shortRef(v.SessionID)
		action := " Inspect with `blerg-runner session result " + ref + "`; narrow the next check or stop redundant work."
		if v.GateFraction > .5 {
			out = append(out, Nudge{"telemetry:gates:" + ref, fmt.Sprintf("Session %s spent %.0f%% in gates.", ref, v.GateFraction*100) + action})
		}
		if v.Status == "running" && v.AgeSeconds > 1800 {
			out = append(out, Nudge{"telemetry:long:" + ref, fmt.Sprintf("Session %s has run %.0fs.", ref, v.AgeSeconds) + action})
		}
		if v.SessionID != w.SessionID && v.Status == "idle" && v.AgeSeconds > 900 {
			out = append(out, Nudge{"telemetry:idle:" + ref, fmt.Sprintf("Session %s has been idle %.0fs. Read `blerg-runner session result %s` and collect or resume it.", ref, v.AgeSeconds, ref)})
		}
	}
	for _, v := range w.Workers {
		if v.SessionID == w.SessionID || (v.Status != "running" && v.Status != "idle" && v.Status != "blocked" && v.Status != "waiting") {
			continue
		}
		if len(v.Turns) == 0 {
			continue
		}
		last := v.Turns[len(v.Turns)-1]
		var work, baseline float64
		for _, n := range last.TurnEndSeconds {
			work += n
		}
		previous := v.Turns[:len(v.Turns)-1]
		if len(previous) > 5 {
			previous = previous[len(previous)-5:]
		}
		for _, t := range previous {
			for _, n := range t.TurnEndSeconds {
				baseline += n
			}
		}
		if len(previous) > 0 {
			baseline /= float64(len(previous))
		}
		if work > max(30, baseline*2) {
			out = append(out, Nudge{fmt.Sprintf("telemetry:turn-end:%s:%d", v.SessionID, last.Seq), fmt.Sprintf("Session %s turn %d spent %.0fs ending the turn. Inspect `blerg-runner workspace metrics --session %s --turn %d --json` before another gate.", v.SessionID, last.Seq, work, v.SessionID, last.Seq)})
		}
	}
	for _, notice := range w.Undelivered {
		if notice.AgeSeconds > 300 {
			out = append(out, Nudge{fmt.Sprintf("telemetry:notice:%s:%d", notice.SessionID, notice.TurnSeq), fmt.Sprintf("Session %s turn %d notice is undelivered after %.0fs. Read `blerg-runner session result %s` and collect the result.", notice.SessionID, notice.TurnSeq, notice.AgeSeconds, notice.SessionID)})
		}
	}
	suites := map[string]int{}
	for _, r := range w.Runs {
		key := r.Suite + ":" + r.Commit
		if r.FullSuite {
			suites[key]++
		}
		if r.FullSuite && suites[key] == 3 {
			out = append(out, Nudge{"telemetry:repeated:" + key, fmt.Sprintf("Workspace %s suite %s commit %s has 3 full runs. Use touched checks; inspect `blerg-runner workspace metrics --json` before another full run.", w.SessionID, r.Suite, r.Commit)})
		}
		if r.State == "running" && now.Sub(r.ProgressAt) > 15*time.Minute {
			out = append(out, Nudge{"telemetry:stalled:" + r.ID, fmt.Sprintf("Session %s run %s suite %s commit %s has no progress for over 900s. Inspect `blerg-runner workspace metrics --json`; collect its result or stop it.", r.SessionID, r.ID, r.Suite, r.Commit)})
		}
	}
	if m := w.Machine; m != nil && now.Sub(m.At) <= 5*time.Minute {
		if m.CPUs > 0 && m.Load1 > float64(m.CPUs)*1.5 {
			out = append(out, Nudge{"telemetry:load:" + m.SessionID, fmt.Sprintf("Session %s machine load %.1f exceeds %d CPUs. Defer timing tests and reduce parallel gates; inspect `blerg-runner workspace metrics --json`.", m.SessionID, m.Load1, m.CPUs)})
		}
		if m.DiskFreeBytes < 2<<30 {
			out = append(out, Nudge{"telemetry:disk:" + m.SessionID, fmt.Sprintf("Session %s machine has %dMiB free. Inspect `blerg-runner workspace metrics --json` and remove disposable build output before more gates.", m.SessionID, m.DiskFreeBytes/(1<<20))})
		}
	}
	return out
}

func (w Workspace) Table() string {
	var b strings.Builder
	b.WriteString("SESSION  STATE  AGE(s)  GATES(%)\n")
	for _, v := range w.Workers {
		fmt.Fprintf(&b, "%s  %s  %.0f  %.0f\n", v.SessionID, v.Status, v.AgeSeconds, 100*v.GateFraction)
	}
	for _, r := range w.Runs {
		fmt.Fprintf(&b, "run=%s session=%s suite=%s commit=%s state=%s progress=%d\n", r.ID, r.SessionID, r.Suite, r.Commit, r.State, r.Progress)
	}
	return b.String()
}

// Run is an explicit progress registration. ID, suite and commit identify every
// number; progress means completed work units, not a heartbeat timestamp.
type Run struct {
	ID         string     `json:"id"`
	SessionID  string     `json:"session_id"`
	Suite      string     `json:"suite"`
	Commit     string     `json:"commit"`
	Card       string     `json:"card,omitempty"`
	State      string     `json:"state"`
	FullSuite  bool       `json:"full_suite"`
	Progress   int64      `json:"progress"`
	StartedAt  time.Time  `json:"started_at"`
	ProgressAt time.Time  `json:"progress_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
}

type Notice struct {
	SessionID  string  `json:"session_id"`
	TurnSeq    int64   `json:"turn_seq"`
	AgeSeconds float64 `json:"age_seconds"`
}
