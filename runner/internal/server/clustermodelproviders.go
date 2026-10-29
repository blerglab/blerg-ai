package server

import (
	"context"
	"errors"

	"github.com/blerglab/blerg-ai/runner/internal/codexauth"

	"github.com/blerglab/blerg-ai/runner/internal/models"
)

// Native catalog acquisition is engine-specific; caching, persistence and HTTP
// delivery are shared. A future engine registers its bounded probe here.
type clusterModelProvider struct {
	probe         func(context.Context, *JobManager, string, coreProof, bool) (models.List, error)
	probeOperator func(context.Context, *JobManager, bool) (models.List, error)
	safeError     func(error) string
	reconnect     func(error) bool
	// available, when set, says whether this cluster has a catalog for the
	// engine at all; without one the engine keeps the public-or-none path.
	available func(*JobManager) bool
}

var clusterModelProviders = map[string]clusterModelProvider{
	"codex": {
		reconnect: func(err error) bool {
			return errors.Is(err, ErrClusterCodexCatalogAuth) || errors.Is(err, codexauth.ErrDedicatedLogin)
		},
		probeOperator: func(ctx context.Context, j *JobManager, force bool) (models.List, error) {
			return j.codexOperatorModels(ctx, force)
		},
		probe: func(ctx context.Context, j *JobManager, account string, proof coreProof, force bool) (models.List, error) {
			return j.codexModels(ctx, account, proof, force)
		},
		safeError: func(err error) string {
			if errors.Is(err, codexauth.ErrDedicatedLogin) {
				return codexauth.ErrDedicatedLogin.Error()
			}
			if errors.Is(err, ErrClusterCodexCatalogAuth) {
				return ErrClusterCodexCatalogAuth.Error()
			}
			if errors.Is(err, errNoLivenessProof) {
				return errNoLivenessProof.Error()
			}
			return ErrClusterCodexCatalog.Error()
		},
	},
	// Hermes on the cluster lists the models of the shared provider
	// configuration (hermesproviders.go): the same for every account, probed
	// from the runner with the desktop's probe.
	"hermes": {
		available: func(j *JobManager) bool { return j.hermesSharedConfigured() },
		probeOperator: func(ctx context.Context, j *JobManager, _ bool) (models.List, error) {
			return j.hermesSharedModels(ctx)
		},
		probe: func(ctx context.Context, j *JobManager, _ string, _ coreProof, _ bool) (models.List, error) {
			return j.hermesSharedModels(ctx)
		},
		safeError: func(error) string { return ErrClusterHermesCatalog.Error() },
	},
}

func clusterModelProviderFor(engine string) (clusterModelProvider, bool) {
	p, ok := clusterModelProviders[models.NormalizeEngine(engine)]
	return p, ok
}

// clusterModelProviderOn is clusterModelProviderFor on a given cluster: a
// provider whose catalog depends on configuration counts only where it is
// configured.
func clusterModelProviderOn(engine string, j *JobManager) (clusterModelProvider, bool) {
	p, ok := clusterModelProviderFor(engine)
	if ok && p.available != nil && (j == nil || !p.available(j)) {
		return clusterModelProvider{}, false
	}
	return p, ok
}
func clusterModelErrorMessage(engine string, err error) string {
	if p, ok := clusterModelProviderFor(engine); ok && p.safeError != nil {
		return p.safeError(err)
	}
	return "Could not refresh the model catalog. Check the engine credential and retry."
}
