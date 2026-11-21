package api_test

import (
	"encoding/json"
	"net/url"
	"strconv"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestReviewOlderTagFilterAndPages(t *testing.T) {
	srv, pool := testServer(t)
	ctx := t.Context()
	b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Filter review"})
	if err != nil {
		t.Fatal(err)
	}
	col, err := db.CreateColumn(ctx, pool, b.ID, "Finished", true)
	if err != nil {
		t.Fatal(err)
	}
	tags := []string{"to-test"}
	for range 3 {
		title := "Verify later"
		r, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &col.ID, Tags: &tags}, db.EventMeta{Actor: "human"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `UPDATE cards SET column_entered_at=now()-interval '30 days' WHERE id=$1`, r.Card.ID); err != nil {
			t.Fatal(err)
		}
	}
	filter := url.QueryEscape(`{"q":"","tag":["to-test"],"type":[],"priority":[]}`)
	path := "/api/boards/" + b.ID + "/cards/view?filter=" + filter + "&limit=2"
	resp := request(t, srv, "GET", path, adminToken(t), nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("filter status %d", resp.StatusCode)
	}
	var page struct {
		Cards   []db.Card         `json:"cards"`
		Older   map[string]int    `json:"older"`
		Matched map[string]int    `json:"matched"`
		Totals  map[string]int    `json:"totals"`
		Next    map[string]string `json:"next"`
		Chips   []struct {
			Facet, Value string
			Count        int
		} `json:"chips"`
	}
	decodeBody(t, resp, &page)
	if len(page.Cards) != 2 || page.Older[col.ID] != 3 || page.Matched[col.ID] != 3 || page.Totals[col.ID] != 3 || page.Next[col.ID] == "" {
		t.Fatalf("filtered page: %+v", page)
	}
	found := false
	for _, chip := range page.Chips {
		if chip.Facet == "tag" && chip.Value == "to-test" && chip.Count == 3 {
			found = true
		}
	}
	if !found {
		t.Fatal("older to-test tag missing from chips")
	}
	first := map[string]bool{}
	for _, c := range page.Cards {
		first[c.ID] = true
	}
	resp = request(t, srv, "GET", path+"&older_column="+col.ID+"&cursor="+page.Next[col.ID], adminToken(t), nil, nil)
	decodeBody(t, resp, &page)
	if len(page.Cards) != 1 || first[page.Cards[0].ID] {
		t.Fatalf("second page: %+v", page)
	}
}

func TestReviewServerFilterMatchesBrowser(t *testing.T) {
	srv, pool := testServer(t)
	ctx := t.Context()
	b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Filter parity"})
	if err != nil {
		t.Fatal(err)
	}
	col, err := db.CreateColumn(ctx, pool, b.ID, "Finished", true)
	if err != nil {
		t.Fatal(err)
	}
	title := "a\u0903b" // A spacing combining mark: the browser removes every Unicode mark category.
	card, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &col.ID}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE cards SET column_entered_at=now()-interval '30 days' WHERE id=$1`, card.Card.ID); err != nil {
		t.Fatal(err)
	}
	for i, q := range []string{"ab", "+" + strconv.Itoa(card.Card.Number)} {
		raw, err := json.Marshal(map[string]string{"q": q})
		if err != nil {
			t.Fatal(err)
		}
		resp := request(t, srv, "GET", "/api/boards/"+b.ID+"/cards/view?filter="+url.QueryEscape(string(raw)), adminToken(t), nil, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("filter=%q status=%d", q, resp.StatusCode)
		}
		var page struct{ Cards []db.Card }
		decodeBody(t, resp, &page)
		want := 1 - i
		if len(page.Cards) != want {
			t.Errorf("filter=%q returned %d cards, want %d", q, len(page.Cards), want)
		}
	}
}
