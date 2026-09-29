package db_test

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dispatchFixture is a default board (inbox → ready → in progress → done) with
// its columns resolved by role, plus helpers for filing ranked cards and wiring
// dependency edges. Cards are ranked in creation order, so the first card filed
// into ready is the one a rank-only picker would take.
type dispatchFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	boardID string

	ready    db.Column
	work     db.Column
	terminal db.Column
}

func newDispatchFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	pool := testPool(t)
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	cols, err := db.ListColumns(context.Background(), pool, b.ID)
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	f := &dispatchFixture{t: t, pool: pool, boardID: b.ID}
	for _, c := range cols {
		switch {
		case c.Name == "ready":
			f.ready = c
		case c.Name == "in progress":
			f.work = c
		case c.Terminal() && f.terminal.ID == "":
			f.terminal = c
		}
	}
	if f.ready.ID == "" || f.work.ID == "" || f.terminal.ID == "" {
		t.Fatalf("fixture wants ready/in progress/terminal columns, got %+v", cols)
	}
	return f
}

// card files one card into a column. Successive calls rank later.
func (f *dispatchFixture) card(title string, col db.Column) db.Card {
	f.t.Helper()
	res, err := db.CreateCard(context.Background(), f.pool, f.boardID, db.CardParams{
		Title: strp(title), Repos: &[]string{"blerg-board"}, ColumnID: &col.ID,
	}, db.EventMeta{Actor: "agent"})
	if err != nil {
		f.t.Fatalf("create %q: %v", title, err)
	}
	return res.Card
}

func (f *dispatchFixture) dependsOn(card, blocker db.Card) {
	f.t.Helper()
	if err := db.AddDependency(context.Background(), f.pool, card.ID, blocker.ID,
		db.EventMeta{Actor: "agent"}); err != nil {
		f.t.Fatalf("AddDependency: %v", err)
	}
}

func (f *dispatchFixture) moveTo(c db.Card, col db.Column) {
	f.t.Helper()
	if _, err := db.MoveCard(context.Background(), f.pool, c.ID, col.ID, nil, nil,
		db.EventMeta{Actor: "service"}); err != nil {
		f.t.Fatalf("move #%d to %q: %v", c.Number, col.Name, err)
	}
}

func (f *dispatchFixture) top() (db.Card, error) {
	return db.TopDispatchable(context.Background(), f.pool, f.ready.ID)
}

// mustTop asserts the picker offers exactly the card named by want.
func (f *dispatchFixture) mustTop(want db.Card, why string) {
	f.t.Helper()
	got, err := f.top()
	if err != nil {
		f.t.Fatalf("%s: TopDispatchable: %v", why, err)
	}
	if got.ID != want.ID {
		f.t.Fatalf("%s: picked #%d %q, want #%d %q", why, got.Number, got.Title, want.Number, want.Title)
	}
}

// mustOfferNothing asserts ready holds nothing a worker could be started on.
func (f *dispatchFixture) mustOfferNothing(why string) {
	f.t.Helper()
	if got, err := f.top(); err == nil {
		f.t.Fatalf("%s: picker offered #%d %q, want nothing dispatchable", why, got.Number, got.Title)
	}
}

// Regression: the picker must attach extras — without repos loaded, the
// board-run dispatcher mistook every card for repo-less and stuck-flagged the
// entire ready queue in one tick.
func TestTopDispatchableLoadsRepos(t *testing.T) {
	f := newDispatchFixture(t)
	f.card("toc card", f.ready)
	top, err := f.top()
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	if len(top.Repos) == 0 {
		t.Fatal("TopDispatchable returned no repos — dispatcher would stuck-flag the card")
	}
}

// The headline case: A ranks above B but depends on it. Rank must not decide
// the order. Before the predicate existed, the dispatcher handed A to a worker
// that then built on a base which did not exist yet.
func TestTopDispatchableIgnoresRankWhenABlockerIsPending(t *testing.T) {
	f := newDispatchFixture(t)
	a := f.card("A — depends on B", f.ready)
	b := f.card("B — the blocker", f.ready)
	f.dependsOn(a, b)

	f.mustTop(b, "A outranks B but depends on it")

	// A blocker that has merely left ready is not finished, it is being
	// worked. Handing out A here is exactly the concurrency>1 hazard: two
	// workers in one repo, one of them on a base that does not exist.
	f.moveTo(b, f.work)
	f.mustOfferNothing("blocker is in progress, not done")

	f.moveTo(b, f.terminal)
	f.mustTop(a, "blocker reached a terminal column")
}

// Skip, don't stall: a blocked card at the top of ready must not halt the board
// behind it. Returning it (or returning nothing) would freeze every card below.
func TestTopDispatchableSkipsPastABlockedCard(t *testing.T) {
	f := newDispatchFixture(t)
	blocker := f.card("blocker, being worked", f.work)
	blocked := f.card("blocked, top of ready", f.ready)
	free := f.card("free, ranked below it", f.ready)
	f.dependsOn(blocked, blocker)

	f.mustTop(free, "top-of-ready card is blocked, the next one is not")
}

// A ready column made entirely of blocked cards offers nothing — and says so,
// so a board that cannot start anything reads as blocked rather than as idle.
func TestTopDispatchableReportsAnAllBlockedReadyColumn(t *testing.T) {
	f := newDispatchFixture(t)
	blocker := f.card("blocker, being worked", f.work)
	one := f.card("blocked one", f.ready)
	two := f.card("blocked two", f.ready)
	f.dependsOn(one, blocker)
	f.dependsOn(two, blocker)

	f.mustOfferNothing("every ready card is blocked")

	n, err := db.CountBlocked(context.Background(), f.pool, f.ready.ID)
	if err != nil {
		t.Fatalf("CountBlocked: %v", err)
	}
	if n != 2 {
		t.Fatalf("CountBlocked = %d, want 2 — the run status could not say the board is blocked", n)
	}
}

// Archiving a blocker clears the edge. The alternative — a dangling edge to a
// card no longer on the board blocking forever — strands the dependent with
// nothing a human can click through to.
func TestTopDispatchableTreatsAnArchivedBlockerAsSatisfied(t *testing.T) {
	f := newDispatchFixture(t)
	dependent := f.card("dependent", f.ready)
	blocker := f.card("blocker, about to be archived", f.ready)
	f.dependsOn(dependent, blocker)
	f.mustTop(blocker, "blocker still live")

	if _, err := db.ArchiveCard(context.Background(), f.pool, blocker.ID, nil,
		db.EventMeta{Actor: "human"}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	f.mustTop(dependent, "blocker archived out of the flow")
}

// Only direct blockers are checked, and that suffices: in A→B→C, A cannot slip
// through early while C is pending, because B is itself undispatchable.
func TestTopDispatchableHoldsATransitiveChain(t *testing.T) {
	f := newDispatchFixture(t)
	a := f.card("A", f.ready)
	b := f.card("B", f.ready)
	c := f.card("C", f.ready)
	f.dependsOn(a, b)
	f.dependsOn(b, c)

	f.mustTop(c, "deepest card in the chain goes first")
	f.moveTo(c, f.terminal)
	f.mustTop(b, "C landed, B is free")
	f.moveTo(b, f.terminal)
	f.mustTop(a, "B landed, A is free")
}

// A done-named column with is_terminal unset still clears a blocker: the SQL
// predicate and Column.Terminal are one definition, so a board cannot disagree
// with itself about what "finished" means.
func TestBlockerClearsOnADoneNamedColumnWithoutTheFlag(t *testing.T) {
	f := newDispatchFixture(t)
	legacy, err := db.CreateColumn(context.Background(), f.pool, f.boardID, "Shipped & Done", false)
	if err != nil {
		t.Fatalf("create column: %v", err)
	}
	if legacy.IsTerminal {
		t.Fatal("fixture wants the flag off, so only the name can clear the blocker")
	}
	if !legacy.Terminal() {
		t.Fatal("Column.Terminal must accept a done-named column with the flag off")
	}

	dependent := f.card("dependent", f.ready)
	blocker := f.card("blocker", f.ready)
	f.dependsOn(dependent, blocker)
	f.moveTo(blocker, legacy)
	f.mustTop(dependent, "blocker landed in a done-named column")
}

// The resolved Blockers a card carries are what the UI renders, so they must
// name the blocker and track whether it has landed — and the batch read must
// agree with the single read.
func TestCardCarriesResolvedBlockers(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	dependent := f.card("dependent", f.ready)
	pending := f.card("pending blocker", f.ready)
	landed := f.card("landed blocker", f.ready)
	f.dependsOn(dependent, pending)
	f.dependsOn(dependent, landed)
	f.moveTo(landed, f.terminal)

	got, err := db.GetCard(ctx, f.pool, dependent.ID)
	if err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	if len(got.Blockers) != 2 || len(got.DependsOn) != 2 {
		t.Fatalf("GetCard: %d blockers / %d depends_on, want 2 and 2 (depends_on must stay populated)",
			len(got.Blockers), len(got.DependsOn))
	}
	byNumber := map[int]db.Blocker{}
	for _, b := range got.Blockers {
		byNumber[b.Number] = b
	}
	if b := byNumber[pending.Number]; b.Satisfied || b.Title != "pending blocker" {
		t.Fatalf("pending blocker resolved as %+v, want unsatisfied and titled", b)
	}
	if b := byNumber[landed.Number]; !b.Satisfied {
		t.Fatalf("landed blocker resolved as %+v, want satisfied", b)
	}

	all, err := db.ListCards(ctx, f.pool, f.boardID, false)
	if err != nil {
		t.Fatalf("ListCards: %v", err)
	}
	for _, c := range all {
		if c.ID != dependent.ID {
			continue
		}
		if len(c.Blockers) != 2 {
			t.Fatalf("ListCards: %d blockers, want 2 — batch and single reads disagree", len(c.Blockers))
		}
		for _, b := range c.Blockers {
			if b.Satisfied != byNumber[b.Number].Satisfied {
				t.Fatalf("ListCards disagrees with GetCard on blocker #%d", b.Number)
			}
		}
		return
	}
	t.Fatal("ListCards did not return the dependent card")
}
