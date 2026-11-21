package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGrantAuthorityTransitionsDeduplicateAndReplaceQueuedNotice(t *testing.T) {
	pool := connect(t)
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner")
	record := func(state string) {
		t.Helper()
		if err := db.RecordGrantAuthority(t.Context(), pool, sid, "grant-1", "Ticket board", state); err != nil {
			t.Error(err)
		}
	}
	record("active")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { record("suspended") })
	}
	wg.Wait()
	check := func(events, pending int) {
		t.Helper()
		var gotEvents, gotPending int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='grant_authority'`, sid).Scan(&gotEvents); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1 AND settled_at IS NULL`, sid).Scan(&gotPending); err != nil {
			t.Fatal(err)
		}
		if gotEvents != events || gotPending != pending {
			t.Fatalf("transitions=%d pending=%d, want %d/%d", gotEvents, gotPending, events, pending)
		}
	}
	check(2, 1)
	// A fresh caller after restart sees persisted state, and recovery supersedes
	// the queued suspension before any reconnect can deliver it.
	record("suspended")
	check(2, 1)
	record("active")
	check(3, 1)
	record("unavailable")
	check(4, 1)
	record("revoked")
	check(5, 1)
	var accepted int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='input_accepted'`, sid).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted != 0 {
		t.Fatal("system notices created person-input acceptance events")
	}
	var frame string
	if err := pool.QueryRow(t.Context(), `SELECT frame->>'text' FROM session_inputs WHERE session_id=$1 AND settled_at IS NULL`, sid).Scan(&frame); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := pool.QueryRow(t.Context(), `SELECT frame->>'source' FROM session_inputs WHERE session_id=$1 AND settled_at IS NULL`, sid).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "system" {
		t.Fatalf("permission notice attributed to %q instead of system", source)
	}
	if frame != "[system] Ticket board permission has expired. Check session Permissions and grant access again." {
		t.Fatalf("notice=%q", frame)
	}
}

func TestGrantAuthorityPreservesInputQueueBounds(t *testing.T) {
	pool := connect(t)
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner")
	if err := db.RecordGrantAuthority(t.Context(), pool, sid, "grant", "Ticket board", "active"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO session_inputs(session_id,client_id,frame,event_id) SELECT $1,'unrelated-'||n,'{}'::jsonb,gen_random_uuid() FROM generate_series(1,64) AS pending(n)`, sid); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordGrantAuthority(t.Context(), pool, sid, "grant", "Ticket board", "suspended"); !errors.Is(err, db.ErrSessionInputQueue) {
		t.Fatalf("queue overflow=%v", err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='grant_authority'`, sid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("failed notice advanced state without durable delivery")
	}
}

func TestGrantAuthorityIgnoresCallsWithoutAnOwnedSession(t *testing.T) {
	pool := connect(t)
	for _, sid := range []string{"audit-only", "00000000-0000-4000-8000-000000000748", mustSession(t, pool, mustDaemon(t, pool), "")} {
		if err := db.RecordGrantAuthority(t.Context(), pool, sid, "grant", "Ticket board", "suspended"); err != nil {
			t.Errorf("non-session observation: %v", err)
		}
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE kind='grant_authority'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("notice created without an owned Runner session")
	}
}

func TestGrantAuthorityDoesNotBlockTheExecutionReadFence(t *testing.T) {
	pool := connect(t)
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner")
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), `SELECT id FROM sessions WHERE id=$1 FOR SHARE`, sid); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err = db.RecordGrantAuthority(ctx, pool, sid, "grant", "Ticket board", "suspended"); err != nil {
		t.Fatalf("notice blocked an execution read fence: %v", err)
	}
}

func TestGrantAuthorityUnavailableIsSilentAcrossFlappingAndRecovery(t *testing.T) {
	pool := connect(t)
	for _, tc := range []struct {
		name    string
		states  []string
		notices int
	}{
		{"outage alone", []string{"unavailable", "active", "unavailable", "active"}, 0},
		{"flapping active", []string{"active", "unavailable", "active", "unavailable", "active"}, 0},
		{"suspended across outage", []string{"active", "suspended", "unavailable", "active"}, 2},
		{"flapping suspended", []string{"active", "suspended", "unavailable", "suspended", "unavailable", "suspended", "active"}, 2},
		{"flapping revoked", []string{"active", "revoked", "unavailable", "revoked", "unavailable", "revoked", "active"}, 2},
		{"revoked across outage", []string{"active", "revoked", "unavailable", "active"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sid := mustSession(t, pool, mustDaemon(t, pool), "owner")
			for _, state := range tc.states {
				if err := db.RecordGrantAuthority(t.Context(), pool, sid, "grant", "Ticket board", state); err != nil {
					t.Fatal(err)
				}
			}
			var notices, events int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1`, sid).Scan(&notices); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='grant_authority'`, sid).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if notices != tc.notices || events != len(tc.states) {
				t.Fatalf("notices=%d events=%d want %d/%d", notices, events, tc.notices, len(tc.states))
			}
		})
	}
}

func TestGrantAuthorityUnchangedObservationSkipsPublisherLock(t *testing.T) {
	pool := connect(t)
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner")
	if err := db.RecordGrantAuthority(t.Context(), pool, sid, "grant", "Ticket board", "active"); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, "grant-authority:"+sid); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err = db.RecordGrantAuthority(ctx, pool, sid, "grant", "Ticket board", "active"); err != nil {
		t.Fatalf("unchanged observation waited on publisher transaction: %v", err)
	}
}

func TestGrantAuthorityConcurrentMainAndInheritedObservers(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	main := mustSession(t, pool, daemon, "owner")
	sessions := []string{main}
	for range 5 {
		child := mustSession(t, pool, daemon, "owner")
		if _, err := pool.Exec(t.Context(), `UPDATE sessions SET parent_session_id=$1 WHERE id=$2`, main, child); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, child)
	}
	for _, state := range []string{"active", "suspended", "unavailable", "suspended", "active"} {
		var wg sync.WaitGroup
		for _, sid := range sessions {
			for range 3 {
				wg.Go(func() {
					if err := db.RecordGrantAuthority(t.Context(), pool, sid, "board:shared", "Ticket board", state, db.GrantAuthorityObservation{AccountID: "owner", ProofKind: "session_id", ProofValue: "original-login"}); err != nil {
						t.Error(err)
					}
				})
			}
		}
		wg.Wait()
	}
	var notices int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1`, main).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 2 {
		t.Fatalf("same inherited board generated %d notices, want one suspension and one recovery", notices)
	}
}

func TestGrantAuthorityOrdersObservationsAndBoundsOutageSilence(t *testing.T) {
	pool := connect(t)
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner")
	start := time.Now().UTC()
	record := func(offset time.Duration, state, chain string) {
		t.Helper()
		if err := db.RecordGrantAuthority(t.Context(), pool, sid, "board:shared", "Ticket board", state, db.GrantAuthorityObservation{AskedAt: start.Add(offset), AccountID: "owner", ProofKind: "session_id", ProofValue: chain}); err != nil {
			t.Fatal(err)
		}
	}
	notices := func(want int) {
		t.Helper()
		var n int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1`, sid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("notices=%d want %d", n, want)
		}
	}
	record(0, "active", "chain")
	record(time.Minute, "unavailable", "chain")
	record(2*time.Minute, "unavailable", "chain")
	notices(0)
	record(6*time.Minute, "unavailable", "chain")
	record(7*time.Minute, "unavailable", "chain")
	notices(1)
	record(8*time.Minute, "active", "chain")
	var pending int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1 AND settled_at IS NULL`, sid).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatal("recovery left a stale verification notice queued")
	}

	record(3*time.Minute, "revoked", "chain") // late result cannot undo recovery
	notices(1)
	record(9*time.Minute, "suspended", "chain")
	record(10*time.Minute, "active", "chain")
	record(9*time.Minute+time.Second, "suspended", "chain")
	notices(3)
	// A separate original login chain is a separate grant, even on the same board.
	record(11*time.Minute, "suspended", "independent-chain")
	notices(4)
	var stale int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='grant_authority' AND payload::jsonb->>'state'='revoked'`, sid).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Fatal("stale core result replaced newer observed authority")
	}
}

func TestGrantAuthorityCoreRollingRestart(t *testing.T) {
	pool := connect(t)
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner")
	var stage atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch stage.Load() {
		case 0:
			http.Error(w, "warming up", http.StatusServiceUnavailable)
		case 1:
			http.Error(w, "not found", http.StatusNotFound) // pre-standing Core in a mixed-version rollout
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "fixture-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
		}
	}))
	defer srv.Close()
	for step := int32(0); step < 3; step++ {
		stage.Store(step)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				// Independent clients stand in for independent runner processes.
				client := mcpgw.HTTPBoardExchanger{BaseURL: srv.URL, InternalKey: "fixture-key", HTTP: srv.Client()}
				askedAt := time.Now()
				_, err := client.Exchange(t.Context(), mcpgw.Proof{AccountID: "owner", SessionID: "original-chain"}, "board", sid, true)
				state := mcpgw.AuthorityState(err)
				if step < 2 && state != "unavailable" {
					t.Errorf("restart classified %s", state)
				}
				if err := db.RecordGrantAuthority(t.Context(), pool, sid, "board:shared", "Ticket board", state, db.GrantAuthorityObservation{AskedAt: askedAt, AccountID: "owner", ProofKind: "session_id", ProofValue: "original-chain"}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
	}
	var notices int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1`, sid).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 0 {
		t.Fatalf("rolling restart emitted %d notices", notices)
	}
}

func TestGrantAuthorityAdoptsLegacyNoticeHistory(t *testing.T) {
	pool := connect(t)
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner")
	// Legacy observer keys (also backfilled by the migration) already notified.
	if err := db.RecordGrantAuthority(t.Context(), pool, sid, "board:shared", "Ticket board", "suspended"); err != nil {
		t.Fatal(err)
	}
	// Reapply the new migration to a populated pre-upgrade journal.
	if _, err := pool.Exec(t.Context(), `DROP TABLE grant_authority_observations`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM schema_migrations WHERE filename='196_grant_authority_observations.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := db.RunMigrations(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	opts := db.GrantAuthorityObservation{AccountID: "owner", ProofKind: "session_id", ProofValue: "chain"}
	if err := db.RecordGrantAuthority(t.Context(), pool, sid, "board:shared", "Ticket board", "suspended", opts); err != nil {
		t.Fatal(err)
	}
	var notices int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1`, sid).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatalf("upgrade repeated existing suspension: %d notices", notices)
	}
	if err := db.RecordGrantAuthority(t.Context(), pool, sid, "board:shared", "Ticket board", "active", opts); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1 AND settled_at IS NULL`, sid).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("stale legacy notices survived recovery: %d pending", pending)
	}
}

func TestGrantAuthorityKeepsSeparateProofChainsOnSameBoard(t *testing.T) {
	pool := connect(t)
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner")
	for _, state := range []string{"active", "suspended", "active"} {
		for _, chain := range []string{"original-chain-a", "original-chain-b"} {
			if err := db.RecordGrantAuthority(t.Context(), pool, sid, "board:shared", "Ticket board", state, db.GrantAuthorityObservation{AccountID: "owner", ProofKind: "session_id", ProofValue: chain}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var notices int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1`, sid).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 4 {
		t.Fatalf("distinct proof chains generated %d notices, want separate suspension and recovery for both (4)", notices)
	}
}

func TestGrantAuthorityOwnerMissingNoticeExplainsRepair(t *testing.T) {
	pool := connect(t)
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner")
	start := time.Now()
	for _, offset := range []time.Duration{0, 5 * time.Minute, 6 * time.Minute} {
		if err := db.RecordGrantAuthority(t.Context(), pool, sid, "board:shared", "Ticket board", "unavailable", db.GrantAuthorityObservation{AskedAt: start.Add(offset), AccountID: "owner", ProofKind: "session_id", ProofValue: "chain", Cause: "board_owner_missing"}); err != nil {
			t.Fatal(err)
		}
	}
	var text string
	if err := pool.QueryRow(t.Context(), `SELECT frame->>'text' FROM session_inputs WHERE session_id=$1`, sid).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "no recorded owner") || !strings.Contains(text, "owner or an administrator to assign an owner") || strings.Contains(text, "retry") {
		t.Fatalf("ownerless grant notice is not actionable: %q", text)
	}
	var notices int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1`, sid).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatalf("ownerless outage repeated notice %d times", notices)
	}
}
