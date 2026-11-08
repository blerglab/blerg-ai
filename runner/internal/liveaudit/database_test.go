package liveaudit

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendRefusesUnsafeDatabaseBeforeCompiling(t *testing.T) {
	const safe = "postgres://audit:canary-password@127.0.0.1:55440/postgres"
	for _, tc := range []struct{ name, database, testDatabase string }{
		{"missing database", "", safe},
		{"missing test database", safe, ""},
		{"different databases", safe, "postgres://audit:canary-password@127.0.0.1:55440/other"},
		{"remote test port", "postgres://audit:canary-password@example.invalid:55440/postgres", "postgres://audit:canary-password@example.invalid:55440/postgres"},
		{"unmarked local", "postgres://audit:canary-password@localhost:5432/postgres", "postgres://audit:canary-password@localhost:5432/postgres"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", tc.database)
			t.Setenv("TEST_DATABASE_URL", tc.testDatabase)
			code, err := Backend(Options{Go: filepath.Join(t.TempDir(), "must-not-compile")})
			if code != 2 || err == nil || !strings.HasPrefix(err.Error(), "live_data_mutation_database_") {
				t.Fatalf("database was not refused before compilation: code=%d err=%v", code, err)
			}
			if strings.Contains(err.Error(), "canary-password") {
				t.Fatal("refusal disclosed a credential")
			}
		})
	}
}

func TestDatabaseTargetValidation(t *testing.T) {
	for _, tc := range []struct {
		url     string
		allowed bool
	}{
		{"postgres://localhost:55440/postgres", true},
		{"postgresql://127.0.0.1:55440/postgres?sslmode=disable", true},
		{"postgres://LOCALHOST/blerg_test", true},
		{"postgres://localhost:5432/test_blerg", true},
		{"postgres://localhost:5432/tests", true},
		{"postgres://localhost:5432/blerg.tests.audit", true},
		{"postgres://localhost:5432/blerg-test", true},
		{"postgres://localhost:5432/contest", false},
		{"postgres://localhost:5432/latest", false},
		{"postgres://localhost:5432/testimonial", false},
		{"postgres://localhost:55440/", false},
		{"postgres://localhost:55440/postgres?host=example.invalid", false},
		{"postgres://localhost:55440/postgres?hostaddr=192.0.2.1", false},
		{"postgres://localhost:55440/postgres?port=5432", false},
		{"postgres://localhost/blerg_test?dbname=production", false},
		{"postgres://localhost/blerg_test?service=production", false},
		{"postgres://localhost/blerg_test?%68ost=example.invalid", false},
		{"postgres://localhost/blerg_test?host=%zz", false},
		{"postgres://localhost.example.invalid:55440/test", false},
		{"postgres://[::1]:55440/test", false},
		{"postgres://localhost,example.invalid:55440/test", false},
		{"host=localhost port=55440 dbname=test", false},
		{"https://localhost:55440/test", false},
		{"postgres://localhost:65536/test", false},
		{"postgres://localhost:0/test", false},
		{"postgres://localhost:abc/test", false},
		{"postgres://localhost:55440/postgres#test", false},
		{"postgres://localhost:55440/production%2Ftest", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			err := validateDatabase(tc.url, tc.url)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
			}
		})
	}
}
