package db

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GrantAuthorityObservation binds an observation to its original authority and
// the instant verification started, rather than when a slow request completed.
type GrantAuthorityObservation struct {
	Cause                            string
	AskedAt                          time.Time
	AccountID, ProofKind, ProofValue string
}

// RecordGrantAuthority uses the existing event journal and input outbox. It does
// not mutate grants or their provenance. An advisory lock serializes transitions
// across replicas and restarts; a shared lifecycle fence keeps execution readers
// compatible. A newer transition settles older
// undelivered notices before publishing its replacement.
func RecordGrantAuthority(ctx context.Context, pool *pgxpool.Pool, session, grant, kind, state string, observations ...GrantAuthorityObservation) error {
	if pool == nil {
		return nil
	}
	var sessionID pgtype.UUID
	if err := sessionID.Scan(session); err != nil || !sessionID.Valid {
		return nil
	}
	switch state {
	case "active", "suspended", "revoked", "unavailable":
	default:
		return errors.New("invalid grant authority state")
	}
	switch kind {
	case "Ticket board", "Blackboard", "Child-session", "MCP tool", "MCP connection":
	default:
		return errors.New("invalid grant authority kind")
	}
	// Follow only same-owner parent links; a worker's safe metadata notice belongs
	// to Main, never to another account or a terminal/hidden session.
	var target string
	err := pool.QueryRow(ctx, `WITH RECURSIVE chain AS (
 SELECT id,parent_session_id,spawning_account_id,0 AS depth FROM sessions WHERE id=$1 AND spawning_account_id IS NOT NULL AND spawning_account_id<>''
 UNION ALL SELECT p.id,p.parent_session_id,p.spawning_account_id,c.depth+1 FROM sessions p JOIN chain c ON p.id=c.parent_session_id
 WHERE p.spawning_account_id=c.spawning_account_id AND c.depth<32)
 SELECT id::text FROM chain ORDER BY depth DESC LIMIT 1`, session).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	observation := GrantAuthorityObservation{AskedAt: time.Now()}
	if len(observations) > 0 {
		observation = observations[0]
	}
	if observation.AskedAt.IsZero() {
		observation.AskedAt = time.Now()
	}
	observation.AskedAt = observation.AskedAt.UTC().Truncate(time.Microsecond)
	identity := session
	if observation.AccountID != "" && observation.ProofValue != "" {
		identity = target + "\x00" + kind + "\x00" + observation.AccountID + "\x00" + observation.ProofKind + "\x00" + observation.ProofValue
	}
	sum := sha256.Sum256([]byte(identity + "\x00" + grant))
	key := fmt.Sprintf("%x", sum[:16])
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `INSERT INTO grant_authority_observations(session_id,grant_key) VALUES($1,$2) ON CONFLICT DO NOTHING`, target, key); err != nil {
		return err
	}
	var previous, lastAuthority string
	var askedAt time.Time
	var unavailableSince *time.Time
	var unavailableNotified bool
	err = tx.QueryRow(ctx, `SELECT state,authority,asked_at,unavailable_since,unavailable_notified FROM grant_authority_observations WHERE session_id=$1 AND grant_key=$2 FOR UPDATE`, target, key).Scan(&previous, &lastAuthority, &askedAt, &unavailableSince, &unavailableNotified)
	if err != nil {
		return err
	}
	legacySum := sha256.Sum256([]byte(session + "\x00" + grant))
	legacyKey := fmt.Sprintf("%x", legacySum[:16])
	if previous == "" && key != legacyKey {
		// Old journals keyed by observer session. Adopt that authority once; new
		// observations then share their provenance key across inherited sessions.
		err = tx.QueryRow(ctx, `SELECT state,authority,asked_at,unavailable_since,unavailable_notified FROM grant_authority_observations WHERE session_id=$1 AND grant_key=$2 AND (adopted_by IS NULL OR adopted_by=$3) FOR UPDATE`, target, legacyKey, key).Scan(&previous, &lastAuthority, &askedAt, &unavailableSince, &unavailableNotified)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			if _, err = tx.Exec(ctx, `UPDATE grant_authority_observations SET adopted_by=$3 WHERE session_id=$1 AND grant_key=$2`, target, legacyKey, key); err != nil {
				return err
			}
		}
	}
	if !observation.AskedAt.After(askedAt) {
		return tx.Commit(ctx)
	}
	outageNotice := false
	if state == "unavailable" {
		if unavailableSince == nil {
			since := observation.AskedAt
			unavailableSince = &since
		}
		outageNotice = !unavailableNotified && observation.AskedAt.Sub(*unavailableSince) >= 5*time.Minute
		unavailableNotified = unavailableNotified || outageNotice
	} else {
		unavailableSince = nil
		unavailableNotified = false
	}
	notify := outageNotice || lastAuthority != state && (state == "suspended" || state == "revoked" || state == "active" && (lastAuthority == "suspended" || lastAuthority == "revoked"))
	if state != "unavailable" {
		lastAuthority = state
	}
	if _, err = tx.Exec(ctx, `UPDATE grant_authority_observations SET state=$3,authority=$4,asked_at=$5,unavailable_since=$6,unavailable_notified=$7 WHERE session_id=$1 AND grant_key=$2`, target, key, state, lastAuthority, observation.AskedAt, unavailableSince, unavailableNotified); err != nil {
		return err
	}
	if key != legacyKey && state != "unavailable" && (previous != state || state == "active") {
		if _, err = tx.Exec(ctx, `UPDATE session_inputs SET settled_at=now() WHERE session_id=$1 AND client_id LIKE $2 AND settled_at IS NULL`, target, "grant-state:"+legacyKey+":%"); err != nil {
			return err
		}
	}
	// An unchanged observation advances ordering without taking the publisher lock.
	if previous == state && !outageNotice {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "grant-authority:"+target); err != nil {
		return err
	}
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT ended_at IS NULL AND archived_at IS NULL AND discard_requested_at IS NULL AND status NOT IN ('ended','stopped') FROM sessions WHERE id=$1 FOR SHARE`, target).Scan(&eligible)
	if err != nil || !eligible {
		return err
	}
	cause := ""
	if state == "unavailable" && observation.Cause == "board_owner_missing" {
		cause = observation.Cause
	}
	payload, _ := json.Marshal(map[string]string{"grant_key": key, "state": state, "kind": kind, "cause": cause})
	var event string
	if err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&event); err != nil {
		return err
	}
	seq, _, err := AppendAgentEventTx(ctx, tx, target, event, "grant_authority", string(payload), time.Time{})
	if err != nil {
		return err
	}
	prefix := "grant-state:" + key + ":"
	// Recovery also settles a queued outage notice when it needs no recovery notice.
	if state != "unavailable" || notify {
		if _, err = tx.Exec(ctx, `UPDATE session_inputs SET settled_at=now() WHERE session_id=$1 AND client_id LIKE $2 AND settled_at IS NULL`, target, prefix+"%"); err != nil {
			return err
		}
	}
	if !notify {
		return tx.Commit(ctx)
	}
	text := kind + " permission is active again. Existing access is restored automatically."
	switch state {
	case "suspended":
		text = kind + " permission is suspended. Sign in to restore access automatically, and finish any required password change."
	case "unavailable":
		text = kind + " permission could not be verified. Check session Permissions to retry verification."
		if cause == "board_owner_missing" {
			text = kind + " permission could not be verified because the board has no recorded owner. Ask the owner or an administrator to assign an owner."
		}
	case "revoked":
		text = kind + " permission has expired. Check session Permissions and grant access again."
	}
	text = "[system] " + text
	id := fmt.Sprintf("%s%d", prefix, seq)
	frame, _ := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: target, ClientID: id, Text: text, Source: "system"})
	// A system notice is not a person's accepted input. Reuse the state
	// journal event as its outbox receipt, without a queued-person bubble.
	var count, size int64
	if err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(octet_length(frame::text)),0) FROM session_inputs WHERE session_id=$1 AND settled_at IS NULL`, target).Scan(&count, &size); err != nil {
		return err
	}
	if count >= SessionInputMaxPending || size+int64(len(frame)) > SessionInputMaxPendingBytes {
		return ErrSessionInputQueue
	}
	if _, err = tx.Exec(ctx, `INSERT INTO session_inputs(session_id,client_id,frame,event_id,event_seq) VALUES($1,$2,$3,$4,$5)`, target, id, string(frame), event, seq); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
