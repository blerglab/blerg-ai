package daemon

import (
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sandboxMountRefusal is the last check before a folder is bind-mounted, writable, at /workspace:
// a sandbox mounts nothing but this session's own clone (sandboxclone.go), with the daemon's record
// of it. Every session start makes the clone first; anything else that reaches Docker (a plain
// folder, a folder with repositories below it, a bare repository, a checkout, a recovery record an
// older daemon wrote) is refused, so no folder of the person's ever enters a container writable.
func sandboxMountRefusal(hostProjectPath, sessionID string) error {
	if !isSandboxClone(hostProjectPath, sessionID) {
		return errors.New("a sandbox mounts only a clone of its own, and this folder is not this session's clone; the session was not started")
	}
	if _, err := loadSandboxClone(sessionID); err != nil {
		return fmt.Errorf("this session's clone has no valid record (%w); the session was not started", err)
	}
	return nil
}

func safeSandboxGitInfo(info os.FileInfo, dir bool) bool {
	return info != nil && ownedByDaemon(info) && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0002 == 0 && ((dir && info.IsDir()) || (!dir && info.Mode().IsRegular()))
}

// safeSandboxGitTarget reports whether path may be a mount target in the container: not one of
// the image's own reserved paths.
func safeSandboxGitTarget(path string) bool {
	for _, reserved := range []string{"/", "/workspace", "/home/agent", "/etc", "/usr", "/bin", "/sbin", "/proc", "/sys", "/dev", "/run", "/root", "/var/run"} {
		if path == reserved || (reserved != "/" && strings.HasPrefix(path, reserved+string(filepath.Separator))) {
			return false
		}
	}
	return true
}

func sandboxBindMount(source, target string) string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"type=bind", "src=" + source, "dst=" + target})
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}
