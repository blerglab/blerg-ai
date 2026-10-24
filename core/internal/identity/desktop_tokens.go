package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
)

var ErrDesktopCredential = errors.New("desktop credential refused")

// IssueDesktopToken grants only the named account's desktop identity. Runner
// separately checks that account and revision against the selected desktop row.
// Neither an agent token nor a daemon can choose this account through a public API.
func (s *Service) IssueDesktopToken(ctx context.Context, owner, desktop string, revision int64) (desktopauth.Binding, string, error) {
	var b desktopauth.Binding
	if !agentTokenIDShape.MatchString(owner) || !agentTokenIDShape.MatchString(desktop) || revision < 1 {
		return b, "", ErrDesktopCredential
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return b, "", err
	}
	raw := "bdt_" + hex.EncodeToString(seed)
	b.DesktopID, b.OwnerID, b.Revision = desktop, owner, revision
	b.ExpiresAt = time.Now().UTC().Add(365 * 24 * time.Hour)
	err := s.st.Pool().QueryRow(ctx, `INSERT INTO desktop_tokens(account_id,desktop_id,ownership_revision,token_hash,expires_at)
 SELECT id,$2,$3,$4,$5 FROM accounts WHERE id=$1 AND disabled_at IS NULL
 RETURNING id::text`, owner, desktop, revision, agentTokenHash(raw), b.ExpiresAt).Scan(&b.TokenID)
	if err != nil {
		return desktopauth.Binding{}, "", ErrDesktopCredential
	}
	return b, raw, nil
}

func (s *Service) DesktopToken(ctx context.Context, raw string) (desktopauth.Binding, error) {
	var b desktopauth.Binding
	if len(raw) != 68 || raw[:4] != "bdt_" {
		return b, ErrDesktopCredential
	}
	err := s.st.Pool().QueryRow(ctx, `SELECT t.id::text,t.desktop_id::text,t.account_id::text,t.ownership_revision,t.expires_at
 FROM desktop_tokens t JOIN accounts a ON a.id=t.account_id
 WHERE t.token_hash=$1 AND t.revoked_at IS NULL AND t.expires_at>now() AND a.disabled_at IS NULL`, agentTokenHash(raw)).Scan(&b.TokenID, &b.DesktopID, &b.OwnerID, &b.Revision, &b.ExpiresAt)
	if err != nil {
		return desktopauth.Binding{}, ErrDesktopCredential
	}
	return b, nil
}

// FirstDesktopOwner is used only by authenticated installer bootstrap. An
// explicit installing account wins; otherwise the oldest enabled admin is used.
func (s *Service) FirstDesktopOwner(ctx context.Context, requested string) (string, error) {
	var id string
	var err error
	if requested != "" {
		if !agentTokenIDShape.MatchString(requested) {
			return "", ErrDesktopCredential
		}
		err = s.st.Pool().QueryRow(ctx, `SELECT id::text FROM accounts WHERE id=$1 AND disabled_at IS NULL`, requested).Scan(&id)
	} else {
		err = s.st.Pool().QueryRow(ctx, `SELECT id::text FROM accounts WHERE role='admin' AND disabled_at IS NULL ORDER BY created_at,id LIMIT 1`).Scan(&id)
	}
	return id, err
}

type DesktopOwner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Service) DesktopOwners(ctx context.Context) ([]DesktopOwner, error) {
	rows, err := s.st.Pool().Query(ctx, `SELECT id::text,provider_subject FROM accounts WHERE disabled_at IS NULL ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DesktopOwner{}
	for rows.Next() {
		var o DesktopOwner
		if err := rows.Scan(&o.ID, &o.Name); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// NewDesktopID creates an identity for a newly enrolled desktop. Public issuance
// never accepts a caller-selected existing ID or future ownership revision.
func NewDesktopID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}
