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

// errWorkerWriteCredential is BLG-460's fail-closed backstop: a cluster worker's own spec
// carried something a worker must never have (PushAuthorized, or a git credential other
// than its one read-only token). Refused outright rather than silently downgraded.
var errWorkerWriteCredential = errors.New("a cluster worker's launch carried a write-capable credential; refused")

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
	defer func() {
		if launchErr != nil && clusterRefusal(launchErr) == nil && progress.current == "authorization" {
			launchErr = clusterWorkspaceRefused("workspace_authorization_lock_failed")
		}
		if refusal := clusterRefusal(launchErr); refusal != nil {
			launchErr = refusal
		}
		logClusterRefusal(launchErr)
	}()
	for _, check := range []struct {
		refused bool
		code    string
	}{
		{!historyID.MatchString(spec.SessionID), "invalid_session_id"},
		{j.historyPool == nil, "workspace_database_unavailable"},
		{spec.Workspace == nil, "workspace_manifest_missing"},
		{spec.NoRepo || spec.NewRepo, "workspace_repo_mode_conflict"},
		{spec.Repo != "", "workspace_legacy_repo_conflict"},
		{spec.GitURL != "", "workspace_legacy_url_conflict"},
	} {
		if check.refused {
			return clusterWorkspaceRefused(check.code)
		}
	}
	if _, exists := spec.ExtraEnv[gitTokenKey]; exists {
		return clusterWorkspaceRefused("workspace_git_token_override")
	}
	if _, exists := spec.ExtraEnv[clusterworkspace.Env]; exists {
		return clusterWorkspaceRefused("workspace_plan_override")
	}

	return db.WithSessionNativeHistoryLock(ctx, j.historyPool, spec.SessionID, func() error {
		w, e := db.GetSessionWorkspace(ctx, j.historyPool, spec.SessionID)
		if e != nil {
			return clusterWorkspaceRefused("workspace_manifest_read_failed")
		}
		if w == nil || w.Manifest == nil {
			return clusterWorkspaceRefused("workspace_manifest_not_saved")
		}
		if !reflect.DeepEqual(*w.Manifest, *spec.Workspace) {
			return clusterWorkspaceRefused("workspace_manifest_mismatch")
		}
		if spec.SpawningAccountID != w.Manifest.OwnerID {
			return clusterWorkspaceRefused("workspace_owner_mismatch")
		}
		if err := j.clusterWorkspaceSessionRefusal(ctx, spec); err != nil {
			return err
		}

		if err := verifyCorePrincipal(ctx, j.CredentialClient, j.CoreURL, j.CoreInternalKey, spec.SpawningAccountID, spec.proof()); err != nil {
			if refusal := clusterRefusal(err); refusal != nil {
				return refusal
			}
			return clusterWorkspaceRefused("owner_authorization_service_unavailable")
		}

		name := "blerg-workspace-" + spec.SessionID
		var expectedUID, launch, owner, expectedName, mode string
		var raw, identities []byte
		e = j.historyPool.QueryRow(ctx, `SELECT claim_uid,launch_generation,contexts,member_identities,owner_id,claim_name,storage_mode FROM cluster_workspace_storage WHERE session_id=$1`, spec.SessionID).Scan(&expectedUID, &launch, &raw, &identities, &owner, &expectedName, &mode)
		fresh := errors.Is(e, pgx.ErrNoRows)
		if e != nil && !fresh {
			return clusterWorkspaceRefused("workspace_storage_read_failed")
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
			return clusterWorkspaceRefused("workspace_storage_mode_invalid")
		}
		members := append([]clusterworkspace.Member(nil), spec.WorkspaceMembers...)
		if !fresh {
			if expectedName != name {
				return clusterWorkspaceRefused("workspace_claim_name_mismatch")
			}
			if owner != spec.SpawningAccountID {
				return clusterWorkspaceRefused("workspace_storage_owner_mismatch")
			}
			if launch != w.Manifest.LaunchGeneration {
				return clusterWorkspaceRefused("workspace_storage_launch_mismatch")
			}
			if json.Unmarshal(raw, &members) != nil {
				return clusterWorkspaceRefused("workspace_storage_contexts_invalid")
			}
		} else if spec.Resume {
			return clusterWorkspaceRefused("workspace_resume_storage_missing")
		}

		plan := clusterworkspace.Plan{UnboundedStorage: mode == "standard", PrepareDeadlineMillis: deadline.UnixMilli(), SessionID: spec.SessionID, WorkBranch: spec.WorkBranch, Manifest: *w.Manifest, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: w.Manifest.LaunchGeneration, Generation: 1}, Members: members}
		// BLG-460: refuse outright, before anything else runs, if a worker's own incoming
		// member set already carries PushAuthorized — whatever produced that spec (a bug in
		// ClusterWorkspaceChild, a future caller that builds members by hand) is wrong, and
		// this must not silently correct it by proceeding read-only anyway.
		if spec.ReadOnlyGitWorker {
			for _, m := range plan.Members {
				if m.PushAuthorized {
					return &clusterAuthorizationRefusal{code: "workspace_worker_write_credential", cause: errWorkerWriteCredential}
				}
			}
		}
		plan.LayoutSources, e = clusterLayoutSources(ctx, j.historyPool, spec.SessionID, *w.Manifest)
		if e != nil {
			return e
		}
		if !fresh {
			if json.Unmarshal(identities, &plan.Identities) != nil || len(plan.Identities) != len(plan.Members) {
				return clusterWorkspaceRefused("workspace_identities_invalid")
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
			return clusterWorkspaceRefused("workspace_plan_invalid")
		}
		for i := range plan.Members {
			plan.Members[i].Token = ""
		}

		progress.next(ctx, "credentials", "Checking every member with independent repository authority")
		// Resolve and precheck EVERY member; never use the operator Git token.
		for i := range plan.Members {
			m := &plan.Members[i]
			if spec.ReadOnlyGitWorker {
				// BLG-460 option B: a cluster worker never reaches fetchPersonalCredential
				// (the launcher's own write-capable token) and never stores
				// PushAuthorized: true — only a read-only, per-owner token, found or not.
				m.PushAuthorized = false
				token, found, e := j.fetchReadOnlyGitToken(ctx, spec.SpawningAccountID, m.Provider, gitOwnerFromCloneURL(m.CloneURL), spec.proof())
				if e != nil {
					if refusal := clusterRefusal(e); refusal != nil {
						return refusal
					}
					return &clusterAuthorizationRefusal{code: "worker_token_lookup_failed", cause: e}
				}
				if found {
					m.Token = token
				} else {
					m.Token = ""
				}
			} else {
				kind, _ := j.gitCredentialFor(m.CloneURL)
				if kind != "" && kind == m.Provider {
					token, found, e := j.fetchPersonalCredential(ctx, spec.SpawningAccountID, kind, spec.proof())
					if e != nil {
						if refusal := clusterRefusal(e); refusal != nil {
							return refusal
						}
						return &clusterAuthorizationRefusal{code: "personal_token_lookup_failed", cause: e}
					}
					if found {
						m.Token = string(token)
						m.PushAuthorized = true
					}
				}
				if m.PushAuthorized && m.Token == "" {
					return &clusterAuthorizationRefusal{code: "owner_token_missing", cause: ErrNoPersonalCredential}
				}
			}
			checkSpec := spec
			checkSpec.ExtraEnv = map[string]string{gitTokenKey: m.Token}
			checkSpec.NoOperatorFallback = true
			if e := j.precheckRepositoryContext(ctx, checkSpec, m.CloneURL, nil, map[string]bool{gitTokenKey: true}, false); e != nil {
				if refusal := clusterRefusal(e); refusal != nil {
					return refusal
				}
				return &clusterAuthorizationRefusal{code: "repository_read_refused", cause: e}
			}
		}
		if e := plan.Validate(); e != nil {
			return clusterWorkspaceRefused("workspace_credential_plan_invalid")
		}
		progress.next(ctx, "storage", "Allocating and verifying retained claim identity; binding is deferred to the consumer pod")
		claim, e := j.historyClaim(ctx, name)
		if e != nil {
			return e
		}
		if fresh && claim != nil {
			return clusterWorkspaceRefused("workspace_unexpected_claim")
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
				return clusterWorkspaceRefused("workspace_claim_read_failed")
			}
		}
		if claim == nil || claim.Metadata.Name != name || claim.Metadata.UID == "" || claim.Metadata.Deleted != nil || len(claim.Metadata.Owners) != 0 || claim.Metadata.Labels["app"] != "blerg-workspace" || claim.Metadata.Labels["session"] != spec.SessionID || claim.Metadata.Labels["account"] != spec.SpawningAccountID || (!fresh && claim.Metadata.UID != expectedUID) {
			return clusterWorkspaceRefused("workspace_claim_identity_mismatch")
		}

		if fresh {
			stored := append([]clusterworkspace.Member(nil), plan.Members...)
			for i := range stored {
				stored[i].Token = ""
			}
			raw, e = json.Marshal(stored)
			if e != nil {
				return clusterWorkspaceRefused("workspace_contexts_encode_failed")
			}
			if _, e = j.historyPool.Exec(ctx, `INSERT INTO cluster_workspace_storage(session_id,launch_generation,claim_name,claim_uid,contexts,owner_id,storage_mode) VALUES($1,$2,$3,$4,$5,$6,$7)`, spec.SessionID, w.Manifest.LaunchGeneration, name, claim.Metadata.UID, raw, spec.SpawningAccountID, mode); e != nil {
				return clusterWorkspaceRefused("workspace_storage_save_failed")
			}
		}
		if !j.nativeHistorySessionCurrent(ctx, spec) {
			return clusterWorkspaceRefused("workspace_session_changed")
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
				return clusterWorkspaceRefused("workspace_runtime_manifest_changed")
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
				return clusterWorkspaceRefused("workspace_runtime_binding_changed")
			}
			verifiedClaim, err := j.historyClaim(fenced, name)
			if err != nil || verifiedClaim == nil || verifiedClaim.Metadata.UID != currentUID || verifiedClaim.Metadata.Deleted != nil || len(verifiedClaim.Metadata.Owners) != 0 || verifiedClaim.Metadata.Labels["app"] != "blerg-workspace" || verifiedClaim.Metadata.Labels["session"] != spec.SessionID || verifiedClaim.Metadata.Labels["account"] != spec.SpawningAccountID {
				return clusterWorkspaceRefused("workspace_runtime_claim_changed")
			}
			if !fresh && (json.Unmarshal(currentIdentities, &plan.Identities) != nil || len(plan.Identities) != len(plan.Members)) {
				return clusterWorkspaceRefused("workspace_runtime_identities_invalid")
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
	if request > limit {
		request = limit
	}
	// BLG-558 stopgap (owner decision 2026-11-05): the configured pod limit
	// bounds Git together with the agent and its builds. Clamping the whole
	// container to Git's budget OOM-killed every cluster worker; Git gets its
	// own memory group in 0.7.0.
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
				// BLG-460: a cluster worker never inherits its parent Main's push
				// authority, even transiently in the projected member context — the
				// child's own credential stage (withClusterWorkspace, ReadOnlyGitWorker)
				// decides push authority fresh, and for a worker that is always false.
				c.PushAuthorized = false
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
		return nil, clusterWorkspaceRefused("workspace_layout_invalid")
	}
	seen := map[string]bool{}
	for !seen[id] && ctx.Err() == nil {
		seen[id] = true
		row, err := db.GetSession(ctx, pool, id)
		if err != nil || row == nil {
			return nil, clusterWorkspaceRefused("workspace_layout_session_missing")
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
					return nil, clusterWorkspaceRefused("workspace_layout_path_mismatch")
				}
			}
			return sources, nil
		}
		parent, err := db.GetSessionWorkspace(ctx, pool, *row.ParentSessionID)
		if err != nil || parent == nil || parent.Manifest == nil {
			return nil, clusterWorkspaceRefused("workspace_layout_parent_missing")
		}
		if parent.Manifest.Runtime == "host" && manifest.Runtime == "cluster" {
			if err := validateHostClusterLayoutChild(*parent.Manifest, manifest); err != nil {
				return nil, err
			}
			sources := make([]protocol.DiscoverySource, len(parent.Manifest.Members))
			for i, member := range parent.Manifest.Members {
				sources[i] = protocol.DiscoverySource{ID: member.ID, Name: member.Name}
			}
			return sources, nil
		}
		if protocol.ValidateWorkspaceChild(*parent.Manifest, manifest) != nil {
			return nil, clusterWorkspaceRefused("workspace_layout_parent_binding_refused")
		}
		id, manifest = *row.ParentSessionID, *parent.Manifest
	}
	return nil, clusterWorkspaceRefused("workspace_layout_ancestry_cycle")
}

// A desktop Main's cluster child intentionally changes only runtime and the
// destination representation. All selected source identities, paths, providers,
// projects, owner and launch-generation separation remain independently checked.
func validateHostClusterLayoutChild(parent, child protocol.WorkspaceManifest) error {
	if parent.Runtime != "host" || child.Runtime != "cluster" || parent.Validate() != nil {
		return clusterWorkspaceRefused("workspace_layout_parent_binding_refused")
	}
	projected := parent
	projected.Runtime = "cluster"
	paths, err := projectHostClusterMemberPaths(parent, workspaceselection.MemberPaths)
	if err != nil {
		return err
	}
	projected.Members = append([]protocol.WorkspaceMember(nil), parent.Members...)
	for _, member := range child.Members {
		if validateRepoName(member.Project) != nil {
			return clusterWorkspaceRefused("workspace_layout_parent_binding_refused")
		}
	}
	for i := range projected.Members {
		if projected.Members[i].Path == "." {
			projected.Members[i].Path = paths[i]
		}
		if projected.Members[i].Project != "" {
			projected.Members[i].Destination = projected.Members[i].Project
		}
	}
	if protocol.ValidateWorkspaceChild(projected, child) != nil {
		return clusterWorkspaceRefused("workspace_layout_parent_binding_refused")
	}
	return nil
}
