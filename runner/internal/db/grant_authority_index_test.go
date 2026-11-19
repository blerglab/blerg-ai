package db_test

import (
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestGrantAuthorityIndexPlans(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner")
	// A million events on the hot parent exceeds the lab's 925k total / 91k parent.
	if _, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload,ts)
 SELECT $1,i,gen_random_uuid(),CASE WHEN i%10=0 THEN 'grant_authority' ELSE 'message' END,
 jsonb_build_object('grant_key','key-'||(i%997),'state',CASE WHEN i%3=0 THEN 'unavailable' ELSE 'active' END),now()
 FROM generate_series(1,1000000) i`, sid); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureConcurrentIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE agent_events`); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{"", " AND payload::jsonb->>'state'<>'unavailable'"} {
		for _, key := range []string{"key-10", "missing"} {
			rows, err := pool.Query(ctx, `EXPLAIN SELECT payload::jsonb->>'state' FROM agent_events WHERE session_id=$1 AND kind='grant_authority' AND payload::jsonb->>'grant_key'=$2`+extra+` ORDER BY seq DESC LIMIT 1`, sid, key)
			if err != nil {
				t.Fatal(err)
			}
			var plan string
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				plan += line + "\n"
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(plan, "Index Scan using agent_events_grant_authority_idx") || strings.Contains(plan, "Sort") {
				t.Errorf("lookup key=%s extra=%q must use ordered grant index:\n%s", key, extra, plan)
			}
		}
	}
}

func TestGrantAuthorityIndexExistingLabDefinitionIsUnchanged(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	// Same definition as the lab; schema follows this isolated fixture's search_path.
	if _, err := pool.Exec(ctx, `CREATE INDEX agent_events_grant_authority_idx ON agent_events USING btree (session_id, ((payload ->> 'grant_key'::text)), seq DESC) WHERE (kind = 'grant_authority'::text)`); err != nil {
		t.Fatal(err)
	}
	var before uint32
	if err := pool.QueryRow(ctx, `SELECT 'agent_events_grant_authority_idx'::regclass::oid`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.EnsureConcurrentIndexes(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var after uint32
	if err := pool.QueryRow(ctx, `SELECT 'agent_events_grant_authority_idx'::regclass::oid`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		var columns []string
		if err := pool.QueryRow(ctx, `SELECT ARRAY(SELECT pg_get_indexdef('agent_events_grant_authority_idx'::regclass,k,false) FROM generate_series(1,3) k)`).Scan(&columns); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("rebuilt the lab's valid index; columns=%q", columns)
	}
}
