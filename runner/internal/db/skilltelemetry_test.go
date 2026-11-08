package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/skills"
)

func TestSkillTelemetryOwnerTree(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	root := mustSession(t, pool, daemon, "skill-owner")
	worker := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "skill-owner", ParentSessionID: root})
	foreign := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "other-owner", ParentSessionID: root})
	now := time.Now().UTC()
	for _, id := range []string{root, worker, foreign} {
		load := skills.Load{SessionID: id, Skill: "ux-design", Version: "test-version", Mode: "on_demand", Bytes: 100}
		raw, _ := json.Marshal(load)
		if _, _, err := db.AppendAgentEventAt(ctx, pool, id, "11111111-1111-4111-8111-111111111111", "skill_load", string(raw), now); err != nil {
			t.Fatal(err)
		}
	}
	report, err := db.SkillTelemetry(ctx, pool, "skill-owner", root, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || len(report.Loads) != 2 {
		t.Fatalf("owner tree: %+v %v", report, err)
	}
	for _, load := range report.Loads {
		if load.SessionID == foreign {
			t.Fatal("foreign load leaked")
		}
	}
	report, err = db.SkillTelemetry(ctx, pool, "other-owner", root, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || len(report.Loads) != 0 {
		t.Fatal("foreign workspace widened scope")
	}
}

func TestSkillTelemetryNewestAndIndependentFailure(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	root := mustSession(t, pool, daemon, "skill-owner")
	// One statement seeds deterministic ordered records without 5,001 round trips.
	_, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload,ts) SELECT $1::uuid,n,gen_random_uuid(),'skill_load',jsonb_build_object('session_id',$1::text,'skill','ux-design','version',n::text),now()-interval '1 hour'+n*interval '1 millisecond' FROM generate_series(1,5001) n`, root)
	if err != nil {
		t.Fatal(err)
	}
	report, err := db.SkillTelemetry(ctx, pool, "skill-owner", root, time.Time{}, time.Now())
	if err != nil || !report.Truncated || len(report.Loads) != 5000 || report.Loads[0].Version != "5001" || report.Loads[4999].Version != "2" {
		t.Fatalf("newest cap: %d %+v %v", len(report.Loads), report.Truncated, err)
	}
	_, err = pool.Exec(ctx, `UPDATE agent_events SET payload='{"bytes":"invalid"}' WHERE session_id=$1 AND seq=5001`, root)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := db.WorkspaceMetrics(ctx, pool, root, time.Now())
	if err != nil || len(metrics.Workers) != 1 || !metrics.Workers[0].Skills.Unavailable {
		t.Fatalf("skill failure hid metrics: %+v %v", metrics, err)
	}
}

func TestSkillEvidenceRateBound(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	root := mustSession(t, pool, daemon, "skill-owner")
	for i := 0; i < 31; i++ {
		accepted, err := db.AppendSkillEvidence(ctx, pool, root, fmt.Sprintf("00000000-0000-4000-8000-%012d", i), `{"source":"person"}`)
		if err != nil || accepted != (i < 30) {
			t.Fatalf("assessment %d accepted=%v: %v", i, accepted, err)
		}
	}
}

func TestSkillEvidenceLoadedVersionAndSeparateOwnerQuota(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	root := mustSession(t, pool, mustDaemon(t, pool), "skill-owner")
	payload := `{"skill":"ux-design","version":"version-a","source":"agent"}`
	other := mustSession(t, pool, mustDaemon(t, pool), "other-owner")
	if _, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload) VALUES($1,1,gen_random_uuid(),'skill_load','{"skill":"ux-design","version":"version-a"}')`, other); err != nil {
		t.Fatal(err)
	}
	_, err := db.AppendSkillEvidence(ctx, pool, root, "00000000-0000-4000-8000-000000001000", payload)
	if !errors.Is(err, db.ErrSkillEvidenceVersion) {
		t.Fatalf("unloaded evidence accepted: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload) VALUES($1,1,gen_random_uuid(),'skill_load','{"skill":"ux-design","version":"version-b"}')`, root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.AppendSkillEvidence(ctx, pool, root, "00000000-0000-4000-8000-000000001000", payload)
	if !errors.Is(err, db.ErrSkillEvidenceVersion) {
		t.Fatalf("wrong loaded version accepted: %v", err)
	}
	payload = strings.ReplaceAll(payload, "version-a", "version-b")
	for i := 0; i < 31; i++ {
		ok, err := db.AppendSkillEvidence(ctx, pool, root, fmt.Sprintf("00000000-0000-4000-8000-%012d", i+2000), payload)
		if err != nil || ok != (i < 30) {
			t.Fatalf("agent quota %d: %v %v", i, ok, err)
		}
	}
	// Retrying an accepted receipt remains accepted after quota exhaustion.
	ok, err := db.AppendSkillEvidence(ctx, pool, root, "00000000-0000-4000-8000-000000002000", payload)
	if !ok || err != nil {
		t.Fatalf("retry rejected: %v %v", ok, err)
	}
	ok, err = db.AppendSkillEvidence(ctx, pool, root, "00000000-0000-4000-8000-000000003000", strings.ReplaceAll(payload, "agent", "person"))
	if !ok || err != nil {
		t.Fatalf("agent quota blocked owner: %v %v", ok, err)
	}
}
