package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
)

// HandleWorkspaceMetrics uses Main's own credential or its human owner. A
// child's token cannot inspect siblings, and a desktop token grants no access.
func (a *API) HandleWorkspaceMetrics(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, 503, noDatabaseMsg)
		return
	}
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		writeError(w, 404, "session not found")
		return
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "read metrics", "metrics", err)
		return
	}
	if p, ok := a.verifyBrowserToken(a.rawBearer(r)); ok {
		if p.Kind != "human" || !ownsSession(p.Sub, row) {
			writeError(w, 404, "session not found")
			return
		}
	} else {
		tokenSession, ok := a.authMessaging(w, r)
		if !ok {
			return
		}
		if tokenSession != sid {
			writeError(w, 403, "metrics need the session's own credential")
			return
		}
	}
	if row == nil {
		writeError(w, 404, "session not found")
		return
	}
	if r.Method == http.MethodPost {
		var run activity.Run
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&run) != nil || !metricRef.MatchString(run.ID) || !metricRef.MatchString(run.Suite) || !metricRef.MatchString(run.Commit) || run.Progress < 0 || len(run.Card) > 128 || (run.State != "running" && run.State != "finished" && run.State != "failed" && run.State != "stopped") {

			writeError(w, 422, "name a run, suite, commit, nonnegative progress, and valid state")
			return
		}
		now := time.Now().UTC()
		if run.StartedAt.After(now.Add(time.Minute)) || (run.EndedAt != nil && (run.EndedAt.After(now.Add(time.Minute)) || run.EndedAt.Before(run.StartedAt))) {
			writeError(w, 422, "invalid run interval")
			return
		}
		if run.Card != "" {
			board, num, ok := strings.Cut(run.Card, "#")
			n, err := strconv.Atoi(num)
			if !ok || !metricRef.MatchString(board) || err != nil || n < 1 || n > 10000000 {
				writeError(w, 422, "card must be BOARD#N")
				return
			}
		}
		if err := db.RegisterMetricRun(r.Context(), a.dbPool, sid, run); err != nil {
			if errors.Is(err, db.ErrMetricRunLimit) {
				writeError(w, 429, err.Error())
				return
			}
			writeDatabaseFailure(w, "register run", "metrics run", err)
			return
		}
		writeJSON(w, 200, map[string]string{"session_id": sid, "run_id": run.ID})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	metricsCtx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	metrics, err := db.WorkspaceMetrics(metricsCtx, a.dbPool, sid, time.Now().UTC())
	if err != nil {
		writeDatabaseFailure(w, "read metrics", "workspace metrics", err)
		return
	}
	if card := r.URL.Query().Get("card"); card != "" {
		board, num, ok := strings.Cut(card, "#")
		n, parseErr := strconv.Atoi(num)
		if !ok || !metricRef.MatchString(board) || parseErr != nil || n < 1 || n > 10000000 {
			writeError(w, 422, "card must be BOARD#N")
			return
		}
		raw, err := db.CardData(r.Context(), a.dbPool, derefOrEmpty(row.SpawningAccountID), board, n)
		if err != nil {
			writeDatabaseFailure(w, "read card metrics", "card metrics", err)
			return
		}
		var linked struct {
			Sessions []struct {
				ID string `json:"id"`
			} `json:"sessions"`
		}
		if err = json.Unmarshal(raw, &linked); err != nil {
			writeError(w, 500, "could not read card metrics")
			return
		}
		ids := map[string]bool{}
		for _, s := range linked.Sessions {
			ids[s.ID] = true
		}
		filtered := metrics.Workers[:0]
		for _, worker := range metrics.Workers {
			if ids[worker.SessionID] {
				filtered = append(filtered, worker)
			}
		}
		metrics.Workers = filtered
		runs := metrics.Runs[:0]
		for _, run := range metrics.Runs {
			if ids[run.SessionID] || run.Card == card {
				runs = append(runs, run)
			}
		}
		metrics.Runs = runs
	}
	if session := r.URL.Query().Get("session"); session != "" {
		found := false
		for _, worker := range metrics.Workers {
			if worker.SessionID == session {
				metrics.Workers = []activity.Worker{worker}
				found = true
				break
			}
		}
		if !found {
			writeError(w, 404, "session not in this workspace")
			return
		}
		runs := metrics.Runs[:0]
		for _, run := range metrics.Runs {
			if run.SessionID == session {
				runs = append(runs, run)
			}
		}
		metrics.Runs = runs
		notices := metrics.Undelivered[:0]
		for _, notice := range metrics.Undelivered {
			if notice.SessionID == session {
				notices = append(notices, notice)
			}
		}
		metrics.Undelivered = notices
	}
	if raw := r.URL.Query().Get("turn"); raw != "" {
		seq, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || seq < 0 {
			writeError(w, 422, "turn must be an event sequence")
			return
		}
		if len(metrics.Workers) != 1 {
			writeError(w, 422, "select a session for a turn")
			return
		}
		if seq > 0 {
			turn, err := db.StoredTurnMetric(metricsCtx, a.dbPool, metrics.Workers[0].SessionID, seq)
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, 404, "turn not found")
				return
			}
			if err != nil {
				writeDatabaseFailure(w, "read turn metrics", "turn metrics", err)
				return
			}
			writeJSON(w, 200, map[string]any{"session_id": metrics.Workers[0].SessionID, "turn": turn})
			return
		}
		turns, err := db.SessionTurns(r.Context(), a.dbPool, metrics.Workers[0].SessionID, time.Now().UTC())
		if err != nil {
			writeDatabaseFailure(w, "read turn metrics", "turn metrics", err)
			return
		}
		for _, turn := range turns {
			if turn.Seq == seq {
				writeJSON(w, 200, map[string]any{"session_id": metrics.Workers[0].SessionID, "turn": turn})
				return
			}
		}
		writeError(w, 404, "turn not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("format") == "table" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		//nolint:gosec // Explicit text/plain response contains owned counters and validated registration metadata.
		_, _ = w.Write([]byte(metrics.Table()))
		return
	}
	writeJSON(w, 200, metrics)
}

var metricRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

func (a *API) telemetryPlanFacts(ctx context.Context, row *db.SessionRow, seq int64) ([]planFact, error) {
	if row.ParentSessionID != nil {
		return nil, nil
	}
	now := time.Now().UTC()
	metrics, err := db.WorkspaceMetrics(ctx, a.dbPool, row.ID, now, 1)
	if err != nil {
		return nil, err
	}
	facts := []planFact{}
	if snapshot := metrics.Snapshot(); snapshot != "" {
		facts = append(facts, planFact{key: fmt.Sprintf("telemetry:snapshot:%d", seq), text: snapshot})
	}
	for _, n := range metrics.Nudges(now) {
		if len(facts) >= 4 {
			break
		}
		facts = append(facts, planFact{key: n.Key, text: n.Text})
	}
	return facts, nil
}

// Each turn has an independent small telemetry budget. Errors are deliberately
// isolated from plan checks and the carrying input's delivery path.
func (a *API) queueTelemetryTurn(id string, seq int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := db.SaveTurnMetrics(ctx, a.dbPool, id, time.Now()); err != nil {
		return
	}
	row, err := db.GetSession(ctx, a.dbPool, id)
	if err != nil || row == nil || row.Kind != "agent" || row.ParentSessionID != nil {
		return
	}
	on, err := db.PlanNudgesOn(ctx, a.dbPool, id)
	if err != nil || !on {
		return
	}
	facts, err := a.telemetryPlanFacts(ctx, row, seq)
	if err != nil {
		return
	}
	_, _ = a.dbPool.Exec(ctx, `DELETE FROM session_plan_nudges WHERE session_id=$1 AND key LIKE 'telemetry:snapshot:%' AND key<>$2 AND nudged_at IS NULL`, id, fmt.Sprintf("telemetry:snapshot:%d", seq))
	for _, fact := range facts {
		if err := db.AddPlanFact(ctx, a.dbPool, id, fact.key, fact.text); err != nil {
			return
		}
	}
}
