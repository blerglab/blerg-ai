package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInitialPromptClientIDIsCarriedInSessionJob(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s-input-id", Repo: "proj", InitialPrompt: "Continue", InitialPromptClientID: "browser-input-123", Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.createdSecrets) != 1 {
		t.Fatalf("created secrets: %d", len(f.createdSecrets))
	}
	body, _ := json.Marshal(f.createdSecrets[0])
	if !strings.Contains(string(body), `"BLERG_RUNNER_INITIAL_PROMPT_CLIENT_ID":"browser-input-123"`) {
		t.Fatalf("missing prompt correlation in session secret: %s", body)
	}
	job, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(job), `"key":"BLERG_RUNNER_INITIAL_PROMPT_CLIENT_ID"`) {
		t.Fatal("job does not reference prompt correlation")
	}
}
