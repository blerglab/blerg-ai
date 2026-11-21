package daemon

import (
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
)

type inputRecorder struct {
	fakeRefreshDriver
	inputs []agent.Input
}

func (d *inputRecorder) EnqueueInput(in agent.Input) { d.inputs = append(d.inputs, in) }

// No engine can promise that a message sent during a turn starts no other (the turn can end first, or
// its last response can already be on its way), so the line never is one. The person reads it at once;
// the agent reads it, in Blerg's voice and once, with the next input it gets anyway.
func TestTaskLimitNeverSendsInputOfItsOwn(t *testing.T) {
	const line = "blerg: process limit reached (4096 tasks); retry with less parallelism"
	for _, status := range []string{"running", "idle"} {
		t.Run(status, func(t *testing.T) {
			sender := &agentTestSender{}
			driver := &inputRecorder{}
			sess := &agentSession{loop: driver, emitter: newWSEmitter(sender, "s1", nil)}
			h := &AgentHost{sessions: map[string]*agentSession{"s1": sess}}
			sess.status.Store(status)

			h.TaskLimit("s1", line)
			if len(driver.inputs) != 0 {
				t.Fatalf("sent input: %+v", driver.inputs)
			}
			note := sender.waitForKind(t, "user_message")
			if !strings.Contains(string(note.Payload), line) || !strings.Contains(string(note.Payload), `"source":"system"`) {
				t.Fatalf("note: %s", note.Payload)
			}

			h.UserInput("s1", agent.Input{Text: "go on", Source: "chat"})
			h.UserInput("s1", agent.Input{Text: "and on", Source: "chat"})
			if len(driver.inputs) != 2 {
				t.Fatalf("inputs: %+v", driver.inputs)
			}
			const nonce = "0123456789abcdef"
			got := driver.inputs[0].ModelText(nonce)
			if !strings.HasPrefix(got, "go on\n\n") || !strings.Contains(got, line) || !strings.Contains(got, "process_limit · "+nonce) {
				t.Fatalf("not relayed in Blerg's voice: %q", got)
			}
			if plain := driver.inputs[0].PlainText(); plain != "go on\n\n"+line {
				t.Fatalf("engines with no marker read %q", plain)
			}
			if driver.inputs[0].Payload().Text != "go on" {
				t.Fatalf("transcript: %+v", driver.inputs[0].Payload())
			}
			if strings.Contains(driver.inputs[1].ModelText(nonce), line) {
				t.Fatalf("told twice: %+v", driver.inputs[1])
			}
		})
	}
	h := &AgentHost{sessions: map[string]*agentSession{}}
	h.TaskLimit("ended", line) // no panic
}
