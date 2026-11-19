package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/persontime"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Classify only an actual open ask, never a title or a keyword in an unrelated push.
// Explicit urgency routes asks to the person and bypasses quiet hours; it grants no approval.
func quietPushQuestion(ctx context.Context, pool *pgxpool.Pool, body, target string) (*string, bool, error) {
	session, ok := strings.CutPrefix(strings.SplitN(target, "?", 2)[0], "/sessions/")
	if !ok || !validSessionID(session) {
		return nil, false, nil
	}
	var id string
	var urgent, ownerOnly, noWait bool
	err := pool.QueryRow(ctx, `SELECT id::text,urgent,owner_only,no_wait FROM messages WHERE session_id=$1 AND kind='ask' AND status='open' AND body=$2 ORDER BY urgent DESC,created_at DESC LIMIT 1`, session, body).Scan(&id, &urgent, &ownerOnly, &noWait)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &id, urgent || (ownerOnly && !noWait) || destructiveQuestion(body), nil
}

type heldPushReplayKey struct{}

func holdQuietPush(ctx context.Context, pool *pgxpool.Pool, account, title, body, target string, now time.Time) bool {
	raw, err := db.GetAccountPreference(ctx, pool, account, prefPersonTime)
	if err != nil {
		logPersonTimeFailure("push_clock_read", err)
		return false
	}
	if raw == nil {
		return false
	}
	var clock persontime.Account
	if err := json.Unmarshal(raw, &clock); err != nil {
		logPersonTimeFailure("push_clock_decode", err)
		return false
	}
	if !inQuietHours(clock, now) {
		return false
	}
	question, interrupt, err := quietPushQuestion(ctx, pool, body, target)
	if err != nil {
		logPersonTimeFailure("push_question_lookup", err)
		return false
	}
	if interrupt {
		return false
	}
	increment := int64(1)
	if ctx.Value(heldPushReplayKey{}) != nil {
		increment = 0
	}
	_, err = pool.Exec(ctx, `INSERT INTO held_pushes(account_id,target,title,body,message_id,lease_until) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(account_id,target) DO UPDATE SET title=EXCLUDED.title,body=EXCLUDED.body,message_id=COALESCE(EXCLUDED.message_id,held_pushes.message_id),revision=held_pushes.revision+1,notice_count=held_pushes.notice_count+$7,lease_until=EXCLUDED.lease_until`, account, target, title, body, question, clock.QuietEnd(now), increment)
	if err != nil {
		logPersonTimeFailure("push_hold_write", err)
		return false
	}
	return true
}

type heldPush struct {
	account, target, title, body string
	message                      *string
	revision                     int64
	count                        int64
}

// Claim bounded batches across replicas; delivery retries recheck privacy, subscriptions,
// live login proofs and the latest account window. Revision fencing retains newer arrivals.
func flushHeldPushes(ctx context.Context, pool *pgxpool.Pool, now time.Time, hubs ...*Hub) error {
	var hub *Hub
	if len(hubs) > 0 {
		hub = hubs[0]
	}
	return drainHeldPushes(ctx, pool, now, hub, sendPushAt)
}

type heldPushDelivery func(context.Context, *pgxpool.Pool, string, string, string, string, time.Time) pushDeliveryStatus

func drainHeldPushes(ctx context.Context, pool *pgxpool.Pool, now time.Time, hub *Hub, deliver heldPushDelivery) (resultErr error) {
	defer func() {
		if resultErr != nil {
			logPersonTimeFailure("push_drain", resultErr)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	rows, err := pool.Query(ctx, `UPDATE held_pushes p SET lease_until=$1+interval '5 minutes'
 FROM (SELECT account_id,target FROM held_pushes WHERE lease_until<=$1 ORDER BY lease_until,account_id,target LIMIT 50 FOR UPDATE SKIP LOCKED) c
 WHERE p.account_id=c.account_id AND p.target=c.target
 RETURNING p.account_id,p.target,p.title,p.body,p.message_id::text,p.revision,p.notice_count`, now)
	if err != nil {
		return err
	}
	var batch []heldPush
	for rows.Next() {
		var p heldPush
		if err := rows.Scan(&p.account, &p.target, &p.title, &p.body, &p.message, &p.revision, &p.count); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range batch {
		status := pushComplete
		active := hub != nil && hub.ActiveWithin(p.account, activityTTL)
		if p.message != nil {
			msg, err := db.GetMessage(ctx, pool, *p.message)
			if err != nil {
				return err
			}
			if msg == nil || msg.Status != "open" {
				p.title = "Blerg activity"
				if p.count == 1 {
					p.body = "1 thing happened while you were away."
				} else {
					p.body = fmt.Sprintf("%d things happened while you were away.", p.count)
				}
			} else {
				p.title = "Blerg needs you"
				p.body = msg.Body
			}
		}
		if !active {
			sendCtx, cancel := context.WithTimeout(context.WithValue(ctx, heldPushReplayKey{}, true), pushSendTimeout)
			status = deliver(sendCtx, pool, p.account, p.title, p.body, p.target, now)
			cancel()
		}
		if status == pushComplete {
			if _, err := pool.Exec(ctx, `DELETE FROM held_pushes WHERE account_id=$1 AND target=$2 AND revision=$3`, p.account, p.target, p.revision); err != nil {
				return err
			}
		}
	}
	return nil
}

// RunHeldPushChecks drains after quiet hours without waking an agent or creating an input.
func RunHeldPushChecks(ctx context.Context, pool *pgxpool.Pool, hubs ...*Hub) {
	if pool == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		_ = flushHeldPushes(ctx, pool, time.Now(), hubs...) // failure is logged with its typed cause at the drain boundary
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
