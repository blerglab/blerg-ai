// Package desktopupgrade upgrades an existing desktop install, keeping recovery
// state outside the checkout and refusing to migrate without a complete backup.
package desktopupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/releasegate"
)

var stable = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type Release struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Published  string `json:"published_at"`
}

func Select(releases []Release, tag string) (Release, error) {
	var best Release
	for _, r := range releases {
		if !stable.MatchString(r.Tag) || r.Draft || r.Prerelease || r.Published == "" || (tag != "" && tag != r.Tag) {
			continue
		}
		if best.Tag == "" || newer(r.Tag, best.Tag) {
			best = r
		}
	}
	if best.Tag == "" {
		return Release{}, errors.New("no matching published stable release")
	}
	return best, nil
}
func newer(a, b string) bool {
	aa, bb := stable.FindStringSubmatch(a), stable.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		x, _ := new(big.Int).SetString(aa[i], 10)
		y, _ := new(big.Int).SetString(bb[i], 10)
		if c := x.Cmp(y); c != 0 {
			return c > 0
		}
	}
	return false
}

type Operation struct {
	MigrationsStarted bool      `json:"migrations_started"`
	MigrationMarker   string    `json:"migration_marker"`
	InstallLog        string    `json:"install_log"`
	OldDaemonPID      int       `json:"old_daemon_pid"`
	InstallStarted    time.Time `json:"install_started"`
	ID                string    `json:"id"`
	Phase             string    `json:"phase"`
	OldCommit         string    `json:"old_commit"`
	OldBranch         string    `json:"old_branch"`
	NewCommit         string    `json:"new_commit"`
	Tag               string    `json:"tag"`
	Backup            string    `json:"backup"`
	Updated           time.Time `json:"updated"`
	Recovery          string    `json:"recovery"`
}
type Upgrader struct {
	Verify                         func(context.Context, Operation) error
	Root, State, Home, DaemonState string
	Run                            releasegate.Command
	Releases                       func(context.Context) ([]Release, error)
	Dump                           func(context.Context, string, io.Writer) error
	Progress                       func(string)
}

func (u Upgrader) lock() (func(), error) {
	if err := os.MkdirAll(u.State, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(u.State)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("upgrade state must be a private directory")
	}
	path := filepath.Join(u.State, "lock")
	if err = os.Mkdir(path, 0700); err != nil {
		return nil, errors.New("upgrade locked; inspect operation.json and recover any interrupted upgrade before removing the lock")
	}
	return func() { _ = os.Remove(path) }, nil
}
func (u Upgrader) save(op Operation) error {
	op.Updated = time.Now().UTC()
	b, err := json.MarshalIndent(op, "", "  ")
	if err != nil {
		return err
	}
	return privateWrite(filepath.Join(u.State, "operation.json"), b)
}
func privateWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
func (u Upgrader) phase(op *Operation, phase string) error {
	op.Phase = phase
	if err := u.save(*op); err != nil {
		return err
	}
	if u.Progress != nil {
		u.Progress(phase)
	}
	return nil
}
func (u Upgrader) clean(ctx context.Context) error {
	b, err := u.Run(ctx, "git", "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) != 0 {
		return errors.New("checkout has local changes; commit or move tracked, staged, and untracked files first")
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "BISECT_LOG", "index.lock"} {
		b, err = u.Run(ctx, "git", "rev-parse", "--git-path", name)
		if err != nil {
			return err
		}
		path := strings.TrimSpace(string(b))
		if !filepath.IsAbs(path) {
			path = filepath.Join(u.Root, path)
		}
		if _, err = os.Stat(path); err == nil {
			return errors.New("finish the active Git operation before upgrading")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
func (u Upgrader) resolve(ctx context.Context, tag, ref string) (string, error) {
	if _, err := u.Run(ctx, "git", "fetch", "--no-tags", "origin", "refs/tags/"+tag+":"+ref); err != nil {
		return "", err
	}
	b, err := u.Run(ctx, "git", "rev-parse", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
func (u Upgrader) daemon(ctx context.Context, start bool) error {
	var args []string
	name := "systemctl"
	switch runtime.GOOS {
	case "darwin":
		name = "launchctl"
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		if start {
			args = []string{"bootstrap", domain, filepath.Join(u.Home, "Library/LaunchAgents/dev.blerg.runner-daemon.plist")}
		} else {
			args = []string{"bootout", domain + "/dev.blerg.runner-daemon"}
		}
	case "linux":
		action := "stop"
		if start {
			action = "start"
		}
		args = []string{"--user", action, "blerg-runner-daemon.service"}
	default:
		return errors.New("desktop upgrade requires Linux or macOS host services")
	}
	_, err := u.Run(ctx, name, args...)
	return err
}
func (u Upgrader) compose(ctx context.Context, args ...string) ([]byte, error) {
	return u.Run(ctx, "docker", append([]string{"compose"}, args...)...)
}
func (u Upgrader) noWork(ctx context.Context) error {
	b, err := u.compose(ctx, "exec", "-T", "postgres", "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "blerg", "-d", "blerg_runner", "-Atc", "SELECT count(*) FROM sessions WHERE status NOT IN ('ended','stopped','error')")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(b)) != "0" {
		return errors.New("stop all live agent sessions before upgrading")
	}
	b, err = u.Run(ctx, filepath.Join(u.Root, "bin/blerg-runner-daemon"), "status", "--open-work")
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) != 0 {
		return errors.New("finish or stop agent work before upgrading")
	}
	return nil
}
func copyPrivate(from, to string, optional bool) error {
	info, err := os.Lstat(from)
	if optional && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("required backup settings are unavailable")
	}
	if !info.Mode().IsRegular() {
		return errors.New("backup settings must be regular files")
	}
	// #nosec G304 -- Paths come from fixed install/state locations, never an HTTP request.
	raw, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return privateWrite(to, raw)
}
func (u Upgrader) backup(ctx context.Context, op *Operation) error {
	dir, err := os.MkdirTemp(u.State, "backup-")
	if err != nil {
		return err
	}
	op.Backup = dir
	if err = u.phase(op, "backing_up"); err != nil {
		return err
	}
	for _, db := range []string{"blerg_core", "blerg_board", "blerg_runner"} {
		path := filepath.Join(dir, db+".sql")
		// #nosec G304 -- Exclusive files inside the private backup directory just created.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		err = u.Dump(ctx, db, f)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			return errors.New("empty database backup")
		}
		// #nosec G304 -- Fixed dump filename inside the private backup directory just created.
		f, err = os.Open(path)
		if err != nil {
			return err
		}
		size := min(info.Size(), int64(4096))
		tail := make([]byte, size)
		_, err = f.ReadAt(tail, info.Size()-size)
		_ = f.Close()
		if err != nil || !completeDump(tail) {
			return errors.New("database backup has no completion trailer")
		}
	}
	if err = copyPrivate(filepath.Join(u.Root, "install/desktop/.env"), filepath.Join(dir, "env"), false); err != nil {
		return err
	}
	for _, pair := range [][2]string{{"settings.json", "daemon-settings.json"}, {"desktop-credential.json", "desktop-credential.json"}} {
		if err = copyPrivate(filepath.Join(u.DaemonState, pair[0]), filepath.Join(dir, pair[1]), true); err != nil {
			return err
		}
	}
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	// Written last. Incomplete directories must never be used as restore points.
	return privateWrite(filepath.Join(dir, "complete.json"), data)
}

func (u Upgrader) Upgrade(ctx context.Context, tag string) (result error) {
	if err := u.clean(ctx); err != nil {
		return err
	}
	unlock, err := u.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if raw, e := os.ReadFile(filepath.Join(u.State, "operation.json")); e == nil {
		var old Operation
		if json.Unmarshal(raw, &old) != nil || (old.Phase != "succeeded" && old.Phase != "failed") {
			return errors.New("previous upgrade requires recovery; inspect operation.json before starting another")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	releases, err := u.Releases(ctx)
	if err != nil {
		return err
	}
	release, err := Select(releases, tag)
	if err != nil {
		return err
	}
	old, err := u.Run(ctx, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	branch, err := u.Run(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	op := Operation{ID: fmt.Sprintf("%d", time.Now().UnixNano()), OldCommit: strings.TrimSpace(string(old)), OldBranch: strings.TrimSpace(string(branch)), Tag: release.Tag, Recovery: "Do not run old binaries against migrated databases. Stop writers, restore all three dumps with matching env and daemon settings, then reinstall old_commit. See docs/backup-and-restore.md. Restore discards writes after the backup; retained workspaces and runner data volumes are kept."}
	rawConnection, readErr := os.ReadFile(filepath.Join(u.DaemonState, "connection.json"))
	if readErr != nil {
		return errors.New("existing daemon connection record is required for an upgrade")
	}
	var oldConnection struct{ PID int }
	if json.Unmarshal(rawConnection, &oldConnection) != nil || oldConnection.PID <= 0 {
		return errors.New("invalid existing daemon connection record")
	}
	op.OldDaemonPID = oldConnection.PID
	if err = u.phase(&op, "validating"); err != nil {
		return err
	}
	op.MigrationMarker = filepath.Join(u.State, "migration-"+op.ID)
	op.InstallLog = filepath.Join(u.State, "install-"+op.ID+".log")
	changed, stopped, daemonStopped := false, false, false
	defer func() {
		if result == nil {
			return
		}
		if _, e := os.Stat(op.MigrationMarker); e == nil {
			op.MigrationsStarted = true
		}
		if op.MigrationsStarted {
			op.Phase = "recovery_required"
		} else {
			op.Phase = "failed"
			recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			if changed {
				ref := op.OldBranch
				if ref == "HEAD" {
					ref = op.OldCommit
				}
				if _, e := u.Run(recoveryCtx, "git", "checkout", ref); e != nil {
					op.Phase = "recovery_required"
					_ = u.save(op)
					return
				}
			}
			if stopped {
				if _, e := u.compose(recoveryCtx, "start", "blerg-core", "blerg-board", "blerg-runner"); e != nil {
					op.Phase = "recovery_required"
				}
			}
			if daemonStopped {
				if e := u.daemon(recoveryCtx, true); e != nil {
					op.Phase = "recovery_required"
				}
			}
		}
		if e := u.save(op); e != nil {
			result = fmt.Errorf("%w; could not save recovery state", result)
		}
	}()
	ref := "refs/blerg-upgrades/" + op.ID
	op.NewCommit, err = u.resolve(ctx, release.Tag, ref)
	if err != nil {
		return err
	}
	currentVersion, readErr := os.ReadFile(filepath.Join(u.Root, "VERSION"))
	if readErr != nil {
		return errors.New("checkout VERSION is unavailable")
	}
	currentTag := "v" + strings.TrimSpace(string(currentVersion))
	if !stable.MatchString(currentTag) {
		return errors.New("checkout VERSION is not a stable version")
	}
	if newer(currentTag, op.Tag) {
		return errors.New("downgrades require restoring matching databases; use the restore runbook")
	}
	if _, e := u.Run(ctx, "git", "merge-base", "--is-ancestor", op.OldCommit, op.NewCommit); e != nil {
		return errors.New("selected release is not a descendant of this checkout; restore matching databases for a downgrade")
	}
	if op.NewCommit == op.OldCommit {
		return u.phase(&op, "succeeded")
	}
	identity := filepath.Join(u.State, release.Tag+".commit")
	// #nosec G304 -- Canonical stable tag filename inside the private state directory.
	prior, e := os.ReadFile(identity)
	if e == nil && string(prior) != op.NewCommit {
		return errors.New("published release tag moved; refusing upgrade")
	}
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e = privateWrite(identity, []byte(op.NewCommit)); e != nil {
		return e
	}
	installer, err := u.Run(ctx, "git", "show", op.NewCommit+":install/desktop/blerg-up.sh")
	if err != nil || !strings.Contains(string(installer), "BLERG_UPGRADE_MIGRATION_MARKER") {
		return errors.New("selected installer does not support safe migration tracking")
	}
	if err = u.noWork(ctx); err != nil {
		return err
	}
	if err = u.phase(&op, "quiescing"); err != nil {
		return err
	}
	stopped = true // A failed stop can still have stopped some services.
	if _, err = u.compose(ctx, "stop", "blerg-core", "blerg-board", "blerg-runner"); err != nil {
		return err
	}
	// No server can admit new work after this point. Check races with the stop.
	if err = u.noWork(ctx); err != nil {
		return err
	}
	daemonStopped = true
	if err = u.daemon(ctx, false); err != nil {
		return err
	}
	if err = u.backup(ctx, &op); err != nil {
		return err
	}
	releases, err = u.Releases(ctx)
	if err != nil {
		return err
	}
	if _, err = Select(releases, release.Tag); err != nil {
		return err
	}
	current, err := u.resolve(ctx, release.Tag, ref+"-verify")
	if err != nil {
		return err
	}
	if current != op.NewCommit {
		return errors.New("published tag changed during backup")
	}
	if err = u.clean(ctx); err != nil {
		return err
	}
	op.InstallStarted = time.Now().UTC()
	if err = u.phase(&op, "installing"); err != nil {
		return err
	}
	changed = true // A failed checkout may have changed files; do not guess.
	if _, err = u.Run(ctx, "git", "checkout", "--detach", op.NewCommit); err != nil {
		return err
	}
	// No --yes: an unexpected active-work check in the installer must refuse.
	if _, err = u.Run(ctx, "env", "BLERG_VERSION="+strings.TrimPrefix(release.Tag, "v"), "BLERG_COMMIT="+op.NewCommit, "BLERG_UPGRADE_MIGRATION_MARKER="+op.MigrationMarker, "BLERG_UPGRADE_INSTALL_LOG="+op.InstallLog, "bash", "./blerg-up.sh", "--no-open"); err != nil {
		return err
	}
	if _, err = os.Stat(op.MigrationMarker); err != nil {
		op.MigrationsStarted = true
		return errors.New("installer did not confirm migration boundary; recovery required")
	}
	op.MigrationsStarted = true
	if err = u.phase(&op, "checking"); err != nil {
		return err
	}
	if err = u.check(ctx, op); err != nil {
		return err
	}
	if u.Verify != nil {
		if err = u.Verify(ctx, op); err != nil {
			return err
		}
	} else {
		if err = VerifyServices(ctx, u.Run, op); err != nil {
			return err
		}
		if err = VerifyDaemonBinary(u.Root, op); err != nil {
			return err
		}
	}
	if err = u.phase(&op, "succeeded"); err != nil {
		return err
	}
	if err = u.pruneBackups(op.Backup); err != nil && u.Progress != nil {
		u.Progress("upgrade succeeded; old backup cleanup failed")
	}
	return nil
}

// Executor returns a command runner anchored to the original checkout. The
// selected installer is invoked only after checkout and backup succeed.
func Executor(root, daemonState string) releasegate.Command {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		// #nosec G204 -- Internal fixed commands and structured arguments; no shell interpolation.
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = root
		if name != "git" {
			cmd.Dir = filepath.Join(root, "install/desktop")
		}
		cmd.Env = append(os.Environ(), "BLERG_RUNNER_DAEMON_STATE_DIR="+daemonState)
		// Never exclude the caller from the daemon's active-work refusal.
		for i := len(cmd.Env) - 1; i >= 0; i-- {
			if strings.HasPrefix(cmd.Env[i], "BLERG_RUNNER_SESSION_ID=") {
				cmd.Env = append(cmd.Env[:i], cmd.Env[i+1:]...)
			}
		}
		logPath := ""
		started := time.Now().UTC()
		if name == "env" {
			for _, arg := range args {
				if strings.HasPrefix(arg, "BLERG_UPGRADE_INSTALL_LOG=") {
					logPath = strings.TrimPrefix(arg, "BLERG_UPGRADE_INSTALL_LOG=")
				}
			}
		}
		redaction := "Subprocess stdout and stderr redacted in full to protect install credentials.\n"
		if logPath != "" {
			if err := privateWrite(logPath, []byte("Installer started at "+started.Format(time.RFC3339)+".\n"+redaction)); err != nil {
				return nil, errors.New("could not create private install log")
			}
		}
		b, err := cmd.Output()
		if logPath != "" {
			status := "completed"
			if err != nil {
				status = "failed"
			}
			code := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			}
			record := fmt.Sprintf("Installer %s. Started %s; finished %s; exit %d.\n%s", status, started.Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), code, redaction)
			if logErr := privateWrite(logPath, []byte(record)); logErr != nil {
				return nil, errors.New("could not save private install log")
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s failed (output withheld); inspect the private operation record", filepath.Base(name))
		}
		return b, nil
	}
}
func DumpExecutor(root string) func(context.Context, string, io.Writer) error {
	return func(ctx context.Context, db string, w io.Writer) error {
		// #nosec G204 -- Database names are the fixed three names from backup, passed without a shell.
		cmd := exec.CommandContext(ctx, "docker", "compose", "exec", "-T", "postgres", "pg_dump", "-U", "blerg", "-d", db)
		cmd.Dir = filepath.Join(root, "install/desktop")
		cmd.Stdout = w
		if err := cmd.Run(); err != nil {
			return errors.New("database backup failed; no checkout or migration was attempted")
		}
		return nil
	}
}
func (u Upgrader) check(ctx context.Context, op Operation) error {
	b, err := u.compose(ctx, "ps", "--format", "json", "blerg-core", "blerg-board", "blerg-runner")
	if err != nil {
		return err
	}
	// Compose emits either an array or one JSON object per line, depending on version.
	var rows []struct{ Service, State string }
	if json.Unmarshal(b, &rows) != nil {
		dec := json.NewDecoder(strings.NewReader(string(b)))
		for {
			var row struct{ Service, State string }
			err = dec.Decode(&row)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return errors.New("invalid Compose health response")
			}
			rows = append(rows, row)
		}
	}
	live := map[string]bool{}
	for _, row := range rows {
		if row.State == "running" {
			live[row.Service] = true
		}
	}
	for _, name := range []string{"blerg-core", "blerg-board", "blerg-runner"} {
		if !live[name] {
			return errors.New("upgraded service is not running")
		}
	}
	// Installer health checks cover all services. Require a newly written,
	// accepted daemon connection as well (its own installer only warns on timeout).
	path := filepath.Join(u.DaemonState, "connection.json")
	info, err := os.Stat(path)
	if err != nil {
		return errors.New("daemon connection is unavailable")
	}
	// #nosec G304 -- Fixed connection.json inside the configured private daemon directory.
	raw, err := os.ReadFile(path)
	if err != nil {
		return errors.New("daemon connection is unreadable")
	}
	var conn struct {
		State string
		PID   int
	}
	if json.Unmarshal(raw, &conn) != nil || conn.State != "connected" || conn.PID <= 0 || conn.PID == op.OldDaemonPID || info.ModTime().Before(op.InstallStarted.Truncate(time.Second)) {
		return errors.New("new daemon has not confirmed its connection")
	}
	return nil
}

// completeDump accepts the optional pg_dump restriction terminator added in
// patched PostgreSQL clients, but rejects arbitrary output after the trailer.
func completeDump(tail []byte) bool {
	lines := strings.Split(strings.TrimSpace(string(tail)), "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[len(lines)-1], `\unrestrict `) {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && (strings.TrimSpace(lines[len(lines)-1]) == "" || strings.TrimSpace(lines[len(lines)-1]) == "--") {
		lines = lines[:len(lines)-1]
	}
	return len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "-- PostgreSQL database dump complete"
}

// Retain the current backup and the two most recent complete predecessors.
// Incomplete backups and recovery operations are never removed here.
func (u Upgrader) pruneBackups(current string) error {
	entries, err := os.ReadDir(u.State)
	if err != nil {
		return err
	}
	type backup struct {
		path    string
		updated time.Time
	}
	var complete []backup
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "backup-") {
			continue
		}
		path := filepath.Join(u.State, entry.Name())
		if path == current {
			continue
		}
		// #nosec G304 -- Completion metadata under backup directories in private upgrade state.
		raw, e := os.ReadFile(filepath.Join(path, "complete.json"))
		var op Operation
		if e != nil || json.Unmarshal(raw, &op) != nil || op.OldCommit == "" {
			continue
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		complete = append(complete, backup{path, info.ModTime()})
	}
	sort.Slice(complete, func(i, j int) bool { return complete[i].updated.After(complete[j].updated) })
	for i := 2; i < len(complete); i++ {
		if err := os.RemoveAll(complete[i].path); err != nil {
			return err
		}
	}
	return nil
}
