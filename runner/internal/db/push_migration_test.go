package db_test

import (
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"testing"
)

func TestPushMigrationRemovesUnownedAndPreservesOwnedForReregistration(t *testing.T) {
	pool := connect(t)
	_, err := pool.Exec(t.Context(), `
 DROP INDEX push_subscriptions_account_idx;
 ALTER TABLE push_subscriptions DROP COLUMN session_id;
 DELETE FROM schema_migrations WHERE filename='105_push_subscription_login.sql';
 INSERT INTO push_subscriptions(endpoint,p256dh,auth,account_id) VALUES
 ('https://push.example.com/legacy','k','a',''),
 ('https://push.example.com/owned','k','a','account');`)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.RunMigrations(t.Context(), pool); err != nil {
			t.Fatal(err)
		}
	}
	subs, err := db.ListPushSubscriptions(t.Context(), pool)
	if err != nil || len(subs) != 1 || subs[0].AccountID != "account" || subs[0].SessionID != "" {
		t.Fatalf("migration retained unsafe subscriptions: %+v %v", subs, err)
	}
	if err := db.UpsertPushSubscription(t.Context(), pool, subs[0].Endpoint, "k", "a", "new-account", "new-login"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPushSubscription(t.Context(), pool, subs[0].Endpoint, "new-key", "new-auth", "new-account", "new-login"); err != nil {
		t.Fatal(err)
	}
	subs, err = db.ListPushSubscriptions(t.Context(), pool)
	if err != nil || len(subs) != 1 || subs[0].AccountID != "new-account" || subs[0].SessionID != "new-login" || subs[0].P256dh != "new-key" {
		t.Fatalf("re-registration: %+v %v", subs, err)
	}
}
