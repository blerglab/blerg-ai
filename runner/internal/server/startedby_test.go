package server

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// A session a tool starts with an agent token records who started it — the principal's kind
// and the owner's label for the token, asked of core — and the browser sees it as started_by;
// a session the person starts records nothing and has no started_by.
func TestStartedByRecordedForAgentTokenStarts(t *testing.T) {
	ctx := context.Background()
	fx := newMCPFx(t)
	fx.api.fetchTokenName = func(_ context.Context, accountID, tokenID string) (string, error) {
		if accountID != mcpAcct || tokenID != "tok-1" {
			t.Errorf("name lookup for %s/%s, want %s/tok-1", accountID, tokenID, mcpAcct)
		}
		return "literary-agent", nil
	}
	agent := runnerPrincipal{Kind: "agent", Sub: "tok-1", OnBehalfOf: mcpAcct, Sid: testSID, Caps: []string{"session.start"}}
	res, apiErr := fx.api.StartSession(ctx, agent, RunnerStartRequest{Repo: "acme/widget", Prompt: "smoke", Runtime: "cluster"}, "")
	if apiErr != nil {
		t.Fatalf("StartSession: %v", apiErr)
	}
	row, err := db.GetSession(ctx, fx.pool, res.SessionID)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.StartedByKind != "agent" || row.StartedByName != "literary-agent" {
		t.Fatalf("started_by = %q/%q, want agent/literary-agent", row.StartedByKind, row.StartedByName)
	}
	if by := sessionRowToInfo(*row, mcpAcct).StartedBy; by == nil || by.Kind != "agent" || by.Name != "literary-agent" {
		t.Fatalf("SessionInfo.started_by = %+v", by)
	}

	// The person's own start records nothing.
	res, apiErr = fx.api.StartSession(ctx, fx.owner(), RunnerStartRequest{Repo: "acme/widget", Prompt: "mine", Runtime: "cluster"}, "")
	if apiErr != nil {
		t.Fatalf("StartSession (human): %v", apiErr)
	}
	row, _ = db.GetSession(ctx, fx.pool, res.SessionID)
	if row == nil || row.StartedByKind != "" || sessionRowToInfo(*row, mcpAcct).StartedBy != nil {
		t.Fatalf("a person's session carries started_by: %+v", row)
	}

	// A cron's session is a cron's whatever else the row says; a name lookup that fails still
	// records the kind.
	cron := "c-1"
	if by := startedByOf(&db.SessionRow{CronID: &cron, StartedByKind: "agent", StartedByName: "x"}); by == nil || by.Kind != "cron" {
		t.Fatalf("cron row started_by = %+v", by)
	}
	if by := startedByOf(&db.SessionRow{StartedByKind: "runner_key"}); by == nil || by.Kind != "runner_key" || by.Name != "" {
		t.Fatalf("operator-key row started_by = %+v", by)
	}
}
