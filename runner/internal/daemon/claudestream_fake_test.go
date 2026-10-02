package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// A fake `claude` that speaks the streaming-input protocol the way the real CLI was observed to behave
// (spikes of 2026-10-01, see docs/design/mid-turn-steering.md). The test binary re-executes itself as the
// fake: the shim written by installFakeClaude runs `<test binary> -test.run=^TestHelperFakeClaude$`.
//
// Behaviour is steered by the text of the message:
//
//	TOOL ...   a tool call that takes FAKE_TOOL_MS; messages arriving meanwhile are folded into the turn,
//	           a control_request interrupt cancels it
//	HANG ...   like TOOL but ignores interrupts and runs for 60 s
//	SLOWCUT .. like TOOL, but an interrupt is acknowledged at once and the turn ends only FAKE_CUT_MS later
//	CRASH ...  the process exits 3 as soon as it reads the message (nothing consumed)
//	SLASH ...  consumed without a replay line (as a slash command)
//	AUTO ...   after the turn, an autonomous turn (no user message) follows
//	anything else: a plain one-line answer (an interrupt that arrives before the answer cuts the turn)
//
// --resume <id> with an id starting "bad-" fails like the CLI does for a conversation that does not exist.
//
// Every start appends a line to FAKE_LOG: "start <args>", and a clean EOF exit appends "eof".
// FAKE_MODE=unknown makes a streaming start fail like an old CLI (unknown option).
func TestHelperFakeClaude(t *testing.T) {
	if os.Getenv("GO_FAKE_CLAUDE") != "1" {
		t.Skip("helper process")
	}
	fakeClaudeMain()
	os.Exit(0)
}

func fakeEnvInt(name string, def int) time.Duration {
	if v := os.Getenv(name); v != "" {
		var n int
		_, _ = fmt.Sscanf(v, "%d", &n)
		return time.Duration(n) * time.Millisecond
	}
	return time.Duration(def) * time.Millisecond
}

func fakeLog(line string) {
	if p := os.Getenv("FAKE_LOG"); p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.WriteString(line + "\n")
			_ = f.Close()
		}
	}
}

func fakeOut(v map[string]any) {
	b, _ := json.Marshal(v)
	_, _ = os.Stdout.Write(append(b, '\n'))
}

type fakeUser struct{ uuid, text string }

func fakeClaudeMain() {
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	streaming := false
	badResume := false
	sid := fmt.Sprintf("fake-sess-%d", os.Getpid())
	prompt := ""
	for i, a := range args {
		switch a {
		case "--input-format":
			streaming = true
		case "--resume":
			if i+1 < len(args) {
				sid = args[i+1]
				badResume = strings.HasPrefix(sid, "bad-")
			}
		case "-p":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				prompt = args[i+1]
			}
		}
	}
	fakeLog("start " + strings.Join(args, " "))
	if streaming && os.Getenv("FAKE_MODE") == "unknown" {
		fmt.Fprintln(os.Stderr, "error: unknown option '--input-format'")
		os.Exit(1)
	}
	if streaming && badResume {
		fmt.Println("No conversation found with session ID: " + sid)
		fakeOut(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": sid, "result": nil})
		os.Exit(1)
	}
	initEv := map[string]any{"type": "system", "subtype": "init", "session_id": sid}
	text := func(s string) map[string]any {
		return map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}}}
	}
	result := func(subtype string, isErr bool, res any) map[string]any {
		return map[string]any{"type": "result", "subtype": subtype, "is_error": isErr, "session_id": sid, "result": res}
	}
	if !streaming { // the per-turn engine: answer the prompt and exit
		fakeOut(initEv)
		fakeOut(text("ok:" + prompt))
		fakeOut(result("success", false, "ok:"+prompt))
		return
	}

	in := make(chan map[string]any, 64)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				in <- m
			}
		}
		close(in)
	}()
	replayDelay := fakeEnvInt("FAKE_REPLAY_MS", 20)
	toolFor := fakeEnvInt("FAKE_TOOL_MS", 400)
	asUser := func(m map[string]any) fakeUser {
		msg, _ := m["message"].(map[string]any)
		s, _ := msg["content"].(string)
		u, _ := m["uuid"].(string)
		return fakeUser{uuid: u, text: s}
	}
	replay := func(u fakeUser) {
		fakeOut(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": u.text},
			"isReplay": true, "uuid": u.uuid, "session_id": sid})
	}
	controlOK := func(m map[string]any) {
		id, _ := m["request_id"].(string)
		fakeOut(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": id, "response": map[string]any{"still_queued": []any{}}}})
	}
	isControl := func(m map[string]any) bool { return m["type"] == "control_request" }

	var backlog []map[string]any // read but not consumed (left behind by an interrupt)
	next := func() (map[string]any, bool) {
		if len(backlog) > 0 {
			m := backlog[0]
			backlog = backlog[1:]
			return m, true
		}
		m, ok := <-in
		return m, ok
	}
	for {
		m, ok := next()
		if !ok {
			fakeLog("eof")
			return
		}
		if isControl(m) {
			controlOK(m)
			continue
		}
		u := asUser(m)
		if strings.HasPrefix(u.text, "CRASH") {
			os.Exit(3)
		}
		time.Sleep(replayDelay / 2)
		fakeOut(initEv)
		if strings.HasPrefix(u.text, "SLASH") {
			fakeOut(result("success", false, ""))
			continue
		}
		time.Sleep(replayDelay)
		replay(u)
		consumed := []fakeUser{u}
		hang := strings.HasPrefix(u.text, "HANG")
		slowCut := strings.HasPrefix(u.text, "SLOWCUT")
		if !hang && !slowCut && !strings.HasPrefix(u.text, "TOOL") {
			// a plain turn: an interrupt already waiting cuts it before the answer
			select {
			case n := <-in:
				if isControl(n) {
					controlOK(n)
					fakeOut(result("error_during_execution", true, nil))
					continue
				}
				backlog = append(backlog, n)
			default:
			}
		}
		if strings.HasPrefix(u.text, "TOOL") || hang || slowCut {
			fakeOut(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
				map[string]any{"type": "tool_use", "id": "tu_1", "name": "Bash", "input": map[string]any{"command": "work"}}}}})
			d := toolFor
			if hang {
				d = 60 * time.Second
			}
			timer := time.After(d)
			interrupted := false
		tool:
			for {
				select {
				case <-timer:
					break tool
				case n, ok := <-in:
					if !ok {
						// stdin closed mid-turn: finish the turn, then exit
						<-timer
						break tool
					}
					if isControl(n) {
						if hang {
							continue
						}
						controlOK(n)
						if slowCut {
							time.Sleep(fakeEnvInt("FAKE_CUT_MS", 800))
						}
						fakeOut(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
							map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "interrupted", "is_error": true}}}})
						fakeOut(result("error_during_execution", true, nil))
						interrupted = true
						break tool
					}
					consumed = append(consumed, asUser(n))
				}
			}
			if interrupted {
				// what arrived during the tool was not consumed: it starts its own turn
				for _, c := range consumed[1:] {
					backlog = append(backlog, map[string]any{"type": "user", "uuid": c.uuid, "message": map[string]any{"role": "user", "content": c.text}})
				}
				continue
			}
			fakeOut(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "done", "is_error": false}}}})
			for _, c := range consumed[1:] {
				replay(c)
			}
		}
		var texts []string
		for _, c := range consumed {
			texts = append(texts, c.text)
		}
		answer := "ok:" + strings.Join(texts, "|")
		fakeOut(text(answer))
		fakeOut(result("success", false, answer))
		if strings.HasPrefix(u.text, "AUTO") {
			time.Sleep(150 * time.Millisecond)
			fakeOut(initEv)
			fakeOut(text("background done"))
			fakeOut(result("success", false, "background done"))
		}
	}
}

// installFakeClaude puts a `claude` shim that runs the fake first on PATH and returns the env to give the
// driver (nothing here relies on the sanitised environment keeping extra variables).
func installFakeClaude(t *testing.T, extra ...string) (env []string, logPath string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logPath = dir + "/calls.log"
	shim := fmt.Sprintf("#!/bin/sh\nexec %q -test.run='^TestHelperFakeClaude$' -- \"$@\"\n", exe)
	if err := os.WriteFile(dir+"/claude", []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	// exec resolves "claude" through the PATH of this process, not of the child's environment list.
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	env = append(os.Environ(), "GO_FAKE_CLAUDE=1", "FAKE_LOG="+logPath)
	env = append(env, extra...)
	return env, logPath
}
