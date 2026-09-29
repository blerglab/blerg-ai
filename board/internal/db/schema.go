package db

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// FieldDef is one entry in a board's declared field schema. blerg-board knows
// nothing about incidents or features — the board declares its own shape and
// the UI/validation work generically off the declaration.
type FieldDef struct {
	Key        string   `json:"key"`
	Label      string   `json:"label,omitempty"`
	Type       string   `json:"type"` // enum|text|number|url|timestamp|bool
	Values     []string `json:"values,omitempty"`
	Display    string   `json:"display,omitempty"` // badge|chip|link|inline|hidden
	Filterable bool     `json:"filterable,omitempty"`
	Order      int      `json:"order"`
}

// FieldError names the offending key so the 422 is actionable.
type FieldError struct {
	Key string
	Msg string
}

func (e *FieldError) Error() string { return fmt.Sprintf("field %q: %s", e.Key, e.Msg) }

// ParseFieldSchema validates a board's field_schema document itself.
func ParseFieldSchema(raw []byte) ([]FieldDef, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var defs []FieldDef
	if err := json.Unmarshal(raw, &defs); err != nil {
		return nil, fmt.Errorf("field_schema is not a JSON array: %w", err)
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if d.Key == "" {
			return nil, &FieldError{Key: d.Key, Msg: "empty key"}
		}
		if seen[d.Key] {
			return nil, &FieldError{Key: d.Key, Msg: "duplicate key"}
		}
		seen[d.Key] = true
		switch d.Type {
		case "enum":
			if len(d.Values) == 0 {
				return nil, &FieldError{Key: d.Key, Msg: "enum with no values"}
			}
		case "text", "number", "url", "timestamp", "bool":
		default:
			return nil, &FieldError{Key: d.Key, Msg: fmt.Sprintf("unknown type %q", d.Type)}
		}
		switch d.Display {
		case "", "badge", "chip", "link", "inline", "hidden":
		default:
			return nil, &FieldError{Key: d.Key, Msg: fmt.Sprintf("unknown display %q", d.Display)}
		}
	}
	return defs, nil
}

// ValidateFields checks a card's fields object against the board's schema.
// Unknown keys and type mismatches are rejected, naming the offending key.
func ValidateFields(defs []FieldDef, fields map[string]any) error {
	byKey := make(map[string]FieldDef, len(defs))
	for _, d := range defs {
		byKey[d.Key] = d
	}
	for k, v := range fields {
		d, ok := byKey[k]
		if !ok {
			return &FieldError{Key: k, Msg: "not declared in the board's field schema"}
		}
		if v == nil {
			continue // explicit null clears a field
		}
		switch d.Type {
		case "enum":
			s, ok := v.(string)
			if !ok {
				return &FieldError{Key: k, Msg: "enum value must be a string"}
			}
			found := false
			for _, allowed := range d.Values {
				if s == allowed {
					found = true
					break
				}
			}
			if !found {
				return &FieldError{Key: k, Msg: fmt.Sprintf("value %q not in %v", s, d.Values)}
			}
		case "text":
			if _, ok := v.(string); !ok {
				return &FieldError{Key: k, Msg: "must be a string"}
			}
		case "number":
			switch v.(type) {
			case float64, int, int64, json.Number:
			default:
				return &FieldError{Key: k, Msg: "must be a number"}
			}
		case "url":
			s, ok := v.(string)
			if !ok {
				return &FieldError{Key: k, Msg: "must be a URL string"}
			}
			u, err := url.Parse(s)
			if err != nil || u.Scheme == "" {
				return &FieldError{Key: k, Msg: "must be an absolute URL"}
			}
		case "timestamp":
			s, ok := v.(string)
			if !ok {
				return &FieldError{Key: k, Msg: "must be an RFC 3339 timestamp string"}
			}
			if _, err := time.Parse(time.RFC3339, s); err != nil {
				return &FieldError{Key: k, Msg: "must be RFC 3339 (e.g. 2026-07-31T12:00:00Z)"}
			}
		case "bool":
			if _, ok := v.(bool); !ok {
				return &FieldError{Key: k, Msg: "must be a boolean"}
			}
		}
	}
	return nil
}
