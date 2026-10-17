package livestate

import (
	"encoding/json"
	"testing"
	"time"
)

func TestChildConfiguredEffortClearsInLiveSnapshot(t *testing.T) {
	events := []Event{
		{Seq: 1, Ts: time.Now(), Kind: "subagent_started", Payload: json.RawMessage(`{"child_id":"child","effort":"low"}`)},
		{Seq: 2, Ts: time.Now(), Kind: "subagent_update", Payload: json.RawMessage(`{"child_id":"child","effort":""}`)},
	}
	children, _ := readSubagents(events)
	if len(children) != 1 || children[0].view(false).Effort != "" {
		t.Fatal("unknown child effort retained stale setting")
	}
}

func TestChildConfiguredEffortSurvivesSameModelPartialUpdate(t *testing.T) {
	events := []Event{
		{Seq: 1, Ts: time.Now(), Kind: "subagent_started", Payload: json.RawMessage(`{"child_id":"child","model":"a","effort":"low"}`)},
		{Seq: 2, Ts: time.Now(), Kind: "subagent_update", Payload: json.RawMessage(`{"child_id":"child","model":"a"}`)},
	}
	children, _ := readSubagents(events)
	if got := children[0].view(false).Effort; got != "low" {
		t.Fatalf("same-model partial update lost effort: %q", got)
	}
	events = append(events, Event{Seq: 3, Ts: time.Now(), Kind: "subagent_update", Payload: json.RawMessage(`{"child_id":"child","model":"b"}`)})
	children, _ = readSubagents(events)
	if children[0].view(false).Effort != "" {
		t.Fatal("changed model inherited stale effort")
	}
}

func TestChildConfiguredEffortExplicitNullClears(t *testing.T) {
	events := []Event{
		{Seq: 1, Ts: time.Now(), Kind: "subagent_started", Payload: json.RawMessage(`{"child_id":"child","model":"a","effort":"high"}`)},
		{Seq: 2, Ts: time.Now(), Kind: "subagent_update", Payload: json.RawMessage(`{"child_id":"child","model":"a","effort":null}`)},
	}
	children, _ := readSubagents(events)
	if children[0].view(false).Effort != "" {
		t.Fatal("explicit null retained old effort")
	}
}

func TestChildModelAndEffortStayPairedAfterBackgroundLaunch(t *testing.T) {
	events := []Event{
		{Seq: 1, Ts: time.Now(), Kind: "subagent_started", Payload: json.RawMessage(`{"child_id":"child","model":"a","effort":"high"}`)},
		{Seq: 2, Ts: time.Now(), Kind: "subagent_done", Payload: json.RawMessage(`{"child_id":"child","model":"a","effort":"high","background":true}`)},
		{Seq: 3, Ts: time.Now(), Kind: "subagent_update", Payload: json.RawMessage(`{"child_id":"child","model":"b","effort":"low"}`)},
	}
	children, _ := readSubagents(events)
	got := children[0].view(false)
	if got.Model != "b" || got.Effort != "low" {
		t.Fatalf("background launch overrode newer settings: %+v", got)
	}
}
