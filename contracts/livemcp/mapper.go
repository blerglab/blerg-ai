// Package livemcp owns bounded declarative normalization of MCP result data.
// It does not authorize tool execution or create polling demand.
package livemcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

const MaxRows = 10000
const MaxBytes = 5 * 1024 * 1024
const maxMappingBytes = 64 * 1024

// Field selects a bounded JSON pointer or a scalar constant. Timestamp formats
// are rfc3339, unix_s, and unix_ms; timezone must be an explicit IANA zone.
// FilterNull filters a row only for explicit null, never for an absent field.
type Field struct {
	Name       string          `json:"name"`
	Path       string          `json:"path,omitempty"`
	Constant   json.RawMessage `json:"constant,omitempty"`
	Type       string          `json:"type"`
	Format     string          `json:"format,omitempty"`
	Timezone   string          `json:"timezone,omitempty"`
	FilterNull bool            `json:"filter_null,omitempty"`
}

type Mapping struct {
	RowsPath string   `json:"rows_path"`
	Fields   []Field  `json:"fields"`
	Keys     []string `json:"keys"`
}

type Row map[string]any

type preparedField struct {
	field    Field
	path     []string
	constant any
}

type preparedMapping struct {
	rows   []string
	fields []preparedField
	keys   []string
}

var fieldName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// Validate must succeed before a mapping is included in a binding proposal.
// Validation does not approve a binding or establish that arguments are nonsecret.
func Validate(mapping Mapping) error {
	_, err := prepare(mapping)
	logFailure(err)
	return err
}

// Map rejects invalid samples atomically. A nil error means every row was
// normalized, or explicitly filtered for null by the approved mapping. Duplicate
// keys are corrections: the last complete row wins, preserving first-key order.
func Map(mapping Mapping, sample []byte) (rows []Row, resultErr error) {
	defer func() { logFailure(resultErr) }()
	prepared, err := prepare(mapping)
	if err != nil {
		return nil, err
	}
	value, err := decode(sample, MaxBytes)
	if err != nil {
		return nil, err
	}
	selected, ok := lookup(value, prepared.rows)
	if !ok {
		return nil, ErrInvalidSample
	}
	input, ok := selected.([]any)
	if !ok {
		return nil, ErrInvalidSample
	}
	if len(input) > MaxRows {
		return nil, ErrSampleLimit
	}
	rows = make([]Row, 0, len(input))
	indices := make(map[string]int, len(input))
	sizes := make(map[string]int, len(input))
	total := 2 // JSON array delimiters; include every comma below.
	for _, item := range input {
		if _, ok := item.(map[string]any); !ok {
			return nil, ErrInvalidSample
		}
		row, filtered, err := mapRow(prepared.fields, item)
		if err != nil {
			return nil, err
		}
		if filtered {
			continue
		}
		keyValues := make([]any, len(prepared.keys))
		for i, key := range prepared.keys {
			keyValues[i] = row[key]
		}
		key, err := json.Marshal(keyValues)
		if err != nil {
			return nil, ErrInvalidSample
		}
		row["key"] = string(key)
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, ErrInvalidSample
		}
		total += len(encoded) + 1 - sizes[string(key)]
		if total > MaxBytes {
			return nil, ErrSampleLimit
		}
		sizes[string(key)] = len(encoded) + 1
		if index, exists := indices[string(key)]; exists {
			rows[index] = row
		} else {
			indices[string(key)] = len(rows)
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func mapRow(fields []preparedField, item any) (Row, bool, error) {
	row := make(Row, len(fields)+1)
	// Validate every selected field before filtering, so a null in one field
	// cannot hide schema drift in another.
	filtered := false
	for _, p := range fields {
		value := p.constant
		if len(p.field.Constant) == 0 {
			var ok bool
			value, ok = lookup(item, p.path)
			if !ok {
				return nil, false, ErrInvalidSample
			}
		}
		if value == nil && p.field.FilterNull {
			filtered = true
			continue
		}
		converted, err := convert(p.field, value)
		if err != nil {
			return nil, false, ErrInvalidSample
		}
		row[p.field.Name] = converted
	}
	return row, filtered, nil
}

func prepare(mapping Mapping) (preparedMapping, error) {
	bad := preparedMapping{}
	raw, err := json.Marshal(mapping)
	if err != nil || len(raw) > maxMappingBytes || len(mapping.Fields) == 0 || len(mapping.Fields) > 64 || len(mapping.Keys) == 0 || len(mapping.Keys) > 8 {
		return bad, ErrInvalidMapping
	}
	rows, ok := pointer(mapping.RowsPath)
	if !ok {
		return bad, ErrInvalidMapping
	}
	out := preparedMapping{rows: rows, keys: append([]string(nil), mapping.Keys...)}
	names := map[string]bool{}
	for _, f := range mapping.Fields {
		if !fieldName.MatchString(f.Name) || f.Name == "key" || names[f.Name] {
			return bad, ErrInvalidMapping
		}
		names[f.Name] = true
		p := preparedField{field: f}
		if len(f.Constant) > 0 {
			if f.Path != "" || f.FilterNull {
				return bad, ErrInvalidMapping
			}
			value, err := decode(f.Constant, 4096)
			if err != nil {
				return bad, ErrInvalidMapping
			}
			_, err = convert(f, value)
			if err != nil {
				return bad, ErrInvalidMapping
			}
			p.constant = value
		} else {
			var ok bool
			p.path, ok = pointer(f.Path)
			if !ok {
				return bad, ErrInvalidMapping
			}
		}
		switch f.Type {
		case "number", "string", "boolean":
			if f.Format != "" || f.Timezone != "" {
				return bad, ErrInvalidMapping
			}
		case "timestamp":
			if f.Timezone == "" || f.Timezone == "Local" {
				return bad, ErrInvalidMapping
			}
			if _, err := time.LoadLocation(f.Timezone); err != nil {
				return bad, ErrInvalidMapping
			}
			if f.Format != "rfc3339" && f.Format != "unix_s" && f.Format != "unix_ms" {
				return bad, ErrInvalidMapping
			}
		default:
			return bad, ErrInvalidMapping
		}
		out.fields = append(out.fields, p)
	}
	keys := map[string]bool{}
	for _, k := range mapping.Keys {
		if !names[k] || keys[k] {
			return bad, ErrInvalidMapping
		}
		keys[k] = true
	}
	return out, nil
}

func pointer(path string) ([]string, bool) {
	if path == "" {
		return nil, true
	}
	if len(path) > 512 || !strings.HasPrefix(path, "/") {
		return nil, false
	}
	tokens := strings.Split(path[1:], "/")
	if len(tokens) > 16 {
		return nil, false
	}
	for i, t := range tokens {
		if strings.ContainsAny(t, "*[]") {
			return nil, false
		}
		for j := 0; j < len(t); j++ {
			if t[j] == '~' {
				j++
				if j == len(t) || (t[j] != '0' && t[j] != '1') {
					return nil, false
				}
			}
		}
		tokens[i] = strings.ReplaceAll(strings.ReplaceAll(t, "~1", "/"), "~0", "~")
	}
	return tokens, true
}

func lookup(value any, path []string) (any, bool) {
	for _, part := range path {
		switch v := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = v[part]
			if !ok {
				return nil, false
			}
		case []any:
			if part == "" || (len(part) > 1 && part[0] == '0') {
				return nil, false
			}
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(v) {
				return nil, false
			}
			value = v[index]
		default:
			return nil, false
		}
	}
	return value, true
}

func convert(f Field, value any) (any, error) {
	switch f.Type {
	case "string":
		if s, ok := value.(string); ok {
			return s, nil
		}
	case "boolean":
		if b, ok := value.(bool); ok {
			return b, nil
		}
	case "number":
		var text string
		switch v := value.(type) {
		case json.Number:
			text = string(v)
		case string:
			text = v
		case float64:
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				return v, nil
			}
		}
		if text != "" {
			n, err := strconv.ParseFloat(text, 64)
			if err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) {
				return n, nil
			}
		}
	case "timestamp":
		var stamp time.Time
		switch f.Format {
		case "rfc3339":
			text, ok := value.(string)
			if !ok {
				break
			}
			parsed, err := time.Parse(time.RFC3339Nano, text)
			if err == nil {
				stamp = parsed
			}
		case "unix_s", "unix_ms":
			// Integer-only epochs avoid float rounding and unbounded conversions.
			var text string
			switch v := value.(type) {
			case json.Number:
				text = string(v)
			case string:
				text = v
			}
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				break
			}
			if f.Format == "unix_ms" {
				if n < -62135596800000 || n > 253402300799999 {
					break
				}
				stamp = time.UnixMilli(n)
			} else {
				if n < -62135596800 || n > 253402300799 {
					break
				}
				stamp = time.Unix(n, 0)
			}
		}
		if !stamp.IsZero() && stamp.Year() >= 1 && stamp.Year() <= 9999 {
			return stamp.UTC().Format(time.RFC3339Nano), nil
		}
	}
	return nil, ErrInvalidSample
}

// Decode with fixed depth/node/byte bounds and reject duplicate object keys.
// Neither decoding errors nor source values escape into logs or returned errors.
func decode(raw []byte, limit int) (any, error) {
	if len(raw) > limit {
		return nil, ErrSampleLimit
	}
	if !utf8.Valid(raw) {
		return nil, ErrInvalidSample
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	nodes := 0
	value, err := decodeValue(dec, 0, &nodes)
	if err != nil {
		return nil, err
	}
	if _, err = dec.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidSample
	}
	return value, nil
}

func decodeValue(dec *json.Decoder, depth int, nodes *int) (any, error) {
	*nodes++
	if depth > 64 || *nodes > 200000 {
		return nil, ErrSampleLimit
	}
	token, err := dec.Token()
	if err != nil {
		return nil, ErrInvalidSample
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := map[string]any{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, ErrInvalidSample
			}
			name, ok := key.(string)
			if !ok {
				return nil, ErrInvalidSample
			}
			if _, exists := object[name]; exists {
				return nil, ErrInvalidSample
			}
			child, err := decodeValue(dec, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			object[name] = child
		}
		ending, err := dec.Token()
		if err != nil || ending != json.Delim('}') {
			return nil, ErrInvalidSample
		}
		return object, nil
	case '[':
		array := []any{}
		for dec.More() {
			child, err := decodeValue(dec, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			array = append(array, child)
		}
		ending, err := dec.Token()
		if err != nil || ending != json.Delim(']') {
			return nil, ErrInvalidSample
		}
		return array, nil
	}
	return nil, ErrInvalidSample
}

func logFailure(err error) {
	if err == nil {
		return
	}
	// Only the three static sentinels below can be emitted.
	cause := "invalid_sample"
	switch {
	case errors.Is(err, ErrInvalidMapping):
		cause = "invalid_mapping"
	case errors.Is(err, ErrSampleLimit):
		cause = "sample_limit"
	}
	failurelog.Default.Event("live_mcp_mapping:"+cause, slog.LevelWarn, "component", "live_data", "operation", "live_mcp_mapping", "cause", cause)
}

// CanonicalObject provides the same byte/depth/node/duplicate-key bounds used
// by mapping. Binding arguments use it before secret/schema checks or storage.
func CanonicalObject(raw []byte) ([]byte, error) {
	value, err := decode(raw, maxMappingBytes)
	if err != nil {
		logFailure(err)
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		logFailure(ErrInvalidSample)
		return nil, ErrInvalidSample
	}
	out, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalidSample
	}
	return out, nil
}
