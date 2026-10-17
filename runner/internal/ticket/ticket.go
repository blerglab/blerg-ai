// Package ticket is the source-neutral ticket an agent pins into its session (`blerg-runner pin`,
// POST /api/sessions/{id}/tickets): a JIRA issue, a Bugzilla bug, a GitHub issue, a Blerg Board
// card, anything a tracker holds, mapped by the agent into one shape. It is pure: Clean validates
// and caps what the agent sent (tracker data is untrusted: every string is cut and cleaned of
// control characters, unknown fields are dropped, a wrong type is refused); Pinned folds the
// session's ticket events into what is pinned now.
package ticket

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/runner/internal/topics"
)

// The caps, in runes (counts for the lists).
const (
	MaxSource         = 32
	MaxKey            = 64
	MaxTitle          = 300
	MaxChip           = 64 // status, type, priority
	MaxPerson         = 120
	MaxLabels         = 20
	MaxLabel          = 64
	MaxDescription    = 20000
	MaxFields         = 30
	MaxFieldName      = 80
	MaxFieldValue     = 500
	MaxComments       = 5
	MaxAt             = 40
	MaxCommentBody    = 2000
	MaxAcceptance     = 30
	MaxAcceptanceText = 500
	MaxURL            = 2000

	// MaxPinned is how many tickets a session may have pinned at once; MaxEvents how many ticket
	// events it may hold.
	MaxPinned = 5
	MaxEvents = 100
)

// Field is one extra field of the tracker's (a sprint, a component, a version).
type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Comment is one of the ticket's latest comments.
type Comment struct {
	Author string `json:"author,omitempty"`
	At     string `json:"at,omitempty"`
	Body   string `json:"body"`
}

// Ticket is the pinned ticket. Description and comment bodies are markdown, drawn as text.
type Ticket struct {
	Source      string    `json:"source"`
	Key         string    `json:"key"`
	URL         string    `json:"url,omitempty"`
	Title       string    `json:"title"`
	Status      string    `json:"status,omitempty"`
	Type        string    `json:"type,omitempty"`
	Priority    string    `json:"priority,omitempty"`
	Assignee    string    `json:"assignee,omitempty"`
	Reporter    string    `json:"reporter,omitempty"`
	Labels      []string  `json:"labels,omitempty"`
	Description string    `json:"description,omitempty"`
	Fields      []Field   `json:"fields,omitempty"`
	Comments    []Comment `json:"comments,omitempty"`
	Acceptance  []string  `json:"acceptance,omitempty"`
}

// Previous is what an update changed from: the status and the title the ticket had.
type Previous struct {
	Status string `json:"status"`
	Title  string `json:"title"`
}

// Payload is a ticket event's payload (kind "ticket"). Action is pin (a new pin), update (a re-pin
// of a ticket already pinned; Previous says what it was) or unpin (Ticket then holds only its
// source, key, title, status and url).
type Payload struct {
	Ticket   Ticket    `json:"ticket"`
	Action   string    `json:"action"`
	Previous *Previous `json:"previous,omitempty"`
}

// Error is a refusal, worded for the agent (the server answers 400 with it).
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func refuse(format string, args ...any) error { return &Error{msg: fmt.Sprintf(format, args...)} }

// line is one-line text: topics.CleanText (whitespace to one space, control characters gone), cut.
func line(s string, limit int) string { return topics.CleanText(s, limit) }

// block is multi-line text (markdown): line breaks and tabs kept, \r\n made \n, other control,
// bidi and zero-width characters gone, cut to limit runes.
func block(s string, limit int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			b.WriteRune('\n')
		case r < 0x20, r >= 0x7f && r <= 0x9f:
		case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0xfeff:
		case r == 0x2028 || r == 0x2029:
			b.WriteRune('\n')
		default:
			b.WriteRune(r)
		}
	}
	return topics.Cut(strings.TrimRightFunc(strings.TrimLeft(b.String(), "\n"), unicode.IsSpace), limit)
}

// reader decodes the known fields of one JSON object, naming the first one of the wrong type.
type reader struct {
	obj  map[string]json.RawMessage
	path string
	err  error
}

func (r *reader) name(f string) string {
	if r.path == "" {
		return f
	}
	return r.path + "." + f
}

func (r *reader) str(f string) string {
	raw, ok := r.obj[f]
	if !ok || r.err != nil {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		r.err = refuse("%s must be a string", r.name(f))
	}
	return s
}

func (r *reader) strs(f string) []string {
	raw, ok := r.obj[f]
	if !ok || r.err != nil {
		return nil
	}
	var out []string
	if json.Unmarshal(raw, &out) != nil {
		r.err = refuse("%s must be a list of strings", r.name(f))
	}
	return out
}

func (r *reader) objs(f, what string) []map[string]json.RawMessage {
	raw, ok := r.obj[f]
	if !ok || r.err != nil {
		return nil
	}
	var out []map[string]json.RawMessage
	if json.Unmarshal(raw, &out) != nil {
		r.err = refuse("%s must be a list of objects (%s)", r.name(f), what)
	}
	return out
}

// Clean validates and caps a ticket the agent sent.
func Clean(raw json.RawMessage) (Ticket, error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return Ticket{}, refuse("the ticket must be a JSON object")
	}
	r := &reader{obj: obj}
	t := Ticket{
		Source:      line(r.str("source"), MaxSource),
		Key:         line(r.str("key"), MaxKey),
		Title:       line(r.str("title"), MaxTitle),
		Status:      line(r.str("status"), MaxChip),
		Type:        line(r.str("type"), MaxChip),
		Priority:    line(r.str("priority"), MaxChip),
		Assignee:    line(r.str("assignee"), MaxPerson),
		Reporter:    line(r.str("reporter"), MaxPerson),
		Description: block(r.str("description"), MaxDescription),
	}
	rawURL := r.str("url")
	labels := r.strs("labels")
	acceptance := r.strs("acceptance")
	fields := r.objs("fields", "name, value")
	comments := r.objs("comments", "author, at, body")
	if r.err != nil {
		return Ticket{}, r.err
	}
	for _, s := range labels {
		if v := line(s, MaxLabel); v != "" && len(t.Labels) < MaxLabels {
			t.Labels = append(t.Labels, v)
		}
	}
	for _, s := range acceptance {
		if v := line(s, MaxAcceptanceText); v != "" && len(t.Acceptance) < MaxAcceptance {
			t.Acceptance = append(t.Acceptance, v)
		}
	}
	for i, o := range fields {
		fr := &reader{obj: o, path: fmt.Sprintf("fields[%d]", i)}
		f := Field{Name: line(fr.str("name"), MaxFieldName), Value: line(fr.str("value"), MaxFieldValue)}
		if fr.err != nil {
			return Ticket{}, fr.err
		}
		if f.Name != "" && len(t.Fields) < MaxFields {
			t.Fields = append(t.Fields, f)
		}
	}
	// The latest comments, oldest first: the last MaxComments given.
	for i, o := range comments {
		cr := &reader{obj: o, path: fmt.Sprintf("comments[%d]", i)}
		c := Comment{Author: line(cr.str("author"), MaxPerson), At: line(cr.str("at"), MaxAt), Body: block(cr.str("body"), MaxCommentBody)}
		if cr.err != nil {
			return Ticket{}, cr.err
		}
		if c.Body != "" {
			t.Comments = append(t.Comments, c)
		}
	}
	if len(t.Comments) > MaxComments {
		t.Comments = append([]Comment(nil), t.Comments[len(t.Comments)-MaxComments:]...)
	}
	switch {
	case t.Source == "":
		return Ticket{}, refuse(`a ticket needs a source (the tracker: "jira", "bugzilla", "github", …)`)
	case t.Key == "":
		return Ticket{}, refuse("a ticket needs a key (its id in the tracker, e.g. ABC-123)")
	case t.Title == "":
		return Ticket{}, refuse("a ticket needs a title")
	}
	if rawURL != "" {
		u, err := CleanURL(rawURL)
		if err != nil {
			return Ticket{}, err
		}
		t.URL = u
	}
	return t, nil
}

// CleanURL accepts an absolute http or https address with a host, and nothing else.
func CleanURL(s string) (string, error) {
	bad := refuse("url must be an http or https address of the ticket (e.g. https://tracker.example/browse/ABC-1)")
	if len(s) > MaxURL {
		return "", refuse("url is longer than %d characters", MaxURL)
	}
	for _, r := range s {
		if r <= 0x20 || r == 0x7f {
			return "", bad
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return "", bad
	}
	if scheme := strings.ToLower(u.Scheme); scheme != "http" && scheme != "https" {
		return "", bad
	}
	return s, nil
}

// Summary is the part of a ticket the state shows of each pin: who it is, its title, status and
// link, and the event that pinned or refreshed it last.
type Summary struct {
	Source    string    `json:"source"`
	Key       string    `json:"key"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	URL       string    `json:"url"`
	EventID   string    `json:"event_id"`
	Seq       int64     `json:"seq"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SummaryOf is the unpin event's ticket: the summary fields only.
func SummaryOf(t Ticket) Ticket {
	return Ticket{Source: t.Source, Key: t.Key, Title: t.Title, Status: t.Status, URL: t.URL}
}

// Row is what the fold reads of one ticket event.
type Row struct {
	Seq     int64
	EventID string
	Ts      time.Time
	Action  string
	Source  string
	Key     string
	Title   string
	Status  string
	URL     string
}

type id struct{ source, key string }

// Pinned folds a session's ticket events (any order) into the tickets pinned now, newest pin or
// refresh first: the first is the current one. A pin is identified by (source, key).
func Pinned(rows []Row) []Summary {
	rows = append([]Row(nil), rows...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Seq < rows[j].Seq })
	byID := map[id]Summary{}
	for _, r := range rows {
		k := id{r.Source, r.Key}
		switch r.Action {
		case "pin", "update":
			byID[k] = Summary{Source: r.Source, Key: r.Key, Title: r.Title, Status: r.Status, URL: r.URL, EventID: r.EventID, Seq: r.Seq, UpdatedAt: r.Ts}
		case "unpin":
			delete(byID, k)
		}
	}
	out := make([]Summary, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	return out
}
