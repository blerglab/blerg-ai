package daemon

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceCapabilitiesMissingConfigurationRefuses(t *testing.T) {
	for _, service := range []*LocalWorkspaceService{nil, {}, {HostExecutable: "/missing-helper"}} {
		if got := service.Capabilities(context.Background()); got != (protocol.WorkspaceCapabilities{}) {
			t.Fatal("incomplete runtime advertised", got)
		}
	}
}
