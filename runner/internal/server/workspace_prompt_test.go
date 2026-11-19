package server

import (
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestLegacyWorkspacePersonMessage(t *testing.T) {
	m := &protocol.WorkspaceManifest{Members: []protocol.WorkspaceMember{{ID: "member", Name: "example", Path: "."}}}
	original := "Review this\n\nKeep my spacing."
	raw, _ := json.Marshal(map[string]any{"text": workspacePrompt(original, m), "source": "chat", "client_id": "client"})
	clean := cleanLegacyWorkspaceMessage(raw, m)
	var payload map[string]any
	if json.Unmarshal(clean, &payload) != nil || payload["text"] != original || payload["client_id"] != "client" {
		t.Fatalf("legacy person message not restored: %s", clean)
	}
	other := &protocol.WorkspaceManifest{Members: []protocol.WorkspaceMember{{ID: "different", Name: "example", Path: "."}}}
	if string(cleanLegacyWorkspaceMessage(raw, other)) != string(raw) {
		t.Fatal("cleaned suffix for another membership")
	}
	untouched := json.RawMessage(`{"text":"Workspace repositories are useful", "source":"chat"}`)
	if string(cleanLegacyWorkspaceMessage(untouched, m)) != string(untouched) {
		t.Fatal("ordinary prose changed")
	}
	if string(cleanLegacyWorkspaceMessage(raw, nil)) != string(raw) {
		t.Fatal("cleaned without trusted membership")
	}
}

func TestChildStartReplayAcceptsLegacyWorkspacePromptHash(t *testing.T) {
	m := &protocol.WorkspaceManifest{OwnerID: "owner", Runtime: "host", Members: []protocol.WorkspaceMember{{ID: "member", Name: "example", Path: "."}}}
	req := runnerStartRequest{ParentSessionID: "parent", Prompt: "Review", Workspace: m}
	legacy := req
	legacy.Prompt = workspacePrompt(req.Prompt, m)
	legacyHash, err := startRequestHash(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if matches, err := childStartHashMatches(req, legacyHash); err != nil || !matches {
		t.Fatalf("old child retry rejected: %v", err)
	}
	currentHash, _ := startRequestHash(req)
	if matches, err := childStartHashMatches(req, currentHash); err != nil || !matches {
		t.Fatalf("current child retry rejected: %v", err)
	}
	req.KeepRunning = true
	if matches, err := childStartHashMatches(req, legacyHash); err != nil || matches {
		t.Fatal("changed policy reused old child")
	}
	req.KeepRunning = false
	req.Prompt = "Different task"
	if matches, err := childStartHashMatches(req, legacyHash); err != nil || matches {
		t.Fatal("different task reused old child")
	}
}
