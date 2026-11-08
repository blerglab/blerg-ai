package db

import (
	"context"
	"encoding/json"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/activity"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Only tool calls carry command input; result payloads are never read here.
const timingPayloadSQL = `CASE e.kind
 WHEN 'tool_call' THEN jsonb_build_object('tool',e.payload->'tool','call_id',e.tool_call_id,'timing_gate',e.payload->'timing_gate','input',jsonb_build_object('paths',CASE WHEN jsonb_typeof(e.payload->'input')='array' THEN jsonb_path_query_array(e.payload->'input','$[*].path') END,'file_path',e.payload->'input'->'file_path','path',e.payload->'input'->'path','patch',CASE WHEN jsonb_typeof(e.payload->'input')='string' AND length(e.payload->>'input')<=65536 THEN e.payload->'input' WHEN length(e.payload->'input'->>'patch')<=65536 THEN e.payload->'input'->'patch' END,'cmd',CASE WHEN length(e.payload->'input'->>'cmd')<=65536 THEN e.payload->'input'->'cmd' END,'command',CASE WHEN length(e.payload->'input'->>'command')<=65536 THEN e.payload->'input'->'command' END))
 WHEN 'tool_result' THEN jsonb_build_object('call_id',e.tool_call_id,'exit_code',CASE WHEN jsonb_typeof(e.payload->'exit_code')='number' THEN e.payload->'exit_code' END,'is_error',e.payload->'is_error')
 WHEN 'process_activity' THEN jsonb_build_object('spans',e.payload->'spans','dropped',e.payload->'dropped')
 WHEN 'turn_done' THEN jsonb_build_object('stop_reason',e.payload->'stop_reason','usage',e.payload->'usage','usage_reported',e.payload->'usage_reported')
 WHEN 'background_task_started' THEN jsonb_build_object('task_id',e.payload->'task_id','call_id',e.payload->'call_id','started_at',e.payload->'started_at','command',CASE WHEN length(e.payload->>'command')<=65536 THEN e.payload->'command' END)
 WHEN 'background_task_done' THEN jsonb_build_object('task_id',e.payload->'task_id','status',e.payload->'status')
 WHEN 'turn_work' THEN jsonb_build_object('seconds',e.payload->'seconds','started_at',e.payload->'started_at','ended_at',e.payload->'ended_at')
 WHEN 'status_changed' THEN jsonb_build_object('status',e.payload->'status')
 WHEN 'mark' THEN jsonb_build_object('kind',e.payload->'kind','type',e.payload->'type','rerun_passed',e.payload->'rerun_passed','verdict',e.payload->'verdict','at',e.payload->'at')
 ELSE '{}'::jsonb END`

// CardData returns only sessions launched by this owner, including private ones.
// Children inherit attribution through an owned linked ancestor only when they have no explicit card links. Waiting intervals
// are explicit blocking asks; no_wait questions and engine idle time are excluded.
// A worker ask its parent can answer leaves human-only duration unknown.
// Migration 010 defines the empty stored engine as Claude. Missing model IDs remain null. Reviews are attributed claims, not
// authenticated proof that the named engine ran the review.
func CardData(ctx context.Context, pool *pgxpool.Pool, owner, board string, number int) (json.RawMessage, error) {
	var raw []byte
	err := pool.QueryRow(ctx, `WITH RECURSIVE linked AS (
 SELECT s.id,s.id AS root_id FROM sessions s WHERE s.spawning_account_id=$1 AND EXISTS
 (SELECT 1 FROM session_card_links l WHERE l.session_id=s.id AND l.board_id=$2 AND l.card_number=$3)
 UNION
 SELECT s.id,p.root_id FROM sessions s JOIN linked p ON s.parent_session_id=p.id WHERE s.spawning_account_id=$1
 AND NOT EXISTS (SELECT 1 FROM session_card_links own_link WHERE own_link.session_id=s.id)
 ), owned AS (SELECT s.*,l.root_id FROM sessions s JOIN linked l ON l.id=s.id), attribution AS (
 SELECT EXISTS(SELECT 1 FROM owned s JOIN session_card_links l ON l.session_id=s.id
 WHERE l.board_id<>$2 OR l.card_number<>$3) OR EXISTS(
 SELECT 1 FROM messages m JOIN owned s ON s.id=m.session_id WHERE m.kind='ask'
 AND NOT EXISTS(SELECT 1 FROM session_failure_alerts f WHERE f.message_id=m.id) AND NOT m.no_wait AND NOT m.owner_only AND s.parent_session_id IS NOT NULL) AS ambiguous
 ), waits AS (
 SELECT m.id,m.session_id,m.owner_only,s.parent_session_id,m.created_at AS started_at,coalesce(m.answered_at,s.ended_at) AS ended_at,
 greatest(0,extract(epoch FROM greatest(m.created_at,coalesce(m.answered_at,s.ended_at,now()))-m.created_at)) AS seconds
 FROM messages m JOIN owned s ON s.id=m.session_id WHERE m.kind='ask' AND NOT m.no_wait AND NOT EXISTS(SELECT 1 FROM session_failure_alerts f WHERE f.message_id=m.id)
 ), ranges AS (
 SELECT range_agg(tstzrange(started_at,greatest(started_at,coalesce(ended_at,now())),'[)')) AS spans FROM waits
 )
 SELECT jsonb_build_object(
 'sessions',coalesce((SELECT jsonb_agg(jsonb_build_object('id',s.id,'engine',coalesce(nullif(s.engine,''),'claude'),'model',nullif(s.model,''),'parent_session_id',s.parent_session_id,'started_at',s.started_at,'ended_at',s.ended_at,
 'timing_scope','whole_session_not_additive_across_cards',
 'shared_with_other_cards',EXISTS(SELECT 1 FROM session_card_links l WHERE l.session_id=s.root_id AND (l.board_id<>$2 OR l.card_number<>$3)),
 'card_links',(SELECT jsonb_agg(DISTINCT jsonb_build_object('board_id',l.board_id,'number',l.card_number)) FROM session_card_links l WHERE l.session_id=s.root_id),
 'timing_event_limit',2000,
 'timing_event_count',(SELECT count(*) FROM agent_events e WHERE e.session_id=s.id AND e.kind IN ('tool_call','tool_result','status_changed','turn_done','mark','turn_work','background_task_started','background_task_done','process_activity')),
 'timing_events_truncated',(SELECT count(*)>2000 FROM agent_events e WHERE e.session_id=s.id AND e.kind IN ('tool_call','tool_result','status_changed','turn_done','mark','turn_work','background_task_started','background_task_done','process_activity')),
 'workers_limit',100,
 'workers_truncated',(SELECT count(*)>100 FROM agent_events e WHERE e.session_id=s.id AND e.kind IN ('subagent_started','subagent_update','subagent_done')),
 'model_events_limit',100,
 'model_events_truncated',(SELECT count(*)>100 FROM agent_events e WHERE e.session_id=s.id AND e.kind IN ('model_changed','turn_done')),
 '_review_worker',s.parent_session_id IS NOT NULL AND EXISTS(SELECT 1 FROM agent_events e WHERE e.session_id=s.id AND e.kind='mark' AND e.payload->>'kind'='review' AND e.payload->>'board_id'=$2 AND e.payload->>'card'=$3::text),
 '_timing_events',coalesce((SELECT jsonb_agg(jsonb_build_object('At',e.ts,'Kind',e.kind,'Payload',`+timingPayloadSQL+`) ORDER BY e.ts,e.seq) FROM (SELECT ts,seq,kind,payload,tool_call_id FROM agent_events WHERE session_id=s.id AND kind IN ('tool_call','tool_result','status_changed','turn_done','mark','turn_work','background_task_started','background_task_done','process_activity') ORDER BY seq DESC LIMIT 2000) e),'[]'::jsonb),
 'links',(SELECT jsonb_agg(jsonb_build_object('source',l.source,'at',l.linked_at)) FROM session_card_links l WHERE l.session_id=s.id AND l.board_id=$2 AND l.card_number=$3),
 'model_events',coalesce((SELECT jsonb_agg(jsonb_build_object('kind',e.kind,'model',e.payload->>'model','usage_by_model',e.payload->'usage_by_model','at',e.ts) ORDER BY e.seq) FROM (SELECT kind,payload,ts,seq FROM agent_events WHERE session_id=s.id AND kind IN ('model_changed','turn_done') ORDER BY seq DESC LIMIT 100) e),'[]'::jsonb),
 'workers',coalesce((SELECT jsonb_agg(jsonb_build_object('kind',e.kind,'id',e.payload->>'child_id','model',e.payload->>'model','type',e.payload->>'agent_type','at',e.ts) ORDER BY e.seq) FROM (SELECT kind,payload,ts,seq FROM agent_events WHERE session_id=s.id AND kind IN ('subagent_started','subagent_update','subagent_done') ORDER BY seq DESC LIMIT 100) e),'[]'::jsonb)) ORDER BY s.started_at) FROM owned s),'[]'::jsonb),
 'reviews',coalesce((SELECT jsonb_agg(e.payload || jsonb_build_object('session_id',e.session_id,'recorded_at',e.ts) ORDER BY e.ts,e.seq) FROM agent_events e JOIN sessions s ON s.id=e.session_id WHERE s.spawning_account_id=$1 AND e.kind='mark' AND e.payload->>'kind'='review' AND e.payload->>'board_id'=$2 AND e.payload->>'card'=$3::text),'[]'::jsonb),
 'failures',coalesce((SELECT jsonb_agg(f.payload ORDER BY f.at) FROM (
 SELECT e.payload || jsonb_build_object('session_id',e.session_id,'recorded_at',e.ts,'source','mark') AS payload,e.ts AS at FROM agent_events e JOIN sessions s ON s.id=e.session_id WHERE s.spawning_account_id=$1 AND e.kind='mark' AND e.payload->>'kind'='failure' AND e.payload->>'board_id'=$2 AND e.payload->>'card'=$3::text
 UNION ALL SELECT jsonb_build_object('session_id',f.session_id,'recorded_at',f.created_at,'source','session_failure_alert','type','session','occurrence',f.occurrence,'turn_seq',f.turn_seq,'message_id',f.message_id,'summary',m.body),f.created_at FROM session_failure_alerts f JOIN owned s ON s.id=f.session_id JOIN messages m ON m.id=f.message_id
 ) f),'[]'::jsonb),
 'reruns',coalesce((SELECT jsonb_agg(e.payload || jsonb_build_object('session_id',e.session_id,'recorded_at',e.ts) ORDER BY e.ts,e.seq) FROM agent_events e JOIN sessions s ON s.id=e.session_id WHERE s.spawning_account_id=$1 AND e.kind='mark' AND e.payload->>'kind'='failure' AND e.payload->>'rerun_passed'='true' AND e.payload->>'board_id'=$2 AND e.payload->>'card'=$3::text),'[]'::jsonb),
 'waiting_on_person',jsonb_build_object('basis','explicit_blocking_asks','intervals',coalesce((SELECT jsonb_agg(to_jsonb(w) ORDER BY started_at) FROM waits w),'[]'::jsonb),
 'attribution_ambiguous',(SELECT ambiguous FROM attribution),
 'seconds',CASE WHEN (SELECT ambiguous FROM attribution) THEN NULL ELSE coalesce((SELECT sum(extract(epoch FROM upper(r)-lower(r))) FROM ranges CROSS JOIN LATERAL unnest(spans) r),0) END))`, owner, board, number).Scan(&raw)
	if err != nil {
		return nil, err
	}
	return addSessionTiming(ctx, pool, raw, time.Now())
}

// addSessionTiming consumes private tool payloads before returning data to callers.
// Timing is for each complete session, not a fabricated allocation across cards.
func addSessionTiming(ctx context.Context, pool *pgxpool.Pool, raw []byte, now time.Time) (json.RawMessage, error) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	var sessions []map[string]json.RawMessage
	if err := json.Unmarshal(data["sessions"], &sessions); err != nil {
		return nil, err
	}
	var waiting struct {
		Intervals []struct {
			SessionID string     `json:"session_id"`
			Start     time.Time  `json:"started_at"`
			End       *time.Time `json:"ended_at"`
			OwnerOnly bool       `json:"owner_only"`
			Parent    *string    `json:"parent_session_id"`
		} `json:"intervals"`
	}
	if err := json.Unmarshal(data["waiting_on_person"], &waiting); err != nil {
		return nil, err
	}
	for _, session := range sessions {
		var id string
		var start, end *time.Time
		var events []activity.Event
		_ = json.Unmarshal(session["id"], &id)
		_ = json.Unmarshal(session["started_at"], &start)
		_ = json.Unmarshal(session["ended_at"], &end)
		if err := json.Unmarshal(session["_timing_events"], &events); err != nil {
			return nil, err
		}
		delete(session, "_timing_events")
		var truncated, reviewWorker bool
		_ = json.Unmarshal(session["timing_events_truncated"], &truncated)
		_ = json.Unmarshal(session["_review_worker"], &reviewWorker)
		delete(session, "_review_worker")
		if reviewWorker && start != nil {
			// A structured child review bounds a review worker's phase from launch
			// through its recorded verdict. This is an explicit phase proxy.
			events = append([]activity.Event{{At: *start, Kind: "review_started", Payload: json.RawMessage(`{}`)}}, events...)
			session["review_phase_basis"] = json.RawMessage(`"child_launch_through_recorded_review"`)
		}

		if start == nil {
			session["timing"] = json.RawMessage(`null`)
			continue
		}
		if end == nil {
			end = &now
		}
		waits := []activity.Interval{}
		for _, w := range waiting.Intervals {
			if w.SessionID == id && (w.OwnerOnly || w.Parent == nil) {
				stop := now
				if w.End != nil {
					stop = *w.End
				}
				waits = append(waits, activity.Interval{Start: w.Start, End: stop})
			}
		}
		var timing activity.Timing
		var err error
		if truncated {
			timing, err = streamSessionTiming(ctx, pool, id, *start, *end, waits, reviewWorker)
			if err != nil {
				return nil, err
			}
		} else {
			timing = activity.Split(*start, *end, events, waits)
		}
		encoded, err := json.Marshal(timing)
		if err != nil {
			return nil, err
		}
		session["timing"] = encoded
		if pool != nil {
			turns, err := SessionTurns(ctx, pool, id, now)
			if err != nil {
				return nil, err
			}
			session["turns_count"], _ = json.Marshal(len(turns))
			session["turns_truncated"], _ = json.Marshal(len(turns) > 100)
			if len(turns) > 100 {
				turns = turns[len(turns)-100:]
			}
			session["turns"], err = json.Marshal(turns)
			if err != nil {
				return nil, err
			}
		}
	}
	encoded, err := json.Marshal(sessions)
	if err != nil {
		return nil, err
	}
	data["sessions"] = encoded
	return json.Marshal(data)
}

// Long histories use a row stream, not a giant JSON aggregate or a partial total.
func streamSessionTiming(ctx context.Context, pool *pgxpool.Pool, id string, start, end time.Time, waits []activity.Interval, reviewWorker bool) (activity.Timing, error) {
	rows, err := pool.Query(ctx, `SELECT CASE WHEN e.kind='mark' THEN
 CASE WHEN e.payload->>'at' ~ 'T.*(Z|[+-][0-9]{2}:[0-9]{2})$' AND pg_input_is_valid(e.payload->>'at','timestamp with time zone') THEN (e.payload->>'at')::timestamptz ELSE e.ts END
 ELSE e.ts END AS effective_at,e.kind,`+timingPayloadSQL+`
 FROM agent_events e WHERE e.session_id=$1 AND e.kind IN ('tool_call','tool_result','status_changed','turn_done','mark','turn_work','background_task_started','background_task_done','process_activity') ORDER BY effective_at,e.seq`, id)
	if err != nil {
		return activity.Timing{}, err
	}
	defer rows.Close()
	var readErr error
	events := func(yield func(activity.Event) bool) {
		if reviewWorker && !yield(activity.Event{At: start, Kind: "review_started", Payload: json.RawMessage(`{}`)}) {
			return
		}
		for rows.Next() {
			var event activity.Event
			if readErr = rows.Scan(&event.At, &event.Kind, &event.Payload); readErr != nil {
				return
			}
			if !yield(event) {
				return
			}
		}
		readErr = rows.Err()
	}
	timing := activity.SplitOrdered(start, end, events, waits)
	return timing, readErr
}
