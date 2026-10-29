package server

import "testing"

// A broker (the board) sees a desktop session's start steps by their fixed names, never a
// stage's own label, detail or hint, which can carry the desktop's or a repository's name.
func TestSanitizedStartStagesKeepFixedNames(t *testing.T) {
	payload := map[string]any{"stages": []any{
		map[string]any{"id": "queued", "label": "Preparing session on raven-desktop", "detail": "/home/x", "hint": "h", "state": "done"},
		map[string]any{"id": "plugins", "label": "Installing 3 plugins for repo-x", "state": "active"},
		map[string]any{"id": "engine", "label": "Starting claude in /home/x", "state": "pending"},
		map[string]any{"id": "something_new", "label": "Secret thing", "state": "pending"},
	}}
	sanitizeDesktopAutomaticEvent("start_stage", payload)
	want := []string{"Preparing session", "Installing plugins", "Starting the agent", "Preparing session"}
	for i, value := range payload["stages"].([]any) {
		stage := value.(map[string]any)
		if stage["label"] != want[i] {
			t.Fatalf("stage %d label = %v, want %q", i, stage["label"], want[i])
		}
		if _, ok := stage["detail"]; ok {
			t.Fatalf("stage %d kept its detail", i)
		}
		if _, ok := stage["hint"]; ok {
			t.Fatalf("stage %d kept its hint", i)
		}
	}
	if payload["metadata_hidden"] != true {
		t.Fatal("metadata_hidden not set")
	}
}
