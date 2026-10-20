package server

import (
	"context"
	"errors"

	"github.com/blerglab/blerg-ai/runner/internal/checklist"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

var errChecklistWorker = errors.New("the worker is not part of this session")

// checklistWorker resolves only identifiers already recorded under this session.
// Native aliases become the canonical child ID; ambiguous aliases never guess.
func checklistWorker(ctx context.Context, q db.Querier, sid string, worker checklist.Assignee) (checklist.Assignee, error) {
	if !checklist.ValidAssignee(worker) {
		return checklist.Assignee{}, errChecklistWorker
	}
	if worker.Kind == "child_session" {
		if !validSessionID(worker.ID) {
			return checklist.Assignee{}, errChecklistWorker
		}
		var owned bool
		err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions child JOIN sessions parent ON parent.id=$1
    WHERE child.id=$2 AND child.parent_session_id=parent.id AND child.kind='agent'
    AND parent.spawning_account_id IS NOT NULL AND child.spawning_account_id=parent.spawning_account_id)`, sid, worker.ID).Scan(&owned)
		if err != nil {
			return checklist.Assignee{}, err
		}
		if !owned {
			return checklist.Assignee{}, errChecklistWorker
		}
		return worker, nil
	}
	resolved, err := checklistNativeWorkers(ctx, q, sid, []string{worker.ID}, 1<<62)
	if err != nil {
		return checklist.Assignee{}, err
	}
	id, ok := resolved[worker.ID]
	if !ok {
		return checklist.Assignee{}, errChecklistWorker
	}
	return checklist.Assignee{Kind: "subagent", ID: id}, nil
}

// Batch resolution is bounded by the current checklist plus one request. Prefer
// known child IDs over earlier call-only events; an ambiguous alias stays unknown.
func checklistNativeWorkers(ctx context.Context, q db.Querier, sid string, ids []string, through int64) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `WITH wanted AS (SELECT unnest($2::text[]) AS id), matches AS (
 SELECT wanted.id, NULLIF(payload->>'child_id','') AS child_id, NULLIF(payload->>'call_id','') AS call_id
 FROM agent_events JOIN wanted ON (payload->>'child_id'=wanted.id OR payload->>'call_id'=wanted.id)
 WHERE session_id=$1 AND seq<=$3 AND kind IN ('subagent_started','subagent_update','subagent_done')
 ), facts AS (
 SELECT id, bool_or(child_id IS NULL AND call_id=id) AS call_only,
 bool_or(child_id=id) AS direct_child,
 bool_or(call_id=id AND child_id IS NOT NULL) AS linked_child FROM matches GROUP BY id
 ), resolved AS (
 SELECT id, COALESCE(child_id,call_id) AS canonical FROM matches m
 WHERE child_id IS NOT NULL OR NOT EXISTS (SELECT 1 FROM matches other WHERE other.id=m.id AND other.child_id IS NOT NULL)
 ) SELECT r.id, min(canonical), count(DISTINCT canonical),
 COALESCE(f.call_only AND f.direct_child AND NOT f.linked_child,false)
 FROM resolved r JOIN facts f ON f.id=r.id
 GROUP BY r.id,f.call_only,f.direct_child,f.linked_child`, sid, ids, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, canonical string
		var count int
		var ambiguous bool
		if err := rows.Scan(&id, &canonical, &count, &ambiguous); err != nil {
			return nil, err
		}
		if count == 1 && !ambiguous && checklist.ValidAssignee(checklist.Assignee{Kind: "subagent", ID: canonical}) {
			out[id] = canonical
		}
	}
	return out, rows.Err()
}

// Resolve historical call-only references without matching titles or rewriting
// past events. Existing explicit canonical assignments take precedence over an
// alias that has since acquired the same canonical identity.
func checklistCanonicalAssignments(items []checklist.Item, resolved map[string]string) {
	canonicalPresent := map[string]bool{}
	for _, item := range items {
		for _, a := range item.Assignees {
			if a.Kind == "subagent" && resolved[a.ID] == a.ID {
				canonicalPresent[a.ID] = true
			}
		}
	}
	seen := map[string]bool{}
	for i := range items {
		kept := []checklist.Assignee{}
		for _, a := range items[i].Assignees {
			if a.Kind == "subagent" {
				id, ok := resolved[a.ID]
				if !ok {
					continue
				} // Unknown/ambiguous references cannot retarget.
				if id != a.ID {
					if canonicalPresent[id] {
						continue
					}
					a.ID = id
				}
			}
			key := a.Kind + "\x00" + a.ID
			if !seen[key] {
				seen[key] = true
				kept = append(kept, a)
			}
		}
		items[i].Assignees = kept
	}
}

func checklistNativeIDs(items []checklist.Item) []string {
	ids := []string{}
	for _, item := range items {
		for _, worker := range item.Assignees {
			if worker.Kind == "subagent" {
				ids = append(ids, worker.ID)
			}
		}
	}
	return ids
}
