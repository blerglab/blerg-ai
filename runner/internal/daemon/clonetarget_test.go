package daemon

// A spawn's CloneFrom: clone a repository the caller named — listed or not,
// public or private — into one folder under the repos root. git itself is a
// fake on PATH here: it records its argv and GIT_* environment, checks the
// Authorization header when told to, and writes an origin like a real clone.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const fakeGit = `
if [ "$1" = "--version" ]; then echo "git version ${FAKE_GIT_VERSION:-2.40.0}"; exit 0; fi
calls="$FAKE_GIT_DIR/calls"
echo "ARGS $*" >> "$calls"
if [ -n "$FAKE_GIT_SLOW" ]; then
  # A clone that never finishes, with a helper child the way git forks
  # git-remote-https: both must die on a kill.
  sleep 300 &
  echo "$$ $!" > "$FAKE_GIT_DIR/pids"
  wait
  exit 1
fi
env | grep '^GIT_' | sed 's/^/ENV /' >> "$calls"
[ "$1" = clone ] || exit 2
remote="$3"; dest="$4"
if [ -n "$FAKE_GIT_FAIL" ]; then
  case "$remote" in
    *$FAKE_GIT_FAIL*)
      # A git that echoes more than it should: the header and the token.
      echo "fatal: Authentication failed for '$remote' ($GIT_CONFIG_VALUE_0) $FAKE_GIT_ECHO" >&2
      exit 128 ;;
  esac
fi
if [ -n "$FAKE_GIT_WANT_HEADER" ] && [ "$GIT_CONFIG_VALUE_0" != "$FAKE_GIT_WANT_HEADER" ]; then
  echo "fatal: could not read Username: terminal prompts disabled" >&2
  exit 128
fi
mkdir -p "$dest/.git"
printf '[remote "origin"]\n\turl = %s\n' "$remote" > "$dest/.git/config"
`

// installFakeGit puts the fake git first on PATH and returns the file its
// calls are recorded in.
func installFakeGit(t *testing.T) string {
	t.Helper()
	dir := writeScript(t, "git", fakeGit)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_GIT_DIR", dir)
	t.Setenv("FAKE_GIT_FAIL", "")
	t.Setenv("FAKE_GIT_WANT_HEADER", "")
	t.Setenv("FAKE_GIT_ECHO", "")
	t.Setenv("FAKE_GIT_SLOW", "")
	t.Setenv("FAKE_GIT_VERSION", "")
	return filepath.Join(dir, "calls")
}

func readCalls(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func writeOrigin(t *testing.T, dir, url string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[remote \"origin\"]\n\turl = "+url+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func basicHeader(user, token string) string {
	return "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+token))
}

// No leftover hidden clone directories after any outcome.
func assertNoCloneLeftovers(t *testing.T, root string) {
	t.Helper()
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".blerg-clone-") {
			t.Errorf("clone leftover %s under the repos root", e.Name())
		}
	}
}

// A public repository needs no token, on either provider: one anonymous
// HTTPS clone to the provider's own host, no auth config at all.
func TestEnsureClonedTargetPublicNoToken(t *testing.T) {
	for _, c := range []struct{ provider, full, folder, url string }{
		{"github", "torvalds/linux", "linux", "https://github.com/torvalds/linux.git"},
		{"gitlab", "gitlab-org/gitlab-runner", "gitlab-runner", "https://gitlab.com/gitlab-org/gitlab-runner.git"},
	} {
		calls := installFakeGit(t)
		root := t.TempDir()
		if err := EnsureClonedTarget(context.Background(), root, CloneTarget{Provider: c.provider, FullName: c.full, Folder: c.folder}); err != nil {
			t.Fatalf("%s: %v", c.full, err)
		}
		got := readCalls(t, calls)
		if strings.Count(got, "ARGS clone") != 1 || !strings.Contains(got, "ARGS clone -- "+c.url+" ") {
			t.Errorf("%s: want one anonymous clone of %s, got:\n%s", c.full, c.url, got)
		}
		if strings.Contains(got, "GIT_CONFIG") {
			t.Errorf("%s: a public clone carried auth config:\n%s", c.full, got)
		}
		if !strings.Contains(got, "ENV GIT_TERMINAL_PROMPT=0") {
			t.Errorf("%s: a clone must never prompt:\n%s", c.full, got)
		}
		b, err := os.ReadFile(filepath.Join(root, c.folder, ".git", "config"))
		if err != nil || !strings.Contains(string(b), c.url) {
			t.Errorf("%s: folder %s is not the clone (%v): %s", c.full, c.folder, err, b)
		}
		assertNoCloneLeftovers(t, root)
	}
}

// A private repository: the caller's token goes as a Basic Authorization
// header scoped to the provider's host, through the environment only — never
// in argv, the URL or the clone's .git/config.
func TestEnsureClonedTargetPrivateWithToken(t *testing.T) {
	for _, c := range []struct{ provider, user, host, full, folder string }{
		{"github", "x-access-token", "github.com", "acme/secret-app", "secret-app"},
		{"gitlab", "oauth2", "gitlab.com", "grp/tool", "tool"},
	} {
		calls := installFakeGit(t)
		const token = "glpat-PRIVATE-0123456789"
		t.Setenv("FAKE_GIT_WANT_HEADER", basicHeader(c.user, token))
		root := t.TempDir()
		if err := EnsureClonedTarget(context.Background(), root, CloneTarget{Provider: c.provider, FullName: c.full, Folder: c.folder, Token: token, HostTokenAllowed: true}); err != nil {
			t.Fatalf("%s: %v", c.full, err)
		}
		got := readCalls(t, calls)
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, "ARGS") && (strings.Contains(line, token) || strings.Contains(line, "@")) {
				t.Errorf("%s: argv carries credentials: %s", c.full, line)
			}
		}
		if !strings.Contains(got, "ENV GIT_CONFIG_KEY_0=http.https://"+c.host+"/.extraHeader") {
			t.Errorf("%s: the header must be scoped to %s:\n%s", c.full, c.host, got)
		}
		cfg, _ := os.ReadFile(filepath.Join(root, c.folder, ".git", "config"))
		if strings.Contains(string(cfg), token) || strings.Contains(string(cfg), base64.StdEncoding.EncodeToString([]byte(c.user+":"+token))) {
			t.Errorf("%s: the token was written into .git/config: %s", c.full, cfg)
		}
		if !strings.Contains(string(cfg), "https://"+c.host+"/"+c.full+".git") {
			t.Errorf("%s: origin should be the token-free HTTPS URL: %s", c.full, cfg)
		}
	}
}

// An existing folder is used only when it already is the repository; any
// other folder of that name — another repository, another provider's, one
// with no origin — is refused untouched, and git never runs.
func TestEnsureClonedTargetFolderCollision(t *testing.T) {
	calls := installFakeGit(t)
	root := t.TempDir()

	writeOrigin(t, filepath.Join(root, "linux"), "git@github.com:someone/linux.git")
	err := EnsureClonedTarget(context.Background(), root, CloneTarget{Provider: "github", FullName: "torvalds/linux", Folder: "linux", Token: "tok"})
	if err == nil || !strings.Contains(err.Error(), "someone/linux") || !strings.Contains(err.Error(), "torvalds/linux") {
		t.Errorf("a different repository's folder was not refused clearly: %v", err)
	}

	writeOrigin(t, filepath.Join(root, "tool"), "https://github.com/grp/tool.git")
	if err := EnsureClonedTarget(context.Background(), root, CloneTarget{Provider: "gitlab", FullName: "grp/tool", Folder: "tool"}); err == nil {
		t.Error("GitHub's grp/tool was accepted as GitLab's")
	}

	mustMkdir(t, filepath.Join(root, "plain"))
	if err := os.WriteFile(filepath.Join(root, "plain", "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureClonedTarget(context.Background(), root, CloneTarget{Provider: "github", FullName: "octo/plain", Folder: "plain"}); err == nil {
		t.Error("a folder with no origin was cloned into")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "plain", "notes.txt")); string(b) != "mine" {
		t.Error("the unrelated folder was touched")
	}

	// The same repository, spelt with different case: used as it is.
	writeOrigin(t, filepath.Join(root, "same"), "git@github.com:Octo/Same.git")
	if err := EnsureClonedTarget(context.Background(), root, CloneTarget{Provider: "github", FullName: "octo/same", Folder: "same", Token: "tok"}); err != nil {
		t.Errorf("the folder that already is the repository was refused: %v", err)
	}

	if got := readCalls(t, calls); got != "" {
		t.Errorf("git ran for a folder that exists:\n%s", got)
	}

	for _, bad := range []string{"", "a/b", "..", ".hidden", "a\\b"} {
		if err := EnsureClonedTarget(context.Background(), root, CloneTarget{Provider: "github", FullName: "octo/x", Folder: bad}); err == nil {
			t.Errorf("folder %q accepted", bad)
		}
	}
	for _, bad := range []CloneTarget{
		{Provider: "", FullName: "octo/x", Folder: "x"},
		{Provider: "bitbucket", FullName: "octo/x", Folder: "x"},
		{Provider: "github", FullName: "x", Folder: "x"},
	} {
		if err := EnsureClonedTarget(context.Background(), root, bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

// HTTPS fails → the machine's own ssh access is tried (without the token);
// both fail → one error naming the repository, with the token scrubbed even
// from a git that echoed it, and nothing left behind.
func TestEnsureClonedTargetFallbackAndFailure(t *testing.T) {
	calls := installFakeGit(t)
	root := t.TempDir()
	t.Setenv("FAKE_GIT_FAIL", "https://")
	if err := EnsureClonedTarget(context.Background(), root, CloneTarget{Provider: "github", FullName: "acme/app", Folder: "app", Token: "tok-123", HostTokenAllowed: true}); err != nil {
		t.Fatalf("ssh fallback: %v", err)
	}
	got := readCalls(t, calls)
	if !strings.Contains(got, "ARGS clone -- git@github.com:acme/app.git ") {
		t.Fatalf("no ssh fallback:\n%s", got)
	}
	sshAt := strings.Index(got, "ARGS clone -- git@")
	if sshAt < 0 {
		t.Fatalf("no ssh clone in the calls:\n%s", got)
	}
	sshPart := got[sshAt:]
	if strings.Contains(sshPart, "GIT_CONFIG") {
		t.Errorf("the ssh attempt carried the token header:\n%s", sshPart)
	}
	assertNoCloneLeftovers(t, root)

	const token = "ghp_LEAKY0123456789"
	t.Setenv("FAKE_GIT_FAIL", "acme")
	t.Setenv("FAKE_GIT_ECHO", token)
	err := EnsureClonedTarget(context.Background(), root, CloneTarget{Provider: "github", FullName: "acme/other", Folder: "other", Token: token, HostTokenAllowed: true})
	if err == nil {
		t.Fatal("both attempts failed but no error")
	}
	enc := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), enc) {
		t.Errorf("the token reached the error: %v", err)
	}
	if !strings.Contains(err.Error(), "acme/other") {
		t.Errorf("the error should name the repository: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "other")); !os.IsNotExist(statErr) {
		t.Error("a failed clone left the destination folder behind")
	}
	assertNoCloneLeftovers(t, root)
}

// The spawn path end to end, on a real (private) tmux server: the repository
// is cloned into the folder, the session starts there, and the token is in
// none of what outlives the clone — the recovery record, the session's own
// environment, the clone's config, the messages sent to the server, the log.
func TestSpawnCloneFromNeverKeepsTheToken(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	var rb [4]byte
	_, _ = rand.Read(rb[:])
	sock := "blerg-test-" + hex.EncodeToString(rb[:])
	restore := setTmuxSocketArgs([]string{"-L", sock})
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", sock, "kill-server").Run()
		restore()
	})

	calls := installFakeGit(t)
	const token = "ghp_SPAWNSECRET0123456789"
	t.Setenv("FAKE_GIT_WANT_HEADER", basicHeader("x-access-token", token))
	envOut := filepath.Join(t.TempDir(), "session.env")
	claudeDir := writeScript(t, "claude", "printenv > "+envOut+"\nsleep 60\n")
	t.Setenv("PATH", claudeDir+":"+os.Getenv("PATH"))

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	root := t.TempDir()
	sender := newRecordingSender(64)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root, AllowHostCredentialClone: true})
	raw, _ := json.Marshal(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-clone-" + sock, Repo: "private-app",
		Provider: "github", CloneFrom: "acme/private-app", GitToken: token, Cols: 80, Rows: 24,
	})
	mgr.handleSpawnSession(raw)
	t.Cleanup(func() {
		mgr.handleKillSession(mustJSON(t, protocol.KillSession{Type: "kill_session", SessionID: "s-clone-" + sock}))
	})

	var started bool
	for _, m := range sender.messages() {
		if strings.Contains(m, `"session_started"`) && strings.Contains(m, `"repo":"private-app"`) {
			started = true
		}
		if strings.Contains(m, token) {
			t.Errorf("the token reached a message to the server: %s", m)
		}
	}
	if !started {
		t.Fatalf("no session_started for the cloned folder: %v\ngit calls:\n%s", sender.messages(), readCalls(t, calls))
	}

	rec, err := os.ReadFile(recordPath(root, "s-clone-"+sock))
	if err != nil {
		t.Fatalf("recovery record: %v", err)
	}
	if strings.Contains(string(rec), token) {
		t.Errorf("the token was persisted in the recovery record: %s", rec)
	}
	cfg, _ := os.ReadFile(filepath.Join(root, "private-app", ".git", "config"))
	if strings.Contains(string(cfg), token) {
		t.Errorf("the token was written into .git/config: %s", cfg)
	}
	env := waitForEnvFile(t, envOut)
	for k, v := range env {
		if strings.Contains(v, token) || strings.HasPrefix(k, "GIT_CONFIG") {
			t.Errorf("the session's environment carries the clone's credential: %s=%s", k, v)
		}
	}
	if strings.Contains(logBuf.String(), token) {
		t.Errorf("the token was logged:\n%s", logBuf.String())
	}
}

// Nothing a daemon persists about a session has a place for a git token.
func TestSessionRecordHasNoGitTokenField(t *testing.T) {
	rt := reflect.TypeOf(SessionRecord{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if strings.Contains(strings.ToLower(f.Name), "git") || strings.Contains(f.Tag.Get("json"), "git") {
			t.Errorf("SessionRecord.%s would persist git credentials", f.Name)
		}
	}
}

// A failed named clone's error is what the server records as the session's
// error reason: it names the repository and never carries the token.
func TestSpawnCloneFromFailureReportsWithoutToken(t *testing.T) {
	installFakeGit(t)
	const token = "ghp_FAILSECRET0123456789"
	t.Setenv("FAKE_GIT_FAIL", "acme")
	t.Setenv("FAKE_GIT_ECHO", token)
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	root := t.TempDir()
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root, AllowHostCredentialClone: true, Command: []string{"head", "-n", "1"}})
	raw, _ := json.Marshal(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-clone-fail", Repo: "app",
		Provider: "github", CloneFrom: "acme/app", GitToken: token, Kind: "agent",
	})
	mgr.handleSpawnSession(raw)
	msgs := sender.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], `"status":"error"`) || !strings.Contains(msgs[0], "acme/app") {
		t.Fatalf("want one error naming the repository, got %v", msgs)
	}
	if strings.Contains(msgs[0], token) || strings.Contains(logBuf.String(), token) {
		t.Errorf("the token leaked:\nmessage: %s\nlog: %s", msgs[0], logBuf.String())
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
