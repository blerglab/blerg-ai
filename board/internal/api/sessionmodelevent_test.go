package api

import (
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/runner"
)

// modelFromEvent reads only genuine model-change reports: everything in the
// event stream flows through it, so a false positive would rewrite a
// session's recorded model from an unrelated payload.
func TestModelFromEvent(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"nested payload", `{"source_kind":"model_changed","payload":{"model":"claude-sonnet-5"}}`, "claude-sonnet-5"},
		{"flat payload", `{"source_kind":"model_changed","model":"claude-opus-5"}`, "claude-opus-5"},
		{"other source kind", `{"source_kind":"turn_done","payload":{"model":"claude-opus-5"}}`, ""},
		{"no source kind", `{"model":"claude-opus-5"}`, ""},
		{"change without a model", `{"source_kind":"model_changed","payload":{}}`, ""},
		{"not an object", `["model_changed"]`, ""},
		{"empty", ``, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := modelFromEvent(runner.Event{Kind: "status", Payload: json.RawMessage(tc.payload)})
			if got != tc.want {
				t.Fatalf("modelFromEvent = %q, want %q", got, tc.want)
			}
		})
	}
}

// sessionIDFromLabel is the ownership check behind "self" and behind refusing
// to let one session re-model another. Spawn APPENDS the tag, and part of a
// label is free text blerg-board does not own (a standing agent's name), so it must
// read the LAST tag and only accept a session-id-shaped value.
func TestSessionIDFromLabel(t *testing.T) {
	cases := map[string]string{
		"card #3 worker session [rs:1e066d80-6721-428b-9af5-2f553bddf212]": "1e066d80-6721-428b-9af5-2f553bddf212",
		"board session [rs:0dd948c1-bbfe-4fc2-a18f-b6f58c4d8521]":          "0dd948c1-bbfe-4fc2-a18f-b6f58c4d8521",
		// a standing agent NAMED like a tag must not shadow the real one
		`standing agent "x [rs:1e066d80-6721-428b-9af5-2f553bddf212]" session [rs:0dd948c1-bbfe-4fc2-a18f-b6f58c4d8521]`: "0dd948c1-bbfe-4fc2-a18f-b6f58c4d8521",
		// nor may a label smuggle in something that is not a session id
		"card #3 worker session [rs:abc-123]": "",
		"sweeper":                             "",
		"card #3 worker session [rs:1e066d80-6721-428b-9af5-2f553bddf212": "",
		"[rs:]": "",
	}
	for label, want := range cases {
		if got := sessionIDFromLabel(label); got != want {
			t.Fatalf("sessionIDFromLabel(%q) = %q, want %q", label, got, want)
		}
	}
}
