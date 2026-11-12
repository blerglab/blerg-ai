package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/persontime"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
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

func TestInputJournalAcceptsPreNudgeReceiptAfterUpgrade(t *testing.T) {
	dir := t.TempDir()
	j, err := newInputJournal(dir, "legacy-session")
	if err != nil {
		t.Fatal(err)
	}
	// This is the encoding used by Input before PlanNudge existed.
	sum := sha256.Sum256([]byte(`{"ExpectedTurnID":"","Text":"Resume","Shown":"","Source":"chat","ClientID":"legacy"}`))
	j.state.Receipts = []inputReceipt{{ID: "legacy", Hash: hex.EncodeToString(sum[:])}}
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newInputJournal(dir, "legacy-session")
	if err != nil {
		t.Fatal(err)
	}
	send, _, err := reopened.accept(agent.Input{Text: "Resume", Source: "chat", ClientID: "legacy"})
	if err != nil || !send {
		t.Fatalf("old durable input refused after upgrade: send=%v err=%v", send, err)
	}
}

func TestInputJournalNudgeRedelivery(t *testing.T) {
	j, err := newInputJournal(t.TempDir(), "nudge-session")
	if err != nil {
		t.Fatal(err)
	}
	in := agent.Input{ClientID: "same-id", Text: "hello", Source: "chat", PlanNudge: "initial facts"}
	if _, _, err := j.accept(in); err != nil {
		t.Fatal(err)
	}
	in.PlanNudge = "updated facts"
	in.PersonTime = &persontime.Account{Timezone: "UTC"}
	if send, _, err := j.accept(in); err != nil || send {
		t.Fatalf("redelivery: send=%v err=%v", send, err)
	}
}

func TestInputJournalRetention(t *testing.T) {
	j, err := newInputJournal("", "retention-session")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 1024 {
		id := fmt.Sprint(i)
		if send, _, err := j.accept(agent.Input{ClientID: id, Text: "hello"}); err != nil || !send {
			t.Fatalf("accept %d: %v", i, err)
		}
		ev := agent.Event{ClientEventID: "event-" + id, Ts: time.Now().Add(-8 * 24 * time.Hour), Kind: "user_message", Payload: agent.UserMessagePayload{ClientID: id, Text: "hello"}}
		if err := j.settle(ev); err != nil {
			t.Fatal(err)
		}
		if err := j.ack(ev.ClientEventID); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := j.accept(agent.Input{ClientID: "fresh"}); err != nil {
		t.Fatal(err)
	}
	if len(j.state.Receipts) != 1 {
		t.Fatalf("expired receipts retained: %d", len(j.state.Receipts))
	}
}

func TestInputJournalSaveFailureRetry(t *testing.T) {
	dir := t.TempDir()
	j, err := newInputJournal(dir, "save-session")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "save-session.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	in := agent.Input{ClientID: "save-id", Text: "private canary"}
	if _, _, err := j.accept(in); err == nil || !strings.HasPrefix(err.Error(), "receipt_save_failed_") {
		t.Fatalf("cause=%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if send, _, err := j.accept(in); err != nil || !send {
		t.Fatalf("retry send=%v err=%v", send, err)
	}
}

func TestInputJournalFailureLogsAreTypedAndPrivate(t *testing.T) {
	var logs bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { failurelog.Default = old })
	j, err := newInputJournal("", "privacy-session")
	if err != nil {
		t.Fatal(err)
	}
	in := agent.Input{ClientID: "private-id", Text: "private-text-canary"}
	if _, _, err := j.accept(in); err != nil {
		t.Fatal(err)
	}
	in.Text = "private-conflict-canary"
	if _, _, err := j.accept(in); !errors.Is(err, inputAcceptError("client_id_conflict")) {
		t.Fatalf("conflict: %v", err)
	}
	in.Text = "private-text-canary"
	j.state.Receipts[0].EventID = "invalid-event"
	j.state.Receipts[0].Kind = "invalid"
	if _, _, err := j.accept(in); !errors.Is(err, inputAcceptError("invalid_receipt")) {
		t.Fatalf("invalid receipt: %v", err)
	}
	j.state.Receipts = make([]inputReceipt, inputReceiptLimit)
	if _, _, err := j.accept(in); !errors.Is(err, inputAcceptError("pending_limit")) {
		t.Fatalf("capacity: %v", err)
	}
	j.closed = true
	if _, _, err := j.accept(in); !errors.Is(err, inputAcceptError("session_stopping")) {
		t.Fatalf("closed: %v", err)
	}
	for _, want := range []string{"privacy-session", "client_id_conflict", "session_stopping", "invalid_receipt", "pending_limit"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("missing %s", want)
		}
	}
	for _, private := range []string{in.ClientID, "private-text-canary", in.Text} {
		if strings.Contains(logs.String(), private) {
			t.Fatal("input leaked into diagnostics")
		}
	}
}
