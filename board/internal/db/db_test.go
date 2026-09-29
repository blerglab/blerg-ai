package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool connects to TEST_DATABASE_URL, runs migrations, and truncates all
// tables so each test starts clean. Tests are skipped without the env var
// (same pattern as blerg-runner).
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	// Automation tokens are sealed at rest; a test key is the default, and the
	// tests about a missing key remove it themselves.
	if err := db.SetAutomationKey(testAutomationKey); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.SetAutomationKey(nil) })
	_, err = pool.Exec(ctx, `TRUNCATE boards, tokens, admission_reviews RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

// testAutomationKey is a fixed 32-byte test key.
var testAutomationKey = []byte("0123456789abcdef0123456789abcdef")

func mkBoard(t *testing.T, pool *pgxpool.Pool, p db.BoardParams) db.Board {
	t.Helper()
	if p.Name == "" {
		p.Name = "test board"
	}
	b, err := db.CreateBoard(context.Background(), pool, p)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	return b
}

func strp(s string) *string { return &s }
func intp(n int) *int       { return &n }

var human = db.EventMeta{Actor: "human"}

func TestBoardCreateDefaults(t *testing.T) {
	pool := testPool(t)
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	if b.GateEnabled {
		t.Error("gate_enabled must default false (stage-2 boards are ungated)")
	}
	cols, err := db.ListColumns(context.Background(), pool, b.ID)
	if err != nil || len(cols) != 4 {
		t.Fatalf("expected 4 default columns, got %d (%v)", len(cols), err)
	}
	if cols[0].Name != "inbox" {
		t.Errorf("first column = %q, want inbox", cols[0].Name)
	}
}

func TestCardNumberAllocation(t *testing.T) {
	pool := testPool(t)
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		res, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
			Title: strp("card"), Repos: &[]string{"blerg-board"},
		}, human)
		if err != nil {
			t.Fatalf("CreateCard %d: %v", i, err)
		}
		if res.Card.Number != i {
			t.Errorf("card %d got number %d", i, res.Card.Number)
		}
	}
}

func TestRepoInvariants(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})

	// require_repo default true: no repos → ErrInvalidRepos.
	_, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: strp("no repo")}, human)
	if !errors.Is(err, db.ErrInvalidRepos) {
		t.Errorf("no-repo create: got %v, want ErrInvalidRepos", err)
	}
	// repo outside board_repos rejected.
	_, err = db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("bad repo"), Repos: &[]string{"not-a-board-repo"}}, human)
	if !errors.Is(err, db.ErrInvalidRepos) {
		t.Errorf("foreign-repo create: got %v, want ErrInvalidRepos", err)
	}

	// require_repo=false board (the blerg-ops case) accepts repo-less cards.
	f := false
	b2 := mkBoard(t, pool, db.BoardParams{Name: "incidents", RequireRepo: &f})
	if _, err := db.CreateCard(ctx, pool, b2.ID, db.CardParams{Title: strp("incident")}, human); err != nil {
		t.Errorf("repo-less create on require_repo=false board: %v", err)
	}
}

// TestRepoLessBoardAcceptsCards: an API-created board with no repos and no
// explicit require_repo must default require_repo=false, so its first card
// doesn't 400 with a message nobody can act on (trial finding). A board
// created WITH repos must keep the require_repo=true default.
func TestRepoLessBoardAcceptsCards(t *testing.T) {
	pool := testPool(t)
	b := mkBoard(t, pool, db.BoardParams{Name: "no-repos"}) // no Repos, RequireRepo nil
	if b.RequireRepo {
		t.Fatal("a board created with no repos must default require_repo=false")
	}
	if _, err := db.CreateCard(context.Background(), pool, b.ID, db.CardParams{Title: strp("first")}, human); err != nil {
		t.Fatalf("repo-less card on repo-less board: %v", err)
	}
	b2 := mkBoard(t, pool, db.BoardParams{Name: "with-repos", Repos: []string{"app"}})
	if !b2.RequireRepo {
		t.Fatal("a board created WITH repos keeps require_repo=true")
	}
}

func TestDedupKeyRefreshesContentOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})

	res1, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("finding v1"), DedupKey: strp("k1"), Repos: &[]string{"blerg-board"},
	}, human)
	if err != nil {
		t.Fatal(err)
	}
	// Human curates: move it out of the intake column.
	cols, _ := db.ListColumns(ctx, pool, b.ID)
	moved, err := db.MoveCard(ctx, pool, res1.Card.ID, cols[2].ID, nil, nil, human)
	if err != nil {
		t.Fatal(err)
	}

	// Sweep re-runs: same dedup_key → refresh, NOT a new card, and NOT a move.
	res2, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("finding v2"), DedupKey: strp("k1"), Repos: &[]string{"blerg-board"},
	}, human)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Refreshed {
		t.Fatal("expected dedup refresh, got a new card")
	}
	if res2.Card.ID != res1.Card.ID {
		t.Error("refresh created a different card")
	}
	if res2.Card.Title != "finding v2" {
		t.Errorf("title not refreshed: %q", res2.Card.Title)
	}
	if res2.Card.ColumnID == nil || *res2.Card.ColumnID != cols[2].ID {
		t.Error("refresh moved the card — it must never undo curation")
	}
	if res2.Card.Version <= moved.Version {
		t.Error("refresh must bump version")
	}

	// Archived cards don't block refiling: archive, then same key creates anew.
	if _, err := db.ArchiveCard(ctx, pool, res1.Card.ID, nil, human); err != nil {
		t.Fatal(err)
	}
	res3, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("finding v3"), DedupKey: strp("k1"), Repos: &[]string{"blerg-board"},
	}, human)
	if err != nil {
		t.Fatalf("refiling after archive: %v", err)
	}
	if res3.Refreshed || res3.Card.ID == res1.Card.ID {
		t.Error("refiling after archive must create a new card")
	}
}

func TestUniformVersioning(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	res, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("c"), Repos: &[]string{"blerg-board"}}, human)
	if err != nil {
		t.Fatal(err)
	}
	card := res.Card
	cols, _ := db.ListColumns(ctx, pool, b.ID)

	// Unguarded move still bumps (blerg-runner's carve-out removed).
	moved, err := db.MoveCard(ctx, pool, card.ID, cols[1].ID, nil, nil, human)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Version != card.Version+1 {
		t.Errorf("unguarded move: version %d → %d, want bump", card.Version, moved.Version)
	}

	// Stale If-Match → ErrStaleVersion.
	_, err = db.UpdateCard(ctx, pool, card.ID, db.CardParams{
		Title: strp("x"), IfMatch: intp(card.Version)}, human)
	if !errors.Is(err, db.ErrStaleVersion) {
		t.Errorf("stale If-Match: got %v, want ErrStaleVersion", err)
	}

	// Archive bumps.
	archived, err := db.ArchiveCard(ctx, pool, card.ID, nil, human)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Version != moved.Version+1 {
		t.Error("archive must bump version")
	}
}

func TestDependencyCycleRefused(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	mk := func(title string) db.Card {
		res, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
			Title: strp(title), Repos: &[]string{"blerg-board"}}, human)
		if err != nil {
			t.Fatal(err)
		}
		return res.Card
	}
	a, bb, c := mk("a"), mk("b"), mk("c")
	if err := db.AddDependency(ctx, pool, a.ID, bb.ID, human); err != nil {
		t.Fatal(err)
	}
	if err := db.AddDependency(ctx, pool, bb.ID, c.ID, human); err != nil {
		t.Fatal(err)
	}
	// c → a would close the cycle a→b→c→a.
	if err := db.AddDependency(ctx, pool, c.ID, a.ID, human); !errors.Is(err, db.ErrDependencyCycle) {
		t.Errorf("cycle: got %v, want ErrDependencyCycle", err)
	}
	// Dependency writes bump BOTH cards.
	a2, _ := db.GetCard(ctx, pool, a.ID)
	b2, _ := db.GetCard(ctx, pool, bb.ID)
	if a2.Version == a.Version || b2.Version == bb.Version {
		t.Error("AddDependency must bump version on both cards")
	}
}

func TestFieldSchemaValidation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	schema := json.RawMessage(`[
		{"key":"severity","type":"enum","values":["sev1","sev2","sev3"],"order":0},
		{"key":"rcca_url","type":"url","order":1}
	]`)
	f := false
	b := mkBoard(t, pool, db.BoardParams{Name: "inc", FieldSchema: schema, RequireRepo: &f})

	// Unknown key rejected, naming the key.
	_, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("x"), Fields: json.RawMessage(`{"sev":"sev1"}`)}, human)
	var fe *db.FieldError
	if !errors.As(err, &fe) || fe.Key != "sev" {
		t.Errorf("unknown key: got %v, want FieldError naming 'sev'", err)
	}
	// Enum outside declared values rejected.
	_, err = db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("x"), Fields: json.RawMessage(`{"severity":"SEV1"}`)}, human)
	if !errors.As(err, &fe) {
		t.Errorf("bad enum value: got %v, want FieldError", err)
	}
	// Valid write accepted.
	if _, err = db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title:  strp("x"),
		Fields: json.RawMessage(`{"severity":"sev1","rcca_url":"https://blerg-ops.example/rcca"}`)}, human); err != nil {
		t.Errorf("valid fields rejected: %v", err)
	}
}

func TestSearch(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	mk := func(title, body string) {
		if _, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
			Title: strp(title), Body: strp(body), Repos: &[]string{"blerg-board"}}, human); err != nil {
			t.Fatal(err)
		}
	}
	mk("add tests for classifyScreen", "screenstate coverage is thin")
	mk("fix websocket reconnect", "keepalive pings")

	got, err := db.SearchCards(ctx, pool, db.SearchParams{BoardID: b.ID, Query: "classifyScreen tests"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Title != "add tests for classifyScreen" {
		t.Errorf("search miss: %+v", got)
	}
	// Trigram catches a short/misspelled query.
	got, err = db.SearchCards(ctx, pool, db.SearchParams{BoardID: b.ID, Query: "websoket reconnect"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Error("trigram search found nothing for 'websoket reconnect'")
	}
}

func TestExternalIDUpsertGuard(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	f := false
	b := mkBoard(t, pool, db.BoardParams{Name: "inc", RequireRepo: &f})
	if _, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("INC one"), ExternalID: strp("INC-20260731-01")}, human); err != nil {
		t.Fatal(err)
	}
	// Same external_id upserts (the driver's idempotent re-push), never errors.
	res, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("INC one again"), ExternalID: strp("INC-20260731-01")}, human)
	if err != nil || !res.Refreshed {
		t.Errorf("duplicate external_id must upsert: err=%v refreshed=%v", err, res.Refreshed)
	}
}

func TestTokenLifecycle(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})

	tok, raw, err := db.MintToken(ctx, pool, &b.ID, "agent", "test agent",
		[]string{"card.read", "card.write"}, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.ValidateToken(ctx, pool, raw)
	if err != nil || got.ID != tok.ID {
		t.Fatalf("validate: %v", err)
	}
	if !got.HasCap("card.write") || got.HasCap("board.admin") {
		t.Error("capability check wrong")
	}

	// Refresh: successor shares lineage; predecessor gains a short fuse.
	succ, rawSucc, err := db.RefreshToken(ctx, pool, got, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if succ.LineageID != got.LineageID {
		t.Error("refresh must preserve lineage")
	}
	if _, err := db.ValidateToken(ctx, pool, rawSucc); err != nil {
		t.Errorf("successor invalid: %v", err)
	}
	if _, err := db.ValidateToken(ctx, pool, raw); err != nil {
		t.Errorf("predecessor must stay valid during the overlap window: %v", err)
	}

	if err := db.RevokeToken(ctx, pool, succ.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ValidateToken(ctx, pool, rawSucc); !errors.Is(err, db.ErrTokenInvalid) {
		t.Error("revoked token must not validate")
	}
}

func TestHeldReviewResolution(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	held := "held"
	exp := time.Now().Add(24 * time.Hour)
	r, err := db.InsertReview(ctx, pool, db.Review{
		BoardID: b.ID, Operation: "create", PayloadHash: []byte{1},
		Payload: json.RawMessage(`{"title":"held card"}`), Verdict: &held,
		PolicyApplied: "held", HeldExpiresAt: &exp,
	})
	if err != nil {
		t.Fatal(err)
	}
	queue, err := db.ListHeldReviews(ctx, pool, b.ID)
	if err != nil || len(queue) != 1 {
		t.Fatalf("held queue: %v (%d)", err, len(queue))
	}
	if _, err := db.ResolveHeldReview(ctx, pool, r.ID, "accept", "human", nil); err != nil {
		t.Fatal(err)
	}
	// Resolution is single-shot.
	if _, err := db.ResolveHeldReview(ctx, pool, r.ID, "accept", "human", nil); !errors.Is(err, db.ErrNotFound) {
		t.Error("second resolve must fail")
	}
}

func TestDisputeBound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	deny := "deny"
	r, err := db.InsertReview(ctx, pool, db.Review{
		BoardID: b.ID, Operation: "create", PayloadHash: []byte{2},
		Payload: json.RawMessage(`{}`), Verdict: &deny, PolicyApplied: "gated",
	})
	if err != nil {
		t.Fatal(err)
	}
	has, err := db.HasDispute(ctx, pool, r.ID)
	if err != nil || has {
		t.Fatalf("fresh review must have no dispute (%v)", err)
	}
	if _, err := db.InsertReview(ctx, pool, db.Review{
		BoardID: b.ID, Operation: "create", PayloadHash: []byte{2},
		Payload: json.RawMessage(`{}`), Verdict: &deny, PolicyApplied: "gated",
		DisputeOf: &r.ID,
	}); err != nil {
		t.Fatal(err)
	}
	has, err = db.HasDispute(ctx, pool, r.ID)
	if err != nil || !has {
		t.Error("dispute not recorded")
	}
}

func TestExternalIDDriverUpsert(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	f := false
	b := mkBoard(t, pool, db.BoardParams{Name: "inc-drive", RequireRepo: &f,
		Columns: []string{"Detected", "Investigating", "Closed"}})
	cols, _ := db.ListColumns(ctx, pool, b.ID)

	svc := db.EventMeta{Actor: "service"}
	res1, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("INC one"), ExternalID: strp("INC-20260731-09"),
		ColumnID: &cols[0].ID}, svc)
	if err != nil {
		t.Fatal(err)
	}
	// Driver re-push with new content and a new column moves + refreshes.
	res2, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("INC one — investigating"), ExternalID: strp("INC-20260731-09"),
		ColumnID: &cols[1].ID}, svc)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Refreshed || res2.Card.ID != res1.Card.ID {
		t.Fatal("external_id re-push must upsert, not duplicate")
	}
	if res2.Card.ColumnID == nil || *res2.Card.ColumnID != cols[1].ID {
		t.Error("driver re-push must move the card to the target column")
	}
	if res2.Card.Title != "INC one — investigating" {
		t.Error("driver re-push must refresh content")
	}
}

func TestSearchAnyWordRecall(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	if _, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("classifyScreen misreads narrow waiting prompts as idle"),
		Body:  strp("the ? prompt is clipped and the classifier falls through"),
		Repos: &[]string{"blerg-board"}}, human); err != nil {
		t.Fatal(err)
	}
	q := "session state detector reports idle when a clipped waiting prompt is on screen"
	// AND semantics (user search) misses the differently-worded card…
	got, err := db.SearchCards(ctx, pool, db.SearchParams{BoardID: b.ID, Query: q})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Logf("AND search unexpectedly matched (fine, but not required)")
	}
	// …but the gate's recall-oriented retrieval must surface it.
	got, err = db.SearchCards(ctx, pool, db.SearchParams{BoardID: b.ID, Query: q, AnyWord: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("AnyWord retrieval must surface the differently-worded duplicate")
	}
}

func TestDeleteBoardWithLiveCards(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	if _, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("live card"), Repos: &[]string{"blerg-board"}}, human); err != nil {
		t.Fatal(err)
	}
	// Live cards referencing live columns must not trip the RESTRICT backstop.
	if err := db.DeleteBoard(ctx, pool, b.ID); err != nil {
		t.Fatalf("DeleteBoard with live cards: %v", err)
	}
	if _, err := db.GetBoard(ctx, pool, b.ID); !errors.Is(err, db.ErrNotFound) {
		t.Error("board still present after delete")
	}
}

// The 'updated' event has to say WHICH fields a patch moved: internal/api's
// spec re-review debounce reads the list and only counts a body rewrite as
// the author working, so that curating a card parked in review (a tag, a
// priority) doesn't buy a whole new adversarial review round.
func TestUpdateCardEventNamesTheChangedFields(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	res, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{
		Title: strp("a spec"), Body: strp("the spec as written"),
		Repos: &[]string{"blerg-board"}, Tags: &[]string{"design"},
	}, human)
	if err != nil {
		t.Fatal(err)
	}
	card := res.Card

	// eventFields returns the field list on the newest 'updated' event.
	eventFields := func() []string {
		events, err := db.ListCardEvents(ctx, pool, card.ID, 0, 500)
		if err != nil {
			t.Fatal(err)
		}
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Type != "updated" {
				continue
			}
			var d struct {
				Fields []string `json:"fields"`
			}
			if err := json.Unmarshal(events[i].Data, &d); err != nil {
				t.Fatalf("event data %s: %v", events[i].Data, err)
			}
			return d.Fields
		}
		t.Fatal("no 'updated' event on the card")
		return nil
	}
	wantFields := func(got, want []string) {
		if len(got) != len(want) {
			t.Fatalf("event fields = %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("event fields = %v, want %v", got, want)
			}
		}
	}

	// a tag-only edit — the curation case
	if _, err := db.UpdateCard(ctx, pool, card.ID,
		db.CardParams{Tags: &[]string{"design", "spike"}}, human); err != nil {
		t.Fatal(err)
	}
	wantFields(eventFields(), []string{"tags"})

	// a body rewrite alongside metadata
	if _, err := db.UpdateCard(ctx, pool, card.ID, db.CardParams{
		Priority: strp("high"), Body: strp("the spec, revised")}, human); err != nil {
		t.Fatal(err)
	}
	wantFields(eventFields(), []string{"body", "priority"})

	// A write that changes nothing still lands an event, naming nothing.
	// Order, padding and duplicate tags are not edits either — they land in
	// the DB normalized.
	if _, err := db.UpdateCard(ctx, pool, card.ID, db.CardParams{
		Title: strp("a spec"), Body: strp("the spec, revised"),
		Tags: &[]string{" spike ", "design", "design"}}, human); err != nil {
		t.Fatal(err)
	}
	wantFields(eventFields(), []string{})

	// An empty list is not the same as no list: newestAuthorActivity reads
	// the key's absence as "written before blerg-board recorded field names" and
	// counts the event, so a no-op write must still carry the key. This is
	// also the jsonb_exists() shape that query relies on.
	var hasKey bool
	if err := pool.QueryRow(ctx, `
		SELECT jsonb_exists(data, 'fields') FROM card_events
		WHERE card_id = $1 AND type = 'updated' ORDER BY id DESC LIMIT 1`,
		card.ID).Scan(&hasKey); err != nil {
		t.Fatal(err)
	}
	if !hasKey {
		t.Error("a no-op update wrote an event with no 'fields' key — indistinguishable from a legacy event")
	}
}
