package db_test

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestSettingsSetGetDelete(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()

	if err := db.SetSetting(ctx, pool, db.SettingMaxSessions, 6, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, pool, db.SettingPodTTLSeconds, 7200, "admin"); err != nil {
		t.Fatal(err)
	}
	m, err := db.GetSettings(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if m[db.SettingMaxSessions] != 6 || m[db.SettingPodTTLSeconds] != 7200 {
		t.Fatalf("stored settings = %v", m)
	}

	if err := db.DeleteSetting(ctx, pool, db.SettingMaxSessions); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSetting(ctx, pool, db.SettingMaxSessions); err != nil {
		t.Fatalf("deleting an absent setting must not fail: %v", err)
	}
	m, err = db.GetSettings(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m[db.SettingMaxSessions]; ok {
		t.Fatalf("deleted setting still stored: %v", m)
	}
	if m[db.SettingPodTTLSeconds] != 7200 {
		t.Fatalf("deleting one setting touched another: %v", m)
	}
}
