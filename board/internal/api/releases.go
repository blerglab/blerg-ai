package api

import (
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

func (a *API) handleReleases(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	board := r.PathValue("id")
	capability := "card.read"
	if r.Method != "GET" {
		capability = "card.write"
	}
	if err := p.RequireBoard(board, capability); err != nil {
		writeError(w, 403, err.Error())
		return
	}
	if r.Method == "GET" {
		if r.PathValue("release") == "" {
			v, err := db.ListReleases(r.Context(), a.Pool, board)
			if err != nil {
				writeDBError(w, err)
				return
			}
			writeJSON(w, 200, v)
		} else {
			v, err := db.ReadRelease(r.Context(), a.Pool, board, r.PathValue("release"))
			if err != nil {
				writeDBError(w, err)
				return
			}
			writeJSON(w, 200, v)
		}
		return
	}
	if err := p.RequireArchive(board); err != nil {
		writeError(w, 403, err.Error())
		return
	}
	b, err := db.GetBoard(r.Context(), a.Pool, board)
	if err != nil {
		writeDBError(w, err)
		return
	}
	if b.DrivenBy != nil && *b.DrivenBy != "" {
		writeError(w, 403, "Mirrored boards are read-only.")
		return
	}
	if r.Method == "DELETE" {
		if err = db.DeleteRelease(r.Context(), a.Pool, board, r.PathValue("release")); err != nil {
			writeDBError(w, err)
			return
		}
		a.Hub.Broadcast(board, "board_changed")
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/close") {
		body, err := decode[struct {
			Next string `json:"next_release_id"`
		}](r)
		if err != nil {
			writeError(w, 400, "invalid JSON")
			return
		}
		out, err := a.CloseReleaseGated(r.Context(), p, board, r.PathValue("release"), body.Next)
		if err != nil {
			writeDBError(w, err)
			return
		}
		if out != nil {
			writeJSON(w, out.Status, out.Body)
			return
		}
		a.Hub.Broadcast(board, "board_changed")
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	body, err := decode[db.ReleaseParams](r)
	if err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	v, err := db.SaveRelease(r.Context(), a.Pool, board, r.PathValue("release"), body)
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(board, "board_changed")
	status := 200
	if r.Method == "POST" {
		status = 201
	}
	writeJSON(w, status, v)
}

func (a *API) handleMilestoneBurn(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	board := r.PathValue("id")
	if err := p.RequireBoard(board, "card.read"); err != nil {
		writeError(w, 403, err.Error())
		return
	}
	tag := strings.TrimSpace(r.URL.Query().Get("milestone"))
	if tag == "" || len(tag) > 200 {
		writeError(w, 400, "Choose a milestone tag.")
		return
	}
	chart, err := db.ReleaseBurn(r.Context(), a.Pool, board, "tag:"+tag)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, 200, chart)
}
