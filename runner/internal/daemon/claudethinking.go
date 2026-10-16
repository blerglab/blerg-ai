package daemon

// claudethinking.go: when the model is thinking. With --include-partial-messages (claudeflags.go)
// the CLI writes the API's stream events as stream_event lines beside its whole assistant lines.
// The driver reads only a thinking block's start and stop from them and says each as a transient
// thinking_started / thinking_done (agent.ThinkingPayload): the thinking text still comes, once,
// in the assistant line's assistant_thinking. Every other stream event is ignored.

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

var (
	ccStreamEventType = []byte(`"type":"stream_event"`)
	ccStreamEventKeep = [][]byte{[]byte("content_block_start"), []byte("content_block_stop"), []byte("message_start")}
)

// ccSkipStreamLine reports whether a line is a stream_event the driver has no use for: a delta, a
// message_delta or a message_stop, by far the most of them (one per few tokens of output). Such a
// line is dropped before it is decoded or anything is locked. A line that merely mentions one of the
// kept event types (in its text) is decoded and found out then.
func ccSkipStreamLine(line []byte) bool {
	if !bytes.Contains(line, ccStreamEventType) {
		return false
	}
	for _, k := range ccStreamEventKeep {
		if bytes.Contains(line, k) {
			return false
		}
	}
	return true
}

// forgetThinking drops what a subagent left open when it ends (its Agent call's id).
func (d *claudeCodeDriver) forgetThinking(parent string) {
	if parent == "" {
		return
	}
	prefix := parent + "#"
	d.sm.Lock()
	for k := range d.thinking {
		if strings.HasPrefix(k, prefix) {
			delete(d.thinking, k)
		}
	}
	d.sm.Unlock()
}

// ccStreamEvent is the subset of a stream_event line's event the driver reads.
type ccStreamEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
	} `json:"content_block"`
}

// onStreamEvent handles one stream_event line: a thinking block that opens is thinking_started, its
// stop thinking_done. Blocks are told apart by their index within one message of one agent (the
// main agent, or the subagent the line's parent_tool_use_id names); a new message of that agent
// forgets what an earlier one left open. It returns the kind it emitted ("" when none).
func (d *claudeCodeDriver) onStreamEvent(ev *ccLine) string {
	var se ccStreamEvent
	if len(ev.Event) == 0 || json.Unmarshal(ev.Event, &se) != nil {
		return ""
	}
	agentKey := ev.ParentToolUseID + "#"
	key := agentKey + strconv.Itoa(se.Index)
	var kind string
	d.sm.Lock()
	switch se.Type {
	case "message_start":
		for k := range d.thinking {
			if strings.HasPrefix(k, agentKey) {
				delete(d.thinking, k)
			}
		}
	case "content_block_start":
		if se.ContentBlock.Type == "thinking" || se.ContentBlock.Type == "redacted_thinking" {
			if d.thinking == nil {
				d.thinking = map[string]bool{}
			}
			d.thinking[key] = true
			kind = agent.KindThinkingStarted
		}
	case "content_block_stop":
		if d.thinking[key] {
			delete(d.thinking, key)
			kind = agent.KindThinkingDone
		}
	}
	d.sm.Unlock()
	if kind != "" {
		d.emit(kind, agent.ThinkingPayload{ParentCallID: d.subagentParent(ev.ParentToolUseID)})
	}
	return kind
}
