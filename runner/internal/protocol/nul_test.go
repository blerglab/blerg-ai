package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStripNULReplacesNULInNestedStrings(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"output": "head\x00tail",
		"nested": map[string]any{"list": []any{"a\x00", 1, true}},
		"clean":  "fine",
	})
	if !strings.Contains(string(raw), `\u0000`) {
		t.Fatalf("fixture lost its NUL escape: %s", raw)
	}
	out := StripNUL(raw)
	if strings.Contains(string(out), `\u0000`) {
		t.Fatalf("NUL escape survived: %s", out)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if v["output"] != "head�tail" || v["clean"] != "fine" {
		t.Fatalf("strings = %q / %q", v["output"], v["clean"])
	}
	if got := v["nested"].(map[string]any)["list"].([]any)[0]; got != "a�" {
		t.Fatalf("nested = %q", got)
	}
}

func TestStripNULLeavesCleanPayloadsUntouched(t *testing.T) {
	raw := json.RawMessage(`{"text":"no nul here \\u0041","n":1}`)
	if out := StripNUL(raw); string(out) != string(raw) {
		t.Fatalf("clean payload changed: %s", out)
	}
}

func TestStripNULFallsBackOnInvalidJSON(t *testing.T) {
	raw := json.RawMessage(`{"broken": "x\u0000`)
	out := StripNUL(raw)
	if strings.Contains(string(out), `\u0000`) {
		t.Fatalf("fallback kept the escape: %s", out)
	}
}
