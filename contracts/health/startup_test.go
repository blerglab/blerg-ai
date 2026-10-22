package health

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStartupGateKeepsLivenessWhileNotReady(t *testing.T) {
	var gate Gate
	for _, path := range []string{"/readyz", "/api/sessions", "/healthz"} {
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 503 {
			t.Fatalf("%s status=%d", path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, httptest.NewRequest("GET", "/livez", nil))
	if rec.Code != 200 {
		t.Fatalf("liveness=%d", rec.Code)
	}
	gate.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	rec = httptest.NewRecorder()
	gate.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 204 {
		t.Fatalf("initialized=%d", rec.Code)
	}
}

func TestDatabaseRetryBackoffAndSafeLogs(t *testing.T) {
	var delays []time.Duration
	var logs bytes.Buffer
	attempts := 0
	err := retryDatabase(t.Context(), func(context.Context) error {
		attempts++
		if attempts < 9 {
			return errors.New("private connection detail")
		}
		return nil
	}, func(_ context.Context, delay time.Duration) error { delays = append(delays, delay); return nil },
		Retry{Logf: func(format string, args ...any) { fmt.Fprintf(&logs, format, args...) }, Secrets: []string{"private"}})
	if err != nil || attempts != 9 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	expected := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}
	if fmt.Sprint(delays) != fmt.Sprint(expected) {
		t.Fatalf("delays=%v", delays)
	}
	if strings.Contains(logs.String(), "private") || strings.Count(logs.String(), "attempt") != 9 ||
		strings.Count(logs.String(), "[redacted] connection detail") != 8 {
		t.Fatalf("unsafe or missing attempt log: %s", logs.String())
	}
}

func TestDatabaseRetryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	attempts := 0
	err := RetryDatabase(ctx, func(context.Context) error { attempts++; cancel(); return errors.New("unavailable") }, Retry{})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	err = RetryDatabase(ctx, func(context.Context) error { t.Fatal("attempted after cancellation"); return nil }, Retry{})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestStartupListenerSurvivesInitialization(t *testing.T) {
	srv, err := Listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	client := &http.Client{Timeout: time.Second}
	for _, tc := range []struct {
		path string
		code int
	}{{"/livez", 200}, {"/readyz", 503}} {
		req, err := http.NewRequestWithContext(t.Context(), "GET", "http://"+srv.server.Addr+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.code {
			t.Fatalf("%s status=%d", tc.path, resp.StatusCode)
		}
	}
	srv.SetHandler(Handler(func(context.Context) error { return nil }))
	req, err := http.NewRequestWithContext(t.Context(), "GET", "http://"+srv.server.Addr+"/readyz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ready=%d", resp.StatusCode)
	}
}

// sqlStateError stands in for pgconn.PgError, which contracts does not import.
type sqlStateError struct{ code, msg string }

func (e *sqlStateError) Error() string    { return e.msg }
func (e *sqlStateError) SQLState() string { return e.code }

const fixturePassword = "fixture-Secret-41"

// The errors a failed connection or migration can produce, each carrying the credential in a
// different form.
func credentialErrors() []error {
	return []error{
		fmt.Errorf("connect: %w", &sqlStateError{"28P01", `password authentication failed for user "blerg"`}),
		fmt.Errorf("run migrations: dial postgres://blerg:%s@db.internal:5432/blerg?sslmode=disable: refused", fixturePassword),
		fmt.Errorf("cannot parse `host=db user=blerg password=%s dbname=blerg`: bad", fixturePassword),
		fmt.Errorf("apply 012_x.sql: %w", &sqlStateError{"42601", "syntax error near\n" + fixturePassword}),
		errors.New("postgresql://other:" + "elsewhere-9@h/db failed\r\nforged line"),
	}
}

func TestDatabaseRetryLogsSanitizedReasons(t *testing.T) {
	var logs bytes.Buffer
	cases := credentialErrors()
	attempts := 0
	err := retryDatabase(t.Context(), func(context.Context) error {
		attempts++
		if attempts <= len(cases) {
			return cases[attempts-1]
		}
		return nil
	}, func(context.Context, time.Duration) error { return nil },
		Retry{Logf: func(format string, args ...any) { fmt.Fprintf(&logs, format+"\n", args...) }, Secrets: []string{fixturePassword}})
	if err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, secret := range []string{fixturePassword, "elsewhere-9", "blerg:"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log contains a credential %q:\n%s", secret, out)
		}
	}
	for _, want := range []string{
		`attempt 1 failed (SQLSTATE 28P01: connect: password authentication failed for user "blerg")`,
		"postgres://[redacted]@db.internal:5432/blerg",
		"password=[redacted]",
		"SQLSTATE 42601: apply 012_x.sql: syntax error near [redacted]",
		"postgresql://[redacted]@h/db failed forged line",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "\n") != len(cases)+1 {
		t.Fatalf("a reason broke a log line:\n%s", out)
	}
}

func TestReadyzSaysWhyAfterSustainedFailure(t *testing.T) {
	var gate Gate
	readyz := func() (int, string) {
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
		return rec.Code, rec.Body.String()
	}
	failures := 0
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_ = retryDatabase(ctx, func(context.Context) error {
		failures++
		code, body := readyz()
		if code != 503 {
			t.Fatalf("readyz=%d during startup", code)
		}
		if failures <= SustainedFailures && strings.TrimSpace(body) != "not ready" {
			t.Fatalf("attempt %d: reason shown before a sustained failure: %q", failures, body)
		}
		if failures == SustainedFailures+1 {
			cancel()
		}
		return credentialErrors()[failures%len(credentialErrors())]
	}, func(context.Context, time.Duration) error { return nil }, Retry{Gate: &gate, Secrets: []string{fixturePassword}})
	code, body := readyz()
	if code != 503 || !strings.Contains(body, "failed 6 times in a row") || !strings.Contains(body, "last: database: error") {
		t.Fatalf("readyz=%d %q", code, body)
	}
	assertClassOnly(t, body)
	// Only /readyz explains; other paths and a ready server say nothing more.
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, httptest.NewRequest("GET", "/api/x", nil))
	if strings.TrimSpace(rec.Body.String()) != "not ready" {
		t.Fatalf("api body=%q", rec.Body.String())
	}
	gate.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	if code, _ := readyz(); code != 200 {
		t.Fatalf("ready=%d", code)
	}
}

func TestSafeReasonClassesAndLength(t *testing.T) {
	if got := SafeReason(context.DeadlineExceeded); got != "timeout: context deadline exceeded" {
		t.Fatal(got)
	}
	if got := SafeReason(errors.New(strings.Repeat("x", 1000))); !strings.HasPrefix(got, "error: x") || len([]rune(got)) > maxReason+len("error: ") {
		t.Fatalf("len=%d", len(got))
	}
	if SafeReason(nil) != "" {
		t.Fatal("nil reason")
	}
}

// On a stop signal the server keeps serving through the drain with /readyz at 503, lets an
// in-flight request finish, then refuses new connections and returns nil.
func TestServeUntilSignalDrainsThenShutsDown(t *testing.T) {
	srv, err := Listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	release := make(chan struct{})
	started := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", Handler(func(context.Context) error { return nil }))
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("done"))
	})
	srv.SetHandler(mux)
	base := "http://" + srv.server.Addr
	get := func(path string) (int, error) {
		client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		req, err := http.NewRequestWithContext(context.Background(), "GET", base+path, nil)
		if err != nil {
			return 0, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}
	if code, err := get("/readyz"); err != nil || code != 200 {
		t.Fatalf("ready before the signal: %d %v", code, err)
	}
	slow := make(chan int, 1)
	go func() { code, _ := get("/slow"); slow <- code }()
	<-started

	stop := make(chan os.Signal, 1)
	returned := make(chan error, 1)
	go func() { returned <- srv.serveUntil(t.Context(), stop, 300*time.Millisecond, 5*time.Second, nil) }()
	stop <- syscall.SIGTERM
	time.Sleep(50 * time.Millisecond)
	if code, err := get("/readyz"); err != nil || code != 503 {
		t.Fatalf("readyz during the drain: %d %v", code, err)
	}
	if code, err := get("/livez"); err != nil || code != 200 {
		t.Fatalf("livez during the drain: %d %v", code, err)
	}
	time.Sleep(400 * time.Millisecond) // past the drain: Shutdown waits for /slow
	select {
	case err := <-returned:
		t.Fatalf("returned before the in-flight request finished: %v", err)
	default:
	}
	close(release)
	if code := <-slow; code != 200 {
		t.Fatalf("in-flight request: %d", code)
	}
	if err := <-returned; err != nil {
		t.Fatal(err)
	}
	if _, err := get("/livez"); err == nil {
		t.Fatal("still accepting connections after shutdown")
	}
}

// assertClassOnly fails when a /readyz body carries anything of the error's text: the password,
// the user, the host, the port, the database name or the message.
func assertClassOnly(t *testing.T, body string) {
	t.Helper()
	for _, leak := range []string{fixturePassword, "blerg", "db.internal", "5432", "fixture-host", "fixture_user",
		"fixture_db", "password", "migrations", "syntax", "dial", "postgres", "redacted"} {
		if strings.Contains(body, leak) {
			t.Fatalf("readyz body carries %q: %q", leak, body)
		}
	}
}

func TestReadyzCarriesOnlyTheErrorClass(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 7), Port: 5432},
		Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("failed to connect to `user=fixture_user database=fixture_db`: fixture-host:5432: %w",
			&sqlStateError{"28P01", `password authentication failed for user "fixture_user"`}), "database: SQLSTATE 28P01 (authentication failed)"},
		{fmt.Errorf("connect fixture-host:5432 fixture_db: %w", &sqlStateError{"3D000", `database "fixture_db" does not exist`}), "database: SQLSTATE 3D000 (database does not exist)"},
		{fmt.Errorf("apply 012_x.sql: %w", &sqlStateError{"42601", "syntax error at fixture_db"}), "database: SQLSTATE 42601"},
		{fmt.Errorf("failed to connect to `user=fixture_user database=fixture_db`: %w", refused), "database: connection refused"},
		{fmt.Errorf("dial: %w", &net.DNSError{Err: "no such host", Name: "fixture-host"}), "database: host name not resolved"},
		{fmt.Errorf("fixture-host:5432: %w", context.DeadlineExceeded), "database: timeout"},
		{&sqlStateError{"bad code fixture-host", "x"}, "database: error"},
		{errors.New("postgres://fixture_user:" + fixturePassword + "@fixture-host:5432/fixture_db"), "database: error"},
	}
	for _, tc := range cases {
		var gate Gate
		failures := 0
		ctx, cancel := context.WithCancel(t.Context())
		_ = retryDatabase(ctx, func(context.Context) error {
			failures++
			if failures == SustainedFailures {
				cancel()
			}
			return tc.err
		}, func(context.Context, time.Duration) error { return nil }, Retry{Gate: &gate, Secrets: []string{fixturePassword}})
		cancel()
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
		body := rec.Body.String()
		if rec.Code != 503 || !strings.HasSuffix(strings.TrimSpace(body), "last: "+tc.want) {
			t.Fatalf("%v: readyz=%d %q, want class %q", tc.err, rec.Code, body, tc.want)
		}
		assertClassOnly(t, body)
	}
}

// The logs keep the full sanitized reason that /readyz leaves out.
func TestLogsKeepTheReasonReadyzLeavesOut(t *testing.T) {
	var logs bytes.Buffer
	var gate Gate
	err := fmt.Errorf("failed to connect to `user=fixture_user database=fixture_db`: %w",
		&sqlStateError{"28P01", `password authentication failed for user "fixture_user"`})
	ctx, cancel := context.WithCancel(t.Context())
	n := 0
	_ = retryDatabase(ctx, func(context.Context) error {
		n++
		if n == SustainedFailures {
			cancel()
		}
		return err
	}, func(context.Context, time.Duration) error { return nil },
		Retry{Gate: &gate, Logf: func(f string, a ...any) { fmt.Fprintf(&logs, f+"\n", a...) }})
	cancel()
	if !strings.Contains(logs.String(), `SQLSTATE 28P01: failed to connect to `+"`user=fixture_user database=fixture_db`"+`: password authentication failed for user "fixture_user"`) {
		t.Fatalf("log lacks the reason:\n%s", logs.String())
	}
}
