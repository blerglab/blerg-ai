package blackboard

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

func (s Service) validateMilestoneChange(ctx context.Context, r Record, c Change) error {
	m := c.Milestone
	if m == nil {
		return fmt.Errorf("milestone: required for milestone_add: %w", ErrInvalid)
	}
	if c.Item != (Item{}) || c.Thrust != nil {
		return fmt.Errorf("milestone: cannot combine with item or thrust: %w", ErrInvalid)
	}
	if strings.TrimSpace(m.Name) == "" || utf8.RuneCountInString(m.Name) > 60 || regexp.MustCompile(`#[0-9]+`).MatchString(m.Name) {
		return fmt.Errorf("milestone.name: required, at most 60 characters without card references: %w", ErrInvalid)
	}
	if len(m.Description) > 1000 {
		return fmt.Errorf("milestone.description: at most 1000 bytes: %w", ErrInvalid)
	}
	if err := planLengths(planField{"milestone.board_id", m.BoardID, 200}, planField{"milestone.release_id", m.ReleaseID, 200}); err != nil {
		return err
	}
	if m.Target != "" {
		if _, err := time.Parse("2006-01-02", m.Target); err != nil {
			return fmt.Errorf("milestone.target: use YYYY-MM-DD: %w", ErrInvalid)
		}
	}
	for _, old := range r.Milestones {
		if old.Name == m.Name || old.ID == m.ID && m.ID != "" {
			return fmt.Errorf("milestone.name: must be unique: %w", ErrInvalid)
		}
	}
	if m.BoardID == "" {
		if m.ReleaseID != "" {
			return fmt.Errorf("milestone.board_id: required with release_id: %w", ErrInvalid)
		}
		return nil
	}
	if !boardLinked(r, "", m.BoardID) {
		return fmt.Errorf("milestone.board_id: board must be linked: %w", ErrInvalid)
	}
	releases, err := s.Releases(ctx, m.BoardID)
	if err != nil {
		return err
	}
	if len(releases) == 0 && m.ReleaseID == "" {
		return nil
	}
	for _, release := range releases {
		if release.Name == m.Name && (m.ReleaseID == "" || m.ReleaseID == release.ID) {
			return nil
		}
	}
	return fmt.Errorf("milestone.name: use the board release's exact name; put the theme in description: %w", ErrInvalid)
}

func (s Service) prepareMilestoneChange(ctx context.Context, r Record, c *Change) error {
	if c.Milestone != nil && c.Milestone.ID != "" {
		return fmt.Errorf("milestone.id: assigned by server: %w", ErrInvalid)
	}
	// Infer only an unambiguous workspace board; otherwise require an explicit selection.
	if c.Milestone != nil && c.Milestone.BoardID == "" {
		boards := ReferencedBoards(r)
		if len(boards) == 1 {
			c.Milestone.BoardID = boards[0]
		} else if len(boards) > 1 {
			return fmt.Errorf("milestone.board_id: select a linked board: %w", ErrInvalid)
		}
	}
	if err := s.validateMilestoneChange(ctx, r, *c); err != nil {
		return err
	}
	c.Milestone.ID = NewID()
	return nil
}

func (s Service) applyMilestoneChange(ctx context.Context, r *Record, c Change) error {
	if err := s.validateMilestoneChange(ctx, *r, c); err != nil {
		return err
	}
	m := c.Milestone
	if m.ID == "" {
		return fmt.Errorf("milestone.id: missing server identity: %w", ErrInvalid)
	}
	var target *time.Time
	if m.Target != "" {
		v, _ := time.Parse("2006-01-02", m.Target)
		target = &v
	}
	r.Milestones = append(r.Milestones, Milestone{ID: m.ID, Owner: r.Owner, Name: m.Name, BoardID: m.BoardID, ReleaseID: m.ReleaseID, Target: target, Description: m.Description, Version: 1})
	return nil
}
