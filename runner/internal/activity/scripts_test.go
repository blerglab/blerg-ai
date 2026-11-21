package activity

import (
	"encoding/json"
	"testing"
)

func TestFollowGeneratedScriptOnlyWhenExecuted(t *testing.T) {
	s := scriptSignals{}
	input := func(c string) json.RawMessage { raw, _ := json.Marshal(map[string]string{"cmd": c}); return raw }
	if got := s.classify(input("/bin/bash -lc \"cat > /tmp/gate.sh <<'END'\ngo test ./...\nEND\"")); got != "other" {
		t.Fatal(got)
	}
	if got := s.classify(input("/bin/bash -lc 'bash /tmp/gate.sh'")); got != "tests" {
		t.Fatalf("generated script: %s", got)
	}
	if got := s.classify(input("python3 - <<'END'\nprint('go test ./...')\nEND")); got != "other" {
		t.Fatal(got)
	}
	if got := s.classify(input("python3 - <<'END'\nsubprocess.run(['go','test','./...'])\nEND")); got != "tests" {
		t.Fatal(got)
	}
}
func TestShellWrapperDecodesEscapedQuotesWithoutExpansion(t *testing.T) {
	input := json.RawMessage(`{"cmd":"bash -lc \"cat > /tmp/g.sh <<'END'\nrun_gate() { shift; \\\"$@\\\"; }\nrun_gate unit go test ./...\nEND\nbash /tmp/g.sh\""}`)
	if got := (scriptSignals{}).classify(input); got != "tests" {
		t.Fatalf("escaped shell script = %s", got)
	}
}
