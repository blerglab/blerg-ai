package daemon

import "testing"

// The hello/heartbeat report mirrors the sandbox spawn's own credential
// check: false exactly when that spawn would be refused.
func TestSandboxClaudeCredentialReport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	if got := sandboxClaudeCredentialReport(); got == nil || *got {
		t.Fatalf("no login, no token, no key: report = %v, want false", got)
	}

	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth")
	if got := sandboxClaudeCredentialReport(); !*got {
		t.Error("CLAUDE_CODE_OAUTH_TOKEN set: report false")
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")

	t.Setenv("ANTHROPIC_API_KEY", "key")
	if got := sandboxClaudeCredentialReport(); !*got {
		t.Error("ANTHROPIC_API_KEY set: report false")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")

	writeClaudeLogin(t, home)
	if got := sandboxClaudeCredentialReport(); !*got {
		t.Error("host ~/.claude login: report false")
	}
}
