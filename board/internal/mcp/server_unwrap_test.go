package mcp

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestUnwrapStringJSON(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"stringified array", `{"name":"b","field_schema":"[{\"key\":\"sev\",\"type\":\"enum\"}]"}`,
			`[{"key":"sev","type":"enum"}]`},
		{"stringified object", `{"fields":"{\"severity\":\"sev1\"}"}`, `{"severity":"sev1"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := canonicalArgs(json.RawMessage(c.in))
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			for _, k := range []string{"field_schema", "fields"} {
				if raw, ok := m[k]; ok {
					if string(raw) != c.want {
						t.Errorf("%s = %s, want %s", k, raw, c.want)
					}
				}
			}
		})
	}
	// untouched: proper JSON, plain strings, invalid embedded JSON
	for _, in := range []string{
		`{"field_schema":[{"key":"a","type":"text"}]}`,
		`{"fields":{"a":1}}`,
		`{"title":"just a string arg"}`,
		`{"fields":"not json at all"}`,
		`{"fields":"[broken"}`,
	} {
		out, err := canonicalArgs(json.RawMessage(in))
		if err != nil || !json.Valid(out) {
			t.Errorf("not valid JSON for %s: %s %v", in, out, err)
		}
	}
}

// Arguments are decoded once into a canonical form: one object, nothing after
// it, each key exactly once under any spelling.
func TestCanonicalArgs(t *testing.T) {
	ok := map[string]string{
		"empty":       ``,
		"null":        `null`,
		"empty obj":   `{}`,
		"padded":      "  {\"a\":1}  \n",
		"escaped key": `{"board_id":"x"}`,
		"nested dupe": `{"fields":{"k":1,"K":2}}`, // custom field keys are not argument keys
	}
	for name, in := range ok {
		if out, err := canonicalArgs(json.RawMessage(in)); err != nil || !json.Valid(out) {
			t.Errorf("%s: %q -> %s, %v; want accepted", name, in, out, err)
		}
	}
	if out, _ := canonicalArgs(json.RawMessage(`{"board_id":"x"}`)); string(out) != `{"board_id":"x"}` {
		t.Errorf("unicode-escaped key not normalised: %s", out)
	}
	bad := map[string]string{
		"exact dupe":         `{"a":1,"a":2}`,
		"case dupe":          `{"review_id":"x","REVIEW_ID":"y"}`,
		"mixed case dupe":    `{"Review_Id":"x","review_id":"y"}`,
		"escaped dupe":       `{"board_id":"x","board_id":"y"}`,
		"kelvin sign":        `{"k":1,"K":2}`,
		"long s":             `{"s":1,"ſ":2}`,
		"trailing garbage":   `{"a":1}x`,
		"two objects":        `{"a":1}{"a":2}`,
		"array":              `[1]`,
		"string":             `"{\"a\":1}"`,
		"number":             `1`,
		"truncated":          `{"a":1`,
		"unterminated value": `{"a":`,
	}
	for name, in := range bad {
		out, err := canonicalArgs(json.RawMessage(in))
		if !errors.Is(err, errInvalidArguments) {
			t.Errorf("%s: %q -> %s, %v; want errInvalidArguments", name, in, out, err)
		}
	}
}
