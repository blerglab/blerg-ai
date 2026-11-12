package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// blackboardSessionAccess accepts only a session's own token, including a worker
// under the linked workspace. Daemon credentials cannot borrow a person's identity.
func (a *API) blackboardSessionAccess(w http.ResponseWriter, r *http.Request, bid string) (blackboard.Record, *db.SessionRow, bool) {
	if a.dbPool == nil {
		writeError(w, 503, noDatabaseMsg)
		return blackboard.Record{}, nil, false
	}
	sid, ok := a.authMessaging(w, r)
	if !ok {
		return blackboard.Record{}, nil, false
	}
	if sid == "" {
		writeError(w, 403, "A session credential is required.")
		return blackboard.Record{}, nil, false
	}
	if path := r.PathValue("session"); path != "" && path != sid {
		writeError(w, 403, "Use this session's own credential.")
		return blackboard.Record{}, nil, false
	}
	if !validSessionID(sid) {
		writeError(w, 404, "Session not found.")
		return blackboard.Record{}, nil, false
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil {
		blackboardError(w, err)
		return blackboard.Record{}, nil, false
	}
	if row == nil {
		writeError(w, 404, "Session not found.")
		return blackboard.Record{}, nil, false
	}
	if row.DiscardRequestedAt != nil || row.ArchivedAt != nil || terminalSessionStatus(row.Status) {
		writeError(w, 409, "This session has ended.")
		return blackboard.Record{}, nil, false
	}
	var record blackboard.Record
	record, err = a.blackboardStore().LoadPartition(r.Context(), bid, blackboard.Partition{Kind: blackboard.PartitionCore})
	if err != nil {
		blackboardError(w, err)
		return record, nil, false
	}
	ancestor := row
	linked := false
	workspace := ""
	for i := 0; i < 100 && ancestor != nil; i++ {
		if !ownsSession(record.Owner.ID, ancestor) {
			break
		}
		match := record.WorkspaceID == ancestor.ID
		for _, l := range record.Links {
			if l.WorkspaceID == ancestor.ID {
				match = true
			}
		}
		if match {
			linked = true
			workspace = ancestor.ID
			break
		}
		if ancestor.ParentSessionID == nil {
			break
		}
		ancestor, err = db.GetSession(r.Context(), a.dbPool, *ancestor.ParentSessionID)
		if err != nil {
			blackboardError(w, err)
			return record, nil, false
		}
	}
	if !linked {
		writeError(w, 404, "Blackboard not found in this workspace.")
		return record, nil, false
	}
	service := a.blackboardSessionService(record, row.ID, workspace)
	for _, board := range blackboard.ReferencedBoards(record) {
		var check any
		if err := service.Board.Call(r.Context(), "blerg_board_get", map[string]any{"board_id": board}, &check); err != nil {
			blackboardError(w, err)
			return record, nil, false
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	return record, row, true
}
func (a *API) blackboardSessionService(record blackboard.Record, sid, workspace string) blackboard.Service {
	adapter := blackboardBoardFunc(func(ctx context.Context, tool string, args map[string]any, out any) error {
		board, _ := args["board_id"].(string)
		row, err := db.GetSession(ctx, a.dbPool, sid)
		if err != nil {
			return err
		}
		if row == nil || !ownsSession(record.Owner.ID, row) {
			return blackboard.ErrForbidden
		}
		level, err := a.effectiveBoardLevel(ctx, row, board)
		if err != nil {
			return err
		}
		proof, ok := level.proof()
		if !ok || !mcpgw.BoardLevelAllows(level.Level, tool) {
			return blackboard.ErrForbidden
		}
		caller := a.sessionBoardCaller()
		if caller == nil {
			return blackboard.CheckFailed("board_gateway_missing", "board.gateway", "not configured")
		}
		data, _ := json.Marshal(args)
		raw, err := caller.CallBoard(ctx, mcpgw.BoardCall{Proof: proof, BoardID: board, SessionRef: sid, Tool: tool, Arguments: data, ReadOnly: true})
		if err != nil {
			return blackboardBoardFailure(err)
		}
		if e := boardToolResult(raw, out); e != nil {
			if e.Status == 404 {
				return blackboard.ErrNotFound
			}
			if e.Status == 401 || e.Status == 403 {
				return blackboard.ErrForbidden
			}
			return e
		}
		if tool == "blerg_card_search" || tool == "blerg_card_get" {
			questions, err := a.blackboardQuestions(ctx, workspace, record.Owner.ID, board)
			if err != nil {
				return err
			}
			switch value := out.(type) {
			case *blackboard.CardPage:
				for i := range value.Cards {
					value.Cards[i].NeedsYou = questions[value.Cards[i].Number]
				}
			case *blackboard.Card:
				value.NeedsYou = questions[value.Number]
			}
		}
		return nil
	})
	return blackboard.Service{Store: a.blackboardStore(), BlackboardID: record.ID, Board: adapter, ReadScope: mcpgw.WithBoardReadScope}
}
func (a *API) HandleBlackboardEvent(w http.ResponseWriter, r *http.Request) {
	var record blackboard.Record
	var actor blackboard.Actor
	if _, ok := a.verifyBrowserToken(a.rawBearer(r)); ok {
		rec, p, allowed := a.blackboardAccess(w, r)
		if !allowed {
			return
		}
		record = rec
		actor = blackboard.Actor{ID: p.Sub, Kind: p.Kind}
	} else {
		rec, row, ok := a.blackboardSessionAccess(w, r, r.PathValue("id"))
		if !ok {
			return
		}
		record = rec
		actor = blackboard.Actor{ID: row.ID, Kind: "agent"}
	}
	var input blackboard.EventInput
	if !decodeBlackboard(w, r, &input) {
		return
	}
	event, err := (blackboard.Service{Store: a.blackboardStore(), BlackboardID: record.ID}).RecordEvent(r.Context(), actor, input)
	if err != nil {
		blackboardError(w, err)
		return
	}
	if actor.Kind == "agent" {
		writeJSON(w, 201, map[string]any{"untrusted_data": event})
		return
	}
	writeJSON(w, 201, event)
}
func (a *API) HandleBlackboardTimeline(w http.ResponseWriter, r *http.Request) {
	record, _, ok := a.blackboardAccess(w, r)
	if !ok {
		return
	}
	offset, limit, ok := blackboardPage(w, r)
	if !ok {
		return
	}
	history, err := a.blackboardStore().LoadTimelinePage(r.Context(), record.ID, r.PathValue("milestone"), offset, limit)
	if err != nil {
		blackboardError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"events": history.Events, "snapshots": history.Snapshots, "next_cursor": blackboardCursor(offset, limit, history.Total)})
}
func blackboardJobFailure(stage string, err error) {
	failurelog.Default.Event("blackboard_job:"+stage, slog.LevelError, "component", "runner", "stage", stage, "cause", failurelog.Cause(err))
}

// RunBlackboardSnapshots observes once at startup and hourly thereafter. The
// durable UTC-day key prevents duplicates across servers and restarts; no backfill.
func (a *API) RunBlackboardSnapshots(ctx context.Context) {
	if a.dbPool == nil {
		return
	}
	a.snapshotBlackboards(ctx)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.snapshotBlackboards(ctx)
		}
	}
}
func (a *API) snapshotBlackboards(ctx context.Context) {
	cursor := ""
	for {
		rows, err := a.dbPool.Query(ctx, `SELECT id FROM blackboards WHERE id>$1 ORDER BY id LIMIT 100`, cursor)
		if err != nil {
			blackboardJobFailure("scan", err)
			return
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			blackboardJobFailure("scan", err)
			return
		}
		if len(ids) == 0 {
			return
		}
		for _, id := range ids {
			cursor = id
			record, err := a.blackboardStore().LoadPartition(ctx, id, blackboard.Partition{Kind: blackboard.PartitionCore})
			if err != nil {
				blackboardJobFailure("load", err)
				continue
			}
			workspace := record.WorkspaceID
			if workspace == "" && len(record.Links) > 0 {
				workspace = record.Links[0].WorkspaceID
			}
			service := a.blackboardSessionService(record, workspace, workspace)
			for _, m := range record.Milestones {
				if m.FrozenAt != nil || m.Stage == "Finalizing" {
					continue
				}
				step, cancel := context.WithTimeout(ctx, 30*time.Second)
				_, err := service.SnapshotDay(step, m.ID)
				cancel()
				if err != nil {
					blackboardJobFailure("observation", err)
				}
			}
		}
	}
}
