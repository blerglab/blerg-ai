package daemon

// What a restart of the running daemon would cut off, for `blerg-runner-daemon status
// --open-work` (the installer asks before reinstalling over it). It is read from the recovery
// records (agentrecord.go), which the running daemon keeps current: no server, no credential.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// OpenWorkReport lists the agent sessions in recordsDir that a restart would cut off — a turn
// running, background tasks, subagents — one block per session, or "" when there are none. It
// only reads: the records belong to the running daemon, so nothing is removed or rewritten (not
// even a record it cannot read, which it skips).
//
// self is the session asking, when it is one (its BLERG_RUNNER_SESSION_ID: an agent updating the
// daemon from inside a session): its own running turn is the one asking, and is not listed; its
// background tasks and subagents are.
//
// Only sessions that keep a record are known here: a sandboxed session, or one with a tool
// restriction or an MCP grant, keeps none (and is not recovered after a restart either).
func OpenWorkReport(recordsDir, self string) string {
	exists, err := secureAgentRecordsDir(recordsDir, false)
	if err != nil || !exists {
		return ""
	}
	entries, err := os.ReadDir(recordsDir)
	if err != nil {
		return ""
	}
	var recs []AgentRecord
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		p := filepath.Join(recordsDir, name)
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() || !ownedByDaemon(info) {
			continue
		}
		data, err := os.ReadFile(p) //nolint:gosec // p is a regular file owned by the daemon's user in its private records directory
		var rec AgentRecord
		if err != nil || json.Unmarshal(data, &rec) != nil || rec.SessionID == "" {
			continue
		}
		recs = append(recs, rec)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].SessionID < recs[j].SessionID })

	var b strings.Builder
	for _, rec := range recs {
		shells, agents := cutOffSplit(rec.OpenTasks, rec.OpenSubagents)
		var what []string
		if rec.TurnActive && rec.SessionID != self {
			what = append(what, "a turn is running")
		}
		if n := len(shells); n > 0 {
			what = append(what, plural(n, "background task", "background tasks"))
		}
		if n := len(agents); n > 0 {
			what = append(what, plural(n, "subagent", "subagents"))
		}
		if len(what) == 0 {
			continue
		}
		fmt.Fprintf(&b, "session %s", rec.SessionID)
		if rec.Repo != "" {
			fmt.Fprintf(&b, " (%s)", rec.Repo)
		}
		fmt.Fprintf(&b, ": %s\n", strings.Join(what, ", "))
		for _, t := range shells {
			fmt.Fprintf(&b, "  - background %s task %s: %s\n", t.Kind, t.TaskID, oneLine(t.Description))
		}
		for _, s := range agents {
			kind := s.AgentType
			if kind == "" {
				kind = "subagent"
			}
			fmt.Fprintf(&b, "  - subagent %s: %s\n", kind, oneLine(s.Summary))
		}
	}
	return b.String()
}

// oneLine is s on one line, cut to fit a terminal, with every control or other non-printable
// character (an escape sequence in a command's description) made a space first.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, s)
	return cutRunes(strings.Join(strings.Fields(s), " "), 100)
}
