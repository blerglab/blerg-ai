package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/core/internal/api"
)

func TestLiveDataPostureTransportAndClock(t *testing.T) {
	var mode atomic.Int64
	var calls atomic.Int64
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/internal/live-data/posture" || r.Header.Get("X-Internal-Key") != "fixture" || r.Header.Get("Authorization") != "" {
			t.Error("posture transport did not use dedicated service authority")
			w.WriteHeader(403)
			return
		}
		report := ld.PostureReport{Safe: true, Cause: "isolated_cluster", CheckedAt: time.Now().UTC()}
		switch mode.Load() {
		case 1:
			report.Safe = false
			report.Cause = "host_runtime"
		case 2:
			report.Cause = "agent_says_safe"
		case 3:
			report.CheckedAt = report.CheckedAt.Add(-31 * time.Second)
		case 4:
			report.CheckedAt = report.CheckedAt.Add(31 * time.Second)
		case 5:
			w.WriteHeader(503)
			return
		case 6:
			w.Header().Set("Location", "/redirect-target")
			w.WriteHeader(307)
			return
		case 7:
			_ = json.NewEncoder(w).Encode(map[string]any{"safe": true, "cause": "isolated_cluster", "checked_at": time.Now().UTC(), "override": true})
			return
		case 8:
			_, _ = w.Write([]byte(`{`))
			return
		}
		_ = json.NewEncoder(w).Encode(report)
	}))
	defer runner.Close()
	deps := api.Deps{InternalKey: "fixture", LiveDataRunnerURL: runner.URL, LiveDataRunnerClient: runner.Client()}
	assess := liveDataPosture(deps)
	if safe, e := assess(t.Context()); e != nil || !safe {
		t.Fatal("valid isolated Runner not accepted", e)
	}
	mode.Store(1)
	if safe, e := assess(t.Context()); e != nil || safe || calls.Load() != 2 {
		t.Fatal("privileged runtime hidden by a positive cache", e)
	}
	for _, bad := range []int64{2, 3, 4, 5, 6, 7, 8} {
		mode.Store(bad)
		before := calls.Load()
		if safe, e := assess(t.Context()); e == nil || safe {
			t.Fatal("uncertain posture accepted", bad)
		}
		if calls.Load() != before+1 {
			t.Fatal("posture request followed a redirect", bad)
		}
	}
	deps.LiveDataDesktop = true
	before := calls.Load()
	if safe, e := liveDataPosture(deps)(t.Context()); e != nil || safe || calls.Load() != before {
		t.Fatal("desktop self-attestation bypassed machine consent", e)
	}
	if safe, e := liveDataPosture(api.Deps{})(t.Context()); e == nil || safe {
		t.Fatal("missing dedicated transport attested safe")
	}
}
