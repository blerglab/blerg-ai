package daemon

import (
	"encoding/json"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func subagentDones(em *collectEmitter) []agent.SubagentPayload {
	var out []agent.SubagentPayload
	for _, ev := range em.snapshot() {
		if ev.Kind == "subagent_done" {
			out = append(out, ev.Payload.(agent.SubagentPayload))
		}
	}
	return out
}

// A foreground subagent whose Agent call returned an error is marked is_error; one whose call
// returned normally is not.
func TestSubagentDoneCarriesTheCallsError(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	d.startSubagent("toolu_ok", json.RawMessage(`{"description":"fine","prompt":"p"}`))
	d.startSubagent("toolu_bad", json.RawMessage(`{"description":"broken","prompt":"p"}`))
	d.endSubagent("toolu_ok", "all good", false)
	d.endSubagent("toolu_bad", "Agent crashed", true)
	done := subagentDones(em)
	if len(done) != 2 {
		t.Fatalf("subagent_done = %+v, want two", done)
	}
	if done[0].ChildID != "toolu_ok" || done[0].IsError {
		t.Errorf("a normal end = %+v, want is_error false", done[0])
	}
	if done[1].ChildID != "toolu_bad" || !done[1].IsError || done[1].Summary != "error: Agent crashed" {
		t.Errorf("a failed call = %+v, want is_error and an error: summary", done[1])
	}
}

// A background subagent ends by its notice: only the status "failed" marks it
// is_error; completed, empty, killed and stopped (a person stopped it) do not. Its launch never does.
func TestBackgroundSubagentNoticeSetsIsError(t *testing.T) {
	for _, c := range []struct {
		status string
		want   bool
	}{{"completed", false}, {"", false}, {"failed", true}, {"killed", false}, {"stopped", false}} {
		em := &collectEmitter{}
		d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
		d.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
		d.startSubagent("toolu_bg", json.RawMessage(`{"description":"watch","prompt":"p","run_in_background":true}`))
		d.endSubagent("toolu_bg", "Async agent launched successfully.\nagentId: a1b2c3 (internal)", false)
		d.closeNotified(ccNotice{taskID: "a1b2c3", callID: "toolu_bg", status: c.status, result: "the end"})
		done := subagentDones(em)
		if len(done) != 2 {
			t.Fatalf("status %q: subagent_done = %+v, want the launch and the end", c.status, done)
		}
		if !done[0].Background || done[0].IsError {
			t.Errorf("status %q: the launch = %+v, want background and no is_error", c.status, done[0])
		}
		if done[1].IsError != c.want {
			t.Errorf("status %q: the end's is_error = %v, want %v", c.status, done[1].IsError, c.want)
		}
	}
}
