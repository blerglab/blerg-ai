// Package mcpconn implements a person's MCP connections (docs/design/ai-crons.md section 4): remote
// MCP servers with an optional static credential, stored per account. A static secret is encrypted
// with the same keybackend the credential vault uses (see core/internal/credentials) and is only
// ever decrypted by FetchSecret, which writes an audit row FIRST and fails if it cannot. Nothing
// else in this package, and nothing that marshals a Connection, ever sees or returns a secret.
// OAuth connections (section 4.3) keep their token bundle in the same column; oauth.go holds the
// exchange, oauth_tokens.go the refresh and revocation.
package mcpconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
)

const (
	// MaxPerAccount is the number of connections one account may hold.
	MaxPerAccount = 20
	// AccessLogRetention is how long mcp_secret_access_log rows are kept.
	AccessLogRetention = 180 * 24 * time.Hour

	maxURLLen        = 2048
	maxHeaderNameLen = 64
	maxSecretLen     = 4 << 10
	maxDefaultTools  = 500
	maxToolNameLen   = 200
	maxToolHashLen   = 128

	defaultHeaderName = "Authorization"
)

var (
	// ErrNotFound is returned for an unknown connection id, another account's connection, and an
	// id that is not even a uuid: the API never distinguishes them.
	ErrNotFound = errors.New("mcpconn: not found")
	// ErrLimit means the account already holds MaxPerAccount connections.
	ErrLimit = errors.New("mcpconn: connection limit reached")
	// ErrNameTaken means another connection of the account uses the name (after normalisation).
	ErrNameTaken = errors.New("mcpconn: name already in use")
)

// ValidationError is a user-correctable problem with the input. Msg never contains a secret.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ValidationError{Msg: fmt.Sprintf(format, a...)} }

var (
	slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// toolPrefixUnsafe mirrors runner/internal/daemon/capabilities.go mcpNameUnsafe: Claude Code
	// turns every character outside [A-Za-z0-9_-] in a server name into "_" when it builds the
	// mcp__<server>__<tool> prefix.
	toolPrefixUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]`)
)

var reservedNames = map[string]bool{"board": true, "blerg": true, "gateway": true}

// forbiddenHeaders are hop-by-hop, framing or protocol headers a static credential must not set.
var forbiddenHeaders = map[string]bool{
	"host": true, "content-length": true, "transfer-encoding": true, "connection": true, "upgrade": true,
	"te": true, "trailer": true, "mcp-session-id": true, "content-type": true, "accept": true, "cookie": true,
}

// NormalizeName is the name Claude Code derives its tool prefix from: characters outside
// [A-Za-z0-9_-] become "_". Two connections whose normalised names are equal would share a tool
// namespace, so they may not coexist.
func NormalizeName(name string) string { return toolPrefixUnsafe.ReplaceAllString(name, "_") }

// Connection is the public view of a row. It deliberately has no secret field.
type Connection struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	AuthKind   string `json:"auth_kind"`
	HeaderName string `json:"header_name,omitempty"`
	HasSecret  bool   `json:"has_secret"`
	// OAuthIssuer is the authorization server of an oauth connection (not a secret).
	OAuthIssuer    string          `json:"oauth_issuer,omitempty"`
	DefaultTools   json.RawMessage `json:"default_tools"`
	Status         string          `json:"status"`
	LastVerifiedAt *time.Time      `json:"last_verified_at"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// CreateInput is what a person supplies to add a connection.
type CreateInput struct {
	Name       string
	URL        string
	AuthKind   string // "none" or "static" (oauth connections are created by StartOAuth)
	HeaderName string // static only; defaults to Authorization
	Secret     string // static only
}

// PatchInput lists the fields a PATCH may change; nil/empty means unchanged.
type PatchInput struct {
	Name         *string
	DefaultTools json.RawMessage
	Secret       *string // replaces a static secret
	HeaderName   *string // static only
}

// Proof names the live principal that authorised a secret fetch (exactly one is set); it is
// recorded in the audit row.
type Proof struct {
	SessionID string
	TokenID   string
}

// SecretResult is what the runner receives to reach the upstream server.
type SecretResult struct {
	URL        string
	HeaderName string
	Value      string
	// ExpiresAt is nil for a static secret.
	ExpiresAt *time.Time
}

// Service owns the mcp_connections lifecycle.
type Service struct {
	st      db.Store
	backend keybackend.Backend
	policy  netguard.Policy
	// httpc makes every outbound OAuth request (discovery, registration, token, revocation): the
	// netguard client, so the policy, no-redirect, no-proxy, size and time limits always apply.
	httpc   *http.Client
	limiter *startLimiter
	// flights collapses concurrent refreshes of one connection into one provider call (see
	// oauth_tokens.go); keyed by account and connection id.
	flightMu sync.Mutex
	flights  map[string]*refreshFlight
}

// NewService wires a Service against st, backend (the same envelope encryption the credential
// vault uses) and the operator's outbound URL policy.
func NewService(st db.Store, backend keybackend.Backend, policy netguard.Policy) *Service {
	return &Service{
		st: st, backend: backend, policy: policy,
		httpc:   policy.Client(oauthHTTPTimeout, oauthMaxBody),
		limiter: &startLimiter{max: defaultStartLimit, window: defaultStartWindow, hits: map[string][]time.Time{}},
		flights: map[string]*refreshFlight{},
	}
}

const cols = `id::text, name, url, auth_kind, coalesce(header_name,''), secret_ciphertext IS NOT NULL,
	coalesce(oauth_meta->>'issuer',''), default_tools, status, last_verified_at, created_at, updated_at`

func scan(row pgx.Row) (Connection, error) {
	var c Connection
	var tools []byte
	err := row.Scan(&c.ID, &c.Name, &c.URL, &c.AuthKind, &c.HeaderName, &c.HasSecret, &c.OAuthIssuer,
		&tools, &c.Status, &c.LastVerifiedAt, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return Connection{}, err
	}
	c.DefaultTools = json.RawMessage(tools)
	return c, nil
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func isFKViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23503"
}

func (s *Service) validateName(name string) error {
	if !slugRe.MatchString(name) {
		return invalid("name must be 1-32 characters: lowercase letters, digits and dashes, starting with a letter or digit")
	}
	if reservedNames[NormalizeName(name)] {
		return invalid("name %q is reserved", name)
	}
	return nil
}

func validateHeaderName(h string) error {
	if h == "" || len(h) > maxHeaderNameLen {
		return invalid("header_name must be 1-%d characters", maxHeaderNameLen)
	}
	for i := 0; i < len(h); i++ {
		if !isTchar(h[i]) {
			return invalid("header_name must be a valid HTTP header name (RFC 7230 token)")
		}
	}
	if forbiddenHeaders[strings.ToLower(h)] {
		return invalid("header_name %q may not be set on a connection", h)
	}
	return nil
}

func isTchar(c byte) bool {
	switch {
	case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

func validateSecret(v string) error {
	if v == "" {
		return invalid("secret is required for a static connection")
	}
	if len(v) > maxSecretLen {
		return invalid("secret must be at most %d bytes", maxSecretLen)
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; c == 0 || c == '\r' || c == '\n' || (c < 0x20 && c != '\t') || c == 0x7f {
			return invalid("secret must not contain CR, LF, NUL or other control characters")
		}
	}
	return nil
}

func (s *Service) validateURL(raw string) error {
	if raw == "" || len(raw) > maxURLLen {
		return invalid("url is required and must be at most %d characters", maxURLLen)
	}
	if err := s.policy.CheckURL(raw); err != nil {
		return invalid("url not permitted: %v", err)
	}
	// A secret in the query string (?key=...) would be stored in plain text, returned by the
	// API and copied into every session grant's url_snapshot: credentials go in the header field.
	if strings.Contains(raw, "?") {
		return invalid("url must not contain a query string: put a key in the secret header field instead")
	}
	if strings.Contains(raw, "#") {
		return invalid("url must not contain a fragment")
	}
	return nil
}

// validateDefaultTools checks {"tool": {"mode": "allow"|"propose", "hash": "..."}} and returns
// the canonical encoding.
func validateDefaultTools(raw json.RawMessage) ([]byte, error) {
	bad := func(msg string) ([]byte, error) { return nil, invalid("default_tools: %s", msg) }
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") {
		return bad("must be a JSON object")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return bad("must be a JSON object")
	}
	if len(m) > maxDefaultTools {
		return bad(fmt.Sprintf("at most %d tools", maxDefaultTools))
	}
	type entry struct {
		Mode string `json:"mode"`
		Hash string `json:"hash"`
	}
	out := make(map[string]entry, len(m))
	for name, v := range m {
		if name == "" || len(name) > maxToolNameLen {
			return bad("tool names must be 1-200 characters")
		}
		var e entry
		dec := json.NewDecoder(strings.NewReader(string(v)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil {
			return bad("each tool must be {mode, hash}")
		}
		if e.Mode != "allow" && e.Mode != "propose" {
			return bad(`mode must be "allow" or "propose"`)
		}
		if e.Hash == "" || len(e.Hash) > maxToolHashLen {
			return bad("hash is required")
		}
		out[name] = e
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// lockAccount serialises writers for one account so the connection limit and the name checks
// cannot be raced. The lock is released when tx ends.
func lockAccount(ctx context.Context, tx pgx.Tx, accountID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('mcpconn:' || $1::text, 0))`, accountID)
	return err
}

// nameCollides reports whether another connection (not exceptID) of the account normalises to the
// same tool prefix as name.
func nameCollides(ctx context.Context, tx pgx.Tx, accountID, name, exceptID string) (bool, error) {
	rows, err := tx.Query(ctx, `SELECT name FROM mcp_connections WHERE account_id = $1 AND ($2 = '' OR id::text <> $2)`, accountID, exceptID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	want := NormalizeName(name)
	for rows.Next() {
		var other string
		if err := rows.Scan(&other); err != nil {
			return false, err
		}
		if NormalizeName(other) == want {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Create validates and stores a new connection for accountID.
func (s *Service) Create(ctx context.Context, accountID string, in CreateInput) (Connection, error) {
	if err := s.validateName(in.Name); err != nil {
		return Connection{}, err
	}
	if err := s.validateURL(in.URL); err != nil {
		return Connection{}, err
	}
	header := in.HeaderName
	var ciphertext []byte
	var keyID *string
	switch in.AuthKind {
	case "none":
		if in.Secret != "" {
			return Connection{}, invalid("secret is only valid for a static connection")
		}
		if in.HeaderName != "" {
			return Connection{}, invalid("header_name is only valid for a static connection")
		}
	case "static":
		if header == "" {
			header = defaultHeaderName
		}
		if err := validateHeaderName(header); err != nil {
			return Connection{}, err
		}
		if err := validateSecret(in.Secret); err != nil {
			return Connection{}, err
		}
		ct, kid, err := s.backend.Encrypt(ctx, []byte(in.Secret))
		if err != nil {
			return Connection{}, err
		}
		ciphertext, keyID = ct, &kid
	case "oauth":
		return Connection{}, invalid("oauth connections are created through POST /api/mcp/connections/oauth/start, not here")
	default:
		return Connection{}, invalid(`auth_kind must be "none" or "static"`)
	}

	var hn *string
	if in.AuthKind == "static" {
		hn = &header
	}
	return s.insert(ctx, accountID, in.Name, in.URL, in.AuthKind, hn, ciphertext, keyID, nil)
}

// insert stores a validated connection under the account lock, enforcing the per-account limit
// and the name rules. meta is oauth_meta (nil for none/static).
func (s *Service) insert(ctx context.Context, accountID, name, rawURL, kind string, header *string, ciphertext []byte, keyID *string, meta []byte) (Connection, error) {
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return Connection{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockAccount(ctx, tx, accountID); err != nil {
		return Connection{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM mcp_connections WHERE account_id = $1`, accountID).Scan(&count); err != nil {
		return Connection{}, err
	}
	if count >= MaxPerAccount {
		return Connection{}, ErrLimit
	}
	if taken, err := nameCollides(ctx, tx, accountID, name, ""); err != nil {
		return Connection{}, err
	} else if taken {
		return Connection{}, ErrNameTaken
	}
	c, err := scan(tx.QueryRow(ctx,
		`INSERT INTO mcp_connections (account_id, name, url, auth_kind, header_name, secret_ciphertext, key_id, oauth_meta, last_verified_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8, CASE WHEN $4 = 'oauth' THEN now() END) RETURNING `+cols,
		accountID, name, rawURL, kind, header, ciphertext, keyID, meta))
	switch {
	case isUnique(err):
		return Connection{}, ErrNameTaken
	case isFKViolation(err):
		return Connection{}, ErrNotFound
	case err != nil:
		return Connection{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Connection{}, err
	}
	return c, nil
}

// List returns the account's connections, oldest first. Never carries a secret.
func (s *Service) List(ctx context.Context, accountID string) ([]Connection, error) {
	rows, err := s.st.Pool().Query(ctx,
		`SELECT `+cols+` FROM mcp_connections WHERE account_id = $1 ORDER BY created_at, name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns one of the account's connections, or ErrNotFound.
func (s *Service) Get(ctx context.Context, accountID, id string) (Connection, error) {
	if !uuidRe.MatchString(id) {
		return Connection{}, ErrNotFound
	}
	c, err := scan(s.st.Pool().QueryRow(ctx,
		`SELECT `+cols+` FROM mcp_connections WHERE id = $1 AND account_id = $2`, id, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	return c, err
}

// Patch applies the non-empty fields of in to one of the account's connections.
func (s *Service) Patch(ctx context.Context, accountID, id string, in PatchInput) (Connection, error) {
	if !uuidRe.MatchString(id) {
		return Connection{}, ErrNotFound
	}
	if in.Name == nil && in.DefaultTools == nil && in.Secret == nil && in.HeaderName == nil {
		return Connection{}, invalid("nothing to change")
	}
	var tools []byte
	if in.DefaultTools != nil {
		var err error
		if tools, err = validateDefaultTools(in.DefaultTools); err != nil {
			return Connection{}, err
		}
	}
	if in.Name != nil {
		if err := s.validateName(*in.Name); err != nil {
			return Connection{}, err
		}
	}

	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return Connection{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockAccount(ctx, tx, accountID); err != nil {
		return Connection{}, err
	}
	var kind string
	err = tx.QueryRow(ctx, `SELECT auth_kind FROM mcp_connections WHERE id = $1 AND account_id = $2 FOR UPDATE`, id, accountID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	if err != nil {
		return Connection{}, err
	}
	if (in.Secret != nil || in.HeaderName != nil) && kind != "static" {
		if in.Secret != nil {
			return Connection{}, invalid("secret is only valid for a static connection")
		}
		return Connection{}, invalid("header_name is only valid for a static connection")
	}
	if in.Name != nil {
		if taken, err := nameCollides(ctx, tx, accountID, *in.Name, id); err != nil {
			return Connection{}, err
		} else if taken {
			return Connection{}, ErrNameTaken
		}
		if _, err := tx.Exec(ctx, `UPDATE mcp_connections SET name = $1, updated_at = now() WHERE id = $2`, *in.Name, id); err != nil {
			if isUnique(err) {
				return Connection{}, ErrNameTaken
			}
			return Connection{}, err
		}
	}
	if tools != nil {
		if _, err := tx.Exec(ctx, `UPDATE mcp_connections SET default_tools = $1, updated_at = now() WHERE id = $2`, tools, id); err != nil {
			return Connection{}, err
		}
	}
	if in.HeaderName != nil {
		if err := validateHeaderName(*in.HeaderName); err != nil {
			return Connection{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE mcp_connections SET header_name = $1, updated_at = now() WHERE id = $2`, *in.HeaderName, id); err != nil {
			return Connection{}, err
		}
	}
	if in.Secret != nil {
		if err := validateSecret(*in.Secret); err != nil {
			return Connection{}, err
		}
		ct, kid, err := s.backend.Encrypt(ctx, []byte(*in.Secret))
		if err != nil {
			return Connection{}, err
		}
		// A new secret has not been verified against the server yet.
		if _, err := tx.Exec(ctx,
			`UPDATE mcp_connections SET secret_ciphertext = $1, key_id = $2, status = 'ok', updated_at = now() WHERE id = $3`,
			ct, kid, id); err != nil {
			return Connection{}, err
		}
	}
	c, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM mcp_connections WHERE id = $1`, id))
	if err != nil {
		return Connection{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Connection{}, err
	}
	return c, nil
}

// Delete removes one of the account's connections, or reports ErrNotFound. The row is deleted
// first, in one statement (no transaction, no lock held), and only then is an OAuth connection's
// grant revoked upstream (RFC 7009), best effort: a slow or unreachable provider never holds a
// pooled database connection and never prevents the person from removing the connection. A
// refresh that was in flight when the row vanished revokes the tokens it just obtained itself
// (see oauth_tokens.go).
func (s *Service) Delete(ctx context.Context, accountID, id string) error {
	if !uuidRe.MatchString(id) {
		return ErrNotFound
	}
	var kind string
	var ct []byte
	var keyID *string
	err := s.st.Pool().QueryRow(ctx,
		`DELETE FROM mcp_connections WHERE id = $1 AND account_id = $2 RETURNING auth_kind, secret_ciphertext, key_id`,
		id, accountID).Scan(&kind, &ct, &keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if kind == "oauth" {
		if b, err := s.openBundle(ctx, ct, keyID); err != nil {
			log.Printf("mcpconn: delete %s: token bundle unreadable, nothing to revoke upstream", id)
		} else {
			s.revokeUpstream(ctx, b)
		}
	}
	return nil
}

// FetchSecret hands the runner what it needs to call the upstream server. It is the ONLY place a
// secret is decrypted. Callers must have verified the liveness proof first; FetchSecret does no
// authorization beyond scoping the lookup to accountID. The audit row is written BEFORE the
// decryption and a failure to write it aborts with no secret returned.
func (s *Service) FetchSecret(ctx context.Context, accountID, id string, proof Proof) (SecretResult, error) {
	if !uuidRe.MatchString(id) {
		return SecretResult{}, ErrNotFound
	}
	var url, kind, status string
	var header *string
	var ct []byte
	var keyID *string
	err := s.st.Pool().QueryRow(ctx,
		`SELECT url, auth_kind, status, header_name, secret_ciphertext, key_id FROM mcp_connections WHERE id = $1 AND account_id = $2`,
		id, accountID).Scan(&url, &kind, &status, &header, &ct, &keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SecretResult{}, ErrNotFound
	}
	if err != nil {
		return SecretResult{}, err
	}
	if kind == "oauth" && status != "ok" {
		// needs_auth or error: there is nothing usable to hand over, and the caller (the runner
		// refuses connections whose status is not ok) treats this uniform 404 as "connection
		// unavailable, sign in again". No audit row: no secret is touched.
		return SecretResult{}, ErrNotFound
	}

	if _, err := s.st.Pool().Exec(ctx,
		`INSERT INTO mcp_secret_access_log (account_id, connection_id, fetched_by_session_id, fetched_by_token_id, fetched_at)
		 VALUES ($1, $2, $3, $4, now())`,
		accountID, id, nullable(proof.SessionID), nullable(proof.TokenID)); err != nil {
		return SecretResult{}, fmt.Errorf("mcpconn: audit log write: %w", err)
	}

	if kind == "oauth" {
		// Decrypted (and, inside the refresh window, refreshed) under the connection's row lock.
		return s.oauthAccessToken(ctx, accountID, id)
	}
	res := SecretResult{URL: url}
	if kind == "static" {
		if keyID == nil || len(ct) == 0 {
			return SecretResult{}, errors.New("mcpconn: static connection has no secret")
		}
		plain, err := s.backend.Decrypt(ctx, ct, *keyID)
		if err != nil {
			return SecretResult{}, err
		}
		res.Value = string(plain)
		if header != nil {
			res.HeaderName = *header
		}
	}
	return res, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// PruneAccessLog deletes access-log rows older than maxAge and returns how many.
func (s *Service) PruneAccessLog(ctx context.Context, maxAge time.Duration) (int64, error) {
	tag, err := s.st.Pool().Exec(ctx,
		`DELETE FROM mcp_secret_access_log WHERE fetched_at < now() - make_interval(secs => $1)`, maxAge.Seconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CountQueryURLs counts connections whose stored URL carries a query string or a fragment. New
// connections cannot (validateURL refuses both), but rows from before that rule may hold a key in
// the query. The API redacts them in responses; this count backs a startup warning so the
// operator knows to move the key into the secret header field.
func (s *Service) CountQueryURLs(ctx context.Context) (int, error) {
	var n int
	err := s.st.Pool().QueryRow(ctx, `SELECT count(*) FROM mcp_connections WHERE url LIKE '%?%' OR url LIKE '%#%'`).Scan(&n)
	return n, err
}

// StartPruneLoop prunes the access log once now and then every interval (a day in production)
// until ctx ends. Idempotent, so several core replicas may run it. On start it also warns, once,
// about connections whose URL holds a query string (see CountQueryURLs).
func StartPruneLoop(ctx context.Context, s *Service, interval time.Duration) {
	if n, err := s.CountQueryURLs(ctx); err != nil {
		log.Printf("mcpconn: counting connections with a query string in the URL: %v", err)
	} else if n > 0 {
		log.Printf("mcpconn: WARNING: %d MCP connection(s) have a query string or fragment in their URL, which may hold a secret; the API hides it, but recreate them with the key in the secret header field", n)
	}
	run := func() {
		if n, err := s.PruneAccessLog(ctx, AccessLogRetention); err != nil {
			log.Printf("mcpconn: prune access log: %v", err)
		} else if n > 0 {
			log.Printf("mcpconn: pruned %d access log rows", n)
		}
		if n, err := s.PruneOAuthState(ctx); err != nil {
			log.Printf("mcpconn: prune oauth state: %v", err)
		} else if n > 0 {
			log.Printf("mcpconn: pruned %d expired oauth state rows", n)
		}
	}
	go func() {
		run()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				run()
			}
		}
	}()
}
