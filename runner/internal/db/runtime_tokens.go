package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MintRuntimeLaunch atomically replaces a pod's authority. The messaging token
// already prepared by the caller is retained for managed-credential enrollment.
// Raw runtime credentials are returned once and never stored.
func MintRuntimeLaunch(ctx context.Context, pool *pgxpool.Pool, sessionID, messageRaw string, ttl time.Duration) (runtimeRaw, messagingRaw string, err error) {
	err = WithRuntimeLaunchFence(ctx, pool, sessionID, func(fenced context.Context) error {
		var mintErr error
		runtimeRaw, messagingRaw, mintErr = mintRuntimeLaunch(fenced, pool, sessionID, messageRaw, ttl)
		return mintErr
	})
	return
}
func mintRuntimeLaunch(ctx context.Context, pool *pgxpool.Pool, sessionID, messageRaw string, ttl time.Duration) (runtimeRaw, messagingRaw string, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sid string
	if err = tx.QueryRow(ctx, `SELECT id FROM sessions WHERE id=$1 AND runtime='cluster' AND ended_at IS NULL AND status NOT IN ('stopped','ended') FOR UPDATE`, sessionID).Scan(&sid); err != nil {
		return "", "", err
	}
	var messageID string
	if messageRaw != "" {
		raw, decodeErr := hex.DecodeString(messageRaw)
		if decodeErr != nil {
			return "", "", ErrTokenInvalid
		}
		hash := sha256.Sum256(raw)
		err = tx.QueryRow(ctx, `SELECT id FROM board_tokens WHERE token_hash=$1 AND session_id=$2 AND board_id IS NULL AND capabilities=ARRAY['message']::text[] AND revoked_at IS NULL AND expires_at>now()`, hash[:], sessionID).Scan(&messageID)
		if err != nil {
			return "", "", err
		}
	}
	mint := func(capability string) (string, string, error) {
		b := make([]byte, 32)
		if _, e := rand.Read(b); e != nil {
			return "", "", e
		}
		h := sha256.Sum256(b)
		var id string
		e := tx.QueryRow(ctx, `INSERT INTO board_tokens(session_id,token_hash,capabilities,expires_at) VALUES($1,$2,$3,$4) RETURNING id`, sessionID, h[:], []string{capability}, time.Now().Add(ttl)).Scan(&id)
		return hex.EncodeToString(b), id, e
	}
	// A replacement must never retain the prior pod's messaging capability,
	// even if an internal caller accidentally passes its old raw token.
	if messageID != "" {
		var reused bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_runtime WHERE session_id=$1 AND messaging_token_id=$2)`, sessionID, messageID).Scan(&reused)
		if err != nil {
			return "", "", err
		}
		if reused {
			messageID = ""
		}
	}
	if messageID == "" {
		messageRaw, messageID, err = mint("message")
		if err != nil {
			return "", "", err
		}
	}
	var runtimeID string
	runtimeRaw, runtimeID, err = mint("runtime")
	if err != nil {
		return "", "", err
	}
	_, err = tx.Exec(ctx, `UPDATE board_tokens SET revoked_at=now() WHERE id IN (SELECT runtime_token_id FROM session_runtime WHERE session_id=$1 UNION ALL SELECT messaging_token_id FROM session_runtime WHERE session_id=$1) AND id<>$2::uuid`, sessionID, messageID)
	if err != nil {
		return "", "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO session_runtime(session_id,runtime_token_id,messaging_token_id,repo,project_path) SELECT id,$2,$3,repo,project_path FROM sessions WHERE id=$1 ON CONFLICT(session_id) DO UPDATE SET runtime_token_id=EXCLUDED.runtime_token_id,messaging_token_id=EXCLUDED.messaging_token_id,daemon_id=EXCLUDED.daemon_id,repo=EXCLUDED.repo,project_path=EXCLUDED.project_path`, sessionID, runtimeID, messageID)
	if err != nil {
		return "", "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return runtimeRaw, messageRaw, nil
}

// ValidateRuntimeBinding checks current launch identity on every inbound frame.
// A heartbeat may slide only the two still-live tokens of that launch.
func ValidateRuntimeBinding(ctx context.Context, pool *pgxpool.Pool, sessionID, tokenID string, slide bool) error {
	if pool == nil {
		return ErrTokenInvalid
	}
	var ok bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_runtime r JOIN sessions s ON s.id=r.session_id JOIN board_tokens t ON t.id=r.runtime_token_id WHERE r.session_id=$1 AND r.runtime_token_id=$2 AND s.runtime='cluster' AND s.ended_at IS NULL AND s.status NOT IN ('stopped','ended') AND t.revoked_at IS NULL AND t.expires_at>now())`, sessionID, tokenID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrTokenInvalid
	}
	if slide {
		_, err = pool.Exec(ctx, `UPDATE board_tokens t SET expires_at=now()+interval '24 hours' FROM session_runtime r JOIN sessions s ON s.id=r.session_id JOIN board_tokens rt ON rt.id=r.runtime_token_id WHERE r.session_id=$1 AND r.runtime_token_id=$2 AND s.runtime='cluster' AND s.ended_at IS NULL AND s.status NOT IN ('stopped','ended') AND rt.revoked_at IS NULL AND rt.expires_at>now() AND t.id IN(r.runtime_token_id,r.messaging_token_id) AND t.revoked_at IS NULL AND t.expires_at>now() AND t.expires_at<now()+interval '12 hours'`, sessionID, tokenID)
	}
	return err
}
func ValidateRuntimeToken(ctx context.Context, pool *pgxpool.Pool, raw string) (BoardTokenRow, error) {
	if pool == nil {
		return BoardTokenRow{}, ErrTokenInvalid
	}
	row, err := ValidateBoardToken(ctx, pool, raw)
	if err != nil {
		return BoardTokenRow{}, err
	}
	if row.BoardID != "" || !slices.Equal(row.Capabilities, []string{"runtime"}) {
		return BoardTokenRow{}, ErrTokenInvalid
	}
	if err = ValidateRuntimeBinding(ctx, pool, row.SessionID, row.ID, false); err != nil {
		return BoardTokenRow{}, err
	}
	return row, nil
}

// RuntimeDaemonID is assigned by the server per launch; pod hello identity is ignored.
func RuntimeDaemonID(ctx context.Context, pool *pgxpool.Pool, sessionID, tokenID string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT daemon_id FROM session_runtime WHERE session_id=$1 AND runtime_token_id=$2`, sessionID, tokenID).Scan(&id)
	return id, err
}

// RuntimeScope is immutable launch context, unaffected by runtime reports.
func RuntimeScope(ctx context.Context, pool *pgxpool.Pool, sessionID, tokenID string) (repo, projectPath string, err error) {
	err = pool.QueryRow(ctx, `SELECT repo,project_path FROM session_runtime WHERE session_id=$1 AND runtime_token_id=$2`, sessionID, tokenID).Scan(&repo, &projectPath)
	return repo, projectPath, err
}
