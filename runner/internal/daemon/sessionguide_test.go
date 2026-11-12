package daemon

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/skills"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assertSessionWorkGuide(t *testing.T, text string) {
	t.Helper()
	for _, line := range []string{childSessionGuideLine, workGuideLine, topicGuideLine, checklistWorkMapGuideLine, planAutoGuideLine, boardAccessGuideLine, ticketEstimateGuideLine} {
		if !strings.Contains(text, line) {
			t.Fatalf("missing shared guidance: %s", line)
		}
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

// Agents learn blerg-runner from the text the daemon injects, not from SKILL.md: the injected text
// must name the commands and carry the rule about destructive questions, and it must be the
// CLI's own first paragraph.
func TestChildSessionGuideIsInjected(t *testing.T) {
	for name, text := range map[string]string{"claude prompt": ccSystemPromptFor(""), "managed block": managedBlock, "codex/shared prompt": sessionWorkGuidePrompt()} {
		for _, want := range []string{"`blerg-runner session start", "`blerg-runner budget`", "`blerg-runner session --help`", "destructive or irreversible", "`blerg-runner ask`", "`blerg-runner session answer", "`blerg-runner session escalate", "wait for Main without a time limit", "`--timeout` (seconds)", "Bash tool timeout higher (milliseconds)"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not say %q", name, want)
			}
		}
	}
	if !strings.Contains(managedBlock, "- `blerg-runner session` / `blerg-runner budget` — "+childSessionGuideLine) {
		t.Errorf("managed block lacks the session bullet")
	}
}

func TestWorkerQuestionsGuideRoutesThroughMain(t *testing.T) {
	for _, text := range []string{ccSystemPromptFor(""), managedBlock, sessionWorkGuidePrompt()} {
		for _, want := range []string{"All worker questions go to Main", "Workers send work for review with `blerg-runner ask`", "Main requests the person's approval only when needed", "--summary", "--recommendation", "only the person can approve"} {
			if !strings.Contains(text, want) {
				t.Errorf("missing parent-first guidance: %s", want)
			}
		}
		if strings.Contains(text, "A child can require the person with") {
			t.Error("guide still tells workers to bypass Main")
		}
	}
}

func TestUsageNoticeGuidanceIsInEverySessionGuide(t *testing.T) {
	for _, text := range []string{sessionWorkGuidePrompt(), sessionWorkGuideManaged()} {
		for _, want := range []string{"usage_limit", "80% prefer other engines", "90% start nothing new", "95% finish the current step and save state"} {
			if !strings.Contains(text, want) {
				t.Fatalf("guide omits %q", want)
			}
		}
	}
}

func TestSessionSkillPolicyGuide(t *testing.T) {
	off := false
	for _, main := range []bool{false, true} {
		for _, disabled := range []bool{false, true} {
			override := skills.Overrides{}
			if disabled {
				override.TDD, override.UX = &off, &off
			}
			policy := skills.Resolve(main, override)
			text := ccSystemPromptForPolicy("", policy)
			for marker, want := range map[string]bool{"coordinating": main, "test-driven-development": !disabled, "ux-design": !disabled, "planning": true} {
				if strings.Contains(text, strings.TrimPrefix(marker, "# ")) != want {
					t.Errorf("main=%v disabled=%v marker=%s", main, disabled, marker)
				}
			}
		}
	}
}

func TestSkillPolicyGuideAndProvisioningPrecedence(t *testing.T) {
	on, off := true, false
	for _, main := range []bool{false, true} {
		policy := skills.Resolve(main, skills.Overrides{TDD: &off}, skills.Overrides{TDD: &on}, skills.Overrides{TDD: &on, UX: &off})
		for _, restricted := range []bool{false, true} {
			options := []ccOption{withSessionGuide(), func(o *ccOptions) { o.SkillPolicy = &policy }}
			if restricted {
				options = append(options, withRestrictTools())
			}
			text, ok := appendedPrompt(ccCommonArgs("", "", "", options...))
			if !ok || strings.Contains(text, "Test-driven development") || strings.Contains(text, "UX design") || strings.Contains(text, "Coordinate as Main") != main {
				t.Fatalf("guide policy main=%v restricted=%v was not applied", main, restricted)
			}
		}
		home, repo, _ := setupSkillDirs(t)
		if _, err := ensureSkillSymlink(repo, home, policy); err != nil {
			t.Fatal(err)
		}
		for _, engine := range []string{".claude", ".codex", ".agents", ".hermes"} {
			for _, name := range []string{"test-driven-development", "ux-design", "coordinating"} {
				_, err := os.Stat(filepath.Join(home, engine, "skills", name, "SKILL.md"))
				want := name == "coordinating" && main
				if (err == nil) != want {
					t.Fatalf("%s/%s main=%v: %v", engine, name, main, err)
				}
			}
		}
	}
}

func TestSkillPolicyRecoveryRecord(t *testing.T) {
	off := false
	policy := skills.Resolve(true, skills.Overrides{UX: &off})
	dir := filepath.Join(t.TempDir(), "records")
	if err := writeAgentRecord(dir, AgentRecord{SessionID: "skill-record", Repo: "project", WorkDir: "/workspace", SkillPolicy: &policy}); err != nil {
		t.Fatal(err)
	}
	records := listAgentRecords(dir)
	if len(records) != 1 || records[0].SkillPolicy == nil || *records[0].SkillPolicy != policy {
		t.Fatal("host recovery lost the applied inventory")
	}
}
