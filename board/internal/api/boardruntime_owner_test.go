package api

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
	"github.com/blerglab/blerg-ai/board/internal/testsupport"
)

type observedBoardStart struct {
	runner.Driver
	called bool
}

func (*observedBoardStart) RequiresStartIdentity() bool { return true }
func (d *observedBoardStart) Start(context.Context, runner.StartRequest) (string, error) {
	d.called = true
	return "fixture-session", nil
}

func TestBoardRuntimeOverrideCannotUseAnotherOwnersCredential(t *testing.T) {
	pool := testsupport.Pool(t, db.RunMigrations)
	if err := db.SetAutomationKey([]byte("0123456789abcdef0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.SetAutomationKey(nil) })
	board, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Target ownership"})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetBoardAutomationToken(t.Context(), pool, board.ID, db.AutomationTokenUpdate{Token: "fixture-current-credential", AccountID: "current-owner"}); err != nil {
		t.Fatal(err)
	}
	driver := &observedBoardStart{}
	a := New(pool, nil, nil)
	a.SetRunner(RunnerConfig{Driver: driver})
	// The old owner authorized this target before the board's automation identity changed.
	_, _, err = a.startOnRunnerConfigured(t.Context(), board.ID, runner.StartRequest{Runtime: "docker", DaemonID: "prior-desktop", TargetAccount: "prior-owner"})
	if err == nil || driver.called {
		t.Fatal("a stale target override received the new owner's credential")
	}
}
