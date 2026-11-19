package livepoll

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
)

func TestMain(m *testing.M) {
	if os.Getenv("BLERG_LIVE_MARKET_STUB") == "1" {
		os.Exit(runMarketStub())
	}
	os.Exit(testsupport.Run(m))
}
func attemptFixture(t *testing.T) (Attempts, ld.Scope) {
	t.Helper()
	p := testsupport.Open(t, "TEST_DATABASE_URL", "livepoll", db.RunMigrations)
	s := scope()
	raw, _ := json.Marshal(s)
	if _, err := p.Exec(context.Background(), `INSERT INTO live_mcp_binding_state(binding_id,revision,scope,enabled,fence,lease_until,demand_until) VALUES($1,1,$2,true,1,clock_timestamp()+interval '1 hour',clock_timestamp()+interval '1 minute')`, s.Binding, raw); err != nil {
		t.Fatal(err)
	}
	return Attempts{p}, s
}
func TestAttemptSingleUseAndEpoch(t *testing.T) {
	a, s := attemptFixture(t)
	ctx := context.Background()
	if err := a.ObserveEpoch(ctx, s); err != nil {
		t.Fatal(err)
	}
	id, err := a.Create(ctx, s, 1)
	if err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.Consume(ctx, id, s, 60) == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("single-use winners %d", wins.Load())
	}
	if err := a.Finish(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Pool.Exec(ctx, `UPDATE live_mcp_attempts SET dispatched_at=clock_timestamp()-interval '2 minutes' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := a.Consume(ctx, id, s, 60); err == nil {
		t.Fatal("finished attempt replayed")
	}
	s.AuthorityEpoch = 2
	if err := a.ObserveEpoch(ctx, s); err != nil {
		t.Fatal(err)
	}
	s.AuthorityEpoch = 1
	// New store instance models restart: high-water state is durable.
	if err := (Attempts{a.Pool}).ObserveEpoch(ctx, s); err == nil {
		t.Fatal("epoch rollback after restart accepted")
	}
}
func TestAttemptFences(t *testing.T) {
	for name, change := range map[string]string{
		"wrong audit owner":      `UPDATE live_mcp_attempts SET owner_id='00000000-0000-4000-8000-000000000999'`,
		"wrong audit connection": `UPDATE live_mcp_attempts SET connection_id='00000000-0000-4000-8000-000000000999'`,
		"wrong binding identity": `UPDATE live_mcp_binding_state SET binding_id='00000000-0000-4000-8000-000000000999'; UPDATE live_mcp_attempts SET binding_id='00000000-0000-4000-8000-000000000999'`,
		"wrong stored revision":  `UPDATE live_mcp_binding_state SET revision=2;UPDATE live_mcp_attempts SET revision=2`,
		"no demand":              `UPDATE live_mcp_binding_state SET demand_until=clock_timestamp()-interval '1 second'`,
		"expired lease":          `UPDATE live_mcp_binding_state SET lease_until=clock_timestamp()-interval '1 second'`,
		"stale fence":            `UPDATE live_mcp_binding_state SET fence=2`,
		"paused":                 `UPDATE live_mcp_binding_state SET enabled=false`,
		"expired attempt":        `UPDATE live_mcp_attempts SET due_at=clock_timestamp()-interval '2 minutes',expires_at=clock_timestamp()-interval '1 minute'`,
	} {
		t.Run(name, func(t *testing.T) {
			a, s := attemptFixture(t)
			ctx := context.Background()
			if err := a.ObserveEpoch(ctx, s); err != nil {
				t.Fatal(err)
			}
			id, err := a.Create(ctx, s, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Pool.Exec(ctx, change); err != nil {
				t.Fatal(err)
			}
			if a.Consume(ctx, id, s, 60) == nil {
				t.Fatal("invalid attempt consumed")
			}
		})
	}
}
func TestAttemptConnectionRateLimit(t *testing.T) {
	a, s := attemptFixture(t)
	ctx := context.Background()
	if err := a.ObserveEpoch(ctx, s); err != nil {
		t.Fatal(err)
	}
	id, err := a.Create(ctx, s, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	// Past successful attempts still spend the sliding-window call allowance.
	if _, err := a.Pool.Exec(ctx, `INSERT INTO live_mcp_attempts(binding_id,revision,owner_id,connection_id,scope,fence,due_at,expires_at,dispatched_at,finished_at,outcome) SELECT gen_random_uuid(),1,$2,$3,$4,1,clock_timestamp()-interval '10 seconds',clock_timestamp()+interval '20 seconds',clock_timestamp()-interval '10 seconds',clock_timestamp()-interval '9 seconds','ok' FROM generate_series(1,30) WHERE $1::uuid IS NOT NULL`, s.Binding, s.Owner, s.Connection, raw); err != nil {
		t.Fatal(err)
	}
	if a.Consume(ctx, id, s, 60) == nil {
		t.Fatal("connection quota bypassed")
	}
}

func TestAttemptConcurrencyCaps(t *testing.T) {
	a, base := attemptFixture(t)
	ctx := context.Background()
	if err := a.ObserveEpoch(ctx, base); err != nil {
		t.Fatal(err)
	}
	admit := func(index int, owner string) error {
		s := base
		s.Owner = owner
		s.Binding = fmt.Sprintf("00000000-0000-4000-8000-%012d", 100+index)
		s.Connection = fmt.Sprintf("00000000-0000-4000-8000-%012d", 200+index)
		raw, _ := json.Marshal(s)
		if _, err := a.Pool.Exec(ctx, `INSERT INTO live_mcp_binding_state(binding_id,revision,scope,enabled,fence,lease_until,demand_until) VALUES($1,1,$2,true,1,clock_timestamp()+interval '1 hour',clock_timestamp()+interval '1 minute')`, s.Binding, raw); err != nil {
			t.Fatal(err)
		}
		id, err := a.Create(ctx, s, 1)
		if err != nil {
			t.Fatal(err)
		}
		return a.Consume(ctx, id, s, 60)
	}
	if admit(1, base.Owner) != nil || admit(2, base.Owner) != nil {
		t.Fatal("owner's first two calls refused")
	}
	if admit(3, base.Owner) == nil {
		t.Fatal("owner concurrency exceeded two")
	}
	if admit(4, "00000000-0000-4000-8000-000000000304") != nil || admit(5, "00000000-0000-4000-8000-000000000305") != nil {
		t.Fatal("installation's first four calls refused")
	}
	if admit(6, "00000000-0000-4000-8000-000000000306") == nil {
		t.Fatal("installation concurrency exceeded four")
	}
}

func TestAttemptCannotLowerApprovedInterval(t *testing.T) {
	a, s := attemptFixture(t)
	if err := a.ObserveEpoch(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	id, err := a.Create(t.Context(), s, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET interval_seconds=15`); err != nil {
		t.Fatal(err)
	}
	if err = a.Consume(t.Context(), id, s, 60); err == nil {
		t.Fatal("local interval bypassed approved interval")
	}
}

func TestLiveRetentionAndNoViewPause(t *testing.T) {
	a, s := attemptFixture(t)
	if _, err := a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET last_view_at=clock_timestamp()-interval '8 days',last_payload='[{"value":1}]',payload_at=clock_timestamp()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if err := a.Maintain(t.Context()); err != nil {
		t.Fatal(err)
	}
	var enabled, payload bool
	var fence int64
	if err := a.Pool.QueryRow(t.Context(), `SELECT enabled,last_payload IS NOT NULL,fence FROM live_mcp_binding_state WHERE binding_id=$1`, s.Binding).Scan(&enabled, &payload, &fence); err != nil || enabled || payload || fence != 2 {
		t.Fatal("seven-day pause did not fence and purge", err)
	}
	if _, err := a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET enabled=true,last_view_at=clock_timestamp(),last_payload='[{"value":1}]',payload_at=clock_timestamp()-interval '25 hours'`); err != nil {
		t.Fatal(err)
	}
	if err := a.Maintain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.Pool.QueryRow(t.Context(), `SELECT last_payload IS NOT NULL FROM live_mcp_binding_state`).Scan(&payload); err != nil || payload {
		t.Fatal("24-hour row TTL failed", err)
	}

	id, err := a.Create(t.Context(), s, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Pool.Exec(t.Context(), `UPDATE live_mcp_attempts SET created_at=clock_timestamp()-interval '181 days' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err = a.Maintain(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = a.Pool.QueryRow(t.Context(), `SELECT count(*) FROM live_mcp_attempts`).Scan(&count); err != nil || count != 0 {
		t.Fatal("180-day attempt retention failed", err)
	}
}

func TestAttemptRepeatedEpochObservationDoesNotWrite(t *testing.T) {
	a, s := attemptFixture(t)
	if e := a.ObserveEpoch(t.Context(), s); e != nil {
		t.Fatal(e)
	}
	var before, after string
	if e := a.Pool.QueryRow(t.Context(), `SELECT xmin::text FROM live_mcp_authority_epochs`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	for range 20 {
		if e := a.ObserveEpoch(t.Context(), s); e != nil {
			t.Fatal(e)
		}
	}
	if e := a.Pool.QueryRow(t.Context(), `SELECT xmin::text FROM live_mcp_authority_epochs`).Scan(&after); e != nil {
		t.Fatal(e)
	}
	if before != after {
		t.Fatal("unchanged epoch observations rewrote the durable row")
	}
}

func TestAttemptToolFenceIgnoresExpiredLeaseAndProtectsNewRevision(t *testing.T) {
	a, old := attemptFixture(t)
	current := old
	current.Revision++
	raw, _ := json.Marshal(current)
	if _, e := a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET revision=$1,scope=$2,last_payload='[{"key":"quote","value":185.25}]',payload_at=clock_timestamp(),lease_until=clock_timestamp()-interval '1 hour',demand_until=clock_timestamp()-interval '1 hour'`, current.Revision, raw); e != nil {
		t.Fatal(e)
	}
	if e := a.RevokeRevision(t.Context(), old, "tool_changed"); e != nil {
		t.Fatal(e)
	}
	var enabled, retained bool
	var cause string
	var fence, sequence int64
	read := func() {
		t.Helper()
		if e := a.Pool.QueryRow(t.Context(), `SELECT enabled,last_payload IS NOT NULL,cause,fence,sample_sequence FROM live_mcp_binding_state`).Scan(&enabled, &retained, &cause, &fence, &sequence); e != nil {
			t.Fatal(e)
		}
	}
	read()
	if !enabled || !retained || cause != "" {
		t.Fatal("stale tool observation fenced a newer approved scope")
	}
	if e := a.RevokeRevision(t.Context(), current, "tool_changed"); e != nil {
		t.Fatal(e)
	}
	read()
	if enabled || retained || cause != "tool_changed" {
		t.Fatal("expired lease/demand retained known unsafe tool data")
	}
	beforeFence, beforeSequence := fence, sequence
	if e := a.RevokeRevision(t.Context(), current, "tool_changed"); e != nil {
		t.Fatal(e)
	}
	read()
	if fence != beforeFence || sequence != beforeSequence {
		t.Fatal("repeated tool fence rewrote state")
	}
}

func TestAttemptToolFenceRetiresOnlyUndispatchedExactScope(t *testing.T) {
	a, old := attemptFixture(t)
	ctx := t.Context()
	if err := a.ObserveEpoch(ctx, old); err != nil {
		t.Fatal(err)
	}
	id, err := a.Create(ctx, old, 1)
	if err != nil {
		t.Fatal(err)
	}
	current := old
	current.Revision++
	raw, _ := json.Marshal(current)
	if _, err := a.Pool.Exec(ctx, `UPDATE live_mcp_binding_state SET revision=$1,scope=$2`, current.Revision, raw); err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeRevision(ctx, old, "tool_changed"); err != nil {
		t.Fatal(err)
	}
	var outcome, cause string
	var finished bool
	if err := a.Pool.QueryRow(ctx, `SELECT outcome,refusal_cause,finished_at IS NOT NULL FROM live_mcp_attempts WHERE id=$1`, id).Scan(&outcome, &cause, &finished); err != nil {
		t.Fatal(err)
	}
	if outcome != "refused" || cause != "tool_changed" || !finished {
		t.Fatal("unsafe revision retained an undispatched receipt")
	}
	next, err := a.Create(ctx, current, 1)
	if err != nil {
		t.Fatal("old receipt blocked new approval", err)
	}
	if err := a.RevokeRevision(ctx, old, "tool_changed"); err != nil {
		t.Fatal(err)
	}
	if err := a.Pool.QueryRow(ctx, `SELECT outcome FROM live_mcp_attempts WHERE id=$1`, next).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "undispatched" {
		t.Fatal("stale fence retired a newer approval receipt")
	}
	if err := a.Consume(ctx, next, current, 60); err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeRevision(ctx, current, "tool_changed"); err != nil {
		t.Fatal(err)
	}
	if err := a.Pool.QueryRow(ctx, `SELECT outcome FROM live_mcp_attempts WHERE id=$1`, next).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "dispatched" {
		t.Fatal("fence finalized an in-flight call before its result")
	}
}

func TestAttemptExpiredAuditIsRefusedWithoutReusingReceipt(t *testing.T) {
	a, s := attemptFixture(t)
	if err := a.ObserveEpoch(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	id, err := a.Create(t.Context(), s, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Pool.Exec(t.Context(), `UPDATE live_mcp_attempts SET due_at=clock_timestamp()-interval '2 minutes',expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err = a.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	var outcome, cause string
	var finished bool
	if err = a.Pool.QueryRow(t.Context(), `SELECT outcome,refusal_cause,finished_at IS NOT NULL FROM live_mcp_attempts WHERE id=$1`, id).Scan(&outcome, &cause, &finished); err != nil {
		t.Fatal(err)
	}
	if outcome != "refused" || cause != "attempt_expired" || !finished {
		t.Fatal("expired unused audit did not close as refused")
	}
	if err = a.Consume(t.Context(), id, s, 60); err == nil {
		t.Fatal("expired receipt consumed after reconciliation")
	}
}
