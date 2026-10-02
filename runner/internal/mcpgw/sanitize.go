package mcpgw

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// outBlock is the only content block the gateway returns to a session.
type outBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type outResult struct {
	Content []outBlock `json:"content"`
	IsError bool       `json:"isError,omitempty"`
}

func errorResult(text string) json.RawMessage {
	raw, _ := json.Marshal(outResult{Content: []outBlock{{Type: "text", Text: text}}, IsError: true})
	return raw
}

func textResult(text string) json.RawMessage {
	raw, _ := json.Marshal(outResult{Content: []outBlock{{Type: "text", Text: text}}})
	return raw
}

// sanitizeResult turns an upstream tools/call result into text only: text blocks are kept,
// image, audio, embedded-resource and resource-link blocks (and anything unknown) become a
// short placeholder, structuredContent and _meta are dropped, and the total text is capped
// at maxBytes. Upstream output is untrusted content; nothing else reaches the session.
func sanitizeResult(raw json.RawMessage, maxBytes int) json.RawMessage {
	var in struct {
		Content []json.RawMessage `json:"content"`
		IsError bool              `json:"isError"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return errorResult("the upstream server returned an unreadable result")
	}
	out := outResult{IsError: in.IsError, Content: []outBlock{}}
	remaining := maxBytes
	truncated := false
	for _, b := range in.Content {
		var blk struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(b, &blk); err != nil {
			blk.Type = ""
		}
		text := stripNUL(blk.Text)
		switch blk.Type {
		case "text":
		case "image":
			text = "[image omitted]"
		case "audio":
			text = "[audio omitted]"
		case "resource":
			text = "[embedded resource omitted]"
		case "resource_link":
			text = "[resource link omitted]"
		default:
			text = "[unsupported content omitted]"
		}
		if len(text) > remaining {
			text = cutUTF8(text, remaining)
			truncated = true
		}
		remaining -= len(text)
		out.Content = append(out.Content, outBlock{Type: "text", Text: text})
		if truncated {
			break
		}
	}
	if truncated {
		out.Content = append(out.Content, outBlock{Type: "text",
			Text: fmt.Sprintf("[result truncated: it exceeded %d bytes]", maxBytes)})
	}
	if len(out.Content) == 0 {
		out.Content = append(out.Content, outBlock{Type: "text", Text: ""})
	}
	res, _ := json.Marshal(out)
	return res
}

// stripNUL removes NUL characters: PostgreSQL's jsonb rejects \u0000, so a result carrying one
// could not be stored, and the approval's outcome would go unrecorded.
func stripNUL(s string) string {
	if strings.IndexByte(s, 0) < 0 {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}

// cutUTF8 shortens s to at most n bytes without splitting a rune.
func cutUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// capRunes shortens s to at most n runes.
func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.ToValidUTF8(string([]rune(s)[:n]), "")
}
