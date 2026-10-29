package server

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

// clusterConsumer joins P1's authorized selection to P3's retained-volume and
// credential lifecycle. It never invents a physical root before a pod reports it.
type clusterConsumer struct{ api *API }

func (a *API) ConfigureClusterWorkspaces() {
	if j := a.hub.JobManager(); j != nil {
		j.launchHub = a.hub
	}
	a.SetWorkspaceClusterConsumer(&clusterConsumer{api: a})
}
func (c *clusterConsumer) Capabilities() protocol.WorkspaceCapabilities {
	return c.api.hub.JobManager().workspaceCapabilities()
}
func (j *JobManager) workspaceCapabilities() protocol.WorkspaceCapabilities {
	if j == nil || j.historyPool == nil {
		return protocol.WorkspaceCapabilities{}
	}
	return protocol.WorkspaceCapabilitiesFor("cluster", "linux")
}
func (*clusterConsumer) Prepare(_ context.Context, m protocol.WorkspaceManifest) (protocol.WorkspaceRoot, error) {
	if m.Runtime != "cluster" || len(m.Members) == 0 {
		return protocol.WorkspaceRoot{}, errClusterWorkspace
	}
	return protocol.WorkspaceRoot{}, nil
}
func (c *clusterConsumer) Start(ctx context.Context, m protocol.WorkspaceManifest, req WorkspaceLaunchRequest) (string, error) {
	a := c.api
	jm := a.hub.JobManager()
	p := req.principal
	if a.dbPool == nil || jm == nil || m.Validate() != nil || m.Runtime != "cluster" || m.OwnerID != p.Sub || m.Digest != workspaceselection.Digest(m) {
		return "", errClusterWorkspace
	}
	proof := proofFromPrincipal(p)
	if jm.personalCredentialsWired() && !proof.valid() {
		return "", errNoLivenessProof
	}
	id := newUUID()
	members, err := clusterLaunchMembers(jm, m)
	if err != nil {
		return "", err
	}
	if err = db.UpsertDaemon(ctx, a.dbPool, clusterDaemonID, "cluster", "runner", ""); err != nil {
		return "", err
	}
	if err = db.InsertClusterSessionAs(ctx, a.dbPool, id, clusterDaemonID, "starting", clusterworkspace.PublicRoot, "", req.Title, req.Model, sessionOriginFor(p.Sub, false, "")); err != nil {
		return "", err
	}
	plan := clusterWorkspaceStartPlan()
	if !req.selectionStartedAt.IsZero() {
		plan[0].At = req.selectionStartedAt.UTC().Format(time.RFC3339Nano)
	}
	broadcastSessionCreated(ctx, a.hub, a.dbPool, id)
	beginStart(ctx, a.hub, a.dbPool, id, "cluster", plan)
	advanceStart(ctx, a.hub, a.dbPool, id, true, stage("selection", "", protocol.StageStateDone, "Hosted selection authorized"), stage("manifest", "", protocol.StageStateActive, "Saving immutable selection"))
	fail := func(err error) (string, error) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		clusterJobCreateFailed(cleanup, a.hub, a.dbPool, id, "Workspace launch failed; inspect the failed launch stage")
		a.markClusterStartFailed(cleanup, id, "workspace preparation failed")
		_ = db.RevokeBoardTokensForSession(cleanup, a.dbPool, id)
		return "", err
	}
	for _, save := range []func() error{
		func() error { return db.SetSessionKind(ctx, a.dbPool, id, "agent") },
		func() error { return db.SetSessionNoOperatorFallback(ctx, a.dbPool, id) },
		func() error { return db.SetSessionEngine(ctx, a.dbPool, id, req.Engine) },
		func() error { return db.InsertSessionWorkspace(ctx, a.dbPool, id, m) },
	} {
		if err = save(); err != nil {
			return fail(err)
		}
	}
	recordInteraction(ctx, a.dbPool, id, req.Interaction)
	recordLaunchAuthSession(ctx, a.dbPool, id, proof.SessionID)
	recordLaunchEffort(ctx, a.dbPool, id, req.Effort)
	if proof.TokenID != "" {
		if err = db.SetSessionTokenID(ctx, a.dbPool, id, proof.TokenID); err != nil {
			return fail(err)
		}
	}
	advanceStart(ctx, a.hub, a.dbPool, id, true, stage("manifest", "", protocol.StageStateDone, "Immutable manifest saved"))
	env := withClusterSessionToken(ctx, a.dbPool, id, nil)
	if env[sessionTokenEnv] == "" {
		return fail(errClusterWorkspace)
	}
	spec := SessionJobSpec{SessionID: id, Title: req.Title, InitialPrompt: req.Prompt, Engine: req.Engine, Model: req.Model, Effort: req.Effort, Interaction: req.Interaction, SpawningAccountID: p.Sub, AuthSessionID: proof.SessionID, TokenID: proof.TokenID, ExtraEnv: env, Workspace: &m, WorkspaceMembers: members, NoOperatorFallback: true}
	jm.launchStage(ctx, id, "plugins_resolve", protocol.StageStateActive, "Resolving optional launch plugins")
	jm.ResolvePlugins(ctx, &spec)
	jm.launchStage(ctx, id, "plugins_resolve", protocol.StageStateDone, "Launch plugins resolved or safely omitted")

	if err = jm.CreateSessionJob(spec); err != nil { //nolint:contextcheck // JobManager owns bounded launch and rollback after HTTP disconnect.
		return fail(err)
	}
	clusterJobCreated(ctx, a.hub, a.dbPool, id, a.startWatchEvery())
	return id, nil
}
func clusterLaunchMembers(jm *JobManager, m protocol.WorkspaceManifest) ([]clusterworkspace.Member, error) {
	members := make([]clusterworkspace.Member, 0, len(m.Members))
	for _, v := range m.Members {
		if validateRepoName(v.Project) != nil || v.Project == "" || v.Destination != v.Project {
			return nil, errClusterWorkspace
		}
		raw := jm.CloneURLFor(v.Provider, v.Project)
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil {
			return nil, errClusterWorkspace
		}
		// Personal push authority is checked per member by withClusterWorkspace.
		// Anonymous public clone stays possible and never receives operator authority.
		members = append(members, clusterworkspace.Member{ID: v.ID, Provider: v.Provider, CloneURL: raw, PushURL: raw, DestinationIdentity: v.Destination, CredentialHost: u.Host})
	}
	return members, nil
}
func (c *clusterConsumer) StartChild(ctx context.Context, parent, child protocol.WorkspaceManifest, spec SessionJobSpec) error {
	if c.api.dbPool == nil || c.api.hub.JobManager() == nil || protocol.ValidateWorkspaceChild(parent, child) != nil || spec.SpawningAccountID != child.OwnerID {
		return errClusterWorkspace
	}
	// Re-read the parent's private immutable contexts, never reconstruct child
	// authority from mutable repository config or inherit the parent's token.
	row, err := db.GetSession(ctx, c.api.dbPool, spec.SessionID)
	if err != nil || row == nil || row.ParentSessionID == nil {
		return errClusterWorkspace
	}
	var raw []byte
	if err := c.api.dbPool.QueryRow(ctx, `SELECT contexts FROM cluster_workspace_storage WHERE session_id=$1 AND launch_generation=$2 AND owner_id=$3`, *row.ParentSessionID, parent.LaunchGeneration, parent.OwnerID).Scan(&raw); err != nil {
		return err
	}
	var contexts []clusterworkspace.Member
	if json.Unmarshal(raw, &contexts) != nil {
		return errClusterWorkspace
	}
	members, err := ClusterWorkspaceChild(parent, child, contexts)
	if err != nil {
		return err
	}
	spec.Workspace = &child
	spec.WorkspaceMembers = members
	return c.api.hub.JobManager().CreateSessionJob(spec) //nolint:contextcheck // JobManager owns bounded launch/rollback.
}
