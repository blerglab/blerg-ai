package api

import (
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/board/internal/auth"
)

// Boards overview: the tidbits that make the index worth looking at —
// per-board column counts, whether an agent is working right now, tokens
// today, and a 7-day token sparkline.

type boardOverview struct {
	BoardID       string  `json:"board_id"`
	Inbox         int     `json:"inbox"`
	Ready         int     `json:"ready"`
	InProgress    int     `json:"in_progress"`
	Review        int     `json:"review"`
	Working       int     `json:"working"` // sessions active right now
	TokensToday   int64   `json:"tokens_today"`
	TokenWeek     []int64 `json:"token_week"` // 7 entries, oldest first
	SessionsWeek  int     `json:"sessions_week"`
	AgentSecsWeek int64   `json:"agent_secs_week"`
	ActivityWeek  []int64 `json:"activity_week"` // agent seconds per day, oldest first
}

// handleActiveSessionsGlobal: the fleet, for the root page — every live
// session with its board, role, card, and timing.
func (a *API) handleActiveSessionsGlobal(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := p.RequireGlobalRead(); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	rows, err := a.Pool.Query(r.Context(), `
		SELECT rs.id, rs.board_id, b.name, rs.role, rs.lifecycle, rs.external_session_id,
		       to_char(rs.created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       to_char(COALESCE(rs.last_activity_at, rs.created_at), 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       c.number, c.title
		FROM runner_sessions rs
		JOIN boards b ON b.id = rs.board_id
		LEFT JOIN cards c ON c.id = rs.card_id
		WHERE rs.lifecycle NOT IN ('stopped','error')
		ORDER BY rs.created_at DESC`)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	type sessRow struct {
		ID         string  `json:"id"`
		BoardID    string  `json:"board_id"`
		BoardName  string  `json:"board_name"`
		Role       string  `json:"role"`
		Lifecycle  string  `json:"lifecycle"`
		ExternalID string  `json:"external_session_id"`
		CreatedAt  string  `json:"created_at"`
		LastActive string  `json:"last_active_at"`
		CardNumber *int    `json:"card_number"`
		CardTitle  *string `json:"card_title"`
	}
	out := []sessRow{}
	for rows.Next() {
		var sr sessRow
		if rows.Scan(&sr.ID, &sr.BoardID, &sr.BoardName, &sr.Role, &sr.Lifecycle, &sr.ExternalID,
			&sr.CreatedAt, &sr.LastActive, &sr.CardNumber, &sr.CardTitle) != nil {
			continue
		}
		out = append(out, sr)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleBoardsOverview(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := p.RequireGlobalRead(); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	ctx := r.Context()
	out := map[string]*boardOverview{}
	get := func(id string) *boardOverview {
		if out[id] == nil {
			out[id] = &boardOverview{BoardID: id, TokenWeek: make([]int64, 7), ActivityWeek: make([]int64, 7)}
		}
		return out[id]
	}

	// column counts, classified by name
	rows, err := a.Pool.Query(ctx, `
		SELECT c.board_id, bc.name, count(*)
		FROM cards c JOIN board_columns bc ON bc.id = c.column_id
		WHERE c.archived_at IS NULL
		GROUP BY c.board_id, bc.name`)
	if err == nil {
		for rows.Next() {
			var boardID, name string
			var n int
			if rows.Scan(&boardID, &name, &n) != nil {
				continue
			}
			o := get(boardID)
			ln := strings.ToLower(name)
			switch {
			case strings.Contains(ln, "inbox"):
				o.Inbox += n
			case isReadyColumn(ln):
				o.Ready += n
			case isWorkColumn(ln):
				o.InProgress += n
			case strings.Contains(ln, "review"):
				o.Review += n
			}
		}
		rows.Close()
	}

	// working now: sessions in an active lifecycle with recent signs of life
	rows, err = a.Pool.Query(ctx, `
		SELECT board_id, count(*) FROM runner_sessions
		WHERE lifecycle IN ('starting','running','waiting')
		  AND (last_activity_at > now() - interval '10 minutes'
		       OR created_at > now() - interval '10 minutes')
		GROUP BY board_id`)
	if err == nil {
		for rows.Next() {
			var boardID string
			var n int
			if rows.Scan(&boardID, &n) == nil {
				get(boardID).Working = n
			}
		}
		rows.Close()
	}

	// tokens per day, last 7 days (usage payloads on turn_done events)
	rows, err = a.Pool.Query(ctx, `
		SELECT rs.board_id,
		       (EXTRACT(EPOCH FROM (date_trunc('day', re.ts) - date_trunc('day', now() - interval '6 days'))) / 86400)::int,
		       SUM(COALESCE((re.payload->'usage'->>'input_tokens')::bigint, 0)
		           + COALESCE((re.payload->'usage'->>'output_tokens')::bigint, 0))
		FROM runner_events re
		JOIN runner_sessions rs ON rs.id = re.runner_session_id
		WHERE re.ts > date_trunc('day', now() - interval '6 days')
		  AND re.payload ? 'usage'
		GROUP BY 1, 2`)
	if err == nil {
		for rows.Next() {
			var boardID string
			var idx int
			var tokens int64
			if rows.Scan(&boardID, &idx, &tokens) != nil {
				continue
			}
			o := get(boardID)
			i := idx
			if i >= 0 && i < 7 {
				o.TokenWeek[i] = tokens
				if i == 6 {
					o.TokensToday = tokens
				}
			}
		}
		rows.Close()
	}

	// last-7-day session activity: count + per-day agent seconds
	rows, err = a.Pool.Query(ctx, `
		SELECT board_id,
		       (EXTRACT(EPOCH FROM (day - date_trunc('day', now() - interval '6 days'))) / 86400)::int,
		       count(*), COALESCE(sum(secs), 0)
		FROM (
			SELECT rs.board_id, rs.id, date_trunc('day', rs.created_at) AS day,
			       COALESCE(EXTRACT(EPOCH FROM (max(re.ts) - min(re.ts))), 0)::bigint AS secs
			FROM runner_sessions rs
			LEFT JOIN runner_events re ON re.runner_session_id = rs.id
			WHERE rs.created_at > date_trunc('day', now() - interval '6 days')
			GROUP BY rs.board_id, rs.id
		) s GROUP BY 1, 2`)
	if err == nil {
		for rows.Next() {
			var boardID string
			var idx int
			var n int
			var secs int64
			if rows.Scan(&boardID, &idx, &n, &secs) != nil {
				continue
			}
			o := get(boardID)
			o.SessionsWeek += n
			o.AgentSecsWeek += secs
			if i := idx; i >= 0 && i < 7 {
				o.ActivityWeek[i] += secs
			}
		}
		rows.Close()
	}

	list := make([]*boardOverview, 0, len(out))
	for _, o := range out {
		list = append(list, o)
	}
	writeJSON(w, http.StatusOK, list)
}
