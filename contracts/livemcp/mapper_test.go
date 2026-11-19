package livemcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"log/slog"
	"strings"
	"testing"
)

func quoteMapping() Mapping {
	return Mapping{RowsPath: "/quotes", Fields: []Field{
		{Name: "time", Path: "/timestamp", Type: "timestamp", Format: "rfc3339", Timezone: "America/New_York"},
		{Name: "value", Path: "/close", Type: "number"},
		{Name: "series", Constant: json.RawMessage(`"NVDA"`), Type: "string"},
	}, Keys: []string{"time", "series"}}
}

func TestMapQuotes(t *testing.T) {
	rows, err := Map(quoteMapping(), []byte(`{"quotes":[{"timestamp":"2026-11-02T09:30:00-05:00","close":0},{"timestamp":"2026-11-02T09:31:00-05:00","close":"185.25"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["value"] != float64(0) || rows[1]["value"] != 185.25 || rows[0]["time"] != "2026-11-02T14:30:00Z" || rows[0]["series"] != "NVDA" {
		t.Fatalf("unexpected rows: %#v", rows)
	}
	if rows[0]["key"] == rows[1]["key"] {
		t.Fatal("distinct times share a key")
	}
}

func TestMapAtomicFailure(t *testing.T) {
	for _, tail := range []string{`{"timestamp":"2026-11-02T14:31:00Z"}`, `{"timestamp":"2026-11-02T14:31:00Z","close":null}`, `{"timestamp":"2026-11-02T14:31:00Z","close":"NaN"}`, `{"timestamp":"2026-11-02T14:31:00Z","close":"+Inf"}`, `{"timestamp":"yesterday","close":2}`, `{"timestamp":"2026-11-02T14:31:00Z","close":{}}`, `null`} {
		t.Run(tail, func(t *testing.T) {
			raw := `{"quotes":[{"timestamp":"2026-11-02T14:30:00Z","close":1},` + tail + `]}`
			rows, err := Map(quoteMapping(), []byte(raw))
			if err == nil || rows != nil {
				t.Fatal("invalid sample partially accepted")
			}
		})
	}
}

func TestMapNullFilterAndCorrections(t *testing.T) {
	m := quoteMapping()
	m.Fields[1].FilterNull = true
	rows, err := Map(m, []byte(`{"quotes":[{"timestamp":"2026-11-02T14:30:00Z","close":1},{"timestamp":"2026-11-02T14:31:00Z","close":null},{"timestamp":"2026-11-02T14:30:00Z","close":2}]}`))
	if err != nil || len(rows) != 1 || rows[0]["value"] != float64(2) {
		t.Fatalf("correction/filter: %#v %v", rows, err)
	}
	// Missing differs from explicit null, even with a null filter.
	if rows, err = Map(m, []byte(`{"quotes":[{"timestamp":"2026-11-02T14:30:00Z"}]}`)); err == nil || rows != nil {
		t.Fatal("missing field filtered")
	}
}

func TestMapInvalidMapping(t *testing.T) {
	tests := map[string]func(*Mapping){
		"no fields":        func(m *Mapping) { m.Fields = nil },
		"no keys":          func(m *Mapping) { m.Keys = nil },
		"unknown key":      func(m *Mapping) { m.Keys = []string{"unknown"} },
		"duplicate key":    func(m *Mapping) { m.Keys = []string{"time", "time"} },
		"duplicate field":  func(m *Mapping) { m.Fields[1].Name = "time" },
		"reserved key":     func(m *Mapping) { m.Fields[1].Name = "key" },
		"expression":       func(m *Mapping) { m.Fields[1].Type = "javascript" },
		"both sources":     func(m *Mapping) { m.Fields[1].Constant = json.RawMessage(`1`) },
		"complex constant": func(m *Mapping) { m.Fields[2].Constant = json.RawMessage(`{}`) },
		"wildcard":         func(m *Mapping) { m.RowsPath = "/quotes/*" },
		"deep path":        func(m *Mapping) { m.RowsPath = strings.Repeat("/a", 17) },
		"bad escape":       func(m *Mapping) { m.RowsPath = "/quotes~2" },
		"no timezone":      func(m *Mapping) { m.Fields[0].Timezone = "" },
		"unknown timezone": func(m *Mapping) { m.Fields[0].Timezone = "not-a-zone" },
		"arbitrary format": func(m *Mapping) { m.Fields[0].Format = "today" },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			m := quoteMapping()
			change(&m)
			rows, err := Map(m, []byte(`{"quotes":[]}`))
			if !errors.Is(err, ErrInvalidMapping) || rows != nil {
				t.Fatalf("got %v %v", rows, err)
			}
		})
	}
}

func TestMapLimitsAndMalformedInput(t *testing.T) {
	valid := `{"timestamp":"2026-11-02T14:30:00Z","close":1}`
	for name, raw := range map[string]string{
		"too many rows":     `{"quotes":[` + strings.Repeat(valid+",", MaxRows) + valid + `]}`,
		"too many bytes":    strings.Repeat(" ", MaxBytes+1),
		"trailing document": `{"quotes":[]} {}`,
		"not array":         `{"quotes":{}}`,
		"empty input":       "",
		"deep input":        strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65),
	} {
		t.Run(name, func(t *testing.T) {
			rows, err := Map(quoteMapping(), []byte(raw))
			if err == nil || rows != nil {
				t.Fatal("invalid sample accepted")
			}
		})
	}
	rows, err := Map(quoteMapping(), []byte(`{"quotes":[]}`))
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty array: %v %v", rows, err)
	}
}

func TestMapPointerAndTimestamp(t *testing.T) {
	m := Mapping{RowsPath: "/a~1b", Fields: []Field{{Name: "time", Path: "/values/0", Type: "timestamp", Format: "unix_ms", Timezone: "UTC"}, {Name: "value", Path: "/a~0b", Type: "boolean"}}, Keys: []string{"time"}}
	rows, err := Map(m, []byte(`{"a/b":[{"values":[0],"a~b":false}]}`))
	if err != nil || len(rows) != 1 || rows[0]["time"] != "1970-01-01T00:00:00Z" || rows[0]["value"] != false {
		t.Fatalf("pointer: %v %v", rows, err)
	}
}

func TestMapSafeErrors(t *testing.T) {
	const canary = "secret-canary-value"
	for _, raw := range []string{`{"quotes":[{"timestamp":"` + canary + `","close":0}]}`, `{"quotes":[{"timestamp":"2026-11-02T14:30:00Z","close":"` + canary + `"}]}`} {
		_, err := Map(quoteMapping(), []byte(raw))
		if err == nil || strings.Contains(fmt.Sprint(err), canary) {
			t.Fatal("unsafe error")
		}
	}
}

func TestMapSafeDiagnostics(t *testing.T) {
	var output bytes.Buffer
	prior := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { failurelog.Default = prior })
	const canary = "mapping-secret-canary"
	_, err := Map(quoteMapping(), []byte(`{"quotes":[{"timestamp":"2026-11-02T14:30:00Z","close":"`+canary+`"}]}`))
	if err == nil || output.Len() == 0 || strings.Contains(output.String(), canary) || !strings.Contains(output.String(), "invalid_sample") {
		t.Fatal("missing or unsafe diagnostic")
	}
}
func TestMapNormalizedByteLimit(t *testing.T) {
	m := Mapping{Fields: []Field{{Name: "id", Path: "/id", Type: "number"}, {Name: "label", Constant: json.RawMessage(`"` + strings.Repeat("x", 3000) + `"`), Type: "string"}}, Keys: []string{"id"}}
	var b strings.Builder
	b.WriteByte('[')
	for i := range 2000 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%d}`, i)
	}
	b.WriteByte(']')
	if rows, err := Map(m, []byte(b.String())); !errors.Is(err, ErrSampleLimit) || rows != nil {
		t.Fatal("expanded normalized sample bypassed byte limit")
	}
}
func TestMapDuplicateObjectKeys(t *testing.T) {
	if rows, err := Map(quoteMapping(), []byte(`{"quotes":[],"quotes":[]}`)); err == nil || rows != nil {
		t.Fatal("ambiguous duplicate key accepted")
	}
}
