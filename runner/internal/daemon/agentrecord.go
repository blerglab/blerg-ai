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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
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
	// TurnActive: a turn was running when this was last written.
	TurnActive bool `json:"turn_active,omitempty"`
	// AutoContinues counts the times recovery continued a cut-off turn by itself since a turn
	// last finished. It is what stops a turn that restarts the daemon from being re-run forever.
	AutoContinues int `json:"auto_continues,omitempty"`
	// EnginePID / EngineStarted identify the engine process serving the session: its pid and its
	// start time as `ps -o lstart=` prints it. Together they survive pid reuse.
	EnginePID     int    `json:"engine_pid,omitempty"`
	EngineStarted string `json:"engine_started,omitempty"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
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
}

func newRecordKeeper(dir string, frozen *atomic.Bool, rec AgentRecord) *recordKeeper {
	if rec.CreatedAt == "" {
		rec.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	k := &recordKeeper{dir: dir, frozen: frozen, rec: rec}
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
func (k *recordKeeper) turnEnded(stopReason string) {
	if k == nil {
		return
	}
	k.update(func(r *AgentRecord) bool {
		k.lastTurnErrored = stopReason == "error"
		if k.lastTurnErrored || r.AutoContinues == 0 {
			return false
		}
		r.AutoContinues = 0 // a turn ran to an outcome of its own: the next cut-off may be continued
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
