package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

type gateSuite struct {
	Suite   string   `json:"suite"`
	Seconds *float64 `json:"seconds"`
	Outcome string   `json:"outcome"`
}
type gateReport struct {
	RunID      string    `json:"run_id"`
	Revision   int64     `json:"revision"`
	Commit     string    `json:"commit"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Report     struct {
		Seconds  *float64    `json:"seconds"`
		Complete *bool       `json:"complete"`
		Suites   []gateSuite `json:"suites"`
	} `json:"report"`
}

var gateSuiteName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func validGateSeconds(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v <= 30*24*3600
}
func (g gateReport) valid() bool {
	if !validSessionID(g.RunID) || g.Revision < 1 || !protocol.ValidSessionOriginCommit(g.Commit) || g.StartedAt.IsZero() || g.FinishedAt.Before(g.StartedAt) || g.FinishedAt.IsZero() || !validGateSeconds(g.Report.Seconds) || g.Report.Complete == nil || (g.Report.Seconds != nil && math.Abs(g.FinishedAt.Sub(g.StartedAt).Seconds()-*g.Report.Seconds) > 1) || len(g.Report.Suites) > 128 {
		return false
	}
	seen := map[string]bool{}
	for _, s := range g.Report.Suites {
		if s.Suite == "total" || !gateSuiteName.MatchString(s.Suite) || seen[s.Suite] || !validGateSeconds(s.Seconds) || (s.Outcome != "pass" && s.Outcome != "fail" && s.Outcome != "skip") {
			return false
		}
		seen[s.Suite] = true
	}
	return true
}

// Ingestion accepts pre_merge summary fields inside an explicit run envelope.
// Decode into a typed projection: metadata, paths, logs and command text disappear.
func (a *API) HandleGateReport(w http.ResponseWriter, r *http.Request) {
	sid, raw, ok := a.agentSessionWrite(w, r, "gate reports", 128<<10)
	if !ok {
		return
	}
	var g gateReport
	if json.Unmarshal(raw, &g) != nil || !g.valid() || g.FinishedAt.After(time.Now().Add(5*time.Second)) {
		writeError(w, 400, "invalid gate report")
		return
	}
	g.StartedAt = g.StartedAt.UTC()
	g.FinishedAt = g.FinishedAt.UTC()
	sanitized, _ := json.Marshal(g)
	tx, err := a.dbPool.Begin(r.Context())
	if err != nil {
		datasetFailure("gate_transaction_unavailable")
		writeError(w, 503, "gate ingestion unavailable")
		return
	}
	defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,519))`, sid); err != nil {
		datasetFailure("gate_lock_unavailable")
		writeError(w, 503, "gate ingestion unavailable")
		return
	}
	var requests int
	err = tx.QueryRow(r.Context(), `INSERT INTO live_gate_ingest_limits(session_id,window_started,requests) VALUES($1,clock_timestamp(),1)
 ON CONFLICT(session_id) DO UPDATE SET requests=CASE WHEN live_gate_ingest_limits.window_started<clock_timestamp()-interval '1 minute' THEN 1 ELSE live_gate_ingest_limits.requests+1 END,
 window_started=CASE WHEN live_gate_ingest_limits.window_started<clock_timestamp()-interval '1 minute' THEN clock_timestamp() ELSE live_gate_ingest_limits.window_started END RETURNING requests`, sid).Scan(&requests)
	if err != nil {
		datasetFailure("gate_rate_unavailable")
		writeError(w, 503, "gate ingestion unavailable")
		return
	}
	if requests > 30 {
		datasetFailure("gate_rate_limit")
		writeError(w, 429, "gate report rate limit")
		return
	}
	var count int
	var exists bool
	if err = tx.QueryRow(r.Context(), `SELECT count(*),coalesce(bool_or(run_id=$2),false) FROM live_gate_reports WHERE session_id=$1`, sid, g.RunID).Scan(&count, &exists); err != nil {
		datasetFailure("gate_count_unavailable")
		writeError(w, 503, "gate ingestion unavailable")
		return
	}
	if count >= 256 && !exists {
		datasetFailure("gate_count_limit")
		writeError(w, 429, "gate report count limit")
		return
	}
	tag, err := tx.Exec(r.Context(), `INSERT INTO live_gate_reports(session_id,run_id,revision,report) VALUES($1,$2,$3,$4)
 ON CONFLICT(session_id,run_id) DO UPDATE SET revision=excluded.revision,report=excluded.report,observed_at=clock_timestamp()
 WHERE live_gate_reports.revision<excluded.revision`, sid, g.RunID, g.Revision, sanitized)
	if err != nil {
		datasetFailure("gate_ingestion_unavailable")
		writeError(w, 503, "gate ingestion unavailable")
		return
	}
	if tag.RowsAffected() == 0 {
		var same bool
		if err := tx.QueryRow(r.Context(), `SELECT revision=$3 AND report=$4::jsonb FROM live_gate_reports WHERE session_id=$1 AND run_id=$2`, sid, g.RunID, g.Revision, sanitized).Scan(&same); err != nil {
			datasetFailure("gate_retry_unavailable")
			writeError(w, 503, "gate ingestion unavailable")
			return
		}
		if !same {
			writeError(w, 409, "gate revision conflict")
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			datasetFailure("gate_commit_unavailable")
			writeError(w, 503, "gate ingestion unavailable")
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		datasetFailure("gate_commit_unavailable")
		writeError(w, 503, "gate ingestion unavailable")
		return
	}
	root, err := datasetWorkspaceRoot(r.Context(), a.dbPool, sid)
	if err == nil {
		a.hub.datasets.invalidate("blerg.gates.durations", root)
		a.hub.datasets.invalidate("blerg.gates.bins", root)
	}
	if err != nil {
		datasetFailure("gate_workspace_unavailable")
	}
	writeJSON(w, 201, map[string]bool{"ok": true})
}

func gateDatasetRows(ctx context.Context, pool *pgxpool.Pool, account, sid string, p map[string]string) ([]protocol.DatasetRow, time.Time, string) {
	now := time.Now().UTC()
	since := datasetSince(p, now, 7*24*time.Hour, 30*24*time.Hour)
	observed := time.Time{}
	root, err := datasetWorkspaceRoot(ctx, pool, sid)
	if err != nil {
		return nil, now, "workspace_unavailable"
	}
	// Visibility is applied to source records BEFORE producing any rows.
	result, err := pool.Query(ctx, `WITH RECURSIVE family AS (
 SELECT id,ARRAY[id] AS path FROM sessions WHERE id=$1
 UNION ALL SELECT s.id,f.path||s.id FROM sessions s JOIN family f ON s.parent_session_id=f.id
 WHERE NOT s.id=ANY(f.path) AND cardinality(f.path)<64)
 SELECT g.session_id::text,g.report,g.observed_at FROM live_gate_reports g JOIN family f ON f.id=g.session_id JOIN sessions s ON s.id=g.session_id
 WHERE (NOT s.private OR s.spawning_account_id=$2) AND (g.report->>'finished_at')::timestamptz >= $3 ORDER BY (g.report->>'finished_at')::timestamptz DESC,g.session_id,g.run_id LIMIT 78`, root, account, since)
	if err != nil {
		return nil, now, "gates_unavailable"
	}
	defer result.Close()
	rows := []protocol.DatasetRow{}
	runs := 0
	for result.Next() {
		if runs == 77 {
			markDatasetTruncated(ctx)
			break
		}
		runs++
		var raw []byte
		var source string
		var at time.Time
		var g gateReport
		if result.Scan(&source, &raw, &at) != nil || json.Unmarshal(raw, &g) != nil || !g.valid() {
			return nil, now, "invalid_gate_sample"
		}
		if at.After(observed) {
			observed = at
		}
		outcome := "incomplete"
		if *g.Report.Complete {
			outcome = "pass"
			for _, s := range g.Report.Suites {
				if s.Outcome == "fail" {
					outcome = "fail"
				}
			}
		}
		row := func(suite string, seconds float64, outcome string) protocol.DatasetRow {
			return protocol.DatasetRow{"key": datasetRowKey(source, g.RunID, suite), "run_id": g.RunID, "suite": suite, "seconds": seconds, "outcome": outcome, "commit": g.Commit, "start": g.StartedAt.UTC().Format(time.RFC3339Nano), "end": g.FinishedAt.UTC().Format(time.RFC3339Nano)}
		}
		rows = append(rows, row("total", *g.Report.Seconds, outcome))
		for _, s := range g.Report.Suites {
			rows = append(rows, row(s.Suite, *s.Seconds, s.Outcome))
		}
		if len(rows) > 10000 {
			return nil, now, "row_limit"
		}
	}
	if result.Err() != nil {
		return nil, now, "gates_unavailable"
	}
	if observed.IsZero() {
		observed = now
	}
	return rows, observed, ""
}
