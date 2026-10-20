package server

import (
	"net/http"
	"sort"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/checklist"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/livestate"
	"github.com/blerglab/blerg-ai/runner/internal/topics"
)

const checklistWorkerLimit = 100

type checklistWorkerInfo struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	at     time.Time
	active bool
}

// HandleChecklistWorkers exposes only recorded workers available to this session's
// own credential. It discovers display IDs, not native engine control identities.
func (a *API) HandleChecklistWorkers(w http.ResponseWriter, r *http.Request) {
	sid, _, ok := a.agentSessionWrite(w, r, "checklist", 0)
	if !ok {
		return
	}
	ctx := r.Context()
	fail := func(err error) { writeDatabaseFailure(w, "list checklist workers", "checklist workers "+sid, err) }
	parent, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		fail(err)
		return
	}
	if parent == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if terminalSessionStatus(parent.Status) {
		writeError(w, http.StatusConflict, "This session has ended; workers are available only while it runs.")
		return
	}
	through, err := db.MaxAgentEventSeq(ctx, a.dbPool, sid)
	if err != nil {
		fail(err)
		return
	}
	rows, err := db.ListLiveEvents(ctx, a.dbPool, sid, through)
	if err != nil {
		fail(err)
		return
	}
	state, err := buildLiveState(ctx, a.dbPool, sid, through, rows, livestate.Options{Now: time.Now(), Status: parent.Status})
	if err != nil {
		fail(err)
		return
	}
	sort.Slice(state.Subagents, func(i, j int) bool { return state.Subagents[i].ActiveAt.After(state.Subagents[j].ActiveAt) })
	truncated := len(state.Subagents) > checklistWorkerLimit
	candidates := []checklistWorkerInfo{}
	for i, s := range state.Subagents {
		if i >= checklistWorkerLimit {
			break
		}
		status := "running"
		if s.Background {
			status = "background"
		}
		candidates = append(candidates, checklistWorkerInfo{Kind: "subagent", ID: s.Key, Title: s.Title, Status: status, at: s.ActiveAt, active: true})
	}
	// Bound completed/unknown candidates independently so old work cannot crowd
	// current workers out. No tool arguments, model secrets or native IDs are read.
	recent, err := a.dbPool.Query(ctx, `WITH latest AS (
 SELECT DISTINCT ON (COALESCE(NULLIF(payload->>'child_id',''),NULLIF(payload->>'call_id','')))
 COALESCE(NULLIF(payload->>'child_id',''),NULLIF(payload->>'call_id','')) AS id,kind,payload,ts,seq
 FROM agent_events WHERE session_id=$1 AND seq<=$2 AND kind IN ('subagent_started','subagent_update','subagent_done')
 ORDER BY COALESCE(NULLIF(payload->>'child_id',''),NULLIF(payload->>'call_id','')),seq DESC
 ) SELECT id,COALESCE((SELECT left(s.payload->>'summary',200) FROM agent_events s
 WHERE s.session_id=$1 AND s.seq<=$2 AND s.kind='subagent_started'
 AND (s.payload->>'child_id'=latest.id OR s.payload->>'call_id'=latest.id) ORDER BY s.seq DESC LIMIT 1),''),
 CASE WHEN kind='subagent_done' AND COALESCE(payload->>'background','false')!='true' THEN 'done' ELSE 'unknown' END,ts
 FROM latest WHERE id IS NOT NULL ORDER BY seq DESC LIMIT 101`, sid, through)
	if err != nil {
		fail(err)
		return
	}
	recentCount := 0
	for recent.Next() {
		var v checklistWorkerInfo
		v.Kind = "subagent"
		if err = recent.Scan(&v.ID, &v.Title, &v.Status, &v.at); err != nil {
			recent.Close()
			fail(err)
			return
		}
		candidates = append(candidates, v)
		recentCount++
	}
	err = recent.Err()
	recent.Close()
	if err != nil {
		fail(err)
		return
	}
	truncated = truncated || recentCount > checklistWorkerLimit
	children, err := a.dbPool.Query(ctx, `SELECT child.id::text,COALESCE(child.title,''),child.status,child.started_at
 FROM sessions child JOIN sessions parent ON parent.id=$1
 WHERE child.parent_session_id=parent.id AND child.kind='agent' AND child.archived_at IS NULL
 AND parent.spawning_account_id IS NOT NULL AND child.spawning_account_id=parent.spawning_account_id
 ORDER BY (child.status NOT IN ('ended','error','stopped')) DESC,child.started_at DESC LIMIT 101`, sid)
	if err != nil {
		fail(err)
		return
	}
	childCount := 0
	for children.Next() {
		var v checklistWorkerInfo
		v.Kind = "child_session"
		if err = children.Scan(&v.ID, &v.Title, &v.Status, &v.at); err != nil {
			children.Close()
			fail(err)
			return
		}
		v.active = !terminalSessionStatus(v.Status)
		candidates = append(candidates, v)
		childCount++
	}
	err = children.Err()
	children.Close()
	if err != nil {
		fail(err)
		return
	}
	truncated = truncated || childCount > checklistWorkerLimit
	ids := []string{}
	for _, v := range candidates {
		if v.Kind == "subagent" {
			ids = append(ids, v.ID)
		}
	}
	resolved, err := checklistNativeWorkers(ctx, a.dbPool, sid, ids, through)
	if err != nil {
		fail(err)
		return
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].active != candidates[j].active {
			return candidates[i].active
		}
		if !candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].at.After(candidates[j].at)
		}
		return candidates[i].Kind+candidates[i].ID < candidates[j].Kind+candidates[j].ID
	})
	workers := []checklistWorkerInfo{}
	seen := map[string]bool{}
	for _, v := range candidates {
		if v.Kind == "subagent" {
			id, ok := resolved[v.ID]
			if !ok {
				continue
			}
			v.ID = id
		}
		if !checklist.ValidAssignee(checklist.Assignee{Kind: v.Kind, ID: v.ID}) {
			continue
		}
		key := v.Kind + "\x00" + v.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(workers) >= checklistWorkerLimit {
			truncated = true
			continue
		}
		v.Title = topics.CleanText(v.Title, 200)
		workers = append(workers, v)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"workers": workers, "truncated": truncated})
}
