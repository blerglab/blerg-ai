package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// fakeEngineHost is a scratch HOME and a daemon PATH holding only bin.
func fakeEngineHost(t *testing.T) (home, bin string) {
	t.Helper()
	home = t.TempDir()
	bin = filepath.Join(home, "svcbin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	for _, k := range []string{"CODEX_HOME", "NVM_DIR", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		t.Setenv(k, "")
	}
	return home, bin
}

func writeExe(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeEngineFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func statusOf(t *testing.T, id string) protocol.EngineStatus {
	t.Helper()
	for _, st := range engineStatuses(context.Background()) {
		if st.Engine == id {
			return st
		}
	}
	t.Fatalf("no status for %s", id)
	return protocol.EngineStatus{}
}

func TestEngineStatusesListEveryKnownEngine(t *testing.T) {
	fakeEngineHost(t)
	var ids []string
	for _, st := range engineStatuses(context.Background()) {
		ids = append(ids, st.Engine)
		if st.Status != protocol.EngineNotInstalled {
			t.Errorf("%s: %s, want not_installed on an empty host", st.Engine, st.Status)
		}
	}
	if want := append([]string{"claude"}, engineOrder...); !slices.Equal(ids, want) {
		t.Fatalf("engines %v, want %v", ids, want)
	}
}

func TestEngineStatusEachState(t *testing.T) {
	home, bin := fakeEngineHost(t)

	// not installed, with the PATH searched (home written as ~)
	st := statusOf(t, "codex")
	if st.Status != protocol.EngineNotInstalled || !slices.Equal(st.SearchedPath, []string{"~/svcbin"}) || st.FoundAt != "" {
		t.Fatalf("not installed: %+v", st)
	}

	// installed but not logged in
	writeExe(t, filepath.Join(bin, "codex"), `echo "codex-cli 0.9.0"`)
	st = statusOf(t, "codex")
	if st.Status != protocol.EngineNotLoggedIn || st.FoundAt != "~/svcbin/codex" || st.SearchedPath != nil {
		t.Fatalf("not logged in: %+v", st)
	}
	if slices.Contains(availableEngines(), "codex") {
		t.Fatal("availableEngines disagrees with not_logged_in")
	}

	// logged in: available, the same answer availableEngines gives — and with no restart
	writeEngineFile(t, filepath.Join(home, ".codex", "auth.json"), `{"x":1}`)
	if st = statusOf(t, "codex"); st.Status != protocol.EngineAvailable || !slices.Contains(availableEngines(), "codex") {
		t.Fatalf("logged in: %+v / %v", st, availableEngines())
	}

	// too old
	engineMinVersions["codex"] = "1.2"
	t.Cleanup(func() { delete(engineMinVersions, "codex") })
	if st = statusOf(t, "codex"); st.Status != protocol.EngineOutdated || st.Version != "0.9.0" || st.MinVersion != "1.2" {
		t.Fatalf("outdated: %+v", st)
	}
	writeExe(t, filepath.Join(bin, "codex"), `echo "codex-cli 1.4.2   "`)
	if st = statusOf(t, "codex"); st.Status != protocol.EngineAvailable || st.Version != "1.4.2" {
		t.Fatalf("updated in place is re-read: %+v", st)
	}

	// Hermes installed with no provider: misconfigured, then a provider fixes it
	writeExe(t, filepath.Join(bin, "hermes"), "exit 0")
	if st = statusOf(t, "hermes"); st.Status != protocol.EngineMisconfigure || st.Detail != "no_provider" {
		t.Fatalf("hermes: %+v", st)
	}
	writeEngineFile(t, filepath.Join(home, ".hermes", "config.yaml"), "model:\n  base_url: http://localhost:11434/v1\n")
	if st = statusOf(t, "hermes"); st.Status != protocol.EngineAvailable {
		t.Fatalf("hermes with provider: %+v", st)
	}

	// OpenClaw not onboarded
	writeExe(t, filepath.Join(bin, "openclaw"), "exit 0")
	if st = statusOf(t, "openclaw"); st.Status != protocol.EngineMisconfigure || st.Detail != "not_onboarded" {
		t.Fatalf("openclaw: %+v", st)
	}

	// Claude: CLI without a login on Linux is not logged in; a token in the service env is enough
	writeExe(t, filepath.Join(bin, "claude"), "exit 0")
	if st = statusOf(t, "claude"); st.Status != protocol.EngineNotLoggedIn && st.Detail != "keychain" {
		t.Fatalf("claude without login: %+v", st)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "x")
	if st = statusOf(t, "claude"); st.Status != protocol.EngineAvailable {
		t.Fatalf("claude with a token: %+v", st)
	}
	raw, _ := json.Marshal(engineStatuses(context.Background()))
	if strings.Contains(string(raw), home) {
		t.Fatalf("a status names the home folder: %s", raw)
	}
}

// The usual places a CLI lands that a service PATH lacks are named, so the
// fix can be exact: nvm, a custom npm prefix, Homebrew-like layouts.
func TestEngineStatusFindsCLIOffPath(t *testing.T) {
	cases := map[string]func(home string) string{
		"nvm": func(home string) string {
			writeExe(t, filepath.Join(home, ".nvm/versions/node/v18.2.0/bin/codex"), "exit 0")
			return "~/.nvm/versions/node/v22.11.0/bin/codex"
		},
		"npm prefix": func(home string) string {
			writeEngineFile(t, filepath.Join(home, ".npmrc"), "registry=https://registry.npmjs.org/\nprefix=~/tools/npm\n")
			return "~/tools/npm/bin/codex"
		},
		"bun": func(home string) string { return "~/.bun/bin/codex" },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			home, _ := fakeEngineHost(t)
			want := setup(home)
			writeExe(t, filepath.Join(home, strings.TrimPrefix(want, "~/")), "exit 0")
			st := statusOf(t, "codex")
			if st.Status != protocol.EngineNotInstalled || st.Detail != "found_elsewhere" || st.FoundAt != want {
				t.Fatalf("%+v, want found at %s", st, want)
			}
		})
	}
}

func TestMergePathKeepsOnlySafeFolders(t *testing.T) {
	got := mergePath([]string{"/opt/homebrew/bin", "relative/bin", "/a b", `/x"y`, "/p%h", "/ok/../up", "/usr/bin/", "/home/u/.nvm/versions/node/v22.1.0/bin"},
		[]string{"/usr/bin", "/bin", "/opt/homebrew/bin"})
	want := []string{"/opt/homebrew/bin", "/usr/bin", "/home/u/.nvm/versions/node/v22.1.0/bin", "/bin"}
	if !slices.Equal(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
	long := make([]string, 100)
	for i := range long {
		long[i] = "/d/" + strings.Repeat("x", i+1)
	}
	if n := len(mergePath(long, nil)); n != maxLoginPathItems {
		t.Fatalf("unbounded: %d", n)
	}
}

// fakeLoginShell is a login shell whose profile sets PATH to dirs and prints a
// banner, the way a real profile can.
func fakeLoginShell(t *testing.T, dir string, dirs ...string) string {
	shell := filepath.Join(dir, "fakeshell")
	writeExe(t, shell, `echo "Welcome back"; echo "args:$1" >> "`+filepath.Join(dir, "shell.log")+`"
export PATH="`+strings.Join(dirs, ":")+`"
shift
exec /bin/sh -c "$1"`)
	return shell
}

func TestRedetectPathFromLoginShell(t *testing.T) {
	layouts := map[string][]string{
		"nvm":        {"~/.nvm/versions/node/v22.11.0/bin", "/usr/bin", "/bin"},
		"npm prefix": {"~/.npm-global/bin", "/usr/local/bin", "/usr/bin", "/bin"},
		"homebrew":   {"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/bin", "/bin"},
	}
	for name, layout := range layouts {
		t.Run(name, func(t *testing.T) {
			home, bin := fakeEngineHost(t)
			var dirs []string
			for _, d := range layout {
				dirs = append(dirs, strings.Replace(d, "~", home, 1))
			}
			t.Setenv("SHELL", fakeLoginShell(t, home, dirs...))
			// The daemon's PATH must still find /bin/sh for the fake shell's exec.
			t.Setenv("PATH", bin+":/usr/bin:/bin")
			state := filepath.Join(home, "state")
			codexDir := dirs[0]
			// /opt/homebrew is not writable in tests: that layout checks the PATH alone.
			withCLI := name != "homebrew"
			if withCLI {
				writeExe(t, filepath.Join(codexDir, "codex"), "exit 0")
				if st := statusOf(t, "codex"); st.Status != protocol.EngineNotInstalled {
					t.Fatalf("before: %+v", st)
				}
			}
			res := engineCheck(context.Background(), protocol.EngineCheck{RequestID: "r", RedetectPath: true}, state)
			if res.PathError != "" {
				t.Fatal(res.PathError)
			}
			if !slices.Contains(res.PathAdded, layout[0]) {
				t.Fatalf("added %v, want %s", res.PathAdded, layout[0])
			}
			if got := filepath.SplitList(os.Getenv("PATH")); got[0] != codexDir || !slices.Contains(got, bin) {
				t.Fatalf("PATH %v", got)
			}
			found := false
			for _, st := range res.Statuses {
				found = found || st.Engine == "codex" && st.Status == protocol.EngineNotLoggedIn
			}
			if withCLI && !found {
				t.Fatalf("codex not found after re-detect: %+v", res.Statuses)
			}
			// The interactive login shell was tried first.
			if log, _ := os.ReadFile(filepath.Join(home, "shell.log")); !strings.HasPrefix(string(log), "args:-ilc") {
				t.Fatalf("shell flags: %q", log)
			}
			// A restart applies the saved PATH again.
			t.Setenv("PATH", "/usr/bin:/bin")
			ApplySavedLoginPath(state)
			if got := filepath.SplitList(os.Getenv("PATH")); got[0] != codexDir {
				t.Fatalf("after restart PATH %v", got)
			}
			if info, err := os.Stat(filepath.Join(state, loginPathFile)); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("saved file: %v %v", info, err)
			}
		})
	}
}

func TestRedetectPathFallsBackToPlainLoginShell(t *testing.T) {
	home, _ := fakeEngineHost(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	prev := loginShellCommand
	t.Cleanup(func() { loginShellCommand = prev })
	var calls []bool
	loginShellCommand = func(_ context.Context, _ string, interactive bool) ([]byte, error) {
		calls = append(calls, interactive)
		if interactive {
			return []byte("bash: no job control in this shell\n"), nil
		}
		return []byte("noise\n" + loginPathMarker + "/opt/homebrew/bin:/usr/bin" + loginPathMarker + "\n"), nil
	}
	added, saveErr, err := redetectPath(context.Background(), filepath.Join(home, "state"))
	if err != nil || saveErr != nil || !slices.Equal(added, []string{"/opt/homebrew/bin"}) || !slices.Equal(calls, []bool{true, false}) {
		t.Fatalf("added %v err %v calls %v", added, err, calls)
	}
	loginShellCommand = func(context.Context, string, bool) ([]byte, error) { return nil, context.DeadlineExceeded }
	if _, _, err := redetectPath(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "login shell") {
		t.Fatalf("err %v", err)
	}
}

// A PATH that can't be saved still applies now, and the answer says a restart loses it.
func TestRedetectPathReportsPersistenceFailure(t *testing.T) {
	home, _ := fakeEngineHost(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	prev := loginShellCommand
	t.Cleanup(func() { loginShellCommand = prev })
	loginShellCommand = func(context.Context, string, bool) ([]byte, error) {
		return []byte(loginPathMarker + "/opt/new/bin:/usr/bin" + loginPathMarker), nil
	}
	blocked := filepath.Join(home, "not-a-folder")
	writeEngineFile(t, blocked, "a regular file where the state folder should be")
	res := engineCheck(context.Background(), protocol.EngineCheck{RequestID: "r", RedetectPath: true}, filepath.Join(blocked, "state"))
	if res.PathError != "" || res.PathSaveError == "" || !slices.Equal(res.PathAdded, []string{"/opt/new/bin"}) {
		t.Fatalf("%+v", res)
	}
	if got := filepath.SplitList(os.Getenv("PATH")); got[0] != "/opt/new/bin" {
		t.Fatalf("not applied: %v", got)
	}
	if res = engineCheck(context.Background(), protocol.EngineCheck{RequestID: "r", RedetectPath: true}, ""); res.PathSaveError == "" {
		t.Fatal("no state folder must be reported")
	}
}

// A check that arrives while one runs is answered Busy at once: nothing queues.
func TestEngineCheckHandlerNeverQueues(t *testing.T) {
	home, _ := fakeEngineHost(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	prev := loginShellCommand
	t.Cleanup(func() { loginShellCommand = prev })
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	loginShellCommand = func(context.Context, string, bool) ([]byte, error) {
		started <- struct{}{}
		<-release
		return []byte(loginPathMarker + "/usr/bin" + loginPathMarker), nil
	}
	sender := &fakeCheckSender{}
	handle := EngineCheckHandler(sender, filepath.Join(home, "state"), nil)
	handle([]byte(`{"type":"engine_check","request_id":"first","redetect_path":true}`))
	<-started
	for i := range 10 {
		handle([]byte(`{"type":"engine_check","request_id":"again-` + string(rune('a'+i)) + `","redetect_path":true}`))
	}
	sender.mu.Lock()
	busy := len(sender.sent)
	for _, m := range sender.sent {
		if !m.(protocol.EngineCheckResult).Busy {
			t.Errorf("not busy: %+v", m)
		}
	}
	sender.mu.Unlock()
	if busy != 10 || len(started) != 0 {
		t.Fatalf("busy answers %d, extra shells started %d", busy, len(started))
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		sender.mu.Lock()
		n := len(sender.sent)
		sender.mu.Unlock()
		if n == 11 || time.Now().After(deadline) {
			if n != 11 {
				t.Fatalf("the running check never answered: %d", n)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type fakeCheckSender struct {
	mu   sync.Mutex
	sent []any
	hb   int
}

func (f *fakeCheckSender) Send(msg any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	return nil
}
func (f *fakeCheckSender) RequestHeartbeat() { f.mu.Lock(); f.hb++; f.mu.Unlock() }

func TestEngineCheckHandlerAnswersAndKicks(t *testing.T) {
	fakeEngineHost(t)
	sender := &fakeCheckSender{}
	kicked := make(chan struct{}, 1)
	handle := EngineCheckHandler(sender, "", func() { kicked <- struct{}{} })
	handle([]byte(`{"type":"engine_check"}`)) // no request id: ignored
	handle([]byte(`{"type":"engine_check","request_id":"abc"}`))
	select {
	case <-kicked:
	case <-time.After(5 * time.Second):
		t.Fatal("no answer")
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.sent) != 1 || sender.hb != 1 {
		t.Fatalf("sent %d hb %d", len(sender.sent), sender.hb)
	}
	res := sender.sent[0].(protocol.EngineCheckResult)
	if res.RequestID != "abc" || res.Type != "engine_check_result" || len(res.Statuses) != 1+len(engineOrder) || res.AvailableEngines == nil {
		t.Fatalf("%+v", res)
	}
}
