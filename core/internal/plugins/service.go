// Package plugins stores each account's "always-on plugins": the ordered list of engine plugins
// that blerg-runner installs into every new cluster session started as that account.
//
// The list is configuration, not a secret, so it is plain rows (migration 014) rather than the
// encrypted credential vault. Which engines may register plugins, and what a well-formed entry
// looks like for each, is decided by the small registry below — handlers never hardcode an
// engine name, so a second engine is one registry entry (mirroring the runner's engine and
// model-source registries).
package plugins

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// Entry is one plugin in an account's list.
type Entry = pluginspec.Entry

// Engine describes one engine that can register plugins.
type Engine struct {
	// ID is the value stored in user_plugins.engine and used in the URL.
	ID string
	// Validate checks an entry's shape (not the operator allow-list, which the Service adds).
	Validate func(Entry) error
	// MarketplaceAllowed reports whether the operator permits the entry's marketplace source.
	MarketplaceAllowed func(a pluginspec.Allowlist, source string) bool
}

// registry is the closed set of engines that may register plugins.
var registry = map[string]Engine{
	pluginspec.EngineCodex: {
		ID:                 pluginspec.EngineCodex,
		Validate:           pluginspec.ValidateCodex,
		MarketplaceAllowed: func(a pluginspec.Allowlist, s string) bool { return a.Allows(s) },
	},
	pluginspec.EngineClaude: {
		ID:                 pluginspec.EngineClaude,
		Validate:           pluginspec.ValidateClaude,
		MarketplaceAllowed: func(a pluginspec.Allowlist, s string) bool { return a.Allows(s) },
	},
}

// Lookup returns the registered engine, if any.
func Lookup(id string) (Engine, bool) {
	e, ok := registry[id]
	return e, ok
}

// EngineIDs lists the registered engines, sorted.
func EngineIDs() []string {
	out := make([]string, 0, len(registry))
	for id := range registry {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ValidationError is a problem with the submitted list; its text is safe to show to a browser.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// ErrConflict is returned when a concurrent write to the same list won the race (a primary-key
// violation on insert); the caller should reload and retry.
var ErrConflict = errors.New("plugins: list changed concurrently")

// ErrUnknownEngine is returned for an engine with no registry entry.
var ErrUnknownEngine = errors.New("plugins: unknown engine")

// ErrHumanProof is deliberately indistinguishable for absent, foreign, revoked,
// expired sessions and disabled accounts.
var ErrHumanProof = errors.New("plugins: live human proof required")

// Service reads and writes the user_plugins table under the operator's marketplace allow-list.
type Service struct {
	st    db.Store
	allow pluginspec.Allowlist
}

// NewService wires a Service; allow is BLERG_CORE_PLUGIN_MARKETPLACES, already parsed.
func NewService(st db.Store, allow pluginspec.Allowlist) *Service {
	return &Service{st: st, allow: allow}
}

// Allowlist returns the operator allow-list, for display.
func (s *Service) Allowlist() pluginspec.Allowlist { return s.allow }

// List returns accountID's plugins for engine in the user's order (never nil).
func (s *Service) List(ctx context.Context, accountID, engine string) ([]Entry, error) {
	if _, ok := Lookup(engine); !ok {
		return nil, ErrUnknownEngine
	}
	rows, err := s.st.Pool().Query(ctx,
		`SELECT marketplace_source, plugin_name FROM user_plugins
		 WHERE account_id = $1 AND engine = $2 ORDER BY position`, accountID, engine)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Marketplace, &e.Plugin); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Validate applies every write-time rule to a whole list: cap, shape, allow-list, duplicates.
// Nothing is stored unless the entire list passes.
func (s *Service) Validate(engine string, entries []Entry) error {
	eng, ok := Lookup(engine)
	if !ok {
		return ErrUnknownEngine
	}
	if len(entries) > pluginspec.MaxEntries {
		return &ValidationError{fmt.Sprintf("at most %d plugins", pluginspec.MaxEntries)}
	}
	seen := map[Entry]bool{}
	for _, e := range entries {
		if err := eng.Validate(e); err != nil {
			return &ValidationError{err.Error()}
		}
		if !eng.MarketplaceAllowed(s.allow, e.Marketplace) {
			return &ValidationError{"marketplace not allowed by this install: " + e.Marketplace}
		}
		key := Entry{Marketplace: strings.ToLower(e.Marketplace), Plugin: e.Plugin}
		if seen[key] {
			return &ValidationError{"duplicate plugin: " + e.Plugin + " from " + e.Marketplace}
		}
		seen[key] = true
	}
	return nil
}

// Replace validates entries and atomically makes them accountID's whole list for engine.
func (s *Service) Replace(ctx context.Context, accountID, engine string, entries []Entry) error {
	if err := s.Validate(engine, entries); err != nil {
		return err
	}
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockList(ctx, tx, accountID, engine); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_plugins WHERE account_id = $1 AND engine = $2`, accountID, engine); err != nil {
		return err
	}
	for i, e := range entries {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_plugins (account_id, engine, position, marketplace_source, plugin_name)
			 VALUES ($1, $2, $3, $4, $5)`, accountID, engine, i, e.Marketplace, e.Plugin); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrConflict
			}
			return err
		}
	}
	return tx.Commit(ctx)
}

func lockList(ctx context.Context, tx pgx.Tx, accountID, engine string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "user_plugins:"+accountID+":"+engine)
	return err
}

// AddApproved appends exactly one entry without replacing another request's
// settings. It shares Replace's fence and rechecks the exact browser session
// inside the transaction, after any wait for that fence. The row locks preserve
// the approval's liveness through commit without calling identity through a
// second pool connection. This predicate matches HumanSessionLive; unlike its
// read probe it uses clock_timestamp so lock waits cannot freeze expiry time.
func (s *Service) AddApproved(ctx context.Context, accountID, sessionID, engine string, entry Entry) error {
	if err := s.Validate(engine, []Entry{entry}); err != nil {
		return err
	}
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockList(ctx, tx, accountID, engine); err != nil {
		return err
	}
	var proof, role string
	err = tx.QueryRow(ctx, `SELECT hs.id::text,a.role FROM human_sessions hs JOIN accounts a ON a.id=hs.account_id
		WHERE hs.chain_id=$1 AND hs.account_id=$2 AND hs.revoked_at IS NULL
		AND hs.expires_at>clock_timestamp() AND a.disabled_at IS NULL AND NOT a.must_change_password
		ORDER BY hs.created_at DESC LIMIT 1 FOR SHARE OF hs,a`, sessionID, accountID).Scan(&proof, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrHumanProof
	}
	if err != nil {
		return err
	}
	// Settings PUT requires card.read from the account's current role. Internal
	// approval cannot grant a capability that the same person's Settings lacks.
	if !slices.Contains(identity.PlatformRoleCaps[role], "card.read") {
		return ErrHumanProof
	}
	commitApproved := func() error {
		// Row locks prevent revocation/disable/role changes, but wall time
		// keeps advancing while those locks are acquired and rows are written.
		var live bool
		if err := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() AND revoked_at IS NULL FROM human_sessions WHERE id=$1`, proof).Scan(&live); err != nil {
			return err
		}
		if !live {
			return ErrHumanProof
		}
		return tx.Commit(ctx)
	}
	rows, err := tx.Query(ctx, `SELECT marketplace_source,plugin_name FROM user_plugins WHERE account_id=$1 AND engine=$2 ORDER BY position`, accountID, engine)
	if err != nil {
		return err
	}
	var entries []Entry
	for rows.Next() {
		var existing Entry
		if err := rows.Scan(&existing.Marketplace, &existing.Plugin); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, existing)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, existing := range entries {
		if strings.EqualFold(existing.Marketplace, entry.Marketplace) && existing.Plugin == entry.Plugin {
			return commitApproved()
		}
	}
	if len(entries) >= pluginspec.MaxEntries {
		return &ValidationError{fmt.Sprintf("at most %d plugins", pluginspec.MaxEntries)}
	}
	_, err = tx.Exec(ctx, `INSERT INTO user_plugins(account_id,engine,position,marketplace_source,plugin_name) VALUES($1,$2,$3,$4,$5)`, accountID, engine, len(entries), entry.Marketplace, entry.Plugin)
	if err != nil {
		return err
	}
	return commitApproved()
}
