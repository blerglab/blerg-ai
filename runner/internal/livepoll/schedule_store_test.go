package livepoll

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func scheduleFixture(t *testing.T) (Attempts, ld.Scope, string) {
	t.Helper()
	a, s := attemptFixture(t)
	workspace := "00000000-0000-4000-8000-000000000071"
	daemon := "00000000-0000-4000-8000-000000000072"
	if err := db.UpsertDaemon(t.Context(), a.Pool, daemon, "fixture", "local", "/fixture"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(t.Context(), a.Pool, workspace, daemon, "idle", "/fixture", "fixture", "Fixture", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET workspace_id=$1,expires_at=clock_timestamp()+interval '1 hour',lease_until=clock_timestamp(),next_due_at=clock_timestamp()-interval '1 day'`, workspace); err != nil {
		t.Fatal(err)
	}
	return a, s, workspace
}
func TestScheduleReplicaLeaseAndNoCatchUp(t *testing.T) {
	a, s, _ := scheduleFixture(t)
	var wg sync.WaitGroup
	results := make(chan []Lease, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := a.Claim(t.Context())
			if err != nil {
				t.Error(err)
			}
			results <- l
		}()
	}
	wg.Wait()
	close(results)
	var all []Lease
	for leases := range results {
		all = append(all, leases...)
	}
	if len(all) != 1 || all[0].Scope != s || all[0].Fence != 2 {
		t.Fatalf("claims=%v", all)
	}
	if err := a.ObserveEpoch(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	id, err := a.Create(t.Context(), s, all[0].Fence)
	if err != nil {
		t.Fatal(err)
	}
	all[0].Attempt = id
	if err = a.Consume(t.Context(), id, s, 60); err != nil {
		t.Fatal(err)
	}
	if err = a.Finish(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	if err := a.Complete(t.Context(), all[0], json.RawMessage(`[{"key":"quote","value":7}]`), "", time.Minute); err != nil {
		t.Fatal(err)
	}
	leases, err := a.Claim(t.Context())
	if err != nil || len(leases) != 0 {
		t.Fatal("catch-up burst", err)
	}
	var rows []byte
	var sequence int64
	if err = a.Pool.QueryRow(t.Context(), `SELECT last_payload,sample_sequence FROM live_mcp_binding_state`).Scan(&rows, &sequence); err != nil || sequence != 1 || len(rows) == 0 {
		t.Fatal("sample missing", err)
	}
	// Reclaimed leases cannot publish a late result.
	if _, err = a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET fence=fence+1,lease_until=clock_timestamp()+interval '30 seconds'`); err != nil {
		t.Fatal(err)
	}
	if a.Complete(t.Context(), all[0], json.RawMessage(`[]`), "", time.Minute) == nil {
		t.Fatal("late lease wrote payload")
	}
}
func TestScheduleDemandAndRevocationPurge(t *testing.T) {
	a, s, workspace := scheduleFixture(t)
	if a.Demand(t.Context(), "other-owner", workspace, s.Binding) == nil || a.Demand(t.Context(), s.Owner, "other-workspace", s.Binding) == nil {
		t.Fatal("foreign viewer created demand")
	}
	if err := a.Demand(t.Context(), s.Owner, workspace, s.Binding); err != nil {
		t.Fatal(err)
	}
	leases, err := a.Claim(t.Context())
	if err != nil || len(leases) != 1 {
		t.Fatal(err)
	}
	if _, err = a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET last_payload='[{"key":"quote","value":7}]',payload_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	if err = a.Complete(t.Context(), leases[0], nil, "authority_revoked", time.Minute); err != nil {
		t.Fatal(err)
	}
	var enabled, retained bool
	if err = a.Pool.QueryRow(t.Context(), `SELECT enabled,last_payload IS NOT NULL FROM live_mcp_binding_state`).Scan(&enabled, &retained); err != nil || enabled || retained {
		t.Fatal("revocation retained rows", err)
	}
}
func TestScheduleCredentialStageSingleUse(t *testing.T) {
	a, s := attemptFixture(t)
	if err := a.ObserveEpoch(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	id, err := a.Create(t.Context(), s, 1)
	if err != nil {
		t.Fatal(err)
	}
	r := ld.AttemptReceipt{Scope: s, Attempt: id, Stage: "call"}
	if a.CredentialReceipt(t.Context(), r) == nil {
		t.Fatal("credential released before audit consumed")
	}
	r.Stage = "list"
	if err = a.CredentialReceipt(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if a.CredentialReceipt(t.Context(), r) == nil {
		t.Fatal("listing receipt replayed")
	}
	if err = a.Consume(t.Context(), id, s, 60); err != nil {
		t.Fatal(err)
	}
	r.Stage = "call"
	if err = a.CredentialReceipt(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if a.CredentialReceipt(t.Context(), r) == nil {
		t.Fatal("call receipt replayed")
	}
}

func TestScheduleRefusalCompletesAuditWithoutDispatch(t *testing.T) {
	a, s, _ := scheduleFixture(t)
	leases, err := a.Claim(t.Context())
	if err != nil || len(leases) != 1 {
		t.Fatal(err)
	}
	l := leases[0]
	l.Attempt, err = a.Create(t.Context(), s, l.Fence)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Complete(t.Context(), l, nil, "authority_unavailable", time.Minute); err != nil {
		t.Fatal(err)
	}
	var outcome, cause string
	var dispatched, finished bool
	if err = a.Pool.QueryRow(t.Context(), `SELECT outcome,refusal_cause,dispatched_at IS NOT NULL,finished_at IS NOT NULL FROM live_mcp_attempts WHERE id=$1`, l.Attempt).Scan(&outcome, &cause, &dispatched, &finished); err != nil || outcome != "refused" || cause != "authority_unavailable" || dispatched || !finished {
		t.Fatal("refusal audit missing", err, outcome, cause)
	}
}

func TestScheduleRowsRequireConsumedSuccessfulAttempt(t *testing.T) {
	a, s, _ := scheduleFixture(t)
	leases, err := a.Claim(t.Context())
	if err != nil || len(leases) != 1 {
		t.Fatal(err)
	}
	l := leases[0]
	l.Attempt, err = a.Create(t.Context(), s, l.Fence)
	if err != nil {
		t.Fatal(err)
	}
	if a.Complete(t.Context(), l, json.RawMessage(`[{"key":"not-dispatched","value":7}]`), "", time.Minute) == nil {
		t.Fatal("stored sample without consumed successful audit")
	}
	var retained bool
	if err = a.Pool.QueryRow(t.Context(), `SELECT last_payload IS NOT NULL FROM live_mcp_binding_state`).Scan(&retained); err != nil || retained {
		t.Fatal("unaudited sample retained", err)
	}
}

func TestScheduleWorkspaceDeletionRetainsAudit(t *testing.T) {
	a, s, workspace := scheduleFixture(t)
	leases, err := a.Claim(t.Context())
	if err != nil || len(leases) != 1 {
		t.Fatal(err)
	}
	if _, err = a.Create(t.Context(), s, leases[0].Fence); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Pool.Exec(t.Context(), `DELETE FROM sessions WHERE id=$1`, workspace); err != nil {
		t.Fatal("live binding blocked workspace deletion", err)
	}
	var bindings, attempts int
	if err = a.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM live_mcp_binding_state),(SELECT count(*) FROM live_mcp_attempts)`).Scan(&bindings, &attempts); err != nil || bindings != 0 || attempts != 1 {
		t.Fatal("deletion lost audit or retained payload", err)
	}
}

func TestScheduleActivateResetsViewOnlyForNewRevision(t *testing.T) {
	a, s, workspace := scheduleFixture(t)
	if _, e := a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET last_view_at=clock_timestamp()-interval '8 days',enabled=false`); e != nil {
		t.Fatal(e)
	}
	s.Revision = 2
	approved, e := approvedFixture(s, time.Now()).ApprovedRevision()
	if e != nil {
		t.Fatal(e)
	}
	var before time.Time
	if e = a.Pool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = a.Activate(t.Context(), approved, workspace); e != nil {
		t.Fatal(e)
	}
	if e = a.Maintain(t.Context()); e != nil {
		t.Fatal(e)
	}
	var viewed time.Time
	var enabled bool
	if e = a.Pool.QueryRow(t.Context(), `SELECT last_view_at,enabled FROM live_mcp_binding_state`).Scan(&viewed, &enabled); e != nil || viewed.Before(before) || !enabled {
		t.Fatal("new approval immediately auto-paused on an old view timestamp", e, enabled)
	}
	if _, e = a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET last_view_at=clock_timestamp()-interval '2 days'`); e != nil {
		t.Fatal(e)
	}
	if e = a.Pool.QueryRow(t.Context(), `SELECT last_view_at FROM live_mcp_binding_state`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = a.Activate(t.Context(), approved, workspace); e != nil {
		t.Fatal(e)
	}
	if e = a.Pool.QueryRow(t.Context(), `SELECT last_view_at FROM live_mcp_binding_state`).Scan(&viewed); e != nil || !viewed.Equal(before) {
		t.Fatal("receipt replay refreshed demand", e)
	}
}
