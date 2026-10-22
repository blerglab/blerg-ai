package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The agent-reopen rule against the vectors the browser (TypeScript) and blerg-runner (Python)
// test too.
func TestAgentReopenRuleSharedVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packages", "chat", "src", "model", "testdata", "agent-reopen-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	type thread struct {
		Status       string            `json:"status"`
		Revision     json.RawMessage   `json:"revision"`
		AgentReopens []json.RawMessage `json:"agentReopens"`
	}
	var v struct {
		Text []struct {
			Text   string `json:"text"`
			Repeat int    `json:"repeat"`
			Valid  bool   `json:"valid"`
		} `json:"text"`
		Revision []struct {
			JSON  string `json:"json"`
			Valid bool   `json:"valid"`
		} `json:"revision"`
		Applies []struct {
			Name    string `json:"name"`
			Person  thread `json:"person"`
			Agent   thread `json:"agent"`
			Applies bool   `json:"applies"`
		} `json:"applies"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Text) == 0 || len(v.Revision) == 0 || len(v.Applies) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range v.Text {
		n := c.Repeat
		if n == 0 {
			n = 1
		}
		if got := validReopenText(strings.Repeat(c.Text, n)); got != c.Valid {
			t.Errorf("text %q×%d: got %v, want %v", c.Text, n, got, c.Valid)
		}
	}
	for _, c := range v.Revision {
		if _, got := validRevision(json.RawMessage(c.JSON)); got != c.Valid {
			t.Errorf("revision %s: got %v, want %v", c.JSON, got, c.Valid)
		}
	}
	for _, c := range v.Applies {
		personRev, ok := threadRevision(c.Person.Revision)
		if !ok {
			t.Fatalf("%s: person revision %s", c.Name, c.Person.Revision)
		}
		if _, got := reopenApplies(c.Person.Status, personRev, c.Agent.Status, c.Agent.Revision, c.Agent.AgentReopens); got != c.Applies {
			t.Errorf("%s: got %v, want %v", c.Name, got, c.Applies)
		}
	}
}
