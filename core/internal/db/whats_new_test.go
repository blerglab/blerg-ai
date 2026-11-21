package db

import "testing"

func TestWhatsNewMigrationAndNewAccountBaseline(t *testing.T) {
	st, err := Open(t.Context(), testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pool := st.Pool()
	// Reconstruct an upgraded install with an account predating the feature.
	_, err = pool.Exec(t.Context(), `DROP TRIGGER accounts_release_baseline ON accounts;
 DROP FUNCTION baseline_account_release();
 ALTER TABLE accounts DROP COLUMN whats_new_seen;
 DROP TABLE app_release_baseline;
 INSERT INTO accounts(provider,provider_subject,role) VALUES('local','existing','member')`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := migrationsFS.ReadFile("migrations/107_whats_new.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), string(migration)); err != nil {
		t.Fatal(err)
	}
	var seen string
	if err := pool.QueryRow(t.Context(), "SELECT whats_new_seen FROM accounts WHERE provider_subject='existing'").Scan(&seen); err != nil || seen != "0.5.4" {
		t.Fatalf("existing baseline: %q %v", seen, err)
	}
	for _, version := range []string{"0.6.0", "0.7.0"} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO app_release_baseline(singleton,version) VALUES(true,$1) ON CONFLICT(singleton) DO UPDATE SET version=EXCLUDED.version`, version); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(t.Context(), `INSERT INTO accounts(provider,provider_subject,role) VALUES('local',$1,'member') RETURNING whats_new_seen`, version).Scan(&seen); err != nil || seen != version {
			t.Fatalf("new account baseline: %q %v", seen, err)
		}
	}
	if err := pool.QueryRow(t.Context(), "SELECT whats_new_seen FROM accounts WHERE provider_subject='0.6.0'").Scan(&seen); err != nil || seen != "0.6.0" {
		t.Fatalf("upgrade changed account baseline: %q %v", seen, err)
	}
	// Replaying migrations must retain each person's already-seen release.
	if _, err := pool.Exec(t.Context(), string(migration)); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), "SELECT whats_new_seen FROM accounts WHERE provider_subject='0.6.0'").Scan(&seen); err != nil || seen != "0.6.0" {
		t.Fatalf("replay changed baseline: %q %v", seen, err)
	}

}
