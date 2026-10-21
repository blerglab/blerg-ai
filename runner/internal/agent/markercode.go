package agent

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// codeReplacement stands in for the session's marker code wherever it is scrubbed.
const codeReplacement = "‹code›"

// ScrubCode replaces every occurrence of the session's marker code in text with "‹code›". The
// match is made on a normalized view of the text, so the forms a person could retype from a
// leaked code are caught too: NFKC (fullwidth or other compatibility digits and letters), any
// case, and whitespace or invisible format characters (zero-width spaces, joiners, soft hyphens)
// between the code's characters, including a line break. The separators inside a match go with it.
func ScrubCode(text, nonce string) string {
	text = scrubTemplateCodes(text)
	if nonce == "" || len(text) < len(nonce) {
		return text
	}
	target := []rune(strings.ToLower(nonce))
	// skeleton is text without separators, normalized; spans maps each of its runes back to the
	// bytes of the original rune it came from.
	type span struct{ start, end int }
	skeleton := make([]rune, 0, len(text))
	spans := make([]span, 0, len(text))
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch {
		case codeSeparator(r):
		case r < utf8.RuneSelf:
			skeleton = append(skeleton, unicode.ToLower(r))
			spans = append(spans, span{i, i + size})
		default:
			for _, n := range strings.ToLower(norm.NFKC.String(string(r))) {
				if codeSeparator(n) {
					continue
				}
				skeleton = append(skeleton, n)
				spans = append(spans, span{i, i + size})
			}
		}
		i += size
	}
	var b strings.Builder
	last := 0
	for k := 0; k+len(target) <= len(skeleton); {
		if !runesEqual(skeleton[k:k+len(target)], target) {
			k++
			continue
		}
		start, end := spans[k].start, spans[k+len(target)-1].end
		if start < last { // one rune expanded into several and the match began inside it
			k++
			continue
		}
		b.WriteString(text[last:start])
		b.WriteString(codeReplacement)
		last = end
		k += len(target)
		for k < len(skeleton) && spans[k].start < last {
			k++
		}
	}
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

// codeSeparator reports a character ScrubCode skips between the code's characters: whitespace,
// invisible format characters (Cf), combining and enclosing marks (Mn, Me: variation selectors
// among them) and the blank lookalikes that render as nothing (Hangul fillers, the halfwidth
// Hangul filler, the braille blank). Visible changes (hyphens, dots, a reversed code) are not
// skipped: the model is told to accept exactly the code.
func codeSeparator(r rune) bool {
	switch r {
	case '\u3164', '\u115f', '\u1160', '\uffa0', '\u2800':
		return true
	}
	return unicode.IsSpace(r) || unicode.In(r, unicode.Cf, unicode.Mn, unicode.Me)
}

// templateCodeRE finds a 16-hex value in the two fixed shapes that carry a marker code, the relay
// header and the system prompt's announcement, whatever session it belongs to.
var templateCodeRE = regexp.MustCompile(`(?i)(blerg\s+system\s+message:\s*[a-z_]+\s*·\s*|this\s+session's\s+code\s+is\s+)[0-9a-f]{16}\b`)

// scrubTemplateCodes replaces the code in those shapes for any session: an agent that reads
// another session's transcript (its own engine history on disk) must not carry that session's
// code into this one's events.
func scrubTemplateCodes(text string) string {
	if !strings.Contains(strings.ToLower(text), "code is") && !strings.Contains(text, "·") {
		return text
	}
	return templateCodeRE.ReplaceAllString(text, "${1}"+codeReplacement)
}

func runesEqual(a, b []rune) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ScrubCodeJSON is ScrubCode for an event payload: every string in the JSON value (keys included)
// is scrubbed. The payload comes back unchanged, byte for byte, when there is nothing to scrub.
func ScrubCodeJSON(payload []byte, nonce string) []byte {
	if len(payload) < 16 {
		return payload
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return payload
	}
	v, changed := scrubValue(v, nonce)
	if !changed {
		return payload
	}
	out, err := json.Marshal(v)
	if err != nil {
		return payload
	}
	return out
}

func scrubValue(v any, nonce string) (any, bool) {
	switch t := v.(type) {
	case string:
		s := ScrubCode(t, nonce)
		return s, s != t
	case []any:
		changed := false
		for i, e := range t {
			var c bool
			t[i], c = scrubValue(e, nonce)
			changed = changed || c
		}
		return t, changed
	case map[string]any:
		changed := false
		out := make(map[string]any, len(t))
		for k, e := range t {
			sk := ScrubCode(k, nonce)
			se, c := scrubValue(e, nonce)
			out[sk] = se
			changed = changed || c || sk != k
		}
		return out, changed
	}
	return v, false
}
