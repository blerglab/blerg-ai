package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestQuietHoursPushHeld(t *testing.T) {
	f := newPrivFixture(t)
	core := livePushCore(t)
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/clock", "k", "a", privAcctA, testSID); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"timezone":"America/New_York","quiet_hours":{"enabled":true,"start":"22:00","end":"07:00"}}`)
	if err := db.PutAccountPreference(t.Context(), f.pool, privAcctA, prefPersonTime, raw); err != nil {
		t.Fatal(err)
	}
	saved := sendWebPush
	sent := 0
	sendWebPush = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = saved })
	ctx, cancel := context.WithTimeout(t.Context(), pushSendTimeout)
	defer cancel()
	now := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	if status := sendPushAt(ctx, f.pool, privAcctA, "Question", "Choose a color", "/sessions/"+f.pubSession, now); status != pushComplete {
		t.Fatal("durable hold should complete original send")
	}
	if sent != 0 {
		t.Fatal("quiet hours interrupted the person")
	}
	var held int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM held_pushes`).Scan(&held); err != nil || held != 1 {
		t.Fatalf("not durably held: %d %v", held, err)
	}
	// A fresh drainer (as after restart) still holds the repeated 01:30 EST hour.
	if err := flushHeldPushes(t.Context(), f.pool, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatal("fall-back hour interrupted the person")
	}
	// Extending the window reschedules the same activity without counting it again.
	if err := db.PutAccountPreference(t.Context(), f.pool, privAcctA, prefPersonTime, json.RawMessage(`{"timezone":"America/New_York","quiet_hours":{"enabled":true,"start":"22:00","end":"08:00"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := flushHeldPushes(t.Context(), f.pool, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := f.pool.QueryRow(t.Context(), `SELECT notice_count FROM held_pushes WHERE account_id=$1`, privAcctA).Scan(&count); err != nil || count != 1 {
		t.Fatal("rechecking quiet hours counted a new event", count, err)
	}
	// End is exclusive: 08:00 EST delivers the held notice.
	morning := time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC)
	core.Store(http.StatusServiceUnavailable)
	if err := flushHeldPushes(t.Context(), f.pool, morning); err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatal("unverified login received morning push")
	}
	core.Store(http.StatusOK)
	if err := flushHeldPushes(t.Context(), f.pool, morning.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatalf("morning deliveries=%d", sent)
	}
}

func TestQuietHoursPushAskExceptionsAndStaleQuestions(t *testing.T) {
	f := newPrivFixture(t)
	livePushCore(t)
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/quiet-ask", "k", "a", privAcctA, testSID); err != nil {
		t.Fatal(err)
	}
	if err := db.PutAccountPreference(t.Context(), f.pool, privAcctA, prefPersonTime, json.RawMessage(`{"timezone":"America/New_York","quiet_hours":{"enabled":true,"start":"22:00","end":"07:00"}}`)); err != nil {
		t.Fatal(err)
	}
	saved := sendWebPush
	sent := 0
	sendWebPush = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = saved })
	now := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		body                       string
		urgent, approval, wantPush bool
	}{
		{"Choose a color", false, false, false},
		{"The release expires in five minutes", true, false, true},
		{"Approve this command", false, true, true},
		{"May I drop the database?", false, false, true},
	} {
		id, err := db.CreateHostedAsk(t.Context(), f.pool, f.pubSession, tc.body, tc.approval, !tc.approval, true, 0)
		if err != nil {
			t.Fatal(err)
		}
		if tc.urgent {
			if _, err := f.pool.Exec(t.Context(), `UPDATE messages SET urgent=true WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
		}
		before := sent
		ctx, cancel := context.WithTimeout(t.Context(), pushSendTimeout)
		status := sendPushAt(ctx, f.pool, privAcctA, "Question", tc.body, "/sessions/"+f.pubSession, now)
		cancel()
		if status != pushComplete || (sent > before) != tc.wantPush {
			t.Fatalf("%s: status=%v sent=%d", tc.body, status, sent-before)
		}
		msg, err := db.GetMessage(t.Context(), f.pool, id)
		if err != nil || msg.Status != "open" {
			t.Fatal("held question disappeared from Needs you", err)
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE messages SET status='answered',answer='done' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	before := sent
	if err := flushHeldPushes(t.Context(), f.pool, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if sent != before+1 {
		t.Fatal("answered overnight question lost its generic morning summary")
	}
}

func TestQuietHoursUrgentAskHTTPPersistence(t *testing.T) {
	f := newPrivFixture(t)
	if _, err := f.pool.Exec(t.Context(), `CREATE FUNCTION reject_urgent_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.urgent AND NOT OLD.urgent THEN RAISE EXCEPTION 'urgent must be inserted atomically'; END IF; RETURN NEW; END $$; CREATE TRIGGER no_urgent_update BEFORE UPDATE ON messages FOR EACH ROW EXECUTE FUNCTION reject_urgent_update()`); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"ask", "note"} {
		raw, _ := json.Marshal(map[string]any{"session_id": f.pubSession, "kind": kind, "body": "A time-sensitive question", "urgent": true})
		w := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", f.api.daemonToken, string(raw))
		if kind == "note" {
			if w.Code != 400 {
				t.Fatalf("urgent note accepted: %d", w.Code)
			}
			continue
		}
		if w.Code != 201 {
			t.Fatalf("urgent ask: %d %s", w.Code, w.Body.String())
		}
		var out struct{ ID string }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		msg, err := db.GetMessage(t.Context(), f.pool, out.ID)
		if err != nil || msg == nil || !msg.Urgent || !msg.PersonRequired {
			t.Fatal("urgency or person-only routing not persisted", err)
		}
	}
}

func TestQuietHoursHeldPushRechecksPrivateOwner(t *testing.T) {
	f := newPrivFixture(t)
	livePushCore(t)
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/private-clock", "k", "a", privAcctA, testSID); err != nil {
		t.Fatal(err)
	}
	if err := db.PutAccountPreference(t.Context(), f.pool, privAcctA, prefPersonTime, json.RawMessage(`{"timezone":"America/New_York","quiet_hours":{"enabled":true,"start":"22:00","end":"07:00"}}`)); err != nil {
		t.Fatal(err)
	}
	saved := sendWebPush
	sent := 0
	sendWebPush = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = saved })
	ctx, cancel := context.WithTimeout(t.Context(), pushSendTimeout)
	defer cancel()
	if sendPushAt(ctx, f.pool, privAcctA, "Private work", "private note", "/sessions/"+f.privSession, time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)) != pushComplete {
		t.Fatal("hold failed")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, f.privSession, privAcctB); err != nil {
		t.Fatal(err)
	}
	if err := flushHeldPushes(t.Context(), f.pool, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatal("old owner received held private content")
	}
}

func TestQuietHoursHeldPushRevisionFence(t *testing.T) {
	f := newPrivFixture(t)
	livePushCore(t)
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/fence-clock", "k", "a", privAcctA, testSID); err != nil {
		t.Fatal(err)
	}
	target := "/sessions/" + f.pubSession
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO held_pushes(account_id,target,title,body) VALUES($1,$2,'Work','old')`, privAcctA, target); err != nil {
		t.Fatal(err)
	}
	saved := sendWebPush
	var bodies []string
	sendWebPush = func(raw []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		var payload map[string]string
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, payload["body"])
		if len(bodies) == 1 {
			// A concurrent arrival after the claim must survive completion of this delivery.
			if _, err := f.pool.Exec(t.Context(), `UPDATE held_pushes SET body='new',revision=revision+1 WHERE account_id=$1 AND target=$2`, privAcctA, target); err != nil {
				t.Fatal(err)
			}
		}
		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = saved })
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if err := flushHeldPushes(t.Context(), f.pool, now); err != nil {
		t.Fatal(err)
	}
	if err := flushHeldPushes(t.Context(), f.pool, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != "old" || bodies[1] != "new" {
		t.Fatalf("lost newer held push: %v", bodies)
	}
}

func TestQuietHoursClockReadFailureSendsPush(t *testing.T) {
	f := newPrivFixture(t)
	livePushCore(t)
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/clock-failure", "k", "a", privAcctA, testSID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE account_preferences RENAME TO unavailable_clock_preferences`); err != nil {
		t.Fatal(err)
	}
	saved := sendWebPush
	sent := 0
	sendWebPush = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = saved })
	ctx, cancel := context.WithTimeout(t.Context(), pushSendTimeout)
	defer cancel()
	if sendPushAt(ctx, f.pool, privAcctA, "Work", "Update", "", time.Now()) != pushComplete || sent != 1 {
		t.Fatal("clock read failure dropped a push")
	}
}

func TestQuietHoursCoalescingKeepsAskAndSummarizesActivity(t *testing.T) {
	f := newPrivFixture(t)
	if err := db.PutAccountPreference(t.Context(), f.pool, privAcctA, prefPersonTime, json.RawMessage(`{"timezone":"America/New_York","quiet_hours":{"enabled":true,"start":"22:00","end":"07:00"}}`)); err != nil {
		t.Fatal(err)
	}
	id, err := db.CreateHostedAsk(t.Context(), f.pool, f.pubSession, "Which color?", true, true, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	target := "/sessions/" + f.pubSession
	now := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	morning := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, body := range []string{"Which color?", "Work finished"} {
		held := holdQuietPush(t.Context(), f.pool, privAcctA, "Work", body, target, now)
		if !held {
			t.Fatal("hold failed")
		}
	}
	var linked *string
	var count, revision int64
	var lease time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT message_id::text,notice_count,revision,lease_until FROM held_pushes WHERE account_id=$1`, privAcctA).Scan(&linked, &count, &revision, &lease); err != nil {
		t.Fatal(err)
	}
	if linked == nil || *linked != id || count != 2 || !lease.Equal(morning) {
		t.Fatalf("ask/count/end lease lost: %v %d %v", linked, count, lease)
	}
	if err := flushHeldPushes(t.Context(), f.pool, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := f.pool.QueryRow(t.Context(), `SELECT revision FROM held_pushes WHERE account_id=$1`, privAcctA).Scan(&after); err != nil || after != revision {
		t.Fatal("quiet drain churned the row", err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE messages SET status='answered' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	deliver := func(_ context.Context, _ *pgxpool.Pool, account, title, body, target string, _ time.Time) pushDeliveryStatus {
		bodies = append(bodies, body)
		return pushComplete
	}

	if err := drainHeldPushes(t.Context(), f.pool, morning, nil, deliver); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || bodies[0] != "2 things happened while you were away." {
		t.Fatalf("lost activity summary: %v", bodies)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO held_pushes(account_id,target,title,body) VALUES($1,$2,'Work','Activity')`, privAcctA, target); err != nil {
		t.Fatal(err)
	}
	f.hub.MarkActivity(privAcctA)
	if err := drainHeldPushes(t.Context(), f.pool, morning, f.hub, deliver); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 {
		t.Fatal("active person received a held push")
	}
}

func TestQuietHoursUrgencyInsertedByEveryAskFactory(t *testing.T) {
	f := newPrivFixture(t)
	id, err := f.api.createCodexQuestion(t.Context(), postMessageRequest{SessionID: f.pubSession, Body: "Urgent Codex", Urgent: true, PersonRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := db.GetMessage(t.Context(), f.pool, id)
	if err != nil || !msg.Urgent {
		t.Fatal("Codex INSERT lost urgency", err)
	}
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	id, err = f.api.persistBoundAsk(t.Context(), tx, f.pubSession, privAcctA, boundSubject{Kind: "draft", DraftID: newUUID()}, "Urgent bound decision", true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	msg, err = db.GetMessage(t.Context(), f.pool, id)
	if err != nil || !msg.Urgent {
		t.Fatal("bound INSERT lost urgency", err)
	}
}
