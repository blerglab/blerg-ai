package gate

import (
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// Regression coverage for the live-dogfood bug: the curator applied the
// CREATE duplicate rubric to move/update/archive, denying valid mutations
// ("Card 2 already exists in the inbox column", "Empty submission") because
// it never saw the target card. Fixed by an op-aware rubric plus threading
// the current card into the prompt.

func testCard() *db.Card {
	body := "body text"
	col := "col-1"
	return &db.Card{
		ID: "card-1", Number: 2, ColumnID: &col, Type: "bug",
		Title: "some card", Body: &body, Tags: []string{"gate"},
	}
}

func TestBuildPromptMutationRubricOmitsDuplicateLanguage(t *testing.T) {
	for _, op := range []string{"update", "move", "archive", "delete"} {
		in := Input{
			Board: db.Board{Name: "b"}, Operation: op,
			Payload: []byte(`{}`), Current: testCard(),
		}
		prompt := buildPrompt(in)
		if strings.Contains(prompt, `"deny" for semantic duplicates`) {
			t.Errorf("op %s: mutation prompt must not carry the create-only duplicate rule", op)
		}
		if !strings.Contains(prompt, "Duplicate detection does NOT apply to mutations") {
			t.Errorf("op %s: mutation prompt must state duplicate detection is create-only", op)
		}
		if !strings.Contains(prompt, "ACCEPT by default") {
			t.Errorf("op %s: mutation prompt must default to accept", op)
		}
		if !strings.Contains(prompt, `"card-1"`) && !strings.Contains(prompt, `"number":2`) {
			t.Errorf("op %s: prompt must include the target card, got:\n%s", op, prompt)
		}
	}
}

func TestBuildPromptCreateRubricKeepsDuplicateLanguage(t *testing.T) {
	in := Input{
		Board: db.Board{Name: "b"}, Operation: "create",
		Payload: []byte(`{"title":"x"}`),
	}
	prompt := buildPrompt(in)
	if !strings.Contains(prompt, `"deny" for semantic duplicates`) {
		t.Error("create prompt must keep the duplicate rule")
	}
	if strings.Contains(prompt, "Duplicate detection does NOT apply to mutations") {
		t.Error("create prompt must not carry the mutation rubric")
	}
}

func TestBuildPromptOmitsCurrentCardOnCreate(t *testing.T) {
	in := Input{
		Board: db.Board{Name: "b"}, Operation: "create",
		Payload: []byte(`{"title":"x"}`),
	}
	prompt := buildPrompt(in)
	if strings.Contains(prompt, "The card being modified:") {
		t.Error("create has no target card; prompt must not claim one")
	}
}
