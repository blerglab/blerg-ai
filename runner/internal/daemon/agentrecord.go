package daemon

// Recovery records for agent-kind sessions (design: docs/design/agent-session-recovery.md).
//
// An agent session lives in the daemon's memory. Without a record a daemon restart ends it: the
// new process does not know it existed. A record holds what is needed to host the session again
// and to resume its Claude conversation; AgentHost.Adopt (agentadopt.go) acts on it once, at
// process start.
//
// Records live in <daemon state dir>/agents, NOT under the repos root: the repos root can be
// changed from the app, and a daemon older than this file reads <repos root>/.blerg-runner/sessions
// and would recreate anything it found there as a terminal session.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

const (
	agentRecordVersion = 1
	agentRecordsSubdir = "agents"
)

// AgentRecord is the on-disk state of one recoverable agent session. It is an allow-list: only
// what adoption needs, never the spawn message. Not recorded, on purpose: the initial prompt
// (recovery must never replay the kickoff), the git token and clone source, and anything about a
// sandbox, a tool restriction or an MCP grant (a session with one of those has no record).
type AgentRecord struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	Repo      string `json:"repo"`
	WorkDir   string `json:"work_dir"` // the resolved workspace

	Interaction string `json:"interaction,omitempty"`
	Assist      bool   `json:"assist,omitempty"`
	BoardID     string `json:"board_id,omitempty"`
	TicketID    string `json:"ticket_id,omitempty"`

	// The session's environment. Secrets, as in the terminal records: the file is 0600 in a
	// directory private to the daemon's user.
	SessionToken string            `json:"session_token,omitempty"`
	BoardToken   string            `json:"board_token,omitempty"`
	ExtraEnv     map[string]string `json:"extra_env,omitempty"`

	// Plugins is the spec only: the snapshot a session loads from is rebuilt at adoption.
	Plugins []pluginspec.Entry `json:"plugins,omitempty"`

	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`

	// ClaudeSessionID is Claude Code's own session id as last reported by the engine ("" until
	// the first turn). It can change: a refused resume starts a fresh conversation.
	ClaudeSessionID string `json:"claude_session_id,omitempty"`
	Engine          string `json:"engine,omitempty"`
	CodexThreadID   string `json:"codex_thread_id,omitempty"`
	CurrentTurnID   string `json:"current_turn_id,omitempty"`
	// TurnActive: a turn was running when this was last written.
	TurnActive bool `json:"turn_active,omitempty"`
	// ToolRunning: one of the agent's own tool calls had started and not returned when this was
	// last written (a command half run, a question waiting for its answer) — not a subagent's
	// call, and not the Agent call that runs a subagent. nil in a record from a daemon that did
	// not keep it: unknown, read as "maybe".
	ToolRunning *bool `json:"tool_running,omitempty"`
	// OpenCalls are those tool calls (ToolRunning's), oldest first, each with what it ran: what the
	// agent is told was cut off. Absent in a record from a daemon that did not keep them.
	OpenCalls []OpenCall `json:"open_calls,omitempty"`
	// Waiting: the session was waiting for the person (status "waiting") when this was last
	// written. Absent (false) in a record from a daemon that did not keep it.
	Waiting bool `json:"waiting,omitempty"`
	// AutoContinues counts the times recovery continued a cut-off turn by itself since a turn
	// last finished. It is what stops a turn that restarts the daemon from being re-run forever.
	AutoContinues int `json:"auto_continues,omitempty"`
	// OpenTasks and OpenSubagents are the background tasks and the subagents that were running
	// when this was last written: what a restart cuts off. The driver's own lists of them are in
	// memory only; adoption reports these stopped and tells the agent about them.
	OpenTasks     []agent.BackgroundTaskStartedPayload `json:"open_tasks,omitempty"`
	OpenSubagents []OpenSubagent                       `json:"open_subagents,omitempty"`
	// KnownSubagents is a bounded ownership allowlist, including completed native children.
	KnownSubagents []OpenSubagent `json:"known_subagents,omitempty"`
	// EnginePID / EngineStarted identify the engine process serving the session: its pid and its
	// start time as `ps -o lstart=` prints it. Together they survive pid reuse.
	EnginePID     int    `json:"engine_pid,omitempty"`
	EngineStarted string `json:"engine_started,omitempty"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// OpenCall is one of the agent's own tool calls that started and has not returned, as a record
// keeps it.
type OpenCall struct {
	CallID string `json:"call_id"`
	Tool   string `json:"tool"`
	// Detail is what it ran, cut short: a command and its description, a file, a pattern.
	Detail string `json:"detail,omitempty"`
	// Destructive: its full command matched restartDenyList when it started. A session cut off in
	// such a call is paused after a restart, never continued by itself.
	Destructive bool `json:"destructive,omitempty"`
	// Checked: Destructive was decided from the command itself (a record written since the
	// deny-list). Without it (an older record) only Detail is left to match.
	Checked bool `json:"checked,omitempty"`
}

// destructive reports whether the call is one a restart must not let the agent pick up by
// itself. A current record decided it from the command alone, never its description; an older
// one has only Detail.
func (c OpenCall) destructive() bool {
	if c.Checked {
		return c.Destructive
	}
	return strings.EqualFold(c.Tool, "Bash") && destructiveCommand(c.Detail)
}

// restartDenyList is the deterministic list of commands that, cut off by a daemon restart, pause
// the session instead of letting it continue by itself (docs: install/desktop/DAEMON.md). Matched
// on the full command, case-insensitively: a force push, a recursive forced delete, a database
// migration, a deploy (helm, kubectl apply/delete), the installer or a service restart (it may
// be what restarted the daemon), SQL that drops or truncates.
var restartDenyList = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bgit\b[^;&|\n]*\bpush\b[^;&|\n]*(\s--force(-with-lease)?\b|\s-[a-z]*f[a-z]*\b|\s\+\S)`),
	// rm recursive and forced, flags in any order and anywhere among its arguments (rm x -rf,
	// rm --recursive --force, rm -r x -f): see rmRecursiveForced.
	regexp.MustCompile(`(?i)\bgit\b[^;&|\n]*\breset\b[^;&|\n]*\s--hard\b`),
	regexp.MustCompile(`(?i)\bgit\b[^;&|\n]*\bclean\b[^;&|\n]*\s(-[a-z]*f[a-z]*|--force)\b`),
	regexp.MustCompile(`(?i)\bterraform\b[^;&|\n]*\b(apply|destroy)\b`),
	regexp.MustCompile(`(?i)\bmigrat(e|es|ed|ion|ions)\b|\b(alembic|goose|flyway|liquibase)\b`),
	regexp.MustCompile(`(?i)\b(re)?deploy|\bhelm\s+(install|upgrade|uninstall|delete|rollback)\b|\bkubectl\b[^;&|\n]*\b(apply|delete)\b`),
	daemonCommand,
	regexp.MustCompile(`(?i)\bdrop\s+(table|database|schema|index|view|owned)\b|\btruncate\b|\bdelete\s+from\b`),
}

// daemonCommand is the deny-list's entry for a command that can stop or restart the daemon
// itself: the installer, blerg-up.sh, a service restart or stop. A restart notice names it as the
// only kind of command that can have caused an unplanned restart (restartCauseWords).
var daemonCommand = regexp.MustCompile(`(?i)\binstall\.sh\b|\bblerg-up\.sh\b|\bsystemctl\b[^;&|\n]*\b(restart|stop|kill)\b|\blaunchctl\b[^;&|\n]*\b(kickstart|stop|unload|bootout)\b|\bservice\s+\S+\s+(restart|stop)\b`)

// rmSegment finds each rm invocation and its arguments, up to the next command separator.
var rmSegment = regexp.MustCompile(`(?i)(^|[\s;&|(])rm(\s[^;&|\n]*)?`)

// rmRecursiveForced reports whether a command runs rm both recursively and forced, whatever the
// order and place of its flags: -rf, -fr, -Rf, -r … -f, x -rf, --recursive --force.
func rmRecursiveForced(cmd string) bool {
	for _, m := range rmSegment.FindAllStringSubmatch(cmd, -1) {
		var r, f bool
		for _, arg := range strings.Fields(m[2]) {
			if arg == "--" {
				break // what follows are file names
			}
			switch {
			case arg == "--recursive":
				r = true
			case arg == "--force":
				f = true
			case strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--"):
				r = r || strings.ContainsAny(arg, "rR")
				f = f || strings.Contains(arg, "f")
			}
		}
		if r && f {
			return true
		}
	}
	return false
}

// destructiveCommand reports whether a command matches restartDenyList.
func destructiveCommand(cmd string) bool {
	if rmRecursiveForced(cmd) {
		return true
	}
	for _, re := range restartDenyList {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}

// destructiveCutOff reports whether one of the record's open calls is on restartDenyList.
func destructiveCutOff(rec AgentRecord) bool {
	for _, c := range rec.OpenCalls {
		if c.destructive() {
			return true
		}
	}
	return false
}

// asksPerson reports whether the call is a question for the person: `blerg-runner ask` (it blocks
// until they answer) or Claude Code's own AskUserQuestion.
func (c OpenCall) asksPerson() bool {
	return c.Tool == "AskUserQuestion" || strings.EqualFold(c.Tool, "Bash") && ccAskCommand.MatchString(c.Detail)
}

// ccAskCommand finds `blerg-runner ask` in a command.
var ccAskCommand = regexp.MustCompile(`(^|[^\w-])blerg-runner\s+ask\b`)

// waitsOnPerson reports whether a record says its session was waiting for the person: its status
// was "waiting" (a permission prompt, a native question), or one of its open calls is a question.
// A record from a daemon that kept neither says no; the server's open asks are asked as well
// (AgentHost.NoteOpenAsks).
func waitsOnPerson(rec AgentRecord) bool {
	if rec.Waiting {
		return true
	}
	for _, c := range rec.OpenCalls {
		if c.asksPerson() {
			return true
		}
	}
	return false
}

// openCallOf is the record's entry for a call: its tool and what it ran.
func openCallOf(callID, tool string, input json.RawMessage) OpenCall {
	c := OpenCall{CallID: callID, Tool: tool, Detail: callDetail(input)}
	if strings.EqualFold(tool, "Bash") {
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) == nil {
			// The whole command, not the cut Detail, and never the description.
			c.Destructive, c.Checked = destructiveCommand(in.Command), true
		}
	}
	return c
}

// callDetail says what a call ran, in at most 200 characters: a command (with its description),
// else the first of the fields that name what it worked on.
func callDetail(input json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	str := func(k string) string {
		v, _ := in[k].(string)
		return strings.Join(strings.Fields(v), " ") // one line: it is listed in a notice
	}
	if cmd := str("command"); cmd != "" {
		if d := str("description"); d != "" {
			return cutRunes(cmd, 160) + " (" + cutRunes(d, 60) + ")"
		}
		return cutRunes(cmd, 200)
	}
	for _, k := range []string{"file_path", "notebook_path", "path", "url", "pattern", "query", "description", "prompt"} {
		if v := str(k); v != "" {
			return cutRunes(v, 200)
		}
	}
	return ""
}

// OpenSubagent is a subagent that started and has not finished, as a record keeps it.
type OpenSubagent struct {
	ChildID   string `json:"child_id"` // the Agent call that started it
	AgentType string `json:"agent_type,omitempty"`
	Model     string `json:"model,omitempty"`
	Summary   string `json:"summary,omitempty"` // its description
	// AgentID is Claude Code's id of a background subagent: a message sent to it resumes it.
	// "" for a subagent that ran in the foreground (its call had not returned).
	AgentID string `json:"agent_id,omitempty"`
	// Background says, for one with an AgentID, whether it ran in the background (a resumed run of
	// a foreground subagent did not). nil in a record older than the field: a subagent with an
	// AgentID was then always a background one.
	Background *bool `json:"background,omitempty"`
	// CumulativeUsage is the latest native total, used as a resumed run baseline.
	CumulativeUsage *agent.Usage `json:"cumulative_usage,omitempty"`
}

// ranInBackground is Background, true when the record predates it.
func (o OpenSubagent) ranInBackground() bool {
	return o.Background == nil || *o.Background
}

// AgentRecordsDir is where records live under stateDir ("" for "").
func AgentRecordsDir(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, agentRecordsSubdir)
}

func agentRecordPath(dir, id string) string {
	return filepath.Join(dir, safeFileName(id)+".json")
}

// secureAgentRecordsDir makes sure dir is a real directory, owned by the daemon's user and
// closed to everyone else, before a record is read from or written to it. With create, it is
// made when missing; without, a missing directory is exists=false and no error.
func secureAgentRecordsDir(dir string, create bool) (exists bool, err error) {
	if dir == "" {
		return false, errors.New("no state directory")
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if !create {
			return false, nil
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return false, err
		}
		if info, err = os.Lstat(dir); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	if err := checkPrivateDir(dir, info); err != nil {
		return false, err
	}
	return true, nil
}

// agentRecordTmpSeq makes every temp file name unique, so two writers can never clobber one
// another's half-written file.
var agentRecordTmpSeq atomic.Uint64

func writeAgentRecord(dir string, rec AgentRecord) error {
	if _, err := secureAgentRecordsDir(dir, true); err != nil {
		return err
	}
	rec.Version = agentRecordVersion
	rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(rec, "", "\t") //nolint:gosec // the record must carry the session token to host the session again; the file is 0600 in a daemon-private 0700 directory
	if err != nil {
		return err
	}
	final := agentRecordPath(dir, rec.SessionID)
	tmp := fmt.Sprintf("%s.%d.%d.tmp", final, os.Getpid(), agentRecordTmpSeq.Add(1))
	if err := writeFileSynced(tmp, data); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// writeFileSynced writes a new 0600 file and flushes it to disk before returning, so the rename
// that follows never publishes a file a power loss could leave empty.
func writeFileSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path is a temp name inside the daemon-private records directory
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// LockAgentRecords takes the records directory for this process, for as long as it lives, and
// reports whether it got it. Records are acted on (a session is hosted again, a leftover engine
// is stopped), so two daemons run by the same user must never both use them: the second one — a
// build started by hand, a daemon for another server — would otherwise stop the first one's
// engines and host its sessions. A daemon that does not get the lock keeps no records and
// recovers nothing. The lock is an flock on a file in the directory; it is released when the
// process exits, however it exits.
func LockAgentRecords(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := secureAgentRecordsDir(dir, true); err != nil {
		log.Printf("agent records: %v — agent sessions will not survive a daemon restart", err)
		return false
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // a fixed name inside the daemon-private records directory
	if err != nil {
		log.Printf("agent records: lock: %v — agent sessions will not survive a daemon restart", err)
		return false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		log.Printf("agent records: %s is in use by another daemon process — this one keeps no recovery records", dir)
		return false
	}
	agentRecordsLock = f // held (and kept from the garbage collector) for the life of the process
	return true
}

var agentRecordsLock *os.File

func deleteAgentRecord(dir, id string) {
	if dir == "" || id == "" {
		return
	}
	if err := os.Remove(agentRecordPath(dir, id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("agent record %s: delete: %v", id, err)
	}
}

// listAgentRecords reads every usable record in dir. A record is acted on (adoption hosts the
// session it describes), so one that is not a regular file owned by the daemon's user, does not
// parse, is of another version, or is not named after its own session id is skipped and removed.
// Leftover temp files are removed too.
func listAgentRecords(dir string) []AgentRecord {
	exists, err := secureAgentRecordsDir(dir, false)
	if err != nil {
		log.Printf("agent records: %v — none are recovered", err)
		return nil
	}
	if !exists {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("agent records: %v", err)
		return nil
	}
	var out []AgentRecord
	for _, e := range entries {
		name := e.Name()
		p := filepath.Join(dir, name)
		if strings.HasSuffix(name, ".tmp") {
			_ = os.Remove(p)
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() || !ownedByDaemon(info) {
			log.Printf("agent records: %s is not a file owned by the daemon's user — ignored", p)
			continue
		}
		data, err := os.ReadFile(p) //nolint:gosec // p is a regular file owned by the daemon in its private records directory
		var rec AgentRecord
		if err == nil {
			err = json.Unmarshal(data, &rec)
		}
		if err == nil && (rec.Version != agentRecordVersion || rec.SessionID == "" || safeFileName(rec.SessionID)+".json" != name) {
			err = errors.New("not a record of this version for the session it is named after")
		}
		if err != nil {
			log.Printf("agent records: %s: %v — removed", p, err)
			_ = os.Remove(p)
			continue
		}
		out = append(out, rec)
	}
	return out
}

// ─── the live record of a hosted session ─────────────────────────────────────

// recordKeeper holds one hosted session's record and rewrites the file on every change. A nil
// keeper (a session that is not recoverable, or a host with no records directory) accepts every
// call and does nothing.
type recordKeeper struct {
	dir    string
	frozen *atomic.Bool // the host's: set when the daemon is stopping

	mu      sync.Mutex
	rec     AgentRecord
	deleted bool
	// lastTurnErrored: the latest turn_done had stop reason "error" (see setTurnActive).
	lastTurnErrored bool
	// calls are the agent's own tool calls that have started and not returned (ToolRunning,
	// OpenCalls), oldest first.
	calls []OpenCall
	// status is the latest status the session reported.
	status string
}

func newRecordKeeper(dir string, frozen *atomic.Bool, rec AgentRecord) *recordKeeper {
	if rec.CreatedAt == "" {
		rec.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if rec.ToolRunning == nil {
		rec.ToolRunning = new(bool)
	}
	k := &recordKeeper{dir: dir, frozen: frozen, rec: rec, calls: rec.OpenCalls}
	k.update(func(*AgentRecord) bool { return true })
	return k
}

// update applies change and writes the record when change reports that something differs.
// Once the daemon is stopping nothing is written: the engine dies with the daemon, the driver
// then reports the turn over, and that must not erase the "a turn was running" recovery needs.
func (k *recordKeeper) update(change func(*AgentRecord) bool) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.deleted || (k.frozen != nil && k.frozen.Load()) {
		return
	}
	if !change(&k.rec) {
		return
	}
	if err := writeAgentRecord(k.dir, k.rec); err != nil {
		log.Printf("agent record %s: write: %v (the session would not survive a daemon restart)", k.rec.SessionID, err)
	}
}

// delete removes the file for good: the session has ended. Unlike update it is not stopped by
// the freeze, since a kill is the person's decision whenever it lands.
func (k *recordKeeper) delete() {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.deleted = true
	deleteAgentRecord(k.dir, k.rec.SessionID)
}

// setTurnActive records that a turn started (on) or that the session went idle. Going idle
// clears it only when the turn that just ended ended by itself: a turn that ended in "error"
// because its engine died (which is also what a stopping daemon's engines do, moments before the
// daemon follows) was cut off, and stays recorded as such until a later turn finishes.
func (k *recordKeeper) setTurnActive(on bool) {
	if k == nil {
		return
	}
	k.update(func(r *AgentRecord) bool {
		if !on && k.lastTurnErrored {
			return false
		}
		if r.TurnActive == on {
			return false
		}
		r.TurnActive = on
		return true
	})
}

// turnEnded notes how the latest turn ended, for setTurnActive and for the auto-continue count.
// A turn that ends with background tasks still running does not reset the count: one of them may
// be what restarts the daemon (an install run in the background), and the work it cuts off must
// not be picked up again by itself on every start.
func (k *recordKeeper) turnEnded(stopReason string) {
	if k == nil {
		return
	}
	k.update(func(r *AgentRecord) bool {
		k.lastTurnErrored = stopReason == "error"
		if k.lastTurnErrored {
			return false
		}
		// A turn that ended by itself has no tool call of its own still running.
		changed := k.setCallsLocked(r, nil)
		if r.AutoContinues == 0 || len(r.OpenTasks) > 0 {
			return changed
		}
		r.AutoContinues = 0 // a turn ran to an outcome of its own: the next cut-off may be continued
		return true
	})
}

// setStatus records the session's status: "waiting" is a session waiting for the person, and a
// turn that starts (idle to running) starts with none of its own tool calls running — the ones a
// turn cut off by its engine's death never returned. A waiting session going back to running is
// the same turn.
func (k *recordKeeper) setStatus(status string) {
	if k == nil {
		return
	}
	k.update(func(r *AgentRecord) bool {
		prev := k.status
		k.status = status
		changed := false
		if prev != "running" && prev != "waiting" && status == "running" {
			changed = k.setCallsLocked(r, nil)
		}
		if waiting := status == "waiting"; r.Waiting != waiting {
			r.Waiting = waiting
			changed = true
		}
		return changed
	})
	k.setTurnActive(status == "running")
}

// toolCall notes that one of the agent's own tool calls started (open) or returned. A subagent's
// calls (parent != "") and the calls that run or message a subagent are not the agent's own: a
// restart cuts them off as subagents. Nor is a call that only waits on background work (ccWaitCall:
// reading a task's output, a sleep-poll): the agent is then waiting on that work.
func (k *recordKeeper) toolCall(callID, tool string, input json.RawMessage, parent string, open bool) {
	if k == nil || callID == "" || parent != "" {
		return
	}
	if open && (ccAgentTools[tool] || tool == ccSendTool || ccWaitCall(tool, input)) {
		return
	}
	k.update(func(r *AgentRecord) bool {
		at := -1
		for i, c := range k.calls {
			if c.CallID == callID {
				at = i
				break
			}
		}
		if open == (at >= 0) {
			return false
		}
		calls := make([]OpenCall, 0, len(k.calls)+1)
		for i, c := range k.calls {
			if i != at {
				calls = append(calls, c)
			}
		}
		if open {
			calls = append(calls, openCallOf(callID, tool, input))
		}
		return k.setCallsLocked(r, calls)
	})
}

// setCallsLocked replaces the open calls and reports whether the record changed. The caller holds
// k.mu (inside update).
func (k *recordKeeper) setCallsLocked(r *AgentRecord, calls []OpenCall) bool {
	if len(calls) == 0 {
		calls = nil
	}
	k.calls = calls
	running := len(calls) > 0
	if r.ToolRunning != nil && *r.ToolRunning == running && reflect.DeepEqual(r.OpenCalls, calls) {
		return false
	}
	r.ToolRunning = &running
	r.OpenCalls = calls
	return true
}

// setOpenWork records the background tasks and subagents running now (none: nil or empty).
func (k *recordKeeper) setOpenWork(tasks []agent.BackgroundTaskStartedPayload, subs []OpenSubagent) {
	if len(tasks) == 0 {
		tasks = nil
	}
	if len(subs) == 0 {
		subs = nil
	}
	k.update(func(r *AgentRecord) bool {
		if reflect.DeepEqual(r.OpenTasks, tasks) && reflect.DeepEqual(r.OpenSubagents, subs) {
			return false
		}
		r.OpenTasks, r.OpenSubagents = tasks, subs
		return true
	})
}

func (k *recordKeeper) setModel(model, effort string) {
	k.update(func(r *AgentRecord) bool {
		if r.Model == model && r.Effort == effort {
			return false
		}
		r.Model, r.Effort = model, effort
		return true
	})
}

func (k *recordKeeper) setClaudeSessionID(id string) {
	k.update(func(r *AgentRecord) bool {
		if r.ClaudeSessionID == id {
			return false
		}
		r.ClaudeSessionID = id
		return true
	})
}

// setEngine records that the engine process pid started serving the session, or (running false)
// that it went away — which only counts if it is still the recorded one, so a late report about
// an old process cannot erase its successor.
func (k *recordKeeper) setEngine(pid int, running bool) {
	if k == nil || pid <= 0 {
		return
	}
	if !running {
		k.update(func(r *AgentRecord) bool {
			if r.EnginePID != pid {
				return false
			}
			r.EnginePID, r.EngineStarted = 0, ""
			return true
		})
		return
	}
	started := processStartTime(pid)
	k.update(func(r *AgentRecord) bool {
		if r.EnginePID == pid && r.EngineStarted == started {
			return false
		}
		r.EnginePID, r.EngineStarted = pid, started
		return true
	})
}

// ─── a leftover engine process ───────────────────────────────────────────────

// processStartTime is pid's start time as `ps -o lstart=` prints it ("" when there is no such
// process). ps, not /proc: it answers the same on Linux and macOS. A var so tests can stub it.
var processStartTime = func(pid int) string {
	if pid <= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // fixed binary and flags; pid is an integer
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// leftoverKillGrace is how long a leftover engine gets between SIGTERM and SIGKILL.
var leftoverKillGrace = 2 * time.Second

// stopLeftoverEngine ends the engine process a record names, if it is still that process: alive,
// with the recorded start time. Under systemd the engine dies with the daemon's control group;
// run any other way it can outlive the daemon and keep writing to the conversation the new
// daemon is about to resume. A pid that now belongs to something else is left alone.
func stopLeftoverEngine(rec AgentRecord) bool {
	if rec.EnginePID <= 1 || rec.EngineStarted == "" || rec.EnginePID == os.Getpid() {
		return false
	}
	if processStartTime(rec.EnginePID) != rec.EngineStarted {
		return false
	}
	log.Printf("agent session %s: stopping the engine process (pid %d) its previous daemon left running", rec.SessionID, rec.EnginePID)
	_ = syscall.Kill(rec.EnginePID, syscall.SIGTERM)
	deadline := time.Now().Add(leftoverKillGrace)
	for time.Now().Before(deadline) {
		if processStartTime(rec.EnginePID) != rec.EngineStarted {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(rec.EnginePID, syscall.SIGKILL)
	return true
}

func (k *recordKeeper) setCurrentTurnID(id string) {
	if id == "" {
		return
	}
	k.update(func(r *AgentRecord) bool {
		if r.CurrentTurnID == id {
			return false
		}
		r.CurrentTurnID = id
		return true
	})
}

func (k *recordKeeper) setKnownSubagents(children []OpenSubagent) {
	if len(children) == 0 {
		children = nil
	}
	k.update(func(r *AgentRecord) bool {
		if reflect.DeepEqual(r.KnownSubagents, children) {
			return false
		}
		r.KnownSubagents = children
		return true
	})
}
