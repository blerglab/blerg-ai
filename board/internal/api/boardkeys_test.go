package api_test

import (
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestBoardKeySuggestionAndArchivedNumberRead(t *testing.T) {
	srv, pool := testServer(t)
	admin := adminToken(t)
	var b db.Board
	resp := request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "Blerg"})
	decodeBody(t, resp, &b)
	resp = request(t, srv, "GET", "/api/board-key-suggestion?name=Blerg", admin, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("suggestion read: %d", resp.StatusCode)
	}
	var suggestion struct {
		Key string `json:"key"`
	}
	decodeBody(t, resp, &suggestion)
	if suggestion.Key != "BLG1" {
		t.Fatalf("suggestion: %s", suggestion.Key)
	}
	resp = request(t, srv, "GET", "/api/board-key-suggestion?name=Blerg", memberToken(t), nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member suggestion: %d", resp.StatusCode)
	}
	resp.Body.Close()
	title := "Archived work"
	created, err := db.CreateCard(t.Context(), pool, b.ID, db.CardParams{Title: &title}, db.EventMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ArchiveCard(t.Context(), pool, created.Card.ID, nil, db.EventMeta{}); err != nil {
		t.Fatal(err)
	}
	resp = request(t, srv, "GET", "/api/boards/"+b.ID+"/cards/by-number/1", memberToken(t), nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archived read: %d", resp.StatusCode)
	}
	var card db.Card
	decodeBody(t, resp, &card)
	if card.ID != created.Card.ID || card.ArchivedAt == nil || card.Reference != "BLG-1" {
		t.Fatalf("archived card: %+v", card)
	}
	other := projectToken(t, "other-board", []string{"card.read"})
	resp = request(t, srv, "GET", "/api/boards/"+b.ID+"/cards/by-number/1", other, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-board read: %d", resp.StatusCode)
	}
	resp.Body.Close()
}
