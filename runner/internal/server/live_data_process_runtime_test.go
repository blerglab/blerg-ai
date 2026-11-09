package server

import (
	"bytes"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLiveDataCrossProcessSharedQuotas(t *testing.T) {
	for _, mode := range []string{"connection rate", "owner rate", "connection concurrency", "owner concurrency", "global concurrency", "queue", "bindings", "expired bindings"} {
		t.Run(mode, func(t *testing.T) {
			h := newLiveProcessMatrix(t)
			h.prepare()
			h.demand()
			if h.runner.call(liveProcessCommand{Action: "quota", Mode: mode}).Error != "" {
				t.Fatal("durable quota fixture")
			}
			before := h.market.call(liveProcessCommand{Action: "counts"})
			h.runner.call(liveProcessCommand{Action: "tick"})
			h.runner.call(liveProcessCommand{Action: "finish"})
			sample := h.runner.call(liveProcessCommand{Action: "sample"})
			calls := h.market.call(liveProcessCommand{Action: "counts"}).Calls - before.Calls
			if mode == "expired bindings" {
				if calls != 1 || sample.Cause != "" {
					t.Fatal("expired bindings consumed active polling slots")
				}
			} else if calls != 0 || sample.Cause == "" {
				t.Fatal("shared quota bypassed at dispatch")
			}
			h.runner.call(liveProcessCommand{Action: "tick"})
			h.runner.call(liveProcessCommand{Action: "finish"})
			if h.market.call(liveProcessCommand{Action: "counts"}).Calls-before.Calls != calls {
				t.Fatal("overload spun or replayed a call")
			}
		})
	}
}
func TestLiveDataCrossProcessExpiredApproval(t *testing.T) {
	for _, mode := range []string{"subscriber purge", "sweeper purge"} {
		t.Run(mode, func(t *testing.T) {
			h := newLiveProcessMatrix(t)
			r := h.prepare()
			r.Scope.Binding = newUUID()
			r.Scope.Digest = ""
			expired := h.core.call(liveProcessCommand{Action: "prepare expired", Revision: r})
			if expired.Error != "" || !expired.Revision.ExpiresAt.Before(time.Now()) {
				t.Fatal("expired approval fixture")
			}
			if h.runner.call(liveProcessCommand{Action: "mint revision", Revision: expired.Revision}).Error == "" {
				t.Fatal("expired Core approval minted")
			}
			if h.runner.call(liveProcessCommand{Action: "seed expired", Revision: expired.Revision}).Error != "" {
				t.Fatal("expired Runner state fixture")
			}
			h.runner.call(liveProcessCommand{Action: "capture queued"})
			h.core.stop()
			if mode == "sweeper purge" {
				if h.runner.call(liveProcessCommand{Action: "maintain"}).Error != "" {
					t.Fatal("expired approval maintenance")
				}
			} else {
				q := h.runner.call(liveProcessCommand{Action: "queued", Mode: "owner"})
				var frame protocol.DatasetDelta
				if !q.Available || json.Unmarshal(q.Rows, &frame) != nil || len(frame.Upserts) > 0 {
					t.Fatal("expired approval delivered queued payload")
				}
			}
			if sample := h.runner.call(liveProcessCommand{Action: "sample"}); sample.Retained || sample.Enabled {
				t.Fatal("expired approval kept durable payload or eligibility")
			}
		})
	}
}
func TestLiveDataCrossProcessSessionListenersRefuseCapabilities(t *testing.T) {
	h := newLiveProcessMatrix(t)
	h.prepare()
	a := h.attempt()
	before := h.market.call(liveProcessCommand{Action: "counts"})
	client := &http.Client{Timeout: 5 * time.Second}
	for _, base := range []string{h.ready.BrowserURL, h.ready.GatewayURL} {
		for _, path := range []string{"/mcp-gw/simulated-market", "/internal/live-data/execute", "/internal/live-data/attempt"} {
			request, e := http.NewRequestWithContext(t.Context(), "POST", base+path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"quotes","arguments":{"symbol":"NVDA"}}}`))
			if e != nil {
				t.Fatal("probe request")
			}
			request.Header.Set("Authorization", "Bearer "+a.Token)
			request.Header.Set("X-Internal-Key", "process-internal-canary")
			response, e := client.Do(request)
			if e != nil {
				t.Fatal("probe transport")
			}
			body, e := io.ReadAll(io.LimitReader(response.Body, 8193))
			_ = response.Body.Close()
			if e != nil || len(body) > 8192 || bytes.Contains(body, []byte(a.Token)) || response.StatusCode != 401 && response.StatusCode != 404 {
				t.Fatal("session listener admitted live-data capability")
			}
		}
	}
	after := h.market.call(liveProcessCommand{Action: "counts"})
	if before.Lists != after.Lists || before.Calls != after.Calls {
		t.Fatal("public or sandbox probe reached provider")
	}
}
func TestLiveDataCrossProcessSecretAuditFailure(t *testing.T) {
	h := newLiveProcessMatrix(t)
	h.prepare()
	liveProcessGoodSample(t, h)
	before := h.market.call(liveProcessCommand{Action: "counts"})
	if h.core.call(liveProcessCommand{Action: "secret audit unavailable"}).Error != "" {
		t.Fatal("secret audit fixture")
	}
	h.demand()
	h.runner.call(liveProcessCommand{Action: "tick"})
	h.runner.call(liveProcessCommand{Action: "finish"})
	after := h.market.call(liveProcessCommand{Action: "counts"})
	sample := h.runner.call(liveProcessCommand{Action: "sample"})
	if after.Lists != before.Lists || after.Calls != before.Calls || sample.Cause != "tool_listing_unavailable" || !sample.Retained {
		t.Fatal("secret audit failure released provider access or replaced good data")
	}
	if h.core.call(liveProcessCommand{Action: "secret audit restored"}).Error != "" {
		t.Fatal("secret audit recovery")
	}
	h.runner.call(liveProcessCommand{Action: "advance cadence"})
	h.runner.call(liveProcessCommand{Action: "tick"})
	h.runner.call(liveProcessCommand{Action: "finish"})
	if h.runner.call(liveProcessCommand{Action: "sample"}).Cause != "" {
		t.Fatal("secret audit recovery did not use fresh attempt")
	}
}

func TestLiveDataCrossProcessCrashKeepsAttemptConsumed(t *testing.T) {
	h := newLiveProcessMatrix(t)
	r := h.prepare()
	a := h.attempt()
	h.market.call(liveProcessCommand{Action: "mode", Mode: "hold"})
	h.runner.call(liveProcessCommand{Action: "start", Token: a.Token, Attempt: a.Attempt})
	if h.market.call(liveProcessCommand{Action: "await call"}).Error != "" {
		t.Fatal("provider call did not start")
	}
	h.runner.crash()
	h.market.call(liveProcessCommand{Action: "mode", Mode: "good"})
	h.restartRunner(r)
	if h.runner.call(liveProcessCommand{Action: "age interrupted"}).Error != "" {
		t.Fatal("interrupted audit clock")
	}
	if h.runner.call(liveProcessCommand{Action: "reconcile"}).Error != "" {
		t.Fatal("interrupted audit reconciliation")
	}
	if h.runner.call(liveProcessCommand{Action: "attempt counts"}).Interrupted != 1 {
		t.Fatal("consumed crashed attempt was not durably interrupted")
	}
	before := h.market.call(liveProcessCommand{Action: "counts"})
	if result := h.execute(a); result.Cause == "" {
		t.Fatal("crashed attempt became reusable")
	}
	if after := h.market.call(liveProcessCommand{Action: "counts"}); after.Calls != before.Calls || after.Lists != before.Lists {
		t.Fatal("replayed crashed attempt reached provider")
	}
	h.demand()
	h.runner.call(liveProcessCommand{Action: "due"})
	h.runner.call(liveProcessCommand{Action: "tick"})
	h.runner.call(liveProcessCommand{Action: "finish"})
	if sample := h.runner.call(liveProcessCommand{Action: "sample"}); sample.Cause != "" || !sample.Retained {
		t.Fatal("fresh audited attempt did not recover crash")
	}
	if h.market.call(liveProcessCommand{Action: "counts"}).Calls != before.Calls+1 {
		t.Fatal("restart caused a catch-up burst")
	}
}
func TestLiveDataCrossProcessCallDeadline(t *testing.T) {
	h := newLiveProcessMatrix(t)
	h.prepare()
	liveProcessGoodSample(t, h)
	before := h.runner.call(liveProcessCommand{Action: "sample"})
	h.market.call(liveProcessCommand{Action: "mode", Mode: "hold"})
	h.demand()
	started := time.Now()
	h.runner.call(liveProcessCommand{Action: "tick"})
	if h.market.call(liveProcessCommand{Action: "await call"}).Error != "" {
		t.Fatal("deadline proof never reached provider")
	}
	h.runner.call(liveProcessCommand{Action: "finish"})
	after := h.runner.call(liveProcessCommand{Action: "sample"})
	if time.Since(started) > 35*time.Second || after.Cause != "call_canceled" || !bytes.Equal(before.Rows, after.Rows) || before.PayloadAt != after.PayloadAt {
		t.Fatal("call deadline exceeded bound or replaced good sample")
	}
}

func TestLiveDataCrossProcessUnusedAttemptExpires(t *testing.T) {
	h := newLiveProcessMatrix(t)
	h.prepare()
	a := h.attempt()
	if h.runner.call(liveProcessCommand{Action: "lifecycle", Mode: "attempt expired"}).Error != "" {
		t.Fatal("unused attempt expiry fixture")
	}
	if h.runner.call(liveProcessCommand{Action: "reconcile"}).Error != "" {
		t.Fatal("unused attempt reconciliation")
	}
	if h.runner.call(liveProcessCommand{Action: "attempt counts"}).Pending != 0 {
		t.Fatal("expired unused attempt remained pending")
	}
	before := h.market.call(liveProcessCommand{Action: "counts"})
	if result := h.execute(a); result.Cause == "" {
		t.Fatal("expired unused attempt replayed")
	}
	if after := h.market.call(liveProcessCommand{Action: "counts"}); after.Calls != before.Calls || after.Lists != before.Lists {
		t.Fatal("expired unused attempt reached provider")
	}
}

func TestLiveDataCrossProcessPersistedKeyReplacement(t *testing.T) {
	h := newLiveProcessMatrix(t)
	h.prepare()
	a := h.attempt()
	if h.core.call(liveProcessCommand{Action: "rotate state key"}).Error != "" {
		t.Fatal("operator key replacement")
	}
	before := h.market.call(liveProcessCommand{Action: "counts"})
	if result := h.execute(a); result.Cause != "keys_unavailable" {
		t.Fatal("old process ignored persisted key replacement")
	}
	if ready := h.restartCore(); !ready.Available {
		t.Fatal("rotated key boot")
	}
	if result := h.execute(a); result.Cause != "credential_invalid" {
		t.Fatal("retired persisted key remained trusted")
	}
	if h.runner.call(liveProcessCommand{Action: "mint"}).Error == "" {
		t.Fatal("key replacement restart retained prior approval")
	}
	h.prepare()
	a = h.attempt()
	if ready := h.restartCore(); !ready.Available {
		t.Fatal("second rotated-key boot")
	}
	if result := h.execute(a); result.Cause != "authority_revoked" {
		t.Fatal("replacement key did not persist or startup retained approval", result.Cause)
	}
	h.prepare()
	if result := h.execute(h.attempt()); result.Cause != "" {
		t.Fatal("reapproval after key replacement could not poll", result.Cause)
	}
	if after := h.market.call(liveProcessCommand{Action: "counts"}); after.Calls != before.Calls+1 {
		t.Fatal("key replacement dispatched an old capability or lost the fresh call")
	}
}
