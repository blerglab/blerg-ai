package blackboard

import (
	"context"
	"slices"
)

// A release may be represented only by a tag. Offer matching cards as
// unplanned; observing a tag never approves additional scope.
func (s Service) taggedUnplanned(ctx context.Context, r Record, m Milestone, v *SliceView, columns map[string][]Column) error {
	if tag, _ := ReleaseTag([]string{m.Name}); tag == "" {
		return nil
	}
	boards := map[string]bool{}
	if m.BoardID != "" {
		boards[m.BoardID] = true
	}
	for _, link := range r.Links {
		if link.BoardID != "" {
			boards[link.BoardID] = true
		}
	}
	seen := map[cardKey]bool{}
	for _, c := range v.Cards {
		seen[cardKey{c.Card.BoardID, c.Card.Number}] = true
		if c.Card.BoardID != "" {
			boards[c.Card.BoardID] = true
		}
	}
	for board := range boards {
		linked := m
		linked.BoardID = board
		cols, ok := columns[board]
		if !ok {
			if err := s.boardCall(ctx, linked, "blerg_column_list", map[string]any{}, &cols); err != nil {
				return err
			}
			columns[board] = cols
		}
		cursor := ""
		cursors := map[string]bool{}
		for page := 0; page < 1000; page++ {
			var result CardPage
			if err := s.boardCall(ctx, linked, "blerg_card_search", map[string]any{"tag": m.Name, "page": true, "summary": true, "limit": 100, "cursor": cursor}, &result); err != nil {
				return err
			}
			if len(result.Cards) > 100 {
				return ErrInvalid
			}
			for _, c := range result.Cards {
				if c.BoardID != board || c.Number <= 0 {
					return ErrInvalid
				}
				// Also protects compatibility with older adapters that ignore tag filters.
				if !slices.Contains(c.Tags, m.Name) || seen[cardKey{board, c.Number}] {
					continue
				}
				seen[cardKey{board, c.Number}] = true
				v.Cards = append(v.Cards, SliceCard{Card: c, Membership: Item{MilestoneID: m.ID, BoardID: board, CardNumber: c.Number, State: "unplanned"}, Stage: r.Mapping.Stage(c, cols), NeedsReason: true})
			}
			if result.NextCursor == "" {
				break
			}
			if cursors[result.NextCursor] || page == 999 {
				return ErrInvalid
			}
			cursors[result.NextCursor] = true
			cursor = result.NextCursor
		}
	}
	return nil
}
