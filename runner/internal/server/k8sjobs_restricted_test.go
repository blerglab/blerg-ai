package server

// F3 MAJOR 2: a restricted session (a cron, a session holding an MCP grant, any RestrictTools
// session) runs claude with file tools only and never needs another engine's secrets, yet its pod
// used to be wired with the operator's codex and hermes credentials. They are withheld now.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func jobBody(t *testing.T, f *fakeK8s) string {
	t.Helper()
	if len(f.created) != 1 {
		t.Fatalf("jobs = %d, want 1", len(f.created))
	}
	raw, err := json.Marshal(f.created[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRestrictedSessionsGetNoOtherEngineSecrets(t *testing.T) {
	grant := &protocol.MCPGatewayConfig{BaseURL: "http://gw:9100", Servers: []protocol.MCPGatewayServer{{Name: "cal", Token: "tok-x"}}}
	cases := map[string]func(*SessionJobSpec){
		"cron":     func(s *SessionJobSpec) { s.NoOperatorFallback, s.RestrictTools = true, true },
		"grant":    func(s *SessionJobSpec) { s.MCPGateway = grant },
		"restrict": func(s *SessionJobSpec) { s.RestrictTools = true },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeK8s{credentialFetchResponses: map[string][]byte{"acct-1:claude": []byte("sk-ant-oat-personal")}}
			jm := newTestJobManager(t, f)
			spec := cronJobSpec("s-" + name)
			spec.NoOperatorFallback = false
			mod(&spec)
			if err := jm.CreateSessionJob(spec); err != nil {
				t.Fatal(err)
			}
			body := jobBody(t, f)
			for _, key := range []string{"CODEX_AUTH_JSON", "HERMES_ENV_CONTENTS"} {
				if strings.Contains(body, key) {
					t.Errorf("a restricted session's Job names %s: %s", key, body)
				}
			}
			// What it strictly needs is still there.
			for _, key := range []string{"BLERG_RUNNER_DAEMON_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
				if !strings.Contains(body, key) {
					t.Errorf("a restricted session lost %s: %s", key, body)
				}
			}
		})
	}
}

// Ordinary sessions retain their selected provider credentials while unrelated
// provider authority stays out of every pod, including unrestricted sessions.
func TestPlainSessionGetsOnlySelectedEngineSecrets(t *testing.T) {
	for _, tc := range []struct {
		engine   string
		selected []string
	}{
		{"claude", []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}},
		{"codex", []string{"CODEX_AUTH_JSON"}},
		{"hermes", []string{"HERMES_ENV_CONTENTS"}},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			f := &fakeK8s{secrets: map[string]map[string]string{"blerg-runner-agent": {"CODEX_AUTH_JSON": b64(`{"OPENAI_API_KEY":"fixture-api-private"}`)}}}
			jm := newTestJobManager(t, f)
			spec := cronJobSpec("s-plain-" + tc.engine)
			spec.NoOperatorFallback = false
			spec.Engine = tc.engine
			if err := jm.CreateSessionJob(spec); err != nil {
				t.Fatal(err)
			}
			env := jobEnv(t, f.created[0])
			for _, key := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "CODEX_AUTH_JSON", "HERMES_ENV_CONTENTS"} {
				selected := false
				for _, want := range tc.selected {
					selected = selected || key == want
				}
				_, present := env[key]
				if present != selected {
					t.Errorf("provider credential %s present=%v, selected=%v", key, present, selected)
				}
			}
			for _, key := range []string{"BLERG_RUNNER_DAEMON_TOKEN", sessionTokenEnv} {
				entry, ok := env[key]
				if !ok {
					t.Fatalf("missing scoped launch credential %s", key)
				}
				source, _ := entry["valueFrom"].(map[string]any)
				ref, _ := source["secretKeyRef"].(map[string]any)
				if ref["name"] != sessionSecretName(spec.SessionID) || ref["key"] != key {
					t.Errorf("%s does not reference the per-session Secret", key)
				}
			}
		})
	}
}
