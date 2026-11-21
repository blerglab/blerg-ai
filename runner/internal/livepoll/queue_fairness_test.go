package livepoll

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

func queueBinding(t *testing.T, a Attempts, base ld.Scope, workspace string, n, owner int) ld.Scope {
	t.Helper()
	s := base
	s.Binding = fmt.Sprintf("00000000-0000-4000-8000-%012d", 1000+n)
	s.Owner = fmt.Sprintf("00000000-0000-4000-8000-%012d", 100+owner)
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Pool.Exec(t.Context(), `INSERT INTO live_mcp_binding_state(binding_id,revision,scope,enabled,fence,lease_until,demand_until,workspace_id,expires_at,next_due_at)
 VALUES($1,1,$2,true,1,clock_timestamp()+interval '1 minute',clock_timestamp()+interval '1 hour',$3,clock_timestamp()+interval '1 hour',clock_timestamp()-interval '1 day'+make_interval(secs=>$4))`, s.Binding, raw, workspace, owner); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestAttemptQueueEightySlotsAcrossReplicas(t *testing.T) {
	a, base, workspace := scheduleFixture(t)
	if _, err := a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	var first string
	for i := 0; i < 79; i++ {
		s := queueBinding(t, a, base, workspace, i, i/20)
		id, err := a.Create(t.Context(), s, 1)
		if err != nil {
			t.Fatal("queue refused before capacity", i, err)
		}
		if i == 0 {
			first = id
		}
	}
	var contenders []ld.Scope
	for i := 79; i < 87; i++ {
		contenders = append(contenders, queueBinding(t, a, base, workspace, i, 4))
	}
	var wg sync.WaitGroup
	wins := make(chan string, len(contenders))
	for _, s := range contenders {
		wg.Go(func() {
			id, err := (Attempts{a.Pool}).Create(t.Context(), s, 1)
			if err == nil {
				wins <- id
			}
		})
	}
	wg.Wait()
	close(wins)
	if len(wins) != 1 {
		t.Fatal("queue capacity race winners", len(wins))
	}
	var pending int
	if err := a.Pool.QueryRow(t.Context(), `SELECT count(*) FROM live_mcp_attempts WHERE outcome='undispatched' AND expires_at>clock_timestamp()`).Scan(&pending); err != nil || pending != 80 {
		t.Fatal("shared queue not exactly 80", pending, err)
	}
	extra := queueBinding(t, a, base, workspace, 99, 5)
	if _, err := a.Create(t.Context(), extra, 1); err == nil {
		t.Fatal("81st queued attempt admitted")
	}
	if _, err := a.Pool.Exec(t.Context(), `UPDATE live_mcp_attempts SET due_at=clock_timestamp()-interval '2 minutes',expires_at=clock_timestamp()-interval '1 second' WHERE id::text=$1`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Create(t.Context(), extra, 1); err != nil {
		t.Fatal("expired queue slot not reusable", err)
	}
}
func TestScheduleFairOwnerRotationBeyondQueueWindow(t *testing.T) {
	a, base, workspace := scheduleFixture(t)
	if _, err := a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	// Six owners with twenty overdue bindings each. The last two owners would
	// otherwise sit outside the eighty-row window behind older owners' backlogs.
	for owner := 0; owner < 6; owner++ {
		for n := 0; n < 20; n++ {
			queueBinding(t, a, base, workspace, owner*20+n, owner)
		}
	}
	if _, err := a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for round := 0; round < 3; round++ {
		leases, err := (Attempts{a.Pool}).Claim(t.Context())
		if err != nil || len(leases) != 4 {
			t.Fatal("fair claim batch", len(leases), err)
		}
		owners := map[string]bool{}
		for _, l := range leases {
			owners[l.Scope.Owner] = true
			seen[l.Scope.Owner]++
		}
		if len(owners) != 4 {
			t.Fatal("one owner monopolized worker batch", owners)
		}
	}
	if len(seen) != 6 {
		t.Fatal("owners beyond queue window starved", len(seen))
	}
	// Persisted turns survive a new scheduler/store instance and require no
	// elapsed time or catch-up burst to move past an older owner's backlog.
	for owner, calls := range seen {
		if calls != 2 {
			t.Fatal("owner rotation is not fair across complete rounds", owner, calls)
		}
	}
	var turns int
	if err := a.Pool.QueryRow(t.Context(), `SELECT count(*) FROM live_mcp_owner_schedule WHERE last_claimed_turn>0`).Scan(&turns); err != nil || turns != 6 {
		t.Fatal("owner rotation not durable", turns, err)
	}
}

func TestScheduleQueueWindowIsEighty(t *testing.T) {
	a, base, workspace := scheduleFixture(t)
	if _, err := a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 81; n++ {
		queueBinding(t, a, base, workspace, n, n/20)
	}
	if _, err := a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	holder, err := a.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(t.Context()) }()
	locked, err := holder.Query(t.Context(), `SELECT binding_id FROM live_mcp_binding_state WHERE enabled ORDER BY next_due_at,binding_id LIMIT 80 FOR UPDATE`)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for locked.Next() {
		count++
	}
	locked.Close()
	if err = locked.Err(); err != nil || count != 80 {
		t.Fatal("queue lock fixture", count, err)
	}
	leases, err := (Attempts{a.Pool}).Claim(t.Context())
	if err != nil || len(leases) != 0 {
		t.Fatal("scheduler scanned beyond eighty-slot queue", len(leases), err)
	}
	if err = holder.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	leases, err = a.Claim(t.Context())
	if err != nil || len(leases) != 4 {
		t.Fatal("unlocked queue did not resume", len(leases), err)
	}
}

func TestScheduleConcurrentClaimers(t *testing.T) {
	a, base, workspace := scheduleFixture(t)
	if _, err := a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 60; n++ {
		queueBinding(t, a, base, workspace, n, n%6)
	}
	if _, err := a.Pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET lease_until=clock_timestamp() WHERE enabled`); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 5; round++ {
		start := make(chan struct{})
		results := make(chan []Lease, 3)
		errs := make(chan error, 3)
		var wg sync.WaitGroup
		for replica := 0; replica < 3; replica++ {
			wg.Go(func() { <-start; leases, err := (Attempts{a.Pool}).Claim(t.Context()); results <- leases; errs <- err })
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal("concurrent claimer refused", err)
			}
		}
		seen := map[string]bool{}
		for leases := range results {
			for _, l := range leases {
				if seen[l.Scope.Binding] {
					t.Fatal("duplicate concurrent lease", l.Scope.Binding)
				}
				seen[l.Scope.Binding] = true
			}
		}
		if len(seen) != 12 {
			t.Fatal("concurrent claimers did not fill leases", len(seen))
		}
	}
}
