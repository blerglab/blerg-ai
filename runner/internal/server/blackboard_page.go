package server

import (
	"net/http"
	"strconv"
)

func blackboardPage(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	offset, limit := 0, 50
	var err error
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 1000000 {
			writeError(w, 400, "Invalid blackboard cursor.")
			return 0, 0, false
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, 400, "Choose 1–100 rows.")
			return 0, 0, false
		}
	}
	return offset, limit, true
}
func blackboardRows[T any](rows []T, offset, limit int) []T {
	if offset >= len(rows) {
		return []T{}
	}
	return rows[offset:min(len(rows), offset+limit)]
}
func blackboardCursor(offset, limit, total int) string {
	if offset+limit < total {
		return strconv.Itoa(offset + limit)
	}
	return ""
}
