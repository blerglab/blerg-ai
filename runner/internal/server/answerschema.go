package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"unicode/utf8"
)

const maxAnswerBytes = 16 << 10
const maxAnswerSchemaBytes = 8 << 10

var errAnswerSchema = errors.New("unsupported answer schema; see docs/answer-back-pages.md")
var errAnswerResult = errors.New("result does not match the answer schema")

// Decode exactly one JSON value. JSON reserialization also removes whitespace before storage.
func answerJSON(raw []byte, limit int) (any, error) {
	if len(raw) > limit || !utf8.Valid(raw) {
		return nil, errAnswerResult
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var v any
	if d.Decode(&v) != nil || d.Decode(new(any)) != io.EOF || !answerDepth(v, 0) {
		return nil, errAnswerResult
	}
	return v, nil
}
func answerDepth(v any, depth int) bool {
	if depth > 16 {
		return false
	}
	switch x := v.(type) {
	case map[string]any:
		for _, v := range x {
			if !answerDepth(v, depth+1) {
				return false
			}
		}
	case []any:
		for _, v := range x {
			if !answerDepth(v, depth+1) {
				return false
			}
		}
	}
	return true
}
func parseAnswerSchema(raw []byte) (map[string]any, error) {
	v, e := answerJSON(raw, maxAnswerSchemaBytes)
	if e != nil {
		return nil, errAnswerSchema
	}
	s, ok := v.(map[string]any)
	if !ok || !checkAnswerSchema(s, 0) {
		return nil, errAnswerSchema
	}
	return s, nil
}
func checkAnswerSchema(s map[string]any, depth int) bool {
	if depth > 8 {
		return false
	}
	typ, ok := s["type"].(string)
	if !ok {
		return false
	}
	allowed := map[string]bool{"type": true, "enum": true}
	switch typ {
	case "object":
		allowed["properties"] = true
		allowed["required"] = true
		allowed["additionalProperties"] = true
		if s["additionalProperties"] != false {
			return false
		}
		props, ok := s["properties"].(map[string]any)
		if !ok {
			return false
		}
		for _, v := range props {
			child, ok := v.(map[string]any)
			if !ok || !checkAnswerSchema(child, depth+1) {
				return false
			}
		}
		if req, exists := s["required"]; exists {
			rs, ok := req.([]any)
			if !ok {
				return false
			}
			seen := map[string]bool{}
			for _, v := range rs {
				k, ok := v.(string)
				if !ok || seen[k] {
					return false
				}
				if _, ok := props[k]; !ok {
					return false
				}
				seen[k] = true
			}
		}
	case "array":
		allowed["items"] = true
		allowed["minItems"] = true
		allowed["maxItems"] = true
		child, ok := s["items"].(map[string]any)
		if !ok || !checkAnswerSchema(child, depth+1) {
			return false
		}
	case "string":
		allowed["minLength"] = true
		allowed["maxLength"] = true
	case "number", "integer":
		allowed["minimum"] = true
		allowed["maximum"] = true
	case "boolean", "null":
	default:
		return false
	}
	for k, v := range s {
		if !allowed[k] {
			return false
		}
		switch k {
		case "minItems", "maxItems", "minLength", "maxLength":
			n, ok := v.(float64)
			if !ok || n < 0 || math.Trunc(n) != n || n > maxAnswerBytes {
				return false
			}
		case "minimum", "maximum":
			if _, ok := v.(float64); !ok {
				return false
			}
		case "enum":
			a, ok := v.([]any)
			if !ok || len(a) == 0 {
				return false
			}
		}
	}
	for _, pair := range [][2]string{{"minimum", "maximum"}, {"minItems", "maxItems"}, {"minLength", "maxLength"}} {
		lo, lok := s[pair[0]].(float64)
		hi, hok := s[pair[1]].(float64)
		if lok && hok && lo > hi {
			return false
		}
	}
	return true
}
func validateAnswer(s map[string]any, v any) bool {
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if reflect.DeepEqual(e, v) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	within := func(n float64, lo, hi string) bool {
		l, lok := s[lo].(float64)
		h, hok := s[hi].(float64)
		return (!lok || n >= l) && (!hok || n <= h)
	}
	switch s["type"] {
	case "object":
		x, ok := v.(map[string]any)
		if !ok {
			return false
		}
		props, ok := s["properties"].(map[string]any)
		if !ok {
			return false
		}
		for k, v := range x {
			p, ok := props[k].(map[string]any)
			if !ok || !validateAnswer(p, v) {
				return false
			}
		}
		if req, ok := s["required"].([]any); ok {
			for _, k := range req {
				key, ok := k.(string)
				if !ok {
					return false
				}
				if _, exists := x[key]; !exists {
					return false
				}
			}
		}
	case "array":
		x, ok := v.([]any)
		if !ok || !within(float64(len(x)), "minItems", "maxItems") {
			return false
		}
		items, ok := s["items"].(map[string]any)
		if !ok {
			return false
		}
		for _, v := range x {
			if !validateAnswer(items, v) {
				return false
			}
		}
	case "string":
		x, ok := v.(string)
		if !ok || !within(float64(utf8.RuneCountInString(x)), "minLength", "maxLength") {
			return false
		}
	case "number", "integer":
		x, ok := v.(float64)
		if !ok || !within(x, "minimum", "maximum") || (s["type"] == "integer" && math.Trunc(x) != x) {
			return false
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return false
		}
	case "null":
		if v != nil {
			return false
		}
	default:
		return false
	}
	return true
}
