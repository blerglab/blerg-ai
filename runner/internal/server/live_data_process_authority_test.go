package server

import (
	"bytes"
	"encoding/json"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func liveProcessGoodSample(t *testing.T, h *liveProcessMatrix) {
	t.Helper()
	h.demand()
	h.runner.call(liveProcessCommand{Action: "tick"})
	if h.runner.call(liveProcessCommand{Action: "finish"}).Cause != "" {
		t.Fatal("initial scheduled call failed")
	}
	if sample := h.runner.call(liveProcessCommand{Action: "sample"}); !sample.Retained || !bytes.Contains(sample.Rows, []byte("185.25")) {
		t.Fatal("initial sample missing")
	}
	if h.runner.call(liveProcessCommand{Action: "capture queued"}).Error != "" {
		t.Fatal("queue fixture")
	}
	h.runner.call(liveProcessCommand{Action: "advance cadence"})
}
func TestLiveDataCrossProcessAuthorityLoss(t *testing.T) {
	for _, action := range []string{"disable owner", "password reset", "sign out everywhere", "connection revoked", "needs sign in", "policy off", "policy propose", "binding deleted", "no machine consent"} {
		t.Run(action, func(t *testing.T) {
			h := newLiveProcessMatrix(t)
			r := h.prepare()
			liveProcessGoodSample(t, h)
			attempt := h.attempt()
			before := h.market.call(liveProcessCommand{Action: "counts"})
			if h.core.call(liveProcessCommand{Action: action, Revision: r}).Error != "" {
				t.Fatal("authority transition refused")
			}
			result := h.execute(attempt)
			if result.Cause != "authority_revoked" || len(result.Rows) > 0 && string(result.Rows) != "null" {
				t.Fatalf("durable authority loss dispatched or returned rows: %s", result.Cause)
			}
			after := h.market.call(liveProcessCommand{Action: "counts"})
			if after.Lists != before.Lists || after.Calls != before.Calls {
				t.Fatal("revoked authority reached provider")
			}
			if h.runner.call(liveProcessCommand{Action: "mint"}).Error == "" {
				t.Fatal("revoked grant minted")
			}
			queued := h.runner.call(liveProcessCommand{Action: "queued", Mode: "owner"})
			var frame protocol.DatasetDelta
			if !queued.Available || json.Unmarshal(queued.Rows, &frame) != nil || len(frame.Upserts) > 0 || strings.Contains(string(queued.Rows), "185.25") {
				t.Fatal("revoked queued frame retained rows")
			}
			if sample := h.runner.call(liveProcessCommand{Action: "sample"}); sample.Enabled || sample.Retained {
				t.Fatal("authority loss did not durably purge local sample")
			}
		})
	}
}
func TestLiveDataCrossProcessCapabilityAndClock(t *testing.T) {
	h := newLiveProcessMatrix(t)
	for _, mode := range []string{"forged", "expired", "skew", "replayed attempt"} {
		t.Run(mode, func(t *testing.T) {
			h.t = t
			h.prepare()
			a := h.attempt()
			before := h.market.call(liveProcessCommand{Action: "counts"})
			want := "credential_invalid"
			switch mode {
			case "forged":
				parts := strings.Split(a.Token, ".")
				parts[2] = strings.Repeat("A", len(parts[2]))
				a.Token = strings.Join(parts, ".")
			case "expired":
				h.runner.call(liveProcessCommand{Action: "clock offset", Mode: "expired"})
			case "skew":
				h.runner.call(liveProcessCommand{Action: "clock offset", Mode: "skew"})
				want = "clock_skew"
			case "replayed attempt":
				if first := h.execute(a); first.Cause != "" {
					t.Fatal("first use failed")
				}
				before = h.market.call(liveProcessCommand{Action: "counts"})
				want = "tool_listing_unavailable" // Core refuses the consumed listing receipt.
			}
			result := h.execute(a)
			h.runner.call(liveProcessCommand{Action: "clock offset", Mode: "reset"})
			if result.Cause != want || len(result.Rows) > 0 && string(result.Rows) != "null" {
				t.Fatalf("invalid capability accepted or wrong refusal: %s", result.Cause)
			}
			after := h.market.call(liveProcessCommand{Action: "counts"})
			if before.Calls != after.Calls || before.Lists != after.Lists {
				t.Fatal("invalid credential or replay reached provider")
			}
		})
	}
}
func TestLiveDataCrossProcessCoreOutage(t *testing.T) {
	h := newLiveProcessMatrix(t)
	h.prepare()
	liveProcessGoodSample(t, h)
	a := h.attempt()
	before := h.market.call(liveProcessCommand{Action: "counts"})
	h.core.stop()
	if result := h.execute(a); result.Cause != "keys_unavailable" {
		t.Fatalf("Core outage reused cached capability: %s", result.Cause)
	}
	if scope := h.runner.call(liveProcessCommand{Action: "scope", Mode: "owner"}); scope.Available || scope.Cause != "authority_unavailable" {
		t.Fatal("Core outage served cached positive authority")
	}
	after := h.market.call(liveProcessCommand{Action: "counts"})
	if before.Calls != after.Calls || before.Lists != after.Lists {
		t.Fatal("Core outage reached provider")
	}
	if ready := h.restartCore(); !ready.Available {
		t.Fatal("Core recovery")
	}
	if result := h.execute(a); result.Cause != "authority_revoked" {
		t.Fatal("Core restart retained pre-outage approval", result.Cause)
	}
	h.prepare()
	if result := h.execute(h.attempt()); result.Cause != "" {
		t.Fatal("new approval did not recover Core outage", result.Cause)
	}
}

func TestLiveDataCrossProcessAuditFailures(t *testing.T) {
	for _, which := range []string{"Core", "Runner"} {
		t.Run(which, func(t *testing.T) {
			h := newLiveProcessMatrix(t)
			h.prepare()
			liveProcessGoodSample(t, h)
			process := h.core
			want := "authority_unavailable"
			if which == "Runner" {
				process = h.runner
				want = "attempt_store_unavailable"
			}
			before := h.market.call(liveProcessCommand{Action: "counts"})
			if process.call(liveProcessCommand{Action: "audit unavailable"}).Error != "" {
				t.Fatal("audit failure fixture")
			}
			h.demand()
			h.runner.call(liveProcessCommand{Action: "tick"})
			h.runner.call(liveProcessCommand{Action: "finish"})
			sample := h.runner.call(liveProcessCommand{Action: "sample"})
			if sample.Cause != want || !sample.Retained {
				t.Fatalf("audit failure did not pause with old authorized data: %s", sample.Cause)
			}
			after := h.market.call(liveProcessCommand{Action: "counts"})
			if before.Lists != after.Lists || before.Calls != after.Calls {
				t.Fatal("audit failure dispatched without durable audit")
			}
			if process.call(liveProcessCommand{Action: "audit restored"}).Error != "" {
				t.Fatal("audit fixture recovery")
			}
			h.runner.call(liveProcessCommand{Action: "advance cadence"})
			h.runner.call(liveProcessCommand{Action: "tick"})
			h.runner.call(liveProcessCommand{Action: "finish"})
			if sample = h.runner.call(liveProcessCommand{Action: "sample"}); sample.Cause != "" || !sample.Retained {
				t.Fatal("recovered audit could not schedule fresh attempt")
			}
		})
	}
}
func TestLiveDataCrossProcessGatewayOutageAndRestart(t *testing.T) {
	h := newLiveProcessMatrix(t)
	r := h.prepare()
	a := h.attempt()
	identity, e := livetls.Load(filepath.Join(h.config.Root, "tls", "runner"), "runner")
	if e != nil {
		t.Fatal("dedicated process identity")
	}
	client := &ld.ServiceClient{URL: "https://blerg-core-live-data:8443", HTTP: liveProcessClient(identity, h.config)}
	before := h.core.call(liveProcessCommand{Action: "audit counts"})
	marketBefore := h.market.call(liveProcessCommand{Action: "counts"})
	h.runner.stop()
	if _, err := client.ProviderCredential(t.Context(), ld.ProviderRequest{Token: a.Token, Attempt: a.Attempt, Stage: "list"}); err == nil {
		t.Fatal("gateway outage released a fallback credential")
	}
	after := h.core.call(liveProcessCommand{Action: "audit counts"})
	marketAfter := h.market.call(liveProcessCommand{Action: "counts"})
	if after.SecretAudits != before.SecretAudits || marketAfter.Calls != marketBefore.Calls || marketAfter.Lists != marketBefore.Lists {
		t.Fatal("gateway outage used fallback provider path")
	}
	listener, e := net.Listen("tcp", h.config.RunnerAddress)
	if e != nil {
		t.Fatal("Runner recovery listener")
	}
	h.runner = startLiveProcess(t, os.Args[0], "TestLiveDataRunnerProcessFixture", h.path, listener, false)
	h.processes = append(h.processes, h.runner)
	h.ready = h.runner.read()
	if !h.ready.Ready || h.runner.call(liveProcessCommand{Action: "load existing", Revision: r}).Error != "" {
		t.Fatal("Runner recovery")
	}
	if result := h.execute(a); result.Cause != "" {
		t.Fatalf("unused committed attempt could not recover: %s", result.Cause)
	}
	if result := h.execute(a); result.Cause == "" {
		t.Fatal("restarted gateway replayed consumed attempt")
	}
}

func TestLiveDataCrossProcessSignedCapabilityVariants(t *testing.T) {
	h := newLiveProcessMatrix(t)
	for _, mode := range []string{"valid", "expired", "wrong type", "wrong algorithm", "unknown key", "wrong audience", "wrong owner", "wrong installation", "wrong runner"} {
		t.Run(mode, func(t *testing.T) {
			h.t = t
			r := h.prepare()
			a := h.attempt()
			reply := h.core.call(liveProcessCommand{Action: "capability", Mode: mode, Revision: r})
			if reply.Token == "" {
				t.Fatal("signed capability fixture")
			}
			h.secrets = append(h.secrets, reply.Token)
			a.Token = reply.Token
			before := h.market.call(liveProcessCommand{Action: "counts"})
			result := h.execute(a)
			if mode == "valid" {
				if result.Cause != "" || !bytes.Contains(result.Rows, []byte("185.25")) {
					t.Fatal("valid signed fixture capability failed")
				}
				return
			}
			if result.Cause == "" || len(result.Rows) > 0 && string(result.Rows) != "null" {
				t.Fatal("invalid signed capability returned rows")
			}
			after := h.market.call(liveProcessCommand{Action: "counts"})
			if before.Calls != after.Calls || before.Lists != after.Lists {
				t.Fatal("invalid signed capability reached provider")
			}
		})
	}
}
func TestLiveDataCrossProcessCompromisedKeyFence(t *testing.T) {
	h := newLiveProcessMatrix(t)
	r := h.prepare()
	a := h.attempt()
	if h.core.call(liveProcessCommand{Action: "fence"}).Error != "" {
		t.Fatal("operator key compromise fence")
	}
	if ready := h.restartCore(); !ready.Available {
		t.Fatal("key fence recovery")
	}
	before := h.market.call(liveProcessCommand{Action: "counts"})
	if result := h.execute(a); result.Cause != "credential_invalid" {
		t.Fatalf("compromised signing key remained trusted: %s", result.Cause)
	}
	if h.runner.call(liveProcessCommand{Action: "mint"}).Error == "" {
		t.Fatal("key fence resurrected old approval")
	}
	if after := h.market.call(liveProcessCommand{Action: "counts"}); after.Calls != before.Calls || after.Lists != before.Lists {
		t.Fatal("retired signing key reached provider")
	}
	next := h.prepare()
	if next.Scope.AuthorityEpoch <= r.Scope.AuthorityEpoch {
		t.Fatal("compromise did not advance authority epoch")
	}
	if result := h.execute(h.attempt()); result.Cause != "" {
		t.Fatal("new approval did not recover key compromise")
	}
}
