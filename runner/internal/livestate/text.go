package livestate

// text.go: what a running subagent is doing now, in the chat's words (packages/chat
// src/model/subagents.ts doingLine and src/model/toolGroups.ts describeInput).

import (
	"bytes"
	"encoding/json"
	"strings"
)

// preferredFields are describeInput's fields, in its order: the first with text says what a call does.
var preferredFields = []string{
	"command", "cmd", "file_path", "path", "filepath", "file", "notebook_path", "url",
	"pattern", "query", "name", "prompt", "question", "body", "description",
}

// cut keeps n runes of s and marks the cut with "…".
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// cutTo keeps s within n runes, the last of them "…" when it is cut (the chat's working line and
// its doing line cut a Bash description the same way: model/liveCalls callDetail).
func cutTo(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// field is one member of a JSON object, in the object's own order.
type field struct {
	key string
	raw json.RawMessage
}

// objectFields reads a JSON object's members in order; ok is false for anything that is not one.
func objectFields(raw json.RawMessage) ([]field, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var out []field
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return out, true
		}
		key, _ := k.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return out, true
		}
		out = append(out, field{key: key, raw: v})
	}
	return out, true
}

// asText: a string, or an array of strings joined by spaces; ok false for anything else.
func asText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		return strings.Join(list, " "), true
	}
	return "", false
}

// describe is describeInput's summary: a call's input in one line.
func describe(input json.RawMessage) string {
	var s string
	if json.Unmarshal(input, &s) == nil {
		return cut(firstLine(s), 100)
	}
	fields, ok := objectFields(input)
	if !ok {
		return ""
	}
	byKey := make(map[string]json.RawMessage, len(fields))
	for _, f := range fields {
		if _, seen := byKey[f.key]; !seen {
			byKey[f.key] = f.raw
		}
	}
	for _, k := range preferredFields {
		raw, ok := byKey[k]
		if !ok {
			continue
		}
		if text, ok := asText(raw); ok {
			if line := firstLine(text); line != "" {
				return cut(line, 100)
			}
		}
	}
	for _, f := range fields {
		if text, ok := asText(f.raw); ok {
			if line := firstLine(text); line != "" {
				return cut(line, 100)
			}
		}
	}
	return ""
}

func inputField(input json.RawMessage, name string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m[name], &s) != nil {
		return ""
	}
	return s
}

func fileName(path string) string {
	i := strings.LastIndexAny(path, `/\`)
	if name := path[i+1:]; name != "" {
		return name
	}
	return path
}

// doingLine: what a subagent is doing now — its newest tool call ("running Bash · npm test") or
// the first line of its newest message.
func doingLine(ev Event) string {
	if ev.Kind == "assistant_text" {
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		return cut(firstLine(p.Text), 100)
	}
	var call struct {
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(ev.Payload, &call)
	tool := call.Tool
	if tool == "" {
		tool = "tool"
	}
	var hint string
	switch tool {
	case "Bash":
		// What the call says it does, else the start of its command.
		if d := firstLine(inputField(call.Input, "description")); d != "" {
			hint = cutTo(d, 48)
		} else {
			hint = cut(firstLine(inputField(call.Input, "command")), 60)
		}
	case "Read", "Edit", "Write", "NotebookEdit":
		p := inputField(call.Input, "file_path")
		if p == "" {
			p = inputField(call.Input, "notebook_path")
		}
		hint = fileName(p)
	case "Agent", "Task":
		hint = inputField(call.Input, "description")
	default:
		hint = cut(describe(call.Input), 60)
	}
	if hint != "" {
		return "running " + tool + " · " + hint
	}
	return "running " + tool
}
