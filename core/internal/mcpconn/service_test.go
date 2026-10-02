package mcpconn_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

const testSchema = "test_blerg_core_mcpconn"

const goodURL = "https://mcp.example.com/mcp"

const sessA = "00000000-0000-4000-8000-000000000001"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	setup, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", testSchema)); err != nil {
		setup.Close()
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := setup.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", testSchema)); err != nil {
		setup.Close()
		t.Fatalf("create schema: %v", err)
	}
	setup.Close()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = testSchema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", testSchema))
		pool.Close()
	})
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

var acctSeq int64

func newAccount(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	subject := fmt.Sprintf("mcpconn-%d", atomic.AddInt64(&acctSeq, 1))
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO accounts (provider, provider_subject, email, role) VALUES ('local',$1,$1||'@example.com','member') RETURNING id::text`,
		subject).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func newSvc(t *testing.T) (*mcpconn.Service, *pgxpool.Pool) {
	t.Helper()
	pool := testPool(t)
	backend, err := keybackend.NewLocal(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	pol := netguard.Policy{AllowHTTPHosts: []string{"mcp.internal.test"}}
	return mcpconn.NewService(db.NewPgStore(pool), backend, pol), pool
}

func static(name, secret string) mcpconn.CreateInput {
	return mcpconn.CreateInput{Name: name, URL: goodURL, AuthKind: "static", Secret: secret}
}

func wantValidation(t *testing.T, err error, contains string) {
	t.Helper()
	var ve *mcpconn.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *ValidationError containing %q", err, contains)
	}
	if !strings.Contains(ve.Msg, contains) {
		t.Fatalf("validation msg %q does not contain %q", ve.Msg, contains)
	}
}

func TestCreateValidationMatrix(t *testing.T) {
	svc, pool := newSvc(t)
	acct := newAccount(t, pool)
	ctx := context.Background()
	long := strings.Repeat("a", 33)
	bigValue := strings.Repeat("v", 4097)

	cases := []struct {
		name string
		in   mcpconn.CreateInput
		msg  string
	}{
		{"empty name", mcpconn.CreateInput{Name: "", URL: goodURL, AuthKind: "none"}, "name"},
		{"upper case", mcpconn.CreateInput{Name: "Notes", URL: goodURL, AuthKind: "none"}, "name"},
		{"underscore", mcpconn.CreateInput{Name: "my_notes", URL: goodURL, AuthKind: "none"}, "name"},
		{"leading dash", mcpconn.CreateInput{Name: "-notes", URL: goodURL, AuthKind: "none"}, "name"},
		{"too long", mcpconn.CreateInput{Name: long, URL: goodURL, AuthKind: "none"}, "name"},
		{"reserved board", mcpconn.CreateInput{Name: "board", URL: goodURL, AuthKind: "none"}, "reserved"},
		{"reserved blerg", mcpconn.CreateInput{Name: "blerg", URL: goodURL, AuthKind: "none"}, "reserved"},
		{"reserved gateway", mcpconn.CreateInput{Name: "gateway", URL: goodURL, AuthKind: "none"}, "reserved"},
		{"http url", mcpconn.CreateInput{Name: "a", URL: "http://mcp.example.com/mcp", AuthKind: "none"}, "url"},
		{"userinfo url", mcpconn.CreateInput{Name: "a", URL: "https://u:p@mcp.example.com/", AuthKind: "none"}, "url"},
		{"query string", mcpconn.CreateInput{Name: "a", URL: "https://mcp.example.com/mcp?key=SECRET", AuthKind: "none"}, "query"},
		{"empty query marker", mcpconn.CreateInput{Name: "a", URL: "https://mcp.example.com/mcp?", AuthKind: "none"}, "query"},
		{"fragment", mcpconn.CreateInput{Name: "a", URL: "https://mcp.example.com/mcp#frag", AuthKind: "none"}, "fragment"},
		{"literal metadata ip", mcpconn.CreateInput{Name: "a", URL: "https://169.254.169.254/latest", AuthKind: "none"}, "url"},
		{"literal rfc1918 ip", mcpconn.CreateInput{Name: "a", URL: "https://10.0.0.5/mcp", AuthKind: "none"}, "url"}, // scrub:allow (a range the guard must refuse)
		{"file url", mcpconn.CreateInput{Name: "a", URL: "file:///etc/passwd", AuthKind: "none"}, "url"},
		{"empty url", mcpconn.CreateInput{Name: "a", URL: "", AuthKind: "none"}, "url"},
		{"unknown kind", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "basic"}, "auth_kind"},
		{"oauth not yet", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "oauth"}, "oauth"},
		{"none with secret", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "none", Secret: "x"}, "secret"},
		{"none with header", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "none", HeaderName: "X-Key"}, "header_name"},
		{"static without secret", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "static"}, "secret"},
		{"header not a token", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "static", HeaderName: "X Key", Secret: "s"}, "header_name"},
		{"header with colon", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "static", HeaderName: "X-Key:", Secret: "s"}, "header_name"},
		{"header forbidden host", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "static", HeaderName: "host", Secret: "s"}, "header_name"},
		{"header forbidden cookie", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "static", HeaderName: "Cookie", Secret: "s"}, "header_name"},
		{"header forbidden session", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "static", HeaderName: "MCP-Session-Id", Secret: "s"}, "header_name"},
		{"header forbidden content-type", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "static", HeaderName: "Content-Type", Secret: "s"}, "header_name"},
		{"value with CR", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "static", Secret: "a\rb"}, "secret"},
		{"value with LF", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "static", Secret: "a\nb"}, "secret"},
		{"value with NUL", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "static", Secret: "a\x00b"}, "secret"},
		{"value too big", mcpconn.CreateInput{Name: "a", URL: goodURL, AuthKind: "static", Secret: bigValue}, "secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(ctx, acct, tc.in)
			wantValidation(t, err, tc.msg)
		})
	}
	list, err := svc.List(ctx, acct)
	if err != nil || len(list) != 0 {
		t.Fatalf("nothing should have been stored: %+v %v", list, err)
	}
}

func TestCreateAcceptsGoodInputs(t *testing.T) {
	svc, pool := newSvc(t)
	acct := newAccount(t, pool)
	ctx := context.Background()
	for _, tc := range []mcpconn.CreateInput{
		{Name: "a", URL: goodURL, AuthKind: "none"},
		{Name: "notes-2", URL: goodURL, AuthKind: "static", Secret: "Bearer tok"},
		{Name: strings.Repeat("z", 32), URL: goodURL, AuthKind: "static", HeaderName: "X-Api-Key", Secret: "k"},
		{Name: "lan", URL: "http://mcp.internal.test:8080/mcp", AuthKind: "none"}, // allow-listed http host
		{Name: "tabbed", URL: goodURL, AuthKind: "static", Secret: "a\tb"},
	} {
		if _, err := svc.Create(ctx, acct, tc); err != nil {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func TestSummaryNeverCarriesSecret(t *testing.T) {
	svc, pool := newSvc(t)
	acct := newAccount(t, pool)
	ctx := context.Background()
	c, err := svc.Create(ctx, acct, static("notes", "super-secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	if c.HeaderName != "Authorization" || !c.HasSecret || c.Status != "ok" || c.AuthKind != "static" {
		t.Fatalf("unexpected connection %+v", c)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "super-secret-value") {
		t.Fatalf("connection marshals the secret: %s", raw)
	}
	list, _ := svc.List(ctx, acct)
	raw, _ = json.Marshal(list)
	if strings.Contains(string(raw), "super-secret-value") {
		t.Fatalf("list marshals the secret: %s", raw)
	}
}

func TestEncryptionRoundTripAndAtRest(t *testing.T) {
	svc, pool := newSvc(t)
	acct := newAccount(t, pool)
	ctx := context.Background()
	c, err := svc.Create(ctx, acct, static("notes", "Bearer s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	var ct []byte
	var keyID string
	if err := pool.QueryRow(ctx, `SELECT secret_ciphertext, key_id FROM mcp_connections WHERE id=$1`, c.ID).Scan(&ct, &keyID); err != nil {
		t.Fatal(err)
	}
	if len(ct) == 0 || keyID == "" || bytes.Contains(ct, []byte("s3cret")) {
		t.Fatalf("secret is not encrypted at rest (len=%d key=%q)", len(ct), keyID)
	}
	res, err := svc.FetchSecret(ctx, acct, c.ID, mcpconn.Proof{SessionID: sessA})
	if err != nil {
		t.Fatal(err)
	}
	if res.Value != "Bearer s3cret" || res.HeaderName != "Authorization" || res.URL != goodURL || res.ExpiresAt != nil {
		t.Fatalf("FetchSecret = %+v", res)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM mcp_secret_access_log WHERE account_id=$1 AND connection_id=$2 AND fetched_by_session_id IS NOT NULL AND fetched_by_token_id IS NULL`, acct, c.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
}

func TestFetchSecretAuditFailureAborts(t *testing.T) {
	svc, pool := newSvc(t)
	acct := newAccount(t, pool)
	ctx := context.Background()
	c, _ := svc.Create(ctx, acct, static("notes", "tok"))
	if _, err := pool.Exec(ctx, `DROP TABLE mcp_secret_access_log`); err != nil {
		t.Fatal(err)
	}
	res, err := svc.FetchSecret(ctx, acct, c.ID, mcpconn.Proof{TokenID: "00000000-0000-4000-8000-000000000002"})
	if err == nil || errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatalf("want a hard error, got %v", err)
	}
	if res.Value != "" {
		t.Fatal("secret returned despite the audit failure")
	}
}

func TestFetchSecretNoneHasNoValue(t *testing.T) {
	svc, pool := newSvc(t)
	acct := newAccount(t, pool)
	ctx := context.Background()
	c, _ := svc.Create(ctx, acct, mcpconn.CreateInput{Name: "open", URL: goodURL, AuthKind: "none"})
	res, err := svc.FetchSecret(ctx, acct, c.ID, mcpconn.Proof{SessionID: sessA})
	if err != nil || res.URL != goodURL || res.Value != "" || res.HeaderName != "" {
		t.Fatalf("none fetch = %+v, %v", res, err)
	}
}

func TestScopedToAccount(t *testing.T) {
	svc, pool := newSvc(t)
	a, b := newAccount(t, pool), newAccount(t, pool)
	ctx := context.Background()
	c, _ := svc.Create(ctx, a, static("notes", "tok"))
	if _, err := svc.Get(ctx, b, c.ID); !errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatalf("Get other = %v", err)
	}
	newName := "x"
	if _, err := svc.Patch(ctx, b, c.ID, mcpconn.PatchInput{Name: &newName}); !errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatalf("Patch other = %v", err)
	}
	if err := svc.Delete(ctx, b, c.ID); !errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatalf("Delete other = %v", err)
	}
	if _, err := svc.FetchSecret(ctx, b, c.ID, mcpconn.Proof{SessionID: sessA}); !errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatalf("FetchSecret other = %v", err)
	}
	if l, _ := svc.List(ctx, b); len(l) != 0 {
		t.Fatalf("b sees %d connections", len(l))
	}
	// A malformed id is just "not found".
	if _, err := svc.Get(ctx, a, "not-a-uuid"); !errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatalf("Get bad id = %v", err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM mcp_secret_access_log`).Scan(&n)
	if n != 0 {
		t.Fatalf("a cross-account fetch wrote %d audit rows", n)
	}
}

func TestLimitPerAccount(t *testing.T) {
	svc, pool := newSvc(t)
	a, b := newAccount(t, pool), newAccount(t, pool)
	ctx := context.Background()
	for i := 0; i < mcpconn.MaxPerAccount; i++ {
		if _, err := svc.Create(ctx, a, mcpconn.CreateInput{Name: fmt.Sprintf("c%d", i), URL: goodURL, AuthKind: "none"}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := svc.Create(ctx, a, mcpconn.CreateInput{Name: "over", URL: goodURL, AuthKind: "none"}); !errors.Is(err, mcpconn.ErrLimit) {
		t.Fatalf("21st = %v, want ErrLimit", err)
	}
	if _, err := svc.Create(ctx, b, mcpconn.CreateInput{Name: "fine", URL: goodURL, AuthKind: "none"}); err != nil {
		t.Fatalf("other account is limited too: %v", err)
	}
	// Deleting frees a slot.
	l, _ := svc.List(ctx, a)
	if err := svc.Delete(ctx, a, l[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, a, mcpconn.CreateInput{Name: "again", URL: goodURL, AuthKind: "none"}); err != nil {
		t.Fatalf("after delete: %v", err)
	}
}

func TestLimitHoldsUnderConcurrency(t *testing.T) {
	svc, pool := newSvc(t)
	a := newAccount(t, pool)
	ctx := context.Background()
	done := make(chan error, 30)
	for i := 0; i < 30; i++ {
		go func() {
			_, err := svc.Create(ctx, a, mcpconn.CreateInput{Name: fmt.Sprintf("p%d", i), URL: goodURL, AuthKind: "none"})
			done <- err
		}()
	}
	for i := 0; i < 30; i++ {
		<-done
	}
	l, _ := svc.List(ctx, a)
	if len(l) != mcpconn.MaxPerAccount {
		t.Fatalf("stored %d, want exactly %d", len(l), mcpconn.MaxPerAccount)
	}
}

func TestDuplicateNameConflicts(t *testing.T) {
	svc, pool := newSvc(t)
	a, b := newAccount(t, pool), newAccount(t, pool)
	ctx := context.Background()
	if _, err := svc.Create(ctx, a, static("notes", "x")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, a, static("notes", "y")); !errors.Is(err, mcpconn.ErrNameTaken) {
		t.Fatalf("dup = %v", err)
	}
	if _, err := svc.Create(ctx, b, static("notes", "y")); err != nil {
		t.Fatalf("same name on another account: %v", err)
	}
}

func TestToolPrefixNormalisation(t *testing.T) {
	// The rule mirrors the runner's mcpToolPrefix: anything outside [A-Za-z0-9_-] becomes "_".
	if got := mcpconn.NormalizeName("a.b c"); got != "a_b_c" {
		t.Fatalf("NormalizeName = %q", got)
	}
	if got := mcpconn.NormalizeName("ok-name_1"); got != "ok-name_1" {
		t.Fatalf("NormalizeName = %q", got)
	}
	// The slug alphabet is a subset of the safe set, so a valid slug always normalises to itself
	// and two valid slugs can only collide when equal (also enforced by UNIQUE).
	svc, pool := newSvc(t)
	a := newAccount(t, pool)
	if _, err := svc.Create(context.Background(), a, static("a-b", "x")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(context.Background(), a, static("a-b", "x")); !errors.Is(err, mcpconn.ErrNameTaken) {
		t.Fatalf("collision = %v", err)
	}
}

func TestPatch(t *testing.T) {
	svc, pool := newSvc(t)
	a := newAccount(t, pool)
	ctx := context.Background()
	c, _ := svc.Create(ctx, a, static("notes", "old"))
	if _, err := svc.Create(ctx, a, static("other", "x")); err != nil {
		t.Fatal(err)
	}

	rename := "renamed"
	got, err := svc.Patch(ctx, a, c.ID, mcpconn.PatchInput{Name: &rename})
	if err != nil || got.Name != "renamed" {
		t.Fatalf("rename = %+v %v", got, err)
	}
	taken := "other"
	if _, err := svc.Patch(ctx, a, c.ID, mcpconn.PatchInput{Name: &taken}); !errors.Is(err, mcpconn.ErrNameTaken) {
		t.Fatalf("rename onto other = %v", err)
	}
	bad := "board"
	_, err = svc.Patch(ctx, a, c.ID, mcpconn.PatchInput{Name: &bad})
	wantValidation(t, err, "reserved")

	// Replace the secret: the new one is what is handed out.
	newSecret := "brand-new"
	if _, err := svc.Patch(ctx, a, c.ID, mcpconn.PatchInput{Secret: &newSecret}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.FetchSecret(ctx, a, c.ID, mcpconn.Proof{SessionID: sessA})
	if err != nil || res.Value != "brand-new" {
		t.Fatalf("after replace = %+v %v", res, err)
	}
	badSecret := "x\ny"
	_, err = svc.Patch(ctx, a, c.ID, mcpconn.PatchInput{Secret: &badSecret})
	wantValidation(t, err, "secret")

	// default_tools
	tools := json.RawMessage(`{"search":{"mode":"allow","hash":"abc"},"send":{"mode":"propose","hash":"def"}}`)
	got, err = svc.Patch(ctx, a, c.ID, mcpconn.PatchInput{DefaultTools: tools})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]map[string]string
	if err := json.Unmarshal(got.DefaultTools, &back); err != nil || back["send"]["mode"] != "propose" {
		t.Fatalf("default_tools = %s (%v)", got.DefaultTools, err)
	}
	for _, badTools := range []string{
		`[]`, `null`, `"x"`, `{"t":{"mode":"off","hash":"h"}}`, `{"t":{"mode":"allow"}}`, `{"t":"allow"}`,
		`{"t":{"mode":"allow","hash":"h","extra":1}}`, `{"":{"mode":"allow","hash":"h"}}`,
	} {
		_, err := svc.Patch(ctx, a, c.ID, mcpconn.PatchInput{DefaultTools: json.RawMessage(badTools)})
		wantValidation(t, err, "default_tools")
	}

	// An empty patch is a validation error; a secret on a none connection is too.
	_, err = svc.Patch(ctx, a, c.ID, mcpconn.PatchInput{})
	wantValidation(t, err, "nothing")
	open, _ := svc.Create(ctx, a, mcpconn.CreateInput{Name: "open", URL: goodURL, AuthKind: "none"})
	_, err = svc.Patch(ctx, a, open.ID, mcpconn.PatchInput{Secret: &newSecret})
	wantValidation(t, err, "secret")
}

func TestCascadeOnAccountDelete(t *testing.T) {
	svc, pool := newSvc(t)
	a := newAccount(t, pool)
	ctx := context.Background()
	if _, err := svc.Create(ctx, a, static("notes", "x")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM accounts WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM mcp_connections WHERE account_id=$1`, a).Scan(&n)
	if n != 0 {
		t.Fatalf("%d connections survived account deletion", n)
	}
}

func TestPruneAccessLog(t *testing.T) {
	svc, pool := newSvc(t)
	a := newAccount(t, pool)
	ctx := context.Background()
	for _, age := range []string{"200 days", "181 days", "179 days", "1 day"} {
		if _, err := pool.Exec(ctx, `INSERT INTO mcp_secret_access_log (account_id, fetched_at) VALUES ($1, now() - $2::interval)`, a, age); err != nil {
			t.Fatal(err)
		}
	}
	n, err := svc.PruneAccessLog(ctx, mcpconn.AccessLogRetention)
	if err != nil || n != 2 {
		t.Fatalf("pruned %d, %v; want 2", n, err)
	}
	var left int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM mcp_secret_access_log`).Scan(&left)
	if left != 2 {
		t.Fatalf("left %d, want 2", left)
	}
	if mcpconn.AccessLogRetention != 180*24*time.Hour {
		t.Fatalf("retention = %v", mcpconn.AccessLogRetention)
	}
}
