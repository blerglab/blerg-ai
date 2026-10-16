package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
)

// StripNUL returns a JSON payload with every NUL character inside its strings replaced by
// U+FFFD. Postgres jsonb refuses the \u0000 escape Go writes for a NUL, so an agent event whose
// tool output contained one (a `cat` of a binary file, say) could never be stored: the append
// failed, no ack went back, and the daemon re-sent it every 30 s for good. The daemon strips
// before sending and the server before storing, so either side alone is enough.
func StripNUL(raw json.RawMessage) json.RawMessage {
	if !bytes.Contains(raw, []byte(`\u0000`)) && !bytes.Contains(raw, []byte(`\U0000`)) {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Not valid JSON for us: fall back to a textual swap of the escape itself.
		out := bytes.ReplaceAll(raw, []byte(`\u0000`), []byte(`�`))
		return bytes.ReplaceAll(out, []byte(`\U0000`), []byte(`�`))
	}
	out, err := json.Marshal(stripNULValue(v))
	if err != nil {
		return raw
	}
	return out
}

func stripNULValue(v any) any {
	switch t := v.(type) {
	case string:
		return strings.ReplaceAll(t, "\x00", "�")
	case []any:
		for i := range t {
			t[i] = stripNULValue(t[i])
		}
		return t
	case map[string]any:
		for k, val := range t {
			t[k] = stripNULValue(val)
		}
		return t
	default:
		return v
	}
}
