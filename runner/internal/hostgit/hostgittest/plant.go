// Package hostgittest plants, in a repository, the configuration attacks the commit-lookup security
// review (#131) used against host git, and tells whether any of them took effect: a command a
// configuration key or a hook names ran, or a file a key names was opened. Tests only.
package hostgittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Hooks are the hook names git can run (a planted hooks directory has every one).
var Hooks = []string{
	"applypatch-msg", "pre-applypatch", "post-applypatch", "pre-commit", "pre-merge-commit", "prepare-commit-msg",
	"commit-msg", "post-commit", "pre-rebase", "post-checkout", "post-merge", "pre-push", "pre-receive", "update",
	"proc-receive", "post-receive", "post-update", "reference-transaction", "push-to-checkout", "pre-auto-gc",
	"post-rewrite", "sendemail-validate", "fsmonitor-watchman", "p4-changelist", "p4-prepare-changelist",
	"p4-post-changelist", "p4-pre-submit", "post-index-change",
}

// Probe watches what was planted.
type Probe struct {
	Marker string
	Script string
	fifos  []string
	mu     sync.Mutex
	opened []string
	stop   chan struct{}
	done   sync.WaitGroup
}

// Plant writes the attacks into the repository whose git directory is gitDir (a work tree's .git,
// or a bare repository) and starts watching. Call Check (it also stops the watch).
func Plant(t *testing.T, gitDir string) *Probe {
	t.Helper()
	tmp := t.TempDir()
	p := &Probe{Marker: filepath.Join(tmp, "ran"), Script: filepath.Join(tmp, "evil.sh"), stop: make(chan struct{})}
	if err := os.WriteFile(p.Script, []byte("#!/bin/sh\necho \"$0 $*\" >> '"+p.Marker+"'\nexit 0\n"), 0o700); err != nil { //nolint:gosec // test fixture script
		t.Fatal(err)
	}
	hooks := filepath.Join(tmp, "hooks")
	for _, dir := range []string{hooks, filepath.Join(gitDir, "hooks")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, h := range Hooks {
			if err := os.WriteFile(filepath.Join(dir, h), []byte("#!/bin/sh\necho \"hook "+h+"\" >> '"+p.Marker+"'\nexit 0\n"), 0o700); err != nil { //nolint:gosec // test fixture hook
				t.Fatal(err)
			}
		}
	}
	fifo := func(name string) string {
		path := filepath.Join(tmp, name)
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		p.fifos = append(p.fifos, path)
		return path
	}
	cmdline := p.Script + " #"
	for _, kv := range [][2]string{
		{"core.fsmonitor", cmdline}, {"core.hooksPath", hooks}, {"core.sshCommand", cmdline},
		{"core.pager", cmdline}, {"pager.show", cmdline}, {"pager.log", cmdline}, {"pager.diff", cmdline},
		{"core.editor", cmdline}, {"sequence.editor", cmdline}, {"core.askPass", p.Script},
		{"credential.helper", "!" + cmdline}, {"diff.external", p.Script},
		{"diff.evil.command", p.Script}, {"diff.evil.textconv", p.Script},
		{"filter.evil.clean", cmdline}, {"filter.evil.smudge", cmdline}, {"filter.evil.process", cmdline},
		{"log.showSignature", "true"}, {"gpg.program", p.Script}, {"gpg.ssh.program", p.Script}, {"gpg.x509.program", p.Script},
		{"core.alternateRefsCommand", cmdline}, {"uploadpack.packObjectsHook", cmdline},
		{"diff.submodule", "diff"}, {"submodule.recurse", "true"}, {"fetch.recurseSubmodules", "true"},
		{"protocol.ext.allow", "always"}, {"protocol.allow", "always"},
		{"diff.orderFile", fifo("orderfile")}, {"mailmap.file", fifo("mailmap")}, {"log.mailmap", "true"},
		{"core.attributesFile", fifo("attributes")}, {"blame.ignoreRevsFile", fifo("ignorerevs")},
		{"commit.template", fifo("template")},
		// Transport settings (#136 review, M9): what a fetch or push in the repository would use.
		{"http.sslCAInfo", fifo("cainfo")}, {"http.sslCert", fifo("sslcert")}, {"http.cookieFile", fifo("cookies")},
		{"http.saveCookies", "true"}, {"remote.origin.uploadpack", cmdline}, {"remote.origin.receivepack", cmdline},
	} {
		// A command names its key, so a run says which one.
		Config(t, gitDir, kv[0], strings.ReplaceAll(kv[1], cmdline, p.Script+" "+kv[0]+" #"))
	}
	p.watch()
	// The watch runs until the test ends: a later read of a planted file (a fixture's own git
	// after Check) is answered with an empty file, never left blocked.
	t.Cleanup(func() { close(p.stop); p.done.Wait() })
	return p
}

// Config sets key in gitDir's configuration file with a clean git (no hooks, no global config).
func Config(t *testing.T, gitDir, key, value string) {
	t.Helper()
	cmd := exec.Command("git", "config", "--file", filepath.Join(gitDir, "config"), key, value) //nolint:gosec,noctx // test fixture
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config %s: %v: %s", key, err, out)
	}
}

// Attributes writes a work tree's .gitattributes that routes every file to the planted diff and
// filter drivers.
func Attributes(t *testing.T, workTree string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workTree, ".gitattributes"), []byte("* diff=evil filter=evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// watch notices a planted file being opened: a reader on a FIFO lets a non-blocking writer open
// it; the writer then closes it at once, so the reader sees an empty file and never hangs.
func (p *Probe) watch() {
	for _, f := range p.fifos {
		p.done.Add(1)
		go func(path string) {
			defer p.done.Done()
			for {
				select {
				case <-p.stop:
					return
				default:
				}
				fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
				if err == nil {
					_ = syscall.Close(fd)
					p.mu.Lock()
					p.opened = append(p.opened, filepath.Base(path))
					p.mu.Unlock()
				}
				time.Sleep(2 * time.Millisecond)
			}
		}(f)
	}
}

// Check fails the test when a planted command or hook ran or a planted file was opened so far.
// It may be called more than once; the watch goes on until the test ends.
func (p *Probe) Check(t *testing.T) {
	t.Helper()
	time.Sleep(50 * time.Millisecond) // a background helper's last write
	p.mu.Lock()
	opened := append([]string(nil), p.opened...)
	p.mu.Unlock()
	if raw, err := os.ReadFile(p.Marker); err == nil {
		t.Errorf("a planted command or hook ran:\n%s", strings.TrimSpace(string(raw)))
	}
	if len(opened) > 0 {
		t.Errorf("a file named by the planted configuration was read: %v", opened)
	}
}
