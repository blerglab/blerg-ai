package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Board struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Description       *string         `json:"description"`
	DefaultRunner     *string         `json:"default_runner"`
	DeployURL         *string         `json:"deploy_url"`
	GitBase           string          `json:"git_base"`
	Model             string          `json:"model"`
	ReviewerModel     string          `json:"reviewer_model"`
	DiscussModel      string          `json:"discuss_model"`
	ChatModel         string          `json:"chat_model"`
	FieldSchema       json.RawMessage `json:"field_schema"`
	RequireRepo       bool            `json:"require_repo"`
	GateEnabled       bool            `json:"gate_enabled"`
	GateOnUnavailable string          `json:"gate_on_unavailable"`
	GateOnDispute     string          `json:"gate_on_dispute"`
	// DrivenBy names the external system that owns this board's card
	// lifecycle (e.g. "blerg-ops"), when one does. Nil/empty = blerg-board-native.
	DrivenBy *string `json:"driven_by"`
	// CIPolicy decides what auto-merge does when a PR head reports no CI at
	// all: "required" (default) refuses and waits, "if_present" merges on the
	// adversarial approval alone. Only ever about ABSENT checks — a red check
	// blocks under both. See migration 016 and CIRequired.
	CIPolicy string `json:"ci_policy"`
	// Concurrency is how many cards a board run may have in flight at once —
	// the dispatcher's limit, read live on every tick. 0 parks the board: the
	// run stays active but dispatches nothing.
	//
	// Per BOARD, and not a spend ceiling: there is no global cap above the
	// dispatcher's per-board loop, so total parallelism is the sum across
	// running boards, and the limit counts cards in the work column rather
	// than live sessions. See the package comment in internal/api/boardrun.go.
	Concurrency int `json:"concurrency"`
	// AutomationEngine is the engine every session this board starts runs:
	// "claude" (default), "codex" or "hermes". See migration 017.
	AutomationEngine string `json:"automation_engine"`
	// AutomationTokenSet reports whether an automation token is configured.
	// The token itself is write-only and has no field here on purpose: this
	// struct is what every board read serializes, so a token held in it would
	// be one careless writeJSON away from a response. BoardAutomation reads it.
	AutomationTokenSet bool `json:"automation_token_set"`
	// AutomationTokenExpiresAt is the saved token's own expiry (nil when none
	// is set), so a human can see it coming without reading the token back.
	AutomationTokenExpiresAt *time.Time `json:"automation_token_expires_at"`
	// AutomationAccountID is the blerg-core account the token acts for — whose
	// engine credential every session this board starts spends (nil when none
	// is set). Shown so an admin knows whose credential card-writers can use.
	AutomationAccountID *string  `json:"automation_account_id"`
	CreatedAt           string   `json:"created_at"`
	UpdatedAt           string   `json:"updated_at"`
	Repos               []string `json:"repos"`
}

type Column struct {
	ID         string `json:"id"`
	BoardID    string `json:"board_id"`
	Rank       string `json:"rank"`
	Name       string `json:"name"`
	IsTerminal bool   `json:"is_terminal"`
}

// Terminal reports whether landing in this column means the work is done.
// is_terminal is the source of truth, but columns predating the flag (and
// hand-made ones) only carry the name, so a "done"-ish name counts too.
//
// This is THE definition — callers must not re-spell it. TerminalColumnSQL is
// the SQL twin; keep the two in step.
func (c Column) Terminal() bool {
	return c.IsTerminal || strings.Contains(strings.ToLower(c.Name), "done")
}

// TerminalColumnSQL renders Terminal() as a SQL predicate over a board_columns
// row under the given alias. NULL-safe: a card with no column is not terminal,
// so an outer-joined alias yields false rather than NULL.
func TerminalColumnSQL(alias string) string {
	return `coalesce(` + alias + `.is_terminal OR lower(` + alias + `.name) LIKE '%done%', false)`
}

const boardCols = `id, name, description, default_runner, deploy_url, git_base, model, reviewer_model,
	discuss_model, chat_model, field_schema, require_repo,
	gate_enabled, gate_on_unavailable, gate_on_dispute, driven_by, concurrency, ci_policy,
	automation_engine, automation_token <> '', automation_token_expires_at, automation_account_id,
	to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), to_char(updated_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`

func scanBoard(row pgx.Row) (Board, error) {
	var b Board
	err := row.Scan(&b.ID, &b.Name, &b.Description, &b.DefaultRunner, &b.DeployURL, &b.GitBase, &b.Model, &b.ReviewerModel,
		&b.DiscussModel, &b.ChatModel, &b.FieldSchema,
		&b.RequireRepo, &b.GateEnabled, &b.GateOnUnavailable, &b.GateOnDispute, &b.DrivenBy, &b.Concurrency,
		&b.CIPolicy, &b.AutomationEngine, &b.AutomationTokenSet, &b.AutomationTokenExpiresAt, &b.AutomationAccountID,
		&b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

type BoardParams struct {
	Name              string          `json:"name"`
	Description       *string         `json:"description"`
	DefaultRunner     *string         `json:"default_runner"`
	DeployURL         *string         `json:"deploy_url"`
	GitBase           *string         `json:"git_base"`
	Model             *string         `json:"model"`
	ReviewerModel     *string         `json:"reviewer_model"`
	DiscussModel      *string         `json:"discuss_model"`
	ChatModel         *string         `json:"chat_model"`
	FieldSchema       json.RawMessage `json:"field_schema"`
	RequireRepo       *bool           `json:"require_repo"`
	GateEnabled       *bool           `json:"gate_enabled"`
	GateOnUnavailable *string         `json:"gate_on_unavailable"`
	GateOnDispute     *string         `json:"gate_on_dispute"`
	DrivenBy          *string         `json:"driven_by"`
	Concurrency       *int            `json:"concurrency"`
	CIPolicy          *string         `json:"ci_policy"`
	// AutomationEngine: see Board.AutomationEngine. The automation TOKEN is
	// deliberately not a BoardParams field — it is set only through
	// SetBoardAutomationToken, after the API has verified who it belongs to,
	// so neither board create nor the MCP board tools can write one.
	AutomationEngine *string  `json:"automation_engine"`
	Repos            []string `json:"repos"`
	Columns          []string `json:"columns"` // initial column names, in order
	// TerminalColumns names the initial columns created with is_terminal set.
	// Not part of the wire format: only board templates fill it, so a request
	// body cannot reach it.
	TerminalColumns []string `json:"-"`
}

// checkConcurrency guards the one value the dispatcher cannot express. 0 is
// deliberately allowed — dispatchBoardLocked's loop simply never enters, which
// is exactly "park this board" — so the only bad input is a negative, and it
// gets a FieldError so the API answers 422 naming the field rather than a
// generic 500 out of the column's CHECK.
func checkConcurrency(n *int) error {
	if n != nil && *n < 0 {
		return &FieldError{
			Key: "concurrency",
			Msg: "must be 0 or more (0 parks the board: the run stays active, in-flight cards finish, nothing new is dispatched)",
		}
	}
	return nil
}

// CIPolicy values. Kept as constants because three packages spell them: the
// column's CHECK, the sweep's gate, and the MCP tool schema.
const (
	CIRequired  = "required"   // no CI reported → refuse and wait (the default)
	CIIfPresent = "if_present" // no CI reported → merge on the review alone
)

// checkCIPolicy answers 422 naming the field rather than letting the column's
// CHECK surface as a 500. The message lists the legal values because the
// caller is usually an agent, and "invalid input" without the alternatives
// costs it a round trip to find them.
func checkCIPolicy(p *string) error {
	if p == nil || *p == CIRequired || *p == CIIfPresent {
		return nil
	}
	return &FieldError{
		Key: "ci_policy",
		Msg: fmt.Sprintf("must be %q (a green check is mandatory, so a repo with no CI never auto-merges) "+
			"or %q (absent checks are not a failure; a red or pending check still blocks under both)",
			CIRequired, CIIfPresent),
	}
}

// Automation engines: the engines a board may start its sessions on. The
// column's CHECK is the backstop; checkAutomationEngine is the 422.
const (
	EngineClaude = "claude"
	EngineCodex  = "codex"
	EngineHermes = "hermes"
)

// ValidAutomationEngine reports whether e is one of the fixed engine values.
func ValidAutomationEngine(e string) bool {
	return e == EngineClaude || e == EngineCodex || e == EngineHermes
}

func checkAutomationEngine(p *string) error {
	if p == nil || ValidAutomationEngine(*p) {
		return nil
	}
	return &FieldError{
		Key: "automation_engine",
		Msg: fmt.Sprintf("must be %q, %q or %q", EngineClaude, EngineCodex, EngineHermes),
	}
}

// BoardAutomation is the identity a board starts runner sessions under: the
// raw automation token (a bearer credential — never log it, never put it in a
// response) and the engine. Token is "" when none is configured.
type BoardAutomation struct {
	Token  string
	Engine string
}

// GetBoardAutomation reads a board's automation token and engine. It is the
// ONLY reader of automation_token.
func GetBoardAutomation(ctx context.Context, pool *pgxpool.Pool, boardID string) (BoardAutomation, error) {
	var a BoardAutomation
	var stored string
	err := pool.QueryRow(ctx,
		`SELECT automation_token, automation_engine FROM boards WHERE id = $1`, boardID).
		Scan(&stored, &a.Engine)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	// Decrypt only here, at the moment of use. A value that does not open
	// (lost or rotated key, corruption) fails closed with a re-save message.
	a.Token, err = openAutomationToken(boardID, stored)
	return a, err
}

// AutomationTokenUpdate stores (Token != "") or clears (Token == "") a board's
// automation token, with the token's expiry and the account it acts for.
// Callers verify the token first; the db layer only writes it.
type AutomationTokenUpdate struct {
	Token     string
	ExpiresAt *time.Time
	AccountID string
}

// columns returns what the three automation columns are set to: the token
// SEALED for boardID (or "" to clear), its expiry and its account.
func (u AutomationTokenUpdate) columns(boardID string) (token string, expiresAt *time.Time, account *string, err error) {
	if u.Token == "" {
		return "", nil, nil, nil
	}
	sealed, err := sealAutomationToken(boardID, u.Token)
	if err != nil {
		return "", nil, nil, err
	}
	a := u.AccountID
	return sealed, u.ExpiresAt, &a, nil
}

// SetBoardAutomationToken writes only the automation token (tests, tools).
// The PATCH path goes through UpdateBoardWithAutomation so the token lands in
// the same statement as the rest of the update, or not at all.
func SetBoardAutomationToken(ctx context.Context, pool *pgxpool.Pool, boardID string, u AutomationTokenUpdate) error {
	token, exp, account, err := u.columns(boardID)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `
		UPDATE boards SET automation_token = $2, automation_token_expires_at = $3,
			automation_account_id = $4, updated_at = now()
		WHERE id = $1`, boardID, token, exp, account)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateBoard creates a board with its repos and initial columns.
func CreateBoard(ctx context.Context, pool *pgxpool.Pool, p BoardParams) (Board, error) {
	if p.Name == "" {
		return Board{}, fmt.Errorf("board name required")
	}
	schema := p.FieldSchema
	if len(schema) == 0 {
		schema = json.RawMessage(`[]`)
	}
	if _, err := ParseFieldSchema(schema); err != nil {
		return Board{}, err
	}
	if err := checkConcurrency(p.Concurrency); err != nil {
		return Board{}, err
	}
	if err := checkCIPolicy(p.CIPolicy); err != nil {
		return Board{}, err
	}
	if err := checkAutomationEngine(p.AutomationEngine); err != nil {
		return Board{}, err
	}
	engine := EngineClaude
	if p.AutomationEngine != nil {
		engine = *p.AutomationEngine
	}
	ciPolicy := CIRequired
	if p.CIPolicy != nil {
		ciPolicy = *p.CIPolicy
	}
	concurrency := 1
	if p.Concurrency != nil {
		concurrency = *p.Concurrency
	}
	// An API-created board with no repos would otherwise reject its first
	// card with a message nobody can act on (trial): default require_repo to
	// whether the board actually has any repos, not unconditionally true. A
	// board created WITH repos keeps the require_repo=true default; an
	// explicit RequireRepo always wins, so a caller that genuinely wants
	// require_repo=true on a repo-less board (or false on one with repos)
	// still gets exactly that.
	requireRepo := len(p.Repos) > 0
	if p.RequireRepo != nil {
		requireRepo = *p.RequireRepo
	}
	gateEnabled := false
	if p.GateEnabled != nil {
		gateEnabled = *p.GateEnabled
	}
	onUnavail, onDispute := "open", "open"
	if p.GateOnUnavailable != nil {
		onUnavail = *p.GateOnUnavailable
	}
	if p.GateOnDispute != nil {
		onDispute = *p.GateOnDispute
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Board{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned

	gitBase := ""
	if p.GitBase != nil {
		gitBase = *p.GitBase
	}
	b, err := scanBoard(tx.QueryRow(ctx, `
		INSERT INTO boards (name, description, default_runner, deploy_url, git_base, model, reviewer_model,
			discuss_model, chat_model,
			field_schema, require_repo, gate_enabled, gate_on_unavailable, gate_on_dispute, driven_by,
			concurrency, ci_policy, automation_engine)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		RETURNING `+boardCols,
		p.Name, p.Description, p.DefaultRunner, p.DeployURL, gitBase, deref(p.Model), deref(p.ReviewerModel),
		deref(p.DiscussModel), deref(p.ChatModel), schema,
		requireRepo, gateEnabled, onUnavail, onDispute, p.DrivenBy, concurrency, ciPolicy, engine))
	if err != nil {
		return Board{}, fmt.Errorf("insert board: %w", err)
	}

	for _, repo := range p.Repos {
		if _, err := tx.Exec(ctx,
			`INSERT INTO board_repos (board_id, repo) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			b.ID, repo); err != nil {
			return Board{}, fmt.Errorf("insert board repo: %w", err)
		}
	}
	b.Repos = p.Repos

	cols := p.Columns
	if len(cols) == 0 {
		cols = []string{"inbox", "ready", "in progress", "done"}
	}
	rank := ""
	for _, name := range cols {
		next, err := RankBetween(rank, "")
		if err != nil {
			return Board{}, err
		}
		rank = next
		if _, err := tx.Exec(ctx,
			`INSERT INTO board_columns (board_id, rank, name, is_terminal) VALUES ($1,$2,$3,$4)`,
			b.ID, rank, name, slices.Contains(p.TerminalColumns, name)); err != nil {
			return Board{}, fmt.Errorf("insert column: %w", err)
		}
	}

	return b, tx.Commit(ctx)
}

func GetBoard(ctx context.Context, pool *pgxpool.Pool, id string) (Board, error) {
	b, err := scanBoard(pool.QueryRow(ctx, `SELECT `+boardCols+` FROM boards WHERE id = $1`, id))
	if err != nil {
		return b, err
	}
	rows, err := pool.Query(ctx, `SELECT repo FROM board_repos WHERE board_id = $1 ORDER BY repo`, id)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return b, err
		}
		b.Repos = append(b.Repos, r)
	}
	return b, rows.Err()
}

func ListBoards(ctx context.Context, pool *pgxpool.Pool) ([]Board, error) {
	rows, err := pool.Query(ctx, `SELECT `+boardCols+` FROM boards ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Board
	for rows.Next() {
		b, err := scanBoard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UpdateBoard patches board settings. Nil pointers leave fields unchanged.
func UpdateBoard(ctx context.Context, pool *pgxpool.Pool, id string, p BoardParams) (Board, error) {
	return UpdateBoardWithAutomation(ctx, pool, id, p, nil)
}

// UpdateBoardWithAutomation is UpdateBoard that also writes the automation
// token (auto != nil) in the SAME statement and transaction: every field is
// validated before anything is written, so a PATCH refused for any field
// leaves the stored token exactly as it was.
func UpdateBoardWithAutomation(ctx context.Context, pool *pgxpool.Pool, id string, p BoardParams, auto *AutomationTokenUpdate) (Board, error) {
	if len(p.FieldSchema) > 0 {
		if _, err := ParseFieldSchema(p.FieldSchema); err != nil {
			return Board{}, err
		}
	}
	if err := checkConcurrency(p.Concurrency); err != nil {
		return Board{}, err
	}
	if err := checkCIPolicy(p.CIPolicy); err != nil {
		return Board{}, err
	}
	if err := checkAutomationEngine(p.AutomationEngine); err != nil {
		return Board{}, err
	}
	var tokenCol string
	var expCol *time.Time
	var accountCol *string
	if auto != nil {
		// Sealed before anything is written: no key, no PATCH.
		var err error
		if tokenCol, expCol, accountCol, err = auto.columns(id); err != nil {
			return Board{}, err
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Board{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned

	if err := lockBoard(ctx, tx, id); err != nil {
		return Board{}, err
	}

	set, args := []string{"updated_at = now()"}, []any{}
	add := func(col string, v any) {
		args = append(args, v)
		set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if p.Name != "" {
		add("name", p.Name)
	}
	if p.Description != nil {
		add("description", *p.Description)
	}
	if p.DefaultRunner != nil {
		add("default_runner", *p.DefaultRunner)
	}
	if p.DeployURL != nil {
		add("deploy_url", *p.DeployURL)
	}
	if p.GitBase != nil {
		add("git_base", *p.GitBase)
	}
	if p.Model != nil {
		add("model", *p.Model)
	}
	if p.ReviewerModel != nil {
		add("reviewer_model", *p.ReviewerModel)
	}
	if p.DiscussModel != nil {
		add("discuss_model", *p.DiscussModel)
	}
	if p.ChatModel != nil {
		add("chat_model", *p.ChatModel)
	}
	if len(p.FieldSchema) > 0 {
		add("field_schema", p.FieldSchema)
	}
	if p.RequireRepo != nil {
		add("require_repo", *p.RequireRepo)
	}
	if p.GateEnabled != nil {
		add("gate_enabled", *p.GateEnabled)
	}
	if p.GateOnUnavailable != nil {
		add("gate_on_unavailable", *p.GateOnUnavailable)
	}
	if p.GateOnDispute != nil {
		add("gate_on_dispute", *p.GateOnDispute)
	}
	if p.DrivenBy != nil {
		add("driven_by", *p.DrivenBy)
	}
	if p.Concurrency != nil {
		add("concurrency", *p.Concurrency)
	}
	if p.CIPolicy != nil {
		add("ci_policy", *p.CIPolicy)
	}
	if p.AutomationEngine != nil {
		add("automation_engine", *p.AutomationEngine)
	}
	if auto != nil {
		add("automation_token", tokenCol)
		add("automation_token_expires_at", expCol)
		add("automation_account_id", accountCol)
	}
	args = append(args, id)
	b, err := scanBoard(tx.QueryRow(ctx,
		`UPDATE boards SET `+joinSet(set)+fmt.Sprintf(` WHERE id = $%d RETURNING `, len(args))+boardCols,
		args...))
	if err != nil {
		return Board{}, err
	}

	if p.Repos != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM board_repos WHERE board_id = $1`, id); err != nil {
			return Board{}, err
		}
		for _, repo := range p.Repos {
			if _, err := tx.Exec(ctx,
				`INSERT INTO board_repos (board_id, repo) VALUES ($1,$2)`, id, repo); err != nil {
				return Board{}, err
			}
		}
		b.Repos = p.Repos
	}
	return b, tx.Commit(ctx)
}

func joinSet(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func DeleteBoard(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned
	// Cards first: the boards→board_columns cascade would otherwise trip
	// cards.column_id ON DELETE RESTRICT while live cards still reference
	// the columns being cascaded away.
	if _, err := tx.Exec(ctx, `DELETE FROM cards WHERE board_id = $1`, id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM boards WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// ── Columns ──────────────────────────────────────────────────────────────────

func ListColumns(ctx context.Context, pool *pgxpool.Pool, boardID string) ([]Column, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, board_id, rank, name, is_terminal FROM board_columns
		 WHERE board_id = $1 ORDER BY rank`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var c Column
		if err := rows.Scan(&c.ID, &c.BoardID, &c.Rank, &c.Name, &c.IsTerminal); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateColumn appends a column at the end (or after afterID).
func CreateColumn(ctx context.Context, pool *pgxpool.Pool, boardID, name string, isTerminal bool) (Column, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Column{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned
	if err := lockBoard(ctx, tx, boardID); err != nil {
		return Column{}, err
	}
	var maxRank *string
	if err := tx.QueryRow(ctx,
		`SELECT max(rank) FROM board_columns WHERE board_id = $1`, boardID).Scan(&maxRank); err != nil {
		return Column{}, err
	}
	prev := ""
	if maxRank != nil {
		prev = *maxRank
	}
	rank, err := RankBetween(prev, "")
	if err != nil {
		return Column{}, err
	}
	var c Column
	err = tx.QueryRow(ctx, `
		INSERT INTO board_columns (board_id, rank, name, is_terminal)
		VALUES ($1,$2,$3,$4) RETURNING id, board_id, rank, name, is_terminal`,
		boardID, rank, name, isTerminal).Scan(&c.ID, &c.BoardID, &c.Rank, &c.Name, &c.IsTerminal)
	if err != nil {
		return Column{}, err
	}
	return c, tx.Commit(ctx)
}

// DeleteColumn refuses when live cards remain (archive or move them first).
// The FK RESTRICT is the backstop; this returns the typed error.
func DeleteColumn(ctx context.Context, pool *pgxpool.Pool, columnID string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM cards WHERE column_id = $1`, columnID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrColumnHasCards
	}
	tag, err := tx.Exec(ctx, `DELETE FROM board_columns WHERE id = $1`, columnID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// RenameColumn updates a column's name.
func RenameColumn(ctx context.Context, pool *pgxpool.Pool, columnID, name string) error {
	tag, err := pool.Exec(ctx, `UPDATE board_columns SET name = $2 WHERE id = $1`, columnID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetColumn fetches a single column by id.
func GetColumn(ctx context.Context, pool *pgxpool.Pool, columnID string) (Column, error) {
	var c Column
	err := pool.QueryRow(ctx,
		`SELECT id, board_id, rank, name, is_terminal FROM board_columns WHERE id = $1`,
		columnID).Scan(&c.ID, &c.BoardID, &c.Rank, &c.Name, &c.IsTerminal)
	if errors.Is(err, pgx.ErrNoRows) {
		return Column{}, ErrNotFound
	}
	return c, err
}

// MoveColumn repositions a column among its board's siblings, placing it
// immediately before beforeColumnID, or at the end if beforeColumnID is
// nil/empty. Takes the board lock (rank allocation), same scheme as MoveCard.
func MoveColumn(ctx context.Context, pool *pgxpool.Pool, columnID string, beforeColumnID *string) (Column, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Column{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned

	var boardID string
	if err := tx.QueryRow(ctx,
		`SELECT board_id FROM board_columns WHERE id = $1`, columnID).Scan(&boardID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Column{}, ErrNotFound
		}
		return Column{}, err
	}
	if err := lockBoard(ctx, tx, boardID); err != nil {
		return Column{}, err
	}

	// Compute the new rank, same pattern as MoveCard.
	var prev, next string
	if beforeColumnID != nil && *beforeColumnID != "" {
		var beforeRank string
		if err := tx.QueryRow(ctx,
			`SELECT rank FROM board_columns WHERE id = $1 AND board_id = $2`,
			*beforeColumnID, boardID).Scan(&beforeRank); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return Column{}, err
			}
		} else {
			next = beforeRank
			var prevRank *string
			if err := tx.QueryRow(ctx,
				`SELECT max(rank) FROM board_columns WHERE board_id = $1 AND rank < $2 AND id <> $3`,
				boardID, beforeRank, columnID).Scan(&prevRank); err != nil {
				return Column{}, err
			}
			if prevRank != nil {
				prev = *prevRank
			}
		}
	}
	if next == "" {
		var maxRank *string
		if err := tx.QueryRow(ctx,
			`SELECT max(rank) FROM board_columns WHERE board_id = $1 AND id <> $2`,
			boardID, columnID).Scan(&maxRank); err != nil {
			return Column{}, err
		}
		if maxRank != nil {
			prev = *maxRank
		}
	}
	rank, err := RankBetween(prev, next)
	if err != nil {
		return Column{}, err
	}

	var c Column
	err = tx.QueryRow(ctx, `
		UPDATE board_columns SET rank = $2 WHERE id = $1
		RETURNING id, board_id, rank, name, is_terminal`,
		columnID, rank).Scan(&c.ID, &c.BoardID, &c.Rank, &c.Name, &c.IsTerminal)
	if err != nil {
		return Column{}, err
	}
	return c, tx.Commit(ctx)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
