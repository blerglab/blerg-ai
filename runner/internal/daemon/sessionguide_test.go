package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assertSessionWorkGuide(t *testing.T, text string) {
	t.Helper()
	for _, line := range []string{childSessionGuideLine, workGuideLine, topicGuideLine, checklistWorkMapGuideLine, planAutoGuideLine, boardAccessGuideLine} {
		if !strings.Contains(text, line) {
			t.Fatalf("missing shared guidance: %s", line)
		}
	}
}
func TestCodexTerminalManagedGuide(t *testing.T) {
	source := t.TempDir()
	original := []byte("Personal override instructions.\n")
	if err := os.WriteFile(filepath.Join(source, "AGENTS.override.md"), original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "AGENTS.md"), []byte("Shadowed instructions."), 0600); err != nil {
		t.Fatal(err)
	}
	config := []byte("developer_instructions = 'Personal developer instructions'\n")
	if err := os.WriteFile(filepath.Join(source, "config.toml"), config, 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state")
	home, err := prepareCodexTerminalHome(state, "session", source)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, "AGENTS.override.md"))
	if err != nil {
		t.Fatal(err)
	}
	assertSessionWorkGuide(t, string(body))
	if !strings.Contains(string(body), string(original)) || strings.Contains(string(body), "Shadowed instructions") {
		t.Fatal("effective user instructions changed")
	}
	info, err := os.Lstat(filepath.Join(home, "AGENTS.override.md"))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("private guide must be an owned regular file")
	}
	after, err := os.ReadFile(filepath.Join(source, "AGENTS.override.md"))
	if err != nil || string(after) != string(original) {
		t.Fatal("source global instructions were changed")
	}
	after, err = os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil || string(after) != string(config) {
		t.Fatal("existing developer config was changed")
	}
	again, err := prepareCodexTerminalHome(state, "session", source)
	if err != nil || again != home {
		t.Fatal("idempotent terminal preparation failed")
	}
	after, err = os.ReadFile(filepath.Join(home, "AGENTS.override.md"))
	if err != nil || string(after) != string(body) {
		t.Fatal("repeated preparation duplicated managed guide")
	}
}
func TestCodexSharedGuideBundle(t *testing.T) {
	assertSessionWorkGuide(t, ccSystemPromptFor(""))
	assertSessionWorkGuide(t, managedBlock)
}

func TestCodexNativeGuideScope(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		granted, restricted, resume bool
	}{
		{name: "unrestricted"}, {name: "granted", granted: true}, {name: "restricted", restricted: true}, {name: "resume", resume: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := fakeCodex(t)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("PATH", binary+":"+os.Getenv("PATH"))
			log := filepath.Join(t.TempDir(), "rpc.log")
			t.Setenv("FAKE_CODEX_LOG", log)
			d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: &collectEmitter{}})
			if tc.granted || tc.restricted {
				d.RestrictedRuntime = &codexRestrictedRuntime{Restricted: tc.restricted}
			}
			method := "thread/start"
			if tc.resume {
				d.threadID = "prior-conversation"
				method = "thread/resume"
			}
			stop := startTestCodexDriver(context.Background(), t, d)
			defer stop()
			deadline := time.NewTimer(15 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for {
				raw, _ := os.ReadFile(log)
				for _, line := range strings.Split(string(raw), "\n") {
					if !strings.HasPrefix(line, method+" ") {
						continue
					}
					var params struct {
						DeveloperInstructions string `json:"developerInstructions"`
					}
					if json.Unmarshal([]byte(strings.TrimPrefix(line, method+" ")), &params) != nil {
						continue
					}
					if tc.restricted {
						if !strings.Contains(params.DeveloperInstructions, "restricted Blerg session") || strings.Contains(params.DeveloperInstructions, "blerg-runner") {
							t.Fatal("shellless runtime received CLI guidance")
						}
					} else {
						assertSessionWorkGuide(t, params.DeveloperInstructions)
					}
					return
				}
				select {
				case <-deadline.C:
					t.Fatal("native instructions were not sent")
				case <-tick.C:
				}
			}
		})
	}
}
func TestCodexRecoveryRefreshesPrivateGuide(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy custom CODEX_HOME", true: "retained source"}[retained], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			source := t.TempDir()
			t.Setenv("CODEX_HOME", source)
			if retained {
				source = t.TempDir()
			}
			if err := os.WriteFile(filepath.Join(source, "AGENTS.md"), []byte("Retained owner instructions."), 0600); err != nil {
				t.Fatal(err)
			}
			private := t.TempDir()
			if err := os.Chmod(private, 0700); err != nil {
				t.Fatal(err)
			}
			sessionDir := filepath.Join(private, "sessions")
			if err := os.Mkdir(sessionDir, 0700); err != nil {
				t.Fatal(err)
			}
			id := "00000000-0000-4000-8000-000000000001"
			record := `{"type":"session_meta","payload":{"id":"` + id + `","timestamp":"2026-01-01T00:00:00Z","source":"cli"}}` + "\n"
			if err := os.WriteFile(filepath.Join(sessionDir, "rollout-"+id+".jsonl"), []byte(record), 0600); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			rec := SessionRecord{SessionID: "guide-recovery", Engine: "codex", CodexHome: private, ProjectPath: t.TempDir()}
			if retained {
				rec.CodexSourceHome = source
			}
			m := &Manager{client: newRecordingSender(4)}
			m.recoverSession(rec)
			raw, err := os.ReadFile(filepath.Join(private, "AGENTS.override.md"))
			if err != nil {
				t.Fatal(err)
			}
			assertSessionWorkGuide(t, string(raw))
			if !strings.Contains(string(raw), "Retained owner instructions.") {
				t.Fatal("recovery did not preserve effective source instructions")
			}
			if _, err := os.Stat(filepath.Join(home, ".codex", "AGENTS.override.md")); !os.IsNotExist(err) {
				t.Fatal("recovery touched owner default instructions home")
			}
		})
	}
}

// Agents learn blerg-runner from the text the daemon injects, not from SKILL.md: the injected text
// must name the commands and carry the rule about destructive questions, and it must be the
// CLI's own first paragraph.
func TestChildSessionGuideIsInjected(t *testing.T) {
	for name, text := range map[string]string{"claude prompt": ccSystemPromptFor(""), "managed block": managedBlock, "codex/shared prompt": sessionWorkGuidePrompt()} {
		for _, want := range []string{"`blerg-runner session start", "`blerg-runner budget`", "`blerg-runner session --help`", "destructive or irreversible", "`blerg-runner ask`", "`--timeout` (seconds)", "Bash tool timeout higher (milliseconds)"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not say %q", name, want)
			}
		}
	}
	if !strings.Contains(managedBlock, "- `blerg-runner session` / `blerg-runner budget` — "+childSessionGuideLine) {
		t.Errorf("managed block lacks the session bullet")
	}
}
