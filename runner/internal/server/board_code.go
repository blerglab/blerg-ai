package server

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type boardCodeEvidence struct {
	ChecklistActive     bool
	ActivityUnavailable bool
	Activity            []protocol.CodeActivity
	Since               time.Time
	RecordedLanding     bool
}

type boardCodeAction struct {
	Column, Commit, Drift string
	Tag                   bool
	Undeployed            bool
}

var boardMergeTitle = regexp.MustCompile(`^Merge (BLG-[1-9][0-9]{0,6}(?:, BLG-[1-9][0-9]{0,6})*)(?:$|:)`)
var boardKeyNumber = regexp.MustCompile(`BLG-([1-9][0-9]{0,6})`)

func boardMergeNumbers(s string) []int {
	match := boardMergeTitle.FindStringSubmatch(s)
	if match == nil {
		return nil
	}
	var out []int
	for _, m := range boardKeyNumber.FindAllStringSubmatch(match[1], -1) {
		n, _ := strconv.Atoi(m[1])
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// Four hours without a live worker or any explicit BLG reference is a stale-work fact,
// never permission to move the card back. Reverts similarly only flag drift.
const boardCodeStaleAfter = 4 * time.Hour

func boardCodeDecision(c liveBoardCard, history []protocol.CodeCommit, now time.Time, running bool, evidence ...boardCodeEvidence) boardCodeAction {
	var merge *protocol.CodeCommit
	reverted := map[string]bool{}
	bySHA := make(map[string]protocol.CodeCommit, len(history))
	for _, h := range history {
		bySHA[h.SHA] = h
	}
	// Histories may contain multiple repositories. Each graph tip supplies its
	// own first-parent chain; side-branch merge titles cannot close cards.
	referenced := map[string]bool{}
	for _, h := range history {
		for _, parent := range h.Parents {
			referenced[parent] = true
		}
	}
	mainline := map[string]bool{}
	for _, h := range history {
		if referenced[h.SHA] {
			continue
		}
		for sha := h.SHA; sha != "" && !mainline[sha]; {
			mainline[sha] = true
			entry := bySHA[sha]
			if len(entry.Parents) == 0 {
				break
			}
			sha = entry.Parents[0]
		}
	}
	partial := false
	last := time.Time{}
	for i := range history {
		h := &history[i]
		if reverted[h.SHA] {
			continue
		}
		if h.Reverts != "" {
			reverted[h.Reverts] = true
			// An integration revert also removes the card merges it brought
			// onto main. Preserve merges already reachable from its first parent.
			if target, ok := bySHA[h.Reverts]; ok && len(target.Parents) > 1 {
				retained := boardCodeAncestors(bySHA, target.Parents[:1])
				for sha := range boardCodeAncestors(bySHA, target.Parents[1:]) {
					if !retained[sha] {
						reverted[sha] = true
					}
				}
			}
		}
		for _, m := range boardKeyNumber.FindAllStringSubmatch(h.Subject, -1) {
			n, _ := strconv.Atoi(m[1])
			if n == c.Number {
				if at, e := time.Parse(time.RFC3339, h.Date); e == nil && !at.After(now) && at.After(last) {
					last = at
				}
			}
		}
		if mainline[h.SHA] && len(h.Parents) > 1 && len(boardMergeNumbers(h.Subject)) == 0 && strings.HasPrefix(h.Subject, "Merge ") && slices.Contains(boardMentionNumbers(h.Subject), c.Number) {
			partial = true
		}
		if merge == nil && mainline[h.SHA] && len(h.Parents) > 1 && slices.Contains(boardMergeNumbers(h.Subject), c.Number) {
			merge = h
		}
	}
	d := boardCodeAction{Undeployed: slices.Contains(c.Tags, "to-test") && (merge == nil || !merge.Deployed)}
	if merge != nil {
		d.Commit = merge.SHA
		at, err := time.Parse(time.RFC3339, merge.Date)
		if err != nil {
			d.Drift = "invalid_merge_date"
			return d
		}
		// The trail is bounded. If it no longer reaches the merge, fail closed rather
		// than losing a person's later move. No writes to archived cards.
		if c.ArchivedAt != nil {
			return d
		}
		if len(c.Trail) >= 30 {
			first, e := time.Parse(time.RFC3339, c.Trail[0].CreatedAt)
			if e != nil || first.After(at) {
				d.Drift = "history_incomplete"
				return d
			}
		}
		d.Tag = merge.Deployed && !slices.Contains(c.Tags, "to-test")
		for _, e := range c.Trail {
			if e.Type == "moved" && (e.Actor == "human" || e.Actor == "curator") {
				t, err := time.Parse(time.RFC3339, e.CreatedAt)
				if err != nil || !t.Before(at) {
					if !cardDone(&c) {
						d.Drift = "manual_move_preserved"
					}
					return d
				}
			}
		}
		if !cardDone(&c) {
			d.Drift = "merged_not_done"
			for _, col := range c.Columns {
				if col.IsTerminal {
					if d.Column != "" {
						d.Column = ""
						d.Drift = "ambiguous_done_column"
						return d
					}
					d.Column = col.ID
				}
			}
		}
		return d
	}
	// A reverted merge must remain visible even if its card was never moved.
	for _, h := range history {
		if reverted[h.SHA] && slices.Contains(boardMergeNumbers(h.Subject), c.Number) {
			d.Drift = "merge_reverted"
			return d
		}
	}
	if partial && c.ArchivedAt == nil && !cardDone(&c) {
		d.Drift = "partial_merge_preserved"
		return d
	}
	if cardDone(&c) {
		var proof boardCodeEvidence
		if len(evidence) > 0 {
			proof = evidence[0]
		}
		if proof.RecordedLanding || proof.Since.IsZero() || c.ArchivedAt != nil || humanDoneReason(c) {
			return d
		}
		for _, h := range history {
			if len(h.Parents) > 1 && slices.Contains(h.BodyCards, c.Number) {
				return d
			}
		}
		if c.DoneMovedAt != nil {
			if at, err := time.Parse(time.RFC3339, *c.DoneMovedAt); err == nil && at.After(proof.Since) {
				d.Drift = "done_without_main_merge"
			}
		}
		// UpdatedAt includes comments and tags. Only an actual move into the
		// current terminal column proves completion after activation/cutover.
		for i := len(c.Trail) - 1; i >= 0; i-- {
			e := c.Trail[i]
			if e.Type != "moved" {
				continue
			}
			at, err := time.Parse(time.RFC3339, e.CreatedAt)
			if err == nil && e.ToColumn == c.ColumnName && at.After(proof.Since) {
				d.Drift = "done_without_main_merge"
			}
			break
		}
		return d
	}
	var proof boardCodeEvidence
	if len(evidence) > 0 {
		proof = evidence[0]
	}
	for _, activity := range proof.Activity {
		if slices.Contains(boardMentionNumbers(activity.Subject), c.Number) {
			if at, err := time.Parse(time.RFC3339, activity.Date); err == nil && !at.After(now) && at.After(last) {
				last = at
			}
		}
	}
	if columnInProgress(c.ColumnName) && !running && !proof.ChecklistActive && !proof.ActivityUnavailable && (last.IsZero() || now.Sub(last) >= boardCodeStaleAfter) {
		d.Drift = "stalled_without_worker"
	}

	return d
}

func boardCodeAncestors(history map[string]protocol.CodeCommit, roots []string) map[string]bool {
	seen := map[string]bool{}
	pending := slices.Clone(roots)
	for len(pending) > 0 {
		sha := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[sha] {
			continue
		}
		seen[sha] = true
		pending = append(pending, history[sha].Parents...)
	}
	return seen
}

func boardMentionNumbers(subject string) []int {
	var numbers []int
	for _, match := range boardKeyNumber.FindAllStringSubmatch(subject, -1) {
		n, _ := strconv.Atoi(match[1])
		numbers = append(numbers, n)
	}
	return numbers
}

func humanDoneReason(c liveBoardCard) bool {
	if c.HasHumanDoneReason != nil && *c.HasHumanDoneReason {
		return true
	}
	for _, event := range c.Trail {
		if event.Type != "comment" || event.Actor != "human" {
			continue
		}
		if reason, ok := strings.CutPrefix(strings.TrimSpace(event.Text), "Done reason:"); ok && strings.TrimSpace(reason) != "" {
			return true
		}
	}
	return false
}
