package db

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/skills"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SkillTelemetry uses the same owner-fenced recursive scope as activity. Each
// event belongs to exactly one session; subagent records stay under that session.
func SkillTelemetry(ctx context.Context, pool *pgxpool.Pool, owner, workspace string, from, to time.Time) (skills.Report, error) {
	finishProfile := ObserveInsights(InsightsSkills)
	defer finishProfile()
	out := skills.Report{Loads: []skills.Load{}, Reporting: []skills.Load{}, Evidence: []skills.Evidence{}}
	conn, release, err := insightsConnection(ctx, pool)
	if err != nil {
		return out, err
	}
	defer release()
	rows, err := conn.Query(ctx, insightOwnedScope+` SELECT e.kind,e.payload,e.ts FROM agent_events e JOIN scope s ON s.id=e.session_id WHERE e.kind IN ('skill_load','skill_reporting','skill_evidence','skill_applicability') AND e.ts >= $3 AND e.ts < $4 ORDER BY e.ts DESC,e.session_id,e.seq DESC LIMIT 5001`, owner, workspace, from, to)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var kind string
		var raw []byte
		var at time.Time
		if err = rows.Scan(&kind, &raw, &at); err != nil {
			return out, err
		}
		count++
		if count > 5000 {
			out.Truncated = true
			break
		}
		if kind == "skill_applicability" {
			var signal skills.Applicability
			if err = json.Unmarshal(raw, &signal); err != nil {
				return out, err
			}
			signal.At = at
			out.Applicability = append(out.Applicability, signal)
			continue
		}
		if kind == "skill_evidence" {
			var evidence skills.Evidence
			if err = json.Unmarshal(raw, &evidence); err != nil {
				return out, err
			}
			evidence.At = at
			out.Evidence = append(out.Evidence, evidence)
			continue
		}
		var load skills.Load
		if err = json.Unmarshal(raw, &load); err != nil {
			return out, err
		}
		load.At = at
		if kind == "skill_load" {
			out.Loads = append(out.Loads, load)
		} else {
			out.Reporting = append(out.Reporting, load)
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	facts, err := conn.Query(ctx, insightOwnedScope+` SELECT s.id,
 (SELECT count(*) FROM session_artifacts a WHERE a.session_id=s.id AND a.content_type LIKE 'image/%' AND a.created_at >= $3 AND a.created_at < $4),
 (SELECT count(*) FROM workspace_metric_runs r WHERE r.session_id=s.id AND r.data->>'full_suite'='true' AND r.data->>'state'='finished' AND r.updated_at >= $3 AND r.updated_at < $4),
 (SELECT count(*) FROM agent_events e WHERE e.session_id=s.id AND e.kind='mark' AND e.payload->>'kind'='failure' AND e.payload->>'type'='test' AND e.ts >= $3 AND e.ts < $4 AND e.ts < (SELECT min(ed.ts) FROM agent_events ed WHERE ed.session_id=s.id AND ed.kind='skill_applicability'))
 FROM scope s WHERE EXISTS(SELECT 1 FROM agent_events e WHERE e.session_id=s.id AND e.kind IN ('skill_load','skill_reporting')) ORDER BY s.id LIMIT 5001`, owner, workspace, from, to)
	if err != nil {
		return out, err
	}
	defer facts.Close()
	for facts.Next() {
		if len(out.Facts) == 5000 {
			out.Truncated = true
			break
		}
		var fact skills.Facts
		if err = facts.Scan(&fact.SessionID, &fact.ImageArtifacts, &fact.FullSuiteRuns, &fact.ReportedTestFailuresBeforeEdit); err != nil {
			return out, err
		}
		out.Facts = append(out.Facts, fact)
	}
	return out, facts.Err()
}

// Optional telemetry must never hide timing, spending or the other Insights.
func AvailableSkillTelemetry(ctx context.Context, pool *pgxpool.Pool, owner, workspace string, from, to time.Time) skills.Report {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	report, err := SkillTelemetry(ctx, pool, owner, workspace, from, to)
	if err != nil {
		failurelog.Default.Event("skill_telemetry_read", slog.LevelError, "cause", failurelog.Cause(err))
		return skills.Report{Unavailable: true}
	}
	return report
}

// Serialize against the session row so the bound also holds across replicas.
func AppendSkillEvidence(ctx context.Context, pool *pgxpool.Pool, session, receipt, payload string) (bool, error) {
	_, _, err := AppendSkillEvidenceEvent(ctx, pool, session, receipt, payload, time.Time{})
	if errors.Is(err, ErrSkillEvidenceRate) {
		return false, nil
	}
	return err == nil, err
}

var ErrSkillEvidenceVersion = errors.New("skill version was not loaded by this session")
var ErrSkillEvidenceRate = errors.New("skill evidence rate exceeded")

func AppendSkillEvidenceEvent(ctx context.Context, pool *pgxpool.Pool, session, receipt, payload string, at time.Time) (int64, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	if err = tx.QueryRow(ctx, `SELECT id FROM sessions WHERE id=$1 FOR UPDATE`, session).Scan(&id); err != nil {
		return 0, false, err
	}
	// A retry must not consume quota or reject a previously accepted version.
	var seq int64
	err = tx.QueryRow(ctx, `SELECT seq FROM agent_events WHERE session_id=$1 AND client_event_id=$2`, session, receipt).Scan(&seq)
	if err == nil {
		return seq, false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	var evidence skills.Evidence
	if err = json.Unmarshal([]byte(payload), &evidence); err != nil {
		return 0, false, err
	}
	if evidence.Source != "person" {
		var loaded bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_events WHERE session_id=$1 AND kind='skill_load' AND payload->>'skill'=$2 AND payload->>'version'=$3)`, session, evidence.Skill, evidence.Version).Scan(&loaded); err != nil {
			return 0, false, err
		}
		if !loaded {
			return 0, false, ErrSkillEvidenceVersion
		}
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='skill_evidence' AND COALESCE(payload->>'source'='person',false)= $2 AND ts > now()-interval '1 minute'`, session, evidence.Source == "person").Scan(&count); err != nil {
		return 0, false, err
	}
	if count >= 30 {
		return 0, false, ErrSkillEvidenceRate
	}
	seq, fresh, err := appendAgentEvent(ctx, tx, session, receipt, "skill_evidence", payload, at)
	if err != nil {
		return 0, false, err
	}
	return seq, fresh, tx.Commit(ctx)
}
