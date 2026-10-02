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

// An ordinary session keeps every credential it always had.
func TestPlainSessionStillGetsEngineSecrets(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	spec := cronJobSpec("s-plain2")
	spec.NoOperatorFallback = false
	if err := jm.CreateSessionJob(spec); err != nil {
		t.Fatal(err)
	}
	body := jobBody(t, f)
	for _, key := range []string{"CODEX_AUTH_JSON", "HERMES_ENV_CONTENTS", "BLERG_RUNNER_DAEMON_TOKEN"} {
		if !strings.Contains(body, key) {
			t.Errorf("a plain session lost %s: %s", key, body)
		}
	}
}
