// Package activity derives bounded time accounting from recorded events.
// Classification is static and never executes or returns recorded commands.
package activity

import (
	"bytes"
	"container/heap"
	"encoding/json"
	"iter"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/processtelemetry"
	"mvdan.cc/sh/v3/syntax"
)

type Event struct {
	Class   string
	Seq     int64
	At      time.Time
	Kind    string
	Payload json.RawMessage
}
type Interval struct{ Start, End time.Time }
type Timing struct {
	Approximate         bool               `json:"approximate"`
	ClassificationBasis string             `json:"classification_basis"`
	Seconds             map[string]float64 `json:"seconds"`
	Phases              map[string]float64 `json:"phases"`
	Basis               string             `json:"basis"`
	IncompleteTools     int                `json:"incomplete_tools"`
	Hours               []TimingHour       `json:"hours,omitempty"`
	GateRuns            []GateRunBucket    `json:"gate_runs,omitempty"`
}

// TimingHour retains the measured interval's date, rather than assigning a
// whole conversation to its launch day. Buckets are UTC and partition totals.
type TimingHour struct {
	At      time.Time          `json:"at"`
	Phases  map[string]float64 `json:"phases"`
	Seconds map[string]float64 `json:"seconds"`
	Detail  map[string]float64 `json:"detail,omitempty"`
}

// shellCommands visits executable AST calls, never literal heredoc body text.
// Shell wrappers are parsed recursively; no expansions are executed.
func shellCommands(text string) [][]string { return shellCommandsDepth(text, 0) }
func shellCommandsDepth(text string, depth int) [][]string {
	if depth > 8 || len(text) > 1<<20 {
		return nil
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(text), "")
	if err != nil {
		return nil
	}
	var commands [][]string
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		var words []string
		for _, arg := range call.Args {
			words = append(words, shellWord(arg.Parts))
		}
		words = commandWords(words)
		if len(words) == 0 {
			return true
		}
		exe := filepath.Base(words[0])
		if exe == "bash" || exe == "sh" || exe == "zsh" || exe == "dash" {
			for i := 1; i < len(words); i++ {
				if strings.HasPrefix(words[i], "-") && strings.Contains(words[i], "c") && i+1 < len(words) {
					commands = append(commands, shellCommandsDepth(words[i+1], depth+1)...)
					return true
				}
			}
		}
		if exe == "eval" && len(words) > 1 {
			commands = append(commands, shellCommandsDepth(strings.Join(words[1:], " "), depth+1)...)
			return true
		}
		commands = append(commands, words)
		return true
	})
	return commands
}
func shellWord(parts []syntax.WordPart) string { return shellWordMode(parts, false) }
func shellWordMode(parts []syntax.WordPart, double bool) string {
	var out strings.Builder
	for _, part := range parts {
		switch v := part.(type) {
		case *syntax.Lit:
			for i := 0; i < len(v.Value); i++ {
				if v.Value[i] == '\\' && i+1 < len(v.Value) && (!double || strings.ContainsRune("\\\"$`\n", rune(v.Value[i+1]))) {
					i++
					if v.Value[i] == '\n' {
						continue
					}
				}
				out.WriteByte(v.Value[i])
			}
		case *syntax.SglQuoted:
			out.WriteString(v.Value)
		case *syntax.DblQuoted:
			out.WriteString(shellWordMode(v.Parts, true))
		default:
			var b bytes.Buffer
			_ = syntax.NewPrinter().Print(&b, part)
			out.WriteString(b.String())
		}
	}
	return out.String()
}

func command(input json.RawMessage) string {
	var v struct {
		Command string `json:"command"`
		Cmd     string `json:"cmd"`
	}
	_ = json.Unmarshal(input, &v)
	if v.Command != "" {
		return v.Command
	}
	return v.Cmd
}

// commandWords strips only known, static launch wrappers. It does not expand
// variables, interpret shell code passed as an argument, or inspect echoed text.
func commandWords(words []string) []string {
	for len(words) > 0 {
		if strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "/") {
			words = words[1:]
			continue
		}
		exe := filepath.Base(words[0])
		n := 1
		switch exe {
		case "env":
			for n < len(words) {
				v := words[n]
				if v == "-u" || v == "--unset" || v == "-C" || v == "--chdir" {
					n += 2
					continue
				}
				if strings.HasPrefix(v, "-") || strings.Contains(v, "=") {
					n++
					continue
				}
				break
			}
		case "time", "command", "builtin", "nohup":
			for n < len(words) && strings.HasPrefix(words[n], "-") {
				n++
			}
		case "nice":
			for n < len(words) && strings.HasPrefix(words[n], "-") {
				if words[n] == "-n" || words[n] == "--adjustment" {
					n++
				}
				n++
			}
		case "timeout":
			for n < len(words) && strings.HasPrefix(words[n], "-") {
				if words[n] == "-k" || words[n] == "--kill-after" || words[n] == "-s" || words[n] == "--signal" {
					n++
				}
				n++
			}
			n++
		case "flock":
			for n < len(words) && strings.HasPrefix(words[n], "-") {
				if words[n] == "-w" || words[n] == "--wait" || words[n] == "-E" || words[n] == "--conflict-exit-code" {
					n++
				}
				n++
			}
			n++
		case "gate_record.py":
			for n < len(words) && words[n] != "--" {
				n++
			}
			n++
		default:
			return words
		}
		if n >= len(words) {
			return nil
		}
		words = words[n:]
	}
	return words
}

func goAction(words []string) string {
	for i := 1; i < len(words); i++ {
		if words[i] == "-C" {
			i++
			continue
		}
		if !strings.HasPrefix(words[i], "-") {
			return words[i]
		}
	}
	return ""
}

func Classify(tool string, input json.RawMessage) string {
	name := strings.ToLower(tool)
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	switch name {
	case "wait", "wait_agent", "sleep":
		return "waiting"
	case "read", "read_file", "glob", "grep", "list_directory", "search_files":
		return "read"
	case "edit", "write", "write_file", "apply_patch", "multiedit", "patch", "notebookedit":
		return "edit"
	}
	best := "other"
	priority := map[string]int{"other": 0, "read": 1, "waiting": 2, "git": 3, "build_lint": 4, "tests": 5}
	for _, raw := range shellCommands(command(input)) {
		words := commandWords(raw)
		if len(words) == 0 {
			continue
		}
		class := "other"
		exe := filepath.Base(words[0])
		args := words[1:]
		switch exe {
		case "sleep", "wait":
			class = "waiting"
		case "go":
			switch goAction(words) {
			case "test":
				class = "tests"
			case "build", "vet":
				class = "build_lint"
			}
		case "git":
			class = "git"
		case "vitest", "playwright", "pytest":
			class = "tests"
		case "node":
			if nodeTestRunner(words) {
				class = "tests"
			}
		case "pre-merge.sh":
			class = "build_lint"
		case "golangci-lint", "eslint", "tsc", "pre-commit":
			class = "build_lint"
		case "cat", "sed", "rg", "grep", "head", "tail", "ls", "find", "pwd":
			class = "read"
		case "pre_merge.py":
			class = "build_lint"
		case "python", "python3":
			if len(args) > 0 && filepath.Base(args[0]) == "pre_merge.py" {
				class = "build_lint"
			}
			if len(args) > 0 && filepath.Base(args[0]) == "gate_record.py" {
				nested := commandWords(append([]string{"gate_record.py"}, args[1:]...))
				if len(nested) > 0 {
					raw, _ := json.Marshal(map[string]string{"command": strings.Join(nested, " ")})
					class = Classify("shell", raw)
				}
			}
			if len(args) >= 2 && args[0] == "-m" && (args[1] == "pytest" || args[1] == "unittest") {
				class = "tests"
			}
		case "cargo":
			if len(args) > 0 && args[0] == "test" {
				class = "tests"
			}
		case "npm", "pnpm", "yarn", "npx":
			for len(args) > 0 && (args[0] == "run" || strings.HasPrefix(args[0], "-")) {
				if args[0] == "--prefix" || args[0] == "--cwd" {
					args = args[1:]
					if len(args) == 0 {
						break
					}
				}
				args = args[1:]
			}
			if len(args) > 0 {
				switch strings.SplitN(args[0], ":", 2)[0] {
				case "test", "vitest", "playwright":
					class = "tests"
				case "build", "lint", "typecheck", "check", "tsc", "eslint":
					class = "build_lint"
				}
			}
		case "make":
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				if args[0] == "-C" || args[0] == "--directory" || args[0] == "-f" {
					args = args[1:]
					if len(args) == 0 {
						break
					}
				}
				args = args[1:]
			}
			if len(args) > 0 {
				switch args[0] {
				case "test":
					class = "tests"
				case "check", "lint", "build", "gate":
					class = "build_lint"
				}
			}
		}
		if priority[class] > priority[best] {
			best = class
		}
	}
	return best
}

func commandPhase(input json.RawMessage) string {
	for _, raw := range shellCommands(command(input)) {
		words := commandWords(raw)
		if len(words) < 2 {
			continue
		}
		switch filepath.Base(words[0]) {
		case "git":
			skip := false
			for _, arg := range words[1:] {
				if arg == "--no-verify" || arg == "-n" {
					skip = true
				}
			}
			if skip {
				continue
			}
			for i := 1; i < len(words); i++ {
				if words[i] == "-C" || words[i] == "-c" || words[i] == "--git-dir" || words[i] == "--work-tree" {
					i++
					continue
				}
				if strings.HasPrefix(words[i], "-") {
					continue
				}
				if words[i] == "commit" {
					return "gates"
				}
				break
			}
		case "codex", "claude", "hermes":
			if words[1] == "review" {
				return "reviewing"
			}
		}
	}
	return ""
}

type span struct {
	start, end   time.Time
	class, phase string
	stage        string
	run          *gateRun
}
type spanHeap []int

func (h spanHeap) Len() int           { return len(h) }
func (h spanHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h spanHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *spanHeap) Push(v any) {
	index, ok := v.(int)
	if !ok {
		panic("timing heap requires a span index")
	}
	*h = append(*h, index)
}
func (h *spanHeap) Pop() any { old := *h; n := len(old); v := old[n-1]; *h = old[:n-1]; return v }

// Split partitions the recorded lifetime using a sweep over interval boundaries.
// Priority: turn-end work, human wait, tests, build/lint, git, edit, background wait, read, other, model,
// explicit idle, unknown. Every instant belongs to at most one category.
// Missing status events remain unknown; running outside tools is model time
// (including uninstrumented engine work). An unfinished tool stops at end.
func Split(start, end time.Time, events []Event, waits []Interval) Timing {
	events = append([]Event(nil), events...)
	for i := range events {
		if events[i].Kind == "mark" {
			var p struct {
				At string `json:"at"`
			}
			_ = json.Unmarshal(events[i].Payload, &p)
			if at, err := time.Parse(time.RFC3339, p.At); err == nil {
				events[i].At = at
			}
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
	return SplitOrdered(start, end, slices.Values(events), waits)
}

// SplitOrdered consumes effective-time ordered events without retaining their payloads.
func SplitOrdered(start, end time.Time, events iter.Seq[Event], waits []Interval) Timing {
	return splitOrdered(start, end, events, waits, false)
}

// SplitOrderedWithHours adds bounded trend data for Insights. Other timing
// consumers need totals only and do not pay for hourly maps or serialization.
func SplitOrderedWithHours(start, end time.Time, events iter.Seq[Event], waits []Interval) Timing {
	return splitOrdered(start, end, events, waits, true)
}

func splitOrdered(start, end time.Time, events iter.Seq[Event], waits []Interval, withHours bool) Timing {
	result := Timing{Approximate: true, ClassificationBasis: "command_text_approximate", Seconds: map[string]float64{}, Phases: map[string]float64{}, Basis: "recorded_tools_status_background_and_lifecycle; gates_include_explicit_suite_intervals; phases_from_failure_review_marks"}
	for _, k := range []string{"tests", "build_lint", "git", "read", "edit", "other", "model", "idle", "waiting", "waiting_on_person", "unknown"} {
		result.Seconds[k] = 0
	}
	for _, k := range []string{"building", "reviewing", "fixing", "waiting", "waiting_on_person", "gates", "idle", "unknown"} {
		result.Phases[k] = 0
	}
	if end.Before(start) {
		return result
	}
	spans := []span{}
	runs := []*gateRun{}
	addMeta := func(a, b time.Time, class, phase, stage string, run *gateRun) {
		if a.Before(start) {
			a = start
		}
		if b.After(end) {
			b = end
		}
		if b.After(a) {
			if n := len(spans); n > 0 && spans[n-1].end.Equal(a) && spans[n-1].class == class && spans[n-1].phase == phase && spans[n-1].run == run && spans[n-1].stage == stage {
				spans[n-1].end = b
				return
			}
			spans = append(spans, span{a, b, class, phase, stage, run})
		}
	}
	add := func(a, b time.Time, class, phase string) {
		stage := "first"
		if phase == "fixing" {
			stage = "rework"
		}
		addMeta(a, b, class, phase, stage, nil)
	}
	type call struct {
		at             time.Time
		class, phase   string
		stage          string
		run            *gateRun
		implementation bool
	}
	calls := map[string]call{}
	callClasses := map[string]call{}
	scripts := scriptSignals{}
	// Repeated gate invocations commonly differ only in their call IDs. Parse
	// their immutable command evidence once; script observations remain ordered.
	type commandInfo struct {
		class, phase, signature string
		identity                GateEvidence
		implementation          bool
	}
	commands := map[string]commandInfo{}
	status, statusAt := "", start
	statusPhase := "unknown"
	fixing := false
	reviewing := false
	implemented := false
	markedRework := false
	failedSignatures := map[string]GateEvidence{}
	gateOutcome := func(at time.Time, run *gateRun) {
		if reviewing || run.evidence.Signature == "" {
			return
		}
		if run.outcome == "failed" && implemented {
			failedSignatures[run.evidence.Signature] = run.identity
		} else if run.outcome == "passed" {
			for signature, failure := range failedSignatures {
				if gateCovers(run.identity, failure) {
					delete(failedSignatures, signature)
				}
			}
		}
		next := markedRework || len(failedSignatures) > 0
		if next == fixing {
			return
		}
		if status != "" {
			add(statusAt, at, status, statusPhase)
			statusAt = at
		}
		fixing = next
		if status == "model" {
			statusPhase = "building"
			if fixing {
				statusPhase = "fixing"
			}
		}
	}
	for ev := range events {
		var p struct {
			Tool        string          `json:"tool"`
			CallID      string          `json:"call_id"`
			Input       json.RawMessage `json:"input"`
			Status      string          `json:"status"`
			Kind        string          `json:"kind"`
			Type        string          `json:"type"`
			Verdict     string          `json:"verdict"`
			RerunPassed bool            `json:"rerun_passed"`
			StartedAt   time.Time       `json:"started_at"`
			EndedAt     time.Time       `json:"ended_at"`
			TaskID      string          `json:"task_id"`
			Command     string          `json:"command"`
			TimingGate  GateEvidence    `json:"timing_gate"`
			ExitCode    *int            `json:"exit_code"`
			IsError     *bool           `json:"is_error"`
			Outcome     string          `json:"outcome"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		switch ev.Kind {
		case "process_activity":
			var batch processtelemetry.Batch
			if len(ev.Payload) > 128<<10 || json.Unmarshal(ev.Payload, &batch) != nil || len(batch.Spans) > processtelemetry.MaxSpans {
				continue
			}
			for _, observed := range batch.Spans {
				if !processtelemetry.Valid(observed.Program, observed.Class) || observed.Start.IsZero() || observed.End.Before(observed.Start) {
					continue
				}
				add(observed.Start, observed.End, observed.Class, "gates")
				result.ClassificationBasis = "process_tree_and_command_text"
			}
		case "gate_interval":
			if !p.StartedAt.IsZero() && !p.EndedAt.IsZero() {
				kind := p.TimingGate.Kind
				class := kind
				if kind == "commit_hooks" {
					class = "git"
				}
				if class != "tests" && class != "build_lint" && class != "git" {
					class = "gate_unknown"
					p.TimingGate.Kind = "unknown"
				}
				run := &gateRun{at: p.StartedAt, end: p.EndedAt, explicit: true, evidence: p.TimingGate, outcome: p.Outcome, hours: map[time.Time]float64{}}
				if p.EndedAt.After(end) {
					run.outcome = ""
				}
				runs = append(runs, run)
				addMeta(p.StartedAt, p.EndedAt, class, "gates", "first", run)
			}
		case "background_task_started":
			if p.TaskID == "" {
				continue
			}
			input, _ := json.Marshal(map[string]string{"command": p.Command})
			class := Classify("shell", input)
			phase := "building"
			if class == "tests" || class == "build_lint" {
				phase = "gates"
			}
			// Engine task descriptions may be truncated shell text. The original
			// call's classification survives its initial asynchronous result.
			c, known := callClasses[p.CallID]
			stage := "first"
			if fixing {
				stage = "rework"
			}
			if known {
				class, phase = c.class, c.phase
				stage = c.stage
			}
			at := ev.At
			if !p.StartedAt.IsZero() && !p.StartedAt.After(ev.At) {
				at = p.StartedAt
			}
			run := c.run
			if phase == "gates" && run == nil {
				kind := class
				if kind == "git" {
					kind = "commit_hooks"
				}
				run = &gateRun{at: at, evidence: GateEvidence{Kind: kind}, hours: map[time.Time]float64{}}
				runs = append(runs, run)
			}
			calls["bg:"+p.TaskID] = call{at, class, phase, stage, run, false}
			if run != nil {
				run.background = true
				run.outcome = ""
			}
		case "background_task_done":
			if c, ok := calls["bg:"+p.TaskID]; ok {
				addMeta(c.at, ev.At, c.class, c.phase, c.stage, c.run)
				if c.run != nil && !ev.At.After(end) {
					c.run.end = ev.At
					c.run.background = false
					switch {
					case p.ExitCode != nil:
						c.run.outcome = "passed"
						if *p.ExitCode != 0 {
							c.run.outcome = "failed"
						}
					case p.Status == "failed":
						c.run.outcome = "failed"
					case p.Status == "completed":
						c.run.outcome = "passed"
					}
					gateOutcome(ev.At, c.run)
				}
				delete(calls, "bg:"+p.TaskID)
			}
		case "turn_work":
			if !p.StartedAt.IsZero() && !p.EndedAt.IsZero() {
				add(p.StartedAt, p.EndedAt, "turn_end", "turn_end")
			}
		case "review_started":
			if status != "" {
				add(statusAt, ev.At, status, statusPhase)
				statusAt = ev.At
			}
			reviewing = true
			if status == "model" {
				statusPhase = "reviewing"
			}
		case "rework_started":
			markedRework = true
			fixing = true
		case "mark":
			if ev.At.After(end) {
				continue
			}
			if status != "" {
				add(statusAt, ev.At, status, statusPhase)
				statusAt = ev.At
			}
			if p.Kind == "failure" && !reviewing {
				// A mark about an observed gate must not become a second,
				// permanent failure source after that command passes. A red
				// test before implementation is still first-build work.
				if p.RerunPassed {
					markedRework = false
				} else if (p.Type != "test" && p.Type != "gate") || (implemented && len(failedSignatures) == 0) {
					markedRework = true
				}
			}
			if p.Kind == "review" {
				reviewing = false
				markedRework = p.Verdict == "changes" || p.Verdict == "rejected"
			}
			fixing = markedRework || len(failedSignatures) > 0
			if status == "model" {
				statusPhase = "building"
				if reviewing {
					statusPhase = "reviewing"
				}
				if fixing && !reviewing {
					statusPhase = "fixing"
				}
			}
		case "status_changed":
			if status != "" {
				add(statusAt, ev.At, status, statusPhase)
			}
			status = "unknown"
			if p.Status == "running" {
				status = "model"
			}
			if p.Status == "idle" {
				status = "idle"
			}
			statusAt = ev.At
			statusPhase = status
			if status == "model" {
				statusPhase = "building"
				if reviewing {
					statusPhase = "reviewing"
				}
				if fixing && !reviewing {
					statusPhase = "fixing"
				}
			}
		case "turn_done":
			if status != "" {
				add(statusAt, ev.At, status, statusPhase)
			}
			status = "idle"
			statusPhase = "idle"
			statusAt = ev.At
		case "tool_call", "background_call":
			if p.CallID == "" {
				continue
			}
			key := p.Tool + "\x00" + string(p.Input)
			info, ok := commands[key]
			if !ok {
				info = commandInfo{Classify(p.Tool, p.Input), commandPhase(p.Input), commandSignature(p.Input), gateIdentity(p.Input), sourceEdit(p.Input)}
				if len(commands) < 256 && len(key) <= 65536 {
					commands[key] = info
				}
			}
			class := info.class
			if ev.Class == "tests" || ev.Class == "build_lint" {
				class = ev.Class
			}
			if script := scripts.classify(p.Input); script == "tests" || script == "build_lint" && class != "tests" {
				class = script
			}
			phase := "building"
			if reviewing {
				phase = "reviewing"
			}
			if fixing && !reviewing {
				phase = "fixing"
			}
			if derived := info.phase; derived != "" && (!reviewing || derived == "gates") {
				phase = derived
			}
			if class == "tests" || class == "build_lint" || phase == "gates" {
				phase = "gates"
			}
			if class == "waiting" {
				phase = "waiting"
			}
			stage := "first"
			if fixing && !reviewing {
				stage = "rework"
			}
			var run *gateRun
			if phase == "gates" {
				evidence := p.TimingGate
				// Historical commands establish rerun identity without filling
				// the suite/region metadata they never captured.
				evidence.Signature = info.signature
				kind := class
				if class == "git" {
					kind = "commit_hooks"
				}
				evidence.Kind = kind
				run = &gateRun{at: ev.At, evidence: evidence, identity: info.identity, hours: map[time.Time]float64{}}
				runs = append(runs, run)
			}
			calls[p.CallID] = call{ev.At, class, phase, stage, run, info.implementation}
			callClasses[p.CallID] = calls[p.CallID]
			if ev.Kind == "background_call" {
				delete(calls, p.CallID)
			}
		case "tool_result":
			if c, ok := calls[p.CallID]; ok {
				if c.class == "edit" && c.implementation && !reviewing && (p.IsError == nil || !*p.IsError) && (p.ExitCode == nil || *p.ExitCode == 0) {
					implemented = true
				}
				addMeta(c.at, ev.At, c.class, c.phase, c.stage, c.run)
				if c.run != nil && !ev.At.After(end) {
					c.run.end = ev.At
					if p.ExitCode != nil && (!c.run.background || *p.ExitCode != 0) {
						c.run.outcome = "passed"
						if *p.ExitCode != 0 {
							c.run.outcome = "failed"
						}
					} else if p.IsError != nil {
						if *p.IsError {
							c.run.outcome = "failed"
						} else if !c.run.background {
							c.run.outcome = "passed"
						}
					}
					gateOutcome(ev.At, c.run)
				}
				delete(calls, p.CallID)
			}
		}
	}
	if status != "" {
		add(statusAt, end, status, statusPhase)
	}
	for _, c := range calls {
		addMeta(c.at, end, c.class, c.phase, c.stage, c.run)
		result.IncompleteTools++
	}
	for _, w := range waits {
		add(w.Start, w.End, "waiting_on_person", "waiting_on_person")
	}
	type edge struct {
		at           time.Time
		index, delta int
	}
	edges := []edge{{start, -1, 0}, {end, -1, 0}}
	for i, s := range spans {
		edges = append(edges, edge{s.start, i, 1}, edge{s.end, i, -1})
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].at.Before(edges[j].at) })
	priority := map[string]int{"turn_end": 12, "waiting_on_person": 11, "tests": 10, "build_lint": 9, "git": 8, "gate_unknown": 7, "edit": 6, "waiting": 5, "read": 4, "other": 3, "model": 2, "idle": 1, "unknown": 0}
	// Order only pairs that occur in this session. Rebuilding every possible
	// class/phase string at every edge dominated cold historical reads.
	type phaseClass struct{ class, phase string }
	phaseRank := map[string]int{}
	for i, phase := range []string{"turn_end", "gates", "waiting_on_person", "waiting", "reviewing", "fixing", "building", "idle", "unknown"} {
		phaseRank[phase] = i
	}
	candidates := []phaseClass{}
	seen := map[phaseClass]bool{}
	for _, s := range spans {
		pair := phaseClass{s.class, s.phase}
		if !seen[pair] {
			seen[pair] = true
			candidates = append(candidates, pair)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if priority[a.class] != priority[b.class] {
			return priority[a.class] > priority[b.class]
		}
		if phaseRank[a.phase] != phaseRank[b.phase] {
			return phaseRank[a.phase] < phaseRank[b.phase]
		}
		return a.class < b.class
	})
	active := map[phaseClass]int{}
	heaps := map[phaseClass]*spanHeap{}
	present := make([]bool, len(spans))
	previous := start
	for _, e := range edges {
		best, phase := "unknown", "unknown"
		for _, pair := range candidates {
			if active[pair] > 0 {
				best, phase = pair.class, pair.phase
				break
			}
		}
		seconds := e.at.Sub(previous).Seconds()
		winner := -1
		key := phaseClass{best, phase}
		if indices := heaps[key]; indices != nil {
			for indices.Len() > 0 && !present[(*indices)[0]] {
				heap.Pop(indices)
			}
			if indices.Len() > 0 {
				winner = (*indices)[0]
			}
		}
		if seconds > 0 {
			accounted := best
			if best == "gate_unknown" {
				accounted = "unknown"
			}
			result.Seconds[accounted] += seconds
			result.Phases[phase] += seconds
			at := previous
			if oldest := end.Add(-180 * 24 * time.Hour); at.Before(oldest) {
				at = oldest
			}
			for withHours && at.Before(e.at) {
				hour := at.UTC().Truncate(time.Hour)
				until := hour.Add(time.Hour)
				if until.After(e.at) {
					until = e.at
				}
				n := len(result.Hours)
				if n == 0 || !result.Hours[n-1].At.Equal(hour) {
					result.Hours = append(result.Hours, TimingHour{At: hour, Phases: map[string]float64{}, Seconds: map[string]float64{}, Detail: map[string]float64{}})
					n++
				}
				h := &result.Hours[n-1]
				h.Phases[phase] += until.Sub(at).Seconds()
				h.Seconds[accounted] += until.Sub(at).Seconds()
				amount := until.Sub(at).Seconds()
				if key := breakdownKey(accounted, phase); key != "" {
					if phase == "gates" && winner >= 0 && spans[winner].run != nil && spans[winner].run.evidence.Kind == "unknown" {
						key = "gates.unknown"
					}
					h.Detail[key] += amount
					if strings.HasPrefix(key, "building.") {
						stage := "first"
						if winner >= 0 {
							stage = spans[winner].stage
						}
						h.Detail["building."+stage] += amount
					}
				}
				if winner >= 0 && phase == "gates" && spans[winner].run != nil {
					spans[winner].run.hours[hour] += amount
				}
				at = until
			}
		}
		if e.index >= 0 {
			s := spans[e.index]
			active[phaseClass{s.class, s.phase}] += e.delta
			present[e.index] = e.delta > 0
			if e.delta > 0 {
				key := phaseClass{s.class, s.phase}
				if heaps[key] == nil {
					heaps[key] = &spanHeap{}
				}
				heap.Push(heaps[key], e.index)
			}
		}
		previous = e.at
	}
	if withHours {
		result.GateRuns = collectGateRuns(runs, end)
	}
	return result
}
