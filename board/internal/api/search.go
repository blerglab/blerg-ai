package api

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

var supportedCardSearchParams = []string{
	"archived", "column", "cursor", "field.{key}", "include_archived", "limit", "page", "priority", "q", "tag", "type",
}

// Both card endpoints use the same filters; columns are repeatable (OR).
func parseCardSearchParams(boardID string, q url.Values) (db.SearchParams, error) {
	var unknown, repeated []string
	for key, values := range q {
		if key != "column" && len(values) > 1 {
			repeated = append(repeated, key)
		}
		if _, field := cutPrefix(key, "field."); !field && !slices.Contains(supportedCardSearchParams, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return db.SearchParams{}, fmt.Errorf("unknown query parameters: %s", strings.Join(unknown, ", "))
	}
	if len(repeated) > 0 {
		slices.Sort(repeated)
		return db.SearchParams{}, fmt.Errorf("query parameters must appear only once: %s", strings.Join(repeated, ", "))
	}
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return db.SearchParams{}, fmt.Errorf("limit must be a positive whole number")
		}
		limit = n
	}
	params := db.SearchParams{
		BoardID: boardID, Query: q.Get("q"), Type: q.Get("type"),
		Tag: q.Get("tag"), Priority: q.Get("priority"), Columns: q["column"],
		IncludeArchived: q.Get("include_archived") == "1", ArchivedOnly: q.Get("archived") == "1", Limit: limit,
		Fields: map[string]string{},
	}
	for key, vals := range q {
		if k, ok := cutPrefix(key, "field."); ok && len(vals) > 0 {
			params.Fields[k] = vals[0]
		}
	}
	return params, nil
}
