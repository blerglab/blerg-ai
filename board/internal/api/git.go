package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

type workspaceGitReader interface {
	ReadGit(context.Context, string, url.Values, bool) ([]byte, int, error)
}

func (a *API) handleCardGit(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	a.cardLocalGit(w, r, p, false)
}

// Board access alone never authorizes local repository bytes. The Runner checks
// the current human's ownership and the live workspace binding for every read.
func (a *API) cardLocalGit(w http.ResponseWriter, r *http.Request, p auth.Principal, patch bool) {
	w.Header().Set("Cache-Control", "private, no-store")
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.read"); err != nil {
		writeError(w, 403, err.Error())
		return
	}
	if !p.IsHuman() {
		writeError(w, 403, "Sign in to read workspace Git.")
		return
	}
	if a.runner == nil {
		writeError(w, 409, "Open a workspace for this card to read local Git.")
		return
	}
	reader, ok := a.runner.Driver.(workspaceGitReader)
	if !ok {
		writeError(w, 409, "Local Git is unavailable on this runner.")
		return
	}

	// Offer only sessions associated with this card. A session link may point at
	// a workspace opened outside the Board; the Runner still checks its owner.
	rows, err := a.Pool.Query(r.Context(), `SELECT external_session_id, role FROM runner_sessions WHERE card_id=$1 ORDER BY (role='worker') DESC, created_at DESC`, card.ID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	sessions := []map[string]string{}
	seen := map[string]bool{}
	for rows.Next() {
		var id, role string
		if err := rows.Scan(&id, &role); err != nil {
			rows.Close()
			writeDBError(w, err)
			return
		}
		sessions = append(sessions, map[string]string{"id": id, "name": role})
		seen[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeDBError(w, err)
		return
	}
	for _, link := range card.Links {
		prefix := strings.TrimRight(a.runner.UIBase, "/") + "/"
		if a.runner.UIBase != "" && link.Kind == "session" && strings.HasPrefix(link.URL, prefix) {
			id := strings.TrimPrefix(link.URL, prefix)
			if validGitSessionID(id) && !seen[id] {
				sessions = append(sessions, map[string]string{"id": id, "name": "Workspace"})
				seen[id] = true
			}
		}
	}
	if len(sessions) == 0 {
		writeError(w, 409, "Open a workspace for this card and add its session link to read local Git.")
		return
	}
	sid := r.URL.Query().Get("session_id")
	if sid == "" {
		sid = sessions[0]["id"]
	}
	if !seen[sid] {
		writeError(w, 404, "Workspace not linked to this card.")
		return
	}
	q := url.Values{}
	for _, key := range []string{"member_id", "generation", "launch_generation", "branch", "base"} {
		if value := r.URL.Query().Get(key); value != "" {
			q.Set(key, value)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	body, status, err := reader.ReadGit(ctx, sid, q, patch)
	if err != nil {
		if !patch && status != http.StatusUnauthorized {
			writeJSON(w, 200, map[string]any{"sessions": sessions, "session_id": sid, "commits_error": err.Error()})
			return
		}
		writeError(w, status, err.Error())
		return
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	if patch {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.WriteHeader(200)
		_, _ = w.Write(body) //nolint:gosec // G705: the diff is text/plain with nosniff and sandbox, never rendered as HTML
		return
	}
	var info map[string]any
	if json.Unmarshal(body, &info) != nil || info == nil {
		writeError(w, 502, "Invalid workspace Git response.")
		return
	}
	info["sessions"] = sessions
	info["session_id"] = sid
	writeJSON(w, 200, info)

}

func validGitSessionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
