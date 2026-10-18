package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/contracts/boardfiling"
)

func validFilingDraft(d boardfiling.Draft) bool {
	clean := func(s string, n int) bool {
		return utf8.ValidString(s) && utf8.RuneCountInString(s) <= n && !strings.ContainsFunc(s, unicode.IsControl)
	}
	if strings.TrimSpace(d.Title) == "" || !clean(d.Title, 200) || len(d.Body) > 256<<10 || !utf8.ValidString(d.Body) || strings.ContainsRune(d.Body, 0) || !clean(d.Type, 64) || len(d.Repos) > 100 {
		return false
	}
	for _, repo := range d.Repos {
		if repo == "" || !clean(repo, 512) {
			return false
		}
	}
	o := d.Origin
	u, err := url.Parse(o.SessionURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !clean(o.SessionURL, 2048) {
		return false
	}
	return clean(o.Repo, 512) && clean(o.Commit, 64) && clean(o.Engine, 64) && o.Engine != "" && clean(o.ComponentVersion, 200) && clean(o.ComponentCommit, 64)
}

// handleFileCard is the sole use of a narrow Core-signed approval capability. Neither native
// Board tokens nor broader Core tokens can enter this path. Its receipt contains no card body.
func (a *API) handleFileCard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if !p.FromCore || p.Token == nil || p.Token.Kind != "agent" || p.Token.BoardID == nil || *p.Token.BoardID != boardID || !uuidRe.MatchString(p.CoreAccountID) || p.CoreAccountID != p.Token.LineageID {
		writeError(w, 403, "A signed one-card approval is required.")
		return
	}
	intent, digest, ok := boardfiling.ParseCapability(p.Token.Capabilities)
	if !ok || p.Token.ID != "core:"+intent {
		writeError(w, 403, "A signed one-card approval is required.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var draft boardfiling.Draft
	if dec.Decode(&draft) != nil || dec.Decode(new(any)) != io.EOF || !validFilingDraft(draft) {
		writeError(w, 400, "The approved card draft is invalid.")
		return
	}
	if actual := boardfiling.Digest(boardID, draft); subtle.ConstantTimeCompare([]byte(actual), []byte(digest)) != 1 {
		writeError(w, 403, "The card draft differs from the person's approval.")
		return
	}
	// The signed intent is not a native Board token row and must not become a foreign-key actor.
	receipt, replayed, err := db.FileApprovedCard(r.Context(), a.Pool, boardID, p.CoreAccountID, intent, digest, draft, db.EventMeta{Actor: "agent"})
	if errors.Is(err, db.ErrFilingIntentUsed) {
		writeError(w, 409, "This approval has already been used for a different card.")
		return
	}
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(boardID, "card_changed")
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, receipt)
}

// CardForPrincipal is the only positive opt-in to load restricted reproduction details.
// All general DB cards, searches, snapshots, histories and event consumers remain redacted.
func (a *API) CardForPrincipal(ctx context.Context, p auth.Principal, card db.Card) (db.Card, error) {
	if card.Sensitive && p.RequireAdmin(card.BoardID) == nil {
		body, err := db.SensitiveCardBody(ctx, a.Pool, card.ID)
		if err != nil {
			return card, err
		}
		card.Body = body
	}
	return card, nil
}
