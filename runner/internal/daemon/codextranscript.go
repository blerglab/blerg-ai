package daemon

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Codex retains its conversations independently of the daemon's recovery records. Check the
// persisted rollout before promising that a recorded thread can be resumed.
var codexTranscriptExists = func(id string) bool {
	if id == "" || safeFileName(id) != id {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	root := engineHome(home, "CODEX_HOME", ".codex")
	for _, dir := range []string{"sessions", "archived_sessions"} {
		found := false
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if e.Type().IsRegular() && (e.Name() == id+".jsonl" || strings.HasSuffix(e.Name(), "-"+id+".jsonl")) {
				found = true
				return fs.SkipAll
			}
			return nil
		})
		if found {
			return true
		}
	}
	return false
}
