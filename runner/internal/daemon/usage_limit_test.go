package daemon

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// Invented structured CLI messages; never raw terminal captures.
func TestCodexUsageRefusal(t *testing.T) {
	for _, tc := range []struct {
		line        string
		want, reset bool
	}{
		{`{"type":"turn.failed","error":{"message":"You've hit your usage limit. Try again at 2030-01-02T03:04:05Z."}}`, true, true},
		{`{"type":"error","message":"You've hit your usage limit"}`, true, false},
		{`{"type":"turn.failed","error":{"message":"Rate limit reached (HTTP 429)"}}`, false, false},
		{`{"type":"item.completed","item":{"type":"agent_message","text":"You've hit your usage limit"}}`, false, false},
		{`{"type":"turn.failed","error":{"message":"Connection closed"}}`, false, false},
		{`{"type":"account.rateLimits.updated","message":"You've hit your usage limit"}`, false, false},
	} {
		var ev codexEvent
		if err := json.Unmarshal([]byte(tc.line), &ev); err != nil {
			t.Fatal(err)
		}
		p, ok := codexUsageRefusal(ev, "turn-example")
		if ok != tc.want {
			t.Errorf("%s: refusal=%v", tc.line, ok)
			continue
		}
		if ok && (p.Provider != "codex" || p.TurnID != "turn-example" || (p.ResetAt != nil) != tc.reset) {
			t.Errorf("payload: %+v", p)
		}
	}
}

func TestClaudeNormalizedUsageRefusal(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver("", "", "", em, nil)
	d.emitTurnMessage(agent.UserMessagePayload{Text: "continue", Source: "chat"})
	d.noteRateLimit(&ccLine{Error: json.RawMessage(`"rate_limit"`)}, "You've hit your limit")
	var turn string
	var found bool
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, ev := range em.events {
		switch p := ev.Payload.(type) {
		case agent.UserMessagePayload:
			turn = p.TurnID
		case agent.UsageLimitPayload:
			found = true
			if p.Provider != "claude" || p.TurnID != turn || p.ResetAt != nil {
				t.Errorf("payload %+v turn %s", p, turn)
			}
		}
	}
	if !found || turn == "" {
		t.Fatal("missing correlated unknown-reset refusal")
	}
}

func TestUsageResumeRejectsStaleAndBusy(t *testing.T) {
	for _, engine := range []string{"claude", "codex"} {
		for _, state := range []string{"stale", "busy", "queued", "idle"} {
			t.Run(engine+"/"+state, func(t *testing.T) {
				em := &collectEmitter{}
				in := agent.Input{Text: "Continue the interrupted task", Source: "limit_resume", ExpectedTurnID: "current"}
				if state == "stale" {
					in.ExpectedTurnID = "old"
				}
				if engine == "claude" {
					d := newClaudeCodeDriver("", "", "", em, nil)
					d.usageTurnID = "current"
					d.steer.legacy = false
					if state == "busy" {
						d.steer.running = true
					}
					if state == "queued" {
						d.steer.sq = append(d.steer.sq, steerQueued{})
					}
					d.EnqueueInput(in)
					if state == "idle" && len(d.steer.sq) != 1 {
						t.Fatal("idle continuation not queued")
					}
				} else {
					d := newCodexDriver(AgentDriverOpts{Emitter: em})
					d.usageTurnID = "current"
					if state == "busy" {
						d.activeTurn = true
					}
					if state == "queued" {
						d.queue <- queuedMsg{}
					}
					d.EnqueueInput(in)
					if state == "idle" && len(d.queue) != 1 {
						t.Fatal("idle continuation not queued")
					}
				}
				em.mu.Lock()
				defer em.mu.Unlock()
				if state == "idle" && len(em.events) != 0 {
					t.Fatalf("unexpected events: %v", em.events)
				}
				if state != "idle" {
					if len(em.events) != 1 {
						t.Fatalf("missing rejection: %v", em.events)
					}
					p, ok := em.events[0].Payload.(agent.ErrorPayload)
					if !ok || p.Code != "limit_resume_stale" || !p.Retryable {
						t.Fatalf("bad rejection %+v", p)
					}
				}
			})
		}
	}
}

func TestCodexDriverCorrelatesUsageRefusal(t *testing.T) {
	dir := fakeCodex(t)
	t.Setenv("FAKE_CODEX_LIMIT", "1")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Emitter: em, WorkDir: dir})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("continue", "chat")
	deadline := time.Now().Add(15 * time.Second)
	for !contains(em.kinds(), "turn_done") {
		if time.Now().After(deadline) {
			t.Fatal("turn did not finish")
		}
		time.Sleep(time.Millisecond * 10)
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	turn := ""
	refusals := 0
	for _, ev := range em.events {
		switch p := ev.Payload.(type) {
		case agent.UserMessagePayload:
			turn = p.TurnID
		case agent.UsageLimitPayload:
			refusals++
			if p.TurnID == "" || p.TurnID != turn || p.ResetAt == nil {
				t.Fatalf("uncorrelated refusal %+v, turn %q", p, turn)
			}
		}
	}
	if refusals != 1 {
		t.Fatalf("refusals=%d", refusals)
	}
}

func TestClaudeRefusalCodesAndSubagentExclusion(t *testing.T) {
	for _, tc := range []struct {
		text, parent string
		want         bool
	}{
		{"API Error: Rate limit reached", "", true},
		{"You've hit your limit", "tool-child", false},
		{"5-hour limit reached ∙ resets 3pm", "", true},
		{"Weekly limit reached ∙ resets Oct 20, 3pm", "", true},
	} {
		t.Run(tc.text+tc.parent, func(t *testing.T) {
			em := &collectEmitter{}
			d := newClaudeCodeDriver("", "", "", em, nil)
			d.emitTurnMessage(agent.UserMessagePayload{Text: "continue", Source: "chat"})
			d.noteRateLimit(&ccLine{Error: json.RawMessage(`"rate_limit"`), ParentToolUseID: tc.parent}, tc.text)
			if contains(em.kinds(), "usage_limit") != tc.want {
				t.Fatalf("events: %v", em.kinds())
			}
		})
	}
}

func TestUsageTurnIdentitySurvivesRecoveryRecord(t *testing.T) {
	k := newRecordKeeper(t.TempDir(), nil, AgentRecord{SessionID: "session-example"})
	k.setCurrentTurnID("turn-example")
	raw, err := json.Marshal(k.rec)
	if err != nil {
		t.Fatal(err)
	}
	var restored AgentRecord
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.CurrentTurnID != "turn-example" {
		t.Fatalf("lost turn identity %q", restored.CurrentTurnID)
	}
	// Harness output with no turn cannot erase the last accepted input.
	k.setCurrentTurnID("")
	if k.rec.CurrentTurnID != "turn-example" {
		t.Fatal("harness output erased turn")
	}
}

func TestClaudeNormalizedResetTimezoneSafety(t *testing.T) {
	// The CLI in a local sandbox has a different timezone from its desktop
	// daemon. A bare wall clock is therefore insufficient to schedule a retry.
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, time.October, 15, 10, 0, 0, 0, zone)
	for _, tc := range []struct {
		name, text         string
		sandbox, wantReset bool
		want               time.Time
	}{
		{name: "sandbox_bare_clock", text: "You've hit your limit · resets 3pm", sandbox: true},
		{name: "sandbox_bare_date", text: "Weekly limit reached · resets Oct 16, 3pm", sandbox: true},
		{name: "host_bare_clock", text: "You've hit your limit · resets 3pm", wantReset: true, want: time.Date(2030, time.October, 15, 19, 1, 0, 0, time.UTC)},
		{name: "sandbox_explicit_zone", text: "You've hit your limit · resets 3pm (America/New_York)", sandbox: true, wantReset: true, want: time.Date(2030, time.October, 15, 19, 1, 0, 0, time.UTC)},
		{name: "sandbox_unix", text: "Claude AI usage limit reached|" + strconv.FormatInt(now.Add(3*time.Hour).Unix(), 10), sandbox: true, wantReset: true, want: now.Add(3 * time.Hour)},
		{name: "host_invalid_zone", text: "You've hit your limit · resets 3pm (Invalid/Invented)"},
		{name: "host_local_zone_alias", text: "You've hit your limit · resets 3pm (Local)"},
		{name: "host_malformed_zone", text: "You've hit your limit · resets 3pm (Invalid/Invented"},
		{name: "sandbox_invalid_zone", text: "You've hit your limit · resets 3pm (Invalid/Invented)", sandbox: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			em := &collectEmitter{}
			d := newClaudeCodeDriver("", "", "", em, nil)
			d.now = func() time.Time { return now }
			if tc.sandbox {
				d.prefix = sandboxExec{"docker", "exec", "fixture-container"}
			}
			meterReports := 0
			d.onRateLimit = func(time.Time) { meterReports++ }
			d.emitTurnMessage(agent.UserMessagePayload{Text: "continue", Source: "chat"})
			d.noteRateLimit(&ccLine{Error: json.RawMessage(`"rate_limit"`)}, tc.text)
			if meterReports != 1 {
				t.Fatal("existing account-meter reporting changed")
			}
			em.mu.Lock()
			defer em.mu.Unlock()
			for _, event := range em.events {
				if p, ok := event.Payload.(agent.UsageLimitPayload); ok {
					if (p.ResetAt != nil) != tc.wantReset {
						t.Fatalf("reset=%v, want known=%v", p.ResetAt, tc.wantReset)
					}
					if p.ResetAt != nil && (!p.ResetAt.Equal(tc.want) || p.ResetAt.Location() != time.UTC) {
						t.Fatalf("reset=%v, want UTC %v", p.ResetAt, tc.want.UTC())
					}
					return
				}
			}
			t.Fatal("missing normalized refusal")
		})
	}
}

// The provider's session-limit wording is synthetic CLI error text, not prose
// from an assistant. It must reach both the account meter and the retry scheduler.
func TestClaudeSessionLimitRefusal(t *testing.T) {
	now := time.Date(2030, time.October, 15, 14, 55, 0, 0, time.UTC)
	for _, code := range []string{`"rate_limit"`, `""`} {
		t.Run(code, func(t *testing.T) {
			em := &collectEmitter{}
			d := newClaudeCodeDriver("", "", "", em, nil)
			d.now = func() time.Time { return now }
			d.emitTurnMessage(agent.UserMessagePayload{Text: "continue", Source: "chat"})
			var meterReset time.Time
			d.onRateLimit = func(reset time.Time) { meterReset = reset }
			d.noteRateLimit(&ccLine{Error: json.RawMessage(code)}, "You've hit your session limit · resets 3pm (UTC)")
			em.mu.Lock()
			defer em.mu.Unlock()
			for _, ev := range em.events {
				if p, ok := ev.Payload.(agent.UsageLimitPayload); ok {
					if p.Provider != "claude" || p.TurnID == "" || p.ResetAt == nil || !p.ResetAt.Equal(now.Add(6*time.Minute)) || !meterReset.Equal(*p.ResetAt) {
						t.Fatalf("incorrect session-limit refusal: %+v, meter reset %v", p, meterReset)
					}
					return
				}
			}
			t.Fatal("session-limit error did not emit usage_limit; checked option cannot arm")
		})
	}
}
