package daemon

import (
	"io"
	"slices"
	"testing"
)

func TestWorkspaceStatusDisablesIndexPreload(t *testing.T) {
	snapshot := &workspaceStatusSnapshot{}
	// Index loading and preload are separate Git thread pools. Restricting only
	// index.threads still lets threaded lstat exhaust the hardened process budget.
	for _, platform := range []string{"linux", "darwin"} {
		options := snapshot.optionsForPlatform(&pinnedGitDir{}, io.Discard, platform)
		for _, setting := range []string{"index.threads=1", "core.preloadIndex=false"} {
			if !slices.Contains(options.Config, setting) {
				t.Errorf("%s missing resource policy %q", platform, setting)
			}
		}
	}
}
