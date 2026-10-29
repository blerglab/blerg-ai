package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// DesktopEnrollmentCodeTTL is how long a code from "Add a desktop" can be redeemed.
const DesktopEnrollmentCodeTTL = 15 * time.Minute

// maxDesktopEnrollmentCodesPerHour bounds how many codes one person can create in an hour.
const maxDesktopEnrollmentCodesPerHour = 10

var (
	// ErrDesktopEnrollmentCode is every refusal of a redemption: unknown, expired, used,
	// revoked, or its account disabled. The caller never learns which.
	ErrDesktopEnrollmentCode = errors.New("desktop enrollment code refused")
	// ErrDesktopEnrollmentRate is returned when a person created too many codes this hour.
	ErrDesktopEnrollmentRate = errors.New("too many desktop enrollment codes")
)

// DesktopEnrollmentCode is a code's metadata; the code itself is shown once, at creation.
type DesktopEnrollmentCode struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// desktopEnrollmentCodeShape: "bec_" and 40 hex characters (160 random bits).
func desktopEnrollmentCodeShape(raw string) bool {
	if len(raw) != 44 || raw[:4] != "bec_" {
		return false
	}
	_, err := hex.DecodeString(raw[4:])
	return err == nil
}

// IssueDesktopEnrollmentCode creates a single-use code bound to accountID. Only its hash is
// stored. The issue is audited in the same transaction.
func (s *Service) IssueDesktopEnrollmentCode(ctx context.Context, accountID string) (DesktopEnrollmentCode, string, error) {
	var c DesktopEnrollmentCode
	if !agentTokenIDShape.MatchString(accountID) {
		return c, "", ErrDesktopEnrollmentCode
	}
	seed := make([]byte, 20)
	if _, err := rand.Read(seed); err != nil {
		return c, "", err
	}
	raw := "bec_" + hex.EncodeToString(seed)
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return c, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize one account's issues so the hourly count cannot be raced past.
	if _, err := tx.Exec(ctx, `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, accountID); err != nil {
		return c, "", err
	}
	var recent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM desktop_enrollment_codes WHERE account_id=$1 AND created_at>now()-interval '1 hour'`, accountID).Scan(&recent); err != nil {
		return c, "", err
	}
	if recent >= maxDesktopEnrollmentCodesPerHour {
		return c, "", ErrDesktopEnrollmentRate
	}
	err = tx.QueryRow(ctx, `INSERT INTO desktop_enrollment_codes(account_id,code_hash,expires_at)
 SELECT id,$2,now()+make_interval(secs=>$3) FROM accounts WHERE id=$1 AND disabled_at IS NULL
 RETURNING id::text,created_at,expires_at`, accountID, agentTokenHash(raw), DesktopEnrollmentCodeTTL.Seconds()).Scan(&c.ID, &c.CreatedAt, &c.ExpiresAt)
	if err != nil {
		return DesktopEnrollmentCode{}, "", ErrDesktopEnrollmentCode
	}
	if _, err := tx.Exec(ctx, `INSERT INTO desktop_audit(actor_id,action,subject_id) VALUES($1,'code_issued',$2)`, accountID, c.ID); err != nil {
		return DesktopEnrollmentCode{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return DesktopEnrollmentCode{}, "", err
	}
	return c, raw, nil
}

// RedeemDesktopEnrollmentCode consumes a live code once and returns the account it is bound
// to. A refused redemption of a known code is audited for that code's account.
func (s *Service) RedeemDesktopEnrollmentCode(ctx context.Context, raw string) (string, error) {
	if !desktopEnrollmentCodeShape(raw) {
		return "", ErrDesktopEnrollmentCode
	}
	hash := agentTokenHash(raw)
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var account, id string
	err = tx.QueryRow(ctx, `UPDATE desktop_enrollment_codes c SET used_at=now()
 FROM accounts a WHERE a.id=c.account_id AND a.disabled_at IS NULL
 AND c.code_hash=$1 AND c.used_at IS NULL AND c.revoked_at IS NULL AND c.expires_at>now()
 RETURNING c.account_id::text,c.id::text`, hash).Scan(&account, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		var known, codeID string
		if s.st.Pool().QueryRow(ctx, `SELECT account_id::text,id::text FROM desktop_enrollment_codes WHERE code_hash=$1`, hash).Scan(&known, &codeID) == nil {
			_, _ = s.st.Pool().Exec(ctx, `INSERT INTO desktop_audit(actor_id,action,subject_id) VALUES($1,'code_refused',$2)`, known, codeID)
		}
		return "", ErrDesktopEnrollmentCode
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO desktop_audit(actor_id,action,subject_id) VALUES($1,'code_redeemed',$2)`, account, id); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return account, nil
}

// LiveDesktopEnrollmentCodes lists accountID's codes that can still be redeemed.
func (s *Service) LiveDesktopEnrollmentCodes(ctx context.Context, accountID string) ([]DesktopEnrollmentCode, error) {
	rows, err := s.st.Pool().Query(ctx, `SELECT id::text,created_at,expires_at FROM desktop_enrollment_codes
 WHERE account_id=$1 AND used_at IS NULL AND revoked_at IS NULL AND expires_at>now() ORDER BY created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DesktopEnrollmentCode{}
	for rows.Next() {
		var c DesktopEnrollmentCode
		if err := rows.Scan(&c.ID, &c.CreatedAt, &c.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeDesktopEnrollmentCode revokes one of accountID's unused codes. False: no such code.
func (s *Service) RevokeDesktopEnrollmentCode(ctx context.Context, accountID, id string) (bool, error) {
	if !agentTokenIDShape.MatchString(id) {
		return false, nil
	}
	tag, err := s.st.Pool().Exec(ctx, `WITH r AS (UPDATE desktop_enrollment_codes SET revoked_at=now()
 WHERE id=$2 AND account_id=$1 AND used_at IS NULL AND revoked_at IS NULL RETURNING id)
 INSERT INTO desktop_audit(actor_id,action,subject_id) SELECT $1,'code_revoked',id::text FROM r`, accountID, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// RevokeDesktopTokens revokes every live credential of one desktop and audits who did it.
func (s *Service) RevokeDesktopTokens(ctx context.Context, actor, desktopID string) error {
	if !agentTokenIDShape.MatchString(desktopID) {
		return ErrDesktopCredential
	}
	_, err := s.st.Pool().Exec(ctx, `WITH r AS (UPDATE desktop_tokens SET revoked_at=now() WHERE desktop_id=$2 AND revoked_at IS NULL)
 INSERT INTO desktop_audit(actor_id,action,subject_id) VALUES($1,'desktop_revoked',$2)`, actor, desktopID)
	return err
}

// RecordDesktopAudit records a person's desktop action (rename, enroll, assign).
func (s *Service) RecordDesktopAudit(ctx context.Context, actor, action, subject string) error {
	_, err := s.st.Pool().Exec(ctx, `INSERT INTO desktop_audit(actor_id,action,subject_id) VALUES($1,$2,$3)`, actor, action, subject)
	return err
}
