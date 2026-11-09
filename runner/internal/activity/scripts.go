package activity

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// scriptSignals remembers only a class for a literal script written by the
// session. Heredoc text alone is never work: a later explicit interpreter/script
// invocation must name that file. This is approximate static evidence, not code
// evaluation, and complements observed process intervals for future runs.
type scriptSignals map[string]string

var pythonLaunch = regexp.MustCompile(`(?s)(?:subprocess\.(?:run|Popen|call|check_call|check_output)|os\.execv[pel]*|run)\s*\(\s*\[([^\]]{1,1000})\]`)
var pythonWord = regexp.MustCompile(`['"]([^'"\r\n]{1,1000})['"]`)

func pythonClass(body string) string {
	best := "other"
	for _, match := range pythonLaunch.FindAllStringSubmatch(body, 64) {
		var words []string
		for _, word := range pythonWord.FindAllStringSubmatch(match[1], 64) {
			words = append(words, word[1])
		}
		if len(words) == 0 {
			continue
		}
		input, _ := json.Marshal(map[string]string{"command": strings.Join(words, " ")})
		c := Classify("shell", input)
		if c == "tests" {
			return c
		}
		if c == "build_lint" {
			best = c
		}
	}
	return best
}
func (s scriptSignals) classify(input json.RawMessage) string {
	return s.classifyText(command(input), 0)
}
func (s scriptSignals) classifyText(text string, depth int) string {
	if depth > 8 || len(text) > 1<<20 {
		return "other"
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(text), "")
	if err != nil {
		return "other"
	}
	best := "other"
	improve := func(c string) {
		if c == "tests" || c == "build_lint" && best != "tests" {
			best = c
		}
	}
	syntax.Walk(file, func(node syntax.Node) bool {
		stmt, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}
		call, ok := stmt.Cmd.(*syntax.CallExpr)
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
		name := filepath.Base(words[0])
		if name == "bash" || name == "sh" || name == "zsh" {
			for i := 1; i+1 < len(words); i++ {
				if strings.HasPrefix(words[i], "-") && strings.Contains(words[i], "c") {
					improve(s.classifyText(words[i+1], depth+1))
					return true
				}
			}
		}
		var body, target string
		for _, redir := range stmt.Redirs {
			if redir.Hdoc != nil {
				body = shellWord(redir.Hdoc.Parts)
			}
			if redir.Op == syntax.RdrOut && redir.Word != nil {
				target = shellWord(redir.Word.Parts)
			}
		}
		if name == "cp" && len(words) == 3 && len(s) < 256 {
			if c := s[filepath.Clean(words[1])]; c != "" {
				s[filepath.Clean(words[2])] = c
			}
		}
		if name == "cat" && body != "" && target != "" && !strings.ContainsAny(target, "$`\x00") && len(s) < 256 {
			var class string
			if strings.HasSuffix(target, ".py") {
				class = pythonClass(body)
			} else {
				class = classifyScript(body)
			}
			s[filepath.Clean(target)] = class
		}
		if (name == "python" || name == "python3") && body != "" {
			s.pythonWrites(body)
			improve(pythonClass(body))
		}
		if c := s[filepath.Clean(words[0])]; c != "" {
			improve(c)
		}
		if name == "bash" || name == "sh" || name == "python" || name == "python3" {
			for i := 1; i < len(words); i++ {
				if strings.HasPrefix(words[i], "-") {
					continue
				}
				if c := s[filepath.Clean(words[i])]; c != "" {
					improve(c)
				}
				break
			}
		}
		return true
	})
	return best
}

// Resolve only local shell functions that explicitly forward their arguments.
// This covers gate helpers without treating echoed or heredoc text as execution.
func classifyScript(body string) string {
	raw, _ := json.Marshal(map[string]string{"command": body})
	best := Classify("shell", raw)
	file, err := syntax.NewParser().Parse(strings.NewReader(body), "")
	if err != nil {
		return best
	}
	forwards := map[string]int{}
	syntax.Walk(file, func(n syntax.Node) bool {
		f, ok := n.(*syntax.FuncDecl)
		if !ok {
			return true
		}
		shift := 0
		forward := false
		syntax.Walk(f.Body, func(n syntax.Node) bool {
			c, ok := n.(*syntax.CallExpr)
			if !ok || len(c.Args) == 0 {
				return true
			}
			w := shellWord(c.Args[0].Parts)
			if w == "shift" {
				shift++
			}
			if w == "$@" || w == "${@}" {
				forward = true
			}
			return true
		})
		if forward {
			forwards[f.Name.Value] = 1 + shift
		}
		return false
	})
	for _, words := range shellCommands(body) {
		offset, ok := forwards[words[0]]
		if !ok || offset >= len(words) {
			continue
		}
		raw, _ := json.Marshal(map[string]string{"command": strings.Join(words[offset:], " ")})
		c := Classify("shell", raw)
		if c == "tests" {
			return c
		}
		if c == "build_lint" {
			best = c
		}
	}
	return best
}

var pythonScriptPath = regexp.MustCompile(`['"]([^'"\r\n]{1,1000}\.sh)['"]`)
var pythonScriptAssignment = regexp.MustCompile(`([A-Za-z_][A-Za-z_0-9]*)\s*=\s*(?:Path\()?['"]([^'"\r\n]{1,1000}\.sh)['"]`)
var pythonLiteralBody = regexp.MustCompile(`(?s)(?:'''|""")(.*?)(?:'''|""")`)

// Carry a class through literal script reads/copies and writes. No source text
// survives this call; uncertain dynamic paths are left to process observation.
func (s scriptSignals) pythonWrites(body string) {
	if !strings.Contains(body, "write_text(") && !strings.Contains(body, ",'w'") && !strings.Contains(body, ", 'w'") && !strings.Contains(body, `,"w"`) {
		return
	}
	best := "other"
	improve := func(c string) {
		if c == "tests" || c == "build_lint" && best != "tests" {
			best = c
		}
	}
	for _, p := range pythonScriptPath.FindAllStringSubmatch(body, 128) {
		improve(s[filepath.Clean(p[1])])
	}
	for _, b := range pythonLiteralBody.FindAllStringSubmatch(body, 32) {
		improve(classifyScript(b[1]))
	}
	if best == "other" {
		return
	}
	for _, p := range pythonScriptPath.FindAllStringSubmatch(body, 128) {
		path := p[1]
		direct := strings.Contains(body, "open('"+path+"','w')") || strings.Contains(body, "open('"+path+"', 'w')") || strings.Contains(body, "Path('"+path+"').write_text(")
		for _, a := range pythonScriptAssignment.FindAllStringSubmatch(body, 128) {
			if a[2] == path && (strings.Contains(body, a[1]+".write_text(") || strings.Contains(body, "open("+a[1]+",'w')") || strings.Contains(body, "open("+a[1]+", 'w')")) {
				direct = true
			}
		}
		if direct && len(s) < 256 {
			s[filepath.Clean(path)] = best
		}
	}
}
