package server

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Only the existing cluster is addressable in this release. Never accept an
// unknown ID and accidentally execute it on the install's default executor.
func (a *API) runtimeTargetProblem(ctx context.Context, runtime, id string) *APIError {
	if id == "" {
		return nil
	}
	target := db.RuntimeTarget{ID: db.DefaultRuntimeTargetID, Kind: "cluster"}
	if a.dbPool != nil {
		var err error
		target, err = db.GetRuntimeTarget(ctx, a.dbPool, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return apiErrorf(http.StatusUnprocessableEntity, "unknown runtime target")
		}
		if err != nil {
			return apiErrorf(http.StatusServiceUnavailable, "runtime target is unavailable")
		}
	}
	if id != target.ID || target.Kind != "cluster" {
		return apiErrorf(http.StatusUnprocessableEntity, "unknown runtime target")
	}
	if runtime != "" && runtime != "cluster" && runtime != "auto" {
		return apiErrorf(http.StatusUnprocessableEntity, "runtime target requires the cluster runtime")
	}
	return nil
}

func clusterTargetID(runtime string) string {
	if runtime == "cluster" {
		return db.DefaultRuntimeTargetID
	}
	return ""
}

// The seed's config is an empty override: the existing JobManager still owns
// installation configuration and credentials. Never project those secrets.
func (a *API) defaultRuntimeTarget(ctx context.Context) (db.RuntimeTarget, *APIError) {
	if a.dbPool == nil {
		return db.RuntimeTarget{ID: db.DefaultRuntimeTargetID, Kind: "cluster", Name: "Cluster", IsDefault: true}, nil
	}
	target, err := db.GetDefaultRuntimeTarget(ctx, a.dbPool)
	if err != nil {
		return db.RuntimeTarget{}, apiErrorf(http.StatusServiceUnavailable, "runtime target is unavailable")
	}
	return target, nil
}

func nonemptyTarget(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}
