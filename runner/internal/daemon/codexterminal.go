package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var codexThreadIdentity = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Each terminal keeps its own native history. Recovery must never guess from
// another terminal's most recent conversation, even when both share a cwd.
func prepareCodexTerminalHome(state, id, source string) (string, error) {
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." || strings.ContainsAny(id, `/\\`) || !filepath.IsAbs(source) {
		return "", errors.New("invalid Codex terminal identity")
	}
	home := filepath.Join(state, "codex-terminals", id)
	for _, dir := range []string{state, filepath.Dir(home)} {
		if _, err := secureAgentRecordsDir(dir, true); err != nil {
			return "", err
		}
	}
	if _, err := secureAgentRecordsDir(home, true); err != nil {
		return "", err
	}
	for _, name := range []string{"sessions", "archived_sessions"} {
		if _, err := secureAgentRecordsDir(filepath.Join(home, name), true); err != nil {
			return "", err
		}
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return "", errors.New("codex configuration is unavailable")
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "auth.json" && name != "config.toml" && name != "AGENTS.md" && name != "skills" && name != "plugins" && name != "marketplaces" && !strings.HasSuffix(name, ".config.toml") {
			continue
		}
		target, link := filepath.Join(source, name), filepath.Join(home, name)
		if got, err := os.Readlink(link); err == nil && got == target {
			continue
		}
		if err := os.Symlink(target, link); err != nil {
			return "", errors.New("codex terminal configuration could not be linked")
		}
	}
	if err := prepareCodexTerminalGuide(home, source); err != nil {
		return "", err
	}
	return home, nil
}

// Inspect only this terminal's private history; child and exec conversations
// cannot replace the interactive conversation. Read a bounded first line only.
func codexTerminalThread(home string) string {
	if home == "" {
		return ""
	}
	if exists, err := secureAgentRecordsDir(home, false); err != nil || !exists {
		return ""
	}
	homeRoot, err := os.OpenRoot(home)
	if err != nil {
		return ""
	}
	defer func() { _ = homeRoot.Close() }()
	root, err := homeRoot.OpenRoot("sessions")
	if err != nil {
		return ""
	}
	defer func() { _ = root.Close() }()
	var latest time.Time
	var id string
	visited := 0
	_ = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		visited++
		if visited > 50000 {
			id = ""
			return filepath.SkipAll
		}
		if err != nil || entry == nil || !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		file, err := root.Open(path)
		if err != nil {
			return nil
		}
		defer func() { _ = file.Close() }()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 256*1024)
		if !scanner.Scan() {
			return nil
		}
		var meta struct {
			Type    string `json:"type"`
			Payload struct {
				ID        string          `json:"id"`
				Timestamp time.Time       `json:"timestamp"`
				Source    json.RawMessage `json:"source"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &meta) != nil || meta.Type != "session_meta" || meta.Payload.Timestamp.IsZero() || meta.Payload.Timestamp.After(time.Now().Add(24*time.Hour)) || !codexThreadIdentity.MatchString(meta.Payload.ID) || !strings.HasSuffix(entry.Name(), "-"+meta.Payload.ID+".jsonl") {
			return nil
		}
		var source string
		if json.Unmarshal(meta.Payload.Source, &source) != nil || source != "cli" {
			return nil
		}
		if meta.Payload.Timestamp.After(latest) {
			latest, id = meta.Payload.Timestamp, meta.Payload.ID
		}
		return nil
	})
	return id
}
