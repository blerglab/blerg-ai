package liveaudit

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// validateDatabase refuses ambiguous targets before an audit builds or runs any
// code. Port 55440 is this workspace's dedicated test server; otherwise the
// database name must contain a delimited test/tests component.
func validateDatabase(database, testDatabase string) error {
	if database == "" || testDatabase == "" {
		return errors.New("live_data_mutation_database_required: configure both DATABASE_URL and TEST_DATABASE_URL")
	}
	if database != testDatabase {
		return errors.New("live_data_mutation_database_mismatch: DATABASE_URL and TEST_DATABASE_URL must be identical")
	}
	u, err := url.Parse(database)
	if err != nil || u == nil || u.Opaque != "" || u.Fragment != "" || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return errors.New("live_data_mutation_database_invalid: use an explicit PostgreSQL URL")
	}
	if u.Hostname() != "127.0.0.1" && !strings.EqualFold(u.Hostname(), "localhost") {
		return errors.New("live_data_mutation_database_host: test server must be localhost or 127.0.0.1")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return errors.New("live_data_mutation_database_invalid: malformed connection parameters")
	}
	// URL target checks must not be bypassed by libpq/pgx query overrides.
	for key := range query {
		switch strings.ToLower(key) {
		case "host", "hostaddr", "port", "dbname", "database", "service", "servicefile":
			return errors.New("live_data_mutation_database_override: connection target overrides are not permitted")
		}
	}
	name := strings.TrimPrefix(u.Path, "/")
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(name) {
		return errors.New("live_data_mutation_database_name: specify an explicit database name")
	}
	port := u.Port()
	if port != "" && !validPort(port) {
		return errors.New("live_data_mutation_database_port: invalid PostgreSQL port")
	}
	marked := regexp.MustCompile(`(^|[_.-])tests?([_.-]|$)`).MatchString(strings.ToLower(name))
	if port != "55440" && !marked {
		return errors.New("live_data_mutation_database_unmarked: use dedicated test port 55440 or a database name with a delimited test/tests component")
	}
	return nil
}
func validPort(port string) bool {
	n := 0
	for _, c := range port {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
		if n > 65535 {
			return false
		}
	}
	return n > 0
}
