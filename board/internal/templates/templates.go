// Package templates is the registry of board templates: a named starting
// shape (columns and a field schema) that board creation can apply in the same
// transaction as the board itself. It deliberately knows nothing about the
// database; the caller copies a template into db.BoardParams.
package templates

import "encoding/json"

// FocusBoard is the id of the personal focus-board template.
const FocusBoard = "focus-board"

// Column is one column of a template, in board order.
type Column struct {
	Name     string
	Terminal bool // landing here means the work is done
}

// Template is one registered board template.
type Template struct {
	ID          string
	Name        string
	Description string
	Columns     []Column
	FieldSchema json.RawMessage
}

const focusSchema = `[
  {"key":"source","label":"Source","type":"enum","values":["email","calendar","manual"],"display":"badge","filterable":true,"order":1},
  {"key":"due","label":"Due","type":"timestamp","display":"inline","order":2},
  {"key":"tracking","label":"Tracking","type":"text","display":"inline","order":3}
]`

var registry = []Template{
	{
		ID:          FocusBoard,
		Name:        "Focus board",
		Description: "Inbox, Today, This week, Waiting on, Someday, Proposed and Done, with source, due and tracking fields.",
		Columns: []Column{
			{Name: "Inbox"},
			{Name: "Today"},
			{Name: "This week"},
			{Name: "Waiting on"},
			{Name: "Someday"},
			{Name: "Proposed"},
			{Name: "Done", Terminal: true},
		},
		FieldSchema: json.RawMessage(focusSchema),
	},
}

// Get returns the template with the given id. The result is a copy: callers may
// not change the registry through it.
func Get(id string) (Template, bool) {
	for _, t := range registry {
		if t.ID == id {
			t.Columns = append([]Column(nil), t.Columns...)
			t.FieldSchema = append(json.RawMessage(nil), t.FieldSchema...)
			return t, true
		}
	}
	return Template{}, false
}

// List returns every template, in registry order.
func List() []Template {
	out := make([]Template, 0, len(registry))
	for _, t := range registry {
		got, _ := Get(t.ID)
		out = append(out, got)
	}
	return out
}
