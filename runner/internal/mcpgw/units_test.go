package mcpgw

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestToolHashCanonical(t *testing.T) {
	a := ToolHash("t", "does things", json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"number"}}}`))
	b := ToolHash("t", "does things", json.RawMessage(`{
		"properties": {"b": {"type": "number"}, "a": {"type": "string"}},
		"type": "object"
	}`))
	if a == "" || a != b {
		t.Fatalf("key order and whitespace must not change the hash: %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("want hex sha256, got %q", a)
	}
	for name, h := range map[string]string{
		"description": ToolHash("t", "does other things", json.RawMessage(`{"type":"object"}`)),
		"schema":      ToolHash("t", "does things", json.RawMessage(`{"type":"object","x":1}`)),
		"name":        ToolHash("u", "does things", json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"number"}}}`)),
	} {
		if h == a {
			t.Errorf("changing the %s must change the hash", name)
		}
	}
	if ToolHash("t", "d", nil) != ToolHash("t", "d", json.RawMessage(`null`)) {
		t.Error("absent and null schema should hash the same")
	}
	if ToolHash("t", "<b>&", nil) == "" {
		t.Error("html characters must hash fine")
	}
}

func TestSanitizeResult(t *testing.T) {
	raw := json.RawMessage(`{"content":[
		{"type":"text","text":"hello"},
		{"type":"image","data":"AAAA","mimeType":"image/png"},
		{"type":"audio","data":"AAAA","mimeType":"audio/wav"},
		{"type":"resource","resource":{"uri":"file:///x","text":"secret body"}},
		{"type":"resource_link","uri":"https://evil.example/x"},
		{"type":"weird"}
	],"isError":true,"structuredContent":{"a":1},"_meta":{"x":1}}`)
	out := sanitizeResult(raw, 1024)
	var got struct {
		Content []map[string]any `json:"content"`
		IsError bool             `json:"isError"`
		Other   map[string]any   `json:"structuredContent"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if !got.IsError || got.Other != nil {
		t.Fatalf("isError kept, structuredContent dropped: %s", out)
	}
	if len(got.Content) != 6 {
		t.Fatalf("every block becomes a text block: %s", out)
	}
	for _, c := range got.Content {
		if c["type"] != "text" {
			t.Fatalf("non-text block leaked: %v", c)
		}
	}
	all := string(out)
	for _, leak := range []string{"AAAA", "secret body", "evil.example", "file:///x"} {
		if strings.Contains(all, leak) {
			t.Errorf("%q leaked through the placeholder: %s", leak, all)
		}
	}
	if got.Content[0]["text"] != "hello" {
		t.Errorf("text kept verbatim: %v", got.Content[0])
	}
	if !strings.Contains(got.Content[1]["text"].(string), "image") {
		t.Errorf("image placeholder: %v", got.Content[1])
	}
}

func TestSanitizeResultCaps(t *testing.T) {
	big := strings.Repeat("é", 1000) // 2 bytes per rune
	raw, _ := json.Marshal(map[string]any{"content": []map[string]any{
		{"type": "text", "text": big}, {"type": "text", "text": "second"},
	}})
	out := sanitizeResult(raw, 100)
	var got struct {
		Content []struct{ Text string } `json:"content"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, c := range got.Content {
		total += len(c.Text)
	}
	if total > 100+200 { // cap plus the truncation notice
		t.Fatalf("cap not applied, %d bytes of text", total)
	}
	if !strings.Contains(string(out), "truncated") {
		t.Fatalf("a truncation notice is expected: %s", out)
	}
	if strings.Contains(string(out), "second") {
		t.Fatalf("blocks after the cap must be dropped: %s", out)
	}
	if !json.Valid(out) {
		t.Fatal("must stay valid JSON (no split rune)")
	}
}

func TestSanitizeResultMalformed(t *testing.T) {
	out := sanitizeResult(json.RawMessage(`"not an object"`), 100)
	if !json.Valid(out) || !strings.Contains(string(out), `"isError":true`) {
		t.Fatalf("malformed upstream result becomes an error result: %s", out)
	}
}

func TestReadSSEResponse(t *testing.T) {
	stream := "event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\r\n\r\n" +
		": keepalive\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":99,\"method\":\"sampling/createMessage\"}\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":7,\n" +
		"data: \"result\":{\"ok\":true}}\n\n"
	m, err := readSSEResponse(strings.NewReader(stream), "7")
	if err != nil {
		t.Fatal(err)
	}
	if string(m.Result) != `{"ok":true}` {
		t.Fatalf("multi-line data joined and matched by id, got %s", m.Result)
	}
	if _, err := readSSEResponse(strings.NewReader("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"), "2"); err == nil {
		t.Fatal("stream ending without the response must be an error")
	}
	// Final event without a trailing blank line still counts.
	if _, err := readSSEResponse(strings.NewReader("data: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{}}"), "3"); err != nil {
		t.Fatalf("unterminated last event: %v", err)
	}
}

func TestFailLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newFailLimiter(3, time.Minute, func() time.Time { return now })
	for range 3 {
		if l.blocked("1.1.1.1") {
			t.Fatal("blocked too early")
		}
		l.fail("1.1.1.1")
	}
	if !l.blocked("1.1.1.1") {
		t.Fatal("expected block after limit")
	}
	if l.blocked("2.2.2.2") {
		t.Fatal("other addresses are unaffected")
	}
	now = now.Add(61 * time.Second)
	if l.blocked("1.1.1.1") {
		t.Fatal("window expired")
	}
}

func TestSupportedVersion(t *testing.T) {
	if v, ok := negotiateVersion("2025-06-18"); !ok || v != "2025-06-18" {
		t.Errorf("supported version echoed, got %q %v", v, ok)
	}
	if v, ok := negotiateVersion("1999-01-01"); ok || v != latestProtocolVersion {
		t.Errorf("unsupported version answered with the latest, got %q %v", v, ok)
	}
	if v, ok := negotiateVersion(""); ok || v != latestProtocolVersion {
		t.Errorf("empty version, got %q %v", v, ok)
	}
}
