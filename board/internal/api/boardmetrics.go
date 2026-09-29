package api

import (
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// Board metrics: the board's life in numbers, aggregated straight from the
// tables that already record it (sessions, events, admission reviews, card
// events). Postgres GROUP BY day is the whole "metrics pipeline" — a rollup
// table can replace these scans if they ever get slow.

type dayRow struct {
	Day       string `json:"day"`
	Role      string `json:"role,omitempty"`
	Backend   string `json:"backend,omitempty"`
	Model     string `json:"model,omitempty"`
	Count     int64  `json:"count"`
	Seconds   int64  `json:"seconds,omitempty"`
	TokensIn  int64  `json:"tokens_in,omitempty"`
	TokensOut int64  `json:"tokens_out,omitempty"`
	AvgMS     int64  `json:"avg_ms,omitempty"`
}

func (a *API) handleBoardMetrics(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	ctx := r.Context()
	out := map[string]any{}

	// sessions per day by role: count, active seconds, tokens
	sessions := []dayRow{}
	rows, err := a.Pool.Query(ctx, `
		SELECT to_char(day, 'YYYY-MM-DD'), role, count(*),
		       COALESCE(sum(secs), 0), COALESCE(sum(tok_in), 0), COALESCE(sum(tok_out), 0)
		FROM (
			SELECT rs.id, rs.role, date_trunc('day', rs.created_at) AS day,
			       EXTRACT(EPOCH FROM (max(re.ts) - min(re.ts)))::bigint AS secs,
			       SUM(COALESCE((re.payload->'usage'->>'input_tokens')::bigint, 0)
			           + COALESCE((re.payload->'usage'->>'cache_read_tokens')::bigint, 0)) AS tok_in,
			       SUM(COALESCE((re.payload->'usage'->>'output_tokens')::bigint, 0)) AS tok_out
			FROM runner_sessions rs
			LEFT JOIN runner_events re ON re.runner_session_id = rs.id
			WHERE rs.board_id = $1
			GROUP BY rs.id
		) s
		GROUP BY day, role ORDER BY day`, boardID)
	if err == nil {
		for rows.Next() {
			var d dayRow
			if rows.Scan(&d.Day, &d.Role, &d.Count, &d.Seconds, &d.TokensIn, &d.TokensOut) == nil {
				sessions = append(sessions, d)
			}
		}
		rows.Close()
	}
	out["sessions"] = sessions

	// tokens per day by model (the board's model/reviewer_model, surfaced
	// per session from its turn_done payloads): a session's model is its
	// latest turn_done, since a board could change model mid-flight.
	tokensByModel := []dayRow{}
	rows, err = a.Pool.Query(ctx, `
		SELECT to_char(day, 'YYYY-MM-DD'), model, COALESCE(sum(tok_in), 0), COALESCE(sum(tok_out), 0)
		FROM (
			SELECT rs.id, date_trunc('day', rs.created_at) AS day,
			       (SELECT COALESCE(re2.payload->>'model', re2.payload->'payload'->>'model')
			        FROM runner_events re2
			        WHERE re2.runner_session_id = rs.id
			          AND (re2.payload ? 'model' OR re2.payload->'payload' ? 'model')
			        ORDER BY re2.ts DESC LIMIT 1) AS model,
			       SUM(COALESCE((re.payload->'usage'->>'input_tokens')::bigint, 0)
			           + COALESCE((re.payload->'usage'->>'cache_read_tokens')::bigint, 0)) AS tok_in,
			       SUM(COALESCE((re.payload->'usage'->>'output_tokens')::bigint, 0)) AS tok_out
			FROM runner_sessions rs
			LEFT JOIN runner_events re ON re.runner_session_id = rs.id
			WHERE rs.board_id = $1
			GROUP BY rs.id
		) s
		WHERE model IS NOT NULL AND (tok_in > 0 OR tok_out > 0)
		GROUP BY day, model ORDER BY day`, boardID)
	if err == nil {
		for rows.Next() {
			var d dayRow
			if rows.Scan(&d.Day, &d.Model, &d.TokensIn, &d.TokensOut) == nil {
				tokensByModel = append(tokensByModel, d)
			}
		}
		rows.Close()
	}
	out["tokens_by_model"] = tokensByModel

	// cards completed per day (moved into a terminal/done column)
	completed := []dayRow{}
	rows, err = a.Pool.Query(ctx, `
		SELECT to_char(date_trunc('day', ce.created_at), 'YYYY-MM-DD'), count(DISTINCT ce.card_id)
		FROM card_events ce
		JOIN cards c ON c.id = ce.card_id
		JOIN board_columns bc ON bc.id = ce.to_column_id
		WHERE c.board_id = $1 AND ce.type = 'moved'
		  AND `+db.TerminalColumnSQL("bc")+`
		GROUP BY 1 ORDER BY 1`, boardID)
	if err == nil {
		for rows.Next() {
			var d dayRow
			if rows.Scan(&d.Day, &d.Count) == nil {
				completed = append(completed, d)
			}
		}
		rows.Close()
	}
	out["cards_completed"] = completed

	// gate calls per day by backend, with avg latency.
	gate := []dayRow{}
	rows, err = a.Pool.Query(ctx, `
		SELECT to_char(date_trunc('day', submitted_at), 'YYYY-MM-DD'),
		       COALESCE(backend, 'unknown'),
		       count(*), COALESCE(avg(latency_ms), 0)::bigint
		FROM admission_reviews WHERE board_id = $1
		GROUP BY 1, 2 ORDER BY 1`, boardID)
	if err == nil {
		for rows.Next() {
			var d dayRow
			if rows.Scan(&d.Day, &d.Backend, &d.Count, &d.AvgMS) == nil {
				gate = append(gate, d)
			}
		}
		rows.Close()
	}
	out["gate"] = gate

	// rollups
	roll := map[string]any{}
	var totalSessions, totalSecs, totalIn, totalOut int64
	for _, s := range sessions {
		totalSessions += s.Count
		totalSecs += s.Seconds
		totalIn += s.TokensIn
		totalOut += s.TokensOut
	}
	roll["sessions"] = totalSessions
	roll["agent_seconds"] = totalSecs
	roll["tokens_in"] = totalIn
	roll["tokens_out"] = totalOut
	var done, gateCalls, merges int64
	for _, d := range completed {
		done += d.Count
	}
	for _, g := range gate {
		gateCalls += g.Count
	}
	_ = a.Pool.QueryRow(ctx, `
		SELECT count(*) FROM card_events ce JOIN cards c ON c.id = ce.card_id
		WHERE c.board_id = $1 AND ce.type = 'comment'
		  AND (ce.data->>'text' LIKE 'Auto-merged%' OR ce.data->>'text' LIKE 'Accepted by human review — merged%')`,
		boardID).Scan(&merges)
	roll["cards_completed"] = done
	roll["gate_calls"] = gateCalls
	roll["merges"] = merges
	var firstSession *time.Time
	_ = a.Pool.QueryRow(ctx,
		`SELECT min(created_at) FROM runner_sessions WHERE board_id = $1`, boardID).Scan(&firstSession)
	roll["since"] = firstSession
	out["rollup"] = roll

	writeJSON(w, http.StatusOK, out)
}
