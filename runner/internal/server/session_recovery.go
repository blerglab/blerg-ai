package server

import (
	"context"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/nativehistory"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sessionResumeEligibility checks local state and caller authority only. Native
// mode means an eligible attempt: the replacement still verifies live Core
// authority, the retained claim's UID and writer fencing before mounting it.
func sessionResumeEligibility(ctx context.Context, pool *pgxpool.Pool, jobs *JobManager, row *db.SessionRow, account string, proof coreProof) (bool, string, string) {
	if row != nil {
		if problem := parentResumeProblem(ctx, pool, row.ID); problem != nil {
			return false, "", problem.Message
		}
	}
	if row == nil || row.Kind != "agent" || row.Status != "disconnected" || row.EndedAt != nil || derefOrEmpty(row.Runtime) != "cluster" {
		return false, "", "The session is not a disconnected Cluster conversation"
	}
	if jobs == nil || pool == nil {
		return false, "", "Cluster recovery is unavailable"
	}
	owner := derefOrEmpty(row.SpawningAccountID)
	// A live proof is needed only where the pod fetches the owner's personal credentials with it;
	// an install without them wired needs none (CreateSessionJob agrees).
	if owner != "" && (account != owner || (jobs.personalCredentialsWired() && !proof.valid())) {
		return false, "", "Recovery requires the owner's live launch authority"
	}
	grants, err := db.ListMCPGrantsForSession(ctx, pool, row.ID)
	if err != nil {
		return false, "", "Recovery permissions could not be verified"
	}
	if len(grants) > 0 && (owner == "" || account != owner || proof.SessionID == "") {
		return false, "", "Sign in as the owner to restore this session's connections"
	}
	retained, err := db.GetSessionNativeHistory(ctx, pool, row.ID)
	if err != nil {
		return false, "", "Retained conversation state could not be verified"
	}
	_, supported := nativehistory.ForEngine(derefOrEmpty(row.Engine))
	if retained == nil {
		if jobs.NativeHistory.Enabled && supported && owner != "" {
			return false, "", "Native conversation history was not retained for this session; start a new session"
		}
		// Legacy installations deliberately expose reconstruction as a different
		// mode. It never promises the original native conversation identity.
		return true, "transcript_replay", "Legacy recovery reconstructs the conversation from recorded events"
	}
	if !jobs.NativeHistory.Enabled || !supported {
		return false, "", "Retained native history is currently unavailable"
	}
	if retained.DetachedAt != nil || retained.ReclaimedAt != nil || retained.AccountID != owner || retained.Engine != models.NormalizeEngine(derefOrEmpty(row.Engine)) || retained.ClaimName != historyClaimName(row.ID) || retained.ClaimUID == "" {
		return false, "", "Retained native conversation state is unavailable for this session"
	}
	return true, "native", ""
}
