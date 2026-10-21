package server

// The outcome of a session's proposal, told to its agent. A filing or an MCP call in approval mode
// answers the agent "queued"; the person decides later, in the chat or on the Proposals page. When
// they do (or a session permission does, or an approval ends failed or unknown), the agent gets a
// message, the way a review decision reaches it (deliverReviewDecision), and can confirm with
// `blerg-runner ticket status <id>` (GET /api/sessions/{id}/proposals/{pid}, the session's own
// token only).

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"unicode"

	"context"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// proposalNote cleans a person's reason: one line, at most 500 characters.
func proposalNote(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return clipRunes(strings.Join(strings.Fields(s), " "), 500)
}

// proposalOutcome is what the result of a decided proposal says, as the agent and the status
// route read it: a created card, the person's note, or the recorded error. Never a card body or
// an MCP result beyond its text.
type proposalOutcome struct {
	Card  *filedCard
	Note  string
	Error string
	Text  string
}

func readProposalOutcome(p *db.Proposal) proposalOutcome {
	var out proposalOutcome
	var r struct {
		Note    string `json:"note"`
		Error   string `json:"error"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(p.Result, &r)
	out.Note, out.Error = clipRunes(r.Note, 500), clipRunes(r.Error, 500)
	if db.IsBoardFilingProposal(p) && p.State == db.ProposalDone {
		var card filedCard
		if json.Unmarshal(p.Result, &card) == nil && card.ID != "" && card.Number > 0 {
			out.Card = &card
		}
	}
	if !db.IsBoardFilingProposal(p) && p.State == db.ProposalDone {
		out.Text = clipRunes(resultText(p.Result), 1000)
	}
	return out
}

// proposalDecisionMessage is what the session reads about a decided proposal, for example
//
//	Proposal <id> approved: board "Launch Night" #6 "Live check" created.
//	Proposal <id> rejected: Duplicate of #3. No card was created.
//	Proposal <id> could not be confirmed: …
func proposalDecisionMessage(p *db.Proposal) string {
	o := readProposalOutcome(p)
	board := db.IsBoardFilingProposal(p)
	head := "Proposal " + p.ID
	by := ""
	if p.ApprovedByGrant != "" {
		by = " by a session permission the person gave"
	}
	check := " Confirm with: blerg-runner ticket status " + p.ID
	var b strings.Builder
	switch p.State {
	case db.ProposalDone:
		switch {
		case board && o.Card != nil:
			name := "the board"
			if o.Card.BoardName != "" {
				name = "board " + quotedName(o.Card.BoardName)
			}
			fmt.Fprintf(&b, "%s approved%s: %s #%d", head, by, name, o.Card.Number)
			if o.Card.Title != "" {
				b.WriteString(" " + quotedName(o.Card.Title))
			}
			b.WriteString(" created.")
		case board:
			fmt.Fprintf(&b, "%s approved%s: the card was created.", head, by)
		default:
			// The MCP server's answer is not repeated here: a message is the person's channel, and
			// that text is the server's. The status shows it, as a tool result.
			fmt.Fprintf(&b, "%s approved%s: %s %s ran; its result is in the status.", head, by, p.ConnectionName, p.Tool)
		}
	case db.ProposalRejected:
		b.WriteString(head + " rejected")
		if o.Note != "" {
			b.WriteString(": " + strings.TrimRight(o.Note, ".") + ".")
		} else {
			b.WriteString(" by the person.")
		}
		if board {
			b.WriteString(" No card was created; do not file it again unless the person asks.")
		} else {
			b.WriteString(" Nothing was run; do not call it again unless the person asks.")
		}
	case db.ProposalFailed:
		b.WriteString(head + " failed")
		if o.Error != "" {
			b.WriteString(": " + strings.TrimRight(o.Error, ".") + ".")
		} else {
			b.WriteString(".")
		}
		if board {
			b.WriteString(" No card was created.")
		}
	case db.ProposalUnknown:
		b.WriteString(head + " could not be confirmed")
		if o.Error != "" {
			b.WriteString(": " + strings.TrimRight(o.Error, ".") + ".")
		} else {
			b.WriteString(".")
		}
		if board {
			b.WriteString(" Do not file it again: the person checks the board and records what happened.")
		} else {
			b.WriteString(" Do not call it again: the person checks and records what happened.")
		}
	case db.ProposalExpired:
		b.WriteString(head + " expired without a decision; nothing was done.")
	default:
		return ""
	}
	b.WriteString(check)
	return b.String()
}

// deliverProposalDecision sends a decided proposal's outcome to its session's agent as a message:
// durable for an agent session whose host takes receipts (replayed on the daemon's reconnect), a
// note reply for a terminal session. Best effort, logged.
func (a *API) deliverProposalDecision(ctx context.Context, p *db.Proposal) {
	text := proposalDecisionMessage(p)
	if text == "" || p.SessionID == "" {
		return
	}
	sess, err := db.GetSession(ctx, a.dbPool, p.SessionID)
	if err != nil || sess == nil || terminalSessionStatus(sess.Status) {
		return
	}
	if sess.Kind != "agent" {
		a.routeNoteReply(db.MessageRow{SessionID: p.SessionID, Body: "proposal " + p.ID}, text)
		return
	}
	decider := p.AccountID
	if p.DecidedBy != nil && *p.DecidedBy != "" {
		decider = *p.DecidedBy
	}
	principal := runnerPrincipal{Kind: "human", Sub: decider}
	clientID := ""
	if dc := a.hub.FindDaemonForSession(p.SessionID); dc == nil || dc.CanInputReceipts() {
		clientID = "proposal-" + p.ID + "-" + p.State
	}
	var apiErr *APIError
	if clientID != "" {
		apiErr = a.sendRelay(ctx, principal, p.SessionID, text, "proposal", clientID)
	} else {
		apiErr = a.sendRelay(ctx, principal, p.SessionID, text, "proposal")
	}
	if apiErr != nil {
		log.Printf("proposals: deliver %s to %s: %s", p.ID, p.SessionID, apiErr.Message)
	}
}

// HandleSessionProposalStatus is GET /api/sessions/{id}/proposals/{pid}: where one of the session's
// proposals stands, for its own agent (`blerg-runner ticket status`). Only the session's own token
// reads it; another session's proposal, a missing one and a malformed id are the same 404. It
// carries the outcome (a created card, the person's note, the error), never the frozen draft.
func (a *API) HandleSessionProposalStatus(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	tokenSID, ok := a.authMessaging(w, r)
	if !ok {
		return
	}
	if tokenSID == "" || tokenSID != sid {
		writeError(w, http.StatusForbidden, "only the session's own token reads its proposals")
		return
	}
	if a.dbPool == nil || !validSessionID(sid) {
		writeError(w, http.StatusNotFound, msgProposalNotFound)
		return
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil || row == nil || derefOrEmpty(row.SpawningAccountID) == "" {
		writeError(w, http.StatusNotFound, msgProposalNotFound)
		return
	}
	p, err := db.GetOwnedProposal(r.Context(), a.dbPool, *row.SpawningAccountID, r.PathValue("pid"))
	if err != nil || p.SessionID != sid || db.IsSessionPluginProposal(p) {
		writeError(w, http.StatusNotFound, msgProposalNotFound)
		return
	}
	o := readProposalOutcome(p)
	out := map[string]any{"proposal_id": p.ID, "kind": proposalEventKind(p), "state": p.State, "auto_approved": p.ApprovedByGrant != ""}
	if o.Card != nil {
		out["card"] = o.Card
	}
	if o.Note != "" {
		out["note"] = o.Note
	}
	if o.Error != "" && (p.State == db.ProposalFailed || p.State == db.ProposalUnknown) {
		out["error"] = o.Error
	}
	if o.Text != "" {
		out["result"] = o.Text
	}
	if p.State != db.ProposalPending && p.State != db.ProposalExecuting {
		out["message"] = proposalDecisionMessage(p)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}
