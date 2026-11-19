package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/skills"
)

type skillObserver struct {
	ctx                   context.Context
	edits                 map[string][]string
	mu                    sync.Mutex
	session, engine, role string
	entries               []skills.Entry
	calls                 map[string][]string
	exampleCalls          map[string][]string
	pack                  *skillPack
	native                map[string]bool
	initialPath           string
	initialDone, compact  bool
	deliveryAt            time.Time
	initialAcks           map[string]bool
}

func newSkillObserver(ctx context.Context, session, engine string, policy skills.Policy) *skillObserver {
	if engine == "" {
		engine = "claude"
	}
	role := "session"
	if policy.Coordinating {
		role = "Main"
	}
	return &skillObserver{ctx: ctx, session: session, engine: engine, role: role, entries: skills.Inventory(policy), calls: map[string][]string{}, exampleCalls: map[string][]string{}, native: map[string]bool{}, edits: map[string][]string{}}
}
func (o *skillObserver) record(entry skills.Entry, mode, basis, parent string, at time.Time) skills.Load {
	role := o.role
	if parent != "" {
		role = "subagent"
	}
	return skills.Load{SessionID: o.session, ParentCallID: parent, Role: role, Engine: o.engine, Skill: entry.Name, Version: entry.Version(), Mode: mode, Bytes: len(entry.Body), Basis: basis, At: at}
}

func skillReadTool(name string) bool {
	switch strings.ToLower(name) {
	case "read", "read_file", "file_read", "bash", "shell", "exec_command", "terminal":
		return true
	}
	return false
}

// Observe namespaced native loads or complete private-file reads, verifying the
// pack hash before attribution. Paths and output never enter derived records.
// Hidden or partial reads remain coverage gaps.
func (o *skillObserver) observe(ev agent.Event) []agent.Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch ev.Kind {
	case "tool_call":
		p, ok := decodeSkillPayload[agent.ToolCallPayload](ev.Payload)
		if !ok {
			return nil
		}
		switch strings.ToLower(p.Tool) {
		case "edit", "write", "write_file", "edit_file", "file_edit", "file_write", "apply_patch":
			if len(o.edits) < 4096 {
				o.edits[p.CallID] = skillsForDirectEdit(p.Input)
			}
		}
		if o.pack != nil && skillReadTool(p.Tool) && len(o.calls) < 4096 {
			for _, e := range o.entries {
				// File readers and shell tools expose different argument shapes.
				// Only an exact private path can establish Blerg provenance.
				if strings.Contains(string(p.Input), o.pack.path(e.Name)) {
					o.calls[p.CallID] = append(o.calls[p.CallID], e.Name)
				}
				if e.Examples != "" && strings.Contains(string(p.Input), o.pack.examplesPath(e.Name)) && len(o.exampleCalls) < 4096 {
					o.exampleCalls[p.CallID] = append(o.exampleCalls[p.CallID], e.Name)
				}
			}
		}
		if o.pack != nil && strings.EqualFold(p.Tool, "skill") {
			var in struct {
				Skill string `json:"skill"`
				Name  string `json:"name"`
			}
			if json.Unmarshal(p.Input, &in) == nil {
				name := in.Skill
				if name == "" {
					name = in.Name
				}
				for _, e := range o.entries {
					if name == "blerg:"+e.Name {
						if len(o.calls) < 4096 {
							o.calls[p.CallID] = []string{e.Name}
							o.native[p.CallID] = true
						}
					}
				}
			}
		}
	case "tool_result":
		p, ok := decodeSkillPayload[agent.ToolResultPayload](ev.Payload)
		if !ok {
			return nil
		}
		name := o.calls[p.CallID]
		exampleName := o.exampleCalls[p.CallID]
		native := o.native[p.CallID]
		delete(o.native, p.CallID)
		delete(o.calls, p.CallID)
		delete(o.exampleCalls, p.CallID)
		edits, directEdit := o.edits[p.CallID]
		delete(o.edits, p.CallID)
		if p.IsError {
			return nil
		}
		if directEdit && len(edits) == 0 {
			edits = skillsForDirectEdit(json.RawMessage(p.Output))
		}
		var out []agent.Event
		for _, skill := range edits {
			out = append(out, agent.Event{ClientEventID: ccUUID(), Ts: ev.Ts, Kind: "skill_applicability", Payload: skills.Applicability{SessionID: o.session, Skill: skill, ParentCallID: p.ParentCallID, Basis: "successful_direct_edit", At: ev.Ts}})
		}

		for _, e := range o.entries {
			if !slices.Contains(name, e.Name) {
				continue
			}
			load := o.record(e, "on_demand", "complete_body_observed", p.ParentCallID, ev.Ts)
			kind := "skill_load"
			// Numbered file readers preserve the body while adding a line prefix.
			body := p.Output
			// Hermes returns JSON containing numbered content, while Claude and
			// Codex return display text. Decode the documented file-content field.
			var file struct {
				Content string `json:"content"`
			}
			if json.Unmarshal([]byte(body), &file) == nil && file.Content != "" {
				body = file.Content
			}
			body = numberedSkillLines.ReplaceAllString(body, "")
			if o.pack != nil {
				actual, err := o.pack.read(o.ctx, e.Name)
				if err != nil || string(actual) != e.Body {
					failurelog.Default.Event("skill_content_verification", slog.LevelWarn, "skill", e.Name, "cause", failurelog.Cause(err))
					load.Basis = "content_mismatch"
					load.Version = fmt.Sprintf("%x", sha256.Sum256(actual))
					load.Bytes = len(actual)
					if err != nil {
						load.Basis = "file_unavailable"
						load.Version = e.Version()
						load.Bytes = 0
					}
					out = append(out, agent.Event{ClientEventID: ccUUID(), Ts: ev.Ts, Kind: "skill_reporting", Payload: load})
					continue
				}
			}
			if native && o.pack != nil {
				// Claude's native tool returns an acknowledgement, not its injected
				// body. Namespace plus the verified private plugin bytes identify it.
				load.Basis = "native_skill_success"
			} else if !strings.Contains(body, e.Body) && !strings.Contains(p.Output, e.Body) {
				kind = "skill_reporting"
				// The file hash is sound but the tool did not expose a complete
				// body. Do not label truncation as a replacement skill version.
				load.Basis = "partial_native_observation"
				load.Bytes = 0
			}
			out = append(out, agent.Event{ClientEventID: ccUUID(), Ts: ev.Ts, Kind: kind, Payload: load})
		}

		for _, e := range o.entries {
			if e.Examples == "" || !slices.Contains(exampleName, e.Name) {
				continue
			}
			load := o.record(e, "on_demand", "examples_body_observed", p.ParentCallID, ev.Ts)
			load.File = "examples.md"
			load.Version = e.ExamplesVersion()
			load.Bytes = len(e.Examples)
			kind := "skill_load"
			body := p.Output
			var file struct {
				Content string `json:"content"`
			}
			if json.Unmarshal([]byte(body), &file) == nil && file.Content != "" {
				body = file.Content
			}
			body = numberedSkillLines.ReplaceAllString(body, "")
			if o.pack != nil {
				actual, err := o.pack.readExamples(o.ctx, e.Name)
				if err != nil || string(actual) != e.Examples {
					failurelog.Default.Event("skill_content_verification", slog.LevelWarn, "skill", e.Name, "cause", failurelog.Cause(err))
					load.Basis = "content_mismatch"
					load.Version = fmt.Sprintf("%x", sha256.Sum256(actual))
					load.Bytes = len(actual)
					if err != nil {
						load.Basis = "file_unavailable"
						load.Version = e.ExamplesVersion()
						load.Bytes = 0
					}
					out = append(out, agent.Event{ClientEventID: ccUUID(), Ts: ev.Ts, Kind: "skill_reporting", Payload: load})
					continue
				}
			}
			if !strings.Contains(body, e.Examples) && !strings.Contains(p.Output, e.Examples) {
				kind = "skill_reporting"
				load.Basis = "partial_native_observation"
				load.Bytes = 0
			}
			out = append(out, agent.Event{ClientEventID: ccUUID(), Ts: ev.Ts, Kind: kind, Payload: load})
		}
		return out
	}
	return nil
}

var numberedSkillLines = regexp.MustCompile(`(?m)^\s*[0-9]+(?:\t|→|\||: )`)

// A successful completed turn proves the initial instructions were delivered.
// Failed startup, interrupted work and retries do not create an initial load.
// The receipt survives driver and daemon replacement, unlike an observer's memory.
func (o *skillObserver) delivered(ev agent.Event, emit agent.Emitter) {
	if ev.Kind == "status_changed" {
		p, ok := decodeSkillPayload[agent.StatusPayload](ev.Payload)
		if ok && p.Status == "running" {
			o.mu.Lock()
			if o.deliveryAt.IsZero() {
				o.deliveryAt = ev.Ts
				if o.deliveryAt.IsZero() {
					o.deliveryAt = time.Now()
				}
			}
			o.mu.Unlock()
		}
		return
	}
	if ev.Kind != "turn_done" {
		return
	}
	p, ok := decodeSkillPayload[agent.TurnDonePayload](ev.Payload)
	if !ok || (p.StopReason != "end_turn" && p.StopReason != "max_tokens") {
		o.mu.Lock()
		o.deliveryAt = time.Time{}
		o.mu.Unlock()
		return
	}
	at, claimed := o.claimInitial()
	if !claimed {
		return
	}
	o.initial(o.compact, emit, at)

}

// Mark the durable receipt only after every initial event is acknowledged by
// its consumer. Lost acks may replay stable IDs; the server stores each once.
func (o *skillObserver) ackInitial(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	matched := false
	for _, e := range o.entries {
		if id == o.initialEventID(e.Name) {
			matched = true
			break
		}
	}
	if !matched || o.initialPath == "" {
		return
	}
	if o.initialAcks == nil {
		o.initialAcks = map[string]bool{}
	}
	o.initialAcks[id] = true
	if len(o.initialAcks) == len(o.entries) {
		err := os.MkdirAll(filepath.Dir(o.initialPath), 0700)
		if err == nil {
			err = os.WriteFile(o.initialPath, []byte("delivered\n"), 0600)
		}
		if err != nil {
			failurelog.Default.Event("skill_initial_receipt", slog.LevelWarn, "cause", failurelog.Cause(err))
		}
	}
}

func (o *skillObserver) claimInitial() (time.Time, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.initialDone {
		return time.Time{}, false
	}
	if o.initialPath != "" {
		if _, err := os.Stat(o.initialPath); err == nil {
			o.initialDone = true
			return time.Time{}, false
		} else if !os.IsNotExist(err) {
			failurelog.Default.Event("skill_initial_receipt", slog.LevelWarn, "cause", failurelog.Cause(err))
			return time.Time{}, false
		}
	}
	o.initialDone = true
	return o.deliveryAt, true
}

func (o *skillObserver) initial(compact bool, emit agent.Emitter, deliveredAt ...time.Time) {
	now := time.Now()
	if len(deliveredAt) > 0 && !deliveredAt[0].IsZero() {
		now = deliveredAt[0]
	}
	for _, e := range o.entries {
		p := o.record(e, "on_demand", "partial_native_observation", "", now)
		if compact {
			p.Mode = "always"
			p.Basis = "compact_fallback"
			compactText := e.Description + ": " + e.Compact
			p.Bytes = len(compactText)
			p.Version = fmt.Sprintf("%x", sha256.Sum256([]byte(compactText)))
			emit.Emit(agent.Event{ClientEventID: o.initialEventID(e.Name), Ts: now, Kind: "skill_load", Payload: p})
		} else {
			emit.Emit(agent.Event{ClientEventID: o.initialEventID(e.Name), Ts: now, Kind: "skill_reporting", Payload: p})
		}
	}
}

func (o *skillObserver) initialEventID(name string) string {
	b := sha256.Sum256([]byte("blerg-skill-initial\x00" + o.session + "\x00" + name))
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func decodeSkillPayload[T any](value any) (T, bool) {
	if p, ok := value.(T); ok {
		return p, true
	}
	var p T
	raw, err := json.Marshal(value)
	if err != nil {
		return p, false
	}
	return p, json.Unmarshal(raw, &p) == nil
}

// Native adapters use both a single file object and Codex's file-change array.
// Read only explicit file metadata; never guess that a shell command edited code.
func skillsForDirectEdit(raw json.RawMessage) []string {
	type file struct {
		Path     string
		FilePath string `json:"file_path"`
		Patch    string
	}
	var files []file
	var single file
	if json.Unmarshal(raw, &single) == nil {
		files = append(files, single)
	} else {
		_ = json.Unmarshal(raw, &files)
	}
	code, ui := false, false
	inspect := func(path string) {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".tsx", ".jsx", ".css", ".scss", ".sass", ".less", ".html", ".vue", ".svelte", ".svg":
			code = true
			ui = true
		case ".go", ".ts", ".js", ".mjs", ".cjs", ".rs", ".py", ".java", ".c", ".cpp", ".h":
			code = true
		}
	}
	for _, f := range files {
		inspect(f.Path)
		inspect(f.FilePath)
		for _, line := range strings.Split(f.Patch, "\n") {
			for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: "} {
				if path, ok := strings.CutPrefix(line, prefix); ok {
					inspect(path)
				}
			}
		}
	}
	out := []string{}
	if code {
		out = append(out, "test-driven-development")
	}
	if ui {
		out = append(out, "ux-design")
	}
	return out
}
