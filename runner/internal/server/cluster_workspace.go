package server

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errClusterWorkspace = errors.New("cluster workspace storage, credentials or launch binding is unavailable; retained work was not replaced")

// withClusterWorkspace is the server-side writer fence. Selection resolution is
// P1's responsibility; callers supply authorized per-member destinations only.
// The retained claim uses the storage mode bound at the first launch.
func (j *JobManager) withClusterWorkspace(spec SessionJobSpec) (launchErr error) {
	deadline := time.Now().Add(5 * time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	progress := &clusterLaunchProgress{j: j, session: spec.SessionID}
	progress.next(ctx, "authorization", "Checking immutable selection, owner and live authority")
	defer progress.finish(ctx, &launchErr)
	if !historyID.MatchString(spec.SessionID) || j.historyPool == nil || spec.Workspace == nil || spec.NoRepo || spec.NewRepo || spec.Repo != "" || spec.GitURL != "" {
		return errClusterWorkspace
	}
	if _, exists := spec.ExtraEnv[gitTokenKey]; exists {
		return errClusterWorkspace
	}
	if _, exists := spec.ExtraEnv[clusterworkspace.Env]; exists {
		return errClusterWorkspace
	}
	return db.WithSessionNativeHistoryLock(ctx, j.historyPool, spec.SessionID, func() error {
		w, e := db.GetSessionWorkspace(ctx, j.historyPool, spec.SessionID)
		if e != nil || w == nil || w.Manifest == nil || !reflect.DeepEqual(*w.Manifest, *spec.Workspace) || spec.SpawningAccountID != w.Manifest.OwnerID || !j.nativeHistorySessionCurrent(ctx, spec) {
			return errClusterWorkspace
		}
		if err := verifyCorePrincipal(ctx, j.CredentialClient, j.CoreURL, j.CoreInternalKey, spec.SpawningAccountID, spec.proof()); err != nil {
			return err
		}

		name := "blerg-workspace-" + spec.SessionID
		var expectedUID, launch, owner, expectedName, mode string
		var raw, identities []byte
		e = j.historyPool.QueryRow(ctx, `SELECT claim_uid,launch_generation,contexts,member_identities,owner_id,claim_name,storage_mode FROM cluster_workspace_storage WHERE session_id=$1`, spec.SessionID).Scan(&expectedUID, &launch, &raw, &identities, &owner, &expectedName, &mode)
		fresh := errors.Is(e, pgx.ErrNoRows)
		if e != nil && !fresh {
			return errClusterWorkspace
		}
		var class string
		if fresh {
			class = j.WorkspaceStorageClass
			mode = "bounded"
			if class == "" {
				mode = "standard"
				class = j.WorkspaceDefaultClass
			}
		} else if mode != "bounded" && mode != "standard" {
			return errClusterWorkspace
		}
		members := append([]clusterworkspace.Member(nil), spec.WorkspaceMembers...)
		if !fresh {
			if expectedName != name || owner != spec.SpawningAccountID || launch != w.Manifest.LaunchGeneration || json.Unmarshal(raw, &members) != nil {
				return errClusterWorkspace
			}
		} else if spec.Resume {
			return errClusterWorkspace
		}
		plan := clusterworkspace.Plan{UnboundedStorage: mode == "standard", PrepareDeadlineMillis: deadline.UnixMilli(), SessionID: spec.SessionID, Manifest: *w.Manifest, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: w.Manifest.LaunchGeneration, Generation: 1}, Members: members}
		plan.LayoutSources, e = clusterLayoutSources(ctx, j.historyPool, spec.SessionID, *w.Manifest)
		if e != nil {
			return e
		}
		if !fresh {
			if json.Unmarshal(identities, &plan.Identities) != nil || len(plan.Identities) != len(plan.Members) {
				return errClusterWorkspace
			}
		}
		// Bind contexts durably without persisting plaintext credentials. A caller's
		// token can never substitute for personal provider/host authorization.
		for i := range plan.Members {
			plan.Members[i].Token = ""
			if plan.Members[i].PushAuthorized {
				plan.Members[i].Token = "validation-only"
			}
		}
		if e := plan.Validate(); e != nil {
			return e
		}
		for i := range plan.Members {
			plan.Members[i].Token = ""
		}

		progress.next(ctx, "credentials", "Checking every member with independent repository authority")
		// Resolve and precheck EVERY member; never use the operator Git token.
		for i := range plan.Members {
			m := &plan.Members[i]
			kind, _ := j.gitCredentialFor(m.CloneURL)
			if kind != "" && kind == m.Provider {
				token, found, e := j.fetchPersonalCredential(ctx, spec.SpawningAccountID, kind, spec.proof())
				if e != nil {
					return e
				}
				if found {
					m.Token = string(token)
					m.PushAuthorized = true
				}
			}
			if m.PushAuthorized && m.Token == "" {
				return ErrNoPersonalCredential
			}
			checkSpec := spec
			checkSpec.ExtraEnv = map[string]string{gitTokenKey: m.Token}
			checkSpec.NoOperatorFallback = true
			if e := j.precheckRepositoryContext(ctx, checkSpec, m.CloneURL, nil, map[string]bool{gitTokenKey: true}, false); e != nil {
				return e
			}
		}
		if e := plan.Validate(); e != nil {
			return e
		}
		progress.next(ctx, "storage", "Allocating and verifying retained claim identity; binding is deferred to the consumer pod")
		claim, e := j.historyClaim(ctx, name)
		if e != nil {
			return e
		}
		if fresh && claim != nil {
			return errClusterWorkspace
		}
		if claim == nil && fresh {
			storage := map[string]any{"accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]string{"storage": "11520Mi"}}}
			if class != "" {
				storage["storageClassName"] = class
			}
			body := map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": name, "labels": map[string]string{"app": "blerg-workspace", "session": spec.SessionID, "account": spec.SpawningAccountID}}, "spec": storage}
			resp, e := j.createLaunchObject(ctx, "/api/v1/namespaces/"+j.Namespace+"/persistentvolumeclaims", body)
			if e != nil {
				return launchCreateFailure(e, errClusterWorkspace)
			}
			if resp.StatusCode != http.StatusCreated {
				failure := launchCreateFailure(jobSubmissionRejection(resp), errClusterWorkspace)
				_ = resp.Body.Close()
				return failure
			}
			_ = resp.Body.Close()
			claim, e = j.historyClaim(ctx, name)
			if e != nil {
				return errClusterWorkspace
			}
		}
		if claim == nil || claim.Metadata.Name != name || claim.Metadata.UID == "" || claim.Metadata.Deleted != nil || len(claim.Metadata.Owners) != 0 || claim.Metadata.Labels["app"] != "blerg-workspace" || claim.Metadata.Labels["session"] != spec.SessionID || claim.Metadata.Labels["account"] != spec.SpawningAccountID || (!fresh && claim.Metadata.UID != expectedUID) {
			return errClusterWorkspace
		}

		if fresh {
			stored := append([]clusterworkspace.Member(nil), plan.Members...)
			for i := range stored {
				stored[i].Token = ""
			}
			raw, e = json.Marshal(stored)
			if e != nil {
				return errClusterWorkspace
			}
			if _, e = j.historyPool.Exec(ctx, `INSERT INTO cluster_workspace_storage(session_id,launch_generation,claim_name,claim_uid,contexts,owner_id,storage_mode) VALUES($1,$2,$3,$4,$5,$6,$7)`, spec.SessionID, w.Manifest.LaunchGeneration, name, claim.Metadata.UID, raw, spec.SpawningAccountID, mode); e != nil {
				return errClusterWorkspace
			}
		}
		if !j.nativeHistorySessionCurrent(ctx, spec) {
			return errClusterWorkspace
		}
		if err := verifyCorePrincipal(ctx, j.CredentialClient, j.CoreURL, j.CoreInternalKey, spec.SpawningAccountID, spec.proof()); err != nil {
			return err
		}
		progress.next(ctx, "writer_fence", "Waiting for the previous Job and all pods to disappear")
		// Prove the old Job and ALL pods gone before dispatching a new writer.
		if e = j.fenceNativeHistoryWriter(ctx, spec); e != nil {
			return e
		}
		progress.next(ctx, "runtime_binding", "Revoking old authority and binding the next runtime generation")
		var generation int64
		e = db.WithRuntimeLaunchFence(ctx, j.historyPool, spec.SessionID, func(fenced context.Context) error {
			// Revoke old authority before advancing the workspace generation, under
			// the same distributed fence used by every authenticated runtime frame.
			if _, err := j.historyPool.Exec(fenced, `UPDATE board_tokens SET revoked_at=now() WHERE id IN(SELECT runtime_token_id FROM session_runtime WHERE session_id=$1 UNION ALL SELECT messaging_token_id FROM session_runtime WHERE session_id=$1)`, spec.SessionID); err != nil {
				return err
			}
			current, err := db.GetSessionWorkspace(fenced, j.historyPool, spec.SessionID)
			if err != nil || current == nil || current.Manifest == nil || !reflect.DeepEqual(*current.Manifest, *spec.Workspace) || current.Manifest.ValidateResults(current.Results) != nil || !j.nativeHistorySessionCurrent(fenced, spec) {
				return errClusterWorkspace
			}
			var storedContexts, currentIdentities []byte
			var currentUID, currentOwner, currentLaunch, currentName, currentMode string
			if err := j.historyPool.QueryRow(fenced, `SELECT contexts,member_identities,claim_uid,owner_id,launch_generation,claim_name,storage_mode FROM cluster_workspace_storage WHERE session_id=$1`, spec.SessionID).Scan(&storedContexts, &currentIdentities, &currentUID, &currentOwner, &currentLaunch, &currentName, &currentMode); err != nil {
				return err
			}
			stripped := append([]clusterworkspace.Member(nil), plan.Members...)
			for i := range stripped {
				stripped[i].Token = ""
			}
			var contexts []clusterworkspace.Member
			if currentMode != mode || json.Unmarshal(storedContexts, &contexts) != nil || !reflect.DeepEqual(contexts, stripped) || currentName != name || currentUID != claim.Metadata.UID || currentOwner != spec.SpawningAccountID || currentLaunch != current.Manifest.LaunchGeneration {
				return errClusterWorkspace
			}
			verifiedClaim, err := j.historyClaim(fenced, name)
			if err != nil || verifiedClaim == nil || verifiedClaim.Metadata.UID != currentUID || verifiedClaim.Metadata.Deleted != nil || len(verifiedClaim.Metadata.Owners) != 0 || verifiedClaim.Metadata.Labels["app"] != "blerg-workspace" || verifiedClaim.Metadata.Labels["session"] != spec.SessionID || verifiedClaim.Metadata.Labels["account"] != spec.SpawningAccountID {
				return errClusterWorkspace
			}
			if !fresh && (json.Unmarshal(currentIdentities, &plan.Identities) != nil || len(plan.Identities) != len(plan.Members)) {
				return errClusterWorkspace
			}
			plan.PreviousResults = current.Results
			if err := plan.Validate(); err != nil {
				return err
			}
			previous, _, err := db.GetWorkspaceRuntime(fenced, j.historyPool, spec.SessionID)
			if err != nil {
				return err
			}
			generation, err = db.BeginWorkspaceRuntime(fenced, j.historyPool, spec.SessionID, w.Manifest.LaunchGeneration, previous)
			return err
		})
		if e != nil {
			return e
		}
		plan.Runtime.Generation = generation
		workDir := path.Join(clusterworkspace.PublicRoot, plan.Manifest.Members[0].Path)
		if _, e = j.historyPool.Exec(ctx, `UPDATE sessions SET project_path=$2 WHERE id=$1`, spec.SessionID, workDir); e != nil {
			return e
		}
		spec.workspacePlan = &plan
		spec.workspaceClaim = name
		// Native conversation history is independent; workspace preservation does
		// not copy HOME or engine credentials into this volume.
		progress.done(ctx)
		return j.createSessionJobContext(ctx, spec)
	})
}

func clusterWorkspacePod(spec SessionJobSpec, pod map[string]any) error {
	if spec.workspacePlan == nil {
		return nil
	}
	if spec.workspaceClaim == "" {
		return errClusterWorkspace
	}
	containers, ok := pod["containers"].([]map[string]any)
	if !ok || len(containers) != 1 {
		return errClusterWorkspace
	}
	mounts, _ := containers[0]["volumeMounts"].([]map[string]any)
	mounts = append(mounts, map[string]any{"name": "workspace-projects", "mountPath": clusterworkspace.PublicRoot})
	resources, ok := containers[0]["resources"].(map[string]any)
	if !ok {
		return errClusterWorkspace
	}
	requests, ok := resources["requests"].(map[string]any)
	if !ok {
		return errClusterWorkspace
	}
	limits, ok := resources["limits"].(map[string]any)
	if !ok {
		return errClusterWorkspace
	}
	limit, err := clusterMemoryBytes(limits["memory"])
	if err != nil {
		return err
	}
	request, err := clusterMemoryBytes(requests["memory"])
	if err != nil {
		return err
	}
	maximum := protocol.DefaultWorkspaceBudget().GitMemoryBytes
	if limit > maximum {
		limit = maximum
	}
	if request > limit {
		request = limit
	}
	// One enforced cgroup bounds all Git descendants together. The whole runner
	// container shares this conservative ceiling until a dedicated Git cgroup
	// is integrated; a per-process ulimit alone is not an aggregate limit.
	limits["memory"] = strconv.FormatInt(limit, 10)
	requests["memory"] = strconv.FormatInt(request, 10)
	containers[0]["volumeMounts"] = mounts
	isolatedClusterPod(pod, containers[0])
	volumes, _ := pod["volumes"].([]map[string]any)
	pod["volumes"] = append(volumes, map[string]any{"name": "workspace-projects", "persistentVolumeClaim": map[string]string{"claimName": spec.workspaceClaim}})
	return nil
}

// Keep credential-bearing launch JSON out of literals and all public payloads.
func clusterWorkspaceSecret(spec SessionJobSpec, data map[string][]byte, env *[]map[string]any) error {
	if spec.workspacePlan == nil {
		return nil
	}
	raw, e := json.Marshal(spec.workspacePlan)
	if e != nil {
		return errClusterWorkspace
	}
	data[clusterworkspace.Env] = raw
	*env = append(*env, map[string]any{"name": clusterworkspace.Env, "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": sessionSecretName(spec.SessionID), "key": clusterworkspace.Env}}})
	return nil
}

// The private revision ledger and public result replacement commit together.
// This also rejects reordered reports from an older socket of the same pod.
func persistClusterWorkspaceResults(ctx context.Context, pool *pgxpool.Pool, id string, ref protocol.WorkspaceRuntimeRef, revision int64, results []protocol.WorkspaceMemberResult) error {
	return persistClusterWorkspaceReport(ctx, pool, id, ref, revision, results, false)
}

// Final reports seal outcomes in the same transaction as their revision. Late
// frames cannot rewrite settled evidence while Job or terminal cleanup retries.
func persistClusterWorkspaceReport(ctx context.Context, pool *pgxpool.Pool, id string, ref protocol.WorkspaceRuntimeRef, revision int64, results []protocol.WorkspaceMemberResult, final bool) error {
	if revision <= 0 || ref.Generation <= 0 {
		return errClusterWorkspace
	}
	w, e := db.GetSessionWorkspace(ctx, pool, id)
	if e != nil || w == nil || w.Manifest == nil || w.Manifest.LaunchGeneration != ref.LaunchGeneration {
		return errClusterWorkspace
	}
	current, _, err := db.GetWorkspaceRuntime(ctx, pool, id)
	if err != nil || current != ref.Generation || len(results) != len(w.Manifest.Members) {
		return errClusterWorkspace
	}
	if e = w.Manifest.ValidateResults(results); e != nil {
		return e
	}
	tx, e := pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var generation, previous, settled int64
	if e = tx.QueryRow(ctx, `SELECT reported_generation,report_revision,settled_generation FROM cluster_workspace_storage WHERE session_id=$1 AND launch_generation=$2 AND owner_id=$3 FOR UPDATE`, id, ref.LaunchGeneration, w.Manifest.OwnerID).Scan(&generation, &previous, &settled); e != nil {
		return e
	}
	if settled >= ref.Generation {
		return errClusterWorkspace
	}
	if generation > ref.Generation || (generation == ref.Generation && previous >= revision) {
		if final {
			return errClusterWorkspace
		}
		return nil
	}
	if results == nil {
		results = []protocol.WorkspaceMemberResult{}
	}
	raw, e := json.Marshal(results)
	if e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `UPDATE session_workspaces w SET results=$4 FROM sessions s WHERE w.session_id=s.id AND w.session_id=$1 AND w.launch_generation=$2 AND w.runtime_generation=$3 AND w.runtime_binding IS NOT NULL AND s.ended_at IS NULL AND s.status NOT IN ('stopped','ended') AND s.spawning_account_id=$5`, id, ref.LaunchGeneration, ref.Generation, raw, w.Manifest.OwnerID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return errClusterWorkspace
	}
	if _, e = tx.Exec(ctx, `UPDATE cluster_workspace_storage SET reported_generation=$2,report_revision=$3,settled_generation=CASE WHEN $4 THEN $2 ELSE settled_generation END WHERE session_id=$1`, id, ref.Generation, revision, final); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func persistClusterWorkspacePrepared(ctx context.Context, pool *pgxpool.Pool, id string, authorized protocol.WorkspaceRuntimeRef, raw []byte) error {
	var msg struct {
		protocol.WorkspaceRuntimePrepared
		Identities []clusterworkspace.MemberIdentity `json:"identities"`
	}
	if len(raw) > 1<<20 || json.Unmarshal(raw, &msg) != nil || msg.SessionID != id {
		return errClusterWorkspace
	}
	w, e := db.GetSessionWorkspace(ctx, pool, id)
	if e != nil || w == nil || w.Manifest == nil {
		return errClusterWorkspace
	}
	generation, _, e := db.GetWorkspaceRuntime(ctx, pool, id)
	if e != nil {
		return e
	}
	ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: w.Manifest.LaunchGeneration, Generation: generation}
	if ref != authorized {
		return errClusterWorkspace
	}
	if len(msg.Identities) == 0 {
		return db.BindWorkspaceRuntime(ctx, pool, id, ref, msg.Binding)
	}
	if len(msg.Identities) != len(w.Manifest.Members) {
		return errClusterWorkspace
	}
	seen := map[string]bool{}
	for _, identity := range msg.Identities {
		if seen[identity.ID] || identity.Directory.Inode == 0 || identity.GitDirectory.Inode == 0 || identity.Objects.Inode == 0 {
			return errClusterWorkspace
		}
		seen[identity.ID] = true
	}
	for _, member := range w.Manifest.Members {
		if !seen[member.ID] {
			return errClusterWorkspace
		}
	}
	var previous []byte
	if e = pool.QueryRow(ctx, `SELECT member_identities FROM cluster_workspace_storage WHERE session_id=$1 AND launch_generation=$2`, id, w.Manifest.LaunchGeneration).Scan(&previous); e != nil {
		return errClusterWorkspace
	}
	if previous != nil {
		var expected []clusterworkspace.MemberIdentity
		if json.Unmarshal(previous, &expected) != nil || len(expected) != len(msg.Identities) {
			return errClusterWorkspace
		}
		for _, old := range expected {
			found := false
			for _, current := range msg.Identities {
				if old.ID == current.ID && old.Directory.Inode == current.Directory.Inode && old.GitDirectory.Inode == current.GitDirectory.Inode && old.Objects.Inode == current.Objects.Inode {
					found = true
				}
			}
			if !found {
				return errClusterWorkspace
			}
		}
	}

	if e = db.BindWorkspaceRuntime(ctx, pool, id, ref, msg.Binding); e != nil {
		return e
	}
	encoded, e := json.Marshal(msg.Identities)
	if e != nil {
		return errClusterWorkspace
	}
	tag, e := pool.Exec(ctx, `UPDATE cluster_workspace_storage SET member_identities=$3 WHERE session_id=$1 AND launch_generation=$2`, id, ref.LaunchGeneration, encoded)
	if e != nil || tag.RowsAffected() != 1 {
		return errClusterWorkspace
	}
	return nil
}

// ClusterWorkspaceChild consumes an already-authorized immutable child subset.
// The existing delegation rules/approval flow remains mandatory at the caller.
// No credential or shell grant is inherited; the child fetches owner credentials
// independently and gets its own PVC, runtime generation and wip/<child-id>.
func ClusterWorkspaceChild(parent protocol.WorkspaceManifest, child protocol.WorkspaceManifest, contexts []clusterworkspace.Member) ([]clusterworkspace.Member, error) {
	if parent.Runtime != "cluster" || protocol.ValidateWorkspaceChild(parent, child) != nil {
		return nil, errClusterWorkspace
	}
	out := make([]clusterworkspace.Member, 0, len(child.Members))
	for _, member := range child.Members {
		found := false
		for _, c := range contexts {
			if c.ID == member.ID {
				c.Token = ""
				out = append(out, c)
				found = true
				break
			}
		}
		if !found {
			return nil, errClusterWorkspace
		}
	}
	return out, nil
}

var clusterMemoryPattern = regexp.MustCompile(`^([0-9]+)(Ki|Mi|Gi|Ti|K|M|G|T)?$`)

func clusterMemoryBytes(value any) (int64, error) {
	text, ok := value.(string)
	if !ok {
		return 0, errClusterWorkspace
	}
	matches := clusterMemoryPattern.FindStringSubmatch(text)
	if matches == nil {
		return 0, errClusterWorkspace
	}
	amount, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil || amount <= 0 {
		return 0, errClusterWorkspace
	}
	multiplier := int64(1)
	switch matches[2] {
	case "Ki":
		multiplier = 1 << 10
	case "Mi":
		multiplier = 1 << 20
	case "Gi":
		multiplier = 1 << 30
	case "Ti":
		multiplier = 1 << 40
	case "K":
		multiplier = 1000
	case "M":
		multiplier = 1000000
	case "G":
		multiplier = 1000000000
	case "T":
		multiplier = 1000000000000
	}
	if amount > math.MaxInt64/multiplier {
		return 0, errClusterWorkspace
	}
	return amount * multiplier, nil
}

// SetClusterWorkspaceReplyHandler binds P4/P5's private request correlator to
// this authenticated pod connection. The handler must be nonblocking, bounded,
// request-ID scoped and must never broadcast or persist shell/file payloads.
func (d *DaemonConn) SetClusterWorkspaceReplyHandler(handler func(context.Context, []byte)) {
	d.clusterReplyMu.Lock()
	d.clusterReply = handler
	d.clusterReplyMu.Unlock()
}
func (d *DaemonConn) deliverClusterWorkspaceReply(ctx context.Context, pool *pgxpool.Pool, raw []byte) bool {
	var msg struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
		protocol.WorkspaceRuntimeRef
	}
	if pool == nil || len(raw) > 1<<20 || json.Unmarshal(raw, &msg) != nil || msg.SessionID != d.BoundSessionID || len(msg.RequestID) == 0 || len(msg.RequestID) > 128 || msg.WorkspaceRuntimeRef != d.WorkspaceRuntime {
		return false
	}
	if msg.Type == "owner_shell_result" && len(raw) > 32<<10 {
		return false
	}
	generation, binding, err := db.GetWorkspaceRuntime(ctx, pool, msg.SessionID)
	if err != nil || generation != msg.Generation || !msg.Matches(binding) {
		return false
	}
	d.clusterReplyMu.RLock()
	handler := d.clusterReply
	d.clusterReplyMu.RUnlock()
	if handler != nil {
		handler(ctx, raw)
	}
	return true
}

// A subset keeps its immutable parent's paths. Recover the original naming
// context from server-owned ancestry rather than renaming a child.
func clusterLayoutSources(ctx context.Context, pool *pgxpool.Pool, id string, manifest protocol.WorkspaceManifest) ([]protocol.DiscoverySource, error) {
	if manifest.Validate() != nil || manifest.Layout == protocol.WorkspaceLayoutStructure {
		return nil, errClusterWorkspace
	}
	seen := map[string]bool{}
	for !seen[id] && ctx.Err() == nil {
		seen[id] = true
		row, err := db.GetSession(ctx, pool, id)
		if err != nil || row == nil {
			return nil, errClusterWorkspace
		}
		if row.ParentSessionID == nil {
			sources := make([]protocol.DiscoverySource, len(manifest.Members))
			for i, member := range manifest.Members {
				sources[i] = protocol.DiscoverySource{ID: member.ID, Name: member.Name}
			}
			paths, _ := workspaceselection.MemberPaths(protocol.DiscoveryResolved{Members: sources}, workspaceselection.MemberPathOptions{Cluster: true})
			for i, member := range manifest.Members {
				expectedPath := paths[i]
				if manifest.Layout == "" {
					expectedPath = member.ID
				}
				if member.Path != expectedPath {
					return nil, errClusterWorkspace
				}
			}
			return sources, nil
		}
		parent, err := db.GetSessionWorkspace(ctx, pool, *row.ParentSessionID)
		if err != nil || parent == nil || parent.Manifest == nil || protocol.ValidateWorkspaceChild(*parent.Manifest, manifest) != nil {
			return nil, errClusterWorkspace
		}
		id, manifest = *row.ParentSessionID, *parent.Manifest
	}
	return nil, errClusterWorkspace
}
