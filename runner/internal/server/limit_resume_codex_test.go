package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// An unattended resume must use the retained owner's Codex identity, while
// retaining the same credential boundary as a fresh personal session.
func TestAutomaticCodexClusterResumeUsesOnlyPersonalCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, repo string
		git        bool
	}{
		{name: "repository", repo: "org/proj", git: true},
		{name: "scratch", repo: ".scratch-example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const personalCodex = `{"tokens":{"access_token":"invented-codex-fixture"}}`
			const personalGit = "invented-personal-git-fixture"
			backend := &fakeK8s{credentialFetchResponses: map[string][]byte{"owner:codex": []byte(personalCodex)}}
			if tc.git {
				backend.credentialFetchResponses["owner:github"] = []byte(personalGit)
			}
			api, _, pool := clusterRunnerAPI(t, backend)
			api.hub.JobManager().OAuthSecretName = "operator-account-credentials"
			ctx := context.Background()
			resp, problem := api.StartSession(ctx, runnerPrincipal{Kind: runnerKeyPrincipalKind}, runnerStartRequest{Repo: "org/proj", Prompt: "work"}, "")
			if problem != nil {
				t.Fatal(problem)
			}
			if _, err := pool.Exec(ctx, `UPDATE sessions SET status='disconnected',spawning_account_id='owner',engine='codex',repo=$2 WHERE id=$1`, resp.SessionID, tc.repo); err != nil {
				t.Fatal(err)
			}

			// The previous pod exited; a disconnected DB row alone cannot replace a live Job.
			backend.finishSessionJob(resp.SessionID)

			backend.mu.Lock()
			beforeJobs, beforeSecrets := len(backend.created), len(backend.createdSecrets)
			backend.credentialFetchSessionIDs = nil
			backend.credentialFetchTokenIDs = nil
			backend.mu.Unlock()
			if err := resumeClusterSessionStrict(ctx, api.hub, pool, resp.SessionID, limitResumePrompt, "owner", testSID); err != nil {
				t.Fatalf("personal Codex resume rejected: %v", err)
			}

			backend.mu.Lock()
			defer backend.mu.Unlock()
			if len(backend.created) != beforeJobs+1 || len(backend.createdSecrets) != beforeSecrets+1 {
				t.Fatal("resume did not create exactly one replacement Job and credential Secret")
			}
			env := jobEnv(t, backend.created[len(backend.created)-1])
			if got := env["BLERG_RUNNER_ENGINE"]["value"]; got != "codex" {
				t.Fatalf("engine=%v", got)
			}

			// Decode the fake's API round-trip to avoid depending on map types.
			raw, err := json.Marshal(backend.createdSecrets[len(backend.createdSecrets)-1])
			if err != nil {
				t.Fatal(err)
			}
			var secret struct {
				StringData map[string]string `json:"stringData"`
			}
			if err = json.Unmarshal(raw, &secret); err != nil {
				t.Fatal(err)
			}
			// The replacement has live, separate runtime and messaging authority.
			runtime, err := db.ValidateRuntimeToken(ctx, pool, secret.StringData["BLERG_RUNNER_DAEMON_TOKEN"])
			if err != nil || runtime.SessionID != resp.SessionID {
				t.Fatal("replacement lost scoped runtime authority")
			}
			message, err := db.ValidateBoardToken(ctx, pool, secret.StringData[sessionTokenEnv])
			if err != nil || message.SessionID != resp.SessionID || message.ID == runtime.ID || len(message.Capabilities) != 1 || message.Capabilities[0] != "message" {
				t.Fatal("replacement lost separate messaging authority")
			}
			if secret.StringData["CODEX_AUTH_JSON"] != personalCodex {
				t.Fatal("replacement Secret lost the owner's Codex credential")
			}
			expectedSecret := sessionSecretName(resp.SessionID)
			assertPersonalRef := func(key string) {
				t.Helper()
				entry, ok := env[key]
				if !ok {
					t.Fatalf("missing personal credential env %s", key)
				}
				valueFrom, ok := entry["valueFrom"].(map[string]any)
				if !ok {
					t.Fatalf("%s is not a Secret reference", key)
				}
				ref, ok := valueFrom["secretKeyRef"].(map[string]any)
				if !ok || ref["name"] != expectedSecret || ref["key"] != key {
					t.Fatalf("%s does not reference its per-session Secret", key)
				}
			}
			assertPersonalRef("CODEX_AUTH_JSON")
			assertPersonalRef("BLERG_RUNNER_INITIAL_PROMPT_SOURCE")
			assertPersonalRef("BLERG_RUNNER_INITIAL_PROMPT")
			if secret.StringData["BLERG_RUNNER_INITIAL_PROMPT_SOURCE"] != limitResumeSource {
				t.Fatal("automatic continuation source was lost")
			}
			if secret.StringData["BLERG_RUNNER_INITIAL_PROMPT"] != limitResumePrompt {
				t.Fatal("automatic continuation prompt was lost")
			}

			for _, key := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "HERMES_ENV_CONTENTS"} {
				if _, ok := env[key]; ok {
					t.Errorf("unrelated operator credential %s entered Codex pod", key)
				}
				if _, ok := secret.StringData[key]; ok {
					t.Errorf("unrelated credential %s entered per-session Secret", key)
				}
			}
			if tc.git {
				assertPersonalRef("BLERG_RUNNER_GIT_TOKEN")
				if secret.StringData["BLERG_RUNNER_GIT_TOKEN"] != personalGit {
					t.Fatal("repository resume lost personal git credential")
				}
			} else {
				if _, ok := env["BLERG_RUNNER_GIT_TOKEN"]; ok {
					t.Fatal("scratch resume injected git credentials")
				}
				if _, ok := secret.StringData["BLERG_RUNNER_GIT_TOKEN"]; ok {
					t.Fatal("scratch credential Secret contains git credential")
				}
			}
			wantFetches := 1
			if tc.git {
				wantFetches++
			}
			if len(backend.credentialFetchSessionIDs) != wantFetches {
				t.Fatalf("credential fetches=%d, want %d", len(backend.credentialFetchSessionIDs), wantFetches)
			}
			for i, sid := range backend.credentialFetchSessionIDs {
				if sid != testSID {
					t.Fatal("resume did not use retained browser-session proof")
				}
				if backend.credentialFetchTokenIDs[i] != "" {
					t.Fatal("automatic resume substituted agent-token authority")
				}
			}
		})
	}
}
