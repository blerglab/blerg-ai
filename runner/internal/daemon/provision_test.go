package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── upsertManagedBlock ───────────────────────────────────────────────────────

// TestUpsertManagedBlock_EmptyExisting verifies that inserting the block into an
// empty file returns just the block itself.
func TestUpsertManagedBlock_EmptyExisting(t *testing.T) {
	result, changed := upsertManagedBlock("", managedBlock)
	if !changed {
		t.Error("expected changed=true for empty input")
	}
	if result != managedBlock {
		t.Errorf("expected result to equal managedBlock, got:\n%s", result)
	}
}

// TestUpsertManagedBlock_NoMarkers verifies that the block is appended when the
// file has no markers, preserving all existing content.
func TestUpsertManagedBlock_NoMarkers(t *testing.T) {
	existing := "# My global config\n\nSome existing content.\n"
	result, changed := upsertManagedBlock(existing, managedBlock)
	if !changed {
		t.Error("expected changed=true when no markers present")
	}
	if !strings.HasPrefix(result, existing) {
		t.Errorf("expected result to preserve existing content at start;\nresult:\n%s", result)
	}
	if !strings.Contains(result, managedBlock) {
		t.Error("expected result to contain the managed block")
	}
}

// TestUpsertManagedBlock_HasMarkers verifies that existing markers are replaced
// with the current block, and content outside the markers is preserved.
func TestUpsertManagedBlock_HasMarkers(t *testing.T) {
	existing := "# Preamble\n\n" + managedBlockStart + "\nOLD CONTENT\n" + managedBlockEnd + "\n\n# Postamble\n"
	result, changed := upsertManagedBlock(existing, managedBlock)
	if !changed {
		t.Error("expected changed=true when old block present")
	}
	if !strings.Contains(result, "# Preamble") {
		t.Error("expected preamble to be preserved")
	}
	if !strings.Contains(result, "# Postamble") {
		t.Error("expected postamble to be preserved")
	}
	if strings.Contains(result, "OLD CONTENT") {
		t.Error("expected old content between markers to be replaced")
	}
	if !strings.Contains(result, managedBlock) {
		t.Errorf("expected result to contain the new managed block;\nresult:\n%s", result)
	}
}

// TestUpsertManagedBlock_Idempotent verifies that running upsertManagedBlock on a
// file that already contains the current block returns changed=false and does not
// modify the content.
func TestUpsertManagedBlock_Idempotent(t *testing.T) {
	existing := "# Header\n\n" + managedBlock + "\n"
	result, changed := upsertManagedBlock(existing, managedBlock)
	if changed {
		t.Error("expected changed=false when block already current")
	}
	if result != existing {
		t.Errorf("expected result to be unchanged;\ngot:\n%s", result)
	}
}

// TestUpsertManagedBlock_OrphanedStartOnly verifies that when only the START marker
// is present (no END), the function strips the orphaned marker line, preserves all
// other content, and appends the fresh block.
func TestUpsertManagedBlock_OrphanedStartOnly(t *testing.T) {
	existing := "# My config\n\n" + managedBlockStart + "\nsome leftover\n"
	result, changed := upsertManagedBlock(existing, managedBlock)
	if !changed {
		t.Error("expected changed=true for orphaned START-only")
	}
	// User content outside markers must be preserved.
	if !strings.Contains(result, "# My config") {
		t.Error("expected user content '# My config' to be preserved")
	}
	// Leftover content (not the marker itself) should also be preserved.
	if !strings.Contains(result, "some leftover") {
		t.Error("expected non-marker content 'some leftover' to be preserved")
	}
	// Fresh block must be present.
	if !strings.Contains(result, managedBlock) {
		t.Errorf("expected result to contain the managed block;\nresult:\n%s", result)
	}
	// Exactly one START marker — the one inside the fresh block.
	if count := strings.Count(result, managedBlockStart); count != 1 {
		t.Errorf("expected exactly 1 START marker in result, got %d;\nresult:\n%s", count, result)
	}
}

// TestUpsertManagedBlock_OrphanedEndOnly verifies that when only the END marker is
// present (no START), the function strips the orphaned marker line, preserves all
// other content, and appends the fresh block.
func TestUpsertManagedBlock_OrphanedEndOnly(t *testing.T) {
	existing := "# My config\n\nsome user text\n" + managedBlockEnd + "\n"
	result, changed := upsertManagedBlock(existing, managedBlock)
	if !changed {
		t.Error("expected changed=true for orphaned END-only")
	}
	if !strings.Contains(result, "# My config") {
		t.Error("expected user content '# My config' to be preserved")
	}
	if !strings.Contains(result, "some user text") {
		t.Error("expected 'some user text' to be preserved")
	}
	if !strings.Contains(result, managedBlock) {
		t.Errorf("expected result to contain the managed block;\nresult:\n%s", result)
	}
	// Exactly one END marker — the one inside the fresh block.
	if count := strings.Count(result, managedBlockEnd); count != 1 {
		t.Errorf("expected exactly 1 END marker in result, got %d;\nresult:\n%s", count, result)
	}
}

// TestUpsertManagedBlock_MarkersInWrongOrder verifies that when END appears before
// START in the file, the function treats it as garbled, strips both marker lines,
// preserves other content, and appends the fresh block.
func TestUpsertManagedBlock_MarkersInWrongOrder(t *testing.T) {
	existing := "# Config\n\n" + managedBlockEnd + "\nsome content\n" + managedBlockStart + "\n"
	result, changed := upsertManagedBlock(existing, managedBlock)
	if !changed {
		t.Error("expected changed=true for wrong-order markers")
	}
	if !strings.Contains(result, "# Config") {
		t.Error("expected user content '# Config' to be preserved")
	}
	if !strings.Contains(result, "some content") {
		t.Error("expected 'some content' to be preserved")
	}
	if !strings.Contains(result, managedBlock) {
		t.Errorf("expected result to contain the managed block;\nresult:\n%s", result)
	}
	// Both markers should appear exactly once — inside the fresh block.
	if count := strings.Count(result, managedBlockStart); count != 1 {
		t.Errorf("expected exactly 1 START marker, got %d", count)
	}
	if count := strings.Count(result, managedBlockEnd); count != 1 {
		t.Errorf("expected exactly 1 END marker, got %d", count)
	}
}

// TestUpsertManagedBlock_IdempotentAfterRepair verifies that running upsertManagedBlock
// a second time on a file repaired from orphaned markers returns changed=false.
func TestUpsertManagedBlock_IdempotentAfterRepair(t *testing.T) {
	existing := "# My config\n\n" + managedBlockStart + "\nstale content\n"
	repaired, changed := upsertManagedBlock(existing, managedBlock)
	if !changed {
		t.Fatal("first call: expected changed=true")
	}
	result, changed2 := upsertManagedBlock(repaired, managedBlock)
	if changed2 {
		t.Error("second call: expected changed=false (idempotent after repair)")
	}
	if result != repaired {
		t.Errorf("second call: expected result to be unchanged;\ngot:\n%s", result)
	}
}

// ─── ensureClaudeMd ──────────────────────────────────────────────────────────

// TestEnsureClaudeMd_Creates verifies that ensureClaudeMd creates the CLAUDE.md
// file when it does not yet exist.
func TestEnsureClaudeMd_Creates(t *testing.T) {
	home := t.TempDir()
	changed, err := ensureClaudeMd(home)
	if err != nil {
		t.Fatalf("ensureClaudeMd: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when file created")
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), managedBlock) {
		t.Errorf("expected CLAUDE.md to contain the managed block;\ngot:\n%s", string(data))
	}
}

// TestEnsureClaudeMd_Idempotent verifies that a second call returns changed=false
// and leaves the file unchanged.
func TestEnsureClaudeMd_Idempotent(t *testing.T) {
	home := t.TempDir()
	if _, err := ensureClaudeMd(home); err != nil {
		t.Fatalf("first ensureClaudeMd: %v", err)
	}
	changed, err := ensureClaudeMd(home)
	if err != nil {
		t.Fatalf("second ensureClaudeMd: %v", err)
	}
	if changed {
		t.Error("expected changed=false on second call (idempotent)")
	}
}

// ─── ensureCliSymlink ────────────────────────────────────────────────────────

// TestEnsureCliSymlink_Creates verifies that ensureCliSymlink creates the symlink
// when it does not exist.
func TestEnsureCliSymlink_Creates(t *testing.T) {
	home, repoRoot, script := setupSymlinkDirs(t)

	changed, err := ensureCliSymlink(repoRoot, home)
	if err != nil {
		t.Fatalf("ensureCliSymlink: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when symlink created")
	}

	link := filepath.Join(home, ".local", "bin", "blerg-runner")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != script {
		t.Errorf("symlink points to %q, want %q", target, script)
	}
}

// TestEnsureCliSymlink_FixesWrongTarget verifies that a symlink pointing to the
// wrong target is corrected.
func TestEnsureCliSymlink_FixesWrongTarget(t *testing.T) {
	home, repoRoot, script := setupSymlinkDirs(t)

	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(localBin, "blerg-runner")
	if err := os.Symlink("/wrong/path/blerg-runner", link); err != nil {
		t.Fatal(err)
	}

	changed, err := ensureCliSymlink(repoRoot, home)
	if err != nil {
		t.Fatalf("ensureCliSymlink: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when symlink had wrong target")
	}

	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != script {
		t.Errorf("symlink now points to %q, want %q", target, script)
	}
}

// TestEnsureCliSymlink_RegularFileBlocksLink verifies that when a regular
// (non-symlink) file occupies the link path, ensureCliSymlink returns a clear
// error and does NOT clobber the file.
func TestEnsureCliSymlink_RegularFileBlocksLink(t *testing.T) {
	home, repoRoot, _ := setupSymlinkDirs(t)

	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(localBin, "blerg-runner")
	const regularContent = "not a symlink\n"
	if err := os.WriteFile(link, []byte(regularContent), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ensureCliSymlink(repoRoot, home)
	if err == nil {
		t.Fatal("expected an error when a regular file blocks the link path, got nil")
	}
	if !strings.Contains(err.Error(), "non-symlink") {
		t.Errorf("expected error to mention 'non-symlink'; got: %v", err)
	}

	// File must not have been clobbered.
	data, readErr := os.ReadFile(link)
	if readErr != nil {
		t.Fatalf("ReadFile after error: %v", readErr)
	}
	if string(data) != regularContent {
		t.Errorf("regular file was clobbered; got %q, want %q", string(data), regularContent)
	}
}

// TestEnsureCliSymlink_Idempotent verifies that a correct symlink triggers no
// change on a second call.
func TestEnsureCliSymlink_Idempotent(t *testing.T) {
	home, repoRoot, _ := setupSymlinkDirs(t)

	if _, err := ensureCliSymlink(repoRoot, home); err != nil {
		t.Fatalf("first ensureCliSymlink: %v", err)
	}
	changed, err := ensureCliSymlink(repoRoot, home)
	if err != nil {
		t.Fatalf("second ensureCliSymlink: %v", err)
	}
	if changed {
		t.Error("expected changed=false on second call (idempotent)")
	}
}

// ─── prependPathInEnv ────────────────────────────────────────────────────────

// TestPrependPathInEnv verifies the pure PATH-prepend helper used by sessionEnv.
func TestPrependPathInEnv(t *testing.T) {
	const localBin = "/home/user/.local/bin"
	sep := string(os.PathListSeparator)

	tests := []struct {
		name    string
		env     []string
		dir     string
		wantPre string // expected prefix of the PATH= value
		wantLen int    // expected len(result) — same as input
	}{
		{
			name:    "prepends when PATH exists and dir is absent",
			env:     []string{"FOO=bar", "PATH=/usr/bin" + sep + "/bin"},
			dir:     localBin,
			wantPre: localBin,
			wantLen: 2,
		},
		{
			name:    "no-op when dir already first",
			env:     []string{"PATH=" + localBin + sep + "/usr/bin"},
			dir:     localBin,
			wantPre: localBin,
			wantLen: 1,
		},
		{
			name:    "no-op when dir is already present mid-list",
			env:     []string{"PATH=/usr/bin" + sep + localBin + sep + "/bin"},
			dir:     localBin,
			wantPre: "/usr/bin",
			wantLen: 1,
		},
		{
			name:    "adds PATH when not present",
			env:     []string{"FOO=bar"},
			dir:     localBin,
			wantPre: localBin,
			wantLen: 2,
		},
		{
			name:    "empty dir is no-op",
			env:     []string{"PATH=/usr/bin"},
			dir:     "",
			wantPre: "/usr/bin",
			wantLen: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := prependPathInEnv(tc.env, tc.dir)
			if len(result) != tc.wantLen {
				t.Errorf("len(result)=%d, want %d", len(result), tc.wantLen)
			}
			var pathVal string
			for _, e := range result {
				if strings.HasPrefix(e, "PATH=") {
					pathVal = strings.TrimPrefix(e, "PATH=")
					break
				}
			}
			// Only check prefix if we expect a PATH entry.
			if tc.wantPre != "" && !strings.HasPrefix(pathVal, tc.wantPre) {
				t.Errorf("PATH value %q does not start with %q", pathVal, tc.wantPre)
			}
		})
	}
}

// ─── ensureSkillSymlink ──────────────────────────────────────────────────────

// TestEnsureSkillSymlink_Creates verifies that ensureSkillSymlink creates the
// symlink and that SKILL.md inside the symlinked directory is readable.
func TestEnsureSkillSymlink_Creates(t *testing.T) {
	home, repoRoot, skillDir := setupSkillDirs(t)

	changed, err := ensureSkillSymlink(repoRoot, home)
	if err != nil {
		t.Fatalf("ensureSkillSymlink: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when symlink created")
	}

	link := filepath.Join(home, ".claude", "skills", "managing-tickets")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != skillDir {
		t.Errorf("symlink points to %q, want %q", target, skillDir)
	}

	// SKILL.md must be readable through the symlink.
	data, err := os.ReadFile(filepath.Join(link, "SKILL.md"))
	if err != nil {
		t.Fatalf("ReadFile SKILL.md through symlink: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("ReadFile SKILL.md from source: %v", err)
	}
	if string(data) != string(src) {
		t.Errorf("SKILL.md through symlink differs from source;\ngot: %q\nwant: %q", data, src)
	}
}

// TestEnsureSkillSymlink_Idempotent verifies that a second call returns
// changed=false and leaves the symlink unchanged.
func TestEnsureSkillSymlink_Idempotent(t *testing.T) {
	home, repoRoot, _ := setupSkillDirs(t)

	if _, err := ensureSkillSymlink(repoRoot, home); err != nil {
		t.Fatalf("first ensureSkillSymlink: %v", err)
	}
	changed, err := ensureSkillSymlink(repoRoot, home)
	if err != nil {
		t.Fatalf("second ensureSkillSymlink: %v", err)
	}
	if changed {
		t.Error("expected changed=false on second call (idempotent)")
	}
}

// TestEnsureSkillSymlink_FixesWrongTarget verifies that a symlink pointing to
// the wrong target is corrected.
func TestEnsureSkillSymlink_FixesWrongTarget(t *testing.T) {
	home, repoRoot, skillDir := setupSkillDirs(t)

	skillsDir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(skillsDir, "managing-tickets")
	if err := os.Symlink("/wrong/path/managing-tickets", link); err != nil {
		t.Fatal(err)
	}

	changed, err := ensureSkillSymlink(repoRoot, home)
	if err != nil {
		t.Fatalf("ensureSkillSymlink: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when symlink had wrong target")
	}

	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != skillDir {
		t.Errorf("symlink now points to %q, want %q", target, skillDir)
	}
}

// TestEnsureSkillSymlink_UnrelatedSkillUntouched verifies that a pre-existing
// unrelated skill directory in ~/.claude/skills/ is left completely untouched.
func TestEnsureSkillSymlink_UnrelatedSkillUntouched(t *testing.T) {
	home, repoRoot, _ := setupSkillDirs(t)

	// Create a pre-existing unrelated skill.
	otherSkill := filepath.Join(home, ".claude", "skills", "other-skill")
	if err := os.MkdirAll(otherSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	const otherContent = "other skill content\n"
	if err := os.WriteFile(filepath.Join(otherSkill, "README.md"), []byte(otherContent), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ensureSkillSymlink(repoRoot, home); err != nil {
		t.Fatalf("ensureSkillSymlink: %v", err)
	}

	// The unrelated skill must be untouched.
	data, err := os.ReadFile(filepath.Join(otherSkill, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile other skill: %v", err)
	}
	if string(data) != otherContent {
		t.Errorf("other-skill was modified; got %q, want %q", string(data), otherContent)
	}
}

// TestEnsureSkillSymlink_NonSymlinkBlocksLink verifies that when a regular
// (non-symlink) file occupies the target path, ensureSkillSymlink returns a
// clear error and does NOT clobber the file.
func TestEnsureSkillSymlink_NonSymlinkBlocksLink(t *testing.T) {
	home, repoRoot, _ := setupSkillDirs(t)

	skillsDir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(skillsDir, "managing-tickets")
	const regularContent = "not a symlink\n"
	if err := os.WriteFile(link, []byte(regularContent), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ensureSkillSymlink(repoRoot, home)
	if err == nil {
		t.Fatal("expected an error when a regular file blocks the link path, got nil")
	}
	if !strings.Contains(err.Error(), "non-symlink") {
		t.Errorf("expected error to mention 'non-symlink'; got: %v", err)
	}

	// File must not have been clobbered.
	data, readErr := os.ReadFile(link)
	if readErr != nil {
		t.Fatalf("ReadFile after error: %v", readErr)
	}
	if string(data) != regularContent {
		t.Errorf("regular file was clobbered; got %q, want %q", string(data), regularContent)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// setupSkillDirs creates a temp home and a temp repoRoot with the managing-tickets
// skill in the right location for this monorepo layout (under runner/).
// Returns (home, repoRoot, skillDirPath).
func setupSkillDirs(t *testing.T) (home, repoRoot, skillDir string) {
	t.Helper()
	home = t.TempDir()
	repoRoot = t.TempDir()

	skillDir = filepath.Join(repoRoot, "runner", "skills", "managing-tickets")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: managing-tickets\ndescription: stub\n---\nStub.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, repoRoot, skillDir
}

// setupSymlinkDirs creates a temp home and a temp repoRoot with the dummy blerg-runner
// script in the right location for this monorepo layout (under runner/).
// Returns (home, repoRoot, scriptPath).
func setupSymlinkDirs(t *testing.T) (home, repoRoot, script string) {
	t.Helper()
	home = t.TempDir()
	repoRoot = t.TempDir()

	scriptDir := filepath.Join(repoRoot, "runner", ".claude", "skills", "session-messaging")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script = filepath.Join(scriptDir, "blerg-runner")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home, repoRoot, script
}

// ─── Provision (opt-in CLAUDE.md, monorepo-layout symlink targets) ───────────

// TestProvisionClaudeMdIsOptIn verifies that Provision only edits
// ~/.claude/CLAUDE.md when explicitly opted in via provisionClaudeMd=true.
func TestProvisionClaudeMdIsOptIn(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	Provision(repo, home, false)
	if _, err := os.Stat(filepath.Join(home, ".claude", "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("CLAUDE.md must not be written when opt-in is false (err=%v)", err)
	}
	Provision(repo, home, true)
	data, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if err != nil || !strings.Contains(string(data), managedBlock) {
		t.Fatalf("opt-in true must write the managed block: %v", err)
	}
}

// TestProvisionClaudeMdIsOptIn_ByteIdentity is the stronger companion to
// TestProvisionClaudeMdIsOptIn's not-exists check: it seeds an existing
// CLAUDE.md with unrelated content and asserts the bytes are byte-for-byte
// identical after Provision(..., false) — not just "still contains my text",
// which wouldn't catch a spurious append/rewrite that preserves substrings.
func TestProvisionClaudeMdIsOptIn_ByteIdentity(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(claudeDir, "CLAUDE.md")
	original := []byte("# My global config\n\nSome unrelated content that must survive untouched.\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	Provision(repo, home, false)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(data, original) {
		t.Fatalf("CLAUDE.md must be byte-for-byte unchanged when opt-in is false;\ngot:\n%s\nwant:\n%s", data, original)
	}
}

// TestSymlinkTargetsMatchMonorepoLayout verifies that the CLI and skill
// symlinks resolve against runner/.claude/skills/... and runner/skills/...
// under repoRoot — where they actually live in this monorepo — not directly
// under repoRoot.
func TestSymlinkTargetsMatchMonorepoLayout(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	cli := filepath.Join(repo, "runner", ".claude", "skills", "session-messaging", "blerg-runner")
	skill := filepath.Join(repo, "runner", "skills", "managing-tickets")
	if err := os.MkdirAll(filepath.Dir(cli), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureCliSymlink(repo, home); err != nil {
		t.Fatalf("ensureCliSymlink: %v", err)
	}
	if got, _ := os.Readlink(filepath.Join(home, ".local", "bin", "blerg-runner")); got != cli {
		t.Fatalf("cli symlink → %q, want %q", got, cli)
	}
	if _, err := ensureSkillSymlink(repo, home); err != nil {
		t.Fatalf("ensureSkillSymlink: %v", err)
	}
	if got, _ := os.Readlink(filepath.Join(home, ".claude", "skills", "managing-tickets")); got != skill {
		t.Fatalf("skill symlink → %q, want %q", got, skill)
	}
}

// The instructions every Claude Code session reads tell it about `publish` and where the format
// guide is, in a sentence or two (the guide itself is `publish --help`).
func TestManagedBlockMentionsPublish(t *testing.T) {
	for _, want := range []string{"`blerg-runner publish <file>`", "`blerg-runner publish --help`", "file", "`blerg-runner fetch --all`", "same name again creates a new version", "publish it again under the same name", "screenshot it and publish the PNG", "`blerg-runner unpublish <name>`"} {
		if !strings.Contains(managedBlock, want) {
			t.Errorf("managed block does not mention %q", want)
		}
	}
}
