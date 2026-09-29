package db

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrStaleVersion is returned by UpdateTicket / MoveTicket when the caller
// supplies an expectVersion that does not match the ticket's current version.
var ErrStaleVersion = errors.New("stale version")

// Validation sentinels returned (wrapped) from CreateTicket / UpdateTicket when
// ticket input fails validation. Handlers map these to 400 via errors.Is.
var (
	// ErrInvalidPriority is returned when a priority value is not one of the
	// allowed enum values.
	ErrInvalidPriority = errors.New("invalid priority")
	// ErrInvalidSize is returned when a size value is not one of the allowed
	// enum values.
	ErrInvalidSize = errors.New("invalid size")
	// ErrInvalidRepos is returned when the repo set is empty or contains a repo
	// that is not a member of the board.
	ErrInvalidRepos = errors.New("invalid repos")
)

// ─── Row / input types ────────────────────────────────────────────────────────

// TicketRow is a row from the tickets table.
// Tags, Repos, and Blocked are populated by ListTickets; other callers return
// zero values for these fields.
type TicketRow struct {
	ID         string
	BoardID    string
	ColumnID   *string
	Title      string
	Body       *string
	Priority   string
	Size       *string
	Rank       string
	ArchivedAt *time.Time
	SessionID  *string
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// Populated by ListTickets (batch-fetched; zero values for other callers).
	Tags    []string
	Repos   []string
	Blocked bool
}

// TicketDetail extends TicketRow with the ticket's associated collections.
type TicketDetail struct {
	TicketRow
	Repos     []string // ordered by ticket_repos.rank asc
	Tags      []string // alphabetical
	DependsOn []string // ticket IDs this ticket depends on
	Blocks    []string // ticket IDs that depend on this ticket
}

// TicketInput is the input for CreateTicket.
type TicketInput struct {
	BoardID  string
	Title    string
	Body     *string
	Repos    []string
	ColumnID *string
	Tags     []string
	Priority string
	Size     *string
}

// TicketFilter is the filter for ListTickets.
type TicketFilter struct {
	ColumnID     *string
	Tags         []string
	Priority     *string
	Size         *string
	Ready        bool
	ArchivedOnly bool // when true: return archived tickets ordered by archived_at DESC
	Limit        int
	Cursor       string
}

// TicketPatch is the set of changes for UpdateTicket.
//
// Scalar fields (Title, Body, Priority, Size) are version-guarded when
// expectVersion is non-nil. Repos (whole-set replace) is also version-guarded.
// Additive set ops (AddTags/RmTags/AddRepos/RmRepos) are idempotent and do
// not check or bump the version.
type TicketPatch struct {
	Title    *string
	Body     *string
	Priority *string
	Size     *string
	AddTags  []string
	RmTags   []string
	AddRepos []string
	RmRepos  []string
	Repos    *[]string // whole-set replace (version-guarded; ≥1 repo; ⊆ board repos)
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

const ticketCols = `id, board_id, column_id, title, body, priority, size, rank, archived_at, session_id, version, created_at, updated_at`

// blockedPredicate is the SQL EXISTS condition (true when ticket alias `t` has
// at least one unsatisfied dependency: a blocker that is neither archived nor in
// a terminal column). Shared by ListTickets and GetTicketCard so the "blocked"
// card flag stays consistent with the Ready filter.
const blockedPredicate = `EXISTS (
	SELECT 1
	  FROM ticket_dependencies td
	  JOIN tickets blocker ON blocker.id = td.depends_on_ticket_id
	  LEFT JOIN columns bc ON bc.id = blocker.column_id
	 WHERE td.ticket_id = t.id
	   AND blocker.archived_at IS NULL
	   AND COALESCE(bc.is_terminal, false) = false
)`

func scanTicketRow(row pgx.Row) (TicketRow, error) {
	var r TicketRow
	err := row.Scan(
		&r.ID, &r.BoardID, &r.ColumnID, &r.Title, &r.Body, &r.Priority, &r.Size,
		&r.Rank, &r.ArchivedAt, &r.SessionID, &r.Version, &r.CreatedAt, &r.UpdatedAt,
	)
	return r, err
}

// encodeCursor returns a base64-encoded (rank:id) cursor.
func encodeCursor(rank, id string) string {
	return base64.StdEncoding.EncodeToString([]byte(rank + ":" + id))
}

// decodeCursor reverses encodeCursor.
func decodeCursor(cursor string) (rank, id string, err error) {
	b, decErr := base64.StdEncoding.DecodeString(cursor)
	if decErr != nil {
		return "", "", fmt.Errorf("invalid cursor: %w", decErr)
	}
	parts := strings.SplitN(string(b), ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid cursor format")
	}
	return parts[0], parts[1], nil
}

// encodeArchivedCursor encodes an (archivedAt, id) pair as an opaque cursor
// for the ArchivedOnly pagination path (ordered by archived_at DESC, id ASC).
// The timestamp is stored as Unix nanoseconds to avoid the `:` separator clash
// with RFC3339 format.
func encodeArchivedCursor(archivedAt time.Time, id string) string {
	raw := fmt.Sprintf("%d:%s", archivedAt.UTC().UnixNano(), id)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// decodeArchivedCursor reverses encodeArchivedCursor.
func decodeArchivedCursor(cursor string) (archivedAt time.Time, id string, err error) {
	b, decErr := base64.StdEncoding.DecodeString(cursor)
	if decErr != nil {
		return time.Time{}, "", fmt.Errorf("invalid archived cursor: %w", decErr)
	}
	parts := strings.SplitN(string(b), ":", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("invalid archived cursor format")
	}
	ns, parseErr := strconv.ParseInt(parts[0], 10, 64)
	if parseErr != nil {
		return time.Time{}, "", fmt.Errorf("invalid archived cursor ns: %w", parseErr)
	}
	return time.Unix(0, ns).UTC(), parts[1], nil
}

// validatePriority returns an error if p is not a valid priority value.
// An empty string is treated as "medium".
func validatePriority(p string) error {
	switch p {
	case "", "low", "medium", "high", "urgent":
		return nil
	}
	return fmt.Errorf("%w %q: must be one of low, medium, high, urgent", ErrInvalidPriority, p)
}

// validateSize returns an error if s (when non-nil) is not a valid size value.
func validateSize(s *string) error {
	if s == nil {
		return nil
	}
	switch *s {
	case "XS", "S", "M", "L", "XL":
		return nil
	}
	return fmt.Errorf("%w %q: must be one of XS, S, M, L, XL", ErrInvalidSize, *s)
}

// validateBoardRepos checks that every repo in repos exists in the board's
// board_repos table. It runs within the provided transaction in a single
// round-trip: it asks the DB for any input repo NOT present in board_repos and
// rejects on the first one found.
func validateBoardRepos(ctx context.Context, tx pgx.Tx, boardID string, repos []string) error {
	if len(repos) == 0 {
		return fmt.Errorf("%w: at least one repo is required", ErrInvalidRepos)
	}
	var missing string
	err := tx.QueryRow(ctx, `
		SELECT r FROM unnest($2::text[]) AS r
		 WHERE r NOT IN (SELECT repo FROM board_repos WHERE board_id = $1)
		 LIMIT 1
	`, boardID, repos).Scan(&missing)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // every repo is a member
		}
		return fmt.Errorf("check board_repos: %w", err)
	}
	return fmt.Errorf("%w: repo %q is not in board %s", ErrInvalidRepos, missing, boardID)
}

// insertTicketRepos inserts ticket_repo rows for each repo, assigning
// fractional rank keys in list order (first repo has the smallest rank).
func insertTicketRepos(ctx context.Context, tx pgx.Tx, ticketID string, repos []string) error {
	var prevRank string
	for _, repo := range repos {
		rank, err := RankBetween(prevRank, "")
		if err != nil {
			return fmt.Errorf("rank for repo %s: %w", repo, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ticket_repos (ticket_id, repo, rank) VALUES ($1, $2, $3)
		`, ticketID, repo, rank); err != nil {
			return fmt.Errorf("insert ticket_repo %s: %w", repo, err)
		}
		prevRank = rank
	}
	return nil
}

// ─── Public functions ─────────────────────────────────────────────────────────

// CreateTicket creates a new ticket with the given input. Defaults: Priority
// "medium" if empty; ColumnID = first non-terminal column by rank if nil.
// Validates: ≥1 repo, all repos ∈ board repos, priority enum, size enum.
// Inserts ticket + ticket_repos + ticket_tags in one transaction.
func CreateTicket(ctx context.Context, pool *pgxpool.Pool, in TicketInput) (TicketRow, error) {
	// Validate.
	if err := validatePriority(in.Priority); err != nil {
		return TicketRow{}, fmt.Errorf("CreateTicket: %w", err)
	}
	if err := validateSize(in.Size); err != nil {
		return TicketRow{}, fmt.Errorf("CreateTicket: %w", err)
	}

	priority := in.Priority
	if priority == "" {
		priority = "medium"
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return TicketRow{}, fmt.Errorf("CreateTicket begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; on an error path the original error is what gets returned

	// Validate repos.
	if err := validateBoardRepos(ctx, tx, in.BoardID, in.Repos); err != nil {
		return TicketRow{}, fmt.Errorf("CreateTicket: %w", err)
	}

	// Resolve column: explicit or first non-terminal.
	columnID := in.ColumnID
	if columnID == nil {
		var colID string
		err := tx.QueryRow(ctx, `
			SELECT id FROM columns
			 WHERE board_id = $1 AND is_terminal = false
			 ORDER BY rank ASC
			 LIMIT 1
		`, in.BoardID).Scan(&colID)
		if err != nil {
			return TicketRow{}, fmt.Errorf("CreateTicket resolve column: %w", err)
		}
		columnID = &colID
	}

	// Compute rank: place after the last live ticket in the column.
	var prevRank string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(rank), '')
		  FROM tickets
		 WHERE board_id = $1 AND column_id = $2 AND archived_at IS NULL
	`, in.BoardID, *columnID).Scan(&prevRank); err != nil {
		return TicketRow{}, fmt.Errorf("CreateTicket get max rank: %w", err)
	}
	rank, err := RankBetween(prevRank, "")
	if err != nil {
		return TicketRow{}, fmt.Errorf("CreateTicket compute rank: %w", err)
	}

	// Insert ticket.
	row, scanErr := scanTicketRow(tx.QueryRow(ctx, `
		INSERT INTO tickets (board_id, column_id, title, body, priority, size, rank)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+ticketCols,
		in.BoardID, columnID, in.Title, in.Body, priority, in.Size, rank,
	))
	if scanErr != nil {
		return TicketRow{}, fmt.Errorf("CreateTicket insert ticket: %w", scanErr)
	}

	// Insert repos.
	if err := insertTicketRepos(ctx, tx, row.ID, in.Repos); err != nil {
		return TicketRow{}, fmt.Errorf("CreateTicket: %w", err)
	}

	// Insert tags (idempotent: ON CONFLICT DO NOTHING).
	for _, tag := range in.Tags {
		if _, err := tx.Exec(ctx, `
			INSERT INTO ticket_tags (ticket_id, tag) VALUES ($1, $2) ON CONFLICT DO NOTHING
		`, row.ID, tag); err != nil {
			return TicketRow{}, fmt.Errorf("CreateTicket insert tag %s: %w", tag, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return TicketRow{}, fmt.Errorf("CreateTicket commit: %w", err)
	}
	return row, nil
}

// GetTicket looks up a ticket by ID and returns a TicketDetail with its
// repos (ordered by rank), tags (alphabetical), and dependency IDs.
// Returns an error wrapping pgx.ErrNoRows if not found.
func GetTicket(ctx context.Context, pool *pgxpool.Pool, id string) (TicketDetail, error) {
	row, err := scanTicketRow(pool.QueryRow(ctx,
		`SELECT `+ticketCols+` FROM tickets WHERE id = $1`, id,
	))
	if err != nil {
		return TicketDetail{}, fmt.Errorf("GetTicket: %w", err)
	}

	detail := TicketDetail{TicketRow: row}

	// Repos ordered by rank asc.
	repoRows, err := pool.Query(ctx, `
		SELECT repo FROM ticket_repos WHERE ticket_id = $1 ORDER BY rank ASC
	`, id)
	if err != nil {
		return TicketDetail{}, fmt.Errorf("GetTicket repos: %w", err)
	}
	defer repoRows.Close()
	for repoRows.Next() {
		var r string
		if err := repoRows.Scan(&r); err != nil {
			return TicketDetail{}, fmt.Errorf("GetTicket scan repo: %w", err)
		}
		detail.Repos = append(detail.Repos, r)
	}
	if err := repoRows.Err(); err != nil {
		return TicketDetail{}, fmt.Errorf("GetTicket repos rows: %w", err)
	}

	// Tags alphabetical.
	tagRows, err := pool.Query(ctx, `
		SELECT tag FROM ticket_tags WHERE ticket_id = $1 ORDER BY tag ASC
	`, id)
	if err != nil {
		return TicketDetail{}, fmt.Errorf("GetTicket tags: %w", err)
	}
	defer tagRows.Close()
	for tagRows.Next() {
		var t string
		if err := tagRows.Scan(&t); err != nil {
			return TicketDetail{}, fmt.Errorf("GetTicket scan tag: %w", err)
		}
		detail.Tags = append(detail.Tags, t)
	}
	if err := tagRows.Err(); err != nil {
		return TicketDetail{}, fmt.Errorf("GetTicket tags rows: %w", err)
	}

	// DependsOn: tickets this ticket depends on.
	depRows, err := pool.Query(ctx, `
		SELECT depends_on_ticket_id::text FROM ticket_dependencies
		 WHERE ticket_id = $1
		 ORDER BY depends_on_ticket_id ASC
	`, id)
	if err != nil {
		return TicketDetail{}, fmt.Errorf("GetTicket depends_on: %w", err)
	}
	defer depRows.Close()
	for depRows.Next() {
		var d string
		if err := depRows.Scan(&d); err != nil {
			return TicketDetail{}, fmt.Errorf("GetTicket scan dep: %w", err)
		}
		detail.DependsOn = append(detail.DependsOn, d)
	}
	if err := depRows.Err(); err != nil {
		return TicketDetail{}, fmt.Errorf("GetTicket deps rows: %w", err)
	}

	// Blocks: tickets that depend on this ticket.
	blocksRows, err := pool.Query(ctx, `
		SELECT ticket_id::text FROM ticket_dependencies
		 WHERE depends_on_ticket_id = $1
		 ORDER BY ticket_id ASC
	`, id)
	if err != nil {
		return TicketDetail{}, fmt.Errorf("GetTicket blocks: %w", err)
	}
	defer blocksRows.Close()
	for blocksRows.Next() {
		var b string
		if err := blocksRows.Scan(&b); err != nil {
			return TicketDetail{}, fmt.Errorf("GetTicket scan block: %w", err)
		}
		detail.Blocks = append(detail.Blocks, b)
	}
	if err := blocksRows.Err(); err != nil {
		return TicketDetail{}, fmt.Errorf("GetTicket blocks rows: %w", err)
	}

	return detail, nil
}

// GetTicketCard returns a single ticket enriched with the same card-view fields
// ListTickets populates: Tags (alphabetical), Repos (rank order), and Blocked.
// Unlike ListTickets it does NOT filter out archived tickets — it looks up
// purely by id — so it works for archived tickets too (needed for the
// ticket_archived and ticket_split origin WS events).
//
// Returns an error wrapping pgx.ErrNoRows if the ticket does not exist.
func GetTicketCard(ctx context.Context, pool *pgxpool.Pool, id string) (TicketRow, error) {
	var r TicketRow
	err := pool.QueryRow(ctx, `
		SELECT `+ticketCols+`, `+blockedPredicate+` AS blocked
		  FROM tickets t
		 WHERE t.id = $1
	`, id).Scan(
		&r.ID, &r.BoardID, &r.ColumnID, &r.Title, &r.Body, &r.Priority, &r.Size,
		&r.Rank, &r.ArchivedAt, &r.SessionID, &r.Version, &r.CreatedAt, &r.UpdatedAt,
		&r.Blocked,
	)
	if err != nil {
		return TicketRow{}, fmt.Errorf("GetTicketCard: %w", err)
	}

	// Tags (alphabetical).
	tagRows, err := pool.Query(ctx, `
		SELECT tag FROM ticket_tags WHERE ticket_id = $1 ORDER BY tag ASC
	`, id)
	if err != nil {
		return TicketRow{}, fmt.Errorf("GetTicketCard tags: %w", err)
	}
	defer tagRows.Close()
	for tagRows.Next() {
		var t string
		if err := tagRows.Scan(&t); err != nil {
			return TicketRow{}, fmt.Errorf("GetTicketCard scan tag: %w", err)
		}
		r.Tags = append(r.Tags, t)
	}
	if err := tagRows.Err(); err != nil {
		return TicketRow{}, fmt.Errorf("GetTicketCard tags rows: %w", err)
	}

	// Repos (rank order).
	repoRows, err := pool.Query(ctx, `
		SELECT repo FROM ticket_repos WHERE ticket_id = $1 ORDER BY rank ASC
	`, id)
	if err != nil {
		return TicketRow{}, fmt.Errorf("GetTicketCard repos: %w", err)
	}
	defer repoRows.Close()
	for repoRows.Next() {
		var repo string
		if err := repoRows.Scan(&repo); err != nil {
			return TicketRow{}, fmt.Errorf("GetTicketCard scan repo: %w", err)
		}
		r.Repos = append(r.Repos, repo)
	}
	if err := repoRows.Err(); err != nil {
		return TicketRow{}, fmt.Errorf("GetTicketCard repos rows: %w", err)
	}

	return r, nil
}

// listArchivedTickets returns archived tickets for the given board, newest
// archived first (archived_at DESC, id ASC), enriched with tags and repos.
// Called by ListTickets when ArchivedOnly is true.
func listArchivedTickets(ctx context.Context, pool *pgxpool.Pool, boardID string, f TicketFilter, limit int) ([]TicketRow, string, error) {
	args := []any{boardID}
	conds := []string{"t.board_id = $1", "t.archived_at IS NOT NULL"}

	if f.Cursor != "" {
		curAt, curID, err := decodeArchivedCursor(f.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("listArchivedTickets: %w", err)
		}
		args = append(args, curAt, curID)
		n := len(args)
		// Rows after cursor in DESC(archived_at), ASC(id) order:
		// (archived_at < curAt) OR (archived_at = curAt AND id::text > curID)
		conds = append(conds, fmt.Sprintf(
			"(t.archived_at < $%d OR (t.archived_at = $%d AND t.id::text > $%d))",
			n-1, n-1, n,
		))
	}

	args = append(args, limit+1)
	where := strings.Join(conds, " AND ")
	q := fmt.Sprintf(`
		SELECT %s, %s AS blocked
		  FROM tickets t
		 WHERE %s
		 ORDER BY t.archived_at DESC, t.id ASC
		 LIMIT $%d
	`, ticketCols, blockedPredicate, where, len(args))

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("listArchivedTickets query: %w", err)
	}

	var result []TicketRow
	func() {
		defer rows.Close()
		for rows.Next() {
			var r TicketRow
			if err = rows.Scan(
				&r.ID, &r.BoardID, &r.ColumnID, &r.Title, &r.Body, &r.Priority, &r.Size,
				&r.Rank, &r.ArchivedAt, &r.SessionID, &r.Version, &r.CreatedAt, &r.UpdatedAt,
				&r.Blocked,
			); err != nil {
				return
			}
			result = append(result, r)
		}
		err = rows.Err()
	}()
	if err != nil {
		return nil, "", fmt.Errorf("listArchivedTickets scan: %w", err)
	}

	var nextCursor string
	if len(result) > limit {
		result = result[:limit]
		last := result[len(result)-1]
		nextCursor = encodeArchivedCursor(*last.ArchivedAt, last.ID)
	}

	if len(result) > 0 {
		ids := make([]string, len(result))
		idxByID := make(map[string]int, len(result))
		for i, r := range result {
			ids[i] = r.ID
			idxByID[r.ID] = i
		}

		tagRows, tagErr := pool.Query(ctx, `
			SELECT ticket_id::text, tag
			  FROM ticket_tags
			 WHERE ticket_id = ANY($1::uuid[])
			 ORDER BY ticket_id, tag ASC
		`, ids)
		if tagErr != nil {
			return nil, "", fmt.Errorf("listArchivedTickets tags query: %w", tagErr)
		}
		func() {
			defer tagRows.Close()
			for tagRows.Next() {
				var ticketID, tag string
				if scanErr := tagRows.Scan(&ticketID, &tag); scanErr != nil {
					err = scanErr
					return
				}
				i := idxByID[ticketID]
				result[i].Tags = append(result[i].Tags, tag)
			}
			err = tagRows.Err()
		}()
		if err != nil {
			return nil, "", fmt.Errorf("listArchivedTickets tags scan: %w", err)
		}

		repoRows, repoErr := pool.Query(ctx, `
			SELECT ticket_id::text, repo
			  FROM ticket_repos
			 WHERE ticket_id = ANY($1::uuid[])
			 ORDER BY ticket_id, rank ASC
		`, ids)
		if repoErr != nil {
			return nil, "", fmt.Errorf("listArchivedTickets repos query: %w", repoErr)
		}
		func() {
			defer repoRows.Close()
			for repoRows.Next() {
				var ticketID, repo string
				if scanErr := repoRows.Scan(&ticketID, &repo); scanErr != nil {
					err = scanErr
					return
				}
				i := idxByID[ticketID]
				result[i].Repos = append(result[i].Repos, repo)
			}
			err = repoRows.Err()
		}()
		if err != nil {
			return nil, "", fmt.Errorf("listArchivedTickets repos scan: %w", err)
		}
	}

	return result, nextCursor, nil
}

// ListTickets returns non-archived tickets for the given board, filtered by f,
// plus an opaque cursor for the next page. An empty cursor means no more pages.
//
// When f.ArchivedOnly is true, returns archived tickets instead, ordered by
// archived_at DESC, id ASC.
//
// Ready filter: a ticket is ready when every dependency is either archived or
// in a terminal column. A ticket with no dependencies is always ready.
//
// Pagination: stable ordering by (rank ASC, id ASC); cursor encodes the last
// row's (rank, id) as base64("rank:id").
func ListTickets(ctx context.Context, pool *pgxpool.Pool, boardID string, f TicketFilter) ([]TicketRow, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	if f.ArchivedOnly {
		return listArchivedTickets(ctx, pool, boardID, f, limit)
	}

	args := []any{boardID}
	conds := []string{"t.board_id = $1", "t.archived_at IS NULL"}

	if f.ColumnID != nil {
		args = append(args, *f.ColumnID)
		conds = append(conds, fmt.Sprintf("t.column_id = $%d", len(args)))
	}
	if f.Priority != nil {
		args = append(args, *f.Priority)
		conds = append(conds, fmt.Sprintf("t.priority = $%d", len(args)))
	}
	if f.Size != nil {
		args = append(args, *f.Size)
		conds = append(conds, fmt.Sprintf("t.size = $%d", len(args)))
	}
	for _, tag := range f.Tags {
		args = append(args, tag)
		conds = append(conds, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM ticket_tags tt WHERE tt.ticket_id = t.id AND tt.tag = $%d)",
			len(args),
		))
	}

	if f.Ready {
		// A ticket is ready when it has NO unsatisfied blocker.
		// A blocker is unsatisfied when: not archived AND not in a terminal column.
		conds = append(conds, `NOT EXISTS (
			SELECT 1
			  FROM ticket_dependencies td
			  JOIN tickets blocker ON blocker.id = td.depends_on_ticket_id
			  LEFT JOIN columns bc ON bc.id = blocker.column_id
			 WHERE td.ticket_id = t.id
			   AND blocker.archived_at IS NULL
			   AND COALESCE(bc.is_terminal, false) = false
		)`)
	}

	if f.Cursor != "" {
		cursorRank, cursorID, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("ListTickets: %w", err)
		}
		args = append(args, cursorRank, cursorID)
		n := len(args)
		// Composite row-value comparison: (rank, id::text) > (cursorRank, cursorID)
		conds = append(conds, fmt.Sprintf("(t.rank, t.id::text) > ($%d, $%d)", n-1, n))
	}

	where := strings.Join(conds, " AND ")
	// Fetch limit+1 to detect whether a next page exists.
	args = append(args, limit+1)

	q := fmt.Sprintf(`
		SELECT %s, %s AS blocked
		  FROM tickets t
		 WHERE %s
		 ORDER BY t.rank ASC, t.id ASC
		 LIMIT $%d
	`, ticketCols, blockedPredicate, where, len(args))

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("ListTickets query: %w", err)
	}

	// Inline scan: reads the 13 ticketCols columns + the blocked bool.
	var result []TicketRow
	func() {
		defer rows.Close()
		for rows.Next() {
			var r TicketRow
			if err = rows.Scan(
				&r.ID, &r.BoardID, &r.ColumnID, &r.Title, &r.Body, &r.Priority, &r.Size,
				&r.Rank, &r.ArchivedAt, &r.SessionID, &r.Version, &r.CreatedAt, &r.UpdatedAt,
				&r.Blocked,
			); err != nil {
				return
			}
			result = append(result, r)
		}
		err = rows.Err()
	}()
	if err != nil {
		return nil, "", fmt.Errorf("ListTickets scan: %w", err)
	}

	var nextCursor string
	if len(result) > limit {
		result = result[:limit]
		last := result[len(result)-1]
		nextCursor = encodeCursor(last.Rank, last.ID)
	}

	if len(result) > 0 {
		// Batch-fetch tags and repos for all returned ticket IDs (two queries,
		// not N+1). Build index from id → slice position.
		ids := make([]string, len(result))
		idxByID := make(map[string]int, len(result))
		for i, r := range result {
			ids[i] = r.ID
			idxByID[r.ID] = i
		}

		// Tags (alphabetical order).
		tagRows, tagErr := pool.Query(ctx, `
			SELECT ticket_id::text, tag
			  FROM ticket_tags
			 WHERE ticket_id = ANY($1::uuid[])
			 ORDER BY ticket_id, tag ASC
		`, ids)
		if tagErr != nil {
			return nil, "", fmt.Errorf("ListTickets tags query: %w", tagErr)
		}
		func() {
			defer tagRows.Close()
			for tagRows.Next() {
				var ticketID, tag string
				if scanErr := tagRows.Scan(&ticketID, &tag); scanErr != nil {
					err = scanErr
					return
				}
				i := idxByID[ticketID]
				result[i].Tags = append(result[i].Tags, tag)
			}
			err = tagRows.Err()
		}()
		if err != nil {
			return nil, "", fmt.Errorf("ListTickets tags scan: %w", err)
		}

		// Repos (rank order).
		repoRows, repoErr := pool.Query(ctx, `
			SELECT ticket_id::text, repo
			  FROM ticket_repos
			 WHERE ticket_id = ANY($1::uuid[])
			 ORDER BY ticket_id, rank ASC
		`, ids)
		if repoErr != nil {
			return nil, "", fmt.Errorf("ListTickets repos query: %w", repoErr)
		}
		func() {
			defer repoRows.Close()
			for repoRows.Next() {
				var ticketID, repo string
				if scanErr := repoRows.Scan(&ticketID, &repo); scanErr != nil {
					err = scanErr
					return
				}
				i := idxByID[ticketID]
				result[i].Repos = append(result[i].Repos, repo)
			}
			err = repoRows.Err()
		}()
		if err != nil {
			return nil, "", fmt.Errorf("ListTickets repos scan: %w", err)
		}
	}

	return result, nextCursor, nil
}

// UpdateTicket applies p to the ticket identified by id. Scalar changes
// (Title, Body, Priority, Size) and whole-set Repos replace are version-guarded
// when expectVersion is non-nil: if the current version differs,
// ErrStaleVersion is returned. Additive set ops (AddTags/RmTags/AddRepos/
// RmRepos) are idempotent and ignore the version guard.
func UpdateTicket(ctx context.Context, pool *pgxpool.Pool, id string, p TicketPatch, expectVersion *int) (TicketRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return TicketRow{}, fmt.Errorf("UpdateTicket begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; on an error path the original error is what gets returned

	// Lock and read current ticket.
	cur, err := scanTicketRow(tx.QueryRow(ctx,
		`SELECT `+ticketCols+` FROM tickets WHERE id = $1 FOR UPDATE`, id,
	))
	if err != nil {
		return TicketRow{}, fmt.Errorf("UpdateTicket get ticket: %w", err)
	}

	// A version-guarded change is any scalar field OR a whole-set Repos replace.
	hasScalarField := p.Title != nil || p.Body != nil || p.Priority != nil || p.Size != nil
	hasVersionedChange := hasScalarField || p.Repos != nil
	if hasVersionedChange && expectVersion != nil && cur.Version != *expectVersion {
		return TicketRow{}, fmt.Errorf("UpdateTicket: %w", ErrStaleVersion)
	}

	// ── Scalar field update ───────────────────────────────────────────────────
	// The version bump for ANY versioned change (scalar OR Repos) is folded into
	// this single UPDATE when a scalar field is present, so a combined
	// scalar+Repos patch increments version exactly once.
	if hasScalarField {
		setClauses := []string{"version = version + 1", "updated_at = now()"}
		args := []any{}

		if p.Title != nil {
			args = append(args, *p.Title)
			setClauses = append(setClauses, fmt.Sprintf("title = $%d", len(args)))
		}
		if p.Body != nil {
			args = append(args, *p.Body)
			setClauses = append(setClauses, fmt.Sprintf("body = $%d", len(args)))
		}
		if p.Priority != nil {
			if err := validatePriority(*p.Priority); err != nil {
				return TicketRow{}, fmt.Errorf("UpdateTicket: %w", err)
			}
			args = append(args, *p.Priority)
			setClauses = append(setClauses, fmt.Sprintf("priority = $%d", len(args)))
		}
		if p.Size != nil {
			if err := validateSize(p.Size); err != nil {
				return TicketRow{}, fmt.Errorf("UpdateTicket: %w", err)
			}
			args = append(args, *p.Size)
			setClauses = append(setClauses, fmt.Sprintf("size = $%d", len(args)))
		}

		args = append(args, id)
		q := fmt.Sprintf("UPDATE tickets SET %s WHERE id = $%d",
			strings.Join(setClauses, ", "), len(args))
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			return TicketRow{}, fmt.Errorf("UpdateTicket scalar update: %w", err)
		}
	}

	// ── Repos whole-set replace (version-guarded like a scalar) ──────────────
	if p.Repos != nil {
		if len(*p.Repos) == 0 {
			return TicketRow{}, fmt.Errorf("UpdateTicket: %w: must have at least one repo", ErrInvalidRepos)
		}
		if err := validateBoardRepos(ctx, tx, cur.BoardID, *p.Repos); err != nil {
			return TicketRow{}, fmt.Errorf("UpdateTicket: %w", err)
		}
		// Bump version + updated_at for the Repos replace ONLY if no scalar update
		// already did so — a combined scalar+Repos patch increments version once.
		if !hasScalarField {
			if _, err := tx.Exec(ctx, `
				UPDATE tickets SET version = version + 1, updated_at = now() WHERE id = $1
			`, id); err != nil {
				return TicketRow{}, fmt.Errorf("UpdateTicket bump version for repos replace: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM ticket_repos WHERE ticket_id = $1`, id); err != nil {
			return TicketRow{}, fmt.Errorf("UpdateTicket delete repos: %w", err)
		}
		if err := insertTicketRepos(ctx, tx, id, *p.Repos); err != nil {
			return TicketRow{}, fmt.Errorf("UpdateTicket insert repos: %w", err)
		}
	}

	// ── Additive tag ops ──────────────────────────────────────────────────────
	for _, tag := range p.AddTags {
		if _, err := tx.Exec(ctx, `
			INSERT INTO ticket_tags (ticket_id, tag) VALUES ($1, $2) ON CONFLICT DO NOTHING
		`, id, tag); err != nil {
			return TicketRow{}, fmt.Errorf("UpdateTicket add tag %s: %w", tag, err)
		}
	}
	for _, tag := range p.RmTags {
		if _, err := tx.Exec(ctx, `
			DELETE FROM ticket_tags WHERE ticket_id = $1 AND tag = $2
		`, id, tag); err != nil {
			return TicketRow{}, fmt.Errorf("UpdateTicket rm tag %s: %w", tag, err)
		}
	}

	// ── Additive repo ops ─────────────────────────────────────────────────────
	// Every repo-add path must enforce board membership (there is no FK from
	// ticket_repos to board_repos), so validate before inserting.
	if len(p.AddRepos) > 0 {
		if err := validateBoardRepos(ctx, tx, cur.BoardID, p.AddRepos); err != nil {
			return TicketRow{}, fmt.Errorf("UpdateTicket: %w", err)
		}
	}
	for _, repo := range p.AddRepos {
		// Assign a rank after existing repos (query max rank inside the tx).
		var maxRank string
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(MAX(rank), '') FROM ticket_repos WHERE ticket_id = $1`, id,
		).Scan(&maxRank); err != nil {
			return TicketRow{}, fmt.Errorf("UpdateTicket get max repo rank: %w", err)
		}
		newRank, err := RankBetween(maxRank, "")
		if err != nil {
			return TicketRow{}, fmt.Errorf("UpdateTicket rank for repo %s: %w", repo, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ticket_repos (ticket_id, repo, rank) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING
		`, id, repo, newRank); err != nil {
			return TicketRow{}, fmt.Errorf("UpdateTicket add repo %s: %w", repo, err)
		}
	}
	for _, repo := range p.RmRepos {
		if _, err := tx.Exec(ctx, `
			DELETE FROM ticket_repos WHERE ticket_id = $1 AND repo = $2
		`, id, repo); err != nil {
			return TicketRow{}, fmt.Errorf("UpdateTicket rm repo %s: %w", repo, err)
		}
	}

	// Read final state within the tx (sees all changes above).
	result, err := scanTicketRow(tx.QueryRow(ctx,
		`SELECT `+ticketCols+` FROM tickets WHERE id = $1`, id,
	))
	if err != nil {
		return TicketRow{}, fmt.Errorf("UpdateTicket read result: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return TicketRow{}, fmt.Errorf("UpdateTicket commit: %w", err)
	}
	return result, nil
}

// MoveTicket moves a ticket to the given column, placing it between the
// tickets identified by after (new previous neighbor) and before (new next
// neighbor). Either may be nil for no bound on that side.
//
// The rank-resolution and write are performed in a single transaction that
// first locks the board row (SELECT … FOR UPDATE), serialising concurrent moves
// on the same board so they cannot duplicate a rank.
//
// Optimistic concurrency: when expectVersion is non-nil the UPDATE is guarded
// with AND version = expectVersion; if no row matches, ErrStaleVersion is
// returned, and the move bumps version (+1). When expectVersion is nil the move
// is performed with no version guard and no version bump.
func MoveTicket(ctx context.Context, pool *pgxpool.Pool, id, columnID string, after, before *string, expectVersion *int) (TicketRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return TicketRow{}, fmt.Errorf("MoveTicket begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; on an error path the original error is what gets returned

	// Get board_id from the ticket, then lock board row.
	var boardID string
	if err := tx.QueryRow(ctx, `SELECT board_id FROM tickets WHERE id = $1`, id).Scan(&boardID); err != nil {
		return TicketRow{}, fmt.Errorf("MoveTicket get board_id: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM boards WHERE id = $1 FOR UPDATE`, boardID); err != nil {
		return TicketRow{}, fmt.Errorf("MoveTicket lock board: %w", err)
	}

	var prevRank, nextRank string
	if after != nil {
		if err := tx.QueryRow(ctx, `SELECT rank FROM tickets WHERE id = $1`, *after).Scan(&prevRank); err != nil {
			return TicketRow{}, fmt.Errorf("MoveTicket get after rank: %w", err)
		}
	}
	if before != nil {
		if err := tx.QueryRow(ctx, `SELECT rank FROM tickets WHERE id = $1`, *before).Scan(&nextRank); err != nil {
			return TicketRow{}, fmt.Errorf("MoveTicket get before rank: %w", err)
		}
	}

	rank, err := RankBetween(prevRank, nextRank)
	if err != nil {
		return TicketRow{}, fmt.Errorf("MoveTicket compute rank: %w", err)
	}

	var row TicketRow
	if expectVersion != nil {
		// Version-guarded move: bump version and require the current version to
		// match. A non-matching version yields pgx.ErrNoRows → ErrStaleVersion.
		row, err = scanTicketRow(tx.QueryRow(ctx, `
			UPDATE tickets
			   SET column_id  = $2,
			       rank       = $3,
			       version    = version + 1,
			       updated_at = now()
			 WHERE id = $1 AND version = $4
			RETURNING `+ticketCols,
			id, columnID, rank, *expectVersion,
		))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return TicketRow{}, fmt.Errorf("MoveTicket: %w", ErrStaleVersion)
			}
			return TicketRow{}, fmt.Errorf("MoveTicket update: %w", err)
		}
	} else {
		// Unguarded move: no version check, no version bump.
		row, err = scanTicketRow(tx.QueryRow(ctx, `
			UPDATE tickets
			   SET column_id  = $2,
			       rank       = $3,
			       updated_at = now()
			 WHERE id = $1
			RETURNING `+ticketCols,
			id, columnID, rank,
		))
		if err != nil {
			return TicketRow{}, fmt.Errorf("MoveTicket update: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return TicketRow{}, fmt.Errorf("MoveTicket commit: %w", err)
	}
	return row, nil
}

// ArchiveTicket archives a ticket: sets archived_at = now() and column_id = NULL.
// Returns the updated TicketRow, or an error (wrapping pgx.ErrNoRows if not found).
func ArchiveTicket(ctx context.Context, pool *pgxpool.Pool, id string) (TicketRow, error) {
	row, err := scanTicketRow(pool.QueryRow(ctx, `
		UPDATE tickets
		   SET archived_at = now(),
		       column_id   = NULL,
		       updated_at  = now()
		 WHERE id = $1
		RETURNING `+ticketCols,
		id,
	))
	if err != nil {
		return TicketRow{}, fmt.Errorf("ArchiveTicket: %w", err)
	}
	return row, nil
}

// SetTicketSession binds a session to a ticket by setting tickets.session_id.
// Used when an Assist session is spawned for a specific ticket so the board
// can show which ticket is actively being worked on by an AI session.
func SetTicketSession(ctx context.Context, pool *pgxpool.Pool, ticketID, sessionID string) error {
	_, err := pool.Exec(ctx, `UPDATE tickets SET session_id = $2 WHERE id = $1`, ticketID, sessionID)
	if err != nil {
		return fmt.Errorf("SetTicketSession: %w", err)
	}
	return nil
}

// ClearTicketSessionBySession clears tickets.session_id for all tickets bound
// to the given session. Called when a session ends so the ticket is no longer
// shown as actively worked on. Idempotent: if no tickets are bound, it is a no-op.
func ClearTicketSessionBySession(ctx context.Context, pool *pgxpool.Pool, sessionID string) error {
	_, err := pool.Exec(ctx, `UPDATE tickets SET session_id = NULL WHERE session_id = $1`, sessionID)
	if err != nil {
		return fmt.Errorf("ClearTicketSessionBySession: %w", err)
	}
	return nil
}

// TicketBoardID returns the board_id of the ticket with the given id.
// Returns an error wrapping pgx.ErrNoRows when the ticket doesn't exist.
func TicketBoardID(ctx context.Context, pool *pgxpool.Pool, ticketID string) (string, error) {
	var boardID string
	err := pool.QueryRow(ctx, `SELECT board_id FROM tickets WHERE id = $1`, ticketID).Scan(&boardID)
	if err != nil {
		return "", fmt.Errorf("TicketBoardID: %w", err)
	}
	return boardID, nil
}
