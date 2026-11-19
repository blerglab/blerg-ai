package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// Opt-in cross-service fixture driven by the runner's capture integration test.
func TestCaptureBridge(t *testing.T) {
	if os.Getenv("BLERG_CAPTURE_BRIDGE") != "1" {
		t.Skip("runner-owned cross-service fixture")
	}
	w := newDiffWorld(t)
	if err := w.srv.api.Auth.EnsureServiceToken(t.Context()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(w.srv)
	defer srv.Close()
	var number int
	if err := w.pool.QueryRow(t.Context(), `SELECT number FROM cards WHERE id=$1`, w.ids["A"]["card_id"]).Scan(&number); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"url": srv.URL, "board": w.ids["A"]["board_id"], "number": number})
	fmt.Println("CAPTURE_BRIDGE " + string(raw))
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	card, err := db.GetCard(t.Context(), w.pool, w.ids["A"]["card_id"])
	if err != nil {
		t.Fatal(err)
	}
	var summaries []db.CommitSummary
	if err := json.Unmarshal(card.CommitSummary, &summaries); err != nil || len(summaries) != 1 || summaries[0].Commits[0].Message != "Delivered live" {
		t.Fatalf("live card summary: %s (%v)", card.CommitSummary, err)
	}
}
