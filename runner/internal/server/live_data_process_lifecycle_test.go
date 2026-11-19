package server

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"testing"
)

func TestLiveDataCrossProcessDemandAndLeaseFences(t *testing.T) {
	h := newLiveProcessMatrix(t)
	for _, mode := range []string{"demand expired", "lease expired", "new fence", "attempt expired"} {
		t.Run(mode, func(t *testing.T) {
			h.t = t
			h.prepare()
			a := h.attempt()
			before := h.market.call(liveProcessCommand{Action: "counts"})
			if h.runner.call(liveProcessCommand{Action: "lifecycle", Mode: mode}).Error != "" {
				t.Fatal("lifecycle fixture")
			}
			if result := h.execute(a); result.Cause == "" {
				t.Fatal("expired demand or receipt dispatched")
			}
			after := h.market.call(liveProcessCommand{Action: "counts"})
			if before.Calls != after.Calls || before.Lists != after.Lists {
				t.Fatal("expired lease, demand, receipt or fence reached provider")
			}
		})
	}
}
func TestLiveDataCrossProcessRetentionPurgesQueuedRows(t *testing.T) {
	for _, mode := range []string{"idle week", "sample expired"} {
		t.Run(mode, func(t *testing.T) {
			h := newLiveProcessMatrix(t)
			h.prepare()
			liveProcessGoodSample(t, h)
			if h.runner.call(liveProcessCommand{Action: "lifecycle", Mode: mode}).Error != "" {
				t.Fatal("retention fixture")
			}
			if h.runner.call(liveProcessCommand{Action: "maintain"}).Error != "" {
				t.Fatal("retention sweep")
			}
			sample := h.runner.call(liveProcessCommand{Action: "sample"})
			if sample.Retained || (mode == "idle week" && sample.Enabled) {
				t.Fatal("retention kept payload or idle binding enabled")
			}
			queued := h.runner.call(liveProcessCommand{Action: "queued", Mode: "owner"})
			var frame protocol.DatasetDelta
			if !queued.Available || json.Unmarshal(queued.Rows, &frame) != nil {
				t.Fatal("queued retention fixture")
			}
			if len(frame.Upserts) > 0 || frame.Status != "access_removed" {
				t.Fatal("expired local sample escaped through queued delivery")
			}
		})
	}
}
func TestLiveDataCrossProcessNoViewerDoesNotPoll(t *testing.T) {
	h := newLiveProcessMatrix(t)
	h.prepare()
	before := h.market.call(liveProcessCommand{Action: "counts"})
	h.runner.call(liveProcessCommand{Action: "tick"})
	h.runner.call(liveProcessCommand{Action: "finish"})
	after := h.market.call(liveProcessCommand{Action: "counts"})
	if before.Calls != after.Calls || before.Lists != after.Lists {
		t.Fatal("hidden or unsupported preview created polling demand")
	}
}
