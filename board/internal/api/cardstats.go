package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// Card stats: what the work cost. Wall clock (first pickup → done), agent
// time (summed session event spans, split by role), gate time (admission
// review latency by backend — OpenAI-compatible vs Claude), and token usage (summed from
// turn_done payloads the runner emits).

type gateStat struct {
	Backend string `json:"backend"`
	Calls   int    `json:"calls"`
	MS      int64  `json:"ms"`
}

func (a *API) handleCardStats(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	ctx := r.Context()
	out := map[string]any{}

	// wall clock: first move into a work column → latest move into done
	cols, _ := db.ListColumns(ctx, a.Pool, card.BoardID)
	colByID := map[string]string{}
	for _, c := range cols {
		colByID[c.ID] = strings.ToLower(c.Name)
	}
	events, _ := db.ListCardEvents(ctx, a.Pool, card.ID, 0, 1000)
	var startedAt, doneAt *time.Time
	for _, e := range events {
		if e.Type != "moved" || e.ToColumnID == nil {
			continue
		}
		name := colByID[*e.ToColumnID]
		t, err := time.Parse(time.RFC3339, e.CreatedAt)
		if err != nil {
			continue
		}
		if isWorkColumn(name) && startedAt == nil {
			tt := t
			startedAt = &tt
		}
		if strings.Contains(name, "done") || isTerminalCol(cols, *e.ToColumnID) {
			tt := t
			doneAt = &tt
		}
	}
	if startedAt != nil {
		out["started_at"] = startedAt
		if doneAt != nil && doneAt.After(*startedAt) {
			out["wall_seconds"] = int64(doneAt.Sub(*startedAt).Seconds())
		}
	}

	// agent time by role: summed event span per session
	rows, err := a.Pool.Query(ctx, `
		SELECT rs.role, EXTRACT(EPOCH FROM (max(re.ts) - min(re.ts)))::bigint
		FROM runner_sessions rs
		JOIN runner_events re ON re.runner_session_id = rs.id
		WHERE rs.card_id = $1
		GROUP BY rs.id, rs.role`, card.ID)
	if err == nil {
		agentSecs := map[string]int64{}
		var sessions int
		for rows.Next() {
			var role string
			var secs int64
			if rows.Scan(&role, &secs) == nil {
				agentSecs[role] += secs
				sessions++
			}
		}
		rows.Close()
		out["agent_seconds"] = agentSecs
		out["sessions"] = sessions
	}

	// tokens: turn_done payloads carry usage (mapped events flat, legacy wrapped)
	var inTok, outTok, cacheTok int64
	_ = a.Pool.QueryRow(ctx, `
		SELECT
		  COALESCE(SUM(COALESCE((re.payload->'usage'->>'input_tokens')::bigint,
		                        (re.payload->'payload'->'usage'->>'input_tokens')::bigint, 0)), 0),
		  COALESCE(SUM(COALESCE((re.payload->'usage'->>'output_tokens')::bigint,
		                        (re.payload->'payload'->'usage'->>'output_tokens')::bigint, 0)), 0),
		  COALESCE(SUM(COALESCE((re.payload->'usage'->>'cache_read_tokens')::bigint,
		                        (re.payload->'payload'->'usage'->>'cache_read_tokens')::bigint, 0)), 0)
		FROM runner_events re
		JOIN runner_sessions rs ON rs.id = re.runner_session_id
		WHERE rs.card_id = $1
		  AND (re.payload ? 'usage' OR re.payload->'payload' ? 'usage')`,
		card.ID).Scan(&inTok, &outTok, &cacheTok)
	out["tokens"] = map[string]int64{"input": inTok, "output": outTok, "cache_read": cacheTok}

	// model: which Claude model each role ran, from turn_done payloads
	// (mapped events flat, legacy wrapped). Latest wins if it ever changed
	// mid-session.
	mrows, err := a.Pool.Query(ctx, `
		SELECT rs.role, COALESCE(re.payload->>'model', re.payload->'payload'->>'model')
		FROM runner_sessions rs
		JOIN runner_events re ON re.runner_session_id = rs.id
		WHERE rs.card_id = $1
		  AND (re.payload ? 'model' OR re.payload->'payload' ? 'model')
		ORDER BY re.ts DESC`, card.ID)
	if err == nil {
		models := map[string]string{}
		for mrows.Next() {
			var role, model string
			if mrows.Scan(&role, &model) == nil {
				if _, seen := models[role]; !seen {
					models[role] = model
				}
			}
		}
		mrows.Close()
		if len(models) > 0 {
			out["models"] = models
		}
	}

	// gate: admission review latency by backend (openai vs claude).
	grows, err := a.Pool.Query(ctx, `
		SELECT COALESCE(backend, 'unknown'),
		       count(*), COALESCE(sum(latency_ms), 0)
		FROM admission_reviews WHERE card_id = $1 GROUP BY 1`, card.ID)
	if err == nil {
		var gates []gateStat
		for grows.Next() {
			var g gateStat
			if grows.Scan(&g.Backend, &g.Calls, &g.MS) == nil {
				gates = append(gates, g)
			}
		}
		grows.Close()
		out["gate"] = gates
	}
	writeJSON(w, http.StatusOK, out)
}

func isTerminalCol(cols []db.Column, id string) bool {
	for _, c := range cols {
		if c.ID == id {
			return c.IsTerminal
		}
	}
	return false
}
