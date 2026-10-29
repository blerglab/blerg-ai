package api_test

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestSearchCursorPagingAndKeys(t *testing.T) {
	srv, pool := testServer(t)
	token := adminToken(t)
	key := "QA"
	b, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Paging", Key: &key})
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 205; n++ {
		title := fmt.Sprintf("Paging work %d", n)
		tags := []string{"release"}
		if _, err := db.CreateCard(t.Context(), pool, b.ID, db.CardParams{Title: &title, Tags: &tags}, db.EventMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	// Equal timestamps exercise the ID tie breaker at every page boundary.
	if _, err := pool.Exec(t.Context(), "UPDATE cards SET updated_at = '2026-01-01' WHERE board_id = $1", b.ID); err != nil {
		t.Fatal(err)
	}
	path := "/api/boards/" + b.ID + "/cards/search"
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 3; page++ {
		resp := request(t, srv, "GET", path+"?page=true&limit=100&q=Paging&tag=release&cursor="+url.QueryEscape(cursor), token, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("search: %d", resp.StatusCode)
		}
		var got struct {
			Cards      []db.Card `json:"cards"`
			NextCursor string    `json:"next_cursor"`
		}
		decodeBody(t, resp, &got)
		want := 100
		if page == 2 {
			want = 5
		}
		if len(got.Cards) != want {
			t.Fatalf("page %d: got %d cards", page, len(got.Cards))
		}
		for _, c := range got.Cards {
			if seen[c.ID] || len(c.Tags) != 1 {
				t.Fatalf("duplicate or missing tags: %+v", c)
			}
			seen[c.ID] = true
		}
		cursor = got.NextCursor
		if (cursor == "") != (page == 2) {
			t.Fatalf("page %d cursor %q", page, cursor)
		}
	}
	renamed := "NEW"
	if _, err := db.UpdateBoard(t.Context(), pool, b.ID, db.BoardParams{Key: &renamed}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"QA-10", "NEW-10", "#10", "10"} {
		resp := request(t, srv, "GET", path+"?q="+url.QueryEscape(query), token, nil, nil)
		var cards []db.Card
		decodeBody(t, resp, &cards)
		if len(cards) != 1 || cards[0].Number != 10 {
			t.Fatalf("query %q: %+v", query, cards)
		}
	}
	// Admission's recall-oriented search continues to treat numeric prose as text.
	title := "404 fault"
	if _, err := db.CreateCard(t.Context(), pool, b.ID, db.CardParams{Title: &title}, db.EventMeta{}); err != nil {
		t.Fatal(err)
	}
	matches, err := db.SearchCards(t.Context(), pool, db.SearchParams{BoardID: b.ID, Query: "404", AnyWord: true})
	if err != nil || len(matches) != 1 || matches[0].Title != title {
		t.Fatalf("admission text search = %+v: %v", matches, err)
	}

	resp := request(t, srv, "GET", path+"?page=true&cursor=invalid", token, nil, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid cursor: %d", resp.StatusCode)
	}
	resp.Body.Close()
}
