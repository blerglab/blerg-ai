package server

import (
	"context"
	"errors"

	"github.com/blerglab/blerg-ai/runner/internal/models"
)

// Native catalog acquisition is engine-specific; caching, persistence and HTTP
// delivery are shared. A future engine registers its bounded probe here.
type clusterModelProvider struct {
	probe     func(context.Context, *JobManager, string, coreProof, bool) (models.List, error)
	safeError func(error) string
}

var clusterModelProviders = map[string]clusterModelProvider{
	"codex": {
		probe: func(ctx context.Context, j *JobManager, account string, proof coreProof, force bool) (models.List, error) {
			return j.codexModels(ctx, account, proof, force)
		},
		safeError: func(err error) string {
			if errors.Is(err, ErrClusterCodexCatalogAuth) {
				return ErrClusterCodexCatalogAuth.Error()
			}
			if errors.Is(err, errNoLivenessProof) {
				return errNoLivenessProof.Error()
			}
			return ErrClusterCodexCatalog.Error()
		},
	},
}

func clusterModelProviderFor(engine string) (clusterModelProvider, bool) {
	p, ok := clusterModelProviders[models.NormalizeEngine(engine)]
	return p, ok
}
func clusterModelErrorMessage(engine string, err error) string {
	if p, ok := clusterModelProviderFor(engine); ok && p.safeError != nil {
		return p.safeError(err)
	}
	return "Could not refresh the model catalog. Check the engine credential and retry."
}
