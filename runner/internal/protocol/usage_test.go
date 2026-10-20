package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUsageUpdatedStrictReportedCountersAndExplicitUnknown(t *testing.T) {
	good := `{"scope":"turn","model":"fixture","usage":{"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0},"secret":"PRIVATE_FIXTURE"}`
	for _, raw := range []string{good, `{"scope":"turn","usage":null}`} {
		p, ok := DecodeUsageUpdated([]byte(raw))
		if !ok {
			t.Fatalf("valid snapshot refused")
		}
		clean, _ := json.Marshal(p)
		if strings.Contains(string(clean), "PRIVATE_FIXTURE") {
			t.Fatal("unknown native metadata leaked")
		}
	}
	for _, raw := range []string{`{"scope":"turn"}`, `{"scope":"session","usage":null}`, `{"scope":"turn","usage":{}}`, strings.Replace(good, `"output_tokens":0`, `"output_tokens":null`, 1), strings.Replace(good, `"input_tokens":0`, `"input_tokens":-1`, 1), strings.Replace(good, `"input_tokens":0`, `"input_tokens":"10"`, 1), strings.Replace(good, `"input_tokens":0`, `"input_tokens":1.5`, 1), strings.Replace(good, `"input_tokens":0`, `"input_tokens":1125899906842625`, 1), strings.Replace(good, `"fixture"`, `"bad\nmodel"`, 1)} {
		if _, ok := DecodeUsageUpdated([]byte(raw)); ok {
			t.Fatalf("malformed reported snapshot accepted: %s", raw)
		}
	}
}
