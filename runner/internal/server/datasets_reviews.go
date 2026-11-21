package server

import (
	"context"
	"sort"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Marks are attributed claims. Author engine comes only from an owned,
// card-linked delivery matching the target SHA or prefix; ambiguity is unknown.
// For each target/reviewer the latest round wins; later corrections of that
// round replace it. Rebuild from records so retries and reopen never add totals.
func reviewPairDatasetRows(ctx context.Context, pool *pgxpool.Pool, account string, p map[string]string) ([]protocol.DatasetRow, time.Time, string) {
	now := time.Now().UTC()
	result, err := pool.Query(ctx, `WITH normalized AS (
 SELECT e.payload,e.ts,e.seq,lower(trim(coalesce(e.payload->>'target',''))) AS target,
 CASE lower(trim(coalesce(e.payload->>'by',''))) WHEN 'claude' THEN 'claude' WHEN 'codex' THEN 'codex' WHEN 'hermes' THEN 'hermes' ELSE 'unknown' END AS reviewer
 FROM agent_events e JOIN sessions s ON s.id=e.session_id WHERE s.spawning_account_id=$1 AND e.kind='mark' AND e.payload->>'kind'='review' AND e.payload->>'board_id'=$2 AND e.payload->>'card'=$3
 ), latest AS (
 SELECT DISTINCT ON(target,reviewer) payload,ts,seq,target,reviewer FROM normalized WHERE target ~ '^[0-9a-f]{7,40}$'
 ORDER BY target,reviewer,coalesce((payload->>'round')::int,0) DESC,ts DESC,seq DESC
 ) SELECT l.reviewer,l.payload->>'verdict',coalesce(l.payload->'findings','[]'::jsonb),
 ARRAY(SELECT DISTINCT coalesce(nullif(lower(trim(s.engine)),''),'unknown') FROM session_commit_deliveries d JOIN sessions s ON s.id=d.session_id
 WHERE s.spawning_account_id=$1 AND EXISTS(SELECT 1 FROM session_card_links c WHERE c.session_id=s.id AND c.board_id=$2 AND c.card_number::text=$3)
 AND EXISTS(SELECT 1 FROM jsonb_path_query(d.results,'$[*].summary.commits[*].sha') delivered(sha)
 WHERE jsonb_typeof(delivered.sha)='string' AND (delivered.sha #>> '{}') ~* '^[0-9a-f]{40}$'
 AND left(lower(delivered.sha #>> '{}'),length(l.target))=l.target))
 FROM latest l LIMIT 10001`, account, p["board"], p["card"])
	if err != nil {
		return nil, now, "reviews_unavailable"
	}
	defer result.Close()
	groups := map[string]protocol.DatasetRow{}
	count := 0
	for result.Next() {
		count++
		if count > 10000 {
			return nil, now, "row_limit"
		}
		var reviewer, verdict string
		var findings, authors []string
		if result.Scan(&reviewer, &verdict, &findings, &authors) != nil {
			return nil, now, "invalid_review_sample"
		}
		engine := func(e string) string {
			switch e {
			case "claude", "codex", "hermes":
				return e
			}
			return "unknown"
		}
		reviewer = engine(reviewer)
		author := "unknown"
		if len(authors) == 1 {
			author = engine(authors[0])
		}
		key := author + ":" + reviewer
		r := groups[key]
		if r == nil {
			r = protocol.DatasetRow{"key": key, "author_engine": author, "reviewer_engine": reviewer, "reviews": 0, "high": 0, "medium": 0, "low": 0, "approved": 0, "changes": 0, "rejected": 0}
			groups[key] = r
		}
		reviews, _ := r["reviews"].(int)
		r["reviews"] = reviews + 1
		switch verdict {
		case "approved", "changes", "rejected":
			count, _ := r[verdict].(int)
			r[verdict] = count + 1
		default:
			return nil, now, "invalid_review_sample"
		}
		for _, severity := range findings {
			switch severity {
			case "high", "medium", "low":
				count, _ := r[severity].(int)
				r[severity] = count + 1
			default:
				return nil, now, "invalid_review_sample"
			}
		}
	}
	if result.Err() != nil {
		return nil, now, "reviews_unavailable"
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([]protocol.DatasetRow, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, groups[k])
	}
	return rows, now, ""
}
