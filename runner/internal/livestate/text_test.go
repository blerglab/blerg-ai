package livestate

import (
	"encoding/json"
	"strings"
	"testing"
)

// The doing line is the chat's doingLine (packages/chat/src/model/subagents.ts), word for word.
func TestDoingLine(t *testing.T) {
	call := func(tool, input string) Event {
		raw, _ := json.Marshal(map[string]any{"tool": tool, "call_id": "c", "input": json.RawMessage(input)})
		return Event{Kind: "tool_call", Payload: raw}
	}
	text := func(s string) Event {
		raw, _ := json.Marshal(map[string]any{"text": s, "done": true})
		return Event{Kind: "assistant_text", Payload: raw}
	}
	long := strings.Repeat("a", 130)
	for _, tc := range []struct {
		ev   Event
		want string
	}{
		{text("\n  Looking at the tests  \nmore"), "Looking at the tests"},
		{text(long), strings.Repeat("a", 100) + "…"},
		{call("Bash", `{"command":"npm test\nnpm run lint"}`), "running Bash · npm test"},
		{call("Bash", `{"command":"`+long+`"}`), "running Bash · " + strings.Repeat("a", 60) + "…"},
		// A Bash call's description says what it does better than its command.
		{call("Bash", `{"command":"npm test","description":"Run the unit tests"}`), "running Bash · Run the unit tests"},
		{call("Bash", `{"command":"npm test","description":"`+long+`"}`), "running Bash · " + strings.Repeat("a", 47) + "…"},
		{call("Bash", `{"command":"npm test","description":"`+strings.Repeat("b", 48)+`"}`), "running Bash · " + strings.Repeat("b", 48)},
		{call("Bash", `{"command":"npm test","description":"  "}`), "running Bash · npm test"},
		{call("Read", `{"file_path":"/repo/src/App.tsx"}`), "running Read · App.tsx"},
		{call("NotebookEdit", `{"notebook_path":"C:\\n\\x.ipynb"}`), "running NotebookEdit · x.ipynb"},
		{call("Agent", `{"description":"Find the bug","prompt":"..."}`), "running Agent · Find the bug"},
		{call("Grep", `{"output_mode":"content","pattern":"TODO"}`), "running Grep · TODO"},
		// No preferred field: the first text value in the input's own order.
		{call("mcp__x__y", `{"zeta":"first","alpha":"second"}`), "running mcp__x__y · first"},
		{call("WebFetch", `{"url":["a","b"]}`), "running WebFetch · a b"},
		{call("", `{}`), "running tool"},
		{call("Glob", `"*.go"`), "running Glob · *.go"},
		{call("Glob", `[1,2]`), "running Glob"},
		{Event{Kind: "tool_call", Payload: json.RawMessage(`null`)}, "running tool"},
	} {
		if got := doingLine(tc.ev); got != tc.want {
			t.Errorf("doingLine(%s) = %q, want %q", tc.ev.Payload, got, tc.want)
		}
	}
}

func TestTitleOf(t *testing.T) {
	for in, want := range map[string]string{
		"Implement task 3":       "Implement task 3",
		"\n  first line\nsecond": "first line",
		"":                       "Subagent · started earlier",
		"   ":                    "Subagent · started earlier",
	} {
		if got := titleOf(in); got != want {
			t.Errorf("titleOf(%q) = %q, want %q", in, got, want)
		}
	}
}
