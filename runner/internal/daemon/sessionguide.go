package daemon

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// sessionGuideLine keeps command guidance engine-neutral while retaining each
// destination's formatting. Add a line here to extend prompts and managed files.
type sessionGuideLine struct {
	Text          string
	ManagedPrefix string
	ManagedSuffix string
}

const visualGuideLine = "Use mermaid fences for diagrams and vega-lite fences for charts with inline data only; external resources and executable HTML or JavaScript are refused. Publish .mmd or .vl.json files to open them in the same viewer."

func sessionWorkGuideLines() []sessionGuideLine {
	return []sessionGuideLine{
		{Text: visualGuideLine, ManagedPrefix: "- Rich output — "},
		{Text: childSessionGuideLine, ManagedPrefix: "- `blerg-runner session` / `blerg-runner budget` — "},
		{Text: diffGuideLine, ManagedPrefix: "- `blerg-runner diff` — "},
		{Text: workGuideLine, ManagedPrefix: "- `blerg-runner checklist` / `blerg-runner pin` — "},
		{Text: topicGuideLine, ManagedPrefix: "- `blerg-runner topic` — mark a piece of work for the History tab of the session page. ", ManagedSuffix: " Without markers the History tab shows only a summary."},
	}
}

func sessionWorkGuidePrompt() string {
	lines := sessionWorkGuideLines()
	text := make([]string, len(lines))
	for i, line := range lines {
		text[i] = line.Text
	}
	return strings.Join(text, " ")
}

func sessionWorkGuideManaged() string {
	lines := sessionWorkGuideLines()
	text := make([]string, len(lines))
	for i, line := range lines {
		text[i] = line.ManagedPrefix + line.Text + line.ManagedSuffix
	}
	return strings.Join(text, "\n")
}

const codexGuideMaxBytes = 1 << 20

// prepareCodexTerminalGuide owns only the terminal's private instructions file.
// It copies effective user instructions; source config and instructions are
// never edited, including when older private homes contain source symlinks.
func prepareCodexTerminalGuide(home, source string) error {
	if !filepath.IsAbs(home) || !filepath.IsAbs(source) || filepath.Clean(home) == filepath.Clean(source) {
		return errors.New("codex terminal instructions require a private home")
	}
	entryInfo, err := os.Lstat(home)
	if err != nil || !entryInfo.IsDir() || entryInfo.Mode()&os.ModeSymlink != 0 || !ownedByDaemon(entryInfo) {
		return errors.New("codex terminal instructions home is unsafe")
	}
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return errors.New("codex terminal instructions source is unavailable")
	}
	defer func() { _ = sourceRoot.Close() }()
	root, err := os.OpenRoot(home)
	if err != nil {
		return errors.New("codex terminal instructions home is unavailable")
	}
	defer func() { _ = root.Close() }()
	privateInfo, privateErr := root.Stat(".")
	sourceInfo, sourceErr := sourceRoot.Stat(".")
	if privateErr != nil || sourceErr != nil || !os.SameFile(entryInfo, privateInfo) || os.SameFile(privateInfo, sourceInfo) {
		return errors.New("codex terminal instructions require a separate private home")
	}
	if privateInfo.Mode().Perm()&0o077 != 0 {
		if err := root.Chmod(".", 0700); err != nil {
			return errors.New("codex terminal instructions home could not be secured")
		}
	}
	var original string
	for _, name := range []string{"AGENTS.override.md", "AGENTS.md"} {
		// User-managed dotfiles commonly link to another config directory. Read
		// that explicit target without ever publishing back through the link.
		resolved, err := filepath.EvalSymlinks(filepath.Join(source, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return errors.New("codex terminal instructions could not be resolved")
		}
		parent, err := os.OpenRoot(filepath.Dir(resolved))
		if err != nil {
			return errors.New("codex terminal instructions could not be read")
		}
		file, err := workspaceOpenFile(parent, filepath.Base(resolved))
		_ = parent.Close()
		if err != nil {
			return errors.New("codex terminal instructions could not be read")
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > codexGuideMaxBytes {
			_ = file.Close()
			return errors.New("codex terminal instructions are not a bounded regular file")
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, codexGuideMaxBytes+1))
		_ = file.Close()
		if readErr != nil || len(raw) > codexGuideMaxBytes {
			return errors.New("codex terminal instructions could not be read")
		}
		if strings.TrimSpace(string(raw)) != "" {
			original = string(raw)
			break
		}
	}
	body, _ := upsertManagedBlock(original, managedBlock)
	// Codex may bound its instruction document. Keep the shared guide ahead
	// of user text while preserving every byte outside the managed block.
	if start := strings.Index(body, managedBlock); start > 0 {
		body = managedBlock + "\n\n" + body[:start] + body[start+len(managedBlock):]
	}
	// Anchor publication to the secure private directory. Rename replaces any old
	// instruction symlink itself, never its target. No O_TRUNC write is used.
	name := ".blerg-guide-" + ccUUID() + ".tmp"
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("codex terminal instructions could not be created")
	}
	defer func() { _ = root.Remove(name) }()
	_, writeErr := file.WriteString(body)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("codex terminal instructions could not be written")
	}
	if err := root.Rename(name, "AGENTS.override.md"); err != nil {
		return errors.New("codex terminal instructions could not be installed")
	}
	return nil
}
