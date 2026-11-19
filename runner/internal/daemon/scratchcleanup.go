package daemon

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/scratch"
)

// scratchFolder pins the allocation, rather than interpreting a later path as ownership.
type scratchFolder struct {
	path string
	info os.FileInfo
}

func bindScratchFolder(root, path, repo string) *scratchFolder {
	if !scratch.Valid(repo) || filepath.Clean(path) != filepath.Join(root, repo) {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	return &scratchFolder{path, info}
}

// remove removes only an empty folder or the exact metadata of a fresh git init.
// Comparing bytes also preserves unreachable objects, non-HEAD refs, ignored files,
// staged work, custom hooks and metadata we do not understand. No Git runs in the candidate.
// Call only after all session writers have stopped and sandbox work has been returned.
func (s *scratchFolder) remove() string { return s.inspect(true) }

func (s *scratchFolder) inspect(apply bool) string {
	if s == nil {
		return ""
	}
	kept := func(why string) string { return fmt.Sprintf("Scratch folder kept at %s: %s", s.path, why) }
	current, err := os.Lstat(s.path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil || !os.SameFile(s.info, current) || !current.IsDir() {
		return kept("its identity could not be verified")
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return kept("it could not be opened")
	}
	defer func() { _ = root.Close() }()
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(s.info, pinned) {
		return kept("it changed during cleanup")
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return kept("it could not be inspected")
	}
	if len(entries) == 0 {
		if !apply {
			return ""
		}
		if err := os.Remove(s.path); err != nil {
			return kept("it could not be removed")
		}
		return ""
	}
	if len(entries) != 1 || entries[0].Name() != ".git" || !entries[0].IsDir() {
		return kept("it contains work")
	}
	template, err := os.MkdirTemp("", "blerg-empty-git-")
	if err != nil {
		return kept("empty Git metadata could not be verified")
	}
	defer func() { _ = os.RemoveAll(template) }()
	if _, err = hostGit(template, "init", "--quiet", "--initial-branch=main"); err != nil {
		return kept("empty Git metadata could not be verified")
	}
	expected, err := scratchMetadata(os.DirFS(filepath.Join(template, ".git")))
	if err != nil {
		return kept("empty Git metadata could not be verified")
	}
	gitRoot, err := root.OpenRoot(".git")
	if err != nil {
		return kept("its Git metadata could not be opened")
	}
	defer func() { _ = gitRoot.Close() }()
	actual, err := scratchMetadata(gitRoot.FS())
	if err != nil || len(actual) != len(expected) {
		return kept("it contains Git work or unfamiliar metadata")
	}
	// Objects and refs are work even if an unusual installed Git template also
	// contains them. Never authorize their deletion through template equality.
	for name, data := range actual {
		if data != nil && (strings.HasPrefix(name, "objects/") || strings.HasPrefix(name, "refs/") || strings.HasPrefix(name, "logs/") || name == "packed-refs") {
			return kept("it contains Git work")
		}
	}
	for name, want := range expected {
		got, ok := actual[name]
		if !ok || (want == nil) != (got == nil) || !bytes.Equal(want, got) {
			return kept("it contains Git work or unfamiliar metadata")
		}
	}
	if !apply {
		return ""
	}
	// Remove only the inspected template entries. A new file is never recursively
	// deleted; it prevents removal of its parent instead.
	names := make([]string, 0, len(expected))
	for name := range expected {
		names = append(names, name)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names {
		path := name
		if data := expected[name]; data != nil {
			got, err := scratchReadMetadata(gitRoot.FS(), filepath.ToSlash(path))
			if err != nil || !bytes.Equal(data, got) {
				return kept("it changed during cleanup")
			}
		}
		if err := gitRoot.Remove(path); err != nil {
			return kept("it changed during cleanup")
		}
	}
	if err := root.Remove(".git"); err != nil {
		return kept("it changed during cleanup")
	}
	if err := os.Remove(s.path); err != nil {
		return kept("it changed during cleanup")
	}
	return ""
}

func scratchMetadata(root fs.FS) (map[string][]byte, error) {
	result := map[string][]byte{}
	err := fs.WalkDir(root, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		if len(result) > 256 {
			return fmt.Errorf("too much metadata")
		}
		name := path
		if entry.IsDir() {
			result[name] = nil
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 64<<10 {
			return fmt.Errorf("unfamiliar metadata")
		}
		data, err := scratchReadMetadata(root, path)
		if err != nil {
			return err
		}
		result[name] = append([]byte{}, data...)
		return nil
	})
	return result, err
}

func appendScratchReport(message, report string) string {
	if report == "" {
		return message
	}
	if message == "" {
		return report
	}
	return strings.TrimSpace(message) + ". " + report
}

func scratchReadMetadata(root fs.FS, path string) ([]byte, error) {
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if len(data) > 64<<10 {
		return nil, fmt.Errorf("metadata too large")
	}
	return data, err
}

// Another idle session is still live, even when it has no open engine files.
func (h *AgentHost) scratchInUse(folder *scratchFolder) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, sess := range h.sessions {
		if sess.scratch != nil && os.SameFile(folder.info, sess.scratch.info) || sess.workspaceInfo != nil && os.SameFile(folder.info, sess.workspaceInfo) {
			return true
		}
	}
	for _, ad := range h.adopting {
		if !ad.dropped && filepath.Clean(ad.rec.WorkDir) == folder.path {
			return true
		}
	}
	return false
}

func scratchClonePending(id string) bool {
	_, record, ok := sandboxClonePaths(id)
	if !ok {
		return false
	}
	_, err := os.Lstat(record)
	return !os.IsNotExist(err)
}
