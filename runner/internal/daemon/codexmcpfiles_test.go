package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCodexRestrictedFilesConfineAndDenyControlState(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("restricted tools require Linux")
	}
	work := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("private-test-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(work, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".git", "config"), []byte("private-control-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(work, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(work, ".git", "config"), filepath.Join(work, "hardlink")); err != nil {
		t.Fatal(err)
	}
	fs, err := newCodexRestrictedFiles(work)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	for _, p := range []string{"../secret", "/etc/passwd", "escape/secret", ".git/config", "alias/config", "hardlink", ".codex/config.toml"} {
		in, _ := json.Marshal(map[string]string{"path": p})
		if out, err := fs.Execute("read_file", in); err == nil {
			t.Errorf("read allowed %q: %q", p, out)
		}
	}
	for _, p := range []string{"../write", "escape/write", ".git/config", "alias/config", "hardlink", ".claude/settings.json", ".mcp.json", "AGENTS.md"} {
		in, _ := json.Marshal(map[string]string{"path": p, "content": "bad"})
		if _, err := fs.Execute("write_file", in); err == nil {
			t.Errorf("write allowed %q", p)
		}
	}
	if _, err := fs.Execute("write_file", json.RawMessage(`{"path":"src/file.txt","content":"hello\nworld"}`)); err != nil {
		t.Fatal(err)
	}
	if out, err := fs.Execute("read_file", json.RawMessage(`{"path":"src/file.txt"}`)); err != nil || out != "hello\nworld" {
		t.Fatalf("workspace read = %q %v", out, err)
	}
	if _, err := fs.Execute("edit_file", json.RawMessage(`{"path":"src/file.txt","old_string":"world","new_string":"reader"}`)); err != nil {
		t.Fatal(err)
	}
	out, err := fs.Execute("grep", json.RawMessage(`{"pattern":"reader"}`))
	if err != nil || out != "src/file.txt:2:reader\n" {
		t.Fatalf("grep = %q %v", out, err)
	}
}
