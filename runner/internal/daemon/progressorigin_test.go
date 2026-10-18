package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestClaudeProgressOriginRequiresLiveBashCall(t *testing.T) {
	d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	for _, p := range []string{"", "child-run"} {
		var line ccLine
		raw, _ := json.Marshal(map[string]any{"type": "assistant", "parent_tool_use_id": p, "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "bash-call", "name": "Bash", "input": map[string]string{"command": "script"}}}}})
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatal(err)
		}
		d.emitAssistantBlocks(&line)
		parent, child, ok := d.resolveProgressOrigin("bash-call", "")
		if !ok || parent != p || child != "" {
			t.Fatalf("origin parent=%q child=%q ok=%v", parent, child, ok)
		}
		var result ccLine
		if err := json.Unmarshal([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"bash-call","content":"done"}]}}`), &result); err != nil {
			t.Fatal(err)
		}
		d.emitToolResults(&result)
		if _, _, ok := d.resolveProgressOrigin("bash-call", ""); ok {
			t.Fatal("ended Bash retained attribution")
		}
	}
	if _, _, ok := d.resolveProgressOrigin("foreign", ""); ok {
		t.Fatal("foreign call accepted")
	}
}

func TestClaudeProgressHookIsExplicitAndUnrestrictedOnly(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		opts := []ccOption{withSessionGuide()}
		if restricted {
			opts = append(opts, withRestrictTools())
		}
		for _, args := range [][]string{ccTurnArgs("work", "", "", "", opts...), ccSessionArgs("", "", "", opts...)} {
			found := false
			for _, arg := range args {
				if strings.HasPrefix(arg, "--settings=") {
					found = true
					var v map[string]any
					if json.Unmarshal([]byte(strings.TrimPrefix(arg, "--settings=")), &v) != nil || !strings.Contains(arg, "_progress_scope_hook") {
						t.Fatal("invalid progress hook settings")
					}
				}
			}
			if found == restricted {
				t.Fatalf("hook settings found=%v restricted=%v", found, restricted)
			}
		}
	}
}

func TestCodexProgressOriginValidatesOwnedActiveChild(t *testing.T) {
	a, _ := codexPlanFixture()
	if _, _, ok := a.d.resolveProgressOrigin("foreign-call", "fixture-root"); ok {
		t.Fatal("Codex accepted Claude call identity")
	}
	a.d.children = map[string]*codexChild{"child-thread": {callID: "child-run"}}
	a.childTurns = map[string]string{"child-thread": "child-turn"}
	if parent, child, ok := a.d.resolveProgressOrigin("", "child-thread"); !ok || parent != "" || child != "child-run" {
		t.Fatalf("origin %q %q %v", parent, child, ok)
	}
	if parent, child, ok := a.d.resolveProgressOrigin("", "fixture-root"); !ok || parent != "" || child != "" {
		t.Fatal("root origin not accepted")
	}
	a.endChildThread("child-thread")
	if _, _, ok := a.d.resolveProgressOrigin("", "child-thread"); ok {
		t.Fatal("ended native turn accepted")
	}
	for _, id := range []string{"foreign", "", strings.Repeat("x", 257)} {
		if _, _, ok := a.d.resolveProgressOrigin("", id); ok {
			t.Fatal("unknown identity accepted")
		}
	}
}

func TestProgressCallsBoundAndDiscardIncarnations(t *testing.T) {
	var p progressCalls
	p.start("same", "first")
	p.start("same", "second")
	if p.calls["same"] != "first" {
		t.Fatal("duplicate call changed attribution")
	}
	for i := 0; i < 300; i++ {
		p.start(fmt.Sprintf("call-%d", i), "")
	}
	if len(p.calls) != 256 {
		t.Fatal("unbounded live calls")
	}
	p.clear()
	if len(p.calls) != 0 {
		t.Fatal("process exit kept stale calls")
	}
	p.start("new", "new-child")
	if len(p.calls) != 1 || p.calls["new"] != "new-child" {
		t.Fatal("fresh process not isolated")
	}
}

func TestNativeProgressIdentityNeverLeaksBetweenSessionLaunches(t *testing.T) {
	for key := range sessionOriginEnvKeys {
		t.Setenv(key, "fixture-prior-session")
	}
	for _, entry := range sanitizedEnviron() {
		key, _, _ := strings.Cut(entry, "=")
		if sessionOriginEnvKeys[key] {
			t.Fatalf("inherited native identity key %s", key)
		}
	}
	for key := range sessionOriginEnvKeys {
		if validExtraEnvKey(key) {
			t.Fatalf("caller can inject native identity key %s", key)
		}
	}
}
