package checklist

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxID           = 96
	MaxAssignees    = 32
	MaxAssignments  = 128
	MaxPayloadBytes = 128 << 10
)

var itemIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,96}$`)
var legacySeedRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,48}$`)

// Assignee identifies a recorded worker in this session, never a display name.
// The server checks ownership and canonicalizes native aliases before Apply.
type Assignee struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func ValidAssignee(a Assignee) bool {
	if (a.Kind != "subagent" && a.Kind != "child_session") || a.ID == "" || len(a.ID) > 256 || !utf8.ValidString(a.ID) || strings.TrimSpace(a.ID) != a.ID {
		return false
	}
	return !strings.ContainsFunc(a.ID, unicode.IsControl)
}
func newItemID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("checklist identity entropy unavailable")
	}
	return "item-" + hex.EncodeToString(b[:])
}
func legacyID(seed string, i int) string {
	if !legacySeedRe.MatchString(seed) {
		sum := sha256.Sum256([]byte(seed))
		seed = "h" + hex.EncodeToString(sum[:])
	}
	return "legacy-" + seed + "-" + strconv.Itoa(i+1)
}

// Normalize gives each legacy item a deterministic identity scoped to the
// source event. Once written, the full event retains those IDs through edits.
func Normalize(items []Item, seed string) []Item {
	out := append([]Item{}, items...)
	seen := map[string]bool{}
	for i := range out {
		id := out[i].ID
		if !itemIDRe.MatchString(id) || seen[id] {
			id = legacyID(seed, i)
			for n := 0; seen[id]; n++ {
				id = legacyID(fmt.Sprintf("%s:%d:%d", seed, i, n), i)
			}
		}
		out[i].ID = id
		seen[id] = true
		out[i].Commits = append([]string(nil), out[i].Commits...)
		out[i].Assignees = append([]Assignee(nil), out[i].Assignees...)
	}
	return out
}
func changedItem(index int, item Item) *Change {
	return &Change{ID: item.ID, Index: index + 1, Text: item.Text, Done: item.Done, InProgress: item.InProgress, Commits: item.Commits, Diff: item.Diff, Assignees: append([]Assignee(nil), item.Assignees...)}
}
func assignmentKey(a Assignee) string { return a.Kind + "\x00" + a.ID }
func applyWorkmap(next []Item, req Request) (Payload, error) {
	i, err := findAny(next, req)
	if err != nil {
		return Payload{}, err
	}
	changed := (*Change)(nil)
	switch req.Op {
	case "doing":
		next[i].Done = false
		next[i].InProgress = true
	case "pending":
		next[i].Done = false
		next[i].InProgress = false
	case "assign", "unassign":
		if req.Assignee == nil {
			return Payload{}, refuse("name the worker to %s", req.Op)
		}
	case "rename":
		text := clean(req.Item)
		if text == "" {
			return Payload{}, refuse("give the item's new text")
		}
		next[i].Text = text
	case "move":
		if req.Position < 1 || req.Position > len(next) {
			return Payload{}, refuse("position must name a place in this checklist")
		}
		item := next[i]
		next = append(next[:i], next[i+1:]...)
		j := req.Position - 1
		next = append(next, Item{})
		copy(next[j+1:], next[j:])
		next[j] = item
		i = j
	default:
		return Payload{}, refuse("unknown checklist change")
	}
	if req.Assignee != nil {
		if req.Op != "doing" && req.Op != "assign" && req.Op != "unassign" {
			return Payload{}, refuse("worker assignments go with doing, assign and unassign")
		}
		if !ValidAssignee(*req.Assignee) {
			return Payload{}, refuse("worker must name a subagent or child_session identifier")
		}
		key := assignmentKey(*req.Assignee)
		for j := range next {
			if req.Op == "unassign" && j != i {
				continue
			}
			kept := next[j].Assignees[:0]
			for _, a := range next[j].Assignees {
				if assignmentKey(a) != key {
					kept = append(kept, a)
				} else if j != i && changed == nil {
					changed = &Change{FromItemID: next[j].ID}
				}
			}
			next[j].Assignees = kept
		}
		if req.Op != "unassign" {
			if len(next[i].Assignees) >= MaxAssignees {
				return Payload{}, refuse("an item holds at most %d worker assignments", MaxAssignees)
			}
			next[i].Assignees = append(next[i].Assignees, *req.Assignee)
		}
	}
	change := changedItem(i, next[i])
	if changed != nil {
		change.FromItemID = changed.FromItemID
	}
	return Payload{Items: next, Op: req.Op, Changed: change}, nil
}
func boundedPayload(p Payload) (Payload, error) {
	assignments := 0
	for _, item := range p.Items {
		assignments += len(item.Assignees)
		if len(item.Assignees) > MaxAssignees {
			return Payload{}, refuse("an item holds too many worker assignments")
		}
	}
	if assignments > MaxAssignments {
		return Payload{}, refuse("a checklist holds at most %d worker assignments", MaxAssignments)
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > MaxPayloadBytes {
		return Payload{}, refuse("the checklist exceeds its saved size limit")
	}
	return p, nil
}
