package daemon

import (
	"encoding/base64"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func workspaceFixture(t *testing.T, root, name, body string) string {
	t.Helper()
	full := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return full
}
func workspaceLink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}
func TestWorkspaceFileReadAndClassification(t *testing.T) {
	root := t.TempDir()
	workspaceFixture(t, root, "docs/README.md", "# Hello\n")
	got := readWorkspaceFile(root, "./docs//README.md", workspaceFileLimits{})
	body, err := base64.StdEncoding.DecodeString(got.ContentB64)
	if !got.OK || got.Path != "docs/README.md" || got.View != "markdown" || got.ContentType != "text/markdown; charset=utf-8" || string(body) != "# Hello\n" || got.Size != 8 || got.Truncated || err != nil {
		t.Fatalf("read result %+v decode %v", got, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, got.Mtime); err != nil {
		t.Fatal(err)
	}
	workspaceLink(t, "docs/README.md", filepath.Join(root, "alias.md"))
	if got = readWorkspaceFile(root, "alias.md", workspaceFileLimits{}); got.OK || got.ErrorCode != "forbidden" {
		t.Fatalf("internal symlink was followed: %+v", got)
	}
	workspaceLink(t, filepath.Join(root, "docs/README.md"), filepath.Join(root, "absolute-internal.md"))
	if got = readWorkspaceFile(root, "absolute-internal.md", workspaceFileLimits{}); got.OK || got.ErrorCode != "forbidden" {
		t.Fatalf("absolute internal symlink was followed: %+v", got)
	}
}

// Direct chat references do not inherit explorer/search visibility: benign dot
// folders and git-ignored output are readable, while protected names stay closed.
func TestWorkspaceFileOpensBenignDotAndIgnoredFiles(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", root).CombinedOutput(); err != nil {
		t.Fatalf("initialize workspace repository: %v: %s", err, out)
	}
	workspaceFixture(t, root, ".gitignore", ".agent-notes/\nattachments/\n")
	for _, name := range []string{".agent-notes/report.md", "attachments/report.md", ".notes/report.md"} {
		workspaceFixture(t, root, name, "# Report\n")
		got := readWorkspaceFile(root, name, workspaceFileLimits{})
		body, err := base64.StdEncoding.DecodeString(got.ContentB64)
		if !got.OK || got.Path != name || err != nil || string(body) != "# Report\n" {
			t.Fatalf("benign file reference refused: %s: %+v %v", name, got, err)
		}
	}
	ignored, err := exec.Command("git", "-C", root, "check-ignore", ".agent-notes/report.md", "attachments/report.md").CombinedOutput()
	if err != nil || strings.TrimSpace(string(ignored)) != ".agent-notes/report.md\nattachments/report.md" {
		t.Fatalf("fixture files were not git-ignored: %v: %s", err, ignored)
	}
	for _, name := range []string{".env", ".ssh/report.md", ".git/config"} {
		workspaceFixture(t, root, name, "protected")
		got := readWorkspaceFile(root, name, workspaceFileLimits{})
		if got.OK || got.ErrorCode != "forbidden" || !strings.Contains(got.Error, "hold secrets") || got.ContentB64 != "" {
			t.Fatalf("protected file lacked a clear refusal: %+v", got)
		}
	}
}

func TestWorkspaceFileRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"", ".", "..", "../outside", "a/../file", "/etc/passwd", "~/file", "~person/file", "a\\b", "a\x00b", "a\nb", "C:/file", "%2e%2e/file", "%252e%252e/file", "a/%2E%2E/file", "%2fetc/passwd", "%5cfile"} {
		t.Run(path, func(t *testing.T) {
			got := readWorkspaceFile(root, path, workspaceFileLimits{})
			if got.OK || got.ErrorCode != "forbidden" || got.ContentB64 != "" {
				t.Fatalf("path accepted: %+v", got)
			}
		})
	}
	got := readWorkspaceFile(root, "missing.md", workspaceFileLimits{})
	if got.ErrorCode != "not_found" {
		t.Fatalf("missing: %+v", got)
	}
}
func TestWorkspaceFileSecretPatterns(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".env", ".ENV.prod", "tls.pem", "TLS.KEY", "vault.p12", "vault.PFX", "id_rsa", "id_rsa.pub", "id_ed25519-backup", "vault.kdbx", ".netrc", ".NPMRC", ".pypirc", ".git-credentials", "credentials.json", "CREDENTIALS-backup", "my-secret.txt", "SECRET", ".git/config", ".ssh/config", ".aws/config", ".gnupg/data", ".kube/config", ".docker/config.json", ".claude/settings.json", ".codex/auth.json", "id_ecdsa", "id_dsa.pub", "putty.ppk", ".pgpass", ".htpasswd", ".vault-token", "kubeconfig", "main.tfstate", "prod.tfstate.backup", "app.jks", "release.keystore", "secrets/db.yaml", "\u017fecret.txt", "\u212Aubeconfig"} {
		t.Run(name, func(t *testing.T) {
			workspaceFixture(t, root, name, "private fixture")
			got := readWorkspaceFile(root, name, workspaceFileLimits{})
			if got.OK || got.ErrorCode != "forbidden" || !strings.HasPrefix(got.Error, "Blerg doesn't open files that usually hold secrets (") || got.ContentB64 != "" {
				t.Fatalf("secret accepted: %+v", got)
			}
		})
	}
	workspaceFixture(t, root, "secret-project/readme.md", "public")
	if got := readWorkspaceFile(root, "secret-project/readme.md", workspaceFileLimits{}); !got.OK {
		t.Fatalf("ordinary directory name rejected: %+v", got)
	}
}
func TestWorkspaceFileSymlinkEscapesAndSecretAliases(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	workspaceFixture(t, outside, "outside.md", "outside fixture")
	workspaceFixture(t, root, "public.md", "public fixture")
	workspaceFixture(t, root, ".env", "private fixture")
	workspaceLink(t, "/etc/passwd", filepath.Join(root, "system.md"))
	workspaceLink(t, outside, filepath.Join(root, "out"))
	workspaceLink(t, ".env", filepath.Join(root, "alias.md"))
	workspaceLink(t, "public.md", filepath.Join(root, "secret-link"))
	workspaceLink(t, "secret-link", filepath.Join(root, "chain.md"))
	workspaceFixture(t, root, ".ssh/config", "private fixture")
	workspaceLink(t, ".ssh", filepath.Join(root, "ssh-alias"))
	for _, name := range []string{"system.md", "out/outside.md", "alias.md", "chain.md", "ssh-alias/config"} {
		t.Run(name, func(t *testing.T) {
			got := readWorkspaceFile(root, name, workspaceFileLimits{})
			if got.OK || got.ErrorCode != "forbidden" || got.ContentB64 != "" {
				t.Fatalf("unsafe link: %+v", got)
			}
		})
	}
}
func TestWorkspaceFileRegularOnlyAndHardlinks(t *testing.T) {
	root := t.TempDir()
	workspaceFixture(t, root, "dir/file.md", "fixture")
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root) // Keep the AF_UNIX bind pathname short in deep worktrees.
	listener, err := net.Listen("unix", "socket")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	outside := workspaceFixture(t, t.TempDir(), "outside.md", "private fixture")
	if err := os.Link(outside, filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dir", "pipe", "socket", "linked.md"} {
		t.Run(name, func(t *testing.T) {
			started := time.Now()
			got := readWorkspaceFile(root, name, workspaceFileLimits{})
			if got.OK || got.ErrorCode != "forbidden" || got.ContentB64 != "" || time.Since(started) > time.Second {
				t.Fatalf("nonregular/hardlink: %+v", got)
			}
		})
	}
}
func TestWorkspaceFileSwapsBetweenCheckAndOpen(t *testing.T) {
	for _, kind := range []string{"final symlink", "parent escape", "root replacement", "parent secret alias", "fifo swap"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "workspace")
			outside := t.TempDir()
			workspaceFixture(t, root, "docs/readme.md", "public")
			workspaceFixture(t, outside, "readme.md", "private fixture")
			path := "docs/readme.md"
			limits := workspaceFileLimits{beforeOpen: func() {
				switch kind {
				case "final symlink":
					if err := os.Rename(filepath.Join(root, path), filepath.Join(root, "backup.md")); err != nil {
						t.Fatal(err)
					}
					workspaceLink(t, "../backup.md", filepath.Join(root, path))
				case "parent escape":
					if err := os.Rename(filepath.Join(root, "docs"), filepath.Join(root, "old")); err != nil {
						t.Fatal(err)
					}
					workspaceLink(t, outside, filepath.Join(root, "docs"))
				case "root replacement":
					if err := os.Rename(root, root+"-old"); err != nil {
						t.Fatal(err)
					}
					workspaceFixture(t, root, path, "private fixture")
				case "parent secret alias":
					if err := os.Rename(filepath.Join(root, "docs"), filepath.Join(root, ".aws")); err != nil {
						t.Fatal(err)
					}
					workspaceLink(t, ".aws", filepath.Join(root, "docs"))
				case "fifo swap":
					if err := os.Remove(filepath.Join(root, path)); err != nil {
						t.Fatal(err)
					}
					if err := syscall.Mkfifo(filepath.Join(root, path), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}}
			started := time.Now()
			got := readWorkspaceFile(root, path, limits)
			if got.OK || got.ErrorCode != "forbidden" || got.ContentB64 != "" || time.Since(started) > time.Second {
				t.Fatalf("swap escaped: %+v", got)
			}
		})
	}
}
func TestWorkspaceFileBoundsAndGrowingFiles(t *testing.T) {
	root := t.TempDir()
	workspaceFixture(t, root, "small.txt", strings.Repeat("a", 32))
	workspaceFixture(t, root, "large.txt", strings.Repeat("a", 33))
	limits := workspaceFileLimits{Text: 32, Media: 64}
	if got := readWorkspaceFile(root, "small.txt", limits); !got.OK {
		t.Fatalf("exact cap: %+v", got)
	}
	if got := readWorkspaceFile(root, "large.txt", limits); got.ErrorCode != "too_large" || got.ContentB64 != "" || got.Size != 33 {
		t.Fatalf("cap+1: %+v", got)
	}
	workspaceFixture(t, root, "image.png", "\x89PNG\r\n\x1a\n"+strings.Repeat("a", 48))
	if got := readWorkspaceFile(root, "image.png", limits); !got.OK || got.View != "image" {
		t.Fatalf("media cap: %+v", got)
	}
	workspaceFixture(t, root, "growth.txt", "small")
	limits.afterOpen = func() { workspaceFixture(t, root, "growth.txt", strings.Repeat("a", 80)) }
	if got := readWorkspaceFile(root, "growth.txt", limits); got.OK || got.ContentB64 != "" {
		t.Fatalf("growing file bypassed cap: %+v", got)
	}
}
func TestWorkspaceFileLimitsEnvironment(t *testing.T) {
	t.Setenv("BLERG_RUNNER_WORKSPACE_TEXT_MAX_BYTES", "")
	t.Setenv("BLERG_RUNNER_WORKSPACE_MEDIA_MAX_BYTES", "")
	if got := parseWorkspaceFileLimits(); got.Text != 2<<20 || got.Media != 25<<20 {
		t.Fatalf("defaults: %+v", got)
	}
	t.Setenv("BLERG_RUNNER_WORKSPACE_TEXT_MAX_BYTES", "invalid")
	t.Setenv("BLERG_RUNNER_WORKSPACE_MEDIA_MAX_BYTES", "-1")
	if got := parseWorkspaceFileLimits(); got.Text != 2<<20 || got.Media != 25<<20 {
		t.Fatalf("invalid defaults: %+v", got)
	}
	t.Setenv("BLERG_RUNNER_WORKSPACE_TEXT_MAX_BYTES", "33")
	t.Setenv("BLERG_RUNNER_WORKSPACE_MEDIA_MAX_BYTES", "999999999")
	if got := parseWorkspaceFileLimits(); got.Text != 33 || got.Media != 25<<20 {
		t.Fatalf("custom/clamped: %+v", got)
	}
}
