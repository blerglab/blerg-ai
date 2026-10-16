package mcpgw

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalToolBytesHashToToolHash(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"n":{"type":"number","default":1E+2}}}`)
	canon, h := CanonicalTool("read", "Reads a thing", schema)
	if h != ToolHash("read", "Reads a thing", schema) {
		t.Fatal("CanonicalTool and ToolHash disagree")
	}
	sum := sha256.Sum256(canon)
	if hex.EncodeToString(sum[:]) != h {
		t.Fatal("the hash is not sha256 of the canonical bytes")
	}
	// Sorted keys, the name included, numbers verbatim, no trailing newline.
	s := string(canon)
	if !strings.HasPrefix(s, `{"description":"Reads a thing","inputSchema":{`) || !strings.HasSuffix(s, `,"name":"read"}`) {
		t.Fatalf("canonical shape: %s", s)
	}
	if !strings.Contains(s, `1E+2`) {
		t.Fatalf("number lexeme must survive: %s", s)
	}
	var back struct {
		Name, Description string
		InputSchema       json.RawMessage
	}
	if err := json.Unmarshal(canon, &back); err != nil || back.Name != "read" {
		t.Fatalf("canonical must parse back: %v", err)
	}
}

func TestClassifyDefinition(t *testing.T) {
	ok, _ := CanonicalTool("t", "plain text", nil)
	if got := ClassifyDefinition(ok); got != "" {
		t.Fatalf("plain = %q", got)
	}
	nul, _ := CanonicalTool("t", "has\x00nul", nil)
	if got := ClassifyDefinition(nul); got != "nul" {
		t.Fatalf("nul = %q (canonical %s)", got, nul)
	}
	// The six literal characters \u0000 in a description are text, not a NUL.
	literal, _ := CanonicalTool("t", `write \u0000 to the file`, nil)
	if got := ClassifyDefinition(literal); got != "" {
		t.Fatalf("literal escape text = %q", got)
	}
	tag, _ := CanonicalTool("t", "hidden \U000E0041 instruction", nil)
	if got := ClassifyDefinition(tag); got != "hidden_chars" {
		t.Fatalf("tag char = %q", got)
	}
	bidi, _ := CanonicalTool("t", "a\u202Eb", nil)
	if got := ClassifyDefinition(bidi); got != "hidden_chars" {
		t.Fatalf("bidi = %q", got)
	}
	iso, _ := CanonicalTool("t", "a\u2066b\u2069", nil)
	if got := ClassifyDefinition(iso); got != "hidden_chars" {
		t.Fatalf("isolate = %q", got)
	}
	big, _ := CanonicalTool("t", strings.Repeat("x", MaxDefinitionBytes+1), nil)
	if got := ClassifyDefinition(big); got != "too_large" {
		t.Fatalf("big = %q", got)
	}
	// A name is part of the definition too.
	badName, _ := CanonicalTool("re\u202Aad", "d", nil)
	if got := ClassifyDefinition(badName); got != "hidden_chars" {
		t.Fatalf("name bidi = %q", got)
	}
}

// TestToolHashGolden pins the hash of one definition. Changing this value means the canonical form
// or the hash changed, which invalidates EVERY existing pin: every pinned tool would read as
// changed. Do not update it to make the test pass.
func TestToolHashGolden(t *testing.T) {
	const want = "7213fe1d43a3d093316e9d7b4e0cac48c3876aae7d043923813677dd7bb538c3"
	got := ToolHash("read", "Reads a thing", json.RawMessage(`{"type":"object","properties":{"n":{"type":"number","default":1E+2}}}`))
	if got != want {
		t.Fatalf("ToolHash = %s, want %s", got, want)
	}
}
