package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestAgentPublishesCommitSummary(t *testing.T) {
	w := newDiffWorld(t)
	id := w.ids["A"]["card_id"]
	args, _ := json.Marshal(map[string]any{"card_id": id, "commit_summary": []any{map[string]any{"repository": "example-org/app", "session_id": "0b9f3c1e-6a41-4c57-9d0e-2f4a7c8b1e55", "branch": "work/card", "base": "main", "commits": []any{}, "files": []any{}}}})
	result, err := w.srv.dispatch(t.Context(), w.scoped, "blerg_card_update", args)
	if err != nil {
		t.Fatal(err)
	}
	var card db.Card
	if err := json.Unmarshal([]byte(contentText(result)), &card); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(card.CommitSummary), "example-org/app") {
		t.Fatalf("summary = %s", card.CommitSummary)
	}
	w.scoped.Token.Capabilities = []string{"card.read"}
	if _, err := w.srv.dispatch(t.Context(), w.scoped, "blerg_card_update", args); err == nil {
		t.Fatal("card reader could publish metadata")
	}
}
