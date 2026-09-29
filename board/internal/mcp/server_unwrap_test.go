package mcp

import (
	"encoding/json"
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
			out := unwrapStringJSON(json.RawMessage(c.in), "field_schema", "fields")
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
		if out := unwrapStringJSON(json.RawMessage(in), "field_schema", "fields"); !json.Valid(out) {
			t.Errorf("output not valid JSON for %s", in)
		}
	}
}
