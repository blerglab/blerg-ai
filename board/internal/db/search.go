package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SearchParams — one board, text + structured filters.
type SearchParams struct {
	BoardID         string
	Query           string // tsquery + trigram over title/body
	Type            string
	Tag             string
	Priority        string
	Fields          map[string]string // equality on declared fields keys
	IncludeArchived bool
	Limit           int
	// AnyWord makes the text match OR-based (recall-oriented) — used by the
	// admission gate's candidate retrieval, where a differently-worded
	// duplicate must still surface. Default (AND) is right for user search.
	AnyWord bool
}

// SearchCards implements the spec's search: websearch tsquery over
// title+body unioned with a trigram match on title, ranked by ts_rank with
// recency tiebreak; structured filters ANDed on top.
func SearchCards(ctx context.Context, pool *pgxpool.Pool, p SearchParams) ([]Card, error) {
	if p.Limit <= 0 {
		p.Limit = 20
	}
	if p.Limit > 100 {
		p.Limit = 100
	}
	where := []string{"TRUE"}
	args := []any{}
	if p.BoardID != "" {
		args = append(args, p.BoardID)
		where = append(where, "c.board_id = $1")
	}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if !p.IncludeArchived {
		where = append(where, "c.archived_at IS NULL")
	}
	if p.Type != "" {
		add("c.type = $%d", p.Type)
	}
	if p.Priority != "" {
		add("c.priority = $%d", p.Priority)
	}
	if p.Tag != "" {
		add("EXISTS (SELECT 1 FROM card_tags t WHERE t.card_id = c.id AND t.tag = $%d)", p.Tag)
	}
	for k, v := range p.Fields {
		args = append(args, k, v)
		where = append(where, fmt.Sprintf("c.fields->>$%d = $%d", len(args)-1, len(args)))
	}

	order := "c.updated_at DESC"
	if p.AnyWord {
		p.Query = orQuery(p.Query)
	}
	if p.Query != "" {
		add("(c.search_vector @@ websearch_to_tsquery('english', $%d)", p.Query)
		args = append(args, p.Query)
		where[len(where)-1] += fmt.Sprintf(" OR c.title %% $%d)", len(args))
		args = append(args, p.Query)
		order = fmt.Sprintf(
			"ts_rank(c.search_vector, websearch_to_tsquery('english', $%d)) DESC, c.updated_at DESC",
			len(args))
	}

	args = append(args, p.Limit)
	sql := `SELECT ` + prefixCols("c") + ` FROM cards c WHERE ` +
		joinAnd(where) + ` ORDER BY ` + order + fmt.Sprintf(` LIMIT $%d`, len(args))
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := attachExtras(ctx, pool, out); err != nil {
		return nil, err
	}
	return out, nil
}

// orQuery rewrites "a b c" as "a OR b OR c" for websearch_to_tsquery.
func orQuery(q string) string {
	words := strings.Fields(q)
	if len(words) > 24 {
		words = words[:24]
	}
	return strings.Join(words, " OR ")
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}
