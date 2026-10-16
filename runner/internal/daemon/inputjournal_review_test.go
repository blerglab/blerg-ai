package daemon

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func TestInputJournalDoesNotEvictUnacknowledgedConsumption(t *testing.T) {
	j, err := newInputJournal("", "review-session")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < inputReceiptLimit; i++ {
		id := fmt.Sprintf("input-%d", i)
		if send, _, err := j.accept(agent.Input{ClientID: id, Text: id}); err != nil || !send {
			t.Fatalf("accept %d: %v %v", i, send, err)
		}
		if err := j.settle(agent.Event{ClientEventID: fmt.Sprintf("event-%d", i), Ts: time.Now(), Kind: "user_message", Payload: agent.UserMessagePayload{ClientID: id, Text: id}}); err != nil {
			t.Fatal(err)
		}
	}
	if send, _, err := j.accept(agent.Input{ClientID: "beyond-limit", Text: "Next"}); err == nil || send {
		t.Fatalf("evicted unacknowledged receipt: %v %v", send, err)
	}
	send, receipt, err := j.accept(agent.Input{ClientID: "input-0", Text: "input-0"})
	if err != nil || send || receipt == nil {
		t.Fatalf("forgotten original input would execute twice: %v %+v %v", send, receipt, err)
	}
}

func TestInputJournalFailedSettlementRemainsPending(t *testing.T) {
	j, err := newInputJournal(t.TempDir(), "review-session")
	if err != nil {
		t.Fatal(err)
	}
	in := agent.Input{ClientID: "pending", Text: "Keep working"}
	if _, _, err := j.accept(in); err != nil {
		t.Fatal(err)
	}
	j.dir = filepath.Join(j.dir, "missing-directory")
	if err := j.settle(agent.Event{ClientEventID: "event-1", Ts: time.Now(), Kind: "user_message", Payload: agent.UserMessagePayload{ClientID: in.ClientID, Text: in.Text}}); err == nil {
		t.Fatal("settlement unexpectedly saved")
	}
	send, receipt, err := j.accept(in)
	if err != nil || send || receipt != nil {
		t.Fatalf("failed save must not become a completed receipt: %v %+v %v", send, receipt, err)
	}
}
