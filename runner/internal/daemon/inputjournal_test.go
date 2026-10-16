package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func TestInputJournalDeduplicatesPendingAndConsumedAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	j, err := newInputJournal(dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	in := agent.Input{ClientID: "input-1", Text: "Do this once", Source: "chat"}
	send, receipt, err := j.accept(in)
	if err != nil || !send || receipt != nil {
		t.Fatalf("first accept: %v %v", send, err)
	}
	send, _, err = j.accept(in)
	if err != nil || send {
		t.Fatalf("pending duplicate: %v %v", send, err)
	}
	next, err := newInputJournal(dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	send, _, err = next.accept(in)
	if err != nil || !send {
		t.Fatalf("unconsumed restart: %v %v", send, err)
	}
	ev := agent.Event{ClientEventID: "event-1", Ts: time.Now().UTC(), Kind: "user_message", Payload: agent.UserMessagePayload{ClientID: in.ClientID, Text: in.Text, Source: in.Source, TurnID: "turn-1"}}
	if err = next.settle(ev); err != nil {
		t.Fatal(err)
	}
	final, err := newInputJournal(dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	send, receipt, err = final.accept(in)
	if err != nil || send || receipt == nil || receipt.ClientEventID != ev.ClientEventID {
		t.Fatalf("completed restart: %v %+v %v", send, receipt, err)
	}
	if p, ok := receipt.Payload.(agent.UserMessagePayload); !ok || p.ClientID != in.ClientID {
		t.Fatalf("receipt lost correlation: %+v", receipt)
	}
}

func TestInputJournalRejectsConflictingIDsAndUnsafeStorage(t *testing.T) {
	dir := t.TempDir()
	j, err := newInputJournal(dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	in := agent.Input{ClientID: "input-1", Text: "First"}
	if _, _, err = j.accept(in); err != nil {
		t.Fatal(err)
	}
	in.Text = "Different"
	if _, _, err = j.accept(in); err == nil {
		t.Fatal("accepted conflicting client ID")
	}
	if _, err = newInputJournal(dir, "../outside"); err == nil {
		t.Fatal("accepted path traversal")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err = os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(dir, "session-2.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = newInputJournal(dir, "session-2"); err == nil {
		t.Fatal("read symlink journal")
	}
	info, err := os.Stat(filepath.Join(dir, "session-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("journal permissions: %v", info.Mode())
	}
}

func TestInputReceiptReplayDoesNotReobserveOrDuplicateBuffer(t *testing.T) {
	observed := 0
	emitter := newWSEmitter(&agentTestSender{}, "session-1", func(agent.Event) { observed++ })
	ev := agent.Event{ClientEventID: "event-1", Ts: time.Now(), Kind: "user_message", Payload: agent.UserMessagePayload{ClientID: "input-1", Text: "Only once"}}
	emitter.Emit(ev)
	emitter.Replay(ev)
	if observed != 1 {
		t.Fatalf("replayed input observed %d times", observed)
	}
	if emitter.pendingCount() != 1 {
		t.Fatalf("replay duplicated pending event buffer: %d", emitter.pendingCount())
	}
}
