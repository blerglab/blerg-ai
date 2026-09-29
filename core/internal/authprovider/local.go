package authprovider

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"github.com/blerglab/blerg-ai/core/internal/db"
)

// Local is the "local" (password) auth provider: it validates credentials
// submitted via a same-origin form POST against bcrypt hashes stored in the
// accounts table.
type Local struct{ st db.Store }

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

// EnsureBootstrapAdmin seeds exactly one admin account, with a random
// password, the first time the accounts table is empty. Idempotent: a
// non-empty table is a no-op. The password is logged once with a distinct,
// greppable prefix (not written to a local file — see spec §3: this
// process's container filesystem in the k8s deployment path is ephemeral
// and won't survive a restart before an operator reads it).
func (l *Local) EnsureBootstrapAdmin(ctx context.Context) error {
	var count int
	if err := l.st.Pool().QueryRow(ctx, `SELECT count(*) FROM accounts`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	pw := make([]byte, 18)
	if _, err := rand.Read(pw); err != nil {
		return err
	}
	password := base64.RawURLEncoding.EncodeToString(pw)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	subjectBytes := make([]byte, 9)
	if _, err := rand.Read(subjectBytes); err != nil {
		return err
	}
	subject := base64.RawURLEncoding.EncodeToString(subjectBytes)
	_, err = l.st.Pool().Exec(ctx,
		`INSERT INTO accounts (provider, provider_subject, role, must_change_password, password_hash)
		 VALUES ('local', $1, 'admin', true, $2)`, subject, string(hash))
	if err != nil {
		return err
	}
	log.Printf("BLERG_BOOTSTRAP_ADMIN_PASSWORD=%s (login as provider_subject=%s; you must change this on first login)", password, subject)
	return nil
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
	// accounts.email is nullable (EnsureBootstrapAdmin's own INSERT never
	// sets it), so scan into a *string rather than Account.Email directly —
	// scanning a SQL NULL into a non-pointer Go string errors, which would
	// otherwise fail every login for an account with no email, including
	// the bootstrap admin, before the password comparison ever ran.
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
	if len(newPassword) < 12 || len(newPassword) > 72 {
		return ErrWeakPassword
	}
	var hash *string
	err := l.st.Pool().QueryRow(ctx,
		`SELECT password_hash FROM accounts WHERE id = $1 AND provider = 'local'`, accountID).Scan(&hash)
	if err != nil || hash == nil {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(oldPassword))
		return ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*hash), []byte(oldPassword)); err != nil {
		return ErrInvalidCredentials
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = l.st.Pool().Exec(ctx,
		`UPDATE accounts SET password_hash = $2, must_change_password = false WHERE id = $1`, accountID, string(newHash))
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
// rejected without touching the database. A duplicate provider_subject is reported as
// ErrSubjectExists via the ON CONFLICT DO NOTHING + RowsAffected check below, relying on the
// UNIQUE (provider, provider_subject) constraint from migration 005_identity_accounts.sql
// (verified present) rather than a separate existence check, so the create is a single atomic
// statement with no check-then-act race.
func (l *Local) CreateLocalAccount(ctx context.Context, subject, role string) (string, error) {
	if role != "admin" && role != "member" {
		return "", fmt.Errorf("role must be admin or member, got %q", role)
	}
	if subject == "" {
		return "", errors.New("subject is required")
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
