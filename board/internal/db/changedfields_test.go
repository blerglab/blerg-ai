package db

// changedFields is what makes an 'updated' event say WHICH fields moved, so
// the adversarial re-review debounce can tell a spec rewrite from curation.
// These cases need no database: the comparison is pure.

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestChangedFieldsNamesOnlyWhatMoved(t *testing.T) {
	label := "the pr"
	before := Card{
		Title:     "shape the thing",
		Body:      strp("## Design\n\nthe spec as written"),
		Type:      "task",
		Priority:  "medium",
		Size:      strp("M"),
		Model:     "claude-sonnet-5",
		AutoMerge: false,
		Fields:    json.RawMessage(`{"area": "api", "risk": 2}`),
		DedupKey:  strp("spec:review-debounce"),
		Repos:     []string{"blerg-board"},
		Tags:      []string{"design", "review"},
		Links:     []Link{{Kind: "pr", URL: "https://github.com/o/r/pull/1", Label: &label}},
	}

	cases := []struct {
		name  string
		patch CardParams
		want  []string
	}{
		{"empty patch", CardParams{}, []string{}},
		{"tag added", CardParams{Tags: &[]string{"design", "review", "spike"}}, []string{"tags"}},
		{"priority raised", CardParams{Priority: strp("urgent")}, []string{"priority"}},
		{"size set", CardParams{Size: strp("L")}, []string{"size"}},
		{"retitled", CardParams{Title: strp("shape the other thing")}, []string{"title"}},
		{"body rewritten", CardParams{Body: strp("## Design\n\nthe spec, revised")}, []string{"body"}},
		{
			"body rewritten alongside metadata",
			CardParams{Title: strp("a better title"), Body: strp("revised"), Tags: &[]string{"design"}},
			[]string{"title", "body", "tags"},
		},

		// The reason values are compared instead of merely spotted in the
		// patch: a client that PATCHes the whole card back to change one tag
		// would otherwise report a body rewrite it never made.
		{
			"whole card re-sent, only the tags edited",
			CardParams{
				Title: strp("shape the thing"), Body: strp("## Design\n\nthe spec as written"),
				Type: strp("task"), Priority: strp("medium"), Size: strp("M"),
				Model: strp("claude-sonnet-5"), AutoMerge: boolp(false),
				Fields:   json.RawMessage(`{"area": "api", "risk": 2}`),
				DedupKey: strp("spec:review-debounce"), Repos: &[]string{"blerg-board"},
				Tags:  &[]string{"design", "review", "spike"},
				Links: &[]Link{{Kind: "pr", URL: "https://github.com/o/r/pull/1", Label: &label}},
			},
			[]string{"tags"},
		},
		{"body re-sent unchanged", CardParams{Body: strp("## Design\n\nthe spec as written")}, []string{}},

		// jsonb does not preserve key order or whitespace, so the round-tripped
		// row must not read as an edit against the payload the client sent.
		{"fields re-sent, reordered", CardParams{Fields: json.RawMessage(`{"risk":2,"area":"api"}`)}, []string{}},
		{"fields edited", CardParams{Fields: json.RawMessage(`{"area":"web","risk":2}`)}, []string{"fields"}},

		// Tags land in the DB trimmed, de-duplicated and read back sorted;
		// a patch that only reshuffles them changed nothing.
		{"tags re-sent, reordered and padded", CardParams{Tags: &[]string{" review ", "design", "design"}}, []string{}},

		{"links replaced", CardParams{Links: &[]Link{{Kind: "pr", URL: "https://github.com/o/r/pull/2"}}}, []string{"links"}},
		{"link label dropped", CardParams{Links: &[]Link{{Kind: "pr", URL: "https://github.com/o/r/pull/1"}}}, []string{"links"}},
		{"repos re-sent unchanged", CardParams{Repos: &[]string{"blerg-board"}}, []string{}},
		{"repo added", CardParams{Repos: &[]string{"blerg-board", "zarnk"}}, []string{"repos"}},
		{"auto_merge flipped", CardParams{AutoMerge: boolp(true)}, []string{"auto_merge"}},
		{"model cleared", CardParams{Model: strp("")}, []string{"model"}},
		{"dedup key unchanged", CardParams{DedupKey: strp("spec:review-debounce")}, []string{}},

		// "" for type means "leave it alone" on the write path, so it is not
		// a change either.
		{"type blanked", CardParams{Type: strp("")}, []string{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := changedFields(before, tc.patch)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("changedFields = %v, want %v", got, tc.want)
			}
		})
	}
}

// A card with nothing set yet: the nil-valued columns must compare as "" and
// not as a change when the patch sets them empty.
func TestChangedFieldsAgainstAnEmptyCard(t *testing.T) {
	for name, tc := range map[string]struct {
		patch CardParams
		want  []string
	}{
		"body set for the first time": {CardParams{Body: strp("a spec")}, []string{"body"}},
		"body set to empty":           {CardParams{Body: strp("")}, []string{}},
		"size cleared":                {CardParams{Size: strp("")}, []string{}},
		"dedup key cleared":           {CardParams{DedupKey: strp("")}, []string{}},
		"tags cleared":                {CardParams{Tags: &[]string{}}, []string{}},
		"links cleared":               {CardParams{Links: &[]Link{}}, []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			got := changedFields(Card{Fields: json.RawMessage(`{}`)}, tc.patch)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("changedFields = %v, want %v", got, tc.want)
			}
		})
	}
}

// The list is marshalled straight into the event's data column; an empty one
// has to be [] and not null, or "no field list" (a legacy row) and "this write
// changed nothing" would look the same to the debounce query.
func TestChangedFieldsMarshalsEmptyAsArray(t *testing.T) {
	b, err := json.Marshal(map[string]any{"fields": changedFields(Card{}, CardParams{})})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"fields":[]}` {
		t.Errorf("event data = %s, want {\"fields\":[]}", b)
	}
}

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }
