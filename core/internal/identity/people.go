package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/authprovider"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

var ErrPerson = errors.New("person action refused")
var ErrPersonInput = errors.New("enter a name and a valid email address")
var ErrPersonSelf = errors.New("you cannot disable your own account")

type Person struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Disabled bool   `json:"disabled"`
	Pending  bool   `json:"pending"`
}

func (s *Service) People(ctx context.Context) ([]Person, error) {
	rows, err := s.st.Pool().Query(ctx, `SELECT a.id::text,a.display_name,coalesce(a.email,''),a.provider_subject,a.role,a.disabled_at IS NOT NULL,EXISTS(SELECT 1 FROM person_invites i WHERE i.account_id=a.id AND i.used_at IS NULL AND i.expires_at>now()) FROM accounts a ORDER BY a.created_at,a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Name, &p.Email, &p.Username, &p.Role, &p.Disabled, &p.Pending); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// The actor's session fence keeps a concurrent disable or logout from authorizing a write.
func (s *Service) peopleAdmin(ctx context.Context, tx pgx.Tx, actor, sid string) error {
	if err := s.lockAccountSessions(ctx, tx, actor); err != nil {
		return err
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts a JOIN human_sessions h ON h.account_id=a.id WHERE a.id=$1 AND a.role='admin' AND a.disabled_at IS NULL AND NOT a.must_change_password AND h.chain_id::text=$2 AND h.revoked_at IS NULL AND h.expires_at>now())`, actor, sid).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrPerson
	}
	return nil
}

// InvitePerson returns a single-use secret once; only its hash is persisted.
func (s *Service) InvitePerson(ctx context.Context, actor, sid, name, email string) (string, string, error) {
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	parsed, err := mail.ParseAddress(email)
	if name == "" || len(name) > 200 || len(email) > 254 || err != nil || parsed.Address != email {
		return "", "", ErrPersonInput
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", "", err
	}
	code := base64.RawURLEncoding.EncodeToString(seed)
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.peopleAdmin(ctx, tx, actor, sid); err != nil {
		return "", "", err
	}
	pendingID, err := NewDesktopID()
	if err != nil {
		return "", "", err
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO accounts(provider,provider_subject,display_name,email,role,must_change_password) VALUES('local',$1,$2,$3,'member',true) RETURNING id::text`, "invited-"+pendingID, name, email).Scan(&id); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO person_invites(account_id,code_hash,expires_at) VALUES($1,$2,now()+interval '7 days')`, id, agentTokenHash(code)); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO desktop_audit(actor_id,action,subject_id) VALUES($1,'person_invited',$2)`, actor, id); err != nil {
		return "", "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return id, code, nil
}

// AcceptPersonInvite chooses credentials atomically with consuming the invite. No temporary
// password is shared with the administrator. Existing first-login username rules apply.
func (s *Service) AcceptPersonInvite(ctx context.Context, code, username, password string) (string, error) {
	if len(code) != 43 {
		return "", ErrPerson
	}
	var id string
	if err := s.st.Pool().QueryRow(ctx, `SELECT account_id::text FROM person_invites WHERE code_hash=$1 AND used_at IS NULL AND expires_at>now()`, agentTokenHash(code)).Scan(&id); err != nil {
		return "", ErrPerson
	}
	if err := authprovider.ValidateUsername(username); err != nil {
		return "", err
	}
	if len(password) < 12 || len(password) > 72 {
		return "", authprovider.ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.lockAccountSessions(ctx, tx, id); err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `UPDATE person_invites SET used_at=now() WHERE account_id=$1 AND code_hash=$2 AND used_at IS NULL AND expires_at>now() AND EXISTS(SELECT 1 FROM accounts WHERE id=$1 AND disabled_at IS NULL)`, id, agentTokenHash(code))
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", ErrPerson
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET provider_subject=$2,password_hash=$3,must_change_password=false WHERE id=$1`, id, username, string(hash)); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO desktop_audit(actor_id,action,subject_id) VALUES($1,'person_invite_accepted',$1)`, id); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

// DisablePerson is intentionally one-way here: re-enabling requires a separate owner decision.
// Account liveness immediately refuses desktop credentials, including issuance racing this write.
func (s *Service) DisablePerson(ctx context.Context, actor, sid, id string) error {
	if id == actor {
		return ErrPersonSelf
	}
	if !agentTokenIDShape.MatchString(id) {
		return ErrPerson
	}
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Stable fence order avoids two administrators disabling each other deadlocking.
	for _, key := range orderedPeople(actor, id) {
		if err := s.lockAccountSessions(ctx, tx, key); err != nil {
			return err
		}
	}
	if err := s.peopleAdmin(ctx, tx, actor, sid); err != nil {
		return err
	}
	var alreadyDisabled bool
	if err := tx.QueryRow(ctx, `SELECT admin_disabled FROM accounts WHERE id=$1`, id).Scan(&alreadyDisabled); err != nil {
		return ErrPerson
	}
	if alreadyDisabled {
		return tx.Commit(ctx)
	}
	if err := s.RevokeAccountEverywhereTx(ctx, tx, id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE accounts SET disabled_at=coalesce(disabled_at,now()),admin_disabled=true WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrPerson
	}
	if _, err := tx.Exec(ctx, `UPDATE desktop_tokens SET revoked_at=coalesce(revoked_at,now()) WHERE account_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE desktop_enrollment_codes SET revoked_at=coalesce(revoked_at,now()) WHERE account_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO desktop_audit(actor_id,action,subject_id) VALUES($1,'person_disabled',$2)`, actor, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func orderedPeople(a, b string) []string {
	if a < b {
		return []string{a, b}
	}
	return []string{b, a}
}

// PersonInviteLifetime is documented for the one-time reveal UI.
const PersonInviteLifetime = 7 * 24 * time.Hour
