package liveaudit

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

var webTests = []string{"src/components/PayloadExportBoundary.test.tsx", "src/components/ArtifactViewer.visualDownload.test.tsx", "src/components/useDashboardDataset.test.tsx", "src/transport/datasets.test.ts", "src/visual/datasetExpiry.test.ts"}

type tsToken struct {
	a, b int
	text string
}

// Lex only positions: quoted strings, templates and comments cannot introduce guards.
// This deliberately indexes a small fixed surface, not arbitrary TypeScript semantics.
func tsTokens(raw string) []tsToken {
	out := []tsToken{}
	for i := 0; i < len(raw); {
		a := i
		c := raw[i]
		if strings.ContainsRune(" \t\r\n", rune(c)) {
			i++
			continue
		}
		if strings.HasPrefix(raw[i:], "//") {
			j := strings.IndexByte(raw[i:], '\n')
			if j < 0 {
				break
			}
			i += j + 1
			continue
		}
		if strings.HasPrefix(raw[i:], "/*") {
			j := strings.Index(raw[i+2:], "*/")
			if j < 0 {
				break
			}
			i += j + 4
			continue
		}
		previous := ""
		if len(out) > 0 {
			previous = out[len(out)-1].text
		}
		regexContext := false
		switch previous {
		case "", "(", ",", "=", ":", "!", "[", "?", "return":
			regexContext = true
		}
		switch {
		case c == '/' && regexContext:
			i++
			class := false
			for i < len(raw) {
				if raw[i] == '\\' {
					i += 2
					continue
				}
				if raw[i] == '[' {
					class = true
				}
				if raw[i] == ']' {
					class = false
				}
				if raw[i] == '/' && !class {
					i++
					break
				}
				i++
			}
			if i > len(raw) {
				i = len(raw)
			}
		case c == '\'' || c == '"' || c == '`':
			i++
			for i < len(raw) {
				if raw[i] == '\\' {
					i += 2
					continue
				}
				if raw[i] == c {
					i++
					break
				}
				i++
			}
			if i > len(raw) {
				i = len(raw)
			}
		case identifierByte(c):
			i++
			for i < len(raw) {
				d := raw[i]
				if !identifierByte(d) && (d < '0' || d > '9') {
					break
				}
				i++
			}
		default:
			i++
		}
		out = append(out, tsToken{a, i, raw[a:i]})
	}
	return out
}
func identifierByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '$'
}
func webPredicates(file, raw string) ([]Object, error) {
	tokens := tsTokens(raw)
	guards := []Object{}
	add := func(a, b int, kind, replacement string) {
		guards = append(guards, Object{"file": file, "start": a, "end": b, "line": strings.Count(raw[:a], "\n") + 1, "expression": raw[a:b], "kind": kind, "replacement": replacement})
	}
	for i, t := range tokens {
		if t.text == "if" && i+1 < len(tokens) && tokens[i+1].text == "(" {
			depth := 1
			j := i + 2
			for ; j < len(tokens); j++ {
				if tokens[j].text == "(" {
					depth++
				}
				if tokens[j].text == ")" {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if j == len(tokens) {
				return nil, fmt.Errorf("unbalanced client condition")
			}
			if j > i+2 {
				a, b := tokens[i+2].a, tokens[j-1].b
				expression := raw[a:b]
				if !strings.Contains(file, "Viewer.tsx") || strings.Contains(expression, "payloadExportAllowed") {
					add(a, b, "predicate", "false")
				}
			}
		}
		if strings.HasSuffix(file, "/transport/datasets.ts") {
			if i+4 < len(tokens) && t.text == "rows" && tokens[i+1].text == "." && tokens[i+2].text == "clear" && tokens[i+3].text == "(" && tokens[i+4].text == ")" {
				add(t.a, tokens[i+4].b, "retention_purge", "void 0")
			}
			if i+3 < len(tokens) && t.text == "send" && tokens[i+1].text == "(" && tokens[i+2].text == "'unsubscribe_dataset'" && tokens[i+3].text == ")" {
				add(t.a, tokens[i+3].b, "demand_retirement", "void 0")
			}
		}
	}
	return guards, nil
}
func WebCatalog(root string) ([]Object, error) {
	guards := []Object{}
	for _, file := range []string{"packages/chat/src/components/useDashboardDataset.ts", "packages/chat/src/transport/datasets.ts", "packages/chat/src/components/VisualViewer.tsx", "packages/chat/src/components/ArtifactViewer.tsx"} {
		raw, e := source(root, file)
		if e != nil {
			return nil, e
		}
		g, e := webPredicates(file, raw)
		if e != nil {
			return nil, e
		}
		guards = append(guards, g...)
	}
	manual := func(file, name, expression, replacement string, last bool) error {
		raw, e := source(root, file)
		if e != nil {
			return e
		}
		a := strings.Index(raw, expression)
		if last {
			a = strings.LastIndex(raw, expression)
		}
		if a < 0 {
			return fmt.Errorf("missing source marker: %s", name)
		}
		g := guard(file, name, raw, a, a+len(expression), "explicit_boundary")
		g["replacement"] = replacement
		guards = append(guards, g)
		return nil
	}
	hook := "packages/chat/src/components/useDashboardDataset.ts"
	transport := "packages/chat/src/transport/datasets.ts"
	expiry := "packages/chat/src/visual/datasets.ts"
	type marker struct {
		file, name, expression, replacement string
		last                                bool
	}
	markers := []marker{
		{hook, "sample binding", "received?.binding === binding && received?.sessionId === context?.sessionId", "received?.sessionId === context?.sessionId", false},
		{hook, "sample session", "received?.binding === binding && received?.sessionId === context?.sessionId", "received?.binding === binding", false},
		{hook, "prior binding", "previous?.binding === binding && previous.sessionId === context.sessionId", "previous != null && previous.sessionId === context.sessionId", false},
		{hook, "prior session", "previous?.binding === binding && previous.sessionId === context.sessionId", "previous?.binding === binding", false},
		{hook, "intersection demand", "visible && document.visibilityState !== 'hidden' && !element.closest('[hidden]')", "document.visibilityState !== 'hidden' && !element.closest('[hidden]')", false},
		{hook, "document demand", "visible && document.visibilityState !== 'hidden' && !element.closest('[hidden]')", "visible && !element.closest('[hidden]')", false},
		{hook, "ancestor demand", "visible && document.visibilityState !== 'hidden' && !element.closest('[hidden]')", "visible && document.visibilityState !== 'hidden'", false},
		{hook, "MCP unavailable never restores prior payload", "sample.status === 'unavailable' && sample.rows.length === 0 && prior && data.name !== 'blerg.mcp'", "sample.status === 'unavailable' && sample.rows.length === 0 && prior", false},
		{hook, "unsupported cached rows", "unsupportedMCP || expired", "expired", false}, {hook, "expired cached rows", "unsupportedMCP || expired", "unsupportedMCP", false},
		{hook, "fresh render deadline", "mcpSampleExpiry(sample) <= Date.now()", "mcpSampleExpiry(sample) <= now", true},
		{hook, "expiry effect", "setTimeout(purge, deadline - Date.now())", "setTimeout(() => {}, deadline - Date.now())", false},
		{transport, "retired subscription latch", "alive = false", "alive = true", false},
		{transport, "new recovery subscription", "id = crypto.randomUUID()", "id = id", true},
		{transport, "recovery epoch reset", "epoch = ''", "epoch = epoch", true}, {transport, "recovery sequence reset", "sequence = 0", "sequence = sequence", true},
		{transport, "transport expiry effect", "setTimeout(expire, deadline - Date.now())", "setTimeout(() => {}, deadline - Date.now())", false},
		{expiry, "required finite deadlines", "Number.isFinite(observed) && Number.isFinite(until)", "true", false},
		{expiry, "authority deadline cap", "Math.min(until, observed + 24 * 60 * 60_000)", "observed + 24 * 60 * 60_000", false},
		{expiry, "retention deadline cap", "Math.min(until, observed + 24 * 60 * 60_000)", "until", false},
		{"packages/chat/src/components/VisualViewer.tsx", "never cache dashboard payload for later kinds", "payloadExportAllowed(visualKind) ? visual : null", "visual", false},
		{"packages/chat/src/visual/visualExport.ts", "payload kind refusal", "kind === 'mermaid' || kind === 'vega-lite'", "true", false},
		{"packages/chat/src/components/ArtifactViewer.tsx", "source-only controls", "visualKind && visualKind !== 'dashboard'", "visualKind", false},
	}
	for _, m := range markers {
		if e := manual(m.file, m.name, m.expression, m.replacement, m.last); e != nil {
			return nil, e
		}
	}
	for _, g := range guards {
		fn := str(g, "function")
		if fn == "" {
			fn = str(g, "kind")
		}
		g["id"] = "web-" + digest([]byte(str(g, "file") + ":" + fn + ":" + strconv.Itoa(num(g, "start")) + ":" + str(g, "expression")))[:12]
		raw, e := source(root, str(g, "file"))
		if e != nil {
			return nil, e
		}
		g["source_sha256"] = digest([]byte(raw))
		g["suite"] = webTests
	}
	sort.Slice(guards, func(i, j int) bool {
		a, b := guards[i], guards[j]
		if a["file"] != b["file"] {
			return str(a, "file") < str(b, "file")
		}
		if num(a, "start") != num(b, "start") {
			return num(a, "start") < num(b, "start")
		}
		return str(a, "id") < str(b, "id")
	})
	return guards, nil
}
