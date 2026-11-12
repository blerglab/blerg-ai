package server

import (
	"context"
	"errors"
	"log/slog"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/models"
)

// Only fixed diagnostics cross the launch boundary; never upstream error text.
type clusterAuthorizationRefusal struct {
	code  string
	cause error
}

func (e *clusterAuthorizationRefusal) Unwrap() error { return e.cause }

// #nosec G101 -- Fixed user-facing refusal messages, never credential values.
var workspaceRefusalMessages = map[string]string{
	"workspace_member_paths_invalid":          "The workspace member paths could not be projected safely; reopen the workspace and try again.",
	"worker_token_lookup_failed":              "The read-only worker token could not be retrieved; check Settings → Read-only tokens for workers.",
	"personal_token_lookup_failed":            "The personal repository token could not be retrieved; check Settings → Integrations.",
	"repository_read_refused":                 "Repository read access was refused or could not be verified; check the repository name, connection and token permissions.",
	"workspace_credential_plan_invalid":       "The repository credentials do not form a valid authorized workspace plan.",
	"workspace_session_read_failed":           "The session authorization record could not be read.",
	"workspace_session_missing":               "The launch session no longer exists.",
	"workspace_session_ended":                 "The launch session has already ended.",
	"workspace_session_owner_mismatch":        "The session belongs to another owner.",
	"workspace_session_runtime_mismatch":      "The session is not bound to the cluster runtime.",
	"workspace_session_engine_mismatch":       "The session engine does not match the launch engine.",
	"workspace_authorization_lock_failed":     "The workspace authorization lock could not be acquired.",
	"owner_authorization_service_unavailable": "The sign-in authorization service could not be reached or returned an unusable response.",
	"workspace_worker_write_credential":       "A cluster worker was offered a write-capable repository credential.",
	"workspace_plan_invalid":                  "The authorized workspace plan is invalid.",
	"invalid_session_id":                      "The launch session ID is invalid.",
	"workspace_database_unavailable":          "Workspace authorization storage is unavailable.",
	"workspace_manifest_missing":              "The launch has no workspace manifest.",
	"workspace_repo_mode_conflict":            "A workspace launch also requested an incompatible repository mode.",
	"workspace_legacy_repo_conflict":          "A workspace launch also supplied a legacy repository selector.",
	"workspace_legacy_url_conflict":           "A workspace launch also supplied a legacy Git URL.",
	"workspace_git_token_override":            "The launch attempted to override the independently authorized Git token.",
	"workspace_plan_override":                 "The launch attempted to override the protected workspace plan.",
	"workspace_manifest_read_failed":          "The saved workspace manifest could not be read.",
	"workspace_manifest_not_saved":            "The workspace manifest was not saved for this session.",
	"workspace_manifest_mismatch":             "The launch manifest does not match the saved workspace manifest.",
	"workspace_owner_mismatch":                "The launch owner does not match the workspace owner.",
	"workspace_session_not_current":           "The session is ended, reassigned, on another runtime, or bound to another engine.",
	"workspace_storage_read_failed":           "The retained storage binding could not be read.",
	"workspace_storage_mode_invalid":          "The retained storage mode is not supported.",
	"workspace_claim_name_mismatch":           "The retained claim name does not match this session.",
	"workspace_storage_owner_mismatch":        "The retained storage belongs to another owner.",
	"workspace_storage_launch_mismatch":       "The retained storage belongs to another launch generation.",
	"workspace_storage_contexts_invalid":      "The saved repository contexts are invalid.",
	"workspace_resume_storage_missing":        "The resumed workspace has no retained storage binding.",
	"workspace_identities_invalid":            "The saved member identities are invalid.",
	"workspace_unexpected_claim":              "A new workspace unexpectedly already has a storage claim.",
	"workspace_claim_read_failed":             "The new storage claim could not be verified.",
	"workspace_claim_identity_mismatch":       "The storage claim identity, owner, or labels do not match this launch.",
	"workspace_contexts_encode_failed":        "The repository contexts could not be encoded.",
	"workspace_storage_save_failed":           "The retained storage binding could not be saved.",
	"workspace_session_changed":               "The session changed while storage was being prepared.",
	"workspace_runtime_manifest_changed":      "The workspace manifest or session changed before runtime binding.",
	"workspace_runtime_binding_changed":       "The retained storage binding changed before runtime binding.",
	"workspace_runtime_claim_changed":         "The storage claim changed before runtime binding.",
	"workspace_runtime_identities_invalid":    "The retained member identities changed before runtime binding.",
	"workspace_layout_invalid":                "The cluster workspace layout or manifest is invalid.",
	"workspace_layout_session_missing":        "The layout ancestry session could not be read.",
	"workspace_layout_path_mismatch":          "A member path does not match its authorized layout.",
	"workspace_layout_parent_missing":         "The parent workspace manifest could not be read.",
	"workspace_layout_parent_binding_refused": "The child runtime or repository destination does not match its parent workspace binding.",
	"workspace_layout_ancestry_cycle":         "The workspace layout ancestry is cyclic or its deadline expired.",
	"base_branch_not_found":                   "The base branch was not found on the repository host; push it before starting the worker.",
}

func (j *JobManager) clusterWorkspaceSessionRefusal(ctx context.Context, spec SessionJobSpec) error {
	row, err := db.GetSession(ctx, j.historyPool, spec.SessionID)
	if err != nil {
		return clusterWorkspaceRefused("workspace_session_read_failed")
	}
	if row == nil {
		return clusterWorkspaceRefused("workspace_session_missing")
	}
	if row.EndedAt != nil || row.Status == "ended" || row.Status == "stopped" || row.Status == "error" {
		return clusterWorkspaceRefused("workspace_session_ended")
	}
	if derefOrEmpty(row.SpawningAccountID) != spec.SpawningAccountID {
		return clusterWorkspaceRefused("workspace_session_owner_mismatch")
	}
	if derefOrEmpty(row.Runtime) != "cluster" {
		return clusterWorkspaceRefused("workspace_session_runtime_mismatch")
	}
	if models.NormalizeEngine(derefOrEmpty(row.Engine)) != models.NormalizeEngine(spec.Engine) {
		return clusterWorkspaceRefused("workspace_session_engine_mismatch")
	}
	return nil
}

func clusterWorkspaceRefused(code string) error {
	return &clusterAuthorizationRefusal{code: code, cause: errClusterWorkspace}
}

func (e *clusterAuthorizationRefusal) Error() string {
	if message, ok := workspaceRefusalMessages[e.code]; ok {
		return message + " (" + e.code + ")"
	}

	switch e.code {
	case "owner_token_missing":
		return "Your repository token is missing. Add a read-only token in Settings → Integrations."
	case "owner_token_rejected":
		return "Your repository token was rejected or expired (401). Update it in Settings → Integrations."
	case "repository_scope_insufficient":
		return "Your repository token lacks read access. Update its permissions in Settings → Integrations."
	case "repository_not_visible":
		return "The repository is not visible to your token. Check its name and repository access in Settings → Integrations."
	case "workspace_authority_changed":
		return "The workspace selection or launch authority changed. Reopen the workspace and try again."
	default:
		return "Your sign-in cannot authorize this launch. Sign in again and update your repository connection in Settings → Integrations."
	}
}
func (e *clusterAuthorizationRefusal) Is(target error) bool {
	return target == errRepositoryUnreadable && (e.code == "owner_token_missing" || e.code == "owner_token_rejected" || e.code == "repository_scope_insufficient" || e.code == "repository_not_visible")
}
func clusterRefusal(err error) *clusterAuthorizationRefusal {
	var refusal *clusterAuthorizationRefusal
	if errors.As(err, &refusal) {
		return refusal
	}
	if errors.Is(err, errCorePrincipalRefused) || errors.Is(err, ErrNoPersonalCredential) || errors.Is(err, errNoLivenessProof) {
		return &clusterAuthorizationRefusal{code: "owner_authorization_refused", cause: err}
	}
	return nil
}
func logClusterRefusal(err error) {
	if refusal := clusterRefusal(err); refusal != nil {
		failurelog.Default.Event("cluster_launch:"+refusal.code, slog.LevelWarn, "component", "runner", "operation", "cluster_launch", "cause", refusal.code)
	}
}
