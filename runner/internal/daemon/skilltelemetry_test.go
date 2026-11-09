package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/skills"
)

func TestSkillLoadObservation(t *testing.T) {
	o := newSkillObserver(t.Context(), "session", "codex", skills.Resolve(true))
	pack, err := prepareSkillPack(sandboxExec{}, skills.Resolve(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pack.remove)
	o.pack = pack
	result := func(output string, failed bool, parent string) []agent.Event {
		return o.observe(agent.Event{Kind: "tool_result", Ts: time.Now(), Payload: agent.ToolResultPayload{CallID: "read", Output: output, IsError: failed, ParentCallID: parent}})
	}
	if len(result("truncated", false, "")) != 0 || len(result(skills.UXDesign, true, "")) != 0 {
		t.Fatal("inferred load without evidence")
	}
	// A body alone has no trusted provenance.
	if len(result(skills.UXDesign, false, "")) != 0 {
		t.Fatal("unattributed body credited")
	}
	o.observe(agent.Event{Kind: "tool_call", Payload: agent.ToolCallPayload{CallID: "read", Tool: "Skill", Input: json.RawMessage(`{"skill":"blerg:ux-design"}`)}})
	events := result(skills.UXDesign, false, "native-worker")
	if len(events) != 1 {
		t.Fatal("missing full body load")
	}
	p := events[0].Payload.(skills.Load)
	if p.Role != "subagent" || p.Bytes != len(skills.UXDesign) || p.Mode != "on_demand" {
		t.Fatalf("bad attribution: %+v", p)
	}
	o.observe(agent.Event{Kind: "tool_call", Payload: agent.ToolCallPayload{CallID: "read", Tool: "Skill", Input: json.RawMessage(`{"skill":"ux-design"}`)}})
	if len(result("loaded", false, "")) != 0 {
		t.Fatal("repository skill attributed to Blerg")
	}
	if len(result("loaded", false, "")) != 0 {
		t.Fatal("duplicate result counted twice")
	}
}

func TestSkillPrivateReadNumberingAndMismatch(t *testing.T) {
	pack, err := prepareSkillPack(sandboxExec{}, skills.Resolve(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pack.remove)
	o := newSkillObserver(t.Context(), "session", "codex", skills.Resolve(false))
	o.pack = pack
	input, _ := json.Marshal(map[string]string{"file_path": pack.path("ux-design")})
	read := func(output string) []agent.Event {
		o.observe(agent.Event{Kind: "tool_call", Payload: agent.ToolCallPayload{CallID: "read", Tool: "Read", Input: input}})
		return o.observe(agent.Event{Kind: "tool_result", Payload: agent.ToolResultPayload{CallID: "read", Output: output}})
	}
	var numbered strings.Builder
	for i, line := range strings.Split(skills.UXDesign, "\n") {
		fmt.Fprintf(&numbered, "%d\t%s\n", i+1, line)
	}
	events := read(numbered.String())
	if len(events) != 1 || events[0].Kind != "skill_load" {
		t.Fatalf("numbered read: %+v", events)
	}
	wrapped, _ := json.Marshal(map[string]string{"content": strings.ReplaceAll(numbered.String(), "\t", "|")})
	events = read(string(wrapped))
	if len(events) != 1 || events[0].Kind != "skill_load" {
		t.Fatal("Hermes JSON file result not recognized")
	}
	events = read("1|---\n2|name: ux-design\n[truncated]")
	if len(events) != 1 || events[0].Kind != "skill_reporting" || events[0].Payload.(skills.Load).Basis != "partial_native_observation" || events[0].Payload.(skills.Load).Bytes != 0 {
		t.Fatal("partial output credited or reported as tampering")
	}
	tampered := strings.Replace(skills.UXDesign, "UX design", "Malicious replacement", 1)
	if err := os.WriteFile(pack.path("ux-design"), []byte(tampered), 0600); err != nil {
		t.Fatal(err)
	}
	events = read(tampered)
	if len(events) != 1 || events[0].Kind != "skill_reporting" {
		t.Fatalf("tampering credited: %+v", events)
	}
	load := events[0].Payload.(skills.Load)
	if load.Basis != "content_mismatch" || load.Version != fmt.Sprintf("%x", sha256.Sum256([]byte(tampered))) {
		t.Fatal("mismatch claimed embedded version")
	}
	o.observe(agent.Event{Kind: "tool_call", Payload: agent.ToolCallPayload{CallID: "native", Tool: "Skill", Input: json.RawMessage(`{"skill":"blerg:ux-design"}`)}})
	events = o.observe(agent.Event{Kind: "tool_result", Payload: agent.ToolResultPayload{CallID: "native", Output: "Launching skill: blerg:ux-design"}})
	if len(events) != 1 || events[0].Kind != "skill_reporting" || events[0].Payload.(skills.Load).Version != load.Version {
		t.Fatal("native tool credited tampered pack")
	}
}

func TestSkillApplicabilityNeedsSuccessfulEdit(t *testing.T) {
	o := newSkillObserver(t.Context(), "session", "claude", skills.Resolve(false))
	call := agent.Event{Kind: "tool_call", Payload: agent.ToolCallPayload{CallID: "edit", Tool: "Edit", Input: json.RawMessage(`{"file_path":"src/Toolbar.tsx"}`)}}
	o.observe(call)
	if events := o.observe(agent.Event{Kind: "tool_result", Payload: agent.ToolResultPayload{CallID: "edit", IsError: true}}); len(events) != 0 {
		t.Fatal("failed edit establishes applicability")
	}
	o.observe(call)
	if events := o.observe(agent.Event{Kind: "tool_result", Payload: agent.ToolResultPayload{CallID: "edit"}}); len(events) != 2 {
		t.Fatal("successful UI edit must establish UX and testing applicability")
	}
}

func TestSkillBatchedPrivateRead(t *testing.T) {
	pack, err := prepareSkillPack(sandboxExec{}, skills.Resolve(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pack.remove)
	o := newSkillObserver(t.Context(), "session", "codex", skills.Resolve(false))
	o.pack = pack
	input, _ := json.Marshal(map[string]string{"cmd": "cat " + pack.path("managing-tickets") + " " + pack.path("ux-design")})
	o.observe(agent.Event{Kind: "tool_call", Payload: agent.ToolCallPayload{CallID: "batch", Tool: "exec_command", Input: input}})
	events := o.observe(agent.Event{Kind: "tool_result", Payload: agent.ToolResultPayload{CallID: "batch", Output: skills.ManagingTickets + "\n" + skills.UXDesign}})
	if len(events) != 2 {
		t.Fatalf("batched read lost a load: %d", len(events))
	}
	for _, ev := range events {
		if ev.Kind != "skill_load" {
			t.Fatal("complete batched load not verified")
		}
	}
}

func TestCodexAcceptedSkillInputRequiresExactPackPath(t *testing.T) {
	pack, err := prepareSkillPack(sandboxExec{}, skills.Resolve(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pack.remove)
	emitter := &collectEmitter{}
	driver := &codexDriver{emitter: emitter, skillPack: pack}
	driver.recordAcceptedSkillInputs([]any{map[string]any{"type": "skill", "name": "ux-design", "path": "/project/skills/ux-design/SKILL.md"}})
	if len(emitter.snapshot()) != 0 {
		t.Fatal("project skill attributed to Blerg")
	}
	app := &codexApp{d: driver}
	inputs := app.skillInput("/blerg:ux-design Check toolbar")
	if len(inputs) != 2 || !strings.Contains(fmt.Sprint(inputs[0]), skills.UXDesign) {
		t.Fatal("explicit Blerg body missing from context")
	}
	driver.recordAcceptedSkillInputs(inputs)
	events := emitter.snapshot()
	if len(events) != 1 || events[0].Kind != "skill_load" {
		t.Fatal("accepted native input not recorded")
	}
	if err := os.WriteFile(pack.path("ux-design"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	inputs = app.skillInput("/blerg:ux-design inspect")
	driver.recordAcceptedSkillInputs(inputs)
	events = emitter.snapshot()
	if len(events) != 2 || events[1].Kind != "skill_reporting" {
		t.Fatal("Codex loaded a replacement")
	}
}

func TestSkillApplicabilityNativeAdapterShapes(t *testing.T) {
	for _, input := range []string{`[{"path":"src/Toolbar.tsx","kind":{"type":"update"}}]`, `{"patch":"*** Begin Patch\n*** Update File: src/Toolbar.tsx\n@@\n-old\n+new\n*** End Patch"}`} {
		if got := skillsForDirectEdit(json.RawMessage(input)); len(got) != 2 {
			t.Fatalf("native edit shape was not recognized: %v", got)
		}
	}
	if got := skillsForDirectEdit(json.RawMessage(`{"command":"touch src/Toolbar.tsx"}`)); len(got) != 0 {
		t.Fatal("inferred an edit from a command")
	}
}

func TestSkillCatalogDelegationIsNotRead(t *testing.T) {
	for _, tool := range []string{"Agent", "Task", "write_file", "unknown"} {
		t.Run(tool, func(t *testing.T) {
			o := newSkillObserver(t.Context(), "session", "claude", skills.Resolve(true))
			pack, err := prepareSkillPack(sandboxExec{}, skills.Resolve(true))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pack.remove)
			o.pack = pack
			input, _ := json.Marshal(map[string]string{"prompt": pack.guide()})
			o.observe(agent.Event{Kind: "tool_call", Payload: agent.ToolCallPayload{CallID: "delegate", Tool: tool, Input: input}})
			events := o.observe(agent.Event{Kind: "tool_result", Payload: agent.ToolResultPayload{CallID: "delegate", Output: "done"}})
			if len(events) != 0 {
				t.Fatalf("catalog forwarding recorded reads: %+v", events)
			}
		})
	}
}

func TestSkillPackCleanupLogsSafeBoundedCause(t *testing.T) {
	var out bytes.Buffer
	old, oldRun := failurelog.Default, dockerRun
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&out, nil)))
	dockerRun = func(...string) error { return fmt.Errorf("canary-secret-private-path") }
	t.Cleanup(func() { failurelog.Default = old; dockerRun = oldRun })
	pack := &skillPack{dir: "private", prefix: sandboxExec{}}
	// A file used as a directory makes removal fail deterministically on hosts.
	file := t.TempDir() + "/canary-secret-file"
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	pack.dir = file + "/nested"
	pack.remove()
	pack.remove()
	text := out.String()
	if strings.Contains(text, "canary-secret") || strings.Count(text, `"suppressed":0`) != 1 || !strings.Contains(text, `"cause":`) {
		t.Fatalf("cleanup diagnostic: %s", text)
	}
	out.Reset()
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&out, nil)))
	pack.prefix = sandboxExec{"docker", "exec", "private-container"}
	pack.remove()
	pack.remove()
	text = out.String()
	if strings.Contains(text, "canary-secret") || strings.Contains(text, "private-container") || strings.Count(text, `"suppressed":0`) != 1 || !strings.Contains(text, `"cause":`) {
		t.Fatalf("container cleanup diagnostic: %s", text)
	}
}

// A linked examples file is recognized as part of the skill it ships beside,
// verified against its own trusted hash rather than the SKILL.md one (BLG-461).
func TestSkillExamplesReadRecognizedAgainstTrustedHash(t *testing.T) {
	policy := skills.Resolve(true)
	pack, err := prepareSkillPack(sandboxExec{}, policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pack.remove)
	var coordinating skills.Entry
	for _, e := range pack.entries {
		if e.Name == "coordinating" {
			coordinating = e
		}
	}
	if coordinating.Examples == "" {
		t.Fatal("coordinating skill is missing its shipped examples.md")
	}
	o := newSkillObserver(t.Context(), "session", "codex", policy)
	o.pack = pack
	input, _ := json.Marshal(map[string]string{"file_path": pack.examplesPath("coordinating")})
	o.observe(agent.Event{Kind: "tool_call", Payload: agent.ToolCallPayload{CallID: "read", Tool: "Read", Input: input}})
	events := o.observe(agent.Event{Kind: "tool_result", Payload: agent.ToolResultPayload{CallID: "read", Output: coordinating.Examples}})
	if len(events) != 1 || events[0].Kind != "skill_load" {
		t.Fatalf("examples read not recognized: %+v", events)
	}
	p := events[0].Payload.(skills.Load)
	if p.Skill != "coordinating" || p.File != "examples.md" || p.Version != coordinating.ExamplesVersion() || p.Bytes != len(coordinating.Examples) {
		t.Fatalf("bad examples attribution: %+v", p)
	}
	if p.Version == coordinating.Version() {
		t.Fatal("examples read must verify against its own hash, not SKILL.md's")
	}

	// A body that does not match the shipped examples file is reported, never
	// credited as a verified load.
	o2 := newSkillObserver(t.Context(), "session", "codex", policy)
	o2.pack = pack
	o2.observe(agent.Event{Kind: "tool_call", Payload: agent.ToolCallPayload{CallID: "read", Tool: "Read", Input: input}})
	events = o2.observe(agent.Event{Kind: "tool_result", Payload: agent.ToolResultPayload{CallID: "read", Output: "tampered"}})
	if len(events) != 1 || events[0].Kind != "skill_reporting" {
		t.Fatalf("tampered examples body was credited: %+v", events)
	}
}
