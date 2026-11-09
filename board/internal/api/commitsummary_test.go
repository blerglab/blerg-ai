package api_test

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"net/http"
	"testing"
)

// The public card path proves serialization and durable storage without a runner.
func TestCardCommitSummarySurvivesWithoutWorkspace(t *testing.T) {
	_, id, do, pool := artifactCard(t, "")
	path := "/api/cards/" + id
	// Older/imported cards can have version zero; the first guarded write must work.
	if _, err := pool.Exec(t.Context(), `UPDATE cards SET version=0 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	resp := do("PATCH", path, map[string]any{"commit_summary": []any{map[string]any{"patch": "private diff bytes"}}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("patch field: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	summary := []any{map[string]any{"repository": "example-org/app", "session_id": artSession, "branch": "work/card", "base": "main", "commits": []any{map[string]any{"sha": "abcdef1234567890abcdef1234567890abcdef12", "message": "Land work", "author": "Example", "date": "2026-11-01T00:00:00Z"}}, "files": []any{map[string]any{"path": "app.go", "adds": 3, "dels": 1}}}}
	resp = do("PATCH", path, map[string]any{"if_match": 0, "commit_summary": summary})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = do("GET", path, nil)
	var card map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	got, _ := json.Marshal(card["commit_summary"])
	want, _ := json.Marshal(summary)
	if string(got) != string(want) {
		t.Fatalf("durable summary = %s, want %s", got, want)
	}
	// A literal zero is a guard, never interpreted as absent.
	resp = do("PATCH", path, map[string]any{"if_match": 0, "commit_summary": []any{}})
	if resp.StatusCode != 409 {
		t.Fatalf("version 0: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = do("PATCH", path, map[string]any{"if_match": card["version"], "commit_summary": []any{}})
	if resp.StatusCode != 200 {
		t.Fatalf("clear: %d", resp.StatusCode)
	}
	var cleared map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&cleared)
	_ = resp.Body.Close()
	if v, ok := cleared["commit_summary"].([]any); !ok || len(v) != 0 {
		t.Fatalf("empty list = %#v", cleared["commit_summary"])
	}
}

func TestInvalidSummaryNeverReachesAdmission(t *testing.T) {
	srv, pool, board, _, token := testServerHeld(t)
	res, err := db.CreateCard(t.Context(), pool, board.ID, db.CardParams{Title: strPtr("Existing work")}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM admission_reviews`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/cards/" + res.Card.ID, "/api/boards/" + board.ID + "/cards"} {
		method := "PATCH"
		if path == "/api/boards/"+board.ID+"/cards" {
			method = "POST"
		}
		resp := request(t, srv, method, path, token, nil, map[string]any{"title": "Publish work", "commit_summary": []any{map[string]any{"patch": "private bytes"}}})
		_ = resp.Body.Close()
		if resp.StatusCode != 422 {
			t.Fatalf("invalid summary %s: %d", method, resp.StatusCode)
		}
	}
	var after int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM admission_reviews`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("invalid summaries persisted admission payloads: %d -> %d", before, after)
	}
}
