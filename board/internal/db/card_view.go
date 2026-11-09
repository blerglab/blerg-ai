package db

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"
)

type BoardFilter struct {
	Release  string   `json:"release"`
	Q        string   `json:"q"`
	Tag      []string `json:"tag"`
	Type     []string `json:"type"`
	Priority []string `json:"priority"`
}
type ViewChip struct {
	Facet string `json:"facet"`
	Value string `json:"value"`
	Count int    `json:"count"`
	On    bool   `json:"on"`
}
type BoardCardView struct {
	Cards   []Card            `json:"cards"`
	Older   map[string]int    `json:"older"`
	Totals  map[string]int    `json:"totals"`
	Matched map[string]int    `json:"matched"`
	Loaded  map[string]int    `json:"loaded"`
	Next    map[string]string `json:"next"`
	Chips   []ViewChip        `json:"chips"`
}
type CardViewOptions struct {
	Filter        BoardFilter
	Expanded      []string
	Column        string
	Offset, Limit int
}

func foldView(s string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.Is(unicode.M, r) {
			return -1
		}
		return r
	}, norm.NFD.String(s)))
}
func (f BoardFilter) active() bool {
	return f.Release != "" || strings.TrimSpace(f.Q) != "" || len(f.Tag)+len(f.Type)+len(f.Priority) > 0
}
func viewValues(c Card, facet string) []string {
	switch facet {
	case "tag":
		return c.Tags
	case "type":
		return []string{c.Type}
	default:
		return []string{c.Priority}
	}
}
func (f BoardFilter) selected(facet string) []string {
	switch facet {
	case "tag":
		return f.Tag
	case "type":
		return f.Type
	default:
		return f.Priority
	}
}
func (f BoardFilter) matches(c Card, skip string) bool {
	if f.Release != "" && ((f.Release == "none" && c.ReleaseID != nil) || (f.Release != "none" && deref(c.ReleaseID) != f.Release)) {
		return false
	}
	for _, w := range strings.Fields(foldView(f.Q)) {
		rawNumber := strings.TrimPrefix(w, "#")
		numeric := rawNumber != "" && strings.IndexFunc(rawNumber, func(r rune) bool { return r < '0' || r > '9' }) < 0
		n, e := strconv.Atoi(rawNumber)
		hit := numeric && e == nil && n == c.Number
		if !hit {
			if strings.HasPrefix(w, "#") {
				if !numeric && len(w) > 1 {
					for _, t := range c.Tags {
						hit = hit || strings.HasPrefix(foldView(t), w[1:])
					}
				}
			} else {
				hit = strings.Contains(foldView(c.Title), w)
				for _, t := range c.Tags {
					hit = hit || strings.Contains(foldView(t), w)
				}
			}
		}
		if !hit {
			return false
		}
	}
	for _, facet := range []string{"type", "priority", "tag"} {
		sel := f.selected(facet)
		if facet == skip || len(sel) == 0 {
			continue
		}
		hit := false
		for _, v := range viewValues(c, facet) {
			hit = hit || slices.Contains(sel, v)
		}
		if !hit {
			return false
		}
	}
	return true
}
func RecentBoardCards(ctx context.Context, pool *pgxpool.Pool, id string, expanded []string) (BoardCardView, error) {
	return ReadBoardCardView(ctx, pool, id, CardViewOptions{Expanded: expanded})
}

// ReadBoardCardView scans only filter metadata for old cards. Bodies and extras
// are loaded only for recent matches and the requested page of older matches.
func ReadBoardCardView(ctx context.Context, pool *pgxpool.Pool, id string, o CardViewOptions) (BoardCardView, error) {
	out := BoardCardView{Cards: []Card{}, Older: map[string]int{}, Totals: map[string]int{}, Matched: map[string]int{}, Loaded: map[string]int{}, Next: map[string]string{}, Chips: []ViewChip{}}
	if o.Limit == 0 {
		o.Limit = 50
	}
	if o.Limit < 1 || o.Limit > 100 || o.Offset < 0 {
		return out, fmt.Errorf("invalid card page")
	}
	rows, err := pool.Query(ctx, `SELECT c.release_id,c.id,c.column_id,c.title,c.number,c.type,c.priority,ARRAY(SELECT tag FROM card_tags WHERE card_id=c.id),coalesce(col.is_terminal AND c.column_entered_at<now()-interval '14 days',false) FROM cards c LEFT JOIN board_columns col ON col.id=c.column_id WHERE c.board_id=$1 AND c.archived_at IS NULL ORDER BY col.rank NULLS LAST,c.rank,c.id`, id)
	if err != nil {
		return out, err
	}
	var all []Card
	old := map[string]bool{}
	for rows.Next() {
		var c Card
		var aged bool
		if err = rows.Scan(&c.ReleaseID, &c.ID, &c.ColumnID, &c.Title, &c.Number, &c.Type, &c.Priority, &c.Tags, &aged); err != nil {
			rows.Close()
			return out, err
		}
		all = append(all, c)
		old[c.ID] = aged
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	ids := []string{}
	seenOld := map[string]int{}
	for _, c := range all {
		col := ""
		if c.ColumnID != nil {
			col = *c.ColumnID
		}
		out.Totals[col]++
		if !o.Filter.matches(c, "") {
			continue
		}
		out.Matched[col]++
		if old[c.ID] {
			out.Older[col]++
			idx := seenOld[col]
			seenOld[col]++
			if o.Column != "" && col != o.Column {
				continue
			}
			if o.Column == "" && !o.Filter.active() && !slices.Contains(o.Expanded, col) {
				continue
			}
			offset := 0
			if o.Column != "" {
				offset = o.Offset
			}
			if idx < offset || idx >= offset+o.Limit {
				continue
			}
			out.Loaded[col]++
		} else if o.Column != "" {
			continue
		}
		ids = append(ids, c.ID)
	}
	for col, n := range out.Older {
		offset := 0
		if o.Column == col {
			offset = o.Offset
		}
		if out.Loaded[col] > 0 && offset+out.Loaded[col] < n {
			out.Next[col] = strconv.Itoa(offset + out.Loaded[col])
		}
	}
	for _, facet := range []string{"type", "priority", "tag"} {
		counts := map[string]int{}
		for _, c := range all {
			for _, v := range viewValues(c, facet) {
				if v != "" {
					if _, ok := counts[v]; !ok {
						counts[v] = 0
					}
				}
			}
			if o.Filter.matches(c, facet) {
				for _, v := range slices.Compact(slices.Sorted(slices.Values(viewValues(c, facet)))) {
					if v != "" {
						counts[v]++
					}
				}
			}
		}
		for _, v := range o.Filter.selected(facet) {
			if _, ok := counts[v]; !ok {
				counts[v] = 0
			}
		}
		chips := []ViewChip{}
		for v, n := range counts {
			chips = append(chips, ViewChip{facet, v, n, slices.Contains(o.Filter.selected(facet), v)})
		}
		slices.SortFunc(chips, func(a, b ViewChip) int {
			if facet == "priority" {
				order := []string{"urgent", "high", "medium", "low"}
				ai, bi := slices.Index(order, a.Value), slices.Index(order, b.Value)
				if ai != bi {
					return ai - bi
				}
			}
			if facet == "tag" {
				if a.On != b.On {
					if a.On {
						return -1
					}
					return 1
				}
				if a.Count != b.Count {
					return b.Count - a.Count
				}
			}
			return strings.Compare(a.Value, b.Value)
		})
		out.Chips = append(out.Chips, chips...)
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err = pool.Query(ctx, `SELECT `+prefixCols("c")+` FROM cards c WHERE c.id::text=ANY($1::text[]) ORDER BY array_position($1::text[],c.id::text)`, ids)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		c, e := scanCard(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Cards = append(out.Cards, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, attachExtras(ctx, pool, out.Cards)
}
