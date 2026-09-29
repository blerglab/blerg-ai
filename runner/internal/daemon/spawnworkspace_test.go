package daemon

// Workspace preparation is shared by every spawn kind (Manager.prepareWorkspace).
// Agent-kind used to branch off before it ran, which is how a board-driven
// session for a repo that was not checked out got a 202 and then died silently.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// An agent-kind spawn for a repo that is not checked out must fail with a
// reason naming the repo and what to do — never silently.
func TestAgentSpawnMissingRepoReportsActionableError(t *testing.T) {
	root := t.TempDir()
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root})

	raw, _ := json.Marshal(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-agent-missing", Repo: "not-cloned", Kind: "agent",
	})
	mgr.handleSpawnSession(raw)

	msgs := sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("want 1 error message, got %d: %v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0], `"status":"error"`) || !strings.Contains(msgs[0], "not-cloned") ||
		!strings.Contains(msgs[0], "not checked out") {
		t.Errorf("error message must name the repo and what to do, got %s", msgs[0])
	}
}

// An agent-kind new_repo spawn creates the directory, exactly as the terminal
// branch does, instead of dying on a stat.
func TestAgentSpawnNewRepoCreatesWorkspace(t *testing.T) {
	root := t.TempDir()
	sender := newRecordingSender(4)
	// Command makes the spawn succeed end to end: a spawn that fails before its
	// session starts now removes the folder it created (see failedspawn_test.go).
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root, Command: []string{"true"}})

	raw, _ := json.Marshal(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-agent-new", Repo: "fresh", NewRepo: true,
	})
	mgr.handleSpawnSession(raw)

	if st, err := os.Stat(filepath.Join(root, "fresh")); err != nil || !st.IsDir() {
		t.Fatalf("workspace was not created: %v", err)
	}
	for _, m := range sender.messages() {
		if strings.Contains(m, "not checked out") {
			t.Errorf("must not report a missing workspace after creating one: %s", m)
		}
	}
}

// The engine preflight must cover agent-kind too — the kind every
// board-driven session uses. Without it "no engine on the daemon's PATH"
// leaves the board card at "running" with no error_reason.
func TestAgentSpawnPreflightsEngineBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing on PATH
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root})
	mgr.agents = NewAgentHost(sender, AgentHostConfig{ReposRoot: root, HomeDir: t.TempDir()})

	raw, _ := json.Marshal(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-agent-noeng", Repo: "app", Kind: "agent", Engine: "codex",
	})
	mgr.handleSpawnSession(raw)

	msgs := sender.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], `"status":"error"`) ||
		!strings.Contains(msgs[0], "codex not found on PATH") {
		t.Fatalf("want one error naming the missing engine binary, got %v", msgs)
	}
}

// The claude-code driver shells out to `claude`, so a daemon configured for it
// gets the same preflight; the native in-process loop has no binary and must
// not be preflighted (its own missing-credential error is the actionable one).
func TestPreflightEngineAgentKindBinarySelection(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	sender := newRecordingSender(2)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root})

	mgr.agents = NewAgentHost(sender, AgentHostConfig{ReposRoot: root, HomeDir: t.TempDir(), ClaudeCode: true})
	err := mgr.preflightEngine(protocol.SpawnSession{Kind: "agent", SessionID: "s1"})
	if err == nil || !strings.Contains(err.Error(), "claude not found on PATH") {
		t.Errorf("claude-code driver preflight = %v, want a missing-claude error", err)
	}

	mgr.agents = NewAgentHost(sender, AgentHostConfig{ReposRoot: root, HomeDir: t.TempDir()})
	if err := mgr.preflightEngine(protocol.SpawnSession{Kind: "agent", SessionID: "s1"}); err != nil {
		t.Errorf("native-loop preflight = %v, want nil (no binary to check)", err)
	}
}

// OpenClaw is host-only, so a sandboxed spawn gets that reason from preflight —
// not a missing-image or missing-binary error the user would try to fix, and
// not a `docker run` attempt for a session that can never work in a container.
func TestPreflightRefusesOpenClawInTheSandbox(t *testing.T) {
	root := t.TempDir()
	sender := newRecordingSender(2)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root})
	mgr.agents = NewAgentHost(sender, AgentHostConfig{ReposRoot: root, HomeDir: t.TempDir()})
	// Both would-be excuses are available, so only the OpenClaw rule can be
	// what refuses: the image is present and every binary is "in" it.
	mgr.sandboxImagePresent = func() bool { return true }
	mgr.sandboxEnginePresent = func(string) error { return nil }

	err := mgr.preflightEngine(protocol.SpawnSession{
		Kind: "agent", SessionID: "s-oc", Sandbox: true, Engine: "openclaw",
	})
	if err == nil || err.Error() != sandboxOpenclawRefusal {
		t.Fatalf("preflight = %v, want %q", err, sandboxOpenclawRefusal)
	}
	// Unsandboxed, OpenClaw is preflighted like any other CLI engine.
	t.Setenv("PATH", t.TempDir())
	err = mgr.preflightEngine(protocol.SpawnSession{Kind: "agent", SessionID: "s-oc", Engine: "openclaw"})
	if err == nil || !strings.Contains(err.Error(), "not found on PATH") {
		t.Errorf("host OpenClaw preflight = %v, want the PATH error", err)
	}
}

// An existing workspace resolves and is handed to the spawn as ProjectPath.
func TestPrepareWorkspaceExistingRepo(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := newManagerWithSender(newRecordingSender(1), ManagerConfig{ReposRoot: root})
	got, err := mgr.prepareWorkspace(protocol.SpawnSession{Repo: "app", Kind: "agent"})
	if err != nil {
		t.Fatalf("prepareWorkspace: %v", err)
	}
	if want := filepath.Join(root, "app"); got != want {
		t.Errorf("prepareWorkspace = %q, want %q", got, want)
	}
	if _, err := mgr.prepareWorkspace(protocol.SpawnSession{Repo: "../escape"}); err == nil {
		t.Error("prepareWorkspace must reject a traversal repo")
	}
}

// resolveProjectPath must contain an org-qualified repo ("org/name", the shape
// boards store) under the repos root, and still refuse anything that climbs out.
func TestResolveProjectPathOrgQualified(t *testing.T) {
	root := t.TempDir()
	got, err := resolveProjectPath(root, "blerglab/blerg")
	if err != nil {
		t.Fatalf("resolveProjectPath(org/name) = %v, want ok", err)
	}
	if want := filepath.Join(root, "blerglab", "blerg"); got != want {
		t.Errorf("resolveProjectPath(org/name) = %q, want %q", got, want)
	}
	for _, repo := range []string{"org/../../x", "../org/name", "/abs/org/name"} {
		if _, err := resolveProjectPath(root, repo); err == nil {
			t.Errorf("resolveProjectPath(%q) = nil, want rejected", repo)
		}
	}
}

// A symlinked intermediate directory must be caught even when the full path
// does not exist yet: with "org/name" legal, a spawn for a not-yet-created
// repo whose "org" is a symlink out of the repos root would otherwise have
// MkdirAll/clone land outside the containment boundary.
func TestResolveProjectPathSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "org")); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveProjectPath(root, "org/name"); err == nil {
		t.Errorf("resolveProjectPath through a symlinked org = %q, want rejected", got)
	}

	// ...and prepareWorkspace must create nothing outside the root.
	mgr := newManagerWithSender(newRecordingSender(2), ManagerConfig{ReposRoot: root})
	if _, err := mgr.prepareWorkspace(protocol.SpawnSession{Repo: "org/name", NewRepo: true}); err == nil {
		t.Error("prepareWorkspace must reject a symlink-traversing new_repo")
	}
	if _, err := os.Stat(filepath.Join(outside, "name")); !os.IsNotExist(err) {
		t.Errorf("prepareWorkspace created %s — OUTSIDE the repos root", filepath.Join(outside, "name"))
	}

	// A real directory in the same position still resolves.
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveProjectPath(root, "real/name")
	if err != nil {
		t.Fatalf("resolveProjectPath(real/name) = %v, want ok", err)
	}
	if want := filepath.Join(root, "real", "name"); got != want {
		t.Errorf("resolveProjectPath(real/name) = %q, want %q", got, want)
	}
}

// An org-qualified repo carries its own owner; a bare name is qualified with
// the daemon's configured org, and with neither there is no remote at all.
func TestCloneRemote(t *testing.T) {
	const host = "git@github.com:"
	cases := []struct{ repo, org, want string }{
		{"blerg", "blerglab", host + "blerglab/blerg.git"},
		{"blerglab/blerg", "someone-else", host + "blerglab/blerg.git"},
		{"blerglab/blerg", "", host + "blerglab/blerg.git"},
		{"blerg", "", ""},
	}
	for _, c := range cases {
		if got := cloneRemote(c.repo, "", c.org); got != c.want {
			t.Errorf("cloneRemote(%q, \"\", %q) = %q, want %q", c.repo, c.org, got, c.want)
		}
	}
}

// A named provider is honoured strictly: a GitLab-shaped owner/name from a
// caller that sends "gitlab" is never cloned from GitHub, and an unknown
// provider or a bare GitLab name yields no remote rather than a GitHub guess.
func TestCloneRemoteHonoursProvider(t *testing.T) {
	cases := []struct{ repo, provider, org, want string }{
		{"grp/tool", "gitlab", "blerglab", "git@gitlab.com:grp/tool.git"},
		{"grp/tool", "github", "", "git@github.com:grp/tool.git"},
		{"tool", "github", "blerglab", "git@github.com:blerglab/tool.git"},
		{"tool", "gitlab", "blerglab", ""},        // no org fallback off GitHub
		{"grp/tool", "bitbucket", "blerglab", ""}, // unknown provider: nothing
		{"grp/-bad", "gitlab", "", ""},
		{"bad_org/tool", "github", "", ""},
	}
	for _, c := range cases {
		got := cloneRemote(c.repo, c.provider, c.org)
		if got != c.want {
			t.Errorf("cloneRemote(%q, %q, %q) = %q, want %q", c.repo, c.provider, c.org, got, c.want)
		}
		if c.provider != "github" && strings.Contains(got, "github.com") {
			t.Errorf("cloneRemote(%q, %q) fell back to GitHub: %q", c.repo, c.provider, got)
		}
	}
}

// An existing folder whose origin is another provider's repository is
// refused for a spawn that names a provider — the same path can be a GitHub
// and a GitLab repository.
func TestEnsureClonedRefusesAContradictingCheckout(t *testing.T) {
	root := t.TempDir()
	gd := filepath.Join(root, "grp", "tool", ".git")
	if err := os.MkdirAll(gd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gd, "config"), []byte("[remote \"origin\"]\n\turl = git@github.com:grp/tool.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCloned(context.Background(), root, "grp/tool", "gitlab", ""); err == nil {
		t.Error("a GitHub checkout was accepted for a GitLab spawn")
	}
	if err := EnsureCloned(context.Background(), root, "grp/tool", "github", ""); err != nil {
		t.Errorf("matching provider refused: %v", err)
	}
	if err := EnsureCloned(context.Background(), root, "grp/tool", "", ""); err != nil {
		t.Errorf("legacy (no provider) refused: %v", err)
	}
}

// A "No repository" spawn gets an empty scratch folder directly under the repos
// root: created, never cloned (not even with a GitHub org configured, which
// would otherwise clone any bare name), and — being dot-prefixed — absent from
// the folder list the daemon reports for the repo picker.
func TestPrepareWorkspaceNoRepoCreatesScratchFolder(t *testing.T) {
	root := t.TempDir()
	mgr := newManagerWithSender(newRecordingSender(1), ManagerConfig{ReposRoot: root, GithubOrg: "blerglab"})
	mgr.ensureClonedFunc = func(context.Context, string, string, string, string) error {
		t.Error("a no-repo spawn must never clone")
		return nil
	}
	const name = ".scratch-granite-3f9a"
	got, err := mgr.prepareWorkspace(protocol.SpawnSession{Repo: name, NoRepo: true, NewRepo: true, Kind: "agent"})
	if err != nil {
		t.Fatalf("prepareWorkspace: %v", err)
	}
	if want := filepath.Join(root, name); got != want {
		t.Errorf("prepareWorkspace = %q, want %q", got, want)
	}
	entries, err := os.ReadDir(got)
	if err != nil {
		t.Fatalf("scratch folder not created: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("scratch folder should be empty, has %d entries", len(entries))
	}
	for _, r := range listReposOnDisk(root) {
		if r == name {
			t.Errorf("listReposOnDisk reported the scratch folder %q", r)
		}
	}
}

// NoRepo only ever creates a scratch-named folder: any other name is refused
// before anything is created, so the flag cannot mkdir a folder that would then
// show up in the repo picker, or one outside the root.
func TestPrepareWorkspaceNoRepoRefusesNonScratchNames(t *testing.T) {
	root := t.TempDir()
	mgr := newManagerWithSender(newRecordingSender(1), ManagerConfig{ReposRoot: root})
	for _, name := range []string{"app", "org/name", ".scratch-a/b", ".hidden", "../.scratch-x"} {
		if _, err := mgr.prepareWorkspace(protocol.SpawnSession{Repo: name, NoRepo: true, NewRepo: true}); err == nil {
			t.Errorf("prepareWorkspace(NoRepo, %q) = nil, want refused", name)
		}
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("refused no-repo spawns created %d entries under the root", len(entries))
	}
	// A clone request riding along is not honoured either: NoRepo wins and
	// the folder is only created.
	got, err := mgr.prepareWorkspace(protocol.SpawnSession{Repo: ".scratch-x", NoRepo: true, CloneFrom: "org/name", Provider: "github"})
	if err != nil {
		t.Fatalf("prepareWorkspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(got, ".git")); !os.IsNotExist(err) {
		t.Errorf("no-repo workspace has a .git: %v", err)
	}
}

// A scratch folder that cannot be created (here: the repos root is missing —
// Mkdir, not MkdirAll, so it is not silently made) fails the spawn with a
// reason naming it, rather than a later "not checked out".
func TestPrepareWorkspaceNoRepoMkdirFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-root")
	mgr := newManagerWithSender(newRecordingSender(1), ManagerConfig{ReposRoot: root})
	_, err := mgr.prepareWorkspace(protocol.SpawnSession{Repo: ".scratch-x", NoRepo: true, NewRepo: true})
	if err == nil || !strings.Contains(err.Error(), "could not create the scratch folder") {
		t.Fatalf("prepareWorkspace = %v, want a scratch-folder creation error", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("the missing repos root was created: %v", err)
	}
}

// An existing folder of the requested name — an earlier session's, files and
// all — is never reused: the new session gets a fresh folder under a derived
// name, and the old one is left untouched.
func TestPrepareWorkspaceNoRepoNeverReusesAnExistingFolder(t *testing.T) {
	root := t.TempDir()
	taken := filepath.Join(root, ".scratch-taken")
	if err := os.MkdirAll(taken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taken, "earlier.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := newManagerWithSender(newRecordingSender(1), ManagerConfig{ReposRoot: root})
	got, err := mgr.prepareWorkspace(protocol.SpawnSession{Repo: ".scratch-taken", NoRepo: true, NewRepo: true})
	if err != nil {
		t.Fatalf("prepareWorkspace: %v", err)
	}
	if got == taken || filepath.Dir(got) != root || !strings.HasPrefix(filepath.Base(got), ".scratch-taken-") {
		t.Fatalf("prepareWorkspace = %q, want a new sibling .scratch-taken-<random>", got)
	}
	if entries, _ := os.ReadDir(got); len(entries) != 0 {
		t.Errorf("the new scratch folder is not empty: %d entries", len(entries))
	}
	if b, err := os.ReadFile(filepath.Join(taken, "earlier.txt")); err != nil || string(b) != "mine" {
		t.Errorf("the earlier folder was disturbed: %q, %v", b, err)
	}
	// A file (or anything else) of that name is a collision too.
	if err := os.WriteFile(filepath.Join(root, ".scratch-file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := mgr.prepareWorkspace(protocol.SpawnSession{Repo: ".scratch-file", NoRepo: true}); err != nil || !strings.HasPrefix(filepath.Base(got), ".scratch-file-") {
		t.Errorf("prepareWorkspace over a file = %q, %v; want a fresh folder", got, err)
	}
}

// When every name tried is taken, the spawn fails clearly — it never falls
// back to reusing one of them.
func TestPrepareWorkspaceNoRepoGivesUpWhenEveryNameIsTaken(t *testing.T) {
	root := t.TempDir()
	orig := scratchRetry
	t.Cleanup(func() { scratchRetry = orig })
	scratchRetry = func(name string) string { return name } // every retry collides again
	taken := filepath.Join(root, ".scratch-taken")
	if err := os.MkdirAll(taken, 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := newManagerWithSender(newRecordingSender(1), ManagerConfig{ReposRoot: root})
	got, err := mgr.prepareWorkspace(protocol.SpawnSession{Repo: ".scratch-taken", NoRepo: true, NewRepo: true})
	if err == nil || !strings.Contains(err.Error(), "names were all taken") {
		t.Fatalf("prepareWorkspace = %q, %v; want a clear all-taken error", got, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Errorf("root has %d entries, want only the pre-existing folder", len(entries))
	}
}

// Through a whole spawn: after a collision, session_started names the folder
// that was actually created, so the server's row points at it rather than at
// the earlier session's folder.
func TestSpawnNoRepoCollisionReportsTheFolderItCreated(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".scratch-taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root, Command: []string{"true"}})
	raw, _ := json.Marshal(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-collide", Repo: ".scratch-taken", NoRepo: true, NewRepo: true,
	})
	mgr.handleSpawnSession(raw)
	var started *protocol.SessionStarted
	for _, m := range sender.messages() {
		if strings.Contains(m, `"type":"session_started"`) {
			var s protocol.SessionStarted
			if err := json.Unmarshal([]byte(m), &s); err != nil {
				t.Fatal(err)
			}
			started = &s
		}
	}
	if started == nil {
		t.Fatalf("no session_started: %v", sender.messages())
	}
	if !strings.HasPrefix(started.Repo, ".scratch-taken-") || started.ProjectPath != filepath.Join(root, started.Repo) {
		t.Errorf("session_started repo %q path %q, want the new folder", started.Repo, started.ProjectPath)
	}
}

// End to end through the spawn worker: a no-repo spawn creates its scratch
// folder and gets past workspace preparation without a workspace error.
func TestAgentSpawnNoRepoCreatesScratchWorkspace(t *testing.T) {
	root := t.TempDir()
	sender := newRecordingSender(4)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root, Command: []string{"true"}})

	raw, _ := json.Marshal(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-norepo", Repo: ".scratch-ember-01ab", NoRepo: true, NewRepo: true,
	})
	mgr.handleSpawnSession(raw)

	if st, err := os.Stat(filepath.Join(root, ".scratch-ember-01ab")); err != nil || !st.IsDir() {
		t.Fatalf("scratch workspace was not created: %v", err)
	}
	for _, m := range sender.messages() {
		if strings.Contains(m, `"status":"error"`) || strings.Contains(m, "not checked out") {
			t.Errorf("unexpected workspace error after creating the scratch folder: %s", m)
		}
	}
}
