package daemon

import (
	"encoding/json"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"testing"
	"time"
)

func TestClaudeCurrentLimitWordings(t *testing.T) {
	for _, name := range []string{"session limit", "weekly limit", "Opus limit", "Sonnet limit", "Fable limit", "usage credit limit", "limit", "usage limit", "organization's usage limit"} {
		for _, code := range []string{`"rate_limit"`, `""`} {
			t.Run(name+code, func(t *testing.T) {
				em := &collectEmitter{}
				d := newClaudeCodeDriver("", "", "", em, nil)
				d.emitTurnMessage(agent.UserMessagePayload{Text: "continue", Source: "chat"})
				d.noteRateLimit(&ccLine{Error: json.RawMessage(code)}, "You've hit your "+name+" · resets 3pm (UTC)")
				if !contains(em.kinds(), "usage_limit") {
					t.Fatal("no normalized usage refusal")
				}
			})
		}
	}
	t.Run("coded_unknown_wording", func(t *testing.T) {
		em := &collectEmitter{}
		d := newClaudeCodeDriver("", "", "", em, nil)
		d.emitTurnMessage(agent.UserMessagePayload{Text: "continue", Source: "chat"})
		d.noteRateLimit(&ccLine{Error: json.RawMessage(`"rate_limit"`)}, "Account quota exhausted")
		if !contains(em.kinds(), "usage_limit") {
			t.Fatal("rate_limit code dropped")
		}
	})
}

func TestClaudeResetDatesAndJustPassed(t *testing.T) {
	zone, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 10, 27, 15, 0, 5, 0, zone)
	for _, text := range []string{"resets 3pm (America/New_York)", "resets Oct 27, 3pm (America/New_York)", "resets Oct 27, 2026, 3pm (America/New_York)", "resets Oct 27, 2026 at 3pm (America/New_York)"} {
		got, ok := claudeRateLimitReset(text, now)
		want := time.Date(2026, 10, 27, 15, 1, 0, 0, zone)
		if !ok || !got.Equal(want) {
			t.Errorf("%s: %v %v, want %v", text, got, ok, want)
		}
	}
	got, ok := claudeRateLimitReset("resets Jan 2, 2027, 9am (UTC)", time.Date(2026, 12, 30, 8, 0, 0, 0, time.UTC))
	if !ok || !got.Equal(time.Date(2027, 1, 2, 9, 1, 0, 0, time.UTC)) {
		t.Fatalf("dated weekly year: %v %v", got, ok)
	}
}

func TestCodexPrintedRetryTimes(t *testing.T) {
	zone, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, zone)
	for _, retry := range []string{"3:25 PM", "Nov 2nd, 2026 12:59 AM", "Nov 1st, 2026 12:59 AM", "Nov 3rd, 2026 12:59 AM", "Nov 4th, 2026 12:59 AM"} {
		p, ok := codexUsageRefusalAt(codexEvent{Type: "error", Message: "You've hit your usage limit. Upgrade or try again at " + retry + "."}, "turn", now)
		if !ok || p.ResetAt == nil {
			t.Errorf("%s: unreadable retry", retry)
			continue
		}
		if retry == "Nov 2nd, 2026 12:59 AM" && !p.ResetAt.Equal(time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)) {
			t.Errorf("DST: %v", p.ResetAt)
		}
	}
}

func TestCodexRefusalRespectsSandboxClock(t *testing.T) {
	for _, tc := range []struct {
		name, text     string
		sandbox, known bool
	}{
		{"host_local", "You've hit your usage limit. Try again at 3:25 PM.", false, true},
		{"sandbox_local", "You've hit your usage limit. Try again at 3:25 PM.", true, false},
		{"sandbox_instant", "You've hit your usage limit. Try again at 2030-01-02T03:04:05Z.", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			em := &collectEmitter{}
			d := newCodexDriver(AgentDriverOpts{Emitter: em})
			d.usageTurnID = "turn"
			if tc.sandbox {
				d.prefix = sandboxExec{"docker", "exec", "fixture-container"}
			}
			d.appUsageRefusal(tc.text)
			em.mu.Lock()
			defer em.mu.Unlock()
			if len(em.events) != 1 {
				t.Fatal("missing normalized refusal")
			}
			p, ok := em.events[0].Payload.(agent.UsageLimitPayload)
			if !ok || (p.ResetAt != nil) != tc.known {
				t.Fatalf("wrong reset for CLI clock: %+v", p)
			}
		})
	}
}

func TestClaudeRecentWeeklyResetAcrossYear(t *testing.T) {
	now := time.Date(2027, time.January, 1, 0, 0, 5, 0, time.UTC)
	got, ok := claudeRateLimitReset("You've hit your weekly limit · resets Dec 31, 11:59pm (UTC)", now)
	want := time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)
	if !ok || !got.Equal(want) {
		t.Fatalf("just-passed weekly reset crossed the year: got %v %v, want %v", got, ok, want)
	}
}

func TestClaudeAssistantLimitTextCannotSchedule(t *testing.T) {
	for _, line := range []string{
		`{"type":"assistant","message":{"model":"claude-opus-5","content":[{"type":"text","text":"You've hit your weekly limit · resets 3pm (UTC)"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-example","content":"You've hit your weekly limit · resets 3pm (UTC)"}]}}`,
	} {
		em := &collectEmitter{}
		d := newClaudeCodeDriver("", "", "", em, nil)
		d.emitTurnMessage(agent.UserMessagePayload{Text: "continue", Source: "chat"})
		d.onLine(&ccProc{}, []byte(line))
		if contains(em.kinds(), "usage_limit") {
			t.Fatal("ordinary assistant/tool text scheduled a continuation")
		}
	}
}

func TestPrintedResetDuringRepeatedHour(t *testing.T) {
	zone, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 11, 1, 5, 45, 0, 0, time.UTC).In(zone) // first 1:45 AM
	want := time.Date(2026, 11, 1, 6, 31, 0, 0, time.UTC)         // end of the later 1:30 AM minute
	for _, text := range []string{"resets 1:30am", "resets Nov 1, 1:30am (America/New_York)", "resets Nov 1, 2026, 1:30am (America/New_York)"} {
		got, ok := claudeRateLimitReset(text, now)
		if !ok || !got.Equal(want) {
			t.Errorf("%s: got %v %v, want %v", text, got, ok, want)
		}
	}
	p, ok := codexUsageRefusalAt(codexEvent{Type: "error", Message: "You've hit your usage limit. Try again at 1:30 AM."}, "turn", now)
	if !ok || p.ResetAt == nil || !p.ResetAt.Equal(want) {
		t.Fatalf("Codex repeated hour: %+v", p)
	}
}

func TestPrintedResetTomorrowDuringRepeatedHour(t *testing.T) {
	zone, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 11, 1, 3, 45, 0, 0, time.UTC).In(zone) // October 31 at 11:45 PM
	got, ok := claudeRateLimitReset("resets 1:30am", now)
	want := time.Date(2026, 11, 1, 6, 31, 0, 0, time.UTC)
	if !ok || !got.Equal(want) {
		t.Fatalf("next-day repeated hour: %v %v, want %v", got, ok, want)
	}
}
