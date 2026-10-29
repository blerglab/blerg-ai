package server

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Only bounded local reads and writes belong here. Never wait for a daemon RPC
// while holding its transport's exclusive ownership fence.
func (a *API) sessionReadFence(ctx context.Context, id string, fn func(context.Context) error) error {
	if a.dbPool == nil || !validSessionID(id) {
		return fn(ctx)
	}
	row, err := db.GetSession(ctx, a.dbPool, id)
	if err != nil {
		return err
	}
	if row == nil || derefOrEmpty(row.Runtime) == "cluster" {
		return fn(ctx)
	}
	return db.WithDesktopSnapshotFences(ctx, a.dbPool, []string{row.DaemonID}, fn)
}
