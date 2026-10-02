package api

import (
	"context"
	"encoding/json"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/gate"
)

// Service-layer entry points shared by the MCP surface. Each returns either a
// result or a non-nil Denied outcome carrying the gate's structured verdict.

type CardWriteResult struct {
	Card      db.Card
	Refreshed bool
	Denied    *gate.Outcome // non-nil ⇒ the write did not happen
}

func (a *API) gateFor(ctx context.Context, p auth.Principal, board db.Board, current *db.Card, op string, payload json.RawMessage, disputeOf, rebuttal string) (*gate.Outcome, error) {
	if p.IsHuman() || (p.Token != nil && p.Token.HasCap("gate.bypass")) ||
		!board.GateEnabled || a.Gate == nil {
		return &gate.Outcome{Allowed: true}, nil
	}
	cols, err := db.ListColumns(ctx, a.Pool, board.ID)
	if err != nil {
		return nil, err
	}
	out, err := a.Gate.Check(ctx, board, cols, current, p.GateActorTokenID(), op, payload, disputeOf, rebuttal)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateCardGated is the MCP-facing create path (same gate semantics as HTTP).
func (a *API) CreateCardGated(ctx context.Context, p auth.Principal, boardID string, params db.CardParams, disputeOf, rebuttal string) (CardWriteResult, error) {
	if err := p.RequireBoard(boardID, "card.write"); err != nil {
		return CardWriteResult{}, err
	}
	if err := a.resolveArtifactLinks(&params); err != nil {
		return CardWriteResult{}, err
	}
	board, err := db.GetBoard(ctx, a.Pool, boardID)
	if err != nil {
		return CardWriteResult{}, err
	}
	payload, _ := json.Marshal(params)
	out, err := a.gateFor(ctx, p, board, nil, "create", payload, disputeOf, rebuttal)
	if err != nil {
		return CardWriteResult{}, err
	}
	if !out.Allowed {
		return CardWriteResult{Denied: out}, nil
	}
	res, err := db.CreateCard(ctx, a.Pool, boardID, params, meta(p, out.ReviewID))
	if err != nil {
		return CardWriteResult{}, err
	}
	a.stampGate(ctx, res.Card.ID, *out)
	a.Hub.Broadcast(boardID, "card_changed")
	return CardWriteResult{Card: res.Card, Refreshed: res.Refreshed}, nil
}

func (a *API) UpdateCardGated(ctx context.Context, p auth.Principal, cardID string, params db.CardParams, disputeOf, rebuttal string) (CardWriteResult, error) {
	card, err := db.GetCard(ctx, a.Pool, cardID)
	if err != nil {
		return CardWriteResult{}, err
	}
	if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
		return CardWriteResult{}, err
	}
	if err := a.resolveArtifactLinks(&params); err != nil {
		return CardWriteResult{}, err
	}
	board, err := db.GetBoard(ctx, a.Pool, card.BoardID)
	if err != nil {
		return CardWriteResult{}, err
	}
	payload, _ := json.Marshal(params)
	out, err := a.gateFor(ctx, p, board, &card, "update", payload, disputeOf, rebuttal)
	if err != nil {
		return CardWriteResult{}, err
	}
	if !out.Allowed {
		return CardWriteResult{Denied: out}, nil
	}
	updated, err := db.UpdateCard(ctx, a.Pool, card.ID, params, meta(p, out.ReviewID))
	if err != nil {
		return CardWriteResult{}, err
	}
	a.stampGate(ctx, card.ID, *out)
	a.Hub.Broadcast(card.BoardID, "card_changed")
	return CardWriteResult{Card: updated}, nil
}

func (a *API) MoveCardGated(ctx context.Context, p auth.Principal, cardID, columnID string, beforeCardID *string, ifMatch *int, disputeOf, rebuttal string) (CardWriteResult, error) {
	card, err := db.GetCard(ctx, a.Pool, cardID)
	if err != nil {
		return CardWriteResult{}, err
	}
	if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
		return CardWriteResult{}, err
	}
	board, err := db.GetBoard(ctx, a.Pool, card.BoardID)
	if err != nil {
		return CardWriteResult{}, err
	}
	payload, _ := json.Marshal(map[string]any{"card": card.Number, "to_column": columnID})
	out, err := a.gateFor(ctx, p, board, &card, "move", payload, disputeOf, rebuttal)
	if err != nil {
		return CardWriteResult{}, err
	}
	if !out.Allowed {
		return CardWriteResult{Denied: out}, nil
	}
	moved, err := db.MoveCard(ctx, a.Pool, card.ID, columnID, beforeCardID, ifMatch, meta(p, out.ReviewID))
	if err != nil {
		return CardWriteResult{}, err
	}
	a.stampGate(ctx, card.ID, *out)
	a.Hub.Broadcast(card.BoardID, "card_changed")
	go a.afterMove(context.WithoutCancel(ctx), board, moved) // detached: the review-flow reaction (spawn reviewer / relay findings) must outlive the request
	return CardWriteResult{Card: moved}, nil
}

func (a *API) ArchiveCardGated(ctx context.Context, p auth.Principal, cardID string) (CardWriteResult, error) {
	card, err := db.GetCard(ctx, a.Pool, cardID)
	if err != nil {
		return CardWriteResult{}, err
	}
	if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
		return CardWriteResult{}, err
	}
	board, err := db.GetBoard(ctx, a.Pool, card.BoardID)
	if err != nil {
		return CardWriteResult{}, err
	}
	payload, _ := json.Marshal(map[string]any{"card": card.Number, "archive": true})
	out, err := a.gateFor(ctx, p, board, &card, "archive", payload, "", "")
	if err != nil {
		return CardWriteResult{}, err
	}
	if !out.Allowed {
		return CardWriteResult{Denied: out}, nil
	}
	archived, err := db.ArchiveCard(ctx, a.Pool, card.ID, nil, meta(p, out.ReviewID))
	if err != nil {
		return CardWriteResult{}, err
	}
	a.Hub.Broadcast(card.BoardID, "card_changed")
	return CardWriteResult{Card: archived}, nil
}
