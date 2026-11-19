package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

func (a *API) handleBoardCardView(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	id := r.PathValue("id")
	if err := p.RequireBoard(id, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	for key := range r.URL.Query() {
		if key != "expand" && key != "filter" && key != "limit" && key != "older_column" && key != "cursor" {
			writeError(w, http.StatusBadRequest, "supported query parameters: expand, filter, limit, older_column, cursor")
			return
		}
	}
	o := db.CardViewOptions{Expanded: r.URL.Query()["expand"], Column: r.URL.Query().Get("older_column")}
	q := r.URL.Query()
	if raw := q.Get("filter"); raw != "" {
		if len(raw) > 8192 || json.Unmarshal([]byte(raw), &o.Filter) != nil {
			writeError(w, 400, "invalid filter")
			return
		}
	}
	for key, dest := range map[string]*int{"limit": &o.Limit, "cursor": &o.Offset} {
		if raw := q.Get(key); raw != "" {
			n, e := strconv.Atoi(raw)
			if e != nil || n < 0 || (key == "limit" && (n < 1 || n > 100)) {
				writeError(w, 400, "invalid page")
				return
			}
			*dest = n
		}
	}
	view, err := db.ReadBoardCardView(r.Context(), a.Pool, id, o)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *API) handleArchivePreview(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	id := r.PathValue("id")
	if err := p.RequireAdmin(id); err != nil {
		writeError(w, 403, err.Error())
		return
	}
	body, err := decode[struct {
		Days int      `json:"auto_archive_done_days"`
		Tags []string `json:"auto_archive_keep_tags"`
	}](r)
	if err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if body.Tags == nil {
		body.Tags = []string{"to-test"}
	}
	n, err := db.PreviewAutoArchive(r.Context(), a.Pool, id, body.Days, body.Tags)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, 200, map[string]int{"count": n})
}
