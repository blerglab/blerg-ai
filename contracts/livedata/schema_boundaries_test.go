package livedata

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSchemaRejectionBoundaries(t *testing.T) {
	for name, tc := range map[string]struct{ schema, value string }{
		"missing type": {`{}`, `1`}, "unknown construct": {`{"type":"string","pattern":".*"}`, `"x"`},
		"enum shape": {`{"type":"string","enum":{}}`, `"x"`}, "empty enum": {`{"type":"string","enum":[]}`, `"x"`}, "enum value": {`{"type":"string","enum":["y"]}`, `"x"`},
		"object type": {`{"type":"object","properties":{},"additionalProperties":false}`, `[]`}, "properties shape": {`{"type":"object","properties":[],"additionalProperties":false}`, `{}`}, "open object": {`{"type":"object","properties":{},"additionalProperties":true}`, `{}`}, "unknown property": {`{"type":"object","properties":{},"additionalProperties":false}`, `{"x":1}`},
		"secret property": {`{"type":"object","properties":{"password":{"type":"string"}},"additionalProperties":false}`, `{}`}, "property schema shape": {`{"type":"object","properties":{"x":false},"additionalProperties":false}`, `{}`}, "present invalid property": {`{"type":"object","properties":{"x":{"type":"integer"}},"additionalProperties":false}`, `{"x":"bad"}`}, "absent unsafe property": {`{"type":"object","properties":{"x":{"type":"string","format":"password"}},"additionalProperties":false}`, `{}`},
		"required shape": {`{"type":"object","properties":{},"additionalProperties":false,"required":{}}`, `{}`}, "required name shape": {`{"type":"object","properties":{},"additionalProperties":false,"required":[1]}`, `{}`}, "required unknown": {`{"type":"object","properties":{},"additionalProperties":false,"required":["x"]}`, `{}`}, "required absent": {`{"type":"object","properties":{"x":{"type":"string"}},"additionalProperties":false,"required":["x"]}`, `{}`},
		"array type": {`{"type":"array","items":{"type":"integer"}}`, `{}`}, "array item shape": {`{"type":"array","items":true}`, `[]`}, "array invalid item": {`{"type":"array","items":{"type":"integer"}}`, `["bad"]`}, "empty array unsafe items": {`{"type":"array","items":{"type":"string","format":"password"}}`, `[]`},
		"number type": {`{"type":"number"}`, `"1"`}, "integer fraction": {`{"type":"integer"}`, `1.5`}, "minimum": {`{"type":"number","minimum":2}`, `1`}, "maximum": {`{"type":"number","maximum":2}`, `3`}, "minimum shape": {`{"type":"number","minimum":"x"}`, `1`}, "maximum shape": {`{"type":"number","maximum":"x"}`, `1`},
	} {
		t.Run(name, func(t *testing.T) {
			var schema map[string]any
			var value any
			if decodeSchemaJSON([]byte(tc.schema), &schema) != nil || decodeSchemaJSON([]byte(tc.value), &value) != nil {
				t.Fatal("fixture")
			}
			if validateSchema(schema, value, 0) {
				t.Fatal("ineligible arguments accepted")
			}
		})
	}
	if validateSchema(map[string]any{"type": "string"}, "x", 33) {
		t.Fatal("schema depth accepted")
	}
	if validateSchema(nil, nil, 0) {
		t.Fatal("nil schema accepted")
	}
}

func TestSchemaNumberAndLengthBoundaries(t *testing.T) {
	for _, v := range []any{1, "1", json.Number("1e10"), json.Number(strings.Repeat("1", 257)), json.Number("bad")} {
		if _, ok := schemaNumber(v); ok {
			t.Fatal("unsafe schema number accepted")
		}
	}
	for _, v := range []any{"bad", json.Number("-1"), json.Number("1.5")} {
		if lengthOK(map[string]any{"minLength": v}, 3, "minLength", "maxLength") {
			t.Fatal("invalid length limit accepted")
		}
	}
	if lengthOK(map[string]any{"minLength": json.Number("4")}, 3, "minLength", "maxLength") || lengthOK(map[string]any{"maxLength": json.Number("2")}, 3, "minLength", "maxLength") {
		t.Fatal("length bounds ignored")
	}
}

func TestSchemaEligibilityBoundaries(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"type":"unknown"}`, `{"type":"string","format":"password"}`, `{"type":"string","description":1}`, `{"type":"string","title":false}`, `{"type":"string","properties":{}}`, `{"type":"object","properties":[],"additionalProperties":false}`,
		`{"type":"string","required":[]}`, `{"type":"object","properties":{},"additionalProperties":false,"required":{}}`, `{"type":"object","properties":{},"additionalProperties":false,"required":[1]}`,
		`{"type":"string","additionalProperties":false}`, `{"type":"object","properties":{},"additionalProperties":true}`, `{"type":"object","properties":{}}`, `{"type":"object","additionalProperties":false}`,
		`{"type":"string","items":{}}`, `{"type":"string","enum":{}}`, `{"type":"string","enum":[]}`, `{"type":"string","minimum":1}`, `{"type":"number","minimum":"bad"}`,
		`{"type":"string","minItems":1}`, `{"type":"array","minLength":1}`, `{"type":"string","minLength":-1}`, `{"type":"string","minLength":1.5}`, `{"type":"string","minLength":"bad"}`, `{"type":"string","remote":true}`,
		`{"type":"object","properties":{"password":{"type":"string"}},"additionalProperties":false}`, `{"type":"object","properties":{"x":false},"additionalProperties":false}`, `{"type":"object","properties":{"x":{"type":"invalid"}},"additionalProperties":false}`,
		`{"type":"array","items":false}`, `{"type":"array","items":{"type":"invalid"}}`,
	} {
		var schema map[string]any
		if decodeSchemaJSON([]byte(raw), &schema) != nil {
			t.Fatal("fixture")
		}
		if schemaEligible(schema, 0) {
			t.Fatal("unsafe schema eligible:", raw)
		}
	}
	if schemaEligible(map[string]any{"type": "string"}, 33) {
		t.Fatal("schema depth accepted")
	}
}
