package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/jackc/pgx/v5/pgconn"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/testreport"
	"github.com/blerglab/blerg-ai/runner/internal/testtiming"
)

// This is a projection, never raw gate history: machine names, paths and logs
// have no place in project test history. Optional facts remain unknown.
type performanceSuite struct {
	Flakes        *int                    `json:"flakes,omitempty"`
	FlakeReruns   []testtiming.FlakeRerun `json:"flake_reruns,omitempty"`
	Layer         string                  `json:"layer,omitempty"`
	BudgetSeconds *float64                `json:"budget_seconds,omitempty"`
	Suite         string                  `json:"suite"`
	Seconds       float64                 `json:"seconds"`
	Outcome       string                  `json:"outcome"`
	Counts        *testtiming.Counts      `json:"counts"`
	Slowest       []testtiming.TestTiming `json:"slowest"`
}
type performanceRun struct {
	WorkspaceID    string             `json:"workspace_id"`
	RunID          string             `json:"run_id"`
	MemberID       string             `json:"member_id"`
	Revision       int64              `json:"revision"`
	Source         string             `json:"source"`
	Commit         *string            `json:"commit"`
	StartedAt      time.Time          `json:"started_at"`
	FinishedAt     time.Time          `json:"finished_at"`
	Seconds        float64            `json:"seconds"`
	Complete       bool               `json:"complete"`
	CachedPackages *int               `json:"cached_packages,omitempty"`
	Region         *string            `json:"region"`
	Suites         []performanceSuite `json:"suites"`
}

func performanceText(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func projectGateRecord(record json.RawMessage, member string, revision int64, now time.Time) (performanceRun, bool) {
	var r testtiming.Record
	var shape struct {
		Seconds  *float64 `json:"seconds"`
		Complete *bool    `json:"complete"`
		Suites   []struct {
			Seconds *float64 `json:"seconds"`
			Tests   *struct {
				Counts *struct {
					Pass *int `json:"pass"`
					Fail *int `json:"fail"`
					Skip *int `json:"skip"`
				} `json:"counts"`
			} `json:"tests"`
		} `json:"suites"`
	}
	if json.Unmarshal(record, &r) != nil || json.Unmarshal(record, &shape) != nil {
		return performanceRun{}, false
	}
	start, e1 := time.Parse(time.RFC3339Nano, r.StartedAt)
	end, e2 := time.Parse(time.RFC3339Nano, r.FinishedAt)
	if shape.Seconds == nil || shape.Complete == nil || e1 != nil || e2 != nil || !validSessionID(r.ID) || revision < 1 || (r.Commit != "" && !protocol.ValidSessionOriginCommit(r.Commit)) || end.Before(start) || end.After(now.Add(5*time.Second)) || end.Before(now.AddDate(0, 0, -180)) || !validGateSeconds(&r.Seconds) || math.Abs(end.Sub(start).Seconds()-r.Seconds) > 1 || len(r.Suites) > 128 {
		return performanceRun{}, false
	}
	var commit *string
	if r.Commit != "" {
		commit = &r.Commit
	}
	out := performanceRun{RunID: r.ID, MemberID: member, Revision: revision, Source: "gate", Commit: commit, StartedAt: start.UTC(), FinishedAt: end.UTC(), Seconds: r.Seconds, Complete: r.Complete, Region: nil, Suites: []performanceSuite{}}
	seen := map[string]bool{}
	for i, s := range r.Suites {
		if shape.Suites[i].Seconds == nil || !gateSuiteName.MatchString(s.Suite) || seen[s.Suite] || !validGateSeconds(&s.Seconds) || (s.Outcome != "pass" && s.Outcome != "fail" && s.Outcome != "skip") {
			return performanceRun{}, false
		}
		if (s.Layer != "" && !testreport.ValidLayer(s.Layer)) || (s.BudgetSeconds != nil && (!testreport.ValidBudget(s.BudgetSeconds) || s.Layer == "")) {
			return performanceRun{}, false
		}
		seen[s.Suite] = true
		suite := performanceSuite{Layer: s.Layer, BudgetSeconds: s.BudgetSeconds, Suite: s.Suite, Seconds: s.Seconds, Outcome: s.Outcome, Slowest: []testtiming.TestTiming{}}
		if shape.Suites[i].Tests != nil {
			c := s.Tests.Counts
			if c.Pass < 0 || c.Fail < 0 || c.Skip < 0 {
				return performanceRun{}, false
			}
			if counts := shape.Suites[i].Tests.Counts; counts != nil && counts.Pass != nil && counts.Fail != nil && counts.Skip != nil {
				suite.Counts = &c
			}
			sort.SliceStable(s.Tests.Slowest, func(i, j int) bool { return s.Tests.Slowest[i].Seconds > s.Tests.Slowest[j].Seconds })
			if len(s.Tests.Slowest) > 50 {
				s.Tests.Slowest = s.Tests.Slowest[:50]
			}
			for _, t := range s.Tests.Slowest {
				if !performanceText(t.Test, 512) || len(t.Package) > 256 || strings.HasPrefix(t.Package, "/") || strings.Contains(t.Package, "\\") || (t.Package != "" && !performanceText(t.Package, 256)) || !validGateSeconds(&t.Seconds) || (t.Result != "pass" && t.Result != "fail" && t.Result != "skip") {
					return performanceRun{}, false
				}
				suite.Slowest = append(suite.Slowest, t)
			}
		}
		if s.Tests.FlakeReruns != nil {
			evidence := *s.Tests.FlakeReruns
			if !testreport.ValidFlakeReruns(evidence) || (suite.Counts != nil && len(evidence) > suite.Counts.Total()) {
				return performanceRun{}, false
			}
			count := len(evidence)
			suite.Flakes = &count
			suite.FlakeReruns = evidence
		}
		out.Suites = append(out.Suites, suite)
	}
	return out, true
}

// A member is selected on both the reporting session and its workspace root.
// Legacy scalar repositories have precisely one member, never an inferred path.
func (a *API) performanceMember(ctx context.Context, sid, root, member string) (bool, error) {
	seen := map[string]bool{}
	for _, id := range []string{sid, root} {
		if seen[id] {
			continue
		}
		seen[id] = true
		w, err := db.GetSessionWorkspace(ctx, a.dbPool, id)
		if err != nil {
			return false, err
		}
		if w == nil {
			return false, nil
		}
		if w.Manifest == nil {
			if member != "repository" {
				return false, nil
			}
			continue
		}
		found := false
		for _, m := range w.Manifest.Members {
			if m.ID == member {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

func (a *API) performanceRoot(ctx context.Context, account, id string) (*db.SessionRow, error) {
	if !validSessionID(id) {
		return nil, nil
	}
	seen := map[string]bool{}
	for len(seen) < 100 {
		if seen[id] {
			return nil, nil
		}
		seen[id] = true
		row, err := db.GetSession(ctx, a.dbPool, id)
		if err != nil {
			return nil, err
		}
		if row == nil || !ownsSession(account, row) || !canSeeAccount(account, row) {
			return nil, nil
		}
		if row.ParentSessionID == nil {
			return row, nil
		}
		id = *row.ParentSessionID
	}
	return nil, nil
}

func performanceFailure(stage string, err error) {
	cause := failurelog.Cause(err)
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) && len(postgres.Code) == 5 {
		valid := true
		for _, r := range postgres.Code {
			if (r < '0' || r > '9') && (r < 'A' || r > 'Z') {
				valid = false
			}
		}
		if valid {
			cause = "sqlstate_" + postgres.Code
		}
	}
	failurelog.Default.Event("test_performance:"+stage, slog.LevelWarn, "component", "runner", "operation", "test_performance", "stage", stage, "cause", cause)
}

func (a *API) HandleImportTestRun(w http.ResponseWriter, r *http.Request) {
	sid, raw, ok := a.agentSessionWrite(w, r, "test history", 256<<10)
	if !ok {
		return
	}
	var input struct {
		MemberID       string          `json:"member_id"`
		Revision       int64           `json:"revision"`
		Source         string          `json:"source"`
		Region         *string         `json:"region"`
		CachedPackages *int            `json:"cached_packages"`
		Record         json.RawMessage `json:"record"`
	}
	if json.Unmarshal(raw, &input) != nil || !performanceText(input.MemberID, 128) {
		performanceFailure("test_import_invalid", nil)
		writeError(w, 400, "Invalid test history.")
		return
	}
	if input.Source == "" {
		input.Source = "gate"
	}
	if (input.Source != "gate" && input.Source != "custom") || (input.Region != nil && (input.Source != "custom" || !testreport.ValidRegion(*input.Region))) {
		performanceFailure("test_metadata_invalid", nil)
		writeError(w, 400, "Invalid test report metadata.")
		return
	}
	if input.CachedPackages != nil && (input.Source != "custom" || *input.CachedPackages < 0 || *input.CachedPackages > 100000) {
		performanceFailure("test_cache_metadata_invalid", nil)
		writeError(w, 400, "Invalid test report metadata.")
		return
	}
	source, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil || source == nil {
		performanceFailure("test_session_unavailable", err)
		writeError(w, 503, "Test history is unavailable.")
		return
	}
	root, err := a.performanceRoot(r.Context(), derefOrEmpty(source.SpawningAccountID), sid)
	if err != nil {
		performanceFailure("test_workspace_unavailable", err)
		writeError(w, 503, "Test history is unavailable.")
		return
	}
	if root == nil {
		performanceFailure("test_member_refused", nil)
		writeError(w, 400, "Select a project in this workspace.")
		return
	}
	selected, err := a.performanceMember(r.Context(), sid, root.ID, input.MemberID)
	if err != nil {
		performanceFailure("test_member_unavailable", err)
		writeError(w, 503, "Test history is unavailable.")
		return
	}
	if !selected {
		performanceFailure("test_member_refused", nil)
		writeError(w, 400, "Select a project in this workspace.")
		return
	}
	run, ok := projectGateRecord(input.Record, input.MemberID, input.Revision, time.Now().UTC())
	if !ok {
		performanceFailure("test_record_invalid", nil)
		writeError(w, 400, "Invalid test run or run outside the 180-day retention window.")
		return
	}
	if input.CachedPackages != nil && *input.CachedPackages > 0 && run.Complete {
		performanceFailure("test_cache_completeness_invalid", nil)
		writeError(w, 400, "Invalid test report metadata.")
		return
	}
	run.WorkspaceID = root.ID
	run.Source = input.Source
	run.Region = input.Region
	run.CachedPackages = input.CachedPackages
	report, _ := json.Marshal(run)
	tx, err := a.dbPool.Begin(r.Context())
	if err != nil {
		performanceFailure("test_transaction_unavailable", err)
		writeError(w, 503, "Test history is unavailable.")
		return
	}
	defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,402))`, root.ID+":"+input.MemberID); err != nil {
		performanceFailure("test_lock_unavailable", err)
		writeError(w, 503, "Test history is unavailable.")
		return
	}
	tag, err := tx.Exec(r.Context(), `INSERT INTO test_runs(workspace_id,member_id,session_id,run_id,revision,finished_at,report) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(workspace_id,member_id,run_id) DO UPDATE SET revision=excluded.revision,report=excluded.report,finished_at=excluded.finished_at,observed_at=clock_timestamp() WHERE test_runs.revision<excluded.revision`, root.ID, input.MemberID, sid, run.RunID, run.Revision, run.FinishedAt, report)
	if err != nil {
		performanceFailure("test_store_unavailable", err)
		writeError(w, 503, "Test history is unavailable.")
		return
	}
	status := 201
	superseded := false
	if tag.RowsAffected() == 0 {
		var same bool
		var storedRevision int64
		if err = tx.QueryRow(r.Context(), `SELECT revision,revision=$4 AND report=$5::jsonb FROM test_runs WHERE workspace_id=$1 AND member_id=$2 AND run_id=$3`, root.ID, input.MemberID, run.RunID, run.Revision, report).Scan(&storedRevision, &same); err != nil {
			performanceFailure("test_retry_unavailable", err)
			writeError(w, 503, "Test history is unavailable.")
			return
		}
		superseded = storedRevision > run.Revision
		if !same && !superseded {
			performanceFailure("test_revision_conflict", nil)
			writeError(w, 409, "Test run revision conflicts with stored history.")
			return
		}
		status = 200
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM test_runs WHERE workspace_id=$1 AND member_id=$2 AND (finished_at<clock_timestamp()-interval '180 days' OR run_id IN (SELECT run_id FROM test_runs WHERE workspace_id=$1 AND member_id=$2 ORDER BY finished_at DESC,run_id OFFSET 2000))`, root.ID, input.MemberID); err != nil {
		performanceFailure("test_retention_unavailable", err)
		writeError(w, 503, "Test history is unavailable.")
		return
	}
	var retained bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM test_runs WHERE workspace_id=$1 AND member_id=$2 AND run_id=$3)`, root.ID, input.MemberID, run.RunID).Scan(&retained); err != nil {
		performanceFailure("test_receipt_unavailable", err)
		writeError(w, 503, "Test history is unavailable.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		performanceFailure("test_commit_unavailable", err)
		writeError(w, 503, "Test history is unavailable.")
		return
	}
	receiptStatus := "retained"
	if !retained {
		receiptStatus = "not_retained"
	}
	writeJSON(w, status, map[string]any{"ok": true, "superseded": superseded, "status": receiptStatus})
}
