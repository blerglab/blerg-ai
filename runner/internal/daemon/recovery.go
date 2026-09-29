package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// recordSchemaVersion is incremented whenever the SessionRecord format changes
// in a backward-incompatible way. Version 2 added SessionToken/ExtraEnv;
// version-1 records still load (fields zero) and recover without a
// messaging token.
const recordSchemaVersion = 2

// SessionRecord holds the information needed to recreate a Claude session after
// a hard restart (tmux server death). It is persisted as JSON in
// <ReposRoot>/.blerg-runner/sessions/<sessionID>.json.
type SessionRecord struct {
	Version                    int    `json:"version"`
	SessionID                  string `json:"session_id"`
	Repo                       string `json:"repo"`
	ProjectPath                string `json:"project_path"`
	Model                      string `json:"model,omitempty"`
	Effort                     string `json:"effort,omitempty"`
	DangerouslySkipPermissions bool   `json:"dangerously_skip_permissions,omitempty"`
	Sandbox                    bool   `json:"sandbox,omitempty"`
	// Engine: "" (Claude Code) | "codex" | "hermes". A recovered "codex" or
	// "hermes" tmux session always starts fresh (no stored thread/session id
	// to resume for the interactive case) — see recoverSession.
	Engine        string `json:"engine,omitempty"`
	Title         string `json:"title,omitempty"`
	InitialPrompt string `json:"initial_prompt,omitempty"`
	// Assist fields: when Assist is true the recovered session gets board env
	// vars (same as on original spawn).
	Assist     bool   `json:"assist,omitempty"`
	BoardID    string `json:"board_id,omitempty"`
	TicketID   string `json:"ticket_id,omitempty"`
	BoardToken string `json:"board_token,omitempty"`
	// SessionToken / ExtraEnv: the per-session messaging token and the
	// caller-supplied env the session was spawned with, reinjected on
	// recovery. The record file is 0600 in the daemon user's repos root —
	// the same exposure BoardToken already has.
	SessionToken string            `json:"session_token,omitempty"`
	ExtraEnv     map[string]string `json:"extra_env,omitempty"`
	CreatedAt    string            `json:"created_at"`
	UpdatedAt    string            `json:"updated_at"`
}

// recordsDir returns the directory that holds all session records for the
// given repos root.
func recordsDir(reposRoot string) string {
	return filepath.Join(reposRoot, ".blerg-runner", "sessions")
}

// recordPath returns the full file path for a session record.
func recordPath(reposRoot, id string) string {
	return filepath.Join(recordsDir(reposRoot), id+".json")
}

// ownedByDaemon reports whether info belongs to the user the daemon runs as.
// A var so tests can play "another user owns this" without root.
var ownedByDaemon = func(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

// secureRecordsDir makes sure <reposRoot>/.blerg-runner and its sessions
// directory are private to the daemon's user before any record is read or
// written there. Records carry tokens (SessionToken, BoardToken), the
// initial prompt, extra env and the skip-permissions flag, and recovery acts
// on whatever it finds — so a directory someone else made (or can write
// into) is never trusted:
//
//   - a symlink, a non-directory, or a directory owned by another user is
//     refused with an error (never adopted: MkdirAll is a silent no-op on
//     an existing directory, which is exactly how a pre-created one would
//     otherwise be used as is);
//   - one of ours with group/other permission bits is tightened to 0700.
//
// With create, missing directories are made (0700); without it, a missing
// directory is reported as exists=false, not an error.
func secureRecordsDir(reposRoot string, create bool) (exists bool, err error) {
	if create {
		if err := os.MkdirAll(reposRoot, 0o750); err != nil {
			return false, err
		}
	}
	for _, d := range []string{filepath.Join(reposRoot, ".blerg-runner"), recordsDir(reposRoot)} {
		info, err := os.Lstat(d)
		if errors.Is(err, fs.ErrNotExist) {
			if !create {
				return false, nil
			}
			if err := os.Mkdir(d, 0o700); err != nil {
				return false, err
			}
			if info, err = os.Lstat(d); err != nil {
				return false, err
			}
		} else if err != nil {
			return false, err
		}
		if err := checkPrivateDir(d, info); err != nil {
			return false, err
		}
	}
	return true, nil
}

// checkPrivateDir refuses d unless it is a real directory owned by the
// daemon's user, and removes any group/other permission bits it has.
func checkPrivateDir(d string, info fs.FileInfo) error {
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a plain directory", d)
	}
	if !ownedByDaemon(info) {
		return fmt.Errorf("%s is owned by another user", d)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(d, 0o700); err != nil { //nolint:gosec // Chmod on a directory: 0700 (owner rwx) is the tightening; a dir needs the search bit
			return fmt.Errorf("%s is readable or writable by other users and can't be made private: %w", d, err)
		}
		log.Printf("recovery: tightened %s to 0700", d)
	}
	return nil
}

// writeSessionRecord persists rec to <reposRoot>/.blerg-runner/sessions/<id>.json
// atomically (write-then-rename). The directory is created on first use,
// and refused when it is not private to the daemon (secureRecordsDir).
func writeSessionRecord(reposRoot string, rec SessionRecord) error {
	if _, err := secureRecordsDir(reposRoot, true); err != nil {
		return err
	}

	data, err := json.MarshalIndent(rec, "", "\t") //nolint:gosec // the session record must carry the session token to resume the session; the file is 0600 in a daemon-private 0700 directory
	if err != nil {
		return err
	}

	tmp := recordPath(reposRoot, rec.SessionID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp) // best-effort: don't leave a partial temp file behind
		return err
	}
	return os.Rename(tmp, recordPath(reposRoot, rec.SessionID))
}

// readSessionRecord reads and unmarshals the record for the given session ID.
// Only a regular file owned by the daemon's user is read: a record is acted
// on (recovery recreates the session it describes), so one planted by
// anyone else is refused.
func readSessionRecord(reposRoot, id string) (SessionRecord, error) {
	p := recordPath(reposRoot, id)
	info, err := os.Lstat(p)
	if err != nil {
		return SessionRecord{}, err
	}
	if !info.Mode().IsRegular() || !ownedByDaemon(info) {
		return SessionRecord{}, fmt.Errorf("%s is not a file owned by the daemon's user", p)
	}
	data, err := os.ReadFile(p) //nolint:gosec // p is a record under the daemon-private records dir; Lstat above requires a regular file owned by the daemon
	if err != nil {
		return SessionRecord{}, err
	}
	var rec SessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return SessionRecord{}, err
	}
	return rec, nil
}

// deleteSessionRecord removes the record for id. If the file does not exist,
// the call is a no-op. Other errors are logged but not returned.
func deleteSessionRecord(reposRoot, id string) {
	err := os.Remove(recordPath(reposRoot, id))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("recovery: delete session record %s: %v", id, err)
	}
}

// listSessionRecords reads all well-formed session records from the sessions
// directory. Missing directory returns an empty slice. Corrupt files are
// logged and skipped.
func listSessionRecords(reposRoot string) []SessionRecord {
	if exists, err := secureRecordsDir(reposRoot, false); err != nil {
		log.Printf("recovery: not reading session records: %v", err)
		return []SessionRecord{}
	} else if !exists {
		return []SessionRecord{}
	}
	dir := recordsDir(reposRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Missing directory is not an error worth surfacing.
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("recovery: list session records: %v", err)
		}
		return []SessionRecord{}
	}

	recs := []SessionRecord{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		rec, err := readSessionRecord(reposRoot, id)
		if err != nil {
			log.Printf("recovery: skipping corrupt record %s: %v", e.Name(), err)
			continue
		}
		recs = append(recs, rec)
	}
	return recs
}

// migrateSessionRecords moves every session record from oldRoot to newRoot
// when the repos root changes. Records follow the root (they live under it),
// so without this a session started before the change would be forgotten by
// the next kill (its record left behind to resurrect it), by its natural exit
// and by hard-restart recovery. A record is removed from oldRoot only once
// it is written under newRoot. Best-effort: failures are logged.
func migrateSessionRecords(oldRoot, newRoot string) {
	if oldRoot == "" || filepath.Clean(oldRoot) == filepath.Clean(newRoot) {
		return
	}
	for _, rec := range listSessionRecords(oldRoot) {
		if err := writeSessionRecord(newRoot, rec); err != nil {
			log.Printf("recovery: move session record %s to the new repos root: %v", rec.SessionID, err)
			continue
		}
		deleteSessionRecord(oldRoot, rec.SessionID)
	}
}

// recordedSessionIDs returns just the session IDs of all persisted records.
func recordedSessionIDs(reposRoot string) []string {
	recs := listSessionRecords(reposRoot)
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, r.SessionID)
	}
	return ids
}

// transcriptExists reports whether a Claude transcript exists for the given
// session ID. Claude stores transcripts at
// $HOME/.claude/projects/<escaped-project-path>/<sessionID>.jsonl, but the
// directory name uses Claude's own path-escaping (more than just "/"→"-"), so
// rather than guess it we glob for <sessionID>.jsonl under any project dir.
// Session IDs are unique UUIDs, so a match is unambiguous — and this avoids the
// failure mode where a mis-guessed directory name reports "no transcript" for a
// session that has one, causing recovery to reuse an in-use --session-id.
func transcriptExists(sessionID string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	matches, err := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", sessionID+".jsonl"))
	return err == nil && len(matches) > 0
}

// recoverableRecords returns records whose session is neither live in tmux nor
// already managed by the daemon — i.e. abandoned by a hard restart.
func recoverableRecords(records []SessionRecord, liveIDs, managedIDs []string) []SessionRecord {
	skip := make(map[string]struct{})
	for _, id := range liveIDs {
		skip[id] = struct{}{}
	}
	for _, id := range managedIDs {
		skip[id] = struct{}{}
	}
	var out []SessionRecord
	for _, r := range records {
		if _, ok := skip[r.SessionID]; !ok {
			out = append(out, r)
		}
	}
	return out
}
