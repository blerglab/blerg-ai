package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/contracts/releasecut"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/jackc/pgx/v5"
)

type boundCutSubject struct {
	BoardID       string                    `json:"board_id"`
	ReleaseID     string                    `json:"release_id"`
	NextReleaseID string                    `json:"next_release_id"`
	Link          bool                      `json:"link"`
	BaseBoardID   string                    `json:"base_board_id"`
	BaseReleaseID string                    `json:"base_release_id"`
	CardDigest    string                    `json:"card_digest"`
	Snapshot      []releasecut.SnapshotCard `json:"snapshot"`
}
type cutReleaseView struct {
	BoardName string              `json:"board_name"`
	NextName  string              `json:"-"`
	Release   blackboard.Release  `json:"release"`
	Receipt   *releasecut.Receipt `json:"cut_receipt"`
	Preview   *releasecut.Preview `json:"cut_preview"`
}

func cutMilestone(r blackboard.Record, subject boundSubject) (blackboard.Milestone, error) {
	if subject.Cut == nil || subject.Cut.BoardID == "" || subject.Cut.ReleaseID == "" || subject.MilestoneID == "" {
		return blackboard.Milestone{}, boundFailure("invalid_subject")
	}
	for _, m := range r.Milestones {
		if m.ID == subject.MilestoneID {
			if m.FrozenAt != nil || m.Stage == "Finalizing" || m.ShippedAt != nil {
				return m, boundFailure("frozen")
			}
			if m.Revision != subject.Revision {
				return m, boundFailure("stale_revision")
			}
			return m, nil
		}
	}
	return blackboard.Milestone{}, boundFailure("missing_milestone")
}

// Main's stored grant gates the operation. A decision read additionally uses
// the owner's authenticated answer login; no write transaction spans either read.
func (a *API) cutBoardProof(ctx context.Context, r blackboard.Record, board, sid, login string, full bool) (mcpgw.Proof, error) {
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		return mcpgw.Proof{}, boundFailure("db_unavailable")
	}
	if row == nil || row.SpawningAccountID == nil || *row.SpawningAccountID != r.Owner.ID || row.ParentSessionID != nil || row.Kind != "agent" || (sessionInputsEnded(row) || row.Status == "ended" || row.Status == "stopped") || row.ArchivedAt != nil || row.DiscardRequestedAt != nil {
		return mcpgw.Proof{}, boundFailure("reconciliation_main_ended")
	}
	root, err := blackboardMainSession(ctx, a.dbPool, r.Owner.ID, sid)
	if err != nil {
		return mcpgw.Proof{}, boundFailure("db_unavailable")
	}
	if root != sid {
		return mcpgw.Proof{}, boundFailure("main_required")
	}
	level, err := a.effectiveBoardLevel(ctx, row, board)
	if err != nil {
		return mcpgw.Proof{}, boundFailure("db_unavailable")
	}
	proof, ok := level.proof()
	if !ok || !mcpgw.BoardLevelAllows(level.Level, "blerg_release_get") {
		return mcpgw.Proof{}, boundFailure("reconciliation_access_revoked")
	}
	if full && (level.Level != db.BoardLevelFull || !mcpgw.PersonBehind(proof) || proof.AccountID != r.Owner.ID) {
		return mcpgw.Proof{}, boundFailure("lost_credentials")
	}
	if login != "" {
		proof = mcpgw.Proof{AccountID: r.Owner.ID, SessionID: login}
	}
	return proof, nil
}
func (a *API) readCutRelease(ctx context.Context, proof mcpgw.Proof, board, release, sid string) (cutReleaseView, error) {
	var out cutReleaseView
	caller := a.sessionBoardCaller()
	if caller == nil {
		return out, boundFailure("board_unavailable")
	}
	args, _ := json.Marshal(map[string]any{"board_id": board, "release_id": release, "evidence_only": true})
	raw, err := caller.CallBoard(ctx, mcpgw.BoardCall{Proof: proof, BoardID: board, SessionRef: sid, Tool: "blerg_release_get", Arguments: args, ReadOnly: true})
	if err != nil {
		if errors.Is(err, mcpgw.ErrConnectionGone) {
			return out, boundFailure("reconciliation_access_expired_or_revoked")
		}
		return out, boundFailure("release_unavailable")
	}
	if err := boardToolResult(raw, &out); err != nil {
		if err.Status == 401 || err.Status == 403 {
			return out, boundFailure("reconciliation_access_expired_or_revoked")
		}
		return out, boundFailure("release_unavailable")
	}
	if out.Release.ID != release || out.Release.BoardID != board {
		return out, boundFailure("invalid_release_link")
	}
	return out, nil
}
func (a *API) preflightBoundCut(ctx context.Context, r blackboard.Record, subject boundSubject, login, sid string) (cutReleaseView, error) {
	var out cutReleaseView
	if _, err := cutMilestone(r, subject); err != nil {
		return out, err
	}
	c := subject.Cut
	proof, err := a.cutBoardProof(ctx, r, c.BoardID, sid, login, false)
	if err != nil {
		return out, err
	}
	out, err = a.readCutRelease(ctx, proof, c.BoardID, c.ReleaseID, sid)
	if err != nil {
		return out, err
	}
	if out.Release.Status == "released" {
		return out, boundFailure("release_already_released")
	}
	if out.Preview == nil {
		return out, boundFailure("missing_cut_preview")
	}
	digest, err := releasecut.SnapshotDigest(out.Preview.Snapshot, c.BoardID, c.ReleaseID)
	if err != nil || digest != out.Preview.Digest {
		return out, boundFailure("invalid_cut_preview")
	}
	if c.CardDigest != "" && c.CardDigest != digest {
		return out, boundFailure("changed_release")
	}
	open := false
	for _, card := range out.Preview.Snapshot {
		if !card.Terminal {
			open = true
		}
	}
	if c.NextReleaseID == "" {
		if open {
			return out, boundFailure("next_release_required")
		}
	} else {
		if c.NextReleaseID == c.ReleaseID {
			return out, boundFailure("invalid_release_link")
		}
		next, err := a.readCutRelease(ctx, proof, c.BoardID, c.NextReleaseID, sid)
		if err != nil {
			return out, err
		}
		out.NextName = next.Release.Name
		if next.Release.Status == "released" {
			return out, boundFailure("next_release_closed")
		}
	}
	return out, nil
}
func verifyBoundCut(r blackboard.Record, subject boundSubject) error {
	m, err := cutMilestone(r, subject)
	if err != nil {
		return err
	}
	c := subject.Cut
	if m.BoardID != c.BaseBoardID || m.ReleaseID != c.BaseReleaseID {
		return boundFailure("changed_release_link")
	}
	if !c.Link && (m.BoardID != c.BoardID || m.ReleaseID != c.ReleaseID) {
		return boundFailure("explicit_release_link_required")
	}
	return nil
}
func (a *API) createBoundCutAsk(ctx context.Context, req postMessageRequest, session *db.SessionRow) (string, error) {
	subject := *req.Subject
	if subject.Cut == nil || subject.BlackboardID == "" {
		return "", boundFailure("invalid_subject")
	}
	record, err := a.blackboardStore().LoadPartition(ctx, subject.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionCore})
	if err != nil {
		return "", err
	}
	if record.Owner.Kind != "human" || record.Owner.ID != *session.SpawningAccountID || blackboard.WorkspaceKey(record) != session.ID {
		return "", boundFailure("wrong_owner")
	}
	m, err := cutMilestone(record, subject)
	if err != nil {
		return "", err
	}
	c := *subject.Cut
	c.BaseBoardID = m.BoardID
	c.BaseReleaseID = m.ReleaseID
	c.CardDigest = ""
	subject = boundSubject{Kind: "cut", BlackboardID: record.ID, MilestoneID: m.ID, Revision: m.Revision, Cut: &c}
	if err = verifyBoundCut(record, subject); err != nil {
		return "", err
	}
	view, err := a.preflightBoundCut(ctx, record, subject, "", session.ID)
	if err != nil {
		return "", err
	}
	c.CardDigest = view.Preview.Digest
	c.Snapshot = view.Preview.Snapshot
	archived, moved := 0, 0
	for _, entry := range view.Preview.Snapshot {
		if entry.Terminal {
			archived++
		} else {
			moved++
		}
	}
	next := "none (no open cards)"
	if c.NextReleaseID != "" {
		next = boundSummaryField(view.NextName)
	}
	link := ""
	if c.Link {
		link = fmt.Sprintf("Link milestone %s to this exact board release before the cut.", boundSummaryField(m.Name))
	}
	var operations []string
	for _, entry := range c.Snapshot {
		var card struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
		}
		if err := json.Unmarshal(entry.Card, &card); err != nil {
			return "", boundFailure("invalid_cut_preview")
		}
		operation := "Move"
		if entry.Terminal {
			operation = "Archive"
		}
		operations = append(operations, fmt.Sprintf("- %s: #%d %s", operation, card.Number, boundSummaryField(card.Title)))
	}
	body := fmt.Sprintf("Cut %s · %d archive, %d move\n\nBoard: %s\nMilestone: %s · revision %d\nNext release: %s\n%s\n\n%s\n\nReply to this question with yes to authorize this exact cut, or no to decline. Authorization expires 24 hours after your answer. This question expires in 24 hours.", boundSummaryField(view.Release.Name), archived, moved, boundSummaryField(view.BoardName), boundSummaryField(m.Name), m.Revision, next, link, strings.Join(operations, "\n"))
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := db.LockBlackboardDecision(ctx, tx, record.ID)
	if err != nil {
		return "", err
	}
	if !sameBoundScope(record, current) {
		return "", boundFailure("stale_revision")
	}
	if err = verifyBoundCut(current, subject); err != nil {
		return "", err
	}
	return a.persistBoundAsk(ctx, tx, session.ID, record.Owner.ID, subject, body, req.NoWait, req.Urgent, req.SessionPosted)
}

func recordReleaseLink(ctx context.Context, tx pgx.Tx, bid, mid, board, release, ask string, revision int) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO blackboard_release_links(blackboard_id,milestone_id,board_id,release_id,revision,origin_ask_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, bid, mid, board, release, revision, ask).Scan(&id)
	return id, err
}
func (a *API) authorizeBoundCut(ctx context.Context, tx pgx.Tx, subject boundSubject, record blackboard.Record, p identity.Principal, ask string) error {
	if err := verifyBoundCut(record, subject); err != nil {
		return err
	}
	c := subject.Cut
	var linkID int64
	if c.Link {
		err := (db.BlackboardStore{Pool: a.dbPool, Tx: tx}).UpdatePartition(ctx, record.ID, blackboard.Partition{Kind: blackboard.PartitionPlan, MilestoneID: subject.MilestoneID}, func(r *blackboard.Record) error {
			if err := verifyBoundCut(*r, subject); err != nil {
				return err
			}
			for i := range r.Milestones {
				m := &r.Milestones[i]
				if m.ID == subject.MilestoneID {
					m.BoardID = c.BoardID
					m.ReleaseID = c.ReleaseID
					m.Version++
				}
			}
			found := false
			for _, link := range r.Links {
				if link.BoardID == c.BoardID && link.WorkspaceID == blackboard.WorkspaceKey(record) {
					found = true
				}
			}
			if !found {
				r.Links = append(r.Links, blackboard.Link{BoardID: c.BoardID, WorkspaceID: blackboard.WorkspaceKey(record)})
			}
			raw, _ := json.Marshal(map[string]any{"ask_id": ask, "board_id": c.BoardID, "release_id": c.ReleaseID})
			r.Events = append(r.Events, blackboard.Event{ID: blackboard.NewID(), MilestoneID: subject.MilestoneID, AuthenticatedActor: blackboard.Actor{Kind: "human", ID: p.Sub}, RecordedAt: time.Now(), Source: "person", Kind: "release_linked", Payload: raw})
			return nil
		})
		if err != nil {
			return err
		}
		linkID, err = recordReleaseLink(ctx, tx, record.ID, subject.MilestoneID, c.BoardID, c.ReleaseID, ask, subject.Revision)
		if err != nil {
			return err
		}
	} else {
		var board, release string
		err := tx.QueryRow(ctx, `SELECT id,board_id,release_id FROM blackboard_release_links WHERE milestone_id=$1 AND blackboard_id=$2 ORDER BY id DESC LIMIT 1 FOR SHARE`, subject.MilestoneID, record.ID).Scan(&linkID, &board, &release)
		if err != nil || board != c.BoardID || release != c.ReleaseID {
			return boundFailure("explicit_release_link_required")
		}
	}
	principal, _ := json.Marshal(p.Claims)
	_, err := tx.Exec(ctx, `INSERT INTO blackboard_cut_executions(ask_id,workspace_id,blackboard_id,milestone_id,link_id,principal) VALUES($1,$2,$3,$4,$5,$6)`, ask, blackboard.WorkspaceKey(record), record.ID, subject.MilestoneID, linkID, principal)
	return err
}
