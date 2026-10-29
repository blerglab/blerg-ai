package db

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
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
	Columns         []string          // OR of exact case-insensitive column names or UUIDs on BoardID
	ArchivedOnly    bool              // legacy REST archived=1; takes precedence over IncludeArchived
	Fields          map[string]string // equality on declared fields keys
	IncludeArchived bool
	Limit           int
	NoDefaultLimit  bool // unpaged card lists omit default/maximum caps; positive Limit still applies
	offset          int
	// AnyWord makes the text match OR-based (recall-oriented) — used by the
	// admission gate's candidate retrieval, where a differently-worded
	// duplicate must still surface. Default (AND) is right for user search.
	AnyWord bool
}

// SearchCards implements the spec's search: websearch tsquery over
// title+body unioned with a trigram match on title, ranked by ts_rank with
// recency tiebreak; structured filters ANDed on top.
func SearchCards(ctx context.Context, pool *pgxpool.Pool, p SearchParams) ([]Card, error) {
	if !p.NoDefaultLimit {
		p.Limit = searchLimit(p.Limit)
	}
	return searchCards(ctx, pool, p, p.Limit)
}

// SearchPage is opt-in so existing REST and MCP search clients retain array replies.
type SearchPage struct {
	Cards      []Card `json:"cards"`
	NextCursor string `json:"next_cursor"`
}

func searchLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// SearchCardsPage returns an opaque offset cursor in a deterministic search order.
// Like ordinary search, pages reflect current data rather than a frozen snapshot.
func SearchCardsPage(ctx context.Context, pool *pgxpool.Pool, p SearchParams, cursor string) (SearchPage, error) {
	p.Limit = searchLimit(p.Limit)
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(raw) > 10 {
			return SearchPage{}, &FieldError{Key: "cursor", Msg: "invalid search cursor"}
		}
		n, err := strconv.Atoi(string(raw))
		if err != nil || n <= 0 || n > 2147483647 {
			return SearchPage{}, &FieldError{Key: "cursor", Msg: "invalid search cursor"}
		}
		p.offset = n
	}
	cards, err := searchCards(ctx, pool, p, p.Limit+1)
	if err != nil {
		return SearchPage{}, err
	}
	page := SearchPage{Cards: cards}
	if len(cards) > p.Limit {
		page.Cards = cards[:p.Limit]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(p.offset + p.Limit)))
	}
	if page.Cards == nil {
		page.Cards = []Card{}
	}
	return page, nil
}

var searchNumber = regexp.MustCompile(`^(?:([A-Z][A-Z0-9]{1,4})-|#)?([1-9][0-9]{0,6})$`)

func searchCards(ctx context.Context, pool *pgxpool.Pool, p SearchParams, limit int) ([]Card, error) {
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
	if p.ArchivedOnly {
		where = append(where, "c.archived_at IS NOT NULL")
	} else if !p.IncludeArchived {
		where = append(where, "c.archived_at IS NULL")
	}
	if len(p.Columns) > 0 {
		ids, err := searchColumnIDs(ctx, pool, p.BoardID, p.Columns)
		if err != nil {
			return nil, err
		}
		add("c.column_id = ANY($%d::uuid[])", ids)
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
	if ref := searchNumber.FindStringSubmatch(strings.TrimSpace(p.Query)); !p.AnyWord && ref != nil {
		number, _ := strconv.Atoi(ref[2])
		add("c.number = $%d", number)
		if ref[1] != "" {
			add("EXISTS (SELECT 1 FROM board_keys k WHERE k.board_id = c.board_id AND k.key = $%d)", ref[1])
		}
	} else if p.Query != "" {
		add("(c.search_vector @@ websearch_to_tsquery('english', $%d)", p.Query)
		args = append(args, p.Query)
		where[len(where)-1] += fmt.Sprintf(" OR c.title %% $%d)", len(args))
		args = append(args, p.Query)
		order = fmt.Sprintf(
			"ts_rank(c.search_vector, websearch_to_tsquery('english', $%d)) DESC, c.updated_at DESC",
			len(args))
	}

	var rowLimit any // SQL NULL means no limit for unpaged lists.
	if limit > 0 {
		rowLimit = limit
	}
	args = append(args, rowLimit, p.offset)
	sql := `SELECT ` + prefixCols("c") + ` FROM cards c WHERE ` +
		joinAnd(where) + ` ORDER BY ` + order + fmt.Sprintf(`, c.id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))
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

// UnknownSearchColumnError reports a column outside the selected board.
type UnknownSearchColumnError struct {
	Column    string
	Available []string
}

func (e *UnknownSearchColumnError) Error() string {
	return fmt.Sprintf("unknown column %q; board columns: %s", e.Column, strings.Join(e.Available, ", "))
}

func searchColumnIDs(ctx context.Context, pool *pgxpool.Pool, boardID string, names []string) ([]string, error) {
	columns, err := ListColumns(ctx, pool, boardID)
	if err != nil {
		return nil, err
	}
	available := make([]string, 0, len(columns))
	for _, col := range columns {
		available = append(available, col.Name)
	}
	ids := make([]string, 0, len(names))
	for _, name := range names {
		found := false
		for _, col := range columns {
			if strings.EqualFold(name, col.ID) || strings.EqualFold(name, col.Name) {
				ids = append(ids, col.ID)
				found = true
			}
		}
		if !found {
			return nil, &UnknownSearchColumnError{Column: name, Available: available}
		}
	}
	return ids, nil
}
