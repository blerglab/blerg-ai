package main

import (
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/coreauth"
	"github.com/blerglab/blerg-ai/board/internal/gate"
	"github.com/blerglab/blerg-ai/board/internal/localinfer"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// Gate backend selection: account mode when BLERG_BOARD_GATE_ACCOUNT_TOKEN is
// set (and then ONLY the account's credential, never the operator's), legacy
// operator env otherwise, and boot-time refusal of half-configured account
// mode.
func TestGateBackendSelection(t *testing.T) {
	core := coreauth.NewClient("http://core.invalid")
	operatorBox := localinfer.New(localinfer.Config{BaseURL: "http://operator-box.invalid"})
	account := map[string]string{
		"BLERG_BOARD_GATE_ACCOUNT_TOKEN": "agent.jwt.token",
		"BLERG_BOARD_GATE_ENGINE":        "hermes",
		"BLERG_CORE_URL":                 "http://core.invalid",
		"BLERG_BOARD_CORE_INTERNAL_KEY":  "internal-key",
		// present, and must be ignored in account mode:
		"ANTHROPIC_API_KEY": "sk-ant-api-operator",
	}

	t.Run("account mode uses only the account backend", func(t *testing.T) {
		backends, tiebreak, err := gateBackends(envMap(account), operatorBox, core)
		if err != nil {
			t.Fatal(err)
		}
		if len(backends) != 1 {
			t.Fatalf("%d backends, want exactly the account's", len(backends))
		}
		pb, ok := backends[0].(*gate.PersonalBackend)
		if !ok || pb.Name() != "hermes" {
			t.Fatalf("backend = %T %s, want the personal hermes backend", backends[0], backends[0].Name())
		}
		if tiebreak != backends[0] {
			t.Fatalf("tiebreak = %T, want the same account backend (not the operator's Claude key)", tiebreak)
		}
	})

	t.Run("legacy mode when unset", func(t *testing.T) {
		backends, tiebreak, err := gateBackends(envMap(map[string]string{"ANTHROPIC_API_KEY": "sk-ant-api-operator"}), operatorBox, core)
		if err != nil {
			t.Fatal(err)
		}
		if len(backends) != 2 || backends[0].Name() != "openai" || backends[1].Name() != "claude" {
			t.Fatalf("legacy backends = %v", backends)
		}
		if _, ok := tiebreak.(*gate.ClaudeBackend); !ok {
			t.Fatalf("legacy tiebreak = %T", tiebreak)
		}
	})

	for name, mutate := range map[string]func(m map[string]string){
		"codex engine":        func(m map[string]string) { m["BLERG_BOARD_GATE_ENGINE"] = "codex" },
		"no engine":           func(m map[string]string) { delete(m, "BLERG_BOARD_GATE_ENGINE") },
		"no internal key":     func(m map[string]string) { delete(m, "BLERG_BOARD_CORE_INTERNAL_KEY") },
		"no core":             func(m map[string]string) { delete(m, "BLERG_CORE_URL") },
		"engine but no token": func(m map[string]string) { delete(m, "BLERG_BOARD_GATE_ACCOUNT_TOKEN") },
	} {
		t.Run("refuses boot: "+name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range account {
				m[k] = v
			}
			mutate(m)
			c := core
			if m["BLERG_CORE_URL"] == "" {
				c = nil
			}
			_, _, err := gateBackends(envMap(m), operatorBox, c)
			if err == nil {
				t.Fatal("half-configured account mode booted")
			}
			if strings.Contains(err.Error(), "agent.jwt.token") {
				t.Fatal("error leaks the token")
			}
		})
	}
}
