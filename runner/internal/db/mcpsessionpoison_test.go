package db

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
	"log/slog"
	"strings"
	"testing"
)

func TestMCPMalformedFactsDoNotBlockOtherBindings(t *testing.T) {
	pool := testsupport.Open(t, "TEST_DATABASE_URL", "mcp-poison-facts", RunMigrations)
	ctx := context.Background()
	// Simulate corrupt historical data that predates the shape guard.
	if _, err := pool.Exec(ctx, `ALTER TABLE mcp_session_bindings DROP CONSTRAINT mcp_session_bindings_terminal_record_check`); err != nil {
		t.Fatal(err)
	}
	scope := MCPSessionScope{AccountID: "00000000-0000-4000-8000-000000000147", RunnerInstallationID: "synthetic-install", RunnerSessionID: "00000000-0000-4000-8000-000000000145", HumanSessionID: "00000000-0000-4000-8000-000000000146", LaunchGeneration: 1, CredentialGeneration: 1, GrantTokenHash: strings.Repeat("a", 64)}
	encoded, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	bad := []string{`null`, `[]`, `[{}]`, `{"private":"poison-fact-canary"}`, `[{"ended_at":"poison-fact-canary"}]`, `[` + strings.TrimSuffix(strings.Repeat(`{},`, 33), ",") + `]`}
	var logs bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer func() { failurelog.Default = previous }()
	for _, raw := range bad {
		if _, err = pool.Exec(ctx, `INSERT INTO mcp_session_bindings(session_id,connection_id,account_id,scope,control_generation,desired_action,terminal_record) VALUES($1,gen_random_uuid(),$2,$3,1,'suspend',$4::jsonb)`, scope.RunnerSessionID, scope.AccountID, encoded, raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO mcp_session_bindings(session_id,connection_id,account_id,scope,control_generation,desired_action,terminal_record) SELECT $1,gen_random_uuid(),$2,$3,1,'suspend','{"private":"poison-fact-canary"}'::jsonb FROM generate_series(1,100)`, scope.RunnerSessionID, scope.AccountID, encoded); err != nil {
		t.Fatal(err)
	}
	var healthy string
	if err = pool.QueryRow(ctx, `INSERT INTO mcp_session_bindings(session_id,connection_id,account_id,scope,control_generation,desired_action,terminal_record) VALUES($1,gen_random_uuid(),$2,$3,1,'suspend','[{"status":"completed","ended_at":"2026-01-01T00:00:00Z","end_reason":"process_exited"}]') RETURNING id::text`, scope.RunnerSessionID, scope.AccountID, encoded).Scan(&healthy); err != nil {
		t.Fatal(err)
	}
	work, err := ListMCPBindingWork(ctx, pool)
	if err != nil {
		t.Fatal("malformed batch halted reconciliation", err)
	}
	if len(work) == 0 {
		work, err = ListMCPBindingWork(ctx, pool)
	}
	if err != nil || len(work) != 1 || work[0].ID != healthy || work[0].Action != "close" {
		t.Fatal("malformed fact blocked healthy terminal cleanup", len(work), err)
	}
	if strings.Contains(logs.String(), "poison-fact-canary") || !strings.Contains(logs.String(), "binding_id") || !strings.Contains(logs.String(), "terminal_facts_decode") || !strings.Contains(logs.String(), "terminal_facts_limit") {
		t.Fatal("malformed row cause was missing or leaked payload")
	}
}
