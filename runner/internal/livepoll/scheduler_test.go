package livepoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/contracts/livemcp"
	"strings"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

type scheduleFake struct {
	leases []Lease
	order  []string
	rows   json.RawMessage
	cause  string
	delay  time.Duration
}

func (f *scheduleFake) Claim(context.Context) ([]Lease, error) {
	f.order = append(f.order, "claim")
	return f.leases, nil
}
func (f *scheduleFake) Create(context.Context, ld.Scope, int64) (string, error) {
	f.order = append(f.order, "commit")
	return "attempt", nil
}
func (f *scheduleFake) Complete(_ context.Context, _ Lease, r json.RawMessage, c string, d time.Duration) error {
	f.order = append(f.order, "complete")
	f.rows = r
	f.cause = c
	f.delay = d
	return nil
}
func (f *scheduleFake) Reconcile(context.Context) error {
	f.order = append(f.order, "reconcile")
	return nil
}
func (f *scheduleFake) Maintain(context.Context) error { return nil }

type mintFake struct {
	f   *scheduleFake
	err error
}

func (m mintFake) Mint(context.Context, string, int64, bool) (string, error) {
	m.f.order = append(m.f.order, "mint")
	return "opaque", m.err
}

type executionFake struct {
	f   *scheduleFake
	err error
}

func (e executionFake) Execute(context.Context, string, string) (json.RawMessage, error) {
	e.f.order = append(e.f.order, "call")
	return json.RawMessage(`[{"key":"a","value":7}]`), e.err
}
func TestSchedulerDisabledDoesNoIO(t *testing.T) {
	s := Scheduler{}
	if FailureCause(s.Tick(t.Context())) != "polling_disabled" {
		t.Fatal("disabled scheduler did work")
	}
}
func TestSchedulerCommittedAttemptBeforeMintAndCall(t *testing.T) {
	f := &scheduleFake{leases: []Lease{{Scope: scope(), Fence: 1, Interval: 60}}}
	s := Scheduler{Enabled: true, Store: f, Minter: mintFake{f: f}, Executor: executionFake{f: f}}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.order)
	if string(raw) != `["reconcile","claim","commit","mint","call","complete"]` {
		t.Fatal(string(raw))
	}
	if string(f.rows) != `[{"key":"a","value":7}]` || f.cause != "" || f.delay < 60*time.Second {
		t.Fatal("sample or cadence lost")
	}
}
func TestSchedulerAuthorityLossAndTransientOutage(t *testing.T) {
	for _, test := range []struct {
		err   error
		cause string
		pause bool
	}{{ld.ErrDenied, "authority_revoked", true}, {ld.ErrAuthorityUnavailable, "authority_unavailable", false}} {
		f := &scheduleFake{leases: []Lease{{Scope: scope(), Fence: 1, Interval: 60}}}
		s := Scheduler{Enabled: true, Store: f, Minter: mintFake{f: f, err: test.err}, Executor: executionFake{f: f}}
		if err := s.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if f.rows != nil || f.cause != test.cause || terminal(f.cause) != test.pause {
			t.Fatal("wrong failure rule")
		}
		for _, op := range f.order {
			if op == "call" {
				t.Fatal("called provider after mint refusal")
			}
		}
	}
	if !terminal("mapping_failed") || PurgesSample("mapping_failed") || !PurgesSample("tool_changed") {
		t.Fatal("last-good retention differs from authority purge")
	}
}
func TestSchedulerBoundedBackoffAndCancellation(t *testing.T) {
	if retryDelay(60, 0) != time.Minute || retryDelay(15, 30) != 15*time.Minute || retryDelay(86400, 30) != 24*time.Hour {
		t.Fatal("backoff violated interval or cap")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s := Scheduler{Enabled: true}
	if !errors.Is(s.Run(ctx), context.Canceled) {
		t.Fatal("run did not stop")
	}
}

func TestSchedulerDatasetBudgetMatchesSocket(t *testing.T) {
	if SampleCause("Bearer canary_diagnostic_value") != "source_unavailable" {
		t.Fatal("stored diagnostic text became a public label")
	}
	if !datasetRowsFit([]livemcp.Row{{"key": "quote", "value": 185.25}}) {
		t.Fatal("bounded scalar refused")
	}
	for _, row := range []livemcp.Row{{"key": strings.Repeat("k", 129)}, {"key": "quote", "value": strings.Repeat("x", 257)}} {
		if datasetRowsFit([]livemcp.Row{row}) {
			t.Fatal("socket-ineligible sample accepted")
		}
	}
	row := livemcp.Row{"key": "quote"}
	for i := range 16 {
		row[fmt.Sprint(i)] = i
	}
	if datasetRowsFit([]livemcp.Row{row}) {
		t.Fatal("too many columns accepted")
	}
}

type canceledToolExecution struct{ cancel context.CancelFunc }

func (e canceledToolExecution) Execute(context.Context, string, string) (json.RawMessage, error) {
	e.cancel()
	return nil, refuse("tool_changed")
}
func TestSchedulerCancellationCannotEraseToolRevocation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := &scheduleFake{leases: []Lease{{Scope: scope(), Fence: 1, Interval: 15}}}
	scheduler := &Scheduler{Enabled: true, Store: f, Minter: mintFake{f: f}, Executor: canceledToolExecution{cancel: cancel}}
	if e := scheduler.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if f.cause != "tool_changed" || !terminal(f.cause) || !PurgesSample(f.cause) {
		t.Fatal("cancellation converted known tool revocation to a retry")
	}
	for _, cause := range []string{"grant_revoke_failed", "attempt_revoke_failed"} {
		if !terminal(cause) || !PurgesSample(cause) {
			t.Fatal("failed revocation left a retry path")
		}
	}
}

func TestSchedulerProviderDelayCannotBeShortenedByJitter(t *testing.T) {
	f := &scheduleFake{leases: []Lease{{Scope: scope(), Fence: 1, Interval: 60, Failures: 4}}}
	s := Scheduler{Enabled: true, Store: f, Minter: mintFake{f: f}, Executor: executionFake{f: f, err: &Failure{cause: "provider_rate_limited", retryAfter: time.Hour}}}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.delay < time.Hour || f.cause != "provider_rate_limited" {
		t.Fatal("provider Retry-After shortened by local backoff or jitter")
	}
	if retryDelay(60, 1) != 120*time.Second || retryDelay(60, 10) != 15*time.Minute {
		t.Fatal("backoff did not start from effective interval")
	}
}
