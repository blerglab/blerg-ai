package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func takeClusterWorkspace() (*clusterworkspace.Plan, error) {
	raw, present := os.LookupEnv(clusterworkspace.Env)
	if !present {
		return nil, nil
	}
	_ = os.Unsetenv(clusterworkspace.Env)
	if len(raw) > 1<<20 {
		return nil, clusterworkspace.ErrPlan
	}
	var p clusterworkspace.Plan
	if json.Unmarshal([]byte(raw), &p) != nil || p.Validate() != nil {
		return nil, clusterworkspace.ErrPlan
	}
	return &p, nil
}

type workspaceSender struct {
	stopMu sync.Mutex
	stopID string
	*daemon.WSClient
	runtime  *clusterworkspace.Runtime
	requests *clusterRequests
}

func (s *workspaceSender) Send(v any) error {
	if ended, ok := v.(protocol.SessionEnded); ok && s.runtime != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
		defer cancel()
		if s.requests != nil {
			if err := s.requests.Close(ctx); err != nil {
				s.runtime.FenceReaders()
			}
		}
		s.runtime.Stop(ctx)
		results, revision := s.runtime.Snapshot()
		ended.WorkspaceResults = results
		s.stopMu.Lock()
		stopID := s.stopID
		s.stopMu.Unlock()
		sendCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer done()
		return s.SendBounded(sendCtx, struct {
			protocol.SessionEnded
			Revision int64  `json:"workspace_revision"`
			StopID   string `json:"workspace_stop_id,omitempty"`
		}{ended, revision, stopID})
	}
	return s.WSClient.Send(v)
}

func prepareClusterDisk(cfg Config) (*clusterworkspace.DiskGit, *clusterworkspace.Runtime, error) {
	if cfg.Workspace == nil {
		return nil, nil, nil
	}
	if cfg.ClusterWriters == nil || cfg.WorkspaceErr != nil || cfg.Workspace.SessionID != cfg.SessionID || cfg.Repo != "" || cfg.GitURL != "" || cfg.GitToken != "" || cfg.NoRepo || cfg.NewRepo {
		return nil, nil, clusterworkspace.ErrPlan
	}
	// Retained snapshots cannot live in the agent's writable HOME. The trusted
	// adapter supplies storage excluded by its agent mount/UID boundary; mode
	// 0700 alone is not protection from another process running as its owner.
	relative, err := filepath.Rel(cfg.Home, cfg.ClusterPrivateRoot)
	if cfg.ClusterPrivateRoot == "" || !filepath.IsAbs(cfg.ClusterPrivateRoot) || err != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))) {
		return nil, nil, clusterworkspace.ErrPlan
	}
	disk, err := clusterworkspace.NewDiskGitStorage(filepath.Join(cfg.Home, "projects"), cfg.ClusterPrivateRoot, cfg.SessionID, !cfg.Workspace.UnboundedStorage, cfg.Workspace.Identities...)
	if err != nil {
		return nil, nil, err
	}
	rt, err := clusterworkspace.New(*cfg.Workspace, disk)
	if err != nil {
		disk.Close()
		return nil, nil, err
	}
	if cfg.ClusterWriters != nil {
		if err := rt.SetWriters(cfg.ClusterWriters); err != nil {
			disk.Close()
			return nil, nil, err
		}
	}
	if cfg.Supervisor != nil {
		if err := rt.SetSupervisor(cfg.Supervisor, cfg.SupervisorLaunch); err != nil {
			disk.Close()
			return nil, nil, err
		}
	}
	return disk, rt, nil
}

func clusterWorkspaceGuide(plan clusterworkspace.Plan) string {
	var guide strings.Builder
	guide.WriteString("Workspace repositories (commit changes in each affected repository):\n")
	for _, m := range plan.Manifest.Members {
		guide.WriteString("- " + m.ID + ": " + path.Join(clusterworkspace.PublicRoot, m.Path) + " (wip/" + plan.SessionID + ")\n")
	}
	guide.WriteString("Start cluster children with session start --repo <member-id-or-path> (repeat for a subset) or --all-repos. Each child uses its own wip/<child-id> branches.\n\n")
	return guide.String()
}

type clusterWorkspaceReport struct {
	Type      string                           `json:"type"`
	SessionID string                           `json:"session_id"`
	Results   []protocol.WorkspaceMemberResult `json:"results"`
	Revision  int64                            `json:"revision"`
}

func workspaceReport(runtime *clusterworkspace.Runtime, id string) clusterWorkspaceReport {
	results, revision := runtime.Snapshot()
	return clusterWorkspaceReport{Type: "cluster_workspace_results", SessionID: id, Results: results, Revision: revision}
}

func workspaceCommandLauncher(ctx context.Context, rt *clusterworkspace.Runtime, start func(*exec.Cmd) error) func(*exec.Cmd) error {
	if rt == nil || start == nil {
		return start
	}
	return func(cmd *exec.Cmd) error { return rt.StartWriter(ctx, func() error { return start(cmd) }) }
}

// Shared by the shipped Run path and agent-start regression.
func newPodAgentHost(sender daemon.Sender, cfg Config, rt *clusterworkspace.Runtime, services func() ClusterServices, options daemon.AgentHostConfig) *daemon.AgentHost {
	options.AttachmentsStateDir = "/home/agent/.blerg-runner-daemon"
	options.PrepareHostWorkspace = clusterAgentWorkspace(cfg, rt, services)
	return daemon.NewAgentHost(sender, options)
}

// The pod has already prepared this exact runtime. AgentHost's default host
// admission cannot resolve a cluster manifest; it must consume the trusted pod
// reader without substituting a local directory or allocating another runtime.
func clusterAgentWorkspace(cfg Config, rt *clusterworkspace.Runtime, services func() ClusterServices) func(context.Context, protocol.SpawnSession) (string, daemon.WorkspaceExplorer, error) {
	return func(ctx context.Context, msg protocol.SpawnSession) (string, daemon.WorkspaceExplorer, error) {
		if ctx.Err() != nil || cfg.Workspace == nil || rt == nil || !rt.Prepared() || rt.AdmissionFenced() || msg.SessionID != cfg.SessionID || msg.Sandbox || msg.Workspace == nil || msg.Workspace.Runtime != "cluster" || !reflect.DeepEqual(*msg.Workspace, cfg.Workspace.Manifest) {
			return "", nil, errors.New("prepared cluster workspace reader authority unavailable")
		}
		cwd := clusterWorkspaceDirectory(filepath.Join(cfg.Home, "projects"), cfg.Workspace.Manifest)
		reader, ok := services().(daemon.WorkspaceExplorer)
		if !ok || reader == nil || msg.ProjectPath != cwd {
			return "", nil, errors.New("prepared cluster workspace reader authority unavailable")
		}
		return cwd, reader, nil
	}
}

// Pre-hello failures are carried by the bounded Kubernetes termination message.
// They contain fixed identifiers only, never an underlying error or environment.
type ClusterStartupError struct{ Stage string }

func (e *ClusterStartupError) Error() string { return "cluster runtime startup refused at " + e.Stage }
func RecordClusterStartupFailure(err error) {
	var startup *ClusterStartupError
	if !errors.As(err, &startup) {
		return
	}
	stage := startup.Stage
	if stage != "bootstrap" && stage != "storage" {
		return
	}
	raw, _ := json.Marshal(map[string]string{"stage": stage, "reason": "runtime_startup_refused"})
	_ = os.WriteFile("/dev/termination-log", raw, 0600)
}

func clusterWorkspaceDirectory(root string, manifest protocol.WorkspaceManifest) string {
	return filepath.Join(root, manifest.Members[0].Path)
}
