package daemon

// Who may hold a person's token during a named clone, and how a clone is
// kept from holding up the daemon: a kill stops it at once, its own timeout
// is not doubled by an ssh retry, a stalled transfer aborts, nothing ever
// prompts, and a killed daemon's leftovers are swept at the next start.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A token clone on the bare host, from a daemon whose owner did not opt in,
// is refused before git runs: any other unsandboxed session on the machine
// could read the token out of git's environment.
func TestHostTokenCloneRefusedWithoutOptIn(t *testing.T) {
	calls := installFakeGit(t)
	root := t.TempDir()
	err := EnsureClonedTarget(context.Background(), root, CloneTarget{Provider: "github", FullName: "acme/app", Folder: "app", Token: "ghp_x"})
	if err == nil || !strings.Contains(err.Error(), "Local sandbox") {
		t.Fatalf("want a refusal pointing at Local sandbox, got %v", err)
	}
	if got := readCalls(t, calls); got != "" {
		t.Errorf("git ran for a refused token clone:\n%s", got)
	}

	// The same through a spawn on a default-configured daemon.
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root, Command: []string{"head", "-n", "1"}})
	raw, _ := json.Marshal(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-host", Repo: "app",
		Provider: "github", CloneFrom: "acme/app", GitToken: "ghp_x", Kind: "agent"})
	mgr.handleSpawnSession(raw)
	if msgs := sender.messages(); len(msgs) != 1 || !strings.Contains(msgs[0], "Local sandbox") {
		t.Errorf("want one refusal, got %v", msgs)
	}

	// A public clone (no token) still runs on the host as before.
	if err := EnsureClonedTarget(context.Background(), root, CloneTarget{Provider: "github", FullName: "octo/pub", Folder: "pub"}); err != nil {
		t.Errorf("public host clone: %v", err)
	}
}

// fakeCloneDocker stands in for `docker run … sh -c <script> clone <host> <url>`:
// it records argv and stdin, and makes the clone in the bind-mounted folder.
const fakeCloneDocker = `
d="$FAKE_DOCKER_DIR"
echo "ARGS $*" >> "$d/calls"
[ "$1" = run ] || exit 0
cat > "$d/stdin"
src=""; prev=""
for a in "$@"; do
  if [ "$prev" = "--mount" ]; then src=$(echo "$a" | sed -n 's/.*src=\([^,]*\),dst=\/clone.*/\1/p'); fi
  prev="$a"; last="$a"
done
mkdir -p "$src/repo/.git"
printf '[remote "origin"]\n\turl = %s\n' "$last" > "$src/repo/.git/config"
`

// A Local sandbox session's token clone runs inside the sandbox image, with
// the credential on the docker CLI's stdin — never in its argv or env — and
// no git runs on the host at all.
func TestSandboxTokenCloneRunsInContainer(t *testing.T) {
	gitCalls := installFakeGit(t)
	dir := writeScript(t, "docker", fakeCloneDocker)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_DIR", dir)

	const token = "glpat-SANDBOXED0123456789"
	root := t.TempDir()
	if err := EnsureClonedTarget(context.Background(), root, CloneTarget{
		Provider: "gitlab", FullName: "grp/tool", Folder: "tool", Token: token, Sandbox: true,
	}); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "calls"))
	enc := base64.StdEncoding.EncodeToString([]byte("oauth2:" + token))
	if strings.Contains(string(args), token) || strings.Contains(string(args), enc) {
		t.Errorf("the token reached docker's argv: %s", args)
	}
	for _, want := range []string{"--cap-drop ALL", "no-new-privileges", sandboxImage(), "gitlab.com https://gitlab.com/grp/tool.git"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("docker run is missing %q: %s", want, args)
		}
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if strings.TrimSpace(string(stdin)) != enc {
		t.Errorf("the credential should arrive on stdin, got %q", stdin)
	}
	if got := readCalls(t, gitCalls); got != "" {
		t.Errorf("a sandboxed token clone ran git on the host:\n%s", got)
	}
	cfg, err := os.ReadFile(filepath.Join(root, "tool", ".git", "config"))
	if err != nil || !strings.Contains(string(cfg), "https://gitlab.com/grp/tool.git") || strings.Contains(string(cfg), token) {
		t.Errorf("clone not in place, or token in its config (%v): %s", err, cfg)
	}
	assertNoCloneLeftovers(t, root)
}

// The in-container script keeps the credential out of every argv and env:
// the builtin printf writes it into a private HOME's .gitconfig.
func TestSandboxCloneScriptKeepsCredentialOutOfEnvAndArgv(t *testing.T) {
	if strings.Contains(sandboxCloneScript, "export cred") || strings.Contains(sandboxCloneScript, "GIT_CONFIG_VALUE") {
		t.Error("the script exports the credential")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	// Run the script for real with a fake git that dumps what it can see.
	gitDir := writeScript(t, "git", `env > "$OUT/env"; echo "$@" > "$OUT/argv"; cat "$HOME/.gitconfig" > "$OUT/cfg"; mkdir -p "$4"`)
	out := t.TempDir()
	script := strings.ReplaceAll(sandboxCloneScript, "/clone/repo", filepath.Join(out, "repo"))
	cmd := exec.Command(sh, "-c", script, "clone", "github.com", "https://github.com/a/b.git")
	cmd.Env = []string{"PATH=" + gitDir + ":/usr/bin:/bin", "OUT=" + out}
	cmd.Stdin = strings.NewReader("Q1JFRA==\n")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v: %s", err, b)
	}
	env, _ := os.ReadFile(filepath.Join(out, "env"))
	argv, _ := os.ReadFile(filepath.Join(out, "argv"))
	cfg, _ := os.ReadFile(filepath.Join(out, "cfg"))
	if strings.Contains(string(env), "Q1JFRA==") || strings.Contains(string(argv), "Q1JFRA==") {
		t.Errorf("credential in git's env or argv:\n%s\n%s", env, argv)
	}
	if !strings.Contains(string(cfg), `[http "https://github.com/"]`) || !strings.Contains(string(cfg), "extraHeader = Authorization: Basic Q1JFRA==") {
		t.Errorf("header config not written for the host: %s", cfg)
	}
}

// procGone reports whether pid has exited (absent, or a zombie waiting for
// a reaper that is not us).
func procGone(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	fields := strings.Fields(string(b)[strings.LastIndex(string(b), ")")+1:])
	return len(fields) > 0 && fields[0] == "Z"
}

// A kill_session sent while a spawn is cloning stops the clone — git and the
// helper it forked — at once, instead of after the clone finishes; the spawn
// is abandoned quietly and leaves nothing behind.
func TestKillStopsACloneInProgress(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	installFakeGit(t)
	t.Setenv("FAKE_GIT_SLOW", "1")
	root := t.TempDir()
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root, Command: []string{"head", "-n", "1"}})
	raw, _ := json.Marshal(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-slow", Repo: "big",
		Provider: "github", CloneFrom: "torvalds/big", Kind: "agent"})
	done := make(chan struct{})
	go func() { mgr.handleSpawnSession(raw); close(done) }()

	pidFile := filepath.Join(os.Getenv("FAKE_GIT_DIR"), "pids")
	var pids []int
	deadline := time.Now().Add(10 * time.Second)
	for len(pids) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the clone never started")
		}
		if b, err := os.ReadFile(pidFile); err == nil {
			pids = nil
			for _, f := range strings.Fields(string(b)) {
				n, _ := strconv.Atoi(f)
				pids = append(pids, n)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	start := time.Now()
	kill, _ := json.Marshal(protocol.KillSession{Type: "kill_session", SessionID: "s-slow"})
	mgr.handleKillSession(kill)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the spawn was still cloning 10s after its kill")
	}
	if d := time.Since(start); d > 7*time.Second {
		t.Errorf("the kill took %s to stop the clone", d)
	}
	for _, pid := range pids {
		deadline := time.Now().Add(3 * time.Second)
		for !procGone(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if !procGone(pid) {
			t.Errorf("process %d from the clone is still running", pid)
		}
	}
	for _, m := range sender.messages() {
		if strings.Contains(m, `"status":"error"`) {
			t.Errorf("a killed spawn reported an error: %s", m)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "big")); !os.IsNotExist(err) {
		t.Error("a killed clone left its folder")
	}
	assertNoCloneLeftovers(t, root)
}

// A clone that runs out its own timeout over HTTPS is not tried again over
// ssh (which would hold every queued spawn for as long again).
func TestHTTPSTimeoutSkipsSSHRetry(t *testing.T) {
	calls := installFakeGit(t)
	t.Setenv("FAKE_GIT_SLOW", "1")
	prev := cloneTargetTimeout
	cloneTargetTimeout = 300 * time.Millisecond
	t.Cleanup(func() { cloneTargetTimeout = prev })

	start := time.Now()
	err := EnsureClonedTarget(context.Background(), t.TempDir(), CloneTarget{Provider: "github", FullName: "torvalds/linux", Folder: "linux"})
	if err == nil || !strings.Contains(err.Error(), "in time") {
		t.Fatalf("want a timeout error, got %v", err)
	}
	if d := time.Since(start); d > 7*time.Second {
		t.Errorf("a 300ms attempt took %s", d)
	}
	got := readCalls(t, calls)
	if strings.Count(got, "ARGS clone") != 1 || strings.Contains(got, "git@github.com:") {
		t.Errorf("ssh was tried after the HTTPS timeout:\n%s", got)
	}

	// Any other HTTPS failure (auth, not found) still falls back to ssh.
	calls = installFakeGit(t)
	t.Setenv("FAKE_GIT_FAIL", "https://")
	if err := EnsureClonedTarget(context.Background(), t.TempDir(), CloneTarget{Provider: "github", FullName: "acme/app", Folder: "app"}); err != nil {
		t.Fatalf("ssh fallback: %v", err)
	}
	if !strings.Contains(readCalls(t, calls), "git@github.com:acme/app.git") {
		t.Error("no ssh fallback after an HTTPS failure")
	}
}

// Every clone attempt runs with no prompt and ssh in BatchMode.
func TestCloneNeverPrompts(t *testing.T) {
	calls := installFakeGit(t)
	if err := EnsureClonedTarget(context.Background(), t.TempDir(), CloneTarget{Provider: "github", FullName: "octo/tiny", Folder: "tiny"}); err != nil {
		t.Fatal(err)
	}
	got := readCalls(t, calls)
	for _, want := range []string{
		"ENV GIT_TERMINAL_PROMPT=0",
		"ENV GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=",
		"ENV GIT_HTTP_LOW_SPEED_LIMIT=",
		"ENV GIT_HTTP_LOW_SPEED_TIME=",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("clone env is missing %q:\n%s", want, got)
		}
	}
}

// A real git against a server that answers and then stalls with the
// connection open: the low-speed limit aborts it long before the attempt's
// timeout.
func TestStalledTransferAborts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte("001e# service=git-upload-pack\n"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(60 * time.Second):
		}
	}))
	defer srv.Close()
	prevT, prevL := lowSpeedTime, lowSpeedLimit
	lowSpeedTime, lowSpeedLimit = "2", "1000"
	t.Cleanup(func() { lowSpeedTime, lowSpeedLimit = prevT, prevL })

	start := time.Now()
	err := gitClone(context.Background(), 45*time.Second, srv.URL+"/r.git", filepath.Join(t.TempDir(), "r"), nil)
	if err == nil {
		t.Fatal("a stalled clone succeeded")
	}
	if errors.Is(err, errCloneTimedOut) {
		t.Fatalf("the stall ran out the attempt timeout instead of the low-speed limit: %v", err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("the stall took %s to abort", d)
	}
}

// git older than 2.31 would ignore GIT_CONFIG_COUNT and fail as if the token
// were wrong: say what is actually missing, before cloning.
func TestOldHostGitRefusesTokenCloneClearly(t *testing.T) {
	calls := installFakeGit(t)
	t.Setenv("FAKE_GIT_VERSION", "2.30.9")
	err := EnsureClonedTarget(context.Background(), t.TempDir(), CloneTarget{Provider: "github", FullName: "acme/app", Folder: "app", Token: "t", HostTokenAllowed: true})
	if err == nil || !strings.Contains(err.Error(), "git 2.31 or newer") || !strings.Contains(err.Error(), "2.30.9") {
		t.Fatalf("want a git-version refusal, got %v", err)
	}
	if strings.Contains(readCalls(t, calls), "ARGS clone") {
		t.Error("cloned anyway")
	}
	// A public clone does not need the new git.
	if err := EnsureClonedTarget(context.Background(), t.TempDir(), CloneTarget{Provider: "github", FullName: "acme/pub", Folder: "pub"}); err != nil {
		t.Errorf("public clone on old git: %v", err)
	}

	for s, want := range map[string][3]int{
		"git version 2.34.1\n":                 {2, 34, 1},
		"git version 2.39.3 (Apple Git-146)\n": {2, 39, 1},
		"git version 1.9.5":                    {1, 9, 1},
		"nonsense":                             {0, 0, 0},
	} {
		ma, mi, ok := parseGitVersion(s)
		if ma != want[0] || mi != want[1] || ok != (want[2] == 1) {
			t.Errorf("parseGitVersion(%q) = %d.%d %v", s, ma, mi, ok)
		}
	}
}

// A daemon start removes the hidden clone folders a killed daemon left, and
// nothing else.
func TestSweepCloneLeftovers(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-3 * time.Hour)
	for _, d := range []string{".blerg-clone-old", ".blerg-clone-fresh", "project", ".blerg-runner"} {
		mustMkdir(t, filepath.Join(root, d, "x"))
		if d != ".blerg-clone-fresh" {
			if err := os.Chtimes(filepath.Join(root, d), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	sweepCloneLeftovers(root, time.Now())
	for d, keep := range map[string]bool{".blerg-clone-old": false, ".blerg-clone-fresh": true, "project": true, ".blerg-runner": true} {
		_, err := os.Stat(filepath.Join(root, d))
		if keep != (err == nil) {
			t.Errorf("%s: kept=%v, want %v", d, err == nil, keep)
		}
	}
}
