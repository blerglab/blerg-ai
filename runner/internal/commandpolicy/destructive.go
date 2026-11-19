package commandpolicy

import (
	"regexp"
	"strings"
)

// RestartDenyList is the deterministic list of commands that, cut off by a daemon restart, pause
// the session instead of letting it continue by itself (docs: install/desktop/DAEMON.md). Matched
// on the full command, case-insensitively: a force push, a recursive forced delete, a database
// migration, a deploy (helm, kubectl apply/delete), the installer or a service restart (it may
// be what restarted the daemon), SQL that drops or truncates.
var RestartDenyList = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bgit\b[^;&|\n]*\bpush\b[^;&|\n]*(\s--force(-with-lease)?\b|\s-[a-z]*f[a-z]*\b|\s\+\S)`),
	// rm recursive and forced, flags in any order and anywhere among its arguments (rm x -rf,
	// rm --recursive --force, rm -r x -f): see RMRecursiveForced.
	regexp.MustCompile(`(?i)\bgit\b[^;&|\n]*\breset\b[^;&|\n]*\s--hard\b`),
	regexp.MustCompile(`(?i)\bgit\b[^;&|\n]*\bclean\b[^;&|\n]*\s(-[a-z]*f[a-z]*|--force)\b`),
	regexp.MustCompile(`(?i)\bterraform\b[^;&|\n]*\b(apply|destroy)\b`),
	regexp.MustCompile(`(?i)\bmigrat(e|es|ed|ion|ions)\b|\b(alembic|goose|flyway|liquibase)\b`),
	regexp.MustCompile(`(?i)\b(re)?deploy|\bhelm\s+(install|upgrade|uninstall|delete|rollback)\b|\bkubectl\b[^;&|\n]*\b(apply|delete)\b`),
	DaemonCommand,
	regexp.MustCompile(`(?i)\bdrop\s+(table|database|schema|index|view|owned)\b|\btruncate\b|\bdelete\s+from\b`),
}

// DaemonCommand is the deny-list's entry for a command that can stop or restart the daemon
// itself: the installer, blerg-up.sh, a service restart or stop. A restart notice names it as the
// only kind of command that can have caused an unplanned restart (restartCauseWords).
var DaemonCommand = regexp.MustCompile(`(?i)\binstall\.sh\b|\bblerg-up\.sh\b|\bsystemctl\b[^;&|\n]*\b(restart|stop|kill)\b|\blaunchctl\b[^;&|\n]*\b(kickstart|stop|unload|bootout)\b|\bservice\s+\S+\s+(restart|stop)\b|\bmake\b[^;&|\n]*\b(install|restart)-daemon\b`)

// rmSegment finds each rm invocation and its arguments, up to the next command separator.
var rmSegment = regexp.MustCompile(`(?i)(^|[\s;&|(])rm(\s[^;&|\n]*)?`)

// RMRecursiveForced reports whether a command runs rm both recursively and forced, whatever the
// order and place of its flags: -rf, -fr, -Rf, -r … -f, x -rf, --recursive --force.
func RMRecursiveForced(cmd string) bool {
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

// Destructive reports whether text matches the restart safety deny-list.
func Destructive(cmd string) bool {
	if RMRecursiveForced(cmd) {
		return true
	}
	for _, re := range RestartDenyList {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}
