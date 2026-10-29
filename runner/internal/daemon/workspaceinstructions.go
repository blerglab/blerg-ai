package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/agent/skills"
)

type runtimeInstructions struct {
	Skills []skills.Skill
	Agents []agent.AgentTypeDef
}

// WriteRuntimeInstructions is a fixed helper mode, executed as the engine UID
// inside the prepared runtime. VM source paths must never be opened on the host.
func WriteRuntimeInstructions(out io.Writer, root string) int {
	list, _ := skills.Discover(root, "/home/agent")
	raw, err := json.Marshal(runtimeInstructions{Skills: list, Agents: agent.DiscoverAgentTypes(root, "/home/agent")})
	if err != nil || len(raw) > 1<<20 {
		return 1
	}
	if _, err = out.Write(raw); err != nil {
		return 1
	}
	return 0
}
func (d *DockerWorkspaceContainer) discoverInstructions(ctx context.Context) (runtimeInstructions, error) {
	var result runtimeInstructions
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := d.Identity(ctx, d.Session, d.Launch); err != nil {
		return result, err
	}
	raw, err := d.command(ctx, "exec", "--user", sandboxUIDGID(), d.ID, workspaceExplorerBinary, "--public-root", sandboxWorkdir, "--instructions")
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(raw, &result)
	return result, err
}
func sandboxUIDGID() string { return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()) }
