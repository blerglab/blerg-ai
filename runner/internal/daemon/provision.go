package daemon

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ─── Managed-block constants ──────────────────────────────────────────────────

// managedBlockStart / managedBlockEnd are the sentinel comments that bracket the
// blerg-runner-messaging guidance in ~/.claude/CLAUDE.md.  The daemon owns everything
// between them; content outside is untouched.
const (
	managedBlockStart = "<!-- BEGIN blerg-runner-messaging (managed by the blerg-runner daemon — do not edit between these markers) -->"
	managedBlockEnd   = "<!-- END blerg-runner-messaging -->"
)

// managedBlock is the canonical, up-to-date content for the blerg-runner-messaging
// section including the sentinel markers.  Changing this string triggers a
// replacement on the next daemon start (the idempotency check is exact equality).
const managedBlock = managedBlockStart + `

## Messaging the user via blerg-runner

Use the ` + "`blerg-runner`" + ` CLI to reach the user **during execution** (not during brainstorming). Commands:

- ` + "`blerg-runner update \"<milestone>\"`" + ` — brief progress note; infrequent
- ` + "`blerg-runner ask \"<question>\"`" + ` — **BLOCKING**: waits for the user’s reply. The user answers from the Blerg Runner Chat tab or phone — not the session terminal (this blocks a Bash call). Use when the decision can wait and the user may be away. If you’re clearly pairing in-session right now, just ask in the terminal — don’t push an in-flow decision to chat.
- ` + "`blerg-runner note \"<idea>\"`" + ` — non-blocking; the user’s reply arrives as your next input, later

Default to quiet — don’t spam. Works from subagents. If ` + "`blerg-runner`" + ` is missing, skip silently.

` + managedBlockEnd

// ─── upsertManagedBlock (pure, testable) ─────────────────────────────────────

// upsertManagedBlock returns the updated file contents and whether a change was
// made, given the existing file content and the desired managed block.
//
// Rules:
//   - existing == "" → return block (file is new)
//   - no markers     → append block with a separating blank line
//   - has markers in correct order → replace everything between (and including) the markers
//   - already current → return (existing, false) — no write needed
//   - orphaned/garbled markers (only START, only END, or END before START) →
//     strip marker lines from existing content, then append fresh block
func upsertManagedBlock(existing, block string) (string, bool) { //nolint:unparam // pure text function: the block is an input so tests and future blocks do not depend on the package constant
	// Idempotent: block already present and current.
	if strings.Contains(existing, block) {
		return existing, false
	}

	// Has old markers in correct order (possibly with stale content) → replace them.
	startIdx := strings.Index(existing, managedBlockStart)
	endIdx := strings.Index(existing, managedBlockEnd)
	if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
		after := endIdx + len(managedBlockEnd)
		return existing[:startIdx] + block + existing[after:], true
	}

	// Orphaned or garbled markers — strip debris marker lines, then append fresh block.
	base := existing
	if startIdx != -1 || endIdx != -1 {
		base = stripOrphanedMarkers(existing)
	}

	// Append with a separating blank line.
	sep := "\n\n"
	switch {
	case base == "":
		sep = ""
	case strings.HasSuffix(base, "\n\n"):
		sep = ""
	case strings.HasSuffix(base, "\n"):
		sep = "\n"
	}
	return base + sep + block, true
}

// stripOrphanedMarkers removes any line that is exactly managedBlockStart or
// managedBlockEnd from content, so garbled marker debris does not accumulate.
// All other user content is preserved unchanged.
func stripOrphanedMarkers(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == managedBlockStart || line == managedBlockEnd {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// ─── ensureClaudeMd ──────────────────────────────────────────────────────────

// ensureClaudeMd ensures <homeDir>/.claude/CLAUDE.md contains the blerg-runner-messaging
// managed block.  Returns (true, nil) if the file was created or modified.
func ensureClaudeMd(homeDir string) (changed bool, err error) {
	claudeDir := filepath.Join(homeDir, ".claude")
	path := filepath.Join(claudeDir, "CLAUDE.md")

	var existing string
	data, err := os.ReadFile(path) //nolint:gosec // fixed path <home>/.claude/CLAUDE.md
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read %s: %w", path, err)
		}
		// File does not exist — create the directory.
		if mkErr := os.MkdirAll(claudeDir, 0o750); mkErr != nil {
			return false, fmt.Errorf("mkdir %s: %w", claudeDir, mkErr)
		}
	} else {
		existing = string(data)
	}

	newContent, modified := upsertManagedBlock(existing, managedBlock)
	if !modified {
		return false, nil
	}

	// Write atomically: temp file in the same directory, then rename.
	// A plain WriteFile (O_TRUNC) can leave the file empty on a crash between
	// truncate and write; rename is atomic on the same filesystem.
	tmp, tmpErr := os.CreateTemp(claudeDir, ".claude-md-*.tmp")
	if tmpErr != nil {
		return false, fmt.Errorf("create temp in %s: %w", claudeDir, tmpErr)
	}
	tmpName := tmp.Name()
	_, writeErr := tmp.WriteString(newContent)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(tmpName) // best-effort cleanup
		if writeErr != nil {
			return false, fmt.Errorf("write temp %s: %w", tmpName, writeErr)
		}
		return false, fmt.Errorf("close temp %s: %w", tmpName, closeErr)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName) // best-effort cleanup
		return false, fmt.Errorf("rename %s → %s: %w", tmpName, path, err)
	}
	return true, nil
}

// ─── ensureCliSymlink ────────────────────────────────────────────────────────

// ensureCliSymlink ensures <homeDir>/.local/bin/blerg-runner is a symlink to
// <repoRoot>/runner/.claude/skills/session-messaging/blerg-runner — the
// monorepo layout, where `runner/` is its own Go module and the CLI script
// lives under it, not directly under repoRoot.
// Returns (true, nil) if the symlink was created or corrected.
func ensureCliSymlink(repoRoot, homeDir string) (changed bool, err error) {
	target := filepath.Join(repoRoot, "runner", ".claude", "skills", "session-messaging", "blerg-runner")

	if _, err := os.Stat(target); err != nil {
		return false, fmt.Errorf("blerg-runner script not found at %s: %w", target, err)
	}

	localBin := filepath.Join(homeDir, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o750); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", localBin, err)
	}

	link := filepath.Join(localBin, "blerg-runner")

	existing, readErr := os.Readlink(link)
	if readErr == nil {
		if existing == target {
			return false, nil // already correct, nothing to do
		}
		// Wrong target — remove and recreate.
		if err := os.Remove(link); err != nil {
			return false, fmt.Errorf("remove stale symlink %s: %w", link, err)
		}
	} else if !os.IsNotExist(readErr) {
		// EINVAL from Readlink means the path exists but is not a symlink.
		var pathErr *os.PathError
		if errors.As(readErr, &pathErr) && errors.Is(pathErr.Err, syscall.EINVAL) {
			return false, fmt.Errorf("a non-symlink file exists at %s; remove it to let the daemon manage the blerg-runner CLI", link)
		}
		return false, fmt.Errorf("readlink %s: %w", link, readErr)
	}

	if err := os.Symlink(target, link); err != nil {
		return false, fmt.Errorf("symlink %s → %s: %w", link, target, err)
	}
	return true, nil
}

// ─── ensureSkillSymlink ──────────────────────────────────────────────────────

// ensureSkillSymlink ensures <homeDir>/.claude/skills/managing-tickets is a
// symlink to <repoRoot>/runner/skills/managing-tickets — the monorepo
// layout, where `runner/` is its own Go module and the skill lives under it,
// not directly under repoRoot.
// Returns (true, nil) if the symlink was created or corrected.
func ensureSkillSymlink(repoRoot, homeDir string) (changed bool, err error) {
	target := filepath.Join(repoRoot, "runner", "skills", "managing-tickets")

	if _, err := os.Stat(target); err != nil {
		return false, fmt.Errorf("managing-tickets skill not found at %s: %w", target, err)
	}

	skillsDir := filepath.Join(homeDir, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o750); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", skillsDir, err)
	}

	link := filepath.Join(skillsDir, "managing-tickets")

	existing, readErr := os.Readlink(link)
	if readErr == nil {
		if existing == target {
			return false, nil // already correct, nothing to do
		}
		// Wrong target — remove and recreate.
		if err := os.Remove(link); err != nil {
			return false, fmt.Errorf("remove stale symlink %s: %w", link, err)
		}
	} else if !os.IsNotExist(readErr) {
		// EINVAL from Readlink means the path exists but is not a symlink.
		var pathErr *os.PathError
		if errors.As(readErr, &pathErr) && errors.Is(pathErr.Err, syscall.EINVAL) {
			return false, fmt.Errorf("a non-symlink file exists at %s; remove it to let the daemon manage the managing-tickets skill", link)
		}
		return false, fmt.Errorf("readlink %s: %w", link, readErr)
	}

	if err := os.Symlink(target, link); err != nil {
		return false, fmt.Errorf("symlink %s → %s: %w", link, target, err)
	}
	return true, nil
}

// ─── prependPathInEnv ────────────────────────────────────────────────────────

// prependPathInEnv returns a copy of env with dir prepended to the PATH entry.
// If no PATH entry exists one is appended.  No-op when dir is empty or already
// present anywhere in the list (even not at the front).
func prependPathInEnv(env []string, dir string) []string {
	if dir == "" {
		return env
	}
	result := make([]string, len(env))
	copy(result, env)

	for i, e := range result {
		if !strings.HasPrefix(e, "PATH=") {
			continue
		}
		path := strings.TrimPrefix(e, "PATH=")
		for _, p := range filepath.SplitList(path) {
			if p == dir {
				return result // already present, no change
			}
		}
		if path == "" {
			result[i] = "PATH=" + dir
		} else {
			result[i] = "PATH=" + dir + string(os.PathListSeparator) + path
		}
		return result
	}
	// No PATH entry found — add one.
	return append(result, "PATH="+dir)
}

// ─── repoRootFromExecutable ───────────────────────────────────────────────────

// RepoRootFromExecutable derives the repository root from the daemon executable
// path.  The daemon is built to <repo>/bin/blerg-runner-daemon, so the repo root is
// two directories up.
func RepoRootFromExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	return filepath.Dir(filepath.Dir(exe)), nil
}

// ─── Provision ───────────────────────────────────────────────────────────────

// Provision idempotently sets up the blerg-runner tooling on this daemon host:
//  1. If provisionClaudeMd is true, ensures ~/.claude/CLAUDE.md contains the
//     blerg-runner-messaging guidance block. This edits a file the daemon does
//     not own, so it defaults to off (env BLERG_RUNNER_PROVISION_CLAUDE_MD,
//     default false) — opt in explicitly if you want it.
//  2. Ensures ~/.local/bin/blerg-runner is a symlink to the CLI script in the repo.
//  3. Ensures ~/.claude/skills/managing-tickets is a symlink to the skill in the repo.
//
// It is best-effort: any error is logged and the daemon continues to start
// normally.  It is safe to call on every daemon start.
func Provision(repoRoot, homeDir string, provisionClaudeMd bool) {
	if provisionClaudeMd {
		changed, err := ensureClaudeMd(homeDir)
		if err != nil {
			log.Printf("provision: ensureClaudeMd: %v", err)
		} else if changed {
			log.Printf("provision: updated %s/.claude/CLAUDE.md with blerg-runner-messaging block", homeDir)
		}
	} else {
		log.Printf("provision: not editing ~/.claude/CLAUDE.md (set BLERG_RUNNER_PROVISION_CLAUDE_MD=true to add the blerg-runner messaging block)")
	}

	changed, err := ensureCliSymlink(repoRoot, homeDir)
	if err != nil {
		log.Printf("provision: ensureCliSymlink: %v", err)
	} else if changed {
		log.Printf("provision: symlinked %s/.local/bin/blerg-runner → %s/runner/.claude/skills/session-messaging/blerg-runner",
			homeDir, repoRoot)
	}

	changed, err = ensureSkillSymlink(repoRoot, homeDir)
	if err != nil {
		log.Printf("provision: ensureSkillSymlink: %v", err)
	} else if changed {
		log.Printf("provision: symlinked %s/.claude/skills/managing-tickets → %s/runner/skills/managing-tickets",
			homeDir, repoRoot)
	}
}
