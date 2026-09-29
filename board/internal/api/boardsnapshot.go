package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/jackc/pgx/v5"
)

// Board snapshot: the block of board state rendered into a board chat's brief
// at spawn. Without it every board session opens by re-reading the same five
// endpoints (board, schema, columns, cards, /onboard) before it can answer
// anything; a one-line-per-card index costs ~1K tokens and replaces all of
// those round-trips.
//
// Two deliberate omissions:
//
//   - card BODIES — an order of magnitude larger than the index and mostly
//     unread; they stay fetch-on-demand.
//   - card VERSIONS — the snapshot is written once and a chat can stay open
//     for hours while the run dispatcher moves cards underneath it. A session
//     that passed a stale version as if_match would collect spurious 409s and
//     learn to stop guarding its writes. Versions come from a live read only.
//
// The renderer is pure: the caller gathers the rows, so the prompt builders
// stay DB-free (see internal/api/prompt_test.go, which renders every role
// against a nil pool).

const (
	// snapshotTerminalCap bounds a terminal column (done/archive-like): only
	// the most recently updated cards are listed, the rest counted.
	snapshotTerminalCap = 20
	// snapshotColumnCap bounds any other column — generous, since live
	// columns are the ones a chat is asked about, but not unbounded.
	snapshotColumnCap = 150
	// snapshotTitleMax truncates long titles, in runes.
	snapshotTitleMax = 110
)

// boardSnapshotInput is everything renderBoardSnapshot needs. Columns are in
// rank order; Cards are the board's live (non-archived) cards.
type boardSnapshotInput struct {
	Board   db.Board
	Columns []db.Column
	Cards   []db.Card
	Run     string // one-line run-dispatcher state
	Now     time.Time
}

// renderBoardSnapshot renders the board-state block. It returns "" for a
// zero-value board (nothing useful to say), so callers can drop it into the
// template unconditionally.
func renderBoardSnapshot(in boardSnapshotInput) string {
	if in.Board.ID == "" && len(in.Columns) == 0 && len(in.Cards) == 0 {
		return ""
	}
	var b strings.Builder
	stamp := in.Now.UTC().Format(time.RFC3339)
	fmt.Fprintf(&b, "## Board snapshot — taken %s, when this session was spawned\n\n", stamp)
	b.WriteString(`This is a point-in-time copy of the board, not live state: other sessions
and the run dispatcher move cards while you talk, so it ages from the moment
you read it. Answer questions about what is on the board from it — that is
what it is for — but RE-READ anything you are about to change (GET
/api/cards/{id}) and take the ` + "`if_match`" + ` version from that fresh read.
Card versions are deliberately not listed here for exactly that reason, and
card bodies are omitted for size — fetch the ones you need.

Identifiers: every REST path under /api/cards/ takes the card's UUID, never
its "#42" number, so each index line below ends with the id to use — no
lookup call needed. (The blerg_* MCP tools, if you have them, take board_id
+ number instead.)

`)

	fmt.Fprintf(&b, "- board id: %s\n", in.Board.ID)
	fmt.Fprintf(&b, "- repos: %s\n", orNone(strings.Join(in.Board.Repos, ", ")))
	fmt.Fprintf(&b, "- git_base: %s\n", orDefault(in.Board.GitBase, "(server default)"))
	fmt.Fprintf(&b, "- deploy_url: %s\n", orDefault(derefStr(in.Board.DeployURL), "(none)"))
	fmt.Fprintf(&b, "- admission gate: %s\n", enabledWord(in.Board.GateEnabled))
	if in.Run != "" {
		fmt.Fprintf(&b, "- board run: %s\n", in.Run)
	}
	fmt.Fprintf(&b, "- field_schema: %s\n", compactSchema(in.Board.FieldSchema))

	b.WriteString("\n### Columns (rank order)\n\n")
	if len(in.Columns) == 0 {
		b.WriteString("(none)\n")
	}
	for i, c := range in.Columns {
		terminal := ""
		if c.IsTerminal {
			terminal = " — terminal"
		}
		fmt.Fprintf(&b, "%d. %s%s\n", i+1, c.Name, terminal)
	}

	fmt.Fprintf(&b, "\n### Cards (%d live, one line each)\n", len(in.Cards))
	if len(in.Cards) == 0 {
		b.WriteString("\nNo live cards on this board.\n")
		return b.String()
	}
	byColumn := map[string][]db.Card{}
	var loose []db.Card
	for _, c := range in.Cards {
		if c.ColumnID == nil {
			loose = append(loose, c)
			continue
		}
		byColumn[*c.ColumnID] = append(byColumn[*c.ColumnID], c)
	}
	for _, col := range in.Columns {
		writeColumnCards(&b, col.Name, byColumn[col.ID], col.IsTerminal)
		delete(byColumn, col.ID)
	}
	// cards pointing at a column we were not handed, plus unfiled ones —
	// listing them under a catch-all beats silently dropping them
	for _, cards := range byColumn {
		loose = append(loose, cards...)
	}
	if len(loose) > 0 {
		sort.SliceStable(loose, func(i, j int) bool { return loose[i].Number < loose[j].Number })
		writeColumnCards(&b, "(no column)", loose, false)
	}
	return b.String()
}

// writeColumnCards writes one column's group: a header carrying the column
// name and its true card count, the listed lines, and — when the cap bit — an
// explicit count of what was left out. Silent truncation would read to the
// session as a complete board.
func writeColumnCards(b *strings.Builder, name string, cards []db.Card, terminal bool) {
	limit := snapshotColumnCap
	shown := cards
	note := ""
	if terminal {
		limit = snapshotTerminalCap
	}
	if len(cards) > limit {
		if terminal {
			// keep the freshest ones: a done column's tail is history
			shown = append([]db.Card(nil), cards...)
			sort.SliceStable(shown, func(i, j int) bool { return shown[i].UpdatedAt > shown[j].UpdatedAt })
			note = ", most recently updated shown"
		}
		shown = shown[:limit]
	}
	fmt.Fprintf(b, "\n**%s** (%d%s)\n\n", name, len(cards), note)
	for _, c := range shown {
		fmt.Fprintf(b, "- #%d %s%s%s%s\n", c.Number, typeTag(c.Type), snapshotTitle(c.Title), blockedTag(c), idSuffix(c.ID))
	}
	if n := len(cards) - len(shown); n > 0 {
		fmt.Fprintf(b, "… %d more cards in %s, not shown\n", n, name)
	}
}

// idSuffix appends the card's uuid to its index line. REST resolves cards by
// uuid only — there is no by-number route — so a snapshot that listed numbers
// alone would send the session back to GET /api/boards/{id}/cards for every
// mutation, which is the round-trip this whole block exists to remove.
func idSuffix(id string) string {
	if id == "" {
		return ""
	}
	return " (id " + id + ")"
}

// blockedTag names the dependencies still holding a card out of dispatch, so a
// session reading the snapshot sees why a card sitting in ready is not moving.
func blockedTag(c db.Card) string {
	var nums []string
	for _, b := range c.Blockers {
		if !b.Satisfied {
			nums = append(nums, fmt.Sprintf("#%d", b.Number))
		}
	}
	if len(nums) == 0 {
		return ""
	}
	return " [blocked by " + strings.Join(nums, ", ") + "]"
}

func typeTag(t string) string {
	if strings.TrimSpace(t) == "" {
		return ""
	}
	return "[" + t + "] "
}

// snapshotTitle collapses whitespace and caps length: a card title is
// nominally one line, but the index must stay one line per card regardless.
func snapshotTitle(title string) string {
	t := strings.Join(strings.Fields(title), " ")
	if r := []rune(t); len(r) > snapshotTitleMax {
		t = strings.TrimRight(string(r[:snapshotTitleMax]), " ") + "…"
	}
	return t
}

// compactSchema prints the board's declared custom fields as compact JSON —
// the same shape GET /api/boards/{id}/schema returns, so a session can write
// `fields` straight off it.
func compactSchema(raw json.RawMessage) string {
	var buf bytes.Buffer
	if len(raw) > 0 {
		if err := json.Compact(&buf, raw); err != nil {
			return "(unreadable)"
		}
	}
	switch s := buf.String(); s {
	case "", "null", "[]", "{}":
		return "(no custom fields declared)"
	default:
		return s
	}
}

func orNone(s string) string { return orDefault(s, "(none)") }

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func enabledWord(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// boardSnapshot gathers the live rows and renders the snapshot block. Any
// query failure degrades to "" — a board chat with no snapshot behaves
// exactly as it did before this existed, which beats failing the spawn — but
// it is logged: a persistent failure here is otherwise invisible, every board
// chat silently reverting to re-reading the API with nothing saying why.
func (a *API) boardSnapshot(ctx context.Context, board db.Board) string {
	if a.Pool == nil {
		return ""
	}
	cols, err := db.ListColumns(ctx, a.Pool, board.ID)
	if err != nil {
		log.Printf("board snapshot %s: list columns: %v (spawning brief without it)", board.ID, err)
		return ""
	}
	cards, err := db.ListCards(ctx, a.Pool, board.ID, false)
	if err != nil {
		log.Printf("board snapshot %s: list cards: %v (spawning brief without it)", board.ID, err)
		return ""
	}
	return renderBoardSnapshot(boardSnapshotInput{
		Board:   board,
		Columns: cols,
		Cards:   cards,
		Run:     a.runStateLine(ctx, board.ID),
		Now:     time.Now(),
	})
}

// runStateLine summarises the run dispatcher for the snapshot — the same
// numbers GET /api/boards/{id}/run reports. Only a genuine no-rows means
// "never run"; any other error returns "" so the caller omits the line
// entirely, because a brief that asserts something false about the board is
// worse than one that says nothing about it.
func (a *API) runStateLine(ctx context.Context, boardID string) string {
	var state string
	var done, stuck int
	err := a.Pool.QueryRow(ctx, `
		SELECT state, cards_done, cards_stuck FROM board_runs
		WHERE board_id = $1 ORDER BY started_at DESC LIMIT 1`, boardID).Scan(&state, &done, &stuck)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "never run"
	case err != nil:
		log.Printf("board snapshot %s: read run state: %v (omitting the line)", boardID, err)
		return ""
	}
	ready, inflight, blocked := a.runCounts(ctx, boardID)
	// A running board at concurrency 0 is parked, and saying only "running"
	// would have the chat tell a human their board is working when it is
	// deliberately dispatching nothing.
	concurrency := a.boardConcurrency(ctx, boardID)
	dial := fmt.Sprintf("%d at a time", concurrency)
	if state == "running" && concurrency == 0 {
		dial = "PARKED — concurrency 0, so nothing new is dispatched; PATCH the board's concurrency to resume"
	}
	blockedNote := ""
	if blocked > 0 {
		blockedNote = fmt.Sprintf(", %d of them blocked on a dependency", blocked)
	}
	return fmt.Sprintf("%s, %s (%d done, %d stuck this run; %d ready%s, %d in flight now)",
		state, dial, done, stuck, ready, blockedNote, inflight)
}
