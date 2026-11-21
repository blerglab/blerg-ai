package server

import (
	"bytes"
	"context"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/jackc/pgx/v5/pgconn"
	"log/slog"
	"strings"
	"testing"
)

func TestTestPerformanceMemberDatabaseFailureIsUnavailable(t *testing.T) {
	f := newWorkFx(t)
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE session_workspaces RENAME TO held_performance_workspaces`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = f.pool.Exec(context.Background(), `ALTER TABLE held_performance_workspaces RENAME TO session_workspaces`)
	}()
	code, _ := f.postWork("test-runs/import", f.sess, f.sessTok, `{"member_id":"repository","revision":1,"record":{}}`)
	if code != 503 {
		t.Fatalf("database failure reported as selection mistake: %d", code)
	}
}

func TestTestPerformanceFailureLogsTypedCauseOnly(t *testing.T) {
	var output bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&output, nil)))
	defer func() { failurelog.Default = old }()
	performanceFailure("test_read_unavailable", &pgconn.PgError{Code: "42P01", Message: "private-canary", Detail: "private-canary"})
	if !strings.Contains(output.String(), "sqlstate_42P01") || strings.Contains(output.String(), "private-canary") {
		t.Fatalf("unsafe or unhelpful cause: %s", &output)
	}
}
