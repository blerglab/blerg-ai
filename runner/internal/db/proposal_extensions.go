package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProposalExtension preserves who extended which deadline without changing the frozen request.
type ProposalExtension struct {
	ExtendedBy        string    `json:"extended_by"`
	ExtendedAt        time.Time `json:"extended_at"`
	PreviousExpiresAt time.Time `json:"previous_expires_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	// ByYou is set by the server for the viewer: the history shows "you", not an account id.
	ByYou bool `json:"by_you"`
}

// ProposalExtensionStep is how far one extension moves the deadline.
const ProposalExtensionStep = 7 * 24 * time.Hour

// ProposalMaxLifetime is the longest a proposal can stay open, counted from when the agent proposed
// it (owner ruling, 0.4.4): extensions stop there, the last one is shortened to reach it exactly,
// and then the proposal expires. A proposal open that long means something is wrong.
const ProposalMaxLifetime = 30 * 24 * time.Hour

// ErrProposalExtensionCap is an extension refused because the proposal already reaches
// ProposalMaxLifetime.
var ErrProposalExtensionCap = errors.New("proposals can stay open up to 30 days")

// ProposalMaxDeadline is the latest deadline p can be extended to.
func ProposalMaxDeadline(p *Proposal) time.Time { return p.CreatedAt.Add(ProposalMaxLifetime) }

// ProposalExtendable says whether p's deadline can still move.
func ProposalExtendable(p *Proposal) bool { return p.Deadline().Before(ProposalMaxDeadline(p)) }

// ExtendProposal serializes against approval, rejection and expiry. The reviewed deadline is a
// compare-and-set, so a duplicate click cannot add two extensions. Expired proposals stay expired.
func ExtendProposal(ctx context.Context, pool *pgxpool.Pool, account, id string, expected time.Time) (*Proposal, error) {
	if !proposalUUID.MatchString(id) {
		return nil, ErrProposalNotFound
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	p, err := scanProposal(tx.QueryRow(ctx, `SELECT `+proposalColumns+` FROM mcp_proposals WHERE id=$1 AND account_id=$2 FOR UPDATE`, id, account))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProposalNotFound
	}
	if err != nil {
		return nil, err
	}
	if p.State != ProposalPending || !p.Deadline().Equal(expected) {
		return nil, ErrProposalState
	}
	if !ProposalExtendable(p) {
		return nil, ErrProposalExtensionCap
	}
	next, err := scanProposal(tx.QueryRow(ctx, `UPDATE mcp_proposals
 SET expires_at_override=LEAST(`+proposalDeadline+` + make_interval(secs => $3), created_at + make_interval(secs => $4))
 WHERE id=$1 AND account_id=$2 AND state='pending' AND `+proposalDeadline+` > now()
   AND `+proposalDeadline+` < created_at + make_interval(secs => $4) RETURNING `+proposalColumns,
		id, account, ProposalExtensionStep.Seconds(), ProposalMaxLifetime.Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProposalState
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO proposal_extensions(proposal_id,extended_by,previous_expires_at,expires_at) VALUES ($1,$2,$3,$4)`, id, account, p.Deadline(), next.Deadline()); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return next, nil
}

func ListProposalExtensions(ctx context.Context, pool *pgxpool.Pool, account, id string) ([]ProposalExtension, error) {
	byID, err := ProposalExtensionsFor(ctx, pool, account, []string{id})
	if err != nil {
		return nil, err
	}
	if out := byID[id]; out != nil {
		return out, nil
	}
	return []ProposalExtension{}, nil
}

// ProposalExtensionsFor is the extension history of the account's proposals ids, oldest first.
func ProposalExtensionsFor(ctx context.Context, pool *pgxpool.Pool, account string, ids []string) (map[string][]ProposalExtension, error) {
	out := map[string][]ProposalExtension{}
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if proposalUUID.MatchString(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `SELECT p.id::text,e.extended_by,e.extended_at,e.previous_expires_at,e.expires_at FROM proposal_extensions e JOIN mcp_proposals p ON p.id=e.proposal_id WHERE p.account_id=$1 AND p.id=ANY($2::uuid[]) ORDER BY e.id`, account, valid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var e ProposalExtension
		if err := rows.Scan(&id, &e.ExtendedBy, &e.ExtendedAt, &e.PreviousExpiresAt, &e.ExpiresAt); err != nil {
			return nil, err
		}
		out[id] = append(out[id], e)
	}
	return out, rows.Err()
}

// ProposalApproval is the safe audit projection of the permission that approved a proposal.
// Proof, credential and pinned-definition fields never leave the permission table.
type ProposalApproval struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	AccountID      string     `json:"account_id"`
	BoardID        string     `json:"board_id,omitempty"`
	BoardName      string     `json:"board_name,omitempty"`
	ConnectionName string     `json:"connection_name,omitempty"`
	Tool           string     `json:"tool,omitempty"`
	Level          string     `json:"level"`
	CreatedVia     string     `json:"created_via"`
	CreatedAt      time.Time  `json:"created_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	// GrantedByYou is set by the server for the viewer, as ProposalExtension.ByYou.
	GrantedByYou bool `json:"granted_by_you"`
}

func ProposalApprovalAudit(ctx context.Context, pool *pgxpool.Pool, account string, ids []string) (map[string]ProposalApproval, error) {
	out := map[string]ProposalApproval{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `SELECT p.id::text,g.id::text,g.kind,g.account_id,g.board_id,g.board_name,g.connection_name,g.tool,g.level,g.created_via,g.created_at,g.revoked_at
 FROM mcp_proposals p JOIN session_proposal_grants g ON g.id=p.approved_by_grant
 WHERE p.account_id=$1 AND g.account_id=$1 AND p.id=ANY($2::uuid[])`, account, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var a ProposalApproval
		if err := rows.Scan(&id, &a.ID, &a.Kind, &a.AccountID, &a.BoardID, &a.BoardName, &a.ConnectionName, &a.Tool, &a.Level, &a.CreatedVia, &a.CreatedAt, &a.RevokedAt); err != nil {
			return nil, err
		}
		out[id] = a
	}
	return out, rows.Err()
}
