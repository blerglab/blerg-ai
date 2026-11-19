package db_test

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"testing"
)

func TestCardEstimatesFirstSet(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	var p db.CardParams
	if err := json.Unmarshal([]byte(`{"title":"Estimate first","repos":["blerg-board"],"size":"M","risk":"high","complexity":"low"}`), &p); err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateCard(ctx, pool, b.ID, p, human)
	if err != nil {
		t.Fatal(err)
	}
	data := func(c db.Card) map[string]any {
		raw, _ := json.Marshal(c)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m
	}
	matches, err := db.SearchCards(ctx, pool, db.SearchParams{BoardID: b.ID, Risk: "high", Complexity: "low", Size: "M"})
	if err != nil || len(matches) != 1 {
		t.Fatalf("estimate filters: %v %v", matches, err)
	}
	for _, filter := range []db.SearchParams{{Size: "XL"}, {Risk: "low"}, {Complexity: "high"}} {
		filter.BoardID = b.ID
		matches, err := db.SearchCards(ctx, pool, filter)
		if err != nil || len(matches) != 0 {
			t.Fatalf("mismatched estimate filter was ignored: %v %v", matches, err)
		}
	}
	first := data(res.Card)
	for _, k := range []string{"risk", "complexity", "size_first_set_at", "risk_first_set_at", "complexity_first_set_at"} {
		if first[k] == nil || first[k] == "" {
			t.Fatalf("missing %s: %v", k, first)
		}
	}
	_ = json.Unmarshal([]byte(`{"size":"","risk":"","complexity":""}`), &p)
	c, err := db.UpdateCard(ctx, pool, res.Card.ID, p, human)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(`{"size":"L","risk":"medium","complexity":"high"}`), &p)
	c, err = db.UpdateCard(ctx, pool, c.ID, p, human)
	if err != nil {
		t.Fatal(err)
	}
	got := data(c)
	for _, k := range []string{"size_first_set_at", "risk_first_set_at", "complexity_first_set_at"} {
		if got[k] != first[k] {
			t.Fatalf("%s changed: %v => %v", k, first[k], got[k])
		}
	}
	if _, err := db.UpdateCard(ctx, pool, c.ID, db.CardParams{Size: strp("XS")}, human); err == nil {
		t.Fatal("new XS estimate accepted")
	}
	p = db.CardParams{}
	_ = json.Unmarshal([]byte(`{"risk":"extreme"}`), &p)
	if _, err := db.UpdateCard(ctx, pool, c.ID, p, human); err == nil {
		t.Fatal("invalid risk accepted")
	}
}

func TestCardFlowRetainsColumnMeaning(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: strp("Flow"), Repos: &[]string{"blerg-board"}}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MoveCard(ctx, pool, res.Card.ID, cols[2].ID, nil, nil, human); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE board_columns SET name='Later name' WHERE id=$1`, cols[2].ID); err != nil {
		t.Fatal(err)
	}
	flow, err := db.CardFlow(ctx, pool, res.Card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(flow) != 2 || flow[1].InProgress == nil || !*flow[1].InProgress || flow[1].ColumnName == nil || *flow[1].ColumnName != "in progress" {
		t.Fatalf("flow changed: %+v", flow)
	}
}

func TestLegacyEstimateTimeStaysUnknownAfterRevision(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	res, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: strp("Legacy"), Size: strp("M"), Repos: &[]string{"blerg-board"}}, human)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the migrated state: an estimate existed before timestamp collection.
	if _, err := pool.Exec(ctx, `ALTER TABLE cards DISABLE TRIGGER card_estimate_first_set; UPDATE cards SET size_first_set_at=NULL; ALTER TABLE cards ENABLE TRIGGER card_estimate_first_set`); err != nil {
		t.Fatal(err)
	}
	got, err := db.UpdateCard(ctx, pool, res.Card.ID, db.CardParams{Size: strp("L")}, human)
	if err != nil {
		t.Fatal(err)
	}
	if got.SizeFirstSetAt != nil {
		t.Fatal("a revision fabricated the unknown first-set time")
	}
	for _, size := range []string{"", "XL"} {
		got, err = db.UpdateCard(ctx, pool, res.Card.ID, db.CardParams{Size: &size}, human)
		if err != nil {
			t.Fatal(err)
		}
		if got.SizeFirstSetAt != nil {
			t.Fatal("clearing a legacy estimate fabricated its original time")
		}
	}

}

func TestLegacyXSSizeRoundTrips(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	var params db.CardParams
	_ = json.Unmarshal([]byte(`{"title":"Legacy estimate","repos":["blerg-board"]}`), &params)
	res, err := db.CreateCard(ctx, pool, b.ID, params, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE cards SET size='XS' WHERE id=$1`, res.Card.ID); err != nil {
		t.Fatal(err)
	}
	card, err := db.UpdateCard(ctx, pool, res.Card.ID, db.CardParams{Size: strp("XS"), Risk: strp("low")}, human)
	if err != nil {
		t.Fatal(err)
	}
	if card.Size == nil || *card.Size != "XS" || card.Risk == nil || *card.Risk != "low" {
		t.Fatalf("round trip: %+v", card)
	}
}
