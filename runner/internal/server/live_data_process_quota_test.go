package server

import (
	"context"
	"encoding/json"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

func liveProcessQuota(ctx context.Context, pool *pgxpool.Pool, r ld.Revision, workspace, mode string) error {
	count := 0
	outcome := "ok"
	pending := false
	switch mode {
	case "connection rate":
		count = 30
	case "owner rate":
		count = 60
	case "connection concurrency":
		count = 1
		outcome = "dispatched"
	case "owner concurrency":
		count = 2
		outcome = "dispatched"
	case "global concurrency":
		count = 4
		outcome = "dispatched"
	case "queue":
		count = 80
		outcome = "undispatched"
		pending = true
	case "bindings", "expired bindings":
		count = 20
	default:
		return ld.ErrDenied
	}
	for range count {
		s := r.Scope
		s.Binding = newUUID()
		if mode == "owner rate" || mode == "owner concurrency" || mode == "global concurrency" {
			s.Connection = newUUID()
		}
		if mode == "global concurrency" {
			s.Owner = newUUID()
		}
		raw, _ := json.Marshal(s)
		if mode == "bindings" || mode == "expired bindings" {
			expires := time.Now().Add(time.Hour)
			if mode == "expired bindings" {
				expires = time.Now().Add(-time.Hour)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO live_mcp_binding_state(binding_id,revision,scope,enabled,fence,lease_until,demand_until,workspace_id,expires_at) VALUES($1,$2,$3,true,1,clock_timestamp(),clock_timestamp(),$4,$5)`, s.Binding, s.Revision, raw, workspace, expires); err != nil {
				return err
			}
		} else {
			var dispatched, finished any
			if !pending {
				dispatched = time.Now()
			}
			if outcome == "ok" {
				finished = time.Now()
			}
			if _, err := pool.Exec(ctx, `INSERT INTO live_mcp_attempts(binding_id,revision,owner_id,connection_id,scope,fence,due_at,expires_at,dispatched_at,finished_at,outcome) VALUES($1,$2,$3,$4,$5,1,clock_timestamp(),clock_timestamp()+interval '1 minute',$6,$7,$8)`, s.Binding, s.Revision, s.Owner, s.Connection, raw, dispatched, finished, outcome); err != nil {
				return err
			}
		}
	}
	return nil
}
