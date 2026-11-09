package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ticket"
	"github.com/blerglab/blerg-ai/runner/internal/workbranch"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ensureWorkBranch runs only after the session's verified origin is recorded,
// before a runtime is launched. Resume and preference edits keep the saved name.
func ensureWorkBranch(ctx context.Context, pool *pgxpool.Pool, id string, identity func(context.Context, string) (string, error), cardReference ...func(context.Context, *db.SessionRow, ticket.Ticket) string) (string, error) {
	return ensureWorkBranchDefault(ctx, pool, id, identity, false, cardReference...)
}

// Older daemons keep the legacy default. Template choice is captured separately
// from the ref and stays fixed across preference changes and resumes.
func ensureWorkBranchDefault(ctx context.Context, pool *pgxpool.Pool, id string, identity func(context.Context, string) (string, error), legacyDefault bool, cardReference ...func(context.Context, *db.SessionRow, ticket.Ticket) string) (string, error) {
	if pool == nil {
		return workbranch.Effective(id, ""), nil
	}
	row, err := db.GetSession(ctx, pool, id)
	if err != nil || row == nil {
		return "", errors.New("session branch could not be read")
	}
	if row.WorkBranch != "" {
		if !workbranch.Valid(row.WorkBranch) {
			return "", errors.New("invalid recorded work branch")
		}
		return row.WorkBranch, nil
	}
	template, username := "", ""
	if row.SpawningAccountID != nil {
		raw, err := db.GetAccountPreference(ctx, pool, *row.SpawningAccountID, prefBranchTemplate)
		if err != nil {
			return "", err
		}
		if raw != nil && json.Unmarshal(raw, &template) != nil {
			return "", errors.New("invalid branch preference")
		}
		if strings.Contains(template, "{username}") {
			if identity == nil {
				return "", errors.New("branch username unavailable")
			}
			username, err = identity(ctx, *row.SpawningAccountID)
			if err != nil {
				return "", err
			}
		}
	}
	title := ""
	if row.Title != nil {
		title = *row.Title
	}
	// A pinned board card is captured at launch; later pins never rename a branch.
	card := ""
	rows, err := db.ListTicketRows(ctx, pool, id, 1<<62)
	if err != nil {
		return "", err
	}
	if pins := ticket.Pinned(rows); len(pins) > 0 {
		card = pins[0].Key
		if len(cardReference) > 0 && strings.Contains(template, "{card}") {
			card = cardReference[0](ctx, row, ticket.Ticket{Source: pins[0].Source, Key: pins[0].Key, BoardID: pins[0].BoardID})
		}
	}
	resolvedTemplate := template
	if legacyDefault && template == "" {
		resolvedTemplate = "wip/{session}"
	}
	candidate, err := workbranch.Resolve(resolvedTemplate, workbranch.Context{Username: username, Session: id, Title: title, Card: card, Date: row.StartedAt})
	if err != nil {
		return "", err
	}
	return db.AssignSessionBranch(ctx, pool, id, candidate, template != "")
}

func (a *API) branchUsername(ctx context.Context, account string) (string, error) {
	st, err := a.fetchAccountStandingName(ctx, account, true)
	if err != nil || !st.Exists || !st.Active || st.Username == "" {
		return "", errors.New("branch username unavailable")
	}
	return st.Username, nil
}

func (a *API) sessionWorkBranch(ctx context.Context, id string) (string, error) {
	return ensureWorkBranch(ctx, a.dbPool, id, a.branchUsername, a.branchCardReference)
}

func (a *API) recordedWorkBranch(ctx context.Context, id string) string {
	if a.dbPool != nil {
		if row, err := db.GetSession(ctx, a.dbPool, id); err == nil && row != nil {
			return workbranch.Effective(id, row.WorkBranch)
		}
	}
	return workbranch.Effective(id, "")
}

func (a *API) branchCardReference(ctx context.Context, row *db.SessionRow, pin ticket.Ticket) string {
	board, number, ok := ticket.BoardCard(pin)
	if ok {
		if card, err := a.planReadCard(ctx, row, board, number); err == nil && card.Reference != "" {
			return card.Reference
		}
	}
	return pin.Key
}

func recordedBranchCompatible(session, branch string, supported bool) bool {
	return supported || branch == "" || branch == workbranch.Effective(session, "")
}

func workBranchDaemonProblem(dc *DaemonConn, session, branch string, templateChosen bool) string {
	if recordedBranchCompatible(session, branch, dc.CanWorkBranches()) {
		return ""
	}
	if templateChosen {
		return "Update this workstation to use custom branch names."
	}
	return "Update this workstation to resume the recorded work branch."
}

func (a *API) daemonWorkBranch(ctx context.Context, id string, dc *DaemonConn) (string, bool, error) {
	branch, err := ensureWorkBranchDefault(ctx, a.dbPool, id, a.branchUsername, !dc.CanWorkBranches(), a.branchCardReference)
	if err != nil || a.dbPool == nil {
		return branch, false, err
	}
	row, err := db.GetSession(ctx, a.dbPool, id)
	if err != nil {
		return "", false, err
	}
	if row == nil {
		return "", false, errors.New("session branch intent could not be read")
	}
	return branch, row.WorkBranchCustom, nil
}
