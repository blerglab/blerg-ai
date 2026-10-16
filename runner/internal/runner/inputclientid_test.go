package runner

import "testing"

func TestInitialPromptClientIDFromEnvironment(t *testing.T) {
	t.Setenv("BLERG_RUNNER_INITIAL_PROMPT_CLIENT_ID", "browser-input-123")
	if got := ConfigFromEnv().InitialPromptClientID; got != "browser-input-123" {
		t.Fatalf("lost prompt correlation: %q", got)
	}
}
