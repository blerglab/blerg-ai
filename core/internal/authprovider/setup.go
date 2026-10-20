package authprovider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// First-run setup. While the accounts table is empty, core is in setup mode: the person
// creates the first admin by choosing a username and password on /setup. Setup closes for good
// once any account exists; nothing here ever creates a second account.
//
// /setup needs a single-use setup code, unless the request is trusted as local (the desktop
// install, which publishes core on loopback only — the API layer decides that). The code is
// minted only by `blerg-core setup-link` (never at boot, so it never reaches a log); only its
// SHA-256 is stored, with an expiry, and a new code replaces the previous one.

// SetupCodeTTL is how long a setup code stays valid.
const SetupCodeTTL = 24 * time.Hour

// ErrSetupComplete: an account already exists, so setup is closed.
var ErrSetupComplete = errors.New("setup is already complete")

// ErrSetupCode: the setup code is missing, wrong, expired, superseded or already used. One error
// for all of them, so a caller learns nothing about which.
var ErrSetupCode = errors.New("this setup link is not valid: it may be wrong, expired or already used")

// SetupLockTimeout bounds how long the first-account transaction waits for the accounts table
// lock (and, doubled, how long any of its statements may run), so requests cannot pile up behind
// a long writer holding pool connections. A variable so tests can shorten it.
var SetupLockTimeout = 5 * time.Second

// SetupOpen reports whether core is in setup mode: no account exists yet. Once it has seen an
// account it answers false without asking the database again (see Local.setupClosed). The query
// takes no lock, so it never waits behind a writer.
func (l *Local) SetupOpen(ctx context.Context) (bool, error) {
	if l.setupClosed.Load() {
		return false, nil
	}
	var exists bool
	if err := l.st.Pool().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts)`).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		l.setupClosed.Store(true)
	}
	return !exists, nil
}

// MintSetupCode creates a fresh setup code valid for SetupCodeTTL and returns it; any older code
// stops working. It is ErrSetupComplete once an account exists. The code (32 random bytes) is
// returned once and never stored.
func (l *Local) MintSetupCode(ctx context.Context) (string, error) {
	open, err := l.SetupOpen(ctx)
	if err != nil {
		return "", err
	}
	if !open {
		return "", ErrSetupComplete
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	code := base64.RawURLEncoding.EncodeToString(b)
	if _, err := l.st.Pool().Exec(ctx,
		`INSERT INTO setup_code (id, code_hash, expires_at) VALUES (true, $1, now() + $2 * interval '1 second')
		 ON CONFLICT (id) DO UPDATE SET code_hash = EXCLUDED.code_hash, expires_at = EXCLUDED.expires_at, created_at = now()`,
		hashSetupCode(code), int64(SetupCodeTTL/time.Second)); err != nil {
		return "", err
	}
	return code, nil
}

func hashSetupCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// FirstAdmin is what /setup submits.
type FirstAdmin struct {
	Username string
	Password string
	// Code is the setup code from the link. Ignored when Local is set.
	Code string
	// Local means the request needs no code: the caller decided it is trusted as local (the
	// desktop install, see api.Deps.SetupAllowLocal).
	Local bool
}

// CreateFirstAdmin creates the first account, an admin with the chosen username and password and
// no forced password change, and consumes the setup code. Exactly one call can ever succeed: the
// check that no account exists and the insert run in one transaction that holds a lock on the
// accounts table which conflicts with itself and with any other insert, so two concurrent calls
// are serialized and the second finds the first's account (ErrSetupComplete).
//
// Errors: ErrSetupComplete (checked first, without bcrypt or a lock), ErrInvalidUsername,
// ErrWeakPassword, ErrSetupCode (pre-checked without a lock, re-checked under it), or a
// lock_timeout error when the accounts lock is not granted within SetupLockTimeout.
func (l *Local) CreateFirstAdmin(ctx context.Context, in FirstAdmin) (Account, error) {
	// Closed is answered first and cheaply: no bcrypt, no lock, so a set-up install's /auth/setup
	// costs one indexed read at most (none once latched).
	open, err := l.SetupOpen(ctx)
	if err != nil {
		return Account{}, err
	}
	if !open {
		return Account{}, ErrSetupComplete
	}
	if err := ValidateUsername(in.Username); err != nil {
		return Account{}, err
	}
	if len(in.Password) < 12 || len(in.Password) > 72 {
		return Account{}, ErrWeakPassword
	}
	if !in.Local {
		// A wrong code is refused before bcrypt too; the check is repeated under the lock below.
		if in.Code == "" {
			return Account{}, ErrSetupCode
		}
		if err := checkSetupCode(ctx, l.st.Pool(), in.Code, false); err != nil {
			return Account{}, err
		}
	}
	// bcrypt before the transaction, so the table lock is held for milliseconds, not the hash.
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return Account{}, err
	}
	tx, err := l.st.Pool().Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL lock_timeout = %d; SET LOCAL statement_timeout = %d`,
		SetupLockTimeout.Milliseconds(), 2*SetupLockTimeout.Milliseconds())); err != nil {
		return Account{}, err
	}
	if err := lockAccountsForFirstAccount(ctx, tx); err != nil {
		if errors.Is(err, ErrSetupComplete) {
			l.setupClosed.Store(true)
		}
		return Account{}, err
	}
	if !in.Local {
		if err := checkSetupCode(ctx, tx, in.Code, true); err != nil {
			return Account{}, err
		}
	}
	acc := Account{Provider: "local", ProviderSubject: in.Username}
	if err := tx.QueryRow(ctx,
		`INSERT INTO accounts (provider, provider_subject, role, must_change_password, password_hash)
		 VALUES ('local', $1, 'admin', false, $2) RETURNING id::text`, in.Username, string(hash)).Scan(&acc.ID); err != nil {
		return Account{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM setup_code`); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, err
	}
	l.setupClosed.Store(true)
	return acc, nil
}

// lockAccountsForFirstAccount takes SHARE ROW EXCLUSIVE on accounts (it conflicts with itself and
// with the ROW EXCLUSIVE every insert takes, so no account can appear until tx ends) and then
// requires the table to be empty.
func lockAccountsForFirstAccount(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `LOCK TABLE accounts IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts)`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrSetupComplete
	}
	return nil
}

// querier is what checkSetupCode needs: a transaction (locked re-check) or the pool (pre-check).
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// checkSetupCode compares code with the live one; under the row lock when lock is set.
func checkSetupCode(ctx context.Context, q querier, code string, lock bool) error {
	sql := `SELECT code_hash, expires_at > now() FROM setup_code WHERE id`
	if lock {
		sql += ` FOR UPDATE`
	}
	var stored string
	var live bool
	err := q.QueryRow(ctx, sql).Scan(&stored, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSetupCode
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(hashSetupCode(code)), []byte(stored)) != 1 || !live {
		return ErrSetupCode
	}
	return nil
}
