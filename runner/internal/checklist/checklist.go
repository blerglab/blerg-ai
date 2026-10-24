// Package checklist is a session's checklist: a short list of the work the agent will do, which it
// ticks off as it finishes (`blerg-runner checklist`, POST /api/sessions/{id}/checklist). It is
// pure: the server reads the newest checklist event, hands its items and the request to Apply, and
// records the payload Apply returns as the next event. Every event carries the FULL list after the
// change and the change itself, so the transcript replays without state and the chat never diffs.
package checklist

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/topics"
)

const (
	// MaxItems is how many items a checklist holds; MaxText an item's length in runes.
	MaxItems = 50
	MaxText  = 200
	// MaxEvents is how many checklist events one session may hold.
	MaxEvents = 500
	// MaxCommits is how many commits one item holds.
	MaxCommits = 20
)

var (
	shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	// diffRe is an artifact id (32 lower-case hex characters): the diff the CLI published.
	diffRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Item is one line of the list.
type Item struct {
	ID         string     `json:"id"`
	InProgress bool       `json:"in_progress,omitempty"`
	Assignees  []Assignee `json:"assignees,omitempty"`
	Text       string     `json:"text"`
	Done       bool       `json:"done"`
	// Commits are the commits the agent named when it ticked the item (7 to 40 hex characters).
	Commits []string `json:"commits,omitempty"`
	// Diff is the id of the published diff of those commits (the session's artifact), when the
	// CLI could publish one; the panel's "View diff" opens it.
	Diff string `json:"diff,omitempty"`
}

// Change records the stable identity and full state of the touched item, including
// a retired item on removal. Index remains 1-based for legacy clients.
type Change struct {
	ID         string     `json:"id"`
	InProgress bool       `json:"in_progress,omitempty"`
	Assignees  []Assignee `json:"assignees,omitempty"`
	FromItemID string     `json:"from_item_id,omitempty"`
	Index      int        `json:"index"`
	Text       string     `json:"text"`
	Done       bool       `json:"done"`
	// Commits are all the commits the item holds after the change.
	Commits []string `json:"commits,omitempty"`
	// Diff is the item's diff after the change.
	Diff string `json:"diff,omitempty"`
}

// Payload is a checklist event's payload (kind "checklist"): the list after the change, the
// operation, and the item it touched (absent for set and clear).
type Payload struct {
	Items   []Item  `json:"items"`
	Op      string  `json:"op"`
	Changed *Change `json:"changed,omitempty"`
	// Note says why Blerg made the change itself (e.g. "ticked: #127 done" when the session moved
	// card #127 to a done column); empty for the agent's own changes.
	Note string `json:"note,omitempty"`
}

// Request is a session-scoped checklist mutation. Existing-item operations select
// exactly one item by opaque ID, 1-based index, or exact case-sensitive text.
type Request struct {
	ItemID   string    `json:"item_id"`
	Position int       `json:"position"`
	Assignee *Assignee `json:"assignee"`
	Op       string    `json:"op"`
	Items    []string  `json:"items"`
	Item     string    `json:"item"`
	Index    int       `json:"index"`
	Text     string    `json:"text"`
	// Commits (add and tick) are commits to record on the item.
	Commits []string `json:"commits"`
	// Diff (with commits) is the artifact id of the diff the CLI published of those commits.
	Diff string `json:"diff"`
}

// Error is a refusal of the request, worded for the agent (the server answers 400 with it).
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func refuse(format string, args ...any) error { return &Error{msg: fmt.Sprintf(format, args...)} }

// Parse decodes a request body. A value of the wrong type is a refusal that names the field.
func Parse(body []byte) (Request, error) {
	var r Request
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&r); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			switch te.Field {
			case "items":
				return r, refuse("items must be a list of strings")
			case "index":
				return r, refuse("index must be a whole number (1 is the first item)")
			default:
				return r, refuse("%s must be a %s", te.Field, kindName(te.Field))
			}
		}
		return r, refuse("The request body is not valid JSON.")
	}
	return r, nil
}

func kindName(field string) string {
	if field == "assignee" {
		return "object"
	}
	if field == "position" {
		return "number"
	}
	if field == "index" {
		return "number"
	}
	return "string"
}

func clean(s string) string { return topics.CleanText(s, MaxText) }

// Apply returns the payload of the event that applies req to the current list cur (which it does
// not change), or a refusal.
func Apply(cur []Item, req Request) (Payload, error) {
	if req.Assignee != nil && !ValidAssignee(*req.Assignee) {
		return Payload{}, refuse("worker must name a subagent or child_session identifier")
	}
	p, err := apply(cur, req)
	if err != nil {
		return Payload{}, err
	}
	return boundedPayload(p)
}
func apply(cur []Item, req Request) (Payload, error) {
	next := Normalize(cur, "unseeded")
	if (req.Op == "set" || req.Op == "add" || req.Op == "clear") && (req.ItemID != "" || req.Index != 0 || req.Text != "") {
		return Payload{}, refuse("set, add and clear do not select an existing item")
	}
	if req.Assignee != nil && req.Op != "doing" && req.Op != "assign" && req.Op != "unassign" {
		return Payload{}, refuse("worker assignments go with doing, assign and unassign")
	}

	if req.Diff != "" {
		if len(req.Commits) == 0 || (req.Op != "add" && req.Op != "tick") {
			return Payload{}, refuse("a diff goes with the commits of add and tick")
		}
		if !diffRe.MatchString(req.Diff) {
			return Payload{}, refuse("diff must be the id of a file this session published")
		}
	}
	switch req.Op {
	case "set":
		if len(req.Items) > MaxItems {
			return Payload{}, refuse("a checklist holds at most %d items, got %d", MaxItems, len(req.Items))
		}
		out := make([]Item, 0, len(req.Items))
		for _, s := range req.Items {
			if t := clean(s); t != "" {
				out = append(out, Item{ID: newItemID(), Text: t})
			}
		}
		if len(out) == 0 {
			return Payload{}, refuse("give the items of the checklist (to empty it, clear it)")
		}
		return Payload{Items: out, Op: "set"}, nil
	case "add":
		cs, err := cleanCommits(nil, req.Commits)
		if err != nil {
			return Payload{}, err
		}
		t := clean(req.Item)
		if t == "" {
			return Payload{}, refuse("give the text of the item to add")
		}
		if len(next) >= MaxItems {
			return Payload{}, refuse("the checklist already holds %d items, its most", MaxItems)
		}
		next = append(next, Item{ID: newItemID(), Text: t, Commits: cs, Diff: req.Diff})
		return Payload{Items: next, Op: "add", Changed: changedItem(len(next)-1, next[len(next)-1])}, nil
	case "tick", "untick":
		done := req.Op == "tick"
		i, err := find(next, req, done)
		if err != nil {
			return Payload{}, err
		}
		next[i].Done = done
		if done {
			next[i].InProgress = false
		}
		if len(req.Commits) > 0 {
			if !done {
				return Payload{}, refuse("commits go with add and tick, not untick")
			}
			if next[i].Commits, err = cleanCommits(next[i].Commits, req.Commits); err != nil {
				return Payload{}, err
			}
			if req.Diff != "" {
				next[i].Diff = req.Diff
			}
		}
		return Payload{Items: next, Op: req.Op, Changed: changedItem(i, next[i])}, nil
	case "remove":
		if len(req.Commits) > 0 || req.Diff != "" {
			return Payload{}, refuse("commits go with add and tick, not remove")
		}
		i, err := findAny(next, req)
		if err != nil {
			return Payload{}, err
		}
		gone := next[i]
		next = append(next[:i], next[i+1:]...)
		return Payload{Items: next, Op: "remove", Changed: changedItem(i, gone)}, nil
	case "doing", "pending", "assign", "unassign", "rename", "move":
		if len(req.Commits) > 0 || req.Diff != "" {
			return Payload{}, refuse("commits go with add and tick")
		}
		return applyWorkmap(next, req)
	case "clear":
		return Payload{Items: []Item{}, Op: "clear"}, nil
	default:
		return Payload{}, refuse(`op must be "set", "add", "tick", "untick", "remove" or "clear"`)
	}
}

// cleanCommits is have followed by the new shas: each 7 to 40 hex characters (lower-cased),
// duplicates dropped, at most MaxCommits on one item.
func cleanCommits(have, given []string) ([]string, error) {
	if len(given) == 0 {
		return have, nil
	}
	in := make([]agent.TopicCommit, len(given))
	for i, s := range given {
		in[i] = agent.TopicCommit{Sha: s}
	}
	if len(in) > topics.MaxCommits {
		return nil, refuse("at most %d commits on one item, got %d", MaxCommits, len(in))
	}
	clean, err := topics.CleanCommits(in)
	if err != nil {
		return nil, refuse("%s", err.Error())
	}
	out := append([]string{}, have...)
	seen := map[string]bool{}
	for _, s := range have {
		seen[s] = true
	}
	for _, c := range clean {
		if !seen[c.Sha] {
			seen[c.Sha] = true
			out = append(out, c.Sha)
		}
	}
	if len(out) > MaxCommits {
		return nil, refuse("an item holds at most %d commits", MaxCommits)
	}
	return out, nil
}

// find is the 0-based index of the item req names: by index, or the first of the items whose text
// is req.Text exactly that is not yet in the wanted state (the first match when all are).
func find(list []Item, req Request, want bool) (int, error) {
	if len(list) == 0 {
		return 0, refuse("the checklist is empty; set it first")
	}
	switch {
	case req.ItemID != "":
		if req.Index != 0 || req.Text != "" {
			return 0, refuse("name the item by its id, number or text, not several")
		}
		if !itemIDRe.MatchString(req.ItemID) {
			return 0, refuse("item_id must be a saved checklist item identifier")
		}
		for i, it := range list {
			if it.ID == req.ItemID {
				return i, nil
			}
		}
		return 0, refuse("that checklist item is no longer available")
	case req.Index != 0 && req.Text != "":
		return 0, refuse("name the item by its number or by its text, not both")
	case req.Index != 0:
		if req.Index < 1 || req.Index > len(list) {
			return 0, refuse("there is no item %d; the checklist has %d", req.Index, len(list))
		}
		return req.Index - 1, nil
	case req.Text != "":
		first := -1
		for i, it := range list {
			if it.Text != req.Text {
				continue
			}
			if it.Done != want {
				return i, nil
			}
			if first < 0 {
				first = i
			}
		}
		if first < 0 {
			return 0, refuse("no item reads exactly %q", topics.Cut(req.Text, 80))
		}
		return first, nil
	default:
		return 0, refuse("name the item by its number (1 is the first) or by its exact text")
	}
}

// findAny is the 0-based index of the item req names, by its number or by its exact text (the
// first of several matches), whatever state it is in.
func findAny(list []Item, req Request) (int, error) {
	if req.Text != "" && req.Index == 0 && req.ItemID == "" {
		for i, it := range list {
			if it.Text == req.Text {
				return i, nil
			}
		}
	}
	return find(list, req, true)
}

// Done counts the items ticked.
func Done(items []Item) int {
	n := 0
	for _, it := range items {
		if it.Done {
			n++
		}
	}
	return n
}

// Read decodes a stored checklist payload; ok=false when it is not one.
func Read(payload []byte) (Payload, bool) { return ReadWithSeed(payload, string(payload)) }
func ReadWithSeed(payload []byte, seed string) (Payload, bool) {
	var p Payload
	if len(payload) == 0 || len(payload) > MaxPayloadBytes || json.Unmarshal(payload, &p) != nil || len(p.Items) > MaxItems {
		return Payload{}, false
	}
	if p.Items == nil {
		p.Items = []Item{}
	}
	p.Items = Normalize(p.Items, seed)
	assigned := map[string]bool{}
	assignments := 0
	for i := range p.Items {
		if p.Items[i].Done {
			p.Items[i].InProgress = false
		}
		kept := []Assignee{}
		for _, a := range p.Items[i].Assignees {
			key := assignmentKey(a)
			if ValidAssignee(a) && !assigned[key] && len(kept) < MaxAssignees && assignments < MaxAssignments {
				kept = append(kept, a)
				assigned[key] = true
				assignments++
			}
		}
		p.Items[i].Assignees = kept
	}
	// An event is input: keep only well-formed, distinct shas, at most MaxCommits per item.
	for i := range p.Items {
		p.Items[i].Commits = readCommits(p.Items[i].Commits)
		p.Items[i].Diff = readDiff(p.Items[i].Diff)
	}
	if p.Changed != nil {
		p.Changed.Commits = readCommits(p.Changed.Commits)
		p.Changed.Diff = readDiff(p.Changed.Diff)
	}
	return p, true
}

func readCommits(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.ToLower(s)
		if !shaRe.MatchString(s) || seen[s] || len(out) == MaxCommits {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func readDiff(id string) string {
	if diffRe.MatchString(id) {
		return id
	}
	return ""
}
