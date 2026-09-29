package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func writeSettings(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonStateDir(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, "/srv/blerg-state")
	if got := DaemonStateDir(); got != "/srv/blerg-state" {
		t.Errorf("with %s set: %q", DaemonStateDirEnv, got)
	}
	t.Setenv(DaemonStateDirEnv, "")
	t.Setenv("HOME", "/home/someone")
	if got := DaemonStateDir(); got != "/home/someone/.blerg-runner-daemon" {
		t.Errorf("default: %q", got)
	}
	if DaemonSettingsPath("") != "" {
		t.Error("no state dir must mean no settings path")
	}
}

// Boot-time precedence: a saved root overrides the env var; anything wrong
// with the file falls back to the env var with a warning and never crashes.
func TestLoadReposRootSettingPrecedence(t *testing.T) {
	saved := t.TempDir()
	env := t.TempDir()

	cases := []struct {
		name       string
		body       *string // nil = no file
		wantRoot   string
		wantSource string
		wantWarn   string
	}{
		{name: "no file falls back to env", body: nil, wantRoot: env, wantSource: "env"},
		{name: "saved root overrides env", body: ptr(`{"repos_root":"` + saved + `"}`), wantRoot: saved, wantSource: "settings"},
		{name: "saved root is cleaned", body: ptr(`{"repos_root":"` + saved + `/"}`), wantRoot: saved, wantSource: "settings"},
		{name: "file with no root falls back silently", body: ptr(`{}`), wantRoot: env, wantSource: "env"},
		{name: "corrupt file falls back with a warning", body: ptr(`{not json`), wantRoot: env, wantSource: "env", wantWarn: "ignoring saved repos root"},
		{name: "relative saved root falls back with a warning", body: ptr(`{"repos_root":"repos"}`), wantRoot: env, wantSource: "env", wantWarn: "must be absolute"},
		{name: "saved / falls back with a warning", body: ptr(`{"repos_root":"/"}`), wantRoot: env, wantSource: "env", wantWarn: "can't be the repos folder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			file := filepath.Join(t.TempDir(), "state", daemonSettingsFile)
			if tc.body != nil {
				writeSettings(t, file, *tc.body)
			}
			s, source := LoadReposRootSetting(file, env)
			if s.Get() != tc.wantRoot || source != tc.wantSource {
				t.Errorf("got (%q, %q), want (%q, %q)", s.Get(), source, tc.wantRoot, tc.wantSource)
			}
			if tc.wantWarn != "" && !strings.Contains(logs.String(), tc.wantWarn) {
				t.Errorf("log %q does not mention %q", logs.String(), tc.wantWarn)
			}
			if tc.wantWarn == "" && strings.Contains(logs.String(), "ignoring") {
				t.Errorf("unexpected warning: %q", logs.String())
			}
		})
	}
}

func TestLoadReposRootSettingUnreadableFileFallsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	logs := captureLog(t)
	env := t.TempDir()
	file := filepath.Join(t.TempDir(), daemonSettingsFile)
	writeSettings(t, file, `{"repos_root":"`+t.TempDir()+`"}`)
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	s, source := LoadReposRootSetting(file, env)
	if s.Get() != env || source != "env" {
		t.Errorf("got (%q, %q), want the env root", s.Get(), source)
	}
	if !strings.Contains(logs.String(), "ignoring saved repos root") {
		t.Errorf("expected a warning, log: %q", logs.String())
	}
}

// A saved root that has since gone missing is still used (and warned
// about): silently moving new work elsewhere would be worse.
func TestLoadReposRootSettingMissingSavedDirIsKept(t *testing.T) {
	logs := captureLog(t)
	gone := filepath.Join(t.TempDir(), "unplugged")
	file := filepath.Join(t.TempDir(), daemonSettingsFile)
	writeSettings(t, file, `{"repos_root":"`+gone+`"}`)
	s, source := LoadReposRootSetting(file, t.TempDir())
	if s.Get() != gone || source != "settings" {
		t.Errorf("got (%q, %q), want the saved root", s.Get(), source)
	}
	if !strings.Contains(logs.String(), "not currently a directory") {
		t.Errorf("expected a warning, log: %q", logs.String())
	}
}

func ptr(s string) *string { return &s }

func TestPrepareReposRoot(t *testing.T) {
	base := t.TempDir()
	existing := filepath.Join(base, "existing")
	mustMkdir(t, existing)
	aFile := filepath.Join(base, "a-file")
	if err := os.WriteFile(aFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkToDir := filepath.Join(base, "link-to-dir")
	mustSymlink(t, existing, linkToDir)
	linkDangling := filepath.Join(base, "link-dangling")
	mustSymlink(t, filepath.Join(base, "nowhere"), linkDangling)
	linkToFile := filepath.Join(base, "link-to-file")
	mustSymlink(t, aFile, linkToFile)
	linkToRoot := filepath.Join(base, "link-to-root")
	mustSymlink(t, "/", linkToRoot)

	cases := []struct {
		name    string
		in      string
		want    string // "" = expect an error
		wantErr string
	}{
		{name: "empty", in: "", wantErr: "required"},
		{name: "relative", in: "repos", wantErr: "absolute"},
		{name: "dot-relative", in: "./repos", wantErr: "absolute"},
		{name: "filesystem root", in: "/", wantErr: "can't be the repos folder"},
		{name: "filesystem root spelled oddly", in: "/tmp/..", wantErr: "can't be the repos folder"},
		{name: "proc", in: "/proc/self", wantErr: "can't be the repos folder"},
		{name: "sys", in: "/sys", wantErr: "can't be the repos folder"},
		{name: "control chars", in: existing + "\n/etc", wantErr: "control characters"},
		{name: "a file, not a folder", in: aFile, wantErr: "not a folder"},
		{name: "under a file", in: filepath.Join(aFile, "sub"), wantErr: "can't create"},
		{name: "existing dir", in: existing, want: existing},
		{name: "trailing slash is cleaned", in: existing + "/", want: existing},
		{name: "missing dir is created", in: filepath.Join(base, "new", "deeper"), want: filepath.Join(base, "new", "deeper")},
		{name: "symlink to a dir is kept as given", in: linkToDir, want: linkToDir},
		{name: "dangling symlink", in: linkDangling, wantErr: "doesn't exist"},
		{name: "symlink to a file", in: linkToFile, wantErr: "not a folder"},
		{name: "symlink to /", in: linkToRoot, wantErr: "can't be the repos folder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PrepareReposRoot(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("PrepareReposRoot(%q) = (%q, %v), want an error containing %q", tc.in, got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("PrepareReposRoot(%q) = (%q, %v), want %q", tc.in, got, err, tc.want)
			}
			if info, err := os.Stat(got); err != nil || !info.IsDir() {
				t.Errorf("%q is not a directory after preparing it", got)
			}
			// The write probe cleans up after itself.
			entries, _ := os.ReadDir(got)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".blerg-runner-write-check") {
					t.Errorf("write probe left behind: %s", e.Name())
				}
			}
		})
	}
}

func TestPrepareReposRootUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	ro := filepath.Join(t.TempDir(), "read-only")
	mustMkdir(t, ro)
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o755) })

	if _, err := PrepareReposRoot(ro); err == nil || !strings.Contains(err.Error(), "can't write") ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Errorf("read-only dir: err = %v, want a can't-write / permission denied reason", err)
	}
	if _, err := PrepareReposRoot(filepath.Join(ro, "child")); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("child of a read-only dir: err = %v, want permission denied", err)
	}
}

func TestReposRootSettingSetPersistsAndSurvivesRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", daemonSettingsFile)
	env := t.TempDir()
	next := filepath.Join(t.TempDir(), "moved")

	s, _ := LoadReposRootSetting(file, env)
	got, err := s.Set(next, nil)
	if err != nil || got != next || s.Get() != next {
		t.Fatalf("Set = (%q, %v), Get = %q; want %q", got, err, s.Get(), next)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("settings file not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("settings file mode %o, want 600", perm)
	}

	// "Restart": a fresh load with the same env picks the saved root.
	again, source := LoadReposRootSetting(file, env)
	if again.Get() != next || source != "settings" {
		t.Errorf("after restart: (%q, %q), want (%q, settings)", again.Get(), source, next)
	}
}

func TestReposRootSettingSetRefusalChangesNothing(t *testing.T) {
	file := filepath.Join(t.TempDir(), daemonSettingsFile)
	first := t.TempDir()
	s := NewReposRootSetting(t.TempDir(), file)
	if _, err := s.Set(first, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file)

	got, err := s.Set("relative/path", nil)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if got != first || s.Get() != first {
		t.Errorf("after a refusal the root is %q (returned %q), want it unchanged at %q", s.Get(), got, first)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Errorf("settings file changed on a refusal:\n%s\n→\n%s", before, after)
	}
}

func TestReposRootSettingWithoutStateFileRefuses(t *testing.T) {
	orig := t.TempDir()
	s := NewReposRootSetting(orig, "")
	if _, err := s.Set(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), DaemonStateDirEnv) {
		t.Errorf("err = %v, want a refusal naming %s", err, DaemonStateDirEnv)
	}
	if s.Get() != orig {
		t.Errorf("root changed to %q", s.Get())
	}
}

// A save that fails (state dir unwritable) must not apply the change: it
// would silently revert on the next restart.
func TestReposRootSettingSaveFailureChangesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	mustMkdir(t, stateDir)
	if err := os.Chmod(stateDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(stateDir, 0o755) })
	orig := t.TempDir()
	s := NewReposRootSetting(orig, filepath.Join(stateDir, daemonSettingsFile))
	if _, err := s.Set(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "could not save") {
		t.Errorf("err = %v, want a could-not-save refusal", err)
	}
	if s.Get() != orig {
		t.Errorf("root changed to %q despite the failed save", s.Get())
	}
}

// resolveProjectPath's containment stays right for any root the setting
// accepts: trailing slashes, a sibling whose name extends the root's, and a
// symlinked root.
func TestResolveProjectPathAfterRootChange(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repos")
	sibling := filepath.Join(base, "repos-evil")
	mustMkdir(t, root)
	mustMkdir(t, sibling)
	link := filepath.Join(base, "link-repos")
	mustSymlink(t, root, link)

	for _, r := range []string{root, root + "/", link, link + "/"} {
		stored, err := PrepareReposRoot(r)
		if err != nil {
			t.Fatalf("PrepareReposRoot(%q): %v", r, err)
		}
		if _, err := resolveProjectPath(stored, "../repos-evil/x"); err == nil {
			t.Errorf("root %q: a sibling sharing the root's name prefix was accepted", r)
		}
		if _, err := resolveProjectPath(stored, "../repos"); err == nil {
			t.Errorf("root %q: ../repos (the root itself, or outside a linked root) accepted", r)
		}
		p, err := resolveProjectPath(stored, "proj")
		if err != nil || p != filepath.Join(stored, "proj") {
			t.Errorf("root %q: resolveProjectPath(proj) = (%q, %v)", r, p, err)
		}
	}
}

// The live round trip on the daemon: a set_repos_root answer, sessions
// already running keep working in their old paths, new spawns and the repo
// listing use the new root, and recovery records follow the root.
func TestSetReposRootMovesNewWorkAndKeepsRunningSessions(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := filepath.Join(t.TempDir(), "new-root")
	mustMkdir(t, filepath.Join(oldRoot, "alpha"))
	file := filepath.Join(t.TempDir(), daemonSettingsFile)

	sender := newRecordingSender(256)
	mgr := newManagerWithSender(sender, ManagerConfig{
		ReposRoot:        oldRoot,
		ReposRootSetting: NewReposRootSetting(oldRoot, file),
		// Reads one line, echoes it, exits 0: alive until the test talks to it.
		Command: []string{"head", "-n", "1"},
	})

	// A recovery record for a session started under the old root.
	if err := writeSessionRecord(oldRoot, SessionRecord{Version: recordSchemaVersion, SessionID: "rec-1", Repo: "alpha", ProjectPath: filepath.Join(oldRoot, "alpha")}); err != nil {
		t.Fatal(err)
	}

	spawn := func(id, repo string, newRepo bool) protocol.SessionStarted {
		raw, _ := json.Marshal(protocol.SpawnSession{Type: "spawn_session", SessionID: id, Repo: repo, NewRepo: newRepo, Cols: 80, Rows: 24})
		mgr.handleSpawnSession(raw)
		deadline := time.After(5 * time.Second)
		for {
			select {
			case m := <-sender.ch:
				if st, ok := m.(protocol.SessionStarted); ok && st.SessionID == id {
					return st
				}
				if sc, ok := m.(protocol.SessionStateChanged); ok && sc.SessionID == id && sc.Status == "error" {
					t.Fatalf("spawn %s failed: %v", id, *sc.Message)
				}
			case <-deadline:
				t.Fatalf("spawn %s: no session_started", id)
			}
		}
	}

	before := spawn("s-old", "alpha", false)
	if before.ProjectPath != filepath.Join(oldRoot, "alpha") {
		t.Fatalf("old session path %q", before.ProjectPath)
	}

	raw, _ := json.Marshal(protocol.SetReposRoot{Type: "set_repos_root", RequestID: "req-1", ReposRoot: newRoot})
	mgr.handleSetReposRoot(raw)
	var res protocol.ReposRootResult
	deadline := time.After(5 * time.Second)
wait:
	for {
		select {
		case m := <-sender.ch:
			if r, ok := m.(protocol.ReposRootResult); ok {
				res = r
				break wait
			}
		case <-deadline:
			t.Fatal("no repos_root_result")
		}
	}
	if !res.OK || res.RequestID != "req-1" || res.ReposRoot != newRoot || res.Error != "" {
		t.Fatalf("result %+v", res)
	}
	if mgr.reposRoot() != newRoot {
		t.Errorf("live root %q, want %q", mgr.reposRoot(), newRoot)
	}

	// The running session still works: input still echoes back.
	in, _ := json.Marshal(protocol.SendInput{Type: "send_input", SessionID: "s-old", Data: base64.StdEncoding.EncodeToString([]byte("still-alive\n"))})
	mgr.handleSendInput(in)
	if !waitForOutput(sender, "s-old", "still-alive", 5*time.Second) {
		t.Error("the session started before the change stopped working")
	}

	// A new spawn lands under the new root.
	after := spawn("s-new", "beta", true)
	if after.ProjectPath != filepath.Join(newRoot, "beta") {
		t.Errorf("new session path %q, want under %q", after.ProjectPath, newRoot)
	}
	// The repo listing reflects the new root.
	if got := listReposOnDisk(mgr.reposRoot()); len(got) != 1 || got[0] != "beta" {
		t.Errorf("repos under the new root: %v", got)
	}

	// The recovery record moved with the root.
	if _, err := readSessionRecord(newRoot, "rec-1"); err != nil {
		t.Errorf("record not under the new root: %v", err)
	}
	if _, err := os.Stat(recordPath(oldRoot, "rec-1")); !os.IsNotExist(err) {
		t.Errorf("record still under the old root (err=%v)", err)
	}

	// Let the new session's `head -n 1` exit on its own too. (Not a kill: a
	// killed process's exit code is written in pty.go after OutputCh closes,
	// racing streamSession's read of it — a separate, pre-existing issue.)
	end, _ := json.Marshal(protocol.SendInput{Type: "send_input", SessionID: "s-new", Data: base64.StdEncoding.EncodeToString([]byte("bye\n"))})
	mgr.handleSendInput(end)
}

func TestSetReposRootRefusalReportsReasonAndChangesNothing(t *testing.T) {
	root := t.TempDir()
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{
		ReposRoot:        root,
		ReposRootSetting: NewReposRootSetting(root, filepath.Join(t.TempDir(), daemonSettingsFile)),
		Command:          []string{"unused"},
	})
	res := mgr.setReposRoot(protocol.SetReposRoot{Type: "set_repos_root", RequestID: "r", ReposRoot: "/"})
	if res.OK || res.Error == "" || res.ReposRoot != root || res.RequestID != "r" {
		t.Errorf("result %+v, want a refusal that reports the unchanged root", res)
	}
	if mgr.reposRoot() != root {
		t.Errorf("root changed to %q", mgr.reposRoot())
	}
}

// kickingSender records Sends and heartbeat requests, in order.
type kickingSender struct {
	events chan string
}

func (k *kickingSender) Send(msg any) error {
	if r, ok := msg.(protocol.ReposRootResult); ok {
		if r.OK {
			k.events <- "result:ok"
		} else {
			k.events <- "result:refused"
		}
	}
	return nil
}
func (k *kickingSender) RequestHeartbeat() { k.events <- "heartbeat" }

// After a change the daemon asks for a heartbeat right away (it carries the
// new root's remotes), and only after the result; a refusal changes nothing
// worth reporting.
func TestSetReposRootRequestsHeartbeatAfterSuccess(t *testing.T) {
	root := t.TempDir()
	k := &kickingSender{events: make(chan string, 8)}
	mgr := newManagerWithSender(k, ManagerConfig{
		ReposRoot:        root,
		ReposRootSetting: NewReposRootSetting(root, filepath.Join(t.TempDir(), daemonSettingsFile)),
		Command:          []string{"unused"},
	})
	next := func() string {
		select {
		case e := <-k.events:
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("timed out")
			return ""
		}
	}

	raw, _ := json.Marshal(protocol.SetReposRoot{Type: "set_repos_root", RequestID: "a", ReposRoot: t.TempDir()})
	mgr.handleSetReposRoot(raw)
	if a, b := next(), next(); a != "result:ok" || b != "heartbeat" {
		t.Errorf("events %q, %q; want result:ok then heartbeat", a, b)
	}

	raw, _ = json.Marshal(protocol.SetReposRoot{Type: "set_repos_root", RequestID: "b", ReposRoot: "nope"})
	mgr.handleSetReposRoot(raw)
	if e := next(); e != "result:refused" {
		t.Errorf("event %q, want result:refused", e)
	}
	select {
	case e := <-k.events:
		t.Errorf("unexpected %q after a refusal", e)
	case <-time.After(100 * time.Millisecond):
	}
}

// waitForOutput reports whether session id produced output containing want.
func waitForOutput(sender *recordingSender, id, want string, d time.Duration) bool {
	var got strings.Builder
	deadline := time.After(d)
	for {
		select {
		case m := <-sender.ch:
			if o, ok := m.(protocol.SessionOutput); ok && o.SessionID == id {
				b, _ := base64.StdEncoding.DecodeString(o.Data)
				got.Write(b)
				if strings.Contains(got.String(), want) {
					return true
				}
			}
		case <-deadline:
			return false
		}
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// Hello and heartbeat report the live root — and list repos under it — so
// the server and UI see a change without a reconnect.
func TestHeartbeatReportsLiveReposRoot(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	mustMkdir(t, filepath.Join(first, "one"))
	mustMkdir(t, filepath.Join(second, "two"))
	setting := NewReposRootSetting(first, filepath.Join(t.TempDir(), daemonSettingsFile))

	type msg struct {
		Type            string   `json:"type"`
		ReposRoot       string   `json:"repos_root"`
		CheckedOutRepos []string `json:"checked_out_repos"`
	}
	msgs := make(chan msg, 64)
	ts := newTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m msg
			if json.Unmarshal(raw, &m) == nil {
				msgs <- m
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := NewWSClient(Config{
		ServerURL: ts.wsURL(), DaemonToken: "t", DaemonName: "d", DaemonMode: "local",
		ReposRoot: "/configured-at-build-time", ProtocolVersion: "1",
		HeartbeatInterval: 30 * time.Millisecond,
		ReconnectInitial:  100 * time.Millisecond, ReconnectMax: 200 * time.Millisecond,
	})
	client.SetReposRoot(setting.Get)
	go client.Run(ctx, func() []string { return nil })

	next := func(typ string) msg {
		for {
			select {
			case m := <-msgs:
				if m.Type == typ {
					return m
				}
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %s", typ)
			}
		}
	}
	if h := next("daemon_hello"); h.ReposRoot != first || !slices.Equal(h.CheckedOutRepos, []string{"one"}) {
		t.Errorf("hello: %+v", h)
	}
	if _, err := setting.Set(second, nil); err != nil {
		t.Fatal(err)
	}
	for {
		hb := next("daemon_heartbeat")
		if hb.ReposRoot == second {
			if !slices.Equal(hb.CheckedOutRepos, []string{"two"}) {
				t.Errorf("heartbeat after the change lists %v", hb.CheckedOutRepos)
			}
			return
		}
	}
}
