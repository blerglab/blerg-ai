package livedata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
)

// ValidateToolArguments accepts a deliberately closed JSON Schema subset. Unknown
// constructs (including remote refs, compositions and secret annotations) refuse
// eligibility; they are never silently ignored. The canonical definition must
// match the existing MCP pin, including its name and description.
func ValidateToolArguments(definition, arguments []byte, name, hash string) bool {
	var def map[string]json.RawMessage
	if json.Unmarshal(definition, &def) != nil || len(def) != 3 || SecretArguments(definition) {
		return false
	}
	var tool, description string
	if json.Unmarshal(def["name"], &tool) != nil || json.Unmarshal(def["description"], &description) != nil || tool != name {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(definition))
	dec.UseNumber()
	var value any
	if dec.Decode(&value) != nil {
		return false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(value) != nil {
		return false
	}
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}))
	if hex.EncodeToString(sum[:]) != hash {
		return false
	}
	var schema map[string]any
	if decodeSchemaJSON(def["inputSchema"], &schema) != nil {
		return false
	}
	var args any
	if decodeSchemaJSON(arguments, &args) != nil {
		return false
	}
	return schemaEligible(schema, 0) && validateSchema(schema, args, 0)
}
func validateSchema(s map[string]any, v any, depth int) bool {
	if depth > 32 || s == nil {
		return false
	}
	for k := range s {
		switch k {
		case "type", "properties", "required", "additionalProperties", "items", "enum", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "description", "title", "default":
		default:
			return false
		}
	}
	typ, ok := s["type"].(string)
	if !ok {
		return false
	}
	if choices, has := s["enum"]; has {
		list, ok := choices.([]any)
		if !ok || len(list) == 0 {
			return false
		}
		raw, _ := json.Marshal(v)
		match := false
		for _, choice := range list {
			b, _ := json.Marshal(choice)
			if bytes.Equal(raw, b) {
				match = true
			}
		}
		if !match {
			return false
		}
	}
	switch typ {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return false
		}
		props, ok := s["properties"].(map[string]any)
		if !ok {
			return false
		}
		// Open or unspecified schemas cannot prove that unknown authentication inputs
		// are ineligible. This first slice accepts explicitly closed objects only.
		if closed, ok := s["additionalProperties"].(bool); !ok || closed {
			return false
		}
		for key, sub := range props {
			nested, ok := sub.(map[string]any)
			if !ok || secretName(key) {
				return false
			}
			if item, present := obj[key]; present {
				if !validateSchema(nested, item, depth+1) {
					return false
				}
			} else if !schemaEligible(nested, depth+1) {
				return false
			}
		}
		for key := range obj {
			if _, ok := props[key]; !ok {
				return false
			}
		}
		if required, has := s["required"]; has {
			list, ok := required.([]any)
			if !ok {
				return false
			}
			for _, key := range list {
				name, ok := key.(string)
				if !ok {
					return false
				}
				if _, known := props[name]; !known {
					return false
				}
				if _, present := obj[name]; !present {
					return false
				}
			}
		}
	case "array":
		list, ok := v.([]any)
		if !ok || !lengthOK(s, len(list), "minItems", "maxItems") {
			return false
		}
		item, ok := s["items"].(map[string]any)
		if !ok {
			return false
		}
		for _, v := range list {
			if !validateSchema(item, v, depth+1) {
				return false
			}
		}
		return schemaEligible(item, depth+1)
	case "string":
		text, ok := v.(string)
		return ok && lengthOK(s, len([]rune(text)), "minLength", "maxLength")
	case "number", "integer":
		n, ok := schemaNumber(v)
		if !ok || typ == "integer" && !n.IsInt() {
			return false
		}
		if raw, has := s["minimum"]; has {
			lower, ok := schemaNumber(raw)
			if !ok || n.Cmp(lower) < 0 {
				return false
			}
		}
		if raw, has := s["maximum"]; has {
			upper, ok := schemaNumber(raw)
			if !ok || n.Cmp(upper) > 0 {
				return false
			}
		}
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	default:
		return false
	}
	return true
}
func decodeSchemaJSON(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(out)
}
func schemaNumber(v any) (*big.Rat, bool) {
	n, ok := v.(json.Number)
	if !ok || len(n) > 256 {
		return nil, false
	}
	// Reject exponent notation rather than allocating unbounded exponent values.
	text := string(n)
	if strings.ContainsAny(text, "eE") {
		return nil, false
	}
	rat, ok := new(big.Rat).SetString(text)
	return rat, ok
}
func lengthOK(s map[string]any, n int, lo, hi string) bool {
	size := new(big.Rat).SetInt64(int64(n))
	for _, key := range []string{lo, hi} {
		if raw, has := s[key]; has {
			x, ok := schemaNumber(raw)
			if !ok || x.Sign() < 0 || !x.IsInt() || key == lo && size.Cmp(x) < 0 || key == hi && size.Cmp(x) > 0 {
				return false
			}
		}
	}
	return true
}
func schemaEligible(s map[string]any, depth int) bool {
	// Validate absent fields too, so hidden password/writeOnly/ref constructs cannot
	// survive merely because today's arguments omit the property.
	raw, _ := json.Marshal(s)
	if depth > 32 || strings.Contains(string(raw), `"format"`) || SecretArguments(raw) {
		return false
	}
	typ, ok := s["type"].(string)
	if !ok {
		return false
	}
	switch typ {
	case "object", "array", "string", "number", "integer", "boolean", "null":
	default:
		return false
	}
	for key, value := range s {
		switch key {
		case "type":
		case "description", "title":
			if _, ok := value.(string); !ok {
				return false
			}
		case "default": // Metadata only, never inserted into the approved arguments.
		case "properties":
			if typ != "object" {
				return false
			}
			if _, ok := value.(map[string]any); !ok {
				return false
			}
		case "required":
			if typ != "object" {
				return false
			}
			list, ok := value.([]any)
			if !ok {
				return false
			}
			for _, item := range list {
				if _, ok := item.(string); !ok {
					return false
				}
			}
		case "additionalProperties":
			if typ != "object" || value != false {
				return false
			}
		case "items":
			if typ != "array" {
				return false
			}
		case "enum":
			list, ok := value.([]any)
			if !ok || len(list) == 0 {
				return false
			}
		case "minimum", "maximum":
			if typ != "number" && typ != "integer" {
				return false
			}
			if _, ok := schemaNumber(value); !ok {
				return false
			}
		case "minLength", "maxLength", "minItems", "maxItems":
			if strings.HasSuffix(key, "Length") && typ != "string" || strings.HasSuffix(key, "Items") && typ != "array" {
				return false
			}
			x, ok := schemaNumber(value)
			if !ok || x.Sign() < 0 || !x.IsInt() {
				return false
			}
		default:
			return false
		}
	}
	if typ == "object" {
		if _, ok := s["properties"].(map[string]any); !ok {
			return false
		}
		if s["additionalProperties"] != false {
			return false
		}
	}

	if props, ok := s["properties"].(map[string]any); ok {
		for key, sub := range props {
			nested, ok := sub.(map[string]any)
			if secretName(key) || !ok || !schemaEligible(nested, depth+1) {
				return false
			}
		}
	}
	if item, has := s["items"]; has {
		sub, ok := item.(map[string]any)
		if !ok || !schemaEligible(sub, depth+1) {
			return false
		}
	}
	return true
}
