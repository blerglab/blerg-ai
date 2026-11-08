package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
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
//	REALBG ... starts a real `sleep FAKE_BG_SECONDS` in the background that then writes FAKE_BG_MARKER. As
//	           the real CLI does, it lists the shell, stays alive past stdin EOF while the shell runs, and
//	           starts a turn of its own when the shell ends
//	NOWAKEBG . like REALBG, but only the notice is sent when the shell ends — no autonomous turn of its
//	           own — so a wake-up seen afterwards must be Blerg's doing, not the CLI's
//	MOVEDBG .. a foreground Bash call the CLI moves to the background: only tool_use_result names the task
//	FASTBG ... a background shell that ends at once: the list names and drops it before the call's result
//	           arrives, and no notice ever follows
//	RELIST ... the list names FAKE_RELIST, a shell a process before this one started
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
	if !streaming && prompt == "" {
		// `claude -p` with no prompt argument reads the prompt from stdin.
		if in, err := io.ReadAll(os.Stdin); err == nil {
			prompt = string(in)
		}
	}
	for _, a := range args {
		if path, ok := strings.CutPrefix(a, "--append-system-prompt-file="); ok {
			body, err := os.ReadFile(path)
			if err != nil {
				fakeLog("prompt-file unreadable")
				continue
			}
			if st, err := os.Stat(path); err == nil && st.Mode().Perm() != 0o600 {
				fakeLog("prompt-file not private")
			}
			fakeLog("prompt-file " + strings.ReplaceAll(string(body), "\n", " "))
		}
	}
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
		time.Sleep(fakeEnvInt("FAKE_LEGACY_MS", 0))
		fakeOut(initEv)
		if strings.HasPrefix(prompt, "MIXED") {
			fakeMixedBackground()
		}
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
				if m["type"] == "control_request" {
					req, _ := m["request"].(map[string]any)
					if req["subtype"] != "interrupt" {
						fakeLog("control:" + fmt.Sprint(req["subtype"]))
						fakeOut(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": m["request_id"], "response": map[string]any{"mcpServers": []any{}}}})
						continue
					}
					fakeLog("interrupt")
				}
				in <- m
			}
		}
		close(in)
	}()
	replayDelay := fakeEnvInt("FAKE_REPLAY_MS", 20)
	if path := os.Getenv("FAKE_CHILD_PID"); path != "" {
		child := exec.Command("sleep", "60")
		if os.Getenv("FAKE_CHILD_SETSID") == "1" {
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		}
		if child.Start() == nil {
			_ = os.WriteFile(path, []byte(fmt.Sprint(child.Process.Pid)), 0600)
		}
	}
	toolFor := fakeEnvInt("FAKE_TOOL_MS", 400)
	asUser := func(m map[string]any) fakeUser {
		msg, _ := m["message"].(map[string]any)
		s, _ := msg["content"].(string)
		u, _ := m["uuid"].(string)
		return fakeUser{uuid: u, text: s}
	}
	replay := func(u fakeUser) {
		if os.Getenv("FAKE_ECHO_STEER") == "1" {
			fakeLog("consumed:" + u.text)
		}
		fakeOut(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": u.text},
			"isReplay": true, "uuid": u.uuid, "session_id": sid})
	}
	controlOK := func(m map[string]any) {
		id, _ := m["request_id"].(string)
		fakeOut(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": id, "response": map[string]any{"still_queued": []any{}}}})
	}
	isControl := func(m map[string]any) bool {
		req, _ := m["request"].(map[string]any)
		return m["type"] == "control_request" && req["subtype"] == "interrupt"
	}

	// bgDone closes when the real background shell ends; bgHold is closed while none was started.
	bgDone, bgHold := make(chan struct{}), make(chan struct{})
	bgHoldOpen := true
	close(bgHold)
	listShells := func(ids ...string) {
		tasks := []any{}
		for _, id := range ids {
			tasks = append(tasks, map[string]any{"task_id": id, "task_type": "local_bash", "description": "Run the gate"})
		}
		fakeOut(map[string]any{"type": "system", "subtype": "background_tasks_changed", "tasks": tasks, "session_id": sid})
	}
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
			select { // a live background shell keeps the real CLI running after EOF
			case <-bgHold:
			default:
				<-bgDone
			}
			return
		}
		if isControl(m) {
			controlOK(m)
			continue
		}
		u := asUser(m)
		if gate := os.Getenv("FAKE_STALL_INIT"); gate != "" && strings.HasPrefix(u.text, "BLOCKINIT") {
			if _, err := os.Stat(gate); os.IsNotExist(err) {
				_ = os.WriteFile(gate, []byte("blocked"), 0o600)
				for next := range in {
					if isControl(next) && os.Getenv("FAKE_STALL_ACK") == "1" {
						controlOK(next)
					}
				}
				return
			}
		}
		if strings.HasPrefix(u.text, "CRASH") {
			os.Exit(3)
		}
		if strings.HasPrefix(u.text, "DELAYINIT") {
			time.Sleep(fakeEnvInt("FAKE_NEXT_INIT_MS", 900))
		}
		time.Sleep(fakeEnvInt("FAKE_INIT_MS", 0))
		time.Sleep(replayDelay / 2)
		fakeOut(initEv)
		if strings.HasPrefix(u.text, "MIXED") {
			fakeMixedBackground()
		}
		if strings.HasPrefix(u.text, "SLASH") || strings.HasPrefix(u.text, "/") {
			fakeLog("command:" + u.text)
			fakeOut(result("success", false, ""))
			continue
		}
		time.Sleep(replayDelay)
		replay(u)
		consumed := []fakeUser{u}
		if os.Getenv("FAKE_BACKGROUND") == "1" && (strings.HasPrefix(u.text, "TOOL") || strings.HasPrefix(u.text, "BGIDLE")) {
			fakeOut(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
				map[string]any{"type": "tool_use", "id": "bg-call", "name": "Bash", "input": map[string]any{"command": "sleep 60", "description": "Run focused tests", "run_in_background": true}}}}})
			fakeOut(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "bg-call", "content": "Command running in background with ID: bg-fixture. Output is being written to: task.output."}}}})
		}
		if strings.HasPrefix(u.text, "REALBG") && bgHoldOpen {
			bgHoldOpen = false
			bgHold = make(chan struct{})
			child := exec.Command("sh", "-c", fmt.Sprintf("sleep %s; echo done > %s", os.Getenv("FAKE_BG_SECONDS"), os.Getenv("FAKE_BG_MARKER")))
			_ = child.Start()
			listShells("bg-real")
			fakeOut(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
				map[string]any{"type": "tool_use", "id": "bg-real-call", "name": "Bash", "input": map[string]any{"command": "./gate.sh", "description": "Run the gate", "run_in_background": true}}}}})
			fakeOut(map[string]any{"type": "system", "subtype": "task_started", "task_id": "bg-real", "tool_use_id": "bg-real-call", "is_backgrounded": true, "task_type": "local_bash"})
			fakeOut(map[string]any{"type": "user", "tool_use_result": map[string]any{"backgroundTaskId": "bg-real"}, "message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "bg-real-call", "content": "Command running in background with ID: bg-real. Output is being written to: " + os.Getenv("FAKE_BG_MARKER") + ".output. You will be notified when it completes."}}}})
			go func() {
				_ = child.Wait()
				fakeOut(map[string]any{"type": "system", "subtype": "task_notification", "task_id": "bg-real", "tool_use_id": "bg-real-call", "status": "completed", "summary": "Background command completed"})
				listShells()
				fakeOut(initEv)
				fakeOut(text("the gate finished"))
				fakeOut(result("success", false, "the gate finished"))
				close(bgDone)
			}()
		}
		if strings.HasPrefix(u.text, "NOWAKEBG") && bgHoldOpen {
			// Like REALBG, but the task's end is reported by its notice alone, with no autonomous
			// turn of the CLI's own after it: BLG-447's gap is in what Blerg itself does with that
			// notice, so this proves the fix without leaning on the CLI self-waking.
			bgHoldOpen = false
			bgHold = make(chan struct{})
			child := exec.Command("sh", "-c", fmt.Sprintf("sleep %s; echo done > %s", os.Getenv("FAKE_BG_SECONDS"), os.Getenv("FAKE_BG_MARKER")))
			_ = child.Start()
			listShells("bg-nowake")
			fakeOut(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
				map[string]any{"type": "tool_use", "id": "bg-nowake-call", "name": "Bash", "input": map[string]any{"command": "./gate.sh", "description": "Run the gate", "run_in_background": true}}}}})
			fakeOut(map[string]any{"type": "system", "subtype": "task_started", "task_id": "bg-nowake", "tool_use_id": "bg-nowake-call", "is_backgrounded": true, "task_type": "local_bash"})
			fakeOut(map[string]any{"type": "user", "tool_use_result": map[string]any{"backgroundTaskId": "bg-nowake"}, "message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "bg-nowake-call", "content": "Command running in background with ID: bg-nowake. Output is being written to: " + os.Getenv("FAKE_BG_MARKER") + ".output. You will be notified when it completes."}}}})
			go func() {
				_ = child.Wait()
				fakeOut(map[string]any{"type": "system", "subtype": "task_notification", "task_id": "bg-nowake", "tool_use_id": "bg-nowake-call", "status": "completed", "summary": "Background command completed"})
				listShells()
				close(bgDone)
			}()
		}
		if strings.HasPrefix(u.text, "FASTBG") {
			fakeOut(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
				map[string]any{"type": "tool_use", "id": "bg-fast-call", "name": "Bash", "input": map[string]any{"command": "echo hi", "description": "Say hi", "run_in_background": true}}}}})
			listShells("bg-fast")
			listShells()
			fakeOut(map[string]any{"type": "user", "tool_use_result": map[string]any{"backgroundTaskId": "bg-fast"}, "message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "bg-fast-call", "content": "Command running in background with ID: bg-fast. Output is being written to: task.output."}}}})
		}
		if strings.HasPrefix(u.text, "RELIST") {
			listShells(os.Getenv("FAKE_RELIST"))
		}
		if strings.HasPrefix(u.text, "MOVEDBG") {
			fakeOut(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
				map[string]any{"type": "tool_use", "id": "moved-call", "name": "Bash", "input": map[string]any{"command": "./slow.sh", "description": "Run the slow step"}}}}})
			fakeOut(map[string]any{"type": "user", "tool_use_result": map[string]any{"backgroundTaskId": "bg-moved"}, "message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "moved-call", "content": "Command did not complete within its 120s timeout and was moved to the background (ID: bg-moved). Output is being written to: task.output."}}}})
		}
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
						if hang || os.Getenv("FAKE_IGNORE_INTERRUPT") == "1" {
							continue
						}
						lateAck := os.Getenv("FAKE_ACK_AFTER_RESULT") == "1"
						if !lateAck {
							controlOK(n)
						}
						if slowCut {
							time.Sleep(fakeEnvInt("FAKE_CUT_MS", 800))
						}
						fakeOut(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
							map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "interrupted", "is_error": true}}}})
						fakeOut(result("error_during_execution", true, nil))
						if lateAck {
							controlOK(n)
						}
						interrupted = true
						break tool
					}
					if os.Getenv("FAKE_QUEUE_DURING_TOOL") == "1" {
						backlog = append(backlog, n)
						continue
					}
					if os.Getenv("FAKE_ECHO_STEER") == "1" {
						replay(asUser(n))
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

// Exercise the real parser and process-exit path with both engine-owned and detached work.
func fakeMixedBackground() {
	calls := []any{}
	for _, c := range []struct {
		id, tool string
		bg       bool
	}{{"mix-bash", "Bash", true}, {"mix-monitor", "Monitor", false}, {"mix-agent", "Agent", true}, {"mix-foreground", "Agent", false}, {"mix-pending", "Monitor", false}, {"mix-pending-bash", "Bash", true}} {
		calls = append(calls, map[string]any{"type": "tool_use", "id": c.id, "name": c.tool, "input": map[string]any{"command": "sleep 60", "prompt": "Inspect fixture", "description": c.id, "run_in_background": c.bg}})
	}
	fakeOut(map[string]any{"type": "assistant", "message": map[string]any{"content": calls}})
	results := []any{}
	for _, c := range []struct{ id, output string }{{"mix-bash", "Command running in background with ID: mix-bash-task. Output is being written to: fixture"}, {"mix-monitor", "Monitor started (task mix-monitor-task, expires in 5m)."}, {"mix-agent", "Async agent launched successfully.\nagentId: a1b2c3 (internal)"}} {
		results = append(results, map[string]any{"type": "tool_result", "tool_use_id": c.id, "content": c.output})
	}
	fakeOut(map[string]any{"type": "user", "message": map[string]any{"content": results}})
}
