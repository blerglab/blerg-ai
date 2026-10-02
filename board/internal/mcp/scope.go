package mcp

import (
	"context"
	"encoding/json"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// Project scoping for the MCP surface (the REST twin is api/scope.go).
//
// A core-issued token (any kind) carrying a Project claim is confined to one
// board; the arguments are decoded once (canonicalArgs) and this check and the
// tool read that same value.
// Every tool's scope decision is in toolScopes; scopeCheck runs it in front of
// dispatch so a board id, card id, column id or review id that belongs to
// another board fails with db.ErrNotFound, the very error a missing id gets.
// A tool with no entry is refused for scoped tokens, and
// TestEveryToolHasAScopeDecision fails until one is added.

type toolScopeRule int

const (
	// toolSelf: the tool narrows or refuses a scoped token itself (board_list
	// filters to the token's board).
	toolSelf toolScopeRule = iota + 1
	// toolBoardArg: args.board_id must be the token's board.
	toolBoardArg
	// toolColumnArg: args.column_id must belong to the token's board.
	toolColumnArg
	// toolCardRef: args.card_id (or args.board_id + number) must resolve to a
	// card on the token's board.
	toolCardRef
	// toolReviewArg: args.review_id must belong to the token's board.
	toolReviewArg
	// toolDeny: no board to scope to (instance-wide, or acts on a session a
	// core token cannot own); hidden from tools/list and refused.
	toolDeny
)

type toolScope struct {
	rule toolScopeRule
	why  string
}

var toolScopes = map[string]toolScope{
	"blerg_board_list":        {toolSelf, "filtered to the token's board"},
	"blerg_board_get":         {toolBoardArg, "board read"},
	"blerg_board_schema":      {toolBoardArg, "board schema"},
	"blerg_board_create":      {toolDeny, "creating a board is instance-wide"},
	"blerg_board_update":      {toolBoardArg, "board update (needs board.admin, which a scoped token never has)"},
	"blerg_column_list":       {toolBoardArg, "columns of the board"},
	"blerg_column_create":     {toolBoardArg, "create column"},
	"blerg_column_move":       {toolColumnArg, "column must be on the board"},
	"blerg_card_search":       {toolBoardArg, "search within the board"},
	"blerg_card_get":          {toolCardRef, "card read"},
	"blerg_card_create":       {toolBoardArg, "create card"},
	"blerg_card_update":       {toolCardRef, "card update"},
	"blerg_card_move":         {toolCardRef, "card move (target column is checked against the card's board)"},
	"blerg_card_archive":      {toolCardRef, "card archive"},
	"blerg_card_link":         {toolCardRef, "card link"},
	"blerg_card_comment":      {toolCardRef, "card comment"},
	"blerg_review_get":        {toolReviewArg, "review must belong to the board"},
	"blerg_session_set_model": {toolDeny, "a core token owns no runner session; the REST twin refuses the same way"},
}

// scopeCheck returns nil when the call may proceed.
func (s *Server) scopeCheck(ctx context.Context, p auth.Principal, name string, args json.RawMessage) error {
	board, scoped := p.ProjectScoped()
	if !scoped {
		return nil
	}
	rule, ok := toolScopes[name]
	if !ok {
		return db.ErrNotFound // unknown tool: fail closed
	}
	var a struct {
		BoardID  string `json:"board_id"`
		CardID   string `json:"card_id"`
		Number   int    `json:"number"`
		ColumnID string `json:"column_id"`
		ReviewID string `json:"review_id"`
	}
	_ = json.Unmarshal(args, &a)
	switch rule.rule {
	case toolSelf:
		return nil
	case toolDeny:
		return db.ErrNotFound
	case toolBoardArg:
		if a.BoardID == "" {
			return nil // the tool reports the missing argument itself
		}
		if a.BoardID != board {
			return db.ErrNotFound
		}
	case toolColumnArg:
		if a.ColumnID == "" {
			return nil
		}
		if !s.api.InScope(ctx, p, api.OwnerColumn, a.ColumnID) {
			return db.ErrNotFound
		}
	case toolCardRef:
		switch {
		case a.CardID != "":
			if !s.api.InScope(ctx, p, api.OwnerCard, a.CardID) {
				return db.ErrNotFound
			}
		case a.BoardID != "" && a.Number > 0:
			if a.BoardID != board {
				return db.ErrNotFound
			}
		}
		// Neither form present: the tool reports the missing reference.
	case toolReviewArg:
		if a.ReviewID == "" {
			return nil
		}
		if !s.api.InScope(ctx, p, api.OwnerReview, a.ReviewID) {
			return db.ErrNotFound
		}
	}
	return nil
}

// visibleTools is the tools/list answer: everything for an unscoped principal,
// and for a scoped one every tool except those refused outright.
func visibleTools(p auth.Principal) []map[string]any {
	if _, scoped := p.ProjectScoped(); !scoped {
		return toolDefs
	}
	out := make([]map[string]any, 0, len(toolDefs))
	for _, t := range toolDefs {
		name, _ := t["name"].(string)
		if rule, ok := toolScopes[name]; ok && rule.rule != toolDeny {
			out = append(out, t)
		}
	}
	return out
}
