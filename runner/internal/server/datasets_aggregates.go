package server

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

func gateBinRows(rows []protocol.DatasetRow, p map[string]string) ([]protocol.DatasetRow, string) {
	width, err := strconv.Atoi(p["width"])
	if err != nil || width < 1 || width > 86400 {
		return nil, "invalid_bin_width"
	}
	suite := p["suite"]
	if suite == "" {
		suite = "total"
	}
	counts := map[int]int{}
	for _, r := range rows {
		if r["suite"] != suite {
			continue
		}
		v, ok := r["seconds"].(float64)
		if !ok || !validGateSeconds(&v) {
			return nil, "invalid_gate_sample"
		}
		counts[int(math.Floor(v/float64(width)))]++
	}
	keys := make([]int, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	bins := make([]protocol.DatasetRow, 0, len(keys))
	for _, k := range keys {
		bins = append(bins, protocol.DatasetRow{"key": strconv.Itoa(k * width), "start": k * width, "end": (k + 1) * width, "count": counts[k], "suite": suite})
	}
	return bins, ""
}

func mergeHeatmapRows(history *protocol.MergeHistory, p map[string]string) ([]protocol.DatasetRow, string) {
	hourly := map[string]string{}
	for k, v := range p {
		hourly[k] = v
	}
	hourly["bucket"] = "hour"
	rows, cause := mergeDatasetRows(history, hourly, history.ObservedAt)
	if cause != "" {
		return nil, cause
	}
	zone, err := time.LoadLocation(p["timezone"])
	if err != nil {
		return nil, "invalid_timezone"
	}
	groups := map[string]protocol.DatasetRow{}
	for _, r := range rows {
		bucket, ok := r["bucket"].(string)
		if !ok {
			return nil, "invalid_merge_sample"
		}
		at, err := time.Parse(time.RFC3339, bucket)
		if err != nil {
			return nil, "invalid_merge_sample"
		}
		local := at.In(zone)
		day := local.Format("2006-01-02")
		key := fmt.Sprintf("%s:%02d", day, local.Hour())
		group := groups[key]
		if group == nil {
			group = protocol.DatasetRow{"key": key, "day": day, "hour": local.Hour(), "count": 0, "bucket": r["bucket"], "end": r["end"], "timezone": p["timezone"]}
			groups[key] = group
		}
		current, _ := group["count"].(int)
		count, ok := r["count"].(int)
		if !ok {
			return nil, "invalid_merge_sample"
		}
		group["count"] = current + count
		end, _ := r["end"].(string)
		previous, _ := group["end"].(string)
		if end > previous {
			group["end"] = r["end"]
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]protocol.DatasetRow, 0, len(keys))
	for _, k := range keys {
		out = append(out, groups[k])
	}
	return out, ""
}

type workerTimelinePoint struct {
	At       time.Time
	Sequence int64
	Status   string
}

func workerTimelineRows(id string, start, end time.Time, points []workerTimelinePoint) []protocol.DatasetRow {
	// Rebuild deterministic intervals; a corrected point retains its time key and
	// the next boundary moves with it. Before the first fact coverage is unknown.
	sort.SliceStable(points, func(i, j int) bool {
		if points[i].At.Equal(points[j].At) {
			return points[i].Sequence < points[j].Sequence
		}
		return points[i].At.Before(points[j].At)
	})
	filtered := []workerTimelinePoint{{At: start, Status: "unknown"}}
	for _, p := range points {
		if p.At.Before(start) || !p.At.Before(end) {
			continue
		}
		switch p.Status {
		case "running", "idle", "waiting", "starting", "ended", "error", "stopped":
		default:
			p.Status = "unknown"
		}
		if filtered[len(filtered)-1].At.Equal(p.At) {
			filtered[len(filtered)-1] = p
		} else {
			filtered = append(filtered, p)
		}
	}
	out := []protocol.DatasetRow{}
	for i, p := range filtered {
		stop := end
		if i+1 < len(filtered) {
			stop = filtered[i+1].At
		}
		if !stop.After(p.At) {
			continue
		}
		out = append(out, protocol.DatasetRow{"key": id + ":" + p.At.UTC().Format(time.RFC3339Nano), "worker": id, "start": p.At.UTC().Format(time.RFC3339Nano), "end": stop.UTC().Format(time.RFC3339Nano), "status": p.Status})
	}
	return out
}

func timelineDatasetRows(ctx context.Context, pool *pgxpool.Pool, account, sid string, p map[string]string) ([]protocol.DatasetRow, time.Time, string) {
	now := time.Now().UTC()
	since := datasetSince(p, now, 24*time.Hour, 7*24*time.Hour)
	root, err := datasetWorkspaceRoot(ctx, pool, sid)
	if err != nil {
		return nil, now, "workspace_unavailable"
	}
	result, err := pool.Query(ctx, `WITH RECURSIVE family AS (
 SELECT id,ARRAY[id] AS path FROM sessions WHERE parent_session_id=$1
 UNION ALL SELECT s.id,f.path||s.id FROM sessions s JOIN family f ON s.parent_session_id=f.id WHERE NOT s.id=ANY(f.path) AND cardinality(f.path)<64
 ), visible AS (
 SELECT s.id,s.started_at,least(coalesce(s.ended_at,$3),$3) AS ended_at FROM family f JOIN sessions s ON s.id=f.id
 WHERE (NOT s.private OR s.spawning_account_id=$2) AND coalesce(s.ended_at,$3)>$4 AND s.started_at<$3
 ), points AS (
 SELECT v.id,v.started_at AS at,-1::bigint AS seq,'unknown'::text AS status,v.ended_at FROM visible v
 UNION ALL SELECT v.id,e.ts,e.seq,coalesce(e.payload->>'status','unknown'),v.ended_at FROM visible v JOIN agent_events e ON e.session_id=v.id AND e.kind='status_changed' AND e.ts >= $4 AND e.ts>=v.started_at AND e.ts<v.ended_at
 UNION ALL SELECT v.id,e.ts,e.seq,coalesce(e.payload->>'status','unknown'),v.ended_at FROM visible v CROSS JOIN LATERAL (
 SELECT ts,seq,payload FROM agent_events WHERE session_id=v.id AND kind='status_changed' AND ts>=v.started_at AND ts<$4 ORDER BY ts DESC,seq DESC LIMIT 1) e
 ), corrected AS (
 SELECT DISTINCT ON(id,at) id,at,seq,status,ended_at FROM points ORDER BY id,at,seq DESC
 ), intervals AS (
 SELECT id,greatest(at,$4) AS start,lead(at,1,ended_at) OVER(PARTITION BY id ORDER BY at) AS stop,status FROM corrected
 ) SELECT id::text,start,stop,status FROM intervals WHERE stop>start ORDER BY stop DESC,start DESC,id LIMIT 10001`, root, account, now, since)
	if err != nil {
		return nil, now, "timeline_unavailable"
	}
	defer result.Close()
	out := []protocol.DatasetRow{}
	for result.Next() {
		if len(out) == 10000 {
			markDatasetTruncated(ctx)
			break
		}
		var id, status string
		var start, end time.Time
		if result.Scan(&id, &start, &end, &status) != nil {
			return nil, now, "invalid_timeline_sample"
		}
		switch status {
		case "running", "idle", "waiting", "starting", "ended", "error", "stopped":
		default:
			status = "unknown"
		}
		out = append(out, protocol.DatasetRow{"key": datasetRowKey(id, start.UTC().Format(time.RFC3339Nano)), "worker": id, "start": start.UTC().Format(time.RFC3339Nano), "end": end.UTC().Format(time.RFC3339Nano), "status": status})
	}
	if result.Err() != nil {
		return nil, now, "timeline_unavailable"
	}
	return out, now, ""
}
