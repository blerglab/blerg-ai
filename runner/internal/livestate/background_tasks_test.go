package livestate

import (
	"encoding/json"
	"testing"
)

func TestBackgroundTaskFoldIncarnationsAndTargetedDone(t *testing.T) {
	events := []Event{
		{Seq: 1, ID: "old-start", Kind: "background_task_started", Payload: json.RawMessage(`{"task_id":"same","call_id":"old-call","daemon_lifetime":"old"}`)},
		{Seq: 2, ID: "new-start", Kind: "background_task_started", Payload: json.RawMessage(`{"task_id":"same","call_id":"new-call","daemon_lifetime":"new"}`)},
		{Seq: 3, ID: "old-stop", Kind: "background_task_done", Payload: json.RawMessage(`{"task_id":"same","start_event_id":"old-start"}`)},
		{Seq: 4, ID: "old-notice", Kind: "background_task_done", Payload: json.RawMessage(`{"task_id":"same","call_id":"old-call","daemon_lifetime":"old"}`)},
	}
	open := OpenBackgroundTasks(events)
	if len(open) != 1 || open[0].StartEventID != "new-start" || open[0].Lifetime != "new" {
		t.Fatalf("old completion stopped new incarnation: %+v", open)
	}
	events = append(events, Event{Seq: 5, Kind: "background_task_done", Payload: json.RawMessage(`{"task_id":"same","start_event_id":"new-start"}`)})
	if got := OpenBackgroundTasks(events); len(got) != 0 {
		t.Fatalf("targeted stop ignored %+v", got)
	}
}
