package api

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/gate"
)

// Column audit (0.5.0): sessions drive a board's columns, so every column change is recorded, in
// the change's own transaction, with who made it and, for a runner session's board access, which
// session.

// RunnerSessionHeader carries the runner session a board-access call comes from. The runner sets it
// on the calls it makes with an exchanged credential (which the agent never holds); the board
// records it beside the token, which stays the authoritative actor.
const RunnerSessionHeader = "X-Blerg-Runner-Session"

var validRunnerSession = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type runnerSessionKey struct{}

// WithRunnerSession keeps a well-formed runner session reference for the column audit.
func WithRunnerSession(ctx context.Context, ref string) context.Context {
	if !validRunnerSession.MatchString(ref) {
		return ctx
	}
	return context.WithValue(ctx, runnerSessionKey{}, ref)
}

// ColumnAudit is the audit a column change by p records with it.
func ColumnAudit(ctx context.Context, p auth.Principal) db.ColumnAudit {
	who := db.ColumnActor{Actor: string(p.Kind), Account: p.CoreAccountID}
	if who.Account == "" {
		who.Account = p.HumanAccountID()
	}
	if p.Token != nil {
		who.Token = p.Token.ID
	}
	if !p.IsHuman() {
		who.RunnerSession, _ = ctx.Value(runnerSessionKey{}).(string)
	}
	return db.ColumnAudit{Who: who}
}

// columnRemovalCommitted is a test seam: called right after a column removal's cards moved.
var columnRemovalCommitted func()

// DeleteColumnMovingCardsGated removes col, moving its cards to toColumnID first. Moving the cards
// is moving cards: it needs card.write, and every card passes the board's admission gate as a move
// to toColumnID, exactly as blerg_card_move would. One denial and nothing changes (the outcome is
// returned). Otherwise the cards move, the column goes and the change is audited in one
// transaction; each card then gets what a card move gets after it lands (its gate stamp and the
// review-flow reaction; the destination's standing agents are queued in the transaction).
func (a *API) DeleteColumnMovingCardsGated(ctx context.Context, p auth.Principal, col db.Column, toColumnID string) ([]string, *gate.Outcome, error) {
	if err := p.RequireBoard(col.BoardID, "card.write"); err != nil {
		return nil, nil, err
	}
	if toColumnID == col.ID {
		return nil, nil, db.ErrMoveTargetIsColumn
	}
	if dest, err := db.GetColumn(ctx, a.Pool, toColumnID); err != nil || dest.BoardID != col.BoardID {
		return nil, nil, db.ErrNotFound
	}
	board, err := db.GetBoard(ctx, a.Pool, col.BoardID)
	if err != nil {
		return nil, nil, err
	}
	ids, err := db.ColumnCardIDs(ctx, a.Pool, col.ID)
	if err != nil {
		return nil, nil, err
	}
	outcomes := map[string]gate.Outcome{}
	reviews := map[string]*string{}
	for _, id := range ids {
		card, err := db.GetCard(ctx, a.Pool, id)
		if err != nil {
			return nil, nil, err
		}
		payload, _ := json.Marshal(map[string]any{"card": card.Number, "to_column": toColumnID, "reason": "its column is being removed"})
		out, err := a.gateFor(ctx, p, board, &card, "move", payload, "", "")
		if err != nil {
			return nil, nil, err
		}
		if !out.Allowed {
			return nil, out, nil
		}
		outcomes[id], reviews[id] = *out, out.ReviewID
	}
	moved, err := db.DeleteColumnMovingCards(ctx, a.Pool, col.ID, toColumnID, ids, p.EventMeta(), reviews, ColumnAudit(ctx, p))
	if err != nil {
		return nil, nil, err
	}
	if columnRemovalCommitted != nil {
		columnRemovalCommitted()
	}
	// Committed: the cards have moved whatever happens to the request now, so what follows a move
	// (gate stamp, review-flow reaction) runs on a context the caller's cancellation cannot stop.
	after, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, id := range moved {
		a.stampGate(after, id, outcomes[id])
		if card, err := db.GetCard(after, a.Pool, id); err == nil {
			go a.afterMove(context.WithoutCancel(ctx), board, card) // detached, as MoveCardGated
		} else {
			log.Printf("column removal: card %s moved but its move reaction could not load it: %v", id, err)
		}
	}
	a.Hub.Broadcast(col.BoardID, "card_changed")
	a.Hub.Broadcast(col.BoardID, "board_changed")
	return moved, nil, nil
}
