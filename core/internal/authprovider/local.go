package authprovider

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"github.com/blerglab/blerg-ai/core/internal/db"
)

// Local is the "local" (password) auth provider: it validates credentials
// submitted via a same-origin form POST against bcrypt hashes stored in the
// accounts table.
type Local struct {
	st db.Store
	// setupClosed latches once an account is seen to exist: nothing in core deletes accounts, so
	// first-run setup can never reopen while the process runs (setup.go).
	setupClosed atomic.Bool
}

// NewLocal constructs a Local provider backed by st.
func NewLocal(st db.Store) *Local { return &Local{st: st} }

// ID identifies this provider as "local".
func (*Local) ID() string { return "local" }

// LoginURL is the same-origin form POST target the login page submits to.
func (*Local) LoginURL(state string) string { return "/auth/login" }

// ErrInvalidCredentials is returned by Callback when the submitted
// provider_subject/password pair does not match a stored account.
var ErrInvalidCredentials = errors.New("authprovider: invalid credentials")

// ErrWeakPassword is returned by ChangePassword for a new password under 12 characters.
var ErrWeakPassword = errors.New("authprovider: password must be at least 12 characters")

// ErrSubjectExists is returned by CreateLocalAccount when provider_subject already has a
// "local" account — accounts(provider, provider_subject) has a UNIQUE constraint (migration
// 005_identity_accounts.sql) that CreateLocalAccount's ON CONFLICT targets directly.
var ErrSubjectExists = errors.New("authprovider: provider_subject already exists")

// dummyHash is compared against on a lookup miss so that Callback takes
// roughly the same time whether or not the account exists (no username
// enumeration via timing). It MUST be a real, well-formed bcrypt hash (60
// bytes: "$2a$<cost>$" + 22-byte salt + 31-byte digest) — bcrypt's
// CompareHashAndPassword rejects anything shorter than 59 bytes with
// ErrHashTooShort *before* running the expensive key-setup work, which
// would silently defeat the timing-parity defense this exists for. Computed
// once at init (rather than hardcoded) so a hand-edited literal can't
// regress below that length unnoticed.
var dummyHash = mustGenerateDummyHash()

func mustGenerateDummyHash() string {
	h, err := bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing-parity"), bcrypt.DefaultCost)
	if err != nil {
		panic(fmt.Sprintf("authprovider: failed to generate dummy bcrypt hash: %v", err))
	}
	if len(h) < 59 {
		panic(fmt.Sprintf("authprovider: generated dummy hash is too short (%d bytes) to defeat bcrypt's fast-reject path", len(h)))
	}
	return string(h)
}

// Callback checks a submitted provider_subject/password form against the
// bcrypt hash stored for that account. It fails closed: any error (missing
// account, malformed form, wrong password) is reported as
// ErrInvalidCredentials-wrapping failure, never a silent allow.
func (l *Local) Callback(ctx context.Context, r *http.Request) (Account, error) {
	// Cap the form body BEFORE parsing (audit M-4): ParseForm buffers the whole request body
	// into memory. A nil ResponseWriter is fine here — MaxBytesReader only calls w.Header() when
	// the limit is actually exceeded and w is non-nil (see its doc comment); r.Body simply
	// returns an error at that point either way, which ParseForm surfaces to the caller below.
	r.Body = http.MaxBytesReader(nil, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		return Account{}, err
	}
	subject := r.FormValue("provider_subject")
	password := r.FormValue("password")

	var acc Account
	// accounts.email is nullable (the local provider's own INSERTs never
	// set it), so scan into a *string rather than Account.Email directly —
	// scanning a SQL NULL into a non-pointer Go string errors, which would
	// otherwise fail every login for an account with no email, including
	// the first admin, before the password comparison ever ran.
	var email *string
	var hash string
	err := l.st.Pool().QueryRow(ctx,
		`SELECT id::text, email, password_hash FROM accounts WHERE provider='local' AND provider_subject=$1`,
		subject).Scan(&acc.ID, &email, &hash)
	if err != nil {
		// Constant-time-shape dummy compare: no valid account to check
		// against, but still pay bcrypt's cost so lookup misses and wrong
		// passwords aren't distinguishable by timing.
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(password))
		return Account{}, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return Account{}, ErrInvalidCredentials
	}
	if email != nil {
		acc.Email = *email
	}
	acc.Provider, acc.ProviderSubject = "local", subject
	return acc, nil
}

// ChangePassword verifies oldPassword against accountID's stored bcrypt hash, then — in one
// statement — stores newPassword's bcrypt hash and clears must_change_password. A wrong old
// password, or an accountID with no local password_hash at all (a github/oidc account, which
// has nothing for this endpoint to change), returns ErrInvalidCredentials, paying the same
// dummy-hash bcrypt cost as a lookup miss so the two cases aren't distinguishable by timing —
// same convention Callback uses.
//
// newPassword's length is checked in BYTES (len() on a Go string is already a byte count, not a
// rune count): under 12 is ErrWeakPassword; over 72 is ALSO ErrWeakPassword, because bcrypt
// silently truncates/rejects anything past its own 72-byte input limit
// (bcrypt.GenerateFromPassword returns ErrPasswordTooLong, which would otherwise surface as an
// internal 500 here) — a multi-byte-heavy 72-character password can already exceed 72 bytes, so
// bytes (not runes/characters) is the correct unit to bound.
func (l *Local) ChangePassword(ctx context.Context, accountID, oldPassword, newPassword string) error {
	_, err := l.ChangePasswordAndSubject(ctx, accountID, oldPassword, newPassword, "")
	return err
}

// ChangePasswordAndSubject is ChangePassword plus an optional rename: when newSubject is
// non-empty and differs from the account's current provider_subject, the account is renamed in
// the SAME transaction as the password change, so a taken or invalid username leaves the
// password untouched too. It returns the account's provider_subject after the change. A new name
// is validated after the row is read (echoing the current name is a no-op even if it predates
// the rules) and before any bcrypt work; a rename asked of a non-local account is
// ErrUsernameFromProvider (checked before the password, since such an account has no password
// here to check). The password is checked, and the new hash computed, before the row lock is
// taken (see lockUnchanged).
func (l *Local) ChangePasswordAndSubject(ctx context.Context, accountID, oldPassword, newPassword, newSubject string) (string, error) {
	return l.ChangePasswordAndSubjectWith(ctx, accountID, oldPassword, newPassword, newSubject, nil)
}

// ChangePasswordAndSubjectWith is ChangePasswordAndSubject with first, when non-nil, run as the
// first statement of the change's transaction, before the account row is locked. Whatever first
// writes commits with the new password or not at all: the password-change endpoint passes the
// identity service's RevokeAccountEverywhereTx, so a new password never stands while the old
// sessions and tokens still work, and a failed revocation leaves the old password in place for a
// retry. An error from first is returned as is (after the transaction is rolled back).
func (l *Local) ChangePasswordAndSubjectWith(ctx context.Context, accountID, oldPassword, newPassword, newSubject string,
	first func(ctx context.Context, tx pgx.Tx) error) (string, error) {
	if len(newPassword) < 12 || len(newPassword) > 72 {
		return "", ErrWeakPassword
	}
	acc, err := l.readAccount(ctx, accountID)
	if err != nil {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(oldPassword))
		return "", ErrInvalidCredentials
	}
	// Echoing the current name (even a legacy one the rules would refuse) is a no-op, so the
	// rules apply only to a name that is actually new.
	rename := newSubject != "" && newSubject != acc.subject
	if rename {
		if err := ValidateUsername(newSubject); err != nil {
			return "", err
		}
		if acc.provider != "local" {
			return "", ErrUsernameFromProvider
		}
	}
	if err := acc.checkPassword(oldPassword); err != nil {
		return "", err
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	tx, err := l.st.Pool().Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if first != nil {
		if err := first(ctx, tx); err != nil {
			return "", err
		}
	}
	if err := lockUnchanged(ctx, tx, accountID, acc); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE accounts SET password_hash = $2, must_change_password = false WHERE id = $1`, accountID, string(newHash)); err != nil {
		return "", err
	}
	subject := acc.subject
	if rename {
		if err := renameLocal(ctx, tx, accountID, newSubject); err != nil {
			return "", err
		}
		subject = newSubject
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return subject, nil
}

// ErrInvalidUsername is ValidateUsername's refusal. Its text is shown to the person as is.
var ErrInvalidUsername = errors.New("use lowercase letters, digits, dots, dashes and underscores (3–64 characters)")

// ErrUsernameTaken is a rename onto a provider_subject another local account already has
// (accounts' UNIQUE (provider, provider_subject) is the check). Its text is shown as is.
var ErrUsernameTaken = errors.New("that username is taken")

// ErrUsernameFromProvider is a rename asked of a github/oidc account, whose provider_subject is
// the identity provider's own id for the person and is not ours to change. Shown as is.
var ErrUsernameFromProvider = errors.New("username comes from your sign-in provider")

// ValidateUsername is the rule for a username a person chooses: 3 to 64 characters of
// lowercase letters, digits, '.', '_' and '-', starting with a letter or digit. Uppercase is
// refused rather than folded, so what is stored is exactly what was typed. It applies to a new
// name only — a self-chosen one, or one an operator passes to `users create` — not to a name
// already stored (one created before the rule existed, such as an old random bootstrap name).
func ValidateUsername(s string) error {
	if len(s) < 3 || len(s) > 64 {
		return ErrInvalidUsername
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '.' || c == '_' || c == '-') && i > 0:
		default:
			return ErrInvalidUsername
		}
	}
	return nil
}

// ChangeUsername renames a local account behind its current password (POST /auth/username).
// The new name must pass ValidateUsername (ErrInvalidUsername) and not belong to another local
// account (ErrUsernameTaken); a github/oidc account is ErrUsernameFromProvider; a wrong password
// or an unknown accountID is ErrInvalidCredentials, with the same dummy-hash timing as
// ChangePassword. Echoing the current name is a no-op (even a legacy name the rules would
// refuse). Sessions and tokens are unaffected — they name the account id, not the username.
// Returns the account's provider_subject afterwards. As in ChangePasswordAndSubject, the
// password is checked before the row lock is taken.
func (l *Local) ChangeUsername(ctx context.Context, accountID, password, newSubject string) (string, error) {
	acc, err := l.readAccount(ctx, accountID)
	if err != nil {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(password))
		return "", ErrInvalidCredentials
	}
	// The rules and the provider check apply only to a name that is actually new.
	rename := newSubject != acc.subject
	if rename {
		if err := ValidateUsername(newSubject); err != nil {
			return "", err
		}
		if acc.provider != "local" {
			return "", ErrUsernameFromProvider
		}
	}
	if err := acc.checkPassword(password); err != nil {
		return "", err
	}
	if !rename {
		return newSubject, nil
	}
	tx, err := l.st.Pool().Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockUnchanged(ctx, tx, accountID, acc); err != nil {
		return "", err
	}
	if err := renameLocal(ctx, tx, accountID, newSubject); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return newSubject, nil
}

// accountRow is the row a password or username change works on.
type accountRow struct {
	provider, subject string
	hash              *string
}

// readAccount reads the row with a plain SELECT, outside any transaction, so the bcrypt check
// that follows holds neither a row lock nor a pooled connection.
func (l *Local) readAccount(ctx context.Context, accountID string) (accountRow, error) {
	var a accountRow
	err := l.st.Pool().QueryRow(ctx,
		`SELECT provider, provider_subject, password_hash FROM accounts WHERE id = $1`, accountID).
		Scan(&a.provider, &a.subject, &a.hash)
	return a, err
}

// lockUnchanged takes the row lock (FOR UPDATE) inside tx and checks the row is still the one
// the password was checked against. If the hash, name or provider changed in between (a
// concurrent password change, reset or rename), or the row is gone, it is ErrInvalidCredentials:
// the password that was checked may no longer be the account's. Any other error is returned.
func lockUnchanged(ctx context.Context, tx pgx.Tx, accountID string, checked accountRow) error {
	var now accountRow
	if err := tx.QueryRow(ctx,
		`SELECT provider, provider_subject, password_hash FROM accounts WHERE id = $1 FOR UPDATE`, accountID).
		Scan(&now.provider, &now.subject, &now.hash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidCredentials
		}
		return err
	}
	if now.provider != checked.provider || now.subject != checked.subject ||
		(now.hash == nil) != (checked.hash == nil) ||
		(now.hash != nil && *now.hash != *checked.hash) {
		return ErrInvalidCredentials
	}
	return nil
}

// checkPassword compares password with the account's bcrypt hash. An account with no local
// hash (github/oidc) pays the dummy-hash cost and fails, like a wrong password.
func (a accountRow) checkPassword(password string) error {
	if a.provider != "local" || a.hash == nil {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(password))
		return ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(*a.hash), []byte(password)) != nil {
		return ErrInvalidCredentials
	}
	return nil
}

// renameLocal is the rename statement itself, run inside the caller's transaction. The
// UNIQUE (provider, provider_subject) constraint is the uniqueness check: its violation is
// ErrUsernameTaken, so two people racing for one name cannot both get it.
func renameLocal(ctx context.Context, tx pgx.Tx, accountID, newSubject string) error {
	_, err := tx.Exec(ctx,
		`UPDATE accounts SET provider_subject = $2 WHERE id = $1 AND provider = 'local'`, accountID, newSubject)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrUsernameTaken
	}
	return err
}

// oneTimePassword generates a URL-safe random password (18 bytes -> 24 base64 chars, well over
// the CLI test's 20-char floor) suitable for printing once to an operator's terminal.
func oneTimePassword() (string, error) {
	pw := make([]byte, 18)
	if _, err := rand.Read(pw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(pw), nil
}

// CreateLocalAccount inserts a new "local" account with a fresh one-time random password and
// must_change_password=true, returning that password (the ONLY time it is ever available in
// plaintext — it is never stored anywhere but this return value, and the CLI caller prints it
// once to stdout and discards it). role must be "admin" or "member"; any other value is
// rejected without touching the database, and so is a subject that fails ValidateUsername
// (ErrInvalidUsername), so operator-created names follow the same rules as self-chosen ones
// (a name stored before the rules existed is not re-checked). A duplicate provider_subject is reported as
// ErrSubjectExists via the ON CONFLICT DO NOTHING + RowsAffected check below, relying on the
// UNIQUE (provider, provider_subject) constraint from migration 005_identity_accounts.sql
// (verified present) rather than a separate existence check, so the create is a single atomic
// statement with no check-then-act race.
func (l *Local) CreateLocalAccount(ctx context.Context, subject, role string) (string, error) {
	if role != "admin" && role != "member" {
		return "", fmt.Errorf("role must be admin or member, got %q", role)
	}
	if err := ValidateUsername(subject); err != nil {
		return "", err
	}
	pw, err := oneTimePassword()
	if err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	tag, err := l.st.Pool().Exec(ctx,
		`INSERT INTO accounts (provider, provider_subject, role, must_change_password, password_hash)
		 VALUES ('local', $1, $2, true, $3)
		 ON CONFLICT (provider, provider_subject) DO NOTHING`, subject, role, string(hash))
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrSubjectExists
	}
	return pw, nil
}

// ResetPassword replaces subject's password with a fresh one-time random one, sets
// must_change_password=true, and — via revoke — invalidates every existing session/access token
// for that account so a leaked or forgotten old password can't keep working after the reset.
// ErrInvalidCredentials is returned when subject has no local account (mirrors Callback's
// fail-closed convention: an unknown-subject reset looks the same to the caller as any other
// invalid-credentials failure, rather than a distinct "not found" that could be used to enumerate
// accounts).
func (l *Local) ResetPassword(ctx context.Context, subject string, revoke func(context.Context, string) error) (string, error) {
	pw, err := oneTimePassword()
	if err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	var id string
	err = l.st.Pool().QueryRow(ctx,
		`UPDATE accounts SET password_hash = $2, must_change_password = true
		  WHERE provider = 'local' AND provider_subject = $1 RETURNING id::text`, subject, string(hash)).Scan(&id)
	if err != nil {
		return "", ErrInvalidCredentials
	}
	if revoke != nil {
		if err := revoke(ctx, id); err != nil {
			return "", err
		}
	}
	return pw, nil
}
