package activity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type GateEvidence struct {
	Kind      string `json:"kind"`
	Suite     string `json:"suite,omitempty"`
	Region    string `json:"region,omitempty"`
	Signature string `json:"signature,omitempty"`
	Selector  string `json:"-"` // Private identity only; never expose command arguments.
	parts     []GateEvidence
}
type GateRunBucket struct {
	At             time.Time `json:"at"`
	Kind           string    `json:"kind"`
	Suite          string    `json:"suite,omitempty"`
	Region         string    `json:"region,omitempty"`
	Seconds        float64   `json:"seconds"`
	Runs           int       `json:"runs"`
	Passed         int       `json:"passed"`
	Failed         int       `json:"failed"`
	UnknownOutcome int       `json:"unknown_outcome"`
	Repeated       int       `json:"repeated"`
	Reruns         int       `json:"reruns"`
	RepeatUnknown  int       `json:"repeat_unknown"`
	FailedSeconds  float64   `json:"failed_seconds"`
	CountUnknown   bool      `json:"count_unknown,omitempty"`
}
type gateRun struct {
	at              time.Time
	end             time.Time
	explicit        bool
	countUnknown    bool
	background      bool
	evidence        GateEvidence
	identity        GateEvidence
	outcome         string
	repeated, rerun bool
	hours           map[time.Time]float64
}

func safeSuite(s string) string {
	if len(s) == 0 || len(s) > 160 || strings.Contains(strings.ReplaceAll(s, "...", ""), "..") || strings.HasPrefix(s, "/") {
		return ""
	}
	for _, r := range s {
		allowed := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("./_*-:@+", r)
		if !allowed {
			return ""
		}
	}
	return s
}

// Capture only explicit, bounded command selectors. Never publish the command,
// working directory, environment, output or a guess based on the worker title.
func CaptureGateEvidence(kind, payload string) string {
	if kind != "tool_call" || len(payload) > 256<<10 {
		return payload
	}
	var p struct {
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil {
		return payload
	}
	class := Classify(p.Tool, p.Input)
	phase := commandPhase(p.Input)
	if class != "tests" && class != "build_lint" && phase != "gates" {
		return payload
	}
	evidence := GateEvidence{Kind: class}
	if class == "git" {
		evidence.Kind = "commit_hooks"
	}
	text := command(p.Input)
	evidence.Signature = commandSignature(p.Input)
	suites := []string{}
	for _, words := range shellCommands(text) {
		for i, arg := range words {
			if i == 0 && arg == "cd" && len(words) > 1 {
				evidence.Region = safeSuite(words[1])
			}
			if (arg == "--workspace" || arg == "-w" || arg == "--prefix" || arg == "--cwd" || arg == "-C") && i+1 < len(words) {
				evidence.Region = safeSuite(words[i+1])
			}
			if strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "src/") || strings.HasPrefix(arg, "packages/") {
				if name := safeSuite(arg); name != "" {
					suites = append(suites, name)
				}
			}
			if (arg == "--project" || arg == "-run" || arg == "-t" || arg == "--testNamePattern") && i+1 < len(words) {
				if name := safeSuite(words[i+1]); name != "" {
					suites = append(suites, name)
				}
			}
		}
		if len(words) > 0 && (filepath.Base(words[0]) == "npm" || filepath.Base(words[0]) == "pnpm" || filepath.Base(words[0]) == "yarn") {
			for _, arg := range words[1:] {
				if strings.HasPrefix(arg, "test:") {
					suites = append(suites, safeSuite(arg))
				}
			}
		}
	}
	if len(suites) > 0 {
		evidence.Suite = safeSuite(strings.Join(suites, "+"))
		if evidence.Suite == "" && len(suites) == 1 {
			evidence.Suite = suites[0]
		}
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(payload), &object) != nil {
		return payload
	}
	raw, _ := json.Marshal(evidence)
	object["timing_gate"] = raw
	result, err := json.Marshal(object)
	if err != nil {
		return payload
	}
	return string(result)
}

func commandSignature(input json.RawMessage) string {
	identity := gateIdentity(input)
	signature := sha256.Sum256([]byte(identity.Kind + "|" + identity.Region + "|" + identity.Suite + "|" + identity.Selector))
	return hex.EncodeToString(signature[:])
}

// Identity follows the gate's target, never shell decoration or commit prose.
func gateIdentity(input json.RawMessage) GateEvidence {
	region := ""
	var parts []GateEvidence
	for _, words := range shellCommands(command(input)) {
		if len(words) == 0 {
			continue
		}
		if words[0] == "cd" && len(words) > 1 {
			region = safeSuite(filepath.Join(region, words[1]))
			continue
		}
		quoted := make([]string, len(words))
		for i, word := range words {
			quoted[i] = "'" + strings.ReplaceAll(word, "'", "'\"'\"'") + "'"
		}
		part := gateIdentityCall(json.RawMessage(`{"command":` + mustJSON(strings.Join(quoted, " ")) + `}`))
		if part.Kind == "command" {
			continue
		}
		if part.Region == "" {
			part.Region = region
		}
		parts = append(parts, part)
	}
	if len(parts) == 1 {
		return parts[0]
	}
	if len(parts) > 1 {
		keys := make([]string, len(parts))
		for i, p := range parts {
			keys[i] = p.Kind + "|" + p.Region + "|" + p.Suite + "|" + p.Selector
		}
		raw, _ := json.Marshal(keys)
		hash := sha256.Sum256(raw)
		return GateEvidence{Kind: "chain", Suite: hex.EncodeToString(hash[:]), parts: parts}
	}
	return gateIdentityCall(input)
}

func gateIdentityCall(input json.RawMessage) GateEvidence {
	var g GateEvidence
	for _, words := range shellCommands(command(input)) {
		if len(words) == 0 {
			continue
		}
		if words[0] == "cd" && len(words) > 1 {
			g.Region = safeSuite(words[1])
			continue
		}
		name := filepath.Base(words[0])
		if (name == "bash" || name == "sh" || name == "python" || name == "python3" || name == "node" && !nodeTestRunner(words)) && len(words) > 1 && !strings.HasPrefix(words[1], "-") {
			if path := safeSuite(words[1]); path != "" {
				g.Kind = "script"
				g.Suite = path
				return g
			}
		}
		if name == "make" || name == "npx" || nodeTestRunner(words) {
			g.Kind = name
			for i := 1; i < len(words); i++ {
				arg := words[i]
				switch {
				case (arg == "-t" || arg == "--testNamePattern" || arg == "--project") && i+1 < len(words):
					i++
					g.Selector += arg + "=" + words[i] + "\x00"
				case strings.HasPrefix(arg, "--testNamePattern=") || strings.HasPrefix(arg, "-t=") || strings.HasPrefix(arg, "--project="):
					g.Selector += arg + "\x00"
				case (arg == "--maxWorkers" || arg == "--minWorkers" || arg == "--reporter" || arg == "-j") && i+1 < len(words):
					i++
				case arg == "-C" && i+1 < len(words):
					i++
					g.Region = safeSuite(words[i])
				case !strings.HasPrefix(arg, "-"):
					g.Suite += arg + "\x00"
				}
			}
			return g
		}
		if strings.Contains(words[0], "/") && strings.HasSuffix(words[0], ".sh") {
			g.Kind, g.Suite = "script", words[0]
			return g
		}
		if name != "go" && name != "npm" && name != "pnpm" && name != "yarn" && name != "git" && name != "pytest" && name != "cargo" {
			continue
		}
		// Preserve argument boundaries: prose inside a quoted commit message
		// must remain an argument when classifying this individual AST call.
		quoted := make([]string, len(words))
		for i, word := range words {
			quoted[i] = "'" + strings.ReplaceAll(word, "'", "'\"'\"'") + "'"
		}
		class := Classify("Bash", json.RawMessage(`{"command":`+mustJSON(strings.Join(quoted, " "))+`}`))
		if class != "tests" && class != "build_lint" && class != "git" {
			continue
		}
		g.Kind = class
		if class == "git" {
			g.Kind = "commit_hooks"
		} else {
			// A Go package run cannot clear a failed JavaScript suite, and
			// lint passing cannot resolve a failed build in the same region.
			action := ""
			for _, arg := range words[1:] {
				if arg == "test" || strings.HasPrefix(arg, "test:") || arg == "build" || arg == "lint" || arg == "check" || arg == "vet" || arg == "gate" {
					action = arg
					break
				}
			}
			g.Kind += "." + name + "." + action
		}
		var targets []string
		for i := 1; i < len(words); i++ {
			arg := words[i]
			if arg == "-C" || arg == "--cwd" || arg == "--prefix" || arg == "--workspace" || arg == "-w" {
				if i+1 < len(words) {
					i++
					g.Region = safeSuite(words[i])
				}
				continue
			}
			if arg == "-run" || arg == "-t" || arg == "--testNamePattern" {
				if i+1 < len(words) {
					i++
					g.Selector += arg + "=" + words[i] + "\x00"
				}
				continue
			}
			if strings.HasPrefix(arg, "-run=") || strings.HasPrefix(arg, "--testNamePattern=") {
				g.Selector += arg + "\x00"
				continue
			}
			if arg == "-count" || arg == "-timeout" || arg == "--maxWorkers" || arg == "-m" || arg == "--message" || arg == "-tags" || arg == "-p" || arg == "-parallel" || arg == "-bench" || arg == "-benchtime" || arg == "-coverpkg" || arg == "-cpu" || arg == "-exec" || arg == "-shuffle" {
				i++
				continue
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			if strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "src/") || strings.HasPrefix(arg, "packages/") || name == "go" && i > 1 {
				if v := safeSuite(arg); v != "" {
					targets = append(targets, v)
				}
			}
		}
		if g.Kind != "commit_hooks" {
			g.Suite = strings.Join(targets, "+")
			if name == "go" && g.Suite == "" {
				g.Suite = "."
			}
		}
		return g
	}
	// Unknown runners still have a stable, private identity. Hash parsed words
	// rather than publishing a command, credentials, or arbitrary arguments.
	commands := shellCommands(command(input))
	if len(commands) == 0 {
		commands = [][]string{strings.Fields(command(input))}
	}
	normalized, _ := json.Marshal(commands)
	hash := sha256.Sum256(normalized)
	g.Kind, g.Suite = "command", hex.EncodeToString(hash[:])
	return g
}

func nodeTestRunner(words []string) bool {
	return len(words) > 1 && filepath.Base(words[0]) == "node" && (words[1] == "--test" || strings.Contains(words[1], "vitest") || strings.Contains(words[1], "playwright"))
}

func mustJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

func gateCovers(pass, failed GateEvidence) bool {
	if len(failed.parts) > 0 {
		for _, part := range failed.parts {
			if !gateCovers(pass, part) {
				return false
			}
		}
		return true
	}
	if len(pass.parts) > 0 {
		for _, part := range pass.parts {
			if gateCovers(part, failed) {
				return true
			}
		}
		return false
	}
	if pass.Kind != failed.Kind || pass.Region != failed.Region {
		return false
	}
	if pass.Selector != "" && pass.Selector != failed.Selector {
		return false
	}
	if pass.Suite == failed.Suite {
		return true
	}
	for _, failedTarget := range strings.Split(failed.Suite, "+") {
		covered := false
		for _, passTarget := range strings.Split(pass.Suite, "+") {
			if passTarget == failedTarget || strings.HasSuffix(passTarget, "/...") && (strings.HasPrefix(failedTarget, strings.TrimSuffix(passTarget, "...")) || failedTarget == strings.TrimSuffix(passTarget, "/...")) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// Missing paths are gaps, not evidence that production code changed.
func sourceEdit(input json.RawMessage) bool {
	var p struct {
		FilePath string   `json:"file_path"`
		Path     string   `json:"path"`
		Patch    string   `json:"patch"`
		Paths    []string `json:"paths"`
	}
	if json.Unmarshal(input, &p) != nil {
		var changes []struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(input, &changes) == nil {
			for _, change := range changes {
				p.Paths = append(p.Paths, change.Path)
			}
		} else {
			_ = json.Unmarshal(input, &p.Patch)
		}
	}
	paths := []string{p.FilePath, p.Path}
	paths = append(paths, p.Paths...)
	for _, line := range strings.Split(p.Patch, "\n") {
		for _, prefix := range []string{"*** Update File: ", "*** Add File: "} {
			if strings.HasPrefix(line, prefix) {
				paths = append(paths, strings.TrimPrefix(line, prefix))
			}
		}
	}
	for _, path := range paths {
		base := strings.ToLower(filepath.Base(path))
		normalized := "/" + strings.ToLower(filepath.ToSlash(path)) + "/"
		if path == "" || strings.Contains(normalized, "/test/") || strings.Contains(normalized, "/tests/") || strings.Contains(normalized, "/__tests__/") || strings.Contains(base, "_test.") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.HasPrefix(base, "test_") {
			continue
		}
		switch filepath.Ext(base) {
		case ".go", ".ts", ".tsx", ".js", ".jsx", ".rs", ".py", ".java", ".c", ".h", ".cpp", ".cs", ".rb", ".swift", ".kt", ".vue", ".svelte", ".css", ".scss", ".html", ".sql", ".sh", ".yaml", ".yml", ".json", ".toml", ".proto":
			return true
		}
	}
	return false
}

func breakdownKey(class, phase string) string {
	if phase == "gates" {
		switch class {
		case "tests":
			return "gates.tests"
		case "build_lint":
			return "gates.build_lint"
		case "git":
			return "gates.commit_hooks"
		default:
			return "gates.unknown"
		}
	}
	if phase == "waiting" {
		return "building.waiting"
	}
	if phase == "building" || phase == "fixing" {
		if class == "model" {
			return "building.model"
		}
		if class == "unknown" {
			return "building.unknown"
		}
		return "building.tools"
	}
	return ""
}

func collectGateRuns(runs []*gateRun, end time.Time) []GateRunBucket {
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].at.Before(runs[j].at) })
	// Without a recorded correlation ID, an overlapping suite interval and tool
	// call might be the same invocation. Keep time, but never invent run counts.
	for _, a := range runs {
		if !a.explicit {
			continue
		}
		for _, b := range runs {
			if b.explicit {
				continue
			}
			stop := b.end
			if stop.IsZero() {
				stop = end
			}
			if a.at.Before(stop) && b.at.Before(a.end) {
				a.countUnknown = true
				b.countUnknown = true
			}
		}
	}
	type key struct {
		at                  time.Time
		kind, suite, region string
	}
	rows := map[key]*GateRunBucket{}
	order := []key{}
	seen := map[string]string{}
	for _, run := range runs {
		signature := run.evidence.Signature
		previous, repeat := seen[signature]
		run.repeated = signature != "" && repeat
		run.rerun = run.repeated && previous == "failed"
		if signature != "" {
			seen[signature] = run.outcome
		}
		started := run.at.UTC().Truncate(time.Hour)
		if run.hours == nil {
			run.hours = map[time.Time]float64{}
		}
		if run.at.Before(end) && !run.at.Before(end.Add(-180*24*time.Hour)) {
			run.hours[started] += 0
		}
		for at, seconds := range run.hours {
			k := key{at, run.evidence.Kind, run.evidence.Suite, run.evidence.Region}
			row := rows[k]
			if row == nil {
				row = &GateRunBucket{At: at, Kind: k.kind, Suite: k.suite, Region: k.region}
				rows[k] = row
				order = append(order, k)
			}
			row.Seconds += seconds
			if run.outcome == "failed" {
				row.FailedSeconds += seconds
			}
			if at.Equal(started) && run.countUnknown {
				row.CountUnknown = true
			}
			if at.Equal(started) && !run.countUnknown {
				row.Runs++
				switch run.outcome {
				case "passed":
					row.Passed++
				case "failed":
					row.Failed++
				default:
					row.UnknownOutcome++
				}
				if run.repeated {
					row.Repeated++
				}
				if run.rerun {
					row.Reruns++
				}
				if signature == "" {
					row.RepeatUnknown++
				}
			}
		}
	}
	out := make([]GateRunBucket, 0, len(rows))
	for _, k := range order {
		out = append(out, *rows[k])
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		return a.Kind+a.Region+a.Suite < b.Kind+b.Region+b.Suite
	})
	return out
}
