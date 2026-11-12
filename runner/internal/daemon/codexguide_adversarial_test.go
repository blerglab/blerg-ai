package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexNativeRPCGuideMatchesShellAvailability(t *testing.T) {
	for _, mode := range []string{"fresh", "resume", "granted", "restricted"} {
		t.Run(mode, func(t *testing.T) {
			bin := fakeCodex(t)
			logPath := filepath.Join(bin, "guide-calls.log")
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_LOG", logPath)
			emitter := &collectEmitter{}
			driver := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: emitter})
			driver.Verbosity = "terse"
			driver.DeveloperInstructions = ccSystemPromptFor("") + verbosityParagraph("terse")
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
				assertAlwaysOnRules(t, params.Instructions)
				if !strings.Contains(params.Instructions, "Verbosity: Terse") {
					t.Fatal("native RPC missed verbosity guidance")
				}
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
