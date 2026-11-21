package db

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"github.com/jackc/pgx/v5/pgxpool"
)

type InsightCardRef struct {
	Board  string
	Number int
	Latest time.Time
}

// InsightActivityQuestions counts recorded asks, including nonblocking questions,
// by creation date. Failure alerts are asks for delivery only and are excluded.
// Routing markers mean a question reached the person's inbox, not that they read it.
func InsightActivityQuestions(ctx context.Context, pool *pgxpool.Pool, owner, workspace string, from, to time.Time) ([]byte, error) {
	conn, release, err := insightsConnection(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer release()
	finishProfile := ObserveInsights(InsightsQuestions)
	defer finishProfile()
	var raw []byte
	err = conn.QueryRow(ctx, insightOwnedScope+`, questions AS (
 SELECT m.session_id,count(*) AS asked,count(*) FILTER(WHERE
 s.delegation_via='person' OR (s.parent_session_id IS NULL AND coalesce(s.delegation_via,'')='' AND s.started_by_kind<>'session')
 OR m.child_escalated OR m.child_person_notified OR m.person_required OR (m.owner_only AND NOT m.no_wait)) AS reached_person
 FROM messages m JOIN scope sc ON sc.id=m.session_id JOIN sessions s ON s.id=m.session_id
 WHERE m.kind='ask' AND m.created_at >= $3 AND m.created_at < $4
 AND NOT EXISTS(SELECT 1 FROM session_failure_alerts f WHERE f.message_id=m.id)
 GROUP BY m.session_id
 ) SELECT coalesce(jsonb_agg(jsonb_build_object('session_id',q.session_id,'asked',q.asked,'reached_person',q.reached_person,
 'cards',coalesce((SELECT jsonb_agg(DISTINCT m.board_id||'#'||m.card_number) FROM membership m WHERE m.session_id=q.session_id),'[]'::jsonb)) ORDER BY q.session_id),'[]'::jsonb) FROM questions q`, owner, workspace, from, to).Scan(&raw)
	return raw, err
}

// Scope membership does not depend on the time cohort. A long-lived Main can
// record a mark today or finish a card today. Inheritance stops at explicit links.
const insightOwnedScope = `WITH RECURSIVE scope AS (
 SELECT id,parent_session_id,started_at,ended_at FROM sessions WHERE spawning_account_id=$1 AND ($2='' OR id::text=$2)
 UNION SELECT s.id,s.parent_session_id,s.started_at,s.ended_at FROM sessions s JOIN scope p ON s.parent_session_id=p.id WHERE s.spawning_account_id=$1
), lineage AS (
 SELECT id AS original,id,parent_session_id FROM scope
 UNION SELECT l.original,p.id,p.parent_session_id FROM lineage l JOIN sessions p ON p.id=l.parent_session_id
 WHERE p.spawning_account_id=$1 AND NOT EXISTS(SELECT 1 FROM session_card_links x WHERE x.session_id=l.id)
), membership AS (
 SELECT DISTINCT l.original AS session_id,c.board_id,c.card_number,c.linked_at FROM lineage l JOIN session_card_links c ON c.session_id=l.id
)`

func InsightActivityScope(ctx context.Context, pool *pgxpool.Pool, owner, workspace string, from, to time.Time) (map[string]bool, []InsightCardRef, bool, bool, error) {
	conn, release, err := insightsConnection(ctx, pool)
	if err != nil {
		return nil, nil, false, false, err
	}
	defer release()
	finishProfile := ObserveInsights(InsightsScope)
	defer finishProfile()
	exists := true
	if workspace != "" {
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id::text=$1 AND spawning_account_id=$2)`, workspace, owner).Scan(&exists); err != nil {
			return nil, nil, false, false, err
		}
		if !exists {
			return nil, nil, false, false, nil
		}
	}
	// Each read makes bounded historical progress. The statement's transaction
	// holds no schema locks on the hot event/message tables.
	var linksPending bool
	err = conn.QueryRow(ctx, insightOwnedScope+`, pending AS MATERIALIZED (
 SELECT s.id FROM scope s WHERE NOT EXISTS(SELECT 1 FROM session_insight_links_backfilled b WHERE b.session_id=s.id) ORDER BY s.id LIMIT 100
 ), titles AS MATERIALIZED (SELECT link_card_title(id) FROM pending), checklists AS MATERIALIZED (
 SELECT link_card_checklist(e.session_id,e.payload,e.ts) FROM agent_events e JOIN pending p ON p.id=e.session_id WHERE e.kind='checklist'
 ), saved AS (INSERT INTO session_insight_links_backfilled SELECT id FROM pending WHERE (SELECT count(*) FROM titles)>=0 AND (SELECT count(*) FROM checklists)>=0 ON CONFLICT DO NOTHING RETURNING session_id)
 SELECT EXISTS(SELECT 1 FROM scope s WHERE NOT EXISTS(SELECT 1 FROM session_insight_links_backfilled b WHERE b.session_id=s.id) AND s.id NOT IN(SELECT session_id FROM saved))`, owner, workspace).Scan(&linksPending)
	if err != nil {
		return nil, nil, false, exists, err
	}
	rows, err := conn.Query(ctx, insightOwnedScope+` SELECT id FROM scope WHERE started_at < $4 AND (ended_at IS NULL OR ended_at >= $3) ORDER BY started_at DESC,id LIMIT 1001`, owner, workspace, from, to)
	if err != nil {
		return nil, nil, false, exists, err
	}
	selected := map[string]bool{}
	truncated := linksPending
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, false, exists, err
		}
		if len(selected) == 1000 {
			truncated = true
		} else {
			selected[id] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, false, exists, err
	}
	// Candidate cards include older links, so their Board flow can be selected by
	// completion date. Rank by recent known activity, then by live flow in server.
	rows, err = conn.Query(ctx, insightOwnedScope+`, recent AS (
 SELECT e.payload->>'board_id' AS board_id,e.payload->>'card' AS card_number,max(CASE WHEN e.payload->>'at' ~ 'T.*(Z|[+-][0-9]{2}:[0-9]{2})$' AND pg_input_is_valid(e.payload->>'at','timestamp with time zone') THEN (e.payload->>'at')::timestamptz ELSE e.ts END) AS at FROM agent_events e JOIN scope s ON s.id=e.session_id WHERE e.kind='mark' AND e.payload->>'kind' IN ('review','failure') GROUP BY e.payload->>'board_id',e.payload->>'card'
 ), alerts AS (
 SELECT m.board_id,m.card_number,max(f.created_at) AS at FROM membership m JOIN session_failure_alerts f ON f.session_id=m.session_id GROUP BY m.board_id,m.card_number
 ) SELECT m.board_id,m.card_number,max(greatest(m.linked_at,coalesce(r.at,m.linked_at),coalesce(f.at,m.linked_at))) AS latest
 FROM membership m JOIN scope s ON s.id=m.session_id LEFT JOIN recent r ON r.board_id=m.board_id AND r.card_number=m.card_number::text LEFT JOIN alerts f ON f.board_id=m.board_id AND f.card_number=m.card_number
 GROUP BY m.board_id,m.card_number ORDER BY latest DESC,m.board_id,m.card_number DESC LIMIT 1001`, owner, workspace)
	if err != nil {
		return nil, nil, false, exists, err
	}
	defer rows.Close()
	refs := []InsightCardRef{}
	for rows.Next() {
		var ref InsightCardRef
		if err = rows.Scan(&ref.Board, &ref.Number, &ref.Latest); err != nil {
			return nil, nil, false, exists, err
		}
		if len(refs) == 1000 {
			truncated = true
		} else {
			refs = append(refs, ref)
		}
	}
	return selected, refs, truncated, exists, rows.Err()
}

// InsightActivityMarks selects evidence by its own effective timestamp, not by
// the launch date of the recording session. Prose is removed before returning.
func InsightActivityMarks(ctx context.Context, pool *pgxpool.Pool, owner, workspace string, from, to time.Time) ([]byte, error) {
	conn, release, err := insightsConnection(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer release()
	finishProfile := ObserveInsights(InsightsMarks)
	defer finishProfile()
	var raw []byte
	err = conn.QueryRow(ctx, insightOwnedScope+`, records AS (
 SELECT e.payload-'text'-'summary' || jsonb_build_object('session_id',e.session_id,'recorded_at',e.ts,'source','mark','label',CASE WHEN e.payload->>'kind'='milestone' THEN left(e.payload->>'text',160) END) AS payload,
 CASE WHEN e.payload->>'at' ~ 'T.*(Z|[+-][0-9]{2}:[0-9]{2})$' AND pg_input_is_valid(e.payload->>'at','timestamp with time zone') THEN (e.payload->>'at')::timestamptz ELSE e.ts END AS at
 FROM agent_events e JOIN scope s ON s.id=e.session_id WHERE e.kind='mark' AND e.payload->>'kind' IN ('review','failure','milestone')
 UNION ALL SELECT jsonb_build_object('kind','failure','session_id',f.session_id,'recorded_at',f.created_at,'source','session_failure_alert','type','session','occurrence',f.occurrence,'turn_seq',f.turn_seq,'message_id',f.message_id,'card_links',coalesce((SELECT jsonb_agg(DISTINCT m.board_id||'#'||m.card_number) FROM membership m WHERE m.session_id=f.session_id),'[]'::jsonb)),f.created_at
 FROM session_failure_alerts f JOIN scope s ON s.id=f.session_id
 ) SELECT coalesce(jsonb_agg(payload ORDER BY at),'[]'::jsonb) FROM records WHERE at >= $3 AND at < $4`, owner, workspace, from, to).Scan(&raw)
	return raw, err
}

type insightTimingSession struct {
	ID                string           `json:"id"`
	Title             string           `json:"title"`
	NativeTimeUnknown bool             `json:"native_active_time_unknown"`
	HasSubagents      bool             `json:"has_subagents"`
	Parent            *string          `json:"parent_session_id"`
	Engine            string           `json:"engine"`
	Start             *time.Time       `json:"started_at"`
	End               *time.Time       `json:"ended_at"`
	Cards             []string         `json:"cards"`
	Shared            bool             `json:"shared_with_other_cards"`
	ReviewWorker      bool             `json:"_review_worker,omitempty"`
	InitialRework     bool             `json:"_initial_rework,omitempty"`
	Timing            *activity.Timing `json:"timing"`
	FingerprintInputs json.RawMessage  `json:"_fingerprint_inputs,omitempty"`
	Fingerprint       string           `json:"_fingerprint,omitempty"`
	CachedTiming      *activity.Timing `json:"_cached_timing,omitempty"`
}

// InsightActivityTimings uses one metadata/wait read and one ordered event read
// for the entire cohort. SplitOrdered consumes each session once from that shared
// cursor. Deadline interruption retains completed sessions and marks the rest unknown.
func computeInsightActivityTimings(ctx context.Context, pool *pgxpool.Pool, owner string, ids []string, chartWindows ...time.Time) ([]byte, error) {
	return computeInsightActivityTimingsOn(ctx, pool, owner, ids, nil, chartWindows...)
}

func computeInsightActivityTimingsOn(ctx context.Context, pool *pgxpool.Pool, owner string, ids []string, target DBTX, chartWindows ...time.Time) ([]byte, error) {
	conn := target
	release := func() {}
	var err error
	if conn == nil {
		var c *pgxpool.Conn
		c, release, err = insightsConnection(ctx, pool)
		if err != nil {
			return nil, err
		}
		conn = c
	}
	defer release()
	finishProfile := ObserveInsights(InsightsTiming)
	defer finishProfile()
	var raw []byte
	memory := rememberedTimings(pool, owner, ids)
	memoryIDs := []string{}
	for id := range memory {
		memoryIDs = append(memoryIDs, id)
	}
	err = conn.QueryRow(ctx, `WITH RECURSIVE lineage AS (
 SELECT id AS original,id,parent_session_id FROM sessions WHERE spawning_account_id=$1 AND id=ANY($2::uuid[])
 UNION SELECT l.original,p.id,p.parent_session_id FROM lineage l JOIN sessions p ON p.id=l.parent_session_id
 WHERE p.spawning_account_id=$1 AND NOT EXISTS(SELECT 1 FROM session_card_links x WHERE x.session_id=l.id)
	 ), membership AS (SELECT DISTINCT l.original,c.board_id||'#'||c.card_number AS key FROM lineage l JOIN session_card_links c ON c.session_id=l.id),
 card_marks AS MATERIALIZED (SELECT e.id,e.ts,e.seq,e.payload,t.at
 FROM agent_events e JOIN sessions author ON author.id=e.session_id
 CROSS JOIN LATERAL (SELECT CASE WHEN e.payload->>'at' ~ 'T.*(Z|[+-][0-9]{2}:[0-9]{2})$' AND pg_input_is_valid(e.payload->>'at','timestamp with time zone') THEN (e.payload->>'at')::timestamptz ELSE e.ts END AS at) t
 WHERE author.spawning_account_id=$1 AND e.kind='mark' AND e.payload->>'kind' IN ('review','failure')
 AND t.at <= (SELECT max(started_at) FROM sessions WHERE id=ANY($2::uuid[]))

 AND EXISTS(SELECT 1 FROM membership m WHERE m.key=(e.payload->>'board_id')||'#'||(e.payload->>'card'))),
 stages AS (SELECT s.id,coalesce((SELECT bool_or((latest.payload->>'kind'='review' AND latest.payload->>'verdict' IN ('changes','rejected')) OR (latest.payload->>'kind'='failure' AND latest.payload->>'type' IN ('test','gate') AND coalesce(latest.payload->>'rerun_passed','false')<>'true' AND (approval.at IS NULL OR (latest.at,latest.ts,latest.seq,latest.id)>(approval.at,approval.ts,approval.seq,approval.id))))
 FROM (SELECT DISTINCT ON (e.payload->>'kind',CASE WHEN e.payload->>'kind'='failure' THEN jsonb_build_array(e.payload->>'type',coalesce(e.payload->>'suite',''),coalesce(e.payload->>'occurrence','')) ELSE '[]'::jsonb END) e.*
 FROM card_marks e JOIN membership m ON m.original=s.id AND m.key=(e.payload->>'board_id')||'#'||(e.payload->>'card') WHERE e.at<=s.started_at
 ORDER BY e.payload->>'kind',CASE WHEN e.payload->>'kind'='failure' THEN jsonb_build_array(e.payload->>'type',coalesce(e.payload->>'suite',''),coalesce(e.payload->>'occurrence','')) ELSE '[]'::jsonb END,e.at DESC,e.ts DESC,e.seq DESC,e.id DESC) latest
 LEFT JOIN LATERAL (SELECT e.at,e.ts,e.seq,e.id FROM card_marks e JOIN membership m ON m.original=s.id AND m.key=(e.payload->>'board_id')||'#'||(e.payload->>'card') WHERE e.at<=s.started_at AND e.payload->>'kind'='review' AND e.payload->>'verdict'='approved' ORDER BY e.at DESC,e.ts DESC,e.seq DESC,e.id DESC LIMIT 1) approval ON true
 WHERE (SELECT count(*) FROM membership x WHERE x.original=s.id)=1),false) AS rework
 FROM sessions s WHERE s.spawning_account_id=$1 AND s.id=ANY($2::uuid[])),
 fingerprints AS (SELECT s.id,md5('hourly-v5-scoped:'||st.rework::text||s.started_at::text||coalesce(s.parent_session_id::text,'')||coalesce((SELECT string_agg(r.run_id||r.updated_at::text,',' ORDER BY r.run_id) FROM workspace_metric_runs r WHERE r.session_id=s.id),'')||coalesce(s.ended_at::text,'')||coalesce((SELECT max(seq)::text FROM agent_events e WHERE e.session_id=s.id),'')||coalesce((SELECT string_agg(m.id::text||coalesce(m.answered_at::text,'')||m.no_wait::text||m.owner_only::text,',' ORDER BY m.id) FROM messages m WHERE m.session_id=s.id AND m.kind='ask'),'')) AS fingerprint,st.rework FROM sessions s JOIN stages st ON st.id=s.id WHERE s.spawning_account_id=$1 AND s.id=ANY($2::uuid[]))
 SELECT jsonb_build_object('sessions',coalesce((SELECT jsonb_agg(jsonb_build_object(
 'id',s.id,'title',s.title,'parent_session_id',s.parent_session_id,'engine',coalesce(nullif(s.engine,''),'claude'),'started_at',s.started_at,'ended_at',s.ended_at,
 'native_active_time_unknown',EXISTS(SELECT 1 FROM agent_events e WHERE e.session_id=s.id AND e.kind='subagent_started'),
 'has_subagents',EXISTS(SELECT 1 FROM agent_events e WHERE e.session_id=s.id AND e.kind='subagent_started') OR EXISTS(SELECT 1 FROM sessions w WHERE w.parent_session_id=s.id AND w.spawning_account_id=$1 AND coalesce(w.delegation_via,'')<>'person'),
 'cards',coalesce((SELECT jsonb_agg(m.key ORDER BY m.key) FROM membership m WHERE m.original=s.id),'[]'::jsonb),
 '_fingerprint',f.fingerprint,'_fingerprint_inputs',jsonb_build_object('version',5,'started_at',s.started_at,'parent_session_id',s.parent_session_id,'ended_at',s.ended_at,'event_seq',(SELECT max(seq) FROM agent_events e WHERE e.session_id=s.id),'metric_runs',coalesce((SELECT jsonb_agg(jsonb_build_array(r.run_id,r.updated_at) ORDER BY r.run_id) FROM workspace_metric_runs r WHERE r.session_id=s.id),'[]'::jsonb),'waits',coalesce((SELECT jsonb_agg(jsonb_build_array(m.id,m.answered_at,m.no_wait,m.owner_only) ORDER BY m.id) FROM messages m WHERE m.session_id=s.id AND m.kind='ask'),'[]'::jsonb)),'_cached_timing',CASE WHEN s.id=ANY($3::uuid[]) THEN NULL ELSE (SELECT c.timing FROM session_insight_timings c WHERE c.session_id=s.id AND c.fingerprint=f.fingerprint AND (s.ended_at IS NOT NULL OR c.computed_at > now()-interval '1 minute')) END,
 '_initial_rework',f.rework,'_review_worker',s.parent_session_id IS NOT NULL AND EXISTS(SELECT 1 FROM agent_events e WHERE e.session_id=s.id AND e.kind='mark' AND e.payload->>'kind'='review')) ORDER BY s.id)
 FROM sessions s JOIN fingerprints f ON f.id=s.id WHERE s.spawning_account_id=$1 AND s.id=ANY($2::uuid[])),'[]'::jsonb),
 'gates',coalesce((SELECT jsonb_agg(jsonb_build_object('session',r.session_id,'started_at',r.data->'started_at','ended_at',CASE WHEN r.data->>'state'='running' AND r.updated_at<now()-interval '1 hour' THEN to_jsonb(r.updated_at+interval '1 hour') ELSE r.data->'ended_at' END,'suite',r.data->'suite','kind',r.data->'gate_kind','outcome',CASE WHEN r.data->>'state'='failed' THEN to_jsonb('failed'::text) ELSE r.data->'outcome' END)) FROM workspace_metric_runs r JOIN sessions s ON s.id=r.session_id WHERE s.spawning_account_id=$1 AND s.id=ANY($2::uuid[])),'[]'::jsonb),
 'waits',coalesce((SELECT jsonb_agg(jsonb_build_object('session',m.session_id,'start',m.created_at,'end',coalesce(m.answered_at,s.ended_at))) FROM messages m JOIN sessions s ON s.id=m.session_id
 WHERE s.spawning_account_id=$1 AND s.id=ANY($2::uuid[]) AND m.kind='ask' AND NOT m.no_wait AND (m.owner_only OR s.parent_session_id IS NULL)
 AND NOT EXISTS(SELECT 1 FROM session_failure_alerts f WHERE f.message_id=m.id)),'[]'::jsonb))`, owner, ids, memoryIDs).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var data struct {
		Sessions []insightTimingSession `json:"sessions"`
		Gates    []struct {
			Session   string
			StartedAt time.Time  `json:"started_at"`
			EndedAt   *time.Time `json:"ended_at"`
			Suite     string
			Kind      string
			Outcome   string
		} `json:"gates"`
		Waits []struct {
			Session string
			Start   time.Time
			End     *time.Time
		} `json:"waits"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	now := time.Now()
	uncached := []string{}
	for i := range data.Sessions {
		s := &data.Sessions[i]
		if entry, ok := memory[s.ID]; ok && entry.fingerprint == s.Fingerprint && (s.End != nil || time.Since(entry.at) < time.Minute) {
			s.CachedTiming = entry.timing
		}
		if s.CachedTiming != nil {
			s.Timing = s.CachedTiming
		} else {
			uncached = append(uncached, s.ID)
		}
	}
	waits := map[string][]activity.Interval{}
	for _, w := range data.Waits {
		end := now
		if w.End != nil {
			end = *w.End
		}
		waits[w.Session] = append(waits[w.Session], activity.Interval{Start: w.Start, End: end})
	}
	closeRows := func() {}
	finish := func(partial bool) ([]byte, error) {
		closeRows()
		release()
		if len(uncached) > 0 {
			type cached struct {
				ID            string           `json:"id"`
				Fingerprint   string           `json:"fingerprint"`
				Timing        *activity.Timing `json:"timing"`
				InitialRework bool             `json:"initial_rework"`
				Inputs        json.RawMessage  `json:"inputs"`
			}
			entries := []cached{}
			for _, s := range data.Sessions {
				if s.Timing != nil && s.CachedTiming == nil {
					entries = append(entries, cached{s.ID, s.Fingerprint, s.Timing, s.InitialRework, s.FingerprintInputs})
				}
			}
			if len(entries) > 0 {
				payload, err := json.Marshal(entries)
				if err != nil {
					return nil, err
				}

				cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
				writer := DBTX(pool)
				if target != nil {
					writer = target
				}
				_, err = writer.Exec(cacheCtx, `INSERT INTO session_insight_timings(session_id,fingerprint,timing,initial_rework,fingerprint_inputs) SELECT r.id,r.fingerprint,r.timing,r.initial_rework,r.inputs FROM jsonb_to_recordset($1::jsonb) AS r(id uuid,fingerprint text,timing jsonb,initial_rework boolean,inputs jsonb) JOIN sessions s ON s.id=r.id WHERE s.spawning_account_id=$2 ON CONFLICT(session_id) DO UPDATE SET timing=EXCLUDED.timing,fingerprint=EXCLUDED.fingerprint,computed_at=now(),initial_rework=EXCLUDED.initial_rework,fingerprint_inputs=EXCLUDED.fingerprint_inputs`, payload, owner)
				cancel()
				if err != nil {
					failurelog.Default.Event("insights_timing_cache_write", slog.LevelWarn, "operation", "insights_timing_cache_write", "cause", failurelog.Cause(err))
				}
			}
		}
		for i := range data.Sessions {
			rememberTiming(pool, owner, data.Sessions[i])
			data.Sessions[i].Timing = chartTiming(data.Sessions[i].Timing, chartWindows)
			data.Sessions[i].ReviewWorker = false
			data.Sessions[i].InitialRework = false
			data.Sessions[i].Fingerprint = ""
			data.Sessions[i].FingerprintInputs = nil
			data.Sessions[i].CachedTiming = nil
			data.Sessions[i].Shared = len(data.Sessions[i].Cards) > 1
		}
		return json.Marshal(struct {
			Sessions []insightTimingSession `json:"sessions"`
			Partial  bool                   `json:"partial"`
		}{data.Sessions, partial})
	}
	if len(uncached) == 0 {
		return finish(false)
	}
	rows, err := conn.Query(ctx, `SELECT e.session_id,CASE WHEN e.kind='mark' THEN CASE WHEN e.payload->>'at' ~ 'T.*(Z|[+-][0-9]{2}:[0-9]{2})$' AND pg_input_is_valid(e.payload->>'at','timestamp with time zone') THEN (e.payload->>'at')::timestamptz ELSE e.ts END ELSE e.ts END AS effective_at,e.kind,`+timingPayloadSQL+`
 FROM agent_events e JOIN sessions s ON s.id=e.session_id WHERE s.spawning_account_id=$1 AND s.id=ANY($2::uuid[]) AND e.kind IN ('tool_call','tool_result','status_changed','turn_done','mark','turn_work','background_task_started','background_task_done','process_activity') ORDER BY e.session_id,effective_at,e.seq`, owner, uncached)
	if err != nil {
		if ctx.Err() != nil {
			return finish(true)
		}
		return nil, err
	}
	defer rows.Close()
	closeRows = rows.Close
	var current activity.Event
	var currentID string
	var scanErr error
	advance := func() bool {
		if !rows.Next() {
			scanErr = rows.Err()
			return false
		}
		scanErr = rows.Scan(&currentID, &current.At, &current.Kind, &current.Payload)
		return scanErr == nil
	}
	available := advance()
	for i := range data.Sessions {
		s := &data.Sessions[i]
		if s.CachedTiming != nil {
			continue
		}
		if ctx.Err() != nil {
			return finish(true)
		}
		if s.Start == nil {
			continue
		}
		for available && currentID < s.ID {
			available = advance()
		}
		end := now
		if s.End != nil {
			end = *s.End
		}
		events := func(yield func(activity.Event) bool) {
			if s.InitialRework && !s.ReviewWorker && !yield(activity.Event{At: *s.Start, Kind: "rework_started", Payload: json.RawMessage(`{}`)}) {
				return
			}
			for _, g := range data.Gates {
				if g.Session != s.ID {
					continue
				}
				stop := end
				if g.EndedAt != nil {
					stop = *g.EndedAt
				}
				payload, _ := json.Marshal(map[string]any{"started_at": g.StartedAt, "ended_at": stop, "timing_gate": activity.GateEvidence{Kind: g.Kind, Suite: g.Suite}, "outcome": g.Outcome})
				if !yield(activity.Event{At: g.StartedAt, Kind: "gate_interval", Payload: payload}) {
					return
				}
			}
			if s.ReviewWorker && !yield(activity.Event{At: *s.Start, Kind: "review_started", Payload: json.RawMessage(`{}`)}) {
				return
			}
			for available && currentID == s.ID {
				if !yield(current) {
					return
				}
				available = advance()
			}
		}
		timing := activity.SplitOrderedWithHours(*s.Start, end, events, waits[s.ID])
		if scanErr != nil {
			if ctx.Err() != nil {
				return finish(true)
			}
			return nil, scanErr
		}
		if ctx.Err() != nil {
			return finish(true)
		}
		s.Timing = &timing
	}
	if scanErr != nil {
		return nil, scanErr
	}
	return finish(false)
}
