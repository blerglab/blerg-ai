package server

import (
	"context"
	"encoding/json"
	"log"
	"sort"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// supplementReplayContext adds lifecycle metadata only for children a page
// references. Keep pagination based on the original page, never these older
// rows: a sparse tail must not pretend the intervening transcript was loaded.
func supplementReplayContext(ctx context.Context, pool *pgxpool.Pool, sessionID string, page []db.AgentEventRow) []db.AgentEventRow {
	if len(page) == 0 {
		return page
	}
	ids := make([]string, 0, len(page))
	seenIDs := make(map[string]bool)
	for _, row := range page {
		var p struct {
			ChildID      string `json:"child_id"`
			ParentCallID string `json:"parent_call_id"`
		}
		if json.Unmarshal([]byte(row.Payload), &p) != nil {
			continue
		}
		id := p.ParentCallID
		switch row.Kind {
		case "subagent_started", "subagent_update", "subagent_done":
			id = p.ChildID
		}
		if id == "" || len(id) > 512 || seenIDs[id] {
			continue
		}
		seenIDs[id] = true
		ids = append(ids, id)
	}
	contextRows, err := db.ListAgentSubagentContext(ctx, pool, sessionID, ids, page[len(page)-1].Seq)
	if err != nil {
		log.Printf("agent replay context %s: %v", sessionID, err)
		return page
	}
	seenSeqs := make(map[int64]bool, len(page)+len(contextRows))
	for _, row := range page {
		seenSeqs[row.Seq] = true
	}
	for _, row := range contextRows {
		if !seenSeqs[row.Seq] {
			page = append(page, row)
			seenSeqs[row.Seq] = true
		}
	}
	sort.Slice(page, func(i, j int) bool { return page[i].Seq < page[j].Seq })
	return page
}
