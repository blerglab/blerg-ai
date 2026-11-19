package db

import (
	"context"
	"encoding/json"
)

// DefaultRuntimeTargetID names the install's existing cluster. Desktop runtimes
// keep their daemon identity and never acquire a cluster target.
const DefaultRuntimeTargetID = "cluster"

type RuntimeTarget struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Config    json.RawMessage `json:"config"`
	IsDefault bool            `json:"is_default"`
}

func GetDefaultRuntimeTarget(ctx context.Context, conn DBTX) (RuntimeTarget, error) {
	var t RuntimeTarget
	err := conn.QueryRow(ctx, `SELECT id,kind,name,config,is_default FROM runtime_targets WHERE is_default`).Scan(&t.ID, &t.Kind, &t.Name, &t.Config, &t.IsDefault)
	return t, err
}

func GetRuntimeTarget(ctx context.Context, conn DBTX, id string) (RuntimeTarget, error) {
	var t RuntimeTarget
	err := conn.QueryRow(ctx, `SELECT id,kind,name,config,is_default FROM runtime_targets WHERE id=$1`, id).Scan(&t.ID, &t.Kind, &t.Name, &t.Config, &t.IsDefault)
	return t, err
}
