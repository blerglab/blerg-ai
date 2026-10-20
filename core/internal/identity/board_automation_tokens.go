package identity

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// "Run as me": a board mints a run-sessions agent token for the signed-in person, in one click,
// and stores it as the board's automation token. The token is an ordinary row of the person's
// own agent tokens (kind 'token'): it shows in their Settings list, counts toward their cap and
// can be revoked there. Its source column ("board <id>") is what lets the board revoke the token
// it replaces, and nothing else.

// BoardAutomationPreset is the preset a board automation token is minted with. The board starts
// sessions with it and needs nothing more.
const BoardAutomationPreset = "run-sessions"

// maxAgentTokenNameRunes mirrors the Settings limit (api.maxTokenNameLen).
const maxAgentTokenNameRunes = 64

// BoardAutomationSource is the agent_tokens.source value of a token minted for boardID.
func BoardAutomationSource(boardID string) string { return "board " + boardID }

// BoardAutomationTokenName is the name a board automation token gets in the owner's list:
// "Board <name> automation", with the board name cut (and control characters dropped) so the
// whole stays within the 64-character limit Settings applies to names.
func BoardAutomationTokenName(boardName string) string {
	const prefix, suffix = "Board ", " automation"
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(boardName))
	room := maxAgentTokenNameRunes - utf8.RuneCountInString(prefix) - utf8.RuneCountInString(suffix)
	if utf8.RuneCountInString(clean) > room {
		r := []rune(clean)
		clean = strings.TrimSpace(string(r[:room-1])) + "…"
	}
	if clean == "" {
		clean = "(unnamed)"
	}
	return prefix + clean + suffix
}

// CreateBoardAutomationToken mints a run-sessions token for accountID on behalf of boardID, the
// same way CreateAgentToken does for Settings (the role intersection, the live-token cap, the
// born-revoked check), and records the board as its source. The caller has proved that the
// account's owner is present right now; this function does not.
func (s *Service) CreateBoardAutomationToken(ctx context.Context, accountID, boardID, boardName string, expiresIn time.Duration) (AgentTokenRecord, string, error) {
	p := AgentTokenPresets[BoardAutomationPreset]
	return s.createAgentToken(ctx, accountID, BoardAutomationTokenName(boardName), p.Aud, p.Caps, expiresIn,
		BoardAutomationSource(boardID))
}

// RevokeBoardAutomationToken revokes id only when it is accountID's ordinary token minted for
// boardID (source "board <boardID>"). A Settings token, another board's, another account's and an
// unknown id are all ErrNotFound. Idempotent like RevokeAgentToken.
func (s *Service) RevokeBoardAutomationToken(ctx context.Context, accountID, id, boardID string) error {
	if !agentTokenIDShape.MatchString(id) || !agentTokenIDShape.MatchString(accountID) || boardID == "" {
		return ErrNotFound
	}
	var ok bool
	err := s.st.Pool().QueryRow(ctx,
		`UPDATE agent_tokens SET revoked_at = COALESCE(revoked_at, now())
		  WHERE id = $1 AND account_id = $2 AND kind = 'token' AND source = $3
		  RETURNING true`, id, accountID, BoardAutomationSource(boardID)).Scan(&ok)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return s.Revoke(ctx, "sub", id)
}
