package runner

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeBareRepo creates a bare origin with one commit on main.
func makeBareRepo(t *testing.T) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	seed := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(seed, "init", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(seed, "add", ".")
	run(seed, "commit", "-m", "init")
	run(seed, "clone", "--bare", ".", bare)
	return bare
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitRun(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

func TestWriteCodexAuthFromEnvWritesFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_AUTH_JSON", `{"tokens":{"access_token":"tok"}}`)
	if err := writeCodexAuthFromEnv(home); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"tokens":{"access_token":"tok"}}` {
		t.Errorf("auth.json contents = %q", data)
	}
}

func TestWriteCodexAuthFromEnvNoopWhenUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_AUTH_JSON", "")
	if err := writeCodexAuthFromEnv(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Error("expected no .codex dir when CODEX_AUTH_JSON is unset")
	}
}

func TestWriteHermesEnvFromEnvWritesFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HERMES_ENV_CONTENTS", "OPENROUTER_API_KEY=sk-test\n")
	if err := writeHermesEnvFromEnv(home); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".hermes", ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "OPENROUTER_API_KEY=sk-test\n" {
		t.Errorf(".env contents = %q", data)
	}
}

func TestWriteHermesEnvFromEnvNoopWhenUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HERMES_ENV_CONTENTS", "")
	if err := writeHermesEnvFromEnv(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".hermes")); !os.IsNotExist(err) {
		t.Error("expected no .hermes dir when HERMES_ENV_CONTENTS is unset")
	}
}

func TestPrepareWorkspaceFreshCloneAndWipPush(t *testing.T) {
	bare := makeBareRepo(t)
	cfg := Config{Home: t.TempDir(), Repo: "proj", SessionID: "s1", GitURL: bare}

	workDir, state, err := PrepareWorkspace(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if state != "fresh-clone" {
		t.Fatalf("state = %q", state)
	}
	// fresh sessions work on the wip branch from the start
	if br := gitT(t, workDir, "branch", "--show-current"); br != "wip/s1" {
		t.Fatalf("branch = %q", br)
	}

	// commit + push wip
	if err := os.WriteFile(filepath.Join(workDir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, workDir, "add", ".")
	cmd := exec.Command("git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "work")
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
	PushWip(workDir, "s1")

	// origin now has the wip branch
	if out := gitT(t, bare, "branch"); !strings.Contains(out, "wip/s1") {
		t.Fatalf("wip branch not pushed: %s", out)
	}

	// PushWip with no new commits is a no-op (should not error/log-fatal)
	PushWip(workDir, "s1")
}

// A "No repository" pod gets an empty workspace directory and never touches
// git: a GitURL that would fail any clone is ignored, a resume is the same
// empty directory, and the per-turn wip push is a silent no-op.
func TestPrepareWorkspaceNoRepoIsEmptyAndNeverClones(t *testing.T) {
	for _, resume := range []bool{false, true} {
		home := t.TempDir()
		cfg := Config{
			Home: home, SessionID: "s-norepo", NoRepo: true, Resume: resume,
			// Would fail loudly if anything tried to clone it.
			GitURL: filepath.Join(t.TempDir(), "does-not-exist.git"),
		}
		workDir, state, err := PrepareWorkspace(cfg)
		if err != nil {
			t.Fatalf("resume=%v: PrepareWorkspace: %v", resume, err)
		}
		if state != WorkspaceEmpty {
			t.Errorf("resume=%v: state = %q, want %q", resume, state, WorkspaceEmpty)
		}
		if want := filepath.Join(home, ScratchDir); workDir != want {
			t.Errorf("resume=%v: workDir = %q, want %q", resume, workDir, want)
		}
		entries, err := os.ReadDir(workDir)
		if err != nil || len(entries) != 0 {
			t.Errorf("resume=%v: workspace should exist and be empty: %v, %d entries", resume, err, len(entries))
		}
		PushWip(workDir, cfg.SessionID) // no repository: must neither fail nor create one
		if _, err := os.Stat(filepath.Join(workDir, ".git")); !os.IsNotExist(err) {
			t.Errorf("resume=%v: a .git appeared in the no-repo workspace", resume)
		}
	}
}

func TestConfigFromEnvNoRepo(t *testing.T) {
	t.Setenv("BLERG_RUNNER_NO_REPO", "1")
	if !ConfigFromEnv().NoRepo {
		t.Error("BLERG_RUNNER_NO_REPO=1 did not set NoRepo")
	}
	t.Setenv("BLERG_RUNNER_NO_REPO", "")
	if ConfigFromEnv().NoRepo {
		t.Error("NoRepo set without BLERG_RUNNER_NO_REPO")
	}
}

func TestPrepareWorkspaceResumesFromWip(t *testing.T) {
	bare := makeBareRepo(t)
	// First "pod": clone, commit, push wip.
	cfg1 := Config{Home: t.TempDir(), Repo: "proj", SessionID: "s2", GitURL: bare}
	work1, _, err := PrepareWorkspace(cfg1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work1, "progress.txt"), []byte("half-done"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, work1, "add", ".")
	cmd := exec.Command("git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "wip")
	cmd.Dir = work1
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
	PushWip(work1, "s2")

	// Second "pod": resume — must land on the wip branch with the work.
	cfg2 := Config{Home: t.TempDir(), Repo: "proj", SessionID: "s2", GitURL: bare, Resume: true}
	work2, state, err := PrepareWorkspace(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if state != "resumed-from-wip" {
		t.Fatalf("state = %q", state)
	}
	if _, err := os.Stat(filepath.Join(work2, "progress.txt")); err != nil {
		t.Fatalf("wip work missing: %v", err)
	}
}

func TestAuthURLInjectsToken(t *testing.T) {
	got := authURL("https://github.com/org/repo.git", "tok123")
	if !strings.Contains(got, "x-access-token:tok123@github.com") {
		t.Fatalf("got %q", got)
	}
	// non-https untouched
	if authURL("/local/path.git", "tok") != "/local/path.git" {
		t.Fatal("local path must be untouched")
	}
	// GitLab takes its token with the "oauth2" username.
	if got := authURL("https://gitlab.com/grp/repo.git", "tok123"); !strings.Contains(got, "oauth2:tok123@gitlab.com") {
		t.Fatalf("gitlab: got %q", got)
	}
}

// A failed clone's error text reaches the browser (start-stage detail), so
// it must never carry the token, whatever git printed.
func TestPrepareWorkspaceCloneErrorNeverCarriesToken(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	const fake = "FAKE-TOKEN/with:odd@chars"
	home := t.TempDir()
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	_, _, err := PrepareWorkspace(Config{
		Home: home, Repo: "r", SessionID: "s",
		// Nothing listens on port 1: the clone fails fast, offline.
		GitURL: "https://127.0.0.1:1/o/r.git", GitToken: fake,
	})
	if err == nil {
		t.Fatal("clone of an unreachable URL succeeded")
	}
	esc := strings.TrimPrefix(url.UserPassword("u", fake).String(), "u:")
	if strings.Contains(err.Error(), fake) || strings.Contains(err.Error(), esc) {
		t.Fatal("clone error carries the token")
	}
	if got := scrubToken("x "+fake+" y "+esc, fake); strings.Contains(got, fake) || strings.Contains(got, esc) {
		t.Fatalf("scrubToken left the token: %q", got)
	}
}

// A "New repository" session clones when the repository is there (after the
// server created it), and otherwise initialises an empty repository on the wip
// branch whose origin — token included — is where the repository would be.
func TestPrepareWorkspaceNewRepoClonesOrInitialises(t *testing.T) {
	newRepoCloneDelay = 0
	t.Cleanup(func() { newRepoCloneDelay = 2 * time.Second })

	bare := makeBareRepo(t)
	cfg := Config{Home: t.TempDir(), Repo: "me/new-proj", SessionID: "s-new", GitURL: bare, NewRepo: true}
	workDir, state, err := PrepareWorkspace(cfg)
	if err != nil || state != "fresh-clone" {
		t.Fatalf("existing remote: state=%q err=%v", state, err)
	}
	if br := gitT(t, workDir, "branch", "--show-current"); br != "wip/s-new" {
		t.Fatalf("branch = %q", br)
	}

	// No remote: initialise, on the wip branch, with a token-bearing origin.
	missing := "https://github.com/me/new-proj.git"
	cfg = Config{Home: t.TempDir(), Repo: "me/new-proj", SessionID: "s-new2", GitURL: missing, GitToken: "ghp-s3cret", NewRepo: true}
	workDir, state, err = PrepareWorkspace(cfg)
	if err != nil {
		t.Fatalf("missing remote: %v", err)
	}
	if state != WorkspaceInitialised {
		t.Fatalf("state = %q, want %q", state, WorkspaceInitialised)
	}
	if want := filepath.Join(cfg.Home, "me", "new-proj"); workDir != want {
		t.Fatalf("workDir = %q, want %q", workDir, want)
	}
	if br := gitT(t, workDir, "symbolic-ref", "--short", "HEAD"); br != "wip/s-new2" {
		t.Fatalf("branch = %q", br)
	}
	if origin := gitT(t, workDir, "remote", "get-url", "origin"); origin != "https://x-access-token:ghp-s3cret@github.com/me/new-proj.git" {
		t.Fatalf("origin = %q", origin)
	}
	if out := gitT(t, workDir, "status", "--porcelain"); out != "" {
		t.Fatalf("new repository is not clean: %q", out)
	}
	// Nothing to push yet: PushWip is a no-op (no HEAD).
	PushWip(workDir, cfg.SessionID)

	// A resume of an initialised session clones once the remote exists (the
	// repository was created in the meantime), and initialises again otherwise.
	resumed := Config{Home: t.TempDir(), Repo: "me/new-proj", SessionID: "s-new2", GitURL: bare, NewRepo: true, Resume: true}
	if _, state, err := PrepareWorkspace(resumed); err != nil || state != "fresh-clone" {
		t.Fatalf("resume with the remote now present: state=%q err=%v", state, err)
	}
	absent := filepath.Join(t.TempDir(), "nowhere.git")
	resumed.GitURL, resumed.Home = absent, t.TempDir()
	if _, state, err := PrepareWorkspace(resumed); err != nil || state != WorkspaceInitialised {
		t.Fatalf("resume with the remote still missing: state=%q err=%v", state, err)
	}
	// A resume must not start empty because the provider was merely unreachable: PushWip would force the wip
	// branch over the earlier work. (Port 1 refuses the connection; that says nothing about the repository.)
	resumed.GitURL, resumed.Home = "http://127.0.0.1:1/me/new-proj.git", t.TempDir()
	if _, _, err := PrepareWorkspace(resumed); err == nil {
		t.Fatal("a resume must fail, not initialise an empty repository, when the clone fails for a non-missing reason")
	}

	// Without NewRepo the same failure is still a failure.
	cfg.NewRepo = false
	cfg.Home = t.TempDir()
	if _, _, err := PrepareWorkspace(cfg); err == nil {
		t.Fatal("an ordinary session must fail when the clone fails")
	} else if strings.Contains(err.Error(), "ghp-s3cret") {
		t.Fatalf("token in the error: %v", err)
	}
}

// The note the agent gets names the redacted origin and never the token; a
// push into a missing remote is reported once.
func TestNewRepoNoteAndPushProblem(t *testing.T) {
	note := newRepoNote("me/new-proj", "https://github.com/me/new-proj.git")
	for _, want := range []string{"does not exist yet", "gh repo create me/new-proj", "https://github.com/me/new-proj.git"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q: %s", want, note)
		}
	}
	for out, want := range map[string]string{
		"remote: Repository not found.\nfatal: repository 'https://github.com/x/y.git/' not found": "the remote repository does not exist (or this session's token cannot see it)",
		"fatal: Authentication failed for 'https://github.com/x/y.git/'":                           "the remote refused this session's credential",
		"something else": "git push failed; see the pod log",
	} {
		if got := classifyPushFailure(out); got != want {
			t.Errorf("classifyPushFailure(%q) = %q, want %q", out, got, want)
		}
	}

	newRepoCloneDelay = 0
	t.Cleanup(func() { newRepoCloneDelay = 2 * time.Second })
	cfg := Config{Home: t.TempDir(), Repo: "me/x", SessionID: "s-p", GitURL: filepath.Join(t.TempDir(), "nowhere.git"), NewRepo: true}
	workDir, _, err := PrepareWorkspace(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, workDir, "add", ".")
	cmd := exec.Command("git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "work")
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
	var reports []string
	pushProblem = func(_, sessionID, detail string) { reports = append(reports, sessionID+": "+detail) }
	t.Cleanup(func() { pushProblem = nil })
	PushWip(workDir, "s-p")
	if len(reports) != 1 || !strings.Contains(reports[0], "s-p: the remote repository does not exist") {
		t.Fatalf("reports = %v", reports)
	}
}

func TestConfigFromEnvNewRepo(t *testing.T) {
	t.Setenv("BLERG_RUNNER_NEW_REPO", "1")
	if !ConfigFromEnv().NewRepo {
		t.Fatal("NewRepo not read")
	}
	t.Setenv("BLERG_RUNNER_NEW_REPO", "")
	if ConfigFromEnv().NewRepo {
		t.Fatal("NewRepo set without the variable")
	}
}
