package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Link struct {
	Kind  string  `json:"kind"`
	URL   string  `json:"url"`
	Label *string `json:"label"`
}

type Card struct {
	ID          string          `json:"id"`
	BoardID     string          `json:"board_id"`
	Number      int             `json:"number"`
	ColumnID    *string         `json:"column_id"`
	Type        string          `json:"type"`
	Title       string          `json:"title"`
	Body        *string         `json:"body"`
	Priority    string          `json:"priority"`
	AutoMerge   bool            `json:"auto_merge"`
	RunAttempts int             `json:"run_attempts"`
	MergedSHA   *string         `json:"merged_sha"`
	MergedAt    *string         `json:"merged_at"`
	StuckAt     *string         `json:"stuck_at"`
	StaleAt     *string         `json:"stale_at"`
	Size        *string         `json:"size"`
	Model       string          `json:"model"` // overrides the board's model for this card's sessions; "" = board default
	Fields      json.RawMessage `json:"fields"`
	DedupKey    *string         `json:"dedup_key"`
	ExternalID  *string         `json:"external_id"`
	Rank        string          `json:"rank"`
	ArchivedAt  *string         `json:"archived_at"`
	Version     int             `json:"version"`
	GateFlag    *string         `json:"gate_flag"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
	Repos       []string        `json:"repos"`
	Tags        []string        `json:"tags"`
	Links       []Link          `json:"links"`
	DependsOn   []string        `json:"depends_on"` // blocker card ids
	// Blockers is DependsOn resolved: same edges, in card-number order, each
	// carrying the blocker's number/title and whether it has landed. A card
	// with an unsatisfied blocker is skipped by the dispatcher, so this is
	// what the UI renders to explain why nothing is happening.
	Blockers []Blocker `json:"blockers"`
}

const cardCols = `id, board_id, number, column_id, type, title, body, priority, size, model,
	fields, dedup_key, external_id, rank, auto_merge, run_attempts, merged_sha,
	to_char(merged_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(stuck_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(stale_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(archived_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), version, gate_flag,
	to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), to_char(updated_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`

func scanCard(row pgx.Row) (Card, error) {
	var c Card
	err := row.Scan(&c.ID, &c.BoardID, &c.Number, &c.ColumnID, &c.Type, &c.Title, &c.Body,
		&c.Priority, &c.Size, &c.Model, &c.Fields, &c.DedupKey, &c.ExternalID, &c.Rank,
		&c.AutoMerge, &c.RunAttempts, &c.MergedSHA, &c.MergedAt, &c.StuckAt, &c.StaleAt,
		&c.ArchivedAt, &c.Version, &c.GateFlag, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// CardParams is a create/update payload. Nil pointers mean "unchanged" on
// update; on create, sensible defaults apply.
type CardParams struct {
	Type       *string         `json:"type"`
	Title      *string         `json:"title"`
	Body       *string         `json:"body"`
	Priority   *string         `json:"priority"`
	Size       *string         `json:"size"`
	Model      *string         `json:"model"` // "" clears the override
	Fields     json.RawMessage `json:"fields"`
	DedupKey   *string         `json:"dedup_key"`
	ExternalID *string         `json:"external_id"`
	ColumnID   *string         `json:"column_id"`
	AutoMerge  *bool           `json:"auto_merge"`
	Repos      *[]string       `json:"repos"`
	Tags       *[]string       `json:"tags"`
	Links      *[]Link         `json:"links"`
	// AddLinks appends links to the card's existing ones (an entry already present is left alone), atomically
	// with the write: two writers adding a link at once never lose one, which a read-modify-write of Links can.
	AddLinks *[]Link `json:"add_links"`
	// IfMatch guards the write when non-nil (409 on mismatch). Uniform
	// versioning: the write bumps version whether or not a guard was supplied.
	IfMatch *int `json:"if_match"`
	// IfMatchOnHit guards the dedup-refresh path specifically.
	IfMatchOnHit *int `json:"if_match_on_hit"`
}

func normalizeCardParams(p *CardParams) error {
	if p.Model != nil {
		*p.Model = strings.TrimSpace(*p.Model)
	}
	if p.Type != nil {
		*p.Type = strings.ToLower(strings.TrimSpace(*p.Type))
	}
	if p.Size != nil && *p.Size != "" {
		*p.Size = strings.ToUpper(strings.TrimSpace(*p.Size))
		switch *p.Size {
		case "XS", "S", "M", "L", "XL":
		default:
			return fmt.Errorf("invalid size %q (XS|S|M|L|XL)", *p.Size)
		}
	}
	if p.Priority != nil {
		*p.Priority = strings.ToLower(strings.TrimSpace(*p.Priority))
		switch *p.Priority {
		case "low", "medium", "high", "urgent":
		default:
			return fmt.Errorf("invalid priority %q (low|medium|high|urgent)", *p.Priority)
		}
	}
	return nil
}

// validateRepos enforces the board-repo invariants: subset of board_repos and,
// when the board requires it, at least one repo.
func validateRepos(ctx context.Context, tx pgx.Tx, boardID string, requireRepo bool, repos []string) error {
	if len(repos) == 0 {
		if requireRepo {
			return fmt.Errorf("%w — set repos on the board (PATCH /api/boards/{id} {\"repos\":[…]}) or require_repo:false", ErrInvalidRepos)
		}
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT repo FROM board_repos WHERE board_id = $1`, boardID)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			rows.Close()
			return err
		}
		allowed[r] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range repos {
		if !allowed[r] {
			return ErrInvalidRepos
		}
	}
	return nil
}

// loadBoardMeta reads the bits of the board the card paths need, inside the tx.
func loadBoardMeta(ctx context.Context, tx pgx.Tx, boardID string) (requireRepo bool, defs []FieldDef, err error) {
	var raw []byte
	err = tx.QueryRow(ctx,
		`SELECT require_repo, field_schema FROM boards WHERE id = $1`, boardID).
		Scan(&requireRepo, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, ErrNotFound
	}
	if err != nil {
		return false, nil, err
	}
	defs, err = ParseFieldSchema(raw)
	return requireRepo, defs, err
}

func validateFieldsRaw(defs []FieldDef, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("fields is not a JSON object: %w", err)
	}
	return ValidateFields(defs, m)
}

// CreateResult reports whether the write created a card or refreshed one via
// dedup_key (200 vs 201 at the API layer).
type CreateResult struct {
	Card      Card
	Refreshed bool
}

// CreateCard inserts a card — or, when dedup_key matches a live card on the
// board, refreshes that card's content (title/body/fields/tags ONLY: never
// column, rank, priority, or archive state — a sweep must not undo curation).
func CreateCard(ctx context.Context, pool *pgxpool.Pool, boardID string, p CardParams, ev EventMeta) (CreateResult, error) {
	if err := normalizeCardParams(&p); err != nil {
		return CreateResult{}, err
	}
	if p.Links == nil && p.AddLinks != nil { // a new card has nothing to append to
		p.Links = p.AddLinks
	}
	if err := validateParamLinks(p); err != nil {
		return CreateResult{}, err
	}
	if p.Title == nil || *p.Title == "" {
		return CreateResult{}, fmt.Errorf("title required")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return CreateResult{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned

	if err := lockBoard(ctx, tx, boardID); err != nil {
		return CreateResult{}, err
	}
	requireRepo, defs, err := loadBoardMeta(ctx, tx, boardID)
	if err != nil {
		return CreateResult{}, err
	}
	if err := validateFieldsRaw(defs, p.Fields); err != nil {
		return CreateResult{}, err
	}

	// External-id path: the external driver (e.g. blerg-ops) owns these cards and
	// re-pushes idempotently — refresh content AND move the card when a column
	// is specified. Unlike dedup_key, the driver is allowed to drive placement.
	if p.ExternalID != nil && *p.ExternalID != "" {
		existing, err := scanCard(tx.QueryRow(ctx,
			`SELECT `+cardCols+` FROM cards WHERE board_id = $1 AND external_id = $2`,
			boardID, *p.ExternalID))
		if err == nil {
			set, args := []string{"version = version + 1", "updated_at = now()"}, []any{}
			add := func(col string, v any) {
				args = append(args, v)
				set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
			}
			add("title", *p.Title)
			if p.Body != nil {
				add("body", *p.Body)
			}
			if len(p.Fields) > 0 {
				add("fields", p.Fields)
			}
			if p.Type != nil && *p.Type != "" {
				add("type", *p.Type)
			}
			if p.ColumnID != nil && *p.ColumnID != "" &&
				(existing.ColumnID == nil || *existing.ColumnID != *p.ColumnID) {
				// The target column must be on THIS board: the FK alone would
				// let a refresh drag the card into another board's column.
				// Same error as the create path's unresolvable column.
				var onBoard bool
				if err := tx.QueryRow(ctx,
					`SELECT EXISTS (SELECT 1 FROM board_columns WHERE id = $1 AND board_id = $2)`,
					*p.ColumnID, boardID).Scan(&onBoard); err != nil {
					return CreateResult{}, err
				}
				if !onBoard {
					return CreateResult{}, fmt.Errorf("board has no columns")
				}
				// Move to the end of the driver's target column.
				var maxRank *string
				if err := tx.QueryRow(ctx,
					`SELECT max(rank) FROM cards WHERE column_id = $1`, *p.ColumnID).Scan(&maxRank); err != nil {
					return CreateResult{}, err
				}
				prevRank := ""
				if maxRank != nil {
					prevRank = *maxRank
				}
				rank, err := RankBetween(prevRank, "")
				if err != nil {
					return CreateResult{}, err
				}
				add("column_id", *p.ColumnID)
				add("rank", rank)
				set = append(set, "archived_at = NULL")
			}
			args = append(args, existing.ID)
			card, err := scanCard(tx.QueryRow(ctx,
				`UPDATE cards SET `+joinSet(set)+fmt.Sprintf(` WHERE id = $%d RETURNING `, len(args))+cardCols,
				args...))
			if err != nil {
				return CreateResult{}, err
			}
			fromCol, toCol := existing.ColumnID, card.ColumnID
			evType := "refreshed"
			moved := p.ColumnID != nil && (fromCol == nil || toCol == nil || *fromCol != *toCol)
			if moved {
				evType = "moved"
			}
			if err := appendEvent(ctx, tx, card.ID, evType, ev, fromCol, toCol, map[string]any{
				"external_id": *p.ExternalID,
			}); err != nil {
				return CreateResult{}, err
			}
			if moved && toCol != nil {
				if err := enqueueStandingAgents(ctx, tx, *toCol, card.ID, ev.ActorTokenID); err != nil {
					return CreateResult{}, err
				}
			}
			if err := loadCardExtras(ctx, tx, &card); err != nil {
				return CreateResult{}, err
			}
			return CreateResult{Card: card, Refreshed: true}, tx.Commit(ctx)
		}
		if !errors.Is(err, ErrNotFound) {
			return CreateResult{}, err
		}
	}

	// Dedup path: refresh the existing live card.
	if p.DedupKey != nil && *p.DedupKey != "" {
		existing, err := scanCard(tx.QueryRow(ctx,
			`SELECT `+cardCols+` FROM cards
			 WHERE board_id = $1 AND dedup_key = $2 AND archived_at IS NULL`,
			boardID, *p.DedupKey))
		if err == nil {
			if p.IfMatchOnHit != nil && *p.IfMatchOnHit != existing.Version {
				return CreateResult{}, ErrStaleVersion
			}
			set, args := []string{"version = version + 1", "updated_at = now()"}, []any{}
			add := func(col string, v any) {
				args = append(args, v)
				set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
			}
			add("title", *p.Title)
			if p.Body != nil {
				add("body", *p.Body)
			}
			if len(p.Fields) > 0 {
				add("fields", p.Fields)
			}
			args = append(args, existing.ID)
			card, err := scanCard(tx.QueryRow(ctx,
				`UPDATE cards SET `+joinSet(set)+fmt.Sprintf(` WHERE id = $%d RETURNING `, len(args))+cardCols,
				args...))
			if err != nil {
				return CreateResult{}, err
			}
			if p.Tags != nil {
				if err := replaceTags(ctx, tx, card.ID, *p.Tags); err != nil {
					return CreateResult{}, err
				}
			}
			if err := appendEvent(ctx, tx, card.ID, "refreshed", ev, nil, nil, map[string]any{
				"dedup_key": *p.DedupKey,
			}); err != nil {
				return CreateResult{}, err
			}
			if err := loadCardExtras(ctx, tx, &card); err != nil {
				return CreateResult{}, err
			}
			return CreateResult{Card: card, Refreshed: true}, tx.Commit(ctx)
		}
		if !errors.Is(err, ErrNotFound) {
			return CreateResult{}, err
		}
	}

	var repos []string
	if p.Repos != nil {
		repos = *p.Repos
	}
	if err := validateRepos(ctx, tx, boardID, requireRepo, repos); err != nil {
		return CreateResult{}, err
	}

	// Resolve target column: explicit, else first by rank.
	var columnID string
	if p.ColumnID != nil && *p.ColumnID != "" {
		err = tx.QueryRow(ctx,
			`SELECT id FROM board_columns WHERE id = $1 AND board_id = $2`,
			*p.ColumnID, boardID).Scan(&columnID)
	} else {
		err = tx.QueryRow(ctx,
			`SELECT id FROM board_columns WHERE board_id = $1 ORDER BY rank LIMIT 1`,
			boardID).Scan(&columnID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateResult{}, fmt.Errorf("board has no columns")
	}
	if err != nil {
		return CreateResult{}, err
	}

	// Allocate number + rank under the board lock.
	var number int
	if err := tx.QueryRow(ctx,
		`UPDATE boards SET next_card_number = next_card_number + 1
		 WHERE id = $1 RETURNING next_card_number - 1`, boardID).Scan(&number); err != nil {
		return CreateResult{}, err
	}
	var maxRank *string
	if err := tx.QueryRow(ctx,
		`SELECT max(rank) FROM cards WHERE column_id = $1`, columnID).Scan(&maxRank); err != nil {
		return CreateResult{}, err
	}
	prev := ""
	if maxRank != nil {
		prev = *maxRank
	}
	rank, err := RankBetween(prev, "")
	if err != nil {
		return CreateResult{}, err
	}

	typ := "task"
	if p.Type != nil && *p.Type != "" {
		typ = *p.Type
	}
	priority := "medium"
	if p.Priority != nil {
		priority = *p.Priority
	}
	fields := p.Fields
	if len(fields) == 0 {
		fields = json.RawMessage(`{}`)
	}

	card, err := scanCard(tx.QueryRow(ctx, `
		INSERT INTO cards (board_id, number, column_id, type, title, body, priority, size, model,
			fields, dedup_key, external_id, rank)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING `+cardCols,
		boardID, number, columnID, typ, *p.Title, p.Body, priority, p.Size, deref(p.Model),
		fields, emptyToNil(p.DedupKey), emptyToNil(p.ExternalID), rank))
	if err != nil {
		if isUniqueViolation(err, "cards_external_id_uidx") {
			return CreateResult{}, ErrDuplicateExternal
		}
		return CreateResult{}, fmt.Errorf("insert card: %w", err)
	}

	for i, repo := range repos {
		if _, err := tx.Exec(ctx,
			`INSERT INTO card_repos (card_id, repo, rank) VALUES ($1,$2,$3)`,
			card.ID, repo, fmt.Sprintf("%04d", i)); err != nil {
			return CreateResult{}, err
		}
	}
	card.Repos = repos
	if p.Tags != nil {
		if err := replaceTags(ctx, tx, card.ID, *p.Tags); err != nil {
			return CreateResult{}, err
		}
		card.Tags = *p.Tags
	}
	if p.Links != nil {
		if err := replaceLinks(ctx, tx, card.ID, *p.Links); err != nil {
			return CreateResult{}, err
		}
		card.Links = *p.Links
	}

	if err := appendEvent(ctx, tx, card.ID, "created", ev, nil, &columnID, nil); err != nil {
		return CreateResult{}, err
	}
	if err := enqueueStandingAgents(ctx, tx, columnID, card.ID, ev.ActorTokenID); err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Card: card}, tx.Commit(ctx)
}

func emptyToNil(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

func isUniqueViolation(err error, constraint string) bool {
	return err != nil && strings.Contains(err.Error(), constraint)
}

// GetCard loads a card with repos, tags, links, and dependencies. The id may
// be a uuid or a board-scoped number when boardID is supplied.
func GetCard(ctx context.Context, pool *pgxpool.Pool, id string) (Card, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Card{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned
	card, err := scanCard(tx.QueryRow(ctx, `SELECT `+cardCols+` FROM cards WHERE id = $1`, id))
	if err != nil {
		return Card{}, err
	}
	if err := loadCardExtras(ctx, tx, &card); err != nil {
		return Card{}, err
	}
	return card, tx.Commit(ctx)
}

// GetCardByNumber resolves a board-scoped human number ("#42").
func GetCardByNumber(ctx context.Context, pool *pgxpool.Pool, boardID string, number int) (Card, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Card{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned
	card, err := scanCard(tx.QueryRow(ctx,
		`SELECT `+cardCols+` FROM cards WHERE board_id = $1 AND number = $2`, boardID, number))
	if err != nil {
		return Card{}, err
	}
	if err := loadCardExtras(ctx, tx, &card); err != nil {
		return Card{}, err
	}
	return card, tx.Commit(ctx)
}

func loadCardExtras(ctx context.Context, tx pgx.Tx, c *Card) error {
	rows, err := tx.Query(ctx,
		`SELECT repo FROM card_repos WHERE card_id = $1 ORDER BY rank`, c.ID)
	if err != nil {
		return err
	}
	c.Repos = nil
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			rows.Close()
			return err
		}
		c.Repos = append(c.Repos, r)
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT tag FROM card_tags WHERE card_id = $1 ORDER BY tag`, c.ID)
	if err != nil {
		return err
	}
	c.Tags = nil
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		c.Tags = append(c.Tags, t)
	}
	rows.Close()

	rows, err = tx.Query(ctx,
		`SELECT kind, url, label FROM card_links WHERE card_id = $1 ORDER BY rank`, c.ID)
	if err != nil {
		return err
	}
	c.Links = nil
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.Kind, &l.URL, &l.Label); err != nil {
			rows.Close()
			return err
		}
		c.Links = append(c.Links, l)
	}
	rows.Close()

	rows, err = tx.Query(ctx,
		`SELECT bl.id, bl.number, bl.title, `+blockerSatisfiedSQL+` `+blockerJoinSQL+`
		 WHERE d.card_id = $1 ORDER BY bl.number`, c.ID)
	if err != nil {
		return err
	}
	c.DependsOn, c.Blockers = nil, nil
	for rows.Next() {
		var b Blocker
		if err := rows.Scan(&b.ID, &b.Number, &b.Title, &b.Satisfied); err != nil {
			rows.Close()
			return err
		}
		c.DependsOn = append(c.DependsOn, b.ID)
		c.Blockers = append(c.Blockers, b)
	}
	rows.Close()
	return rows.Err()
}

func replaceTags(ctx context.Context, tx pgx.Tx, cardID string, tags []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM card_tags WHERE card_id = $1`, cardID); err != nil {
		return err
	}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO card_tags (card_id, tag) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			cardID, t); err != nil {
			return err
		}
	}
	return nil
}

// onlyAddsLinks reports whether a patch carries nothing but add_links (and guards).
func onlyAddsLinks(p CardParams) bool {
	p.AddLinks, p.IfMatch, p.IfMatchOnHit = nil, nil, nil
	return reflect.DeepEqual(p, CardParams{})
}

// appendLinks adds links after the card's existing ones. It runs inside UpdateCard's transaction, which holds
// the card row, so concurrent appends serialise.
func appendLinks(ctx context.Context, tx pgx.Tx, cardID string, links []Link) error {
	// After the highest rank in use, not after the number of rows: a replace that repeated an entry left a gap.
	var n int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(rank::int) + 1, 0) FROM card_links WHERE card_id = $1`, cardID).Scan(&n); err != nil {
		return err
	}
	for _, l := range links {
		tag, err := tx.Exec(ctx,
			`INSERT INTO card_links (card_id, kind, url, label, rank)
			 VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
			cardID, l.Kind, l.URL, l.Label, fmt.Sprintf("%04d", n))
		if err != nil {
			return err
		}
		n += int(tag.RowsAffected())
	}
	return nil
}

func replaceLinks(ctx context.Context, tx pgx.Tx, cardID string, links []Link) error {
	if _, err := tx.Exec(ctx, `DELETE FROM card_links WHERE card_id = $1`, cardID); err != nil {
		return err
	}
	for i, l := range links {
		if _, err := tx.Exec(ctx,
			`INSERT INTO card_links (card_id, kind, url, label, rank)
			 VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
			cardID, l.Kind, l.URL, l.Label, fmt.Sprintf("%04d", i)); err != nil {
			return err
		}
	}
	return nil
}

// changedFields names the card fields a patch actually changes, comparing each
// patched value against the row as it stands. UpdateCard records the result in
// the 'updated' event's data as {"fields": [...]}, which is what lets a reader
// tell a body rewrite from a tag, priority or title edit — an 'updated' event
// on its own only says "something changed", and the adversarial re-review
// debounce (newestAuthorActivity in internal/api/review.go) needs the
// difference: on a spec card the reviewed artifact is the body, so curating a
// card that is sitting in review must not look like the author revising it.
//
// Values are compared, not merely spotted in the patch: a client that PATCHes
// a whole card back with only its tags edited would otherwise report a body
// rewrite it did not make. An empty list is a real answer — a no-op write —
// and is recorded as such.
//
// Scalars compare exactly. The collections (repos, tags, links) compare after
// only light normalization, so a patch that reorders or repeats entries can
// report a change it did not make. That imprecision is deliberate: nothing
// keys on those names, and the field that gates a re-review — body — is
// compared exactly.
func changedFields(before Card, p CardParams) []string {
	changed := []string{}
	mark := func(name string, differs bool) {
		if differs {
			changed = append(changed, name)
		}
	}
	if p.Title != nil {
		mark("title", *p.Title != before.Title)
	}
	if p.Body != nil {
		mark("body", *p.Body != deref(before.Body))
	}
	if p.Type != nil && *p.Type != "" {
		mark("type", *p.Type != before.Type)
	}
	if p.Priority != nil {
		mark("priority", *p.Priority != before.Priority)
	}
	if p.Size != nil {
		mark("size", *p.Size != deref(before.Size))
	}
	if p.Model != nil {
		mark("model", *p.Model != before.Model)
	}
	if p.AutoMerge != nil {
		mark("auto_merge", *p.AutoMerge != before.AutoMerge)
	}
	if len(p.Fields) > 0 {
		mark("fields", !sameJSON(p.Fields, before.Fields))
	}
	if p.DedupKey != nil {
		mark("dedup_key", *p.DedupKey != deref(before.DedupKey))
	}
	if p.Repos != nil {
		mark("repos", !slices.Equal(*p.Repos, before.Repos))
	}
	if p.Tags != nil {
		mark("tags", !slices.Equal(normalizeTags(*p.Tags), before.Tags))
	}
	if p.AddLinks != nil {
		mark("links", len(newLinks(before.Links, *p.AddLinks)) > 0)
	}
	if p.Links != nil {
		mark("links", !sameLinks(*p.Links, before.Links))
	}
	return changed
}

// normalizeTags mirrors what replaceTags stores (trimmed, no blanks, no
// duplicates) and the order card_tags is read back in, so the two are
// comparable.
func normalizeTags(tags []string) []string {
	out, seen := []string{}, map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

func sameLinks(a, b []Link) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].URL != b[i].URL ||
			deref(a[i].Label) != deref(b[i].Label) {
			return false
		}
	}
	return true
}

// sameJSON compares two JSON documents by value, so key order and whitespace
// (which jsonb does not preserve on the way back out) don't read as an edit.
func sameJSON(a, b json.RawMessage) bool {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// UpdateCard patches card content. Every change bumps version; If-Match
// (when supplied) is checked against the pre-write version.
func UpdateCard(ctx context.Context, pool *pgxpool.Pool, cardID string, p CardParams, ev EventMeta) (Card, error) {
	if err := normalizeCardParams(&p); err != nil {
		return Card{}, err
	}
	if err := validateParamLinks(p); err != nil {
		return Card{}, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Card{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned

	card, err := scanCard(tx.QueryRow(ctx,
		`SELECT `+cardCols+` FROM cards WHERE id = $1 FOR UPDATE`, cardID))
	if err != nil {
		return Card{}, err
	}
	if p.IfMatch != nil && *p.IfMatch != card.Version {
		return Card{}, ErrStaleVersion
	}
	requireRepo, defs, err := loadBoardMeta(ctx, tx, card.BoardID)
	if err != nil {
		return Card{}, err
	}
	if err := validateFieldsRaw(defs, p.Fields); err != nil {
		return Card{}, err
	}
	// Snapshot the row as it stands so the event can name what this patch
	// actually changes. Only the collections need the extra reads, and only
	// when the patch carries them.
	before := card
	if p.Repos != nil || p.Tags != nil || p.Links != nil || p.AddLinks != nil {
		if err := loadCardExtras(ctx, tx, &before); err != nil {
			return Card{}, err
		}
	}
	// An append of links the card already has is not a change: no version bump, no event.
	if p.AddLinks != nil && onlyAddsLinks(p) && len(newLinks(before.Links, *p.AddLinks)) == 0 {
		return before, nil
	}

	set, args := []string{"version = version + 1", "updated_at = now()"}, []any{}
	add := func(col string, v any) {
		args = append(args, v)
		set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if p.Title != nil {
		add("title", *p.Title)
	}
	if p.AutoMerge != nil {
		add("auto_merge", *p.AutoMerge)
	}
	if p.Body != nil {
		add("body", *p.Body)
	}
	if p.Type != nil && *p.Type != "" {
		add("type", *p.Type)
	}
	if p.Priority != nil {
		add("priority", *p.Priority)
	}
	if p.Size != nil {
		if *p.Size == "" {
			set = append(set, "size = NULL")
		} else {
			add("size", *p.Size)
		}
	}
	if len(p.Fields) > 0 {
		add("fields", p.Fields)
	}
	if p.Model != nil {
		add("model", *p.Model)
	}
	if p.DedupKey != nil {
		add("dedup_key", emptyToNil(p.DedupKey))
	}
	args = append(args, cardID)
	card, err = scanCard(tx.QueryRow(ctx,
		`UPDATE cards SET `+joinSet(set)+fmt.Sprintf(` WHERE id = $%d RETURNING `, len(args))+cardCols,
		args...))
	if err != nil {
		if isUniqueViolation(err, "cards_dedup_key_uidx") {
			return Card{}, ErrDuplicateDedupKey
		}
		return Card{}, err
	}

	if p.Repos != nil {
		if err := validateRepos(ctx, tx, card.BoardID, requireRepo, *p.Repos); err != nil {
			return Card{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM card_repos WHERE card_id = $1`, cardID); err != nil {
			return Card{}, err
		}
		for i, repo := range *p.Repos {
			if _, err := tx.Exec(ctx,
				`INSERT INTO card_repos (card_id, repo, rank) VALUES ($1,$2,$3)`,
				cardID, repo, fmt.Sprintf("%04d", i)); err != nil {
				return Card{}, err
			}
		}
	}
	if p.Tags != nil {
		if err := replaceTags(ctx, tx, cardID, *p.Tags); err != nil {
			return Card{}, err
		}
	}
	if p.Links != nil {
		if err := replaceLinks(ctx, tx, cardID, *p.Links); err != nil {
			return Card{}, err
		}
	}
	if p.AddLinks != nil {
		if err := appendLinks(ctx, tx, cardID, *p.AddLinks); err != nil {
			return Card{}, err
		}
	}

	if err := appendEvent(ctx, tx, cardID, "updated", ev, nil, nil,
		map[string]any{"fields": changedFields(before, p)}); err != nil {
		return Card{}, err
	}
	if err := loadCardExtras(ctx, tx, &card); err != nil {
		return Card{}, err
	}
	return card, tx.Commit(ctx)
}

// MoveCard moves a card to a column, placing it before beforeCardID or at the
// end. Takes the board lock (rank allocation); bumps version.
func MoveCard(ctx context.Context, pool *pgxpool.Pool, cardID, toColumnID string, beforeCardID *string, ifMatch *int, ev EventMeta) (Card, error) {
	return moveCard(ctx, pool, cardID, toColumnID, beforeCardID, ifMatch, nil, ev)
}

// ClaimCard is MoveCard for a caller that read the card a while ago and must
// not overwrite what happened since: it moves the card only if it is still
// sitting, unarchived, in fromColumnID, and returns ErrCardMoved otherwise.
//
// `ifMatch` cannot express this. A version guard fails on ANY intervening
// write — and the dispatcher's own spawn writes one (it links the session to
// the card) — so the only writer it would reliably reject is itself. The
// column is the thing the claim actually depends on.
//
// The check happens under the board lock, inside the move's transaction, so
// there is no window between testing and moving.
func ClaimCard(ctx context.Context, pool *pgxpool.Pool, cardID, fromColumnID, toColumnID string, ev EventMeta) (Card, error) {
	return moveCard(ctx, pool, cardID, toColumnID, nil, nil, &fromColumnID, ev)
}

func moveCard(ctx context.Context, pool *pgxpool.Pool, cardID, toColumnID string, beforeCardID *string, ifMatch *int, fromColumnID *string, ev EventMeta) (Card, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Card{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned

	card, err := scanCard(tx.QueryRow(ctx,
		`SELECT `+cardCols+` FROM cards WHERE id = $1`, cardID))
	if err != nil {
		return Card{}, err
	}
	if err := lockBoard(ctx, tx, card.BoardID); err != nil {
		return Card{}, err
	}
	// Re-read post-lock; the pre-lock read only located the board.
	card, err = scanCard(tx.QueryRow(ctx,
		`SELECT `+cardCols+` FROM cards WHERE id = $1`, cardID))
	if err != nil {
		return Card{}, err
	}
	if ifMatch != nil && *ifMatch != card.Version {
		return Card{}, ErrStaleVersion
	}
	if fromColumnID != nil {
		// Archived counts as moved: the UPDATE below clears archived_at, so
		// without this an archived card would come back to life in the target
		// column — the loudest possible way to overwrite someone.
		if card.ArchivedAt != nil || card.ColumnID == nil || *card.ColumnID != *fromColumnID {
			return Card{}, ErrCardMoved
		}
	}

	var colID string
	if err := tx.QueryRow(ctx,
		`SELECT id FROM board_columns WHERE id = $1 AND board_id = $2`,
		toColumnID, card.BoardID).Scan(&colID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Card{}, fmt.Errorf("column not on this board")
		}
		return Card{}, err
	}

	// Compute the new rank.
	var prev, next string
	if beforeCardID != nil && *beforeCardID != "" {
		var beforeRank string
		if err := tx.QueryRow(ctx,
			`SELECT rank FROM cards WHERE id = $1 AND column_id = $2`,
			*beforeCardID, colID).Scan(&beforeRank); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return Card{}, err
			}
		} else {
			next = beforeRank
			var prevRank *string
			if err := tx.QueryRow(ctx,
				`SELECT max(rank) FROM cards WHERE column_id = $1 AND rank < $2 AND id <> $3`,
				colID, beforeRank, cardID).Scan(&prevRank); err != nil {
				return Card{}, err
			}
			if prevRank != nil {
				prev = *prevRank
			}
		}
	}
	if next == "" {
		var maxRank *string
		if err := tx.QueryRow(ctx,
			`SELECT max(rank) FROM cards WHERE column_id = $1 AND id <> $2`,
			colID, cardID).Scan(&maxRank); err != nil {
			return Card{}, err
		}
		if maxRank != nil {
			prev = *maxRank
		}
	}
	rank, err := RankBetween(prev, next)
	if err != nil {
		return Card{}, err
	}

	fromCol := card.ColumnID
	card, err = scanCard(tx.QueryRow(ctx, `
		UPDATE cards SET column_id = $2, rank = $3, archived_at = NULL,
			version = version + 1, updated_at = now()
		WHERE id = $1 RETURNING `+cardCols, cardID, colID, rank))
	if err != nil {
		return Card{}, err
	}
	if err := appendEvent(ctx, tx, cardID, "moved", ev, fromCol, &colID, nil); err != nil {
		return Card{}, err
	}
	// Re-ranking within the same column (drag reorder) isn't "entering" it.
	if fromCol == nil || *fromCol != colID {
		if err := enqueueStandingAgents(ctx, tx, colID, cardID, ev.ActorTokenID); err != nil {
			return Card{}, err
		}
	}
	if err := loadCardExtras(ctx, tx, &card); err != nil {
		return Card{}, err
	}
	return card, tx.Commit(ctx)
}

// ArchiveCard nulls column_id and stamps archived_at; bumps version.
func ArchiveCard(ctx context.Context, pool *pgxpool.Pool, cardID string, ifMatch *int, ev EventMeta) (Card, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Card{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned
	card, err := scanCard(tx.QueryRow(ctx,
		`SELECT `+cardCols+` FROM cards WHERE id = $1 FOR UPDATE`, cardID))
	if err != nil {
		return Card{}, err
	}
	if ifMatch != nil && *ifMatch != card.Version {
		return Card{}, ErrStaleVersion
	}
	fromCol := card.ColumnID
	card, err = scanCard(tx.QueryRow(ctx, `
		UPDATE cards SET column_id = NULL, archived_at = now(),
			version = version + 1, updated_at = now()
		WHERE id = $1 RETURNING `+cardCols, cardID))
	if err != nil {
		return Card{}, err
	}
	if err := appendEvent(ctx, tx, cardID, "archived", ev, fromCol, nil, nil); err != nil {
		return Card{}, err
	}
	return card, tx.Commit(ctx)
}

// DeleteCard hard-deletes (events cascade).
func DeleteCard(ctx context.Context, pool *pgxpool.Pool, cardID string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM cards WHERE id = $1`, cardID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListCards returns a board's live cards ordered by column rank then card rank.
func ListCards(ctx context.Context, pool *pgxpool.Pool, boardID string, archived bool) ([]Card, error) {
	where := `c.board_id = $1 AND c.archived_at IS NULL`
	if archived {
		where = `c.board_id = $1 AND c.archived_at IS NOT NULL`
	}
	rows, err := pool.Query(ctx, `
		SELECT `+prefixCols("c")+` FROM cards c
		LEFT JOIN board_columns col ON col.id = c.column_id
		WHERE `+where+`
		ORDER BY col.rank NULLS LAST, c.rank`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := attachExtras(ctx, pool, out); err != nil {
		return nil, err
	}
	return out, nil
}

// cardColsC is cardCols with every column qualified by the alias "c" — kept
// explicit rather than derived because to_char(...) expressions contain commas.
const cardColsC = `c.id, c.board_id, c.number, c.column_id, c.type, c.title, c.body, c.priority, c.size, c.model,
	c.fields, c.dedup_key, c.external_id, c.rank, c.auto_merge, c.run_attempts, c.merged_sha,
	to_char(c.merged_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(c.stuck_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(c.stale_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(c.archived_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), c.version, c.gate_flag,
	to_char(c.created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), to_char(c.updated_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`

func prefixCols(string) string { return cardColsC }

// attachExtras bulk-loads repos/tags/links/deps for a card slice.
func attachExtras(ctx context.Context, pool *pgxpool.Pool, cards []Card) error {
	if len(cards) == 0 {
		return nil
	}
	idx := make(map[string]*Card, len(cards))
	ids := make([]string, len(cards))
	for i := range cards {
		idx[cards[i].ID] = &cards[i]
		ids[i] = cards[i].ID
	}
	rows, err := pool.Query(ctx,
		`SELECT card_id, repo FROM card_repos WHERE card_id = ANY($1) ORDER BY rank`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, repo string
		if err := rows.Scan(&id, &repo); err != nil {
			rows.Close()
			return err
		}
		idx[id].Repos = append(idx[id].Repos, repo)
	}
	rows.Close()

	rows, err = pool.Query(ctx,
		`SELECT card_id, tag FROM card_tags WHERE card_id = ANY($1) ORDER BY tag`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, tag string
		if err := rows.Scan(&id, &tag); err != nil {
			rows.Close()
			return err
		}
		idx[id].Tags = append(idx[id].Tags, tag)
	}
	rows.Close()

	rows, err = pool.Query(ctx,
		`SELECT card_id, kind, url, label FROM card_links WHERE card_id = ANY($1) ORDER BY rank`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var l Link
		if err := rows.Scan(&id, &l.Kind, &l.URL, &l.Label); err != nil {
			rows.Close()
			return err
		}
		idx[id].Links = append(idx[id].Links, l)
	}
	rows.Close()

	rows, err = pool.Query(ctx,
		`SELECT d.card_id, bl.id, bl.number, bl.title, `+blockerSatisfiedSQL+` `+blockerJoinSQL+`
		 WHERE d.card_id = ANY($1) ORDER BY bl.number`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var b Blocker
		if err := rows.Scan(&id, &b.ID, &b.Number, &b.Title, &b.Satisfied); err != nil {
			rows.Close()
			return err
		}
		idx[id].DependsOn = append(idx[id].DependsOn, b.ID)
		idx[id].Blockers = append(idx[id].Blockers, b)
	}
	rows.Close()
	return rows.Err()
}

// TopDispatchable returns the highest-ranked card in a column that a worker
// could actually be started on. Not simply the top card: it SKIPS over ones
// that are unusable and returns the first usable one below them. Skipped are
//
//   - archived cards, and stuck ones — re-picking a stuck card is an infinite
//     loop, so it stays visible to the human but invisible here;
//   - cards with an unsatisfied hard dependency (see blockerSatisfiedSQL).
//
// Skipping, not stalling, is the whole point of the dependency clause: one
// blocked card at the top of ready must not halt the board behind it.
//
// Only DIRECT blockers are checked. AddDependency refuses cycles, so the graph
// is a DAG, and a transitive blocker holds back its own dependent in turn — so
// A→B→C can never let A through early on a one-level check.
func TopDispatchable(ctx context.Context, pool *pgxpool.Pool, columnID string) (Card, error) {
	c, err := scanCard(pool.QueryRow(ctx,
		`SELECT `+cardCols+` FROM cards
		 WHERE column_id = $1 AND archived_at IS NULL AND stuck_at IS NULL
		   AND NOT `+hasUnsatisfiedBlockerSQL("cards.id")+`
		 ORDER BY rank LIMIT 1`, columnID))
	if err != nil {
		return c, err
	}
	// repos/tags/links live in join tables — without them the dispatcher
	// mistakes every card for repo-less (stuck-flagged the whole queue once)
	cards := []Card{c}
	if err := attachExtras(ctx, pool, cards); err != nil {
		return c, err
	}
	return cards[0], nil
}

// CountBlocked reports how many live cards in a column TopDispatchable will
// skip because a hard dependency has not landed. Same predicate, counted
// instead of picked — so the run status can say "blocked" rather than leaving
// a board that cannot start anything looking merely idle.
func CountBlocked(ctx context.Context, pool *pgxpool.Pool, columnID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM cards
		 WHERE column_id = $1 AND archived_at IS NULL
		   AND `+hasUnsatisfiedBlockerSQL("cards.id"), columnID).Scan(&n)
	return n, err
}
