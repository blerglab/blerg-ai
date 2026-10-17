package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCodexPrivateGuidePreservesEffectiveUserInstructionsAndConfig(t *testing.T) {
	for _, override := range []bool{false, true} {
		name := "default"
		if override {
			name = "override"
		}
		t.Run(name, func(t *testing.T) {
			source, state := t.TempDir(), t.TempDir()
			files := map[string]string{
				"AGENTS.md":   "Default instruction fixture.\n",
				"config.toml": "developer_instructions = 'User developer instruction fixture.'\n",
			}
			want := files["AGENTS.md"]
			if override {
				files["AGENTS.override.md"] = "Preferred instruction fixture.\n"
				want = files["AGENTS.override.md"]
			}
			for file, content := range files {
				if err := os.WriteFile(filepath.Join(source, file), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			home, err := prepareCodexTerminalHome(state, "fixture", source)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "AGENTS.override.md")
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				t.Fatalf("private guide is not an owned private regular file: info=%v err=%v", info, err)
			}
			first, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(first), want) {
				t.Fatalf("effective user instructions missing: err=%v", err)
			}
			if override && strings.Contains(string(first), files["AGENTS.md"]) {
				t.Fatal("default instructions were merged despite a preferred override")
			}
			for _, guide := range []string{workGuideLine, topicGuideLine} {
				if strings.Count(string(first), guide) != 1 {
					t.Fatal("shared guide line missing or duplicated")
				}
			}
			if _, err := prepareCodexTerminalHome(state, "fixture", source); err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(path)
			if err != nil || string(second) != string(first) {
				t.Fatal("guide refresh was not idempotent")
			}
			for file, original := range files {
				got, err := os.ReadFile(filepath.Join(source, file))
				if err != nil || string(got) != original {
					t.Fatalf("source user profile changed: file=%s err=%v", file, err)
				}
			}
		})
	}
}

func TestCodexNativeRPCGuideMatchesShellAvailability(t *testing.T) {
	for _, mode := range []string{"fresh", "resume", "granted", "restricted"} {
		t.Run(mode, func(t *testing.T) {
			bin := fakeCodex(t)
			logPath := filepath.Join(bin, "guide-calls.log")
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_LOG", logPath)
			emitter := &collectEmitter{}
			driver := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: emitter})
			method := "thread/start"
			if mode == "resume" {
				driver.threadID = "cx-thread-1"
				method = "thread/resume"
			}
			if mode == "granted" || mode == "restricted" {
				driver.RestrictedRuntime = &codexRestrictedRuntime{Restricted: mode == "restricted"}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopDriver := startTestCodexDriver(ctx, t, driver)
			defer stopDriver()
			driver.Enqueue("Fixture request", "chat")
			waitUntil(t, "fixture turn complete", func() bool { return countKind(emitter, "turn_done") == 1 })
			body, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, line := range strings.Split(string(body), "\n") {
				if !strings.HasPrefix(line, method+" ") {
					continue
				}
				var params struct {
					Instructions string `json:"developerInstructions"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, method+" ")), &params); err != nil {
					t.Fatal(err)
				}
				found = true
				if mode == "restricted" {
					if strings.Contains(params.Instructions, "blerg-runner") || !strings.Contains(params.Instructions, "restricted Blerg session") {
						t.Fatal("shellless runtime received unavailable CLI guidance")
					}
					continue
				}
				for _, guide := range []string{workGuideLine, topicGuideLine} {
					if !strings.Contains(params.Instructions, guide) {
						t.Fatal("native RPC missed shared command guidance")
					}
				}
			}
			if !found {
				t.Fatal("expected native thread initialization was not observed")
			}
		})
	}
}

func TestCodexPrivateGuideReplacesSymlinkWithoutReadingOrWritingTarget(t *testing.T) {
	source, state := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "AGENTS.md"), []byte("Effective source fixture.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(state, "codex-terminals", "fixture")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "unrelated-instructions")
	const victimText = "Unrelated target must stay unchanged.\n"
	if err := os.WriteFile(victim, []byte(victimText), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(home, "AGENTS.override.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCodexTerminalHome(state, "fixture", source); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != victimText {
		t.Fatal("updating private guidance changed the symlink target")
	}
	guide, err := os.ReadFile(filepath.Join(home, "AGENTS.override.md"))
	if err != nil || strings.Contains(string(guide), victimText) || !strings.Contains(string(guide), workGuideLine) {
		t.Fatal("private guidance read an unrelated symlink target")
	}
}

func TestCodexGuideSourceSymlinkPreservesConfiguredInstructions(t *testing.T) {
	source, home := t.TempDir(), t.TempDir()
	target := filepath.Join(t.TempDir(), "instructions.md")
	const original = "User-managed symlink instruction fixture.\n"
	if err := os.WriteFile(target, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(source, "AGENTS.override.md")); err != nil {
		t.Fatal(err)
	}
	if err := prepareCodexTerminalGuide(home, source); err != nil {
		t.Fatal(err)
	}
	guide, err := os.ReadFile(filepath.Join(home, "AGENTS.override.md"))
	if err != nil || !strings.Contains(string(guide), original) || !strings.Contains(string(guide), workGuideLine) {
		t.Fatal("configured symlink instructions were not preserved")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != original {
		t.Fatal("user-managed symlink target was modified")
	}
}

func TestCodexGuidePrivateHomeAliasCannotRewriteSource(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0750); err != nil {
		t.Fatal(err)
	}
	const original = "Owner instruction fixture.\n"
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	sourceAlias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(home, sourceAlias); err != nil {
		t.Fatal(err)
	}
	if err := prepareCodexTerminalGuide(home, sourceAlias); err == nil {
		t.Fatal("aliased owner profile was accepted as a private session home")
	}
	got, err := os.ReadFile(filepath.Join(home, "AGENTS.md"))
	if err != nil || string(got) != original {
		t.Fatal("aliased source instructions were modified")
	}
	if _, err := os.Lstat(filepath.Join(home, "AGENTS.override.md")); !os.IsNotExist(err) {
		t.Fatal("private guide was written into the aliased owner profile")
	}
	if info, err := os.Stat(home); err != nil || info.Mode().Perm() != 0750 {
		t.Fatalf("rejecting aliased profile changed owner directory permissions: info=%v err=%v", info, err)
	}
}

func TestCodexGuideRefusesFIFOWithoutHanging(t *testing.T) {
	source, home := t.TempDir(), t.TempDir()
	fifo := filepath.Join(source, "AGENTS.override.md")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var result error
	t.Cleanup(func() {
		select {
		case <-done:
			return
		default:
		}
		// An accidental blocking FIFO read must not strand the test goroutine.
		if writer, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = writer.Close()
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("guide reader remained blocked after FIFO cleanup")
		}
	})
	go func() {
		result = prepareCodexTerminalGuide(home, source)
		close(done)
	}()
	select {
	case <-done:
		if result == nil {
			t.Fatal("FIFO instructions were accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FIFO instructions blocked terminal preparation")
	}
}

func TestCodexGuideRejectsOversizeSource(t *testing.T) {
	source, home := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "AGENTS.md"), []byte(strings.Repeat("x", codexGuideMaxBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareCodexTerminalGuide(home, source); err == nil {
		t.Fatal("unbounded source instructions were accepted")
	}
}

func TestCodexGuidePrecedesLongUserInstructions(t *testing.T) {
	source, home := t.TempDir(), t.TempDir()
	original := strings.Repeat("User instruction fixture.\n", 5000)
	if err := os.WriteFile(filepath.Join(source, "AGENTS.md"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareCodexTerminalGuide(home, source); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, "AGENTS.override.md"))
	if err != nil {
		t.Fatal(err)
	}
	userAt := strings.Index(string(body), original)
	if userAt < 0 {
		t.Fatal("long user instructions were truncated or changed")
	}
	for _, guide := range []string{workGuideLine, topicGuideLine} {
		at := strings.Index(string(body), guide)
		if at < 0 || at > userAt {
			t.Fatal("command guide was placed behind potentially budget-truncated user instructions")
		}
	}
}
