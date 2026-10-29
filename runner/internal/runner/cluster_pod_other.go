//go:build !linux

package runner

import "github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"

func ConfigureClusterRuntime(cfg *Config) error {
	if cfg.Workspace != nil {
		return clusterworkspace.ErrPlan
	}
	return cfg.WorkspaceErr
}
func ServeClusterWriter([]string) (bool, int) { return false, 0 }

func podCapabilityServicesReady(Config) bool { return false }
