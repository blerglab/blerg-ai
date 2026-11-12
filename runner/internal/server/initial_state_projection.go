package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type browserProjectionKey struct{}
type browserProjection struct {
	account string
	pool    *pgxpool.Pool
	*db.BrowserProjection
}

func projectionFor(ctx context.Context, pool *pgxpool.Pool, account string) *db.BrowserProjection {
	p, _ := ctx.Value(browserProjectionKey{}).(*browserProjection)
	if p != nil && p.pool == pool && p.account == account {
		return p.BrowserProjection
	}
	return nil
}

func (h *Hub) projectionDesktop(ctx context.Context, account, id string) (*db.DesktopRow, error) {
	if p := projectionFor(ctx, h.desktopPool, account); p != nil {
		return p.Desktops[id], nil
	}
	return db.GetDesktop(ctx, h.desktopPool, id)
}
func (h *Hub) projectionSession(ctx context.Context, account, id string) (*db.SessionRow, error) {
	if p := projectionFor(ctx, h.desktopPool, account); p != nil {
		return p.Sessions[id], nil
	}
	return db.GetSession(ctx, h.desktopPool, id)
}
func (h *Hub) projectionWorkspace(ctx context.Context, account, id string) (*db.SessionWorkspace, error) {
	if p := projectionFor(ctx, h.desktopPool, account); p != nil {
		return p.Workspaces[id], nil
	}
	return db.GetSessionWorkspace(ctx, h.desktopPool, id)
}
func (h *Hub) projectionOperation(ctx context.Context, account, id string) (*db.MembershipOperation, error) {
	if p := projectionFor(ctx, h.desktopPool, account); p != nil {
		return p.Operations[id], nil
	}
	return db.OpenMembershipOperation(ctx, h.desktopPool, id)
}

// Log only typed causes: database errors can contain SQL, paths and credentials.
func logInitialStateFailure(stage string, err error) {
	cause := "database_unavailable"
	var pg *pgconn.PgError
	var syntax *json.SyntaxError
	var decode *json.UnmarshalTypeError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		cause = "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		cause = "canceled"
	case errors.As(err, &syntax) || errors.As(err, &decode):
		cause = "decode"
	case errors.As(err, &pg):
		cause = "postgres_" + pg.Code
	}
	failurelog.Default.Event("browser_initial_state:"+stage+":"+cause, slog.LevelWarn, "component", "runner", "operation", "browser_initial_state", "stage", stage, "cause", cause)
}

func isInitialState(raw []byte) bool {
	var envelope struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &envelope) == nil && envelope.Type == "initial_state"
}

func initialStateMessageInfos(ctx context.Context, pool *pgxpool.Pool, rows []db.MessageRow, viewer string, sessions []db.SessionRow) ([]protocol.MessageInfo, error) {
	byID := map[string]*db.SessionRow{}
	for _, row := range sessions {
		byID[row.ID] = &row
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Kind == "ask" {
			ids = append(ids, row.ID)
		}
	}
	alerts := map[string]bool{}
	if len(ids) > 0 {
		result, err := pool.Query(ctx, `SELECT message_id::text FROM session_failure_alerts WHERE message_id=ANY($1::uuid[])`, ids)
		if err != nil {
			return nil, err
		}
		for result.Next() {
			var id string
			if err = result.Scan(&id); err != nil {
				result.Close()
				return nil, err
			}
			alerts[id] = true
		}
		err = result.Err()
		result.Close()
		if err != nil {
			return nil, err
		}
	}
	var lookupErr error
	owners := questionOwnersWithLookups(pool, rows, func(id string) (*db.SessionRow, error) {
		if row, ok := byID[id]; ok {
			return row, nil
		}
		row, err := db.GetSession(ctx, pool, id)
		byID[id] = row
		if err != nil && lookupErr == nil {
			lookupErr = err
		}
		return row, err
	}, func(id string) (bool, error) { return alerts[id], nil })
	if lookupErr != nil {
		return nil, lookupErr
	}
	return messageInfosWithOwners(rows, viewer, owners), nil
}
