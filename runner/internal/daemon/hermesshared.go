package daemon

import (
	"context"
	"errors"

	"github.com/blerglab/blerg-ai/runner/internal/models"
)

// Entry points for a Hermes provider configuration that is not a file under
// this machine's home: the cluster's shared provider fragment, held by the
// runner server. They run the same parser, probe and local rule the desktop
// uses on ~/.hermes/config.yaml, so a model offered for the cluster carries
// the routing ID and display name it would have on a desktop.

// ProbeHermesModelsConfig lists the models of every endpoint data (a Hermes
// config.yaml document) routes to, exactly as the desktop probe lists those of
// ~/.hermes/config.yaml. Nothing in data is logged or returned in an error.
func ProbeHermesModelsConfig(ctx context.Context, data []byte) ([]models.Model, error) {
	if len(data) == 0 {
		return nil, errNoModelList
	}
	cfg, err := parseHermesRouteConfig(data)
	if err != nil {
		return nil, errors.New("hermes model configuration is unreadable or unsupported")
	}
	return probeHermesRoutes(ctx, cfg, newHermesProbeClient())
}

// HermesConfigLocal applies the configuration half of the desktop's
// local-endpoint rule to data: every endpoint it routes to is on loopback or
// a private network, and no provider in it carries a key. Credentials kept
// elsewhere (a .env, the environment) are the caller's to check. Anything
// unparseable or unclear is not local.
func HermesConfigLocal(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	cfg, err := parseHermesRouteConfig(data)
	return err == nil && hermesRoutesLocal(cfg)
}

// RefreshLocalEngines drops the cached local-engine answer, so the next hello
// or heartbeat re-reads the configuration. A session pod calls it after it
// has written its Hermes configuration.
func RefreshLocalEngines() { resetLocalEnginesCache() }

// HermesProvidersEnvVar carries the cluster's shared Hermes provider fragment
// (a config.yaml document) into a Hermes session pod, from the per-session
// Secret. The pod merges it into ~/.hermes/config.yaml and unsets it at once.
const HermesProvidersEnvVar = "BLERG_RUNNER_HERMES_PROVIDERS"
