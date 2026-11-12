package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/jackc/pgx/v5"
)

func draftDigest(raw json.RawMessage) string {
	// PostgreSQL jsonb has a canonical representation for the stored snapshot.
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (a *API) createBoundDraftAsk(ctx context.Context, req postMessageRequest, session *db.SessionRow) (string, error) {
	subject := *req.Subject
	if subject.DraftID == "" || subject.Revision < 0 {
		return "", boundFailure("invalid_subject")
	}
	draft, err := db.GetBlackboardDraft(ctx, a.dbPool, session.ID)
	if err != nil {
		return "", err
	}
	if draft == nil || draft.ID != subject.DraftID || draft.Revision != subject.Revision {
		return "", boundFailure("stale_draft")
	}
	owner := *session.SpawningAccountID
	plan, err := boundDraftPlan(draft)
	if err != nil {
		return "", err
	}
	if err = a.boundBoardAccess(ctx, draftAccessRecord(plan, owner, session.ID), owner, "", session.ID); err != nil {
		return "", err
	}
	if err = a.boundDraftRelease(ctx, plan, owner, "", session.ID); err != nil {
		return "", err
	}
	subject = boundSubject{Kind: "draft", DraftID: draft.ID, Revision: draft.Revision, Digest: draftDigest(draft.Draft)}
	var body strings.Builder
	fmt.Fprintf(&body, "Create blackboard · %s, %s, %s\n\nDraft %s · revision %d\n\nName: %s\nVision: %s\n", draftCount(len(plan.Goals), "goal"), draftCount(boolCount(plan.Milestone.Name != ""), "milestone"), draftCount(len(plan.Thrusts), "thrust"), draft.ID, draft.Revision, boundSummaryField(plan.Name), boundSummaryField(plan.Vision))
	if plan.BoardID != "" {
		fmt.Fprintf(&body, "Board: %s\n", boundSummaryField(plan.BoardID))
	}
	for i, g := range plan.Goals {
		fmt.Fprintf(&body, "\nGoal %d: %s\n", i+1, boundSummaryField(g))
		for _, t := range plan.Thrusts {
			if t.GoalIndex == i {
				fmt.Fprintf(&body, "- %s\n  Reason: %s\n", boundSummaryField(t.Name), boundSummaryField(t.Reason))
			}
		}
	}
	if plan.Milestone.Name != "" {
		fmt.Fprintf(&body, "\nMilestone: %s\n", boundSummaryField(plan.Milestone.Name))
		if plan.Milestone.ReleaseID != "" {
			fmt.Fprintf(&body, "Release: %s\n", boundSummaryField(plan.Milestone.ReleaseID))
		}
		if plan.Milestone.Target != nil {
			fmt.Fprintf(&body, "Target: %s\n", boundSummaryField(*plan.Milestone.Target))
		}
	}
	body.WriteString("\nReply to this question with yes to create this exact blackboard, or no to discard this draft. Other replies ask for clarification. Expires in 24 hours.")
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = db.LockDraftWorkspace(ctx, tx, session.ID); err != nil {
		return "", err
	}
	var currentOwner string
	if err = tx.QueryRow(ctx, `SELECT coalesce(spawning_account_id,'') FROM sessions WHERE id=$1 FOR UPDATE`, session.ID).Scan(&currentOwner); err != nil {
		return "", err
	}
	if owner != currentOwner {
		return "", boundFailure("wrong_owner")
	}
	if err = a.validateBoundDraft(ctx, tx, subject, owner, session.ID, draftAccessRecord(plan, owner, session.ID)); err != nil {
		return "", err
	}
	return a.persistBoundAsk(ctx, tx, session.ID, owner, subject, body.String(), req.NoWait, req.Urgent, req.SessionPosted)

}
func boolCount(v bool) int {
	if v {
		return 1
	}
	return 0
}
func boundDraftPlan(d *db.BlackboardDraft) (blackboardDraftPlan, error) {
	var plan blackboardDraftPlan
	if err := json.Unmarshal(d.Draft, &plan); err != nil {
		return plan, boundFailure("invalid_draft")
	}
	if err := validateBlackboardDraftPlan(plan); err != nil {
		return plan, err
	}
	if strings.TrimSpace(plan.Name) == "" {
		return plan, boundFailure("incomplete_draft")
	}
	return plan, nil
}
func draftAccessRecord(plan blackboardDraftPlan, owner, sid string) blackboard.Record {
	r := blackboard.Record{Owner: blackboard.Actor{Kind: "human", ID: owner}, WorkspaceID: sid}
	if plan.BoardID != "" {
		r.Links = []blackboard.Link{{BoardID: plan.BoardID, WorkspaceID: sid}}
	}
	return r
}
func (a *API) noExistingDraftBlackboard(ctx context.Context, tx pgx.Tx, sid string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blackboards WHERE record->>'workspace_id'=$1 OR id IN (SELECT blackboard_id FROM blackboard_links WHERE workspace_id=$1))`, sid).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return boundFailure("blackboard_exists")
	}
	return nil
}
func (a *API) validateBoundDraft(ctx context.Context, tx pgx.Tx, subject boundSubject, owner, sid string, checked blackboard.Record) error {
	d, err := db.GetBlackboardDraft(ctx, tx, sid)
	if err != nil {
		return err
	}
	if d == nil || d.ID != subject.DraftID || d.Revision != subject.Revision || draftDigest(d.Draft) != subject.Digest {
		return boundFailure("stale_draft")
	}
	plan, err := boundDraftPlan(d)
	if err != nil {
		return err
	}
	if err = a.noExistingDraftBlackboard(ctx, tx, sid); err != nil {
		return err
	}
	if !sameBoundScope(checked, draftAccessRecord(plan, owner, sid)) {
		return boundFailure("stale_draft")
	}
	return nil
}

// The caller has locked and verified this draft. Its consumption and new record
// join the answer transaction; deleting the pending row retains immutable history.
func (a *API) applyBoundDraft(ctx context.Context, tx pgx.Tx, subject boundSubject, owner, sid, state, askID string) (string, error) {
	id := ""
	if state == "committed" {
		d, err := db.GetBlackboardDraft(ctx, tx, sid)
		if err != nil {
			return "", err
		}
		plan, err := boundDraftPlan(d)
		if err != nil {
			return "", err
		}
		actor := blackboard.Actor{Kind: "human", ID: owner}
		r := draftAccessRecord(plan, owner, sid)
		r.ID = blackboard.NewID()
		r.Version = 1
		r.Name = plan.Name
		r.Vision = plan.Vision
		for _, name := range plan.Goals {
			r.Goals = append(r.Goals, blackboard.Goal{ID: blackboard.NewID(), Owner: actor, Name: name, Version: 1})
		}
		mid := ""
		if plan.Milestone.Name != "" {
			mid = blackboard.NewID()
			var target *time.Time
			if plan.Milestone.Target != nil && *plan.Milestone.Target != "" {
				v, err := time.Parse("2006-01-02", *plan.Milestone.Target)
				if err != nil {
					return "", err
				}
				target = &v
			}
			r.Milestones = []blackboard.Milestone{{ID: mid, Owner: actor, Name: plan.Milestone.Name, BoardID: plan.BoardID, ReleaseID: plan.Milestone.ReleaseID, Target: target, Version: 1}}
		}
		for _, t := range plan.Thrusts {
			r.Thrusts = append(r.Thrusts, blackboard.Thrust{ID: blackboard.NewID(), Owner: actor, Name: t.Name, GoalID: r.Goals[t.GoalIndex].ID, MilestoneID: mid, Version: 1})
		}
		if err := (db.BlackboardStore{Pool: a.dbPool, Tx: tx}).Create(ctx, r); err != nil {
			return "", err
		}
		for _, m := range r.Milestones {
			if m.BoardID != "" && m.ReleaseID != "" {
				if _, err := recordReleaseLink(ctx, tx, r.ID, m.ID, m.BoardID, m.ReleaseID, askID, m.Revision); err != nil {
					return "", err
				}
			}
		}
		id = r.ID
	}
	_, err := tx.Exec(ctx, `DELETE FROM blackboard_drafts WHERE workspace_id=$1 AND id::text=$2 AND revision=$3`, sid, subject.DraftID, subject.Revision)
	return id, err
}

func draftCount(n int, noun string) string {
	if n != 1 {
		noun += "s"
	}
	return fmt.Sprintf("%d %s", n, noun)
}

// Read board scope before taking a write lock. The exact immutable subject and
// current draft are checked again inside the answer transaction.
func (a *API) preflightBoundDraft(ctx context.Context, id, sid, owner, login string) (blackboard.Record, error) {
	var raw []byte
	if err := a.dbPool.QueryRow(ctx, `SELECT draft FROM blackboard_draft_revisions WHERE (id::text,revision)=(SELECT subject->>'draft_id',(subject->>'revision')::integer FROM bound_asks WHERE message_id::text=$1)`, id).Scan(&raw); err != nil {
		return blackboard.Record{}, err
	}
	plan, err := boundDraftPlan(&db.BlackboardDraft{Draft: raw})
	if err != nil {
		return blackboard.Record{}, err
	}
	if err = a.boundDraftRelease(ctx, plan, owner, login, sid); err != nil {
		return blackboard.Record{}, err
	}
	return draftAccessRecord(plan, owner, sid), nil
}

// Linked setup may target only an open release on the named board. Remote reads
// happen before the runner transaction. The cut increment must also verify causal
// link provenance before Final to close a concurrent board-close race.
func (a *API) boundDraftRelease(ctx context.Context, plan blackboardDraftPlan, owner, login, sid string) error {
	if plan.Milestone.ReleaseID == "" {
		return nil
	}
	if plan.BoardID == "" || plan.Milestone.Name == "" {
		return boundFailure("invalid_release_link")
	}
	args := map[string]any{"board_id": plan.BoardID, "release_id": plan.Milestone.ReleaseID}
	var out struct {
		Release blackboard.Release `json:"release"`
	}
	if login == "" {
		if err := a.blackboardSessionService(draftAccessRecord(plan, owner, sid), sid, sid).Board.Call(ctx, "blerg_release_get", args, &out); err != nil {
			return boundFailure("release_unavailable")
		}
	} else {
		caller := a.sessionBoardCaller()
		if caller == nil {
			return boundFailure("board_unavailable")
		}
		rawArgs, _ := json.Marshal(args)
		raw, err := caller.CallBoard(ctx, mcpgw.BoardCall{Proof: mcpgw.Proof{AccountID: owner, SessionID: login}, BoardID: plan.BoardID, SessionRef: sid, Tool: "blerg_release_get", Arguments: rawArgs, ReadOnly: true})
		if err != nil {
			return boundFailure("release_unavailable")
		}
		if resultErr := boardToolResult(raw, &out); resultErr != nil {
			return boundFailure("release_unavailable")
		}
	}
	if out.Release.ID != plan.Milestone.ReleaseID || out.Release.BoardID != plan.BoardID {
		return boundFailure("invalid_release_link")
	}
	if out.Release.Status == "released" {
		return boundFailure("release_already_released")
	}
	if out.Release.Status != "planned" && out.Release.Status != "in progress" {
		return boundFailure("invalid_release_link")
	}
	return nil
}
