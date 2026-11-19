package daemon

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

// SweepSessionSkillPacks runs only after the daemon has acquired its records
// lock. Recoverable sessions retain their paths and initial-delivery receipts.
// Legacy random packs have no ownership record: age them out conservatively.
func SweepSessionSkillPacks(records string) {
	stateDir := DaemonStateDir()
	if stateDir == "" {
		return
	}
	keep := map[string]bool{}
	for _, rec := range listAgentRecords(records) {
		keep[filepath.Base(sessionSkillDir(stateDir, rec.SessionID))] = true
	}
	sweepSkillDirectory(filepath.Dir(sessionSkillDir(stateDir, "")), keep, time.Now(), false)
	sweepSkillDirectory(os.TempDir(), nil, time.Now(), true)
}

func sweepSkillDirectory(root string, keep map[string]bool, now time.Time, legacy bool) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		failurelog.Default.Event("session_skill_sweep", slog.LevelWarn, "cause", failurelog.Cause(err))
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if keep[name] || legacy && !strings.HasPrefix(name, "blerg-skills-") {
			continue
		}
		if !legacy && (len(name) != 64 || strings.Trim(name, "0123456789abcdef") != "") {
			continue
		}
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 || !ownedByDaemon(info) || now.Sub(info.ModTime()) < 24*time.Hour {
			continue
		}
		marker := ".claude-plugin/plugin.json"
		expected := "{\"name\":\"blerg\",\"version\":\"1.0.0\"}"
		if !legacy {
			// Compact-only sessions have a receipt but no plugin installation.
			if _, err := os.Lstat(filepath.Join(path, ".claude-plugin")); os.IsNotExist(err) {
				marker = "initial-delivered"
				expected = "delivered\n"
			}
		}
		meta, err := os.Lstat(filepath.Join(path, marker))
		if err != nil || !meta.Mode().IsRegular() || !ownedByDaemon(meta) || meta.Size() > 256 {
			continue
		}
		raw, err := readSkillSweepMarker(path, marker)
		if err != nil {
			failurelog.Default.Event("session_skill_sweep", slog.LevelWarn, "cause", failurelog.Cause(err))
			continue
		}
		if string(raw) != expected {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			failurelog.Default.Event("session_skill_sweep", slog.LevelWarn, "cause", failurelog.Cause(err))
		}
	}
}

func readSkillSweepMarker(dir, name string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, 257))
}
