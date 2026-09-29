package server

// The three ways a session's ending can be got wrong (M-5, M-6, M-7): a
// revivable status treated as final, an expiry verdict applied to a session
// that has since been resumed, and a cluster session the reconciler cannot
// see because its runtime landed in a second write.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// liveBoardTokens counts a session's unrevoked board tokens.
func liveBoardTokens(t *testing.T, pool *pgxpool.Pool, sessionID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM board_tokens WHERE session_id = $1 AND revoked_at IS NULL`,
		sessionID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// M-5: a daemon-reported "error" is revivable, so it must not fire the
// completion webhook (delivery is claimed once per session — a false
// completion burns it) and must not revoke the session's credentials. A
// daemon-reported "stopped" is the real end and does both.
func TestDaemonReportedTerminalStatusFinality(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       string
		wantDelivery bool
		wantLive     int
	}{
		{"revivable error", "error", false, 1},
		{"daemon says stopped", "stopped", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits := make(chan struct{}, 4)
			recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits <- struct{}{}
				w.WriteHeader(http.StatusOK)
			}))
			defer recv.Close()

			api, hub, pool := clusterRunnerAPI(t, &fakeK8s{})
			api.webhookBackoff = []time.Duration{}
			ctx := context.Background()

			const daemonID = "00000000-0000-4000-8000-0000000000c5"
			if err := db.UpsertDaemon(ctx, pool, daemonID, "desktop", "local", "/repos"); err != nil {
				t.Fatal(err)
			}
			sessionID := newUUID()
			if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running",
				"/repos/org/proj", "org/proj", "T", ""); err != nil {
				t.Fatal(err)
			}
			allowLoopbackDelivery(t, recv.URL+"/hook")
			if err := db.SetSessionCallback(ctx, pool, sessionID, recv.URL+"/hook", ""); err != nil {
				t.Fatal(err)
			}
			board, err := db.CreateBoard(ctx, pool, "T", nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := db.MintBoardToken(ctx, pool, board.ID, sessionID, []string{"read"}, time.Hour); err != nil {
				t.Fatal(err)
			}

			if err := HandleSessionStateChanged(ctx, hub, pool, protocol.SessionStateChanged{
				Type: "session_state_changed", SessionID: sessionID, Status: tc.status,
			}); err != nil {
				t.Fatal(err)
			}

			delivered := false
			select {
			case <-hits:
				delivered = true
			case <-time.After(400 * time.Millisecond):
			}
			if delivered != tc.wantDelivery {
				t.Errorf("webhook delivered = %v, want %v", delivered, tc.wantDelivery)
			}
			if got := liveBoardTokens(t, pool, sessionID); got != tc.wantLive {
				t.Errorf("live session tokens = %d, want %d", got, tc.wantLive)
			}
		})
	}
}

// M-6: the 24 h expiry verdict is taken from a row the reconciler read, and a
// resume can land between that read and the write. The write carries the same
// cutoff, so the resumed session survives.
func TestFinishClusterSessionExpiryLosesToAResume(t *testing.T) {
	_, _, pool := clusterRunnerAPI(t, &fakeK8s{})
	ctx := context.Background()
	const daemonID = "00000000-0000-4000-8000-0000000000c6"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	newDisconnected := func() string {
		id := newUUID()
		if err := db.InsertClusterSession(ctx, pool, id, daemonID, "disconnected",
			"/workspace/org/proj", "org/proj", "T", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE sessions SET status_changed_at = now() - interval '48 hours' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	cutoff := time.Now().Add(-24 * time.Hour)

	// Nobody resumed it: the expiry lands.
	stale := newDisconnected()
	updated, err := db.FinishClusterSession(ctx, pool, stale, "error", reasonNotResumed, db.EndReasonNotResumed, &cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Error("an unresumed session past the window was not finalised")
	}

	// Resumed between the reconciler's read and this write (that is exactly
	// what TouchSessionStatusChanged does): the write must find nothing.
	resumed := newDisconnected()
	if err := db.TouchSessionStatusChanged(ctx, pool, resumed); err != nil {
		t.Fatal(err)
	}
	updated, err = db.FinishClusterSession(ctx, pool, resumed, "error", reasonNotResumed, db.EndReasonNotResumed, &cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Error("a session resumed after the verdict was ended anyway")
	}
	row, err := db.GetSession(ctx, pool, resumed)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	if row.Status != "disconnected" || row.EndedAt != nil {
		t.Errorf("resumed session = %q ended_at %v, want a live disconnected row", row.Status, row.EndedAt)
	}

	// Without a cutoff (the Job-state cases) the write is unconditional on the
	// clock, as before.
	fresh := newDisconnected()
	if err := db.TouchSessionStatusChanged(ctx, pool, fresh); err != nil {
		t.Fatal(err)
	}
	if updated, err := db.FinishClusterSession(ctx, pool, fresh, "ended", "", db.EndReasonJobFinished, nil); err != nil || !updated {
		t.Errorf("unconditional finish = %v, %v; want it to land", updated, err)
	}
}

// M-7: a cluster start writes runtime='cluster' with the row, so the
// reconciler — which selects on that column — can never miss a session whose
// pod died before any follow-up write.
func TestClusterStartRecordsRuntimeWithTheRow(t *testing.T) {
	api, _, pool := clusterRunnerAPI(t, &fakeK8s{})
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()

	resp := startReq(t, srv, runnerTestKey, "", map[string]any{"repo": "org/proj", "prompt": "go"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d (%s)", resp.StatusCode, readAllString(t, resp.Body))
	}
	sessionID := decodeSessionID(t, resp)
	resp.Body.Close()

	row, err := db.GetSession(context.Background(), pool, sessionID)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	if derefOrEmpty(row.Runtime) != "cluster" {
		t.Errorf("runtime = %q, want cluster", derefOrEmpty(row.Runtime))
	}

	rows, err := db.ListClusterSessionsToReconcile(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.ID == sessionID {
			found = true
		}
	}
	if !found {
		t.Error("the reconciler cannot see the session it would have to close out")
	}

	// The pod's own session_started upserts the same row; the runtime must
	// survive it.
	HandleSessionStarted(context.Background(), api.hub, pool, newUUID(), protocol.SessionStarted{
		Type: "session_started", SessionID: sessionID, Repo: "org/proj",
		ProjectPath: "/workspace/org/proj", Kind: "agent",
	})
	row, err = db.GetSession(context.Background(), pool, sessionID)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	if derefOrEmpty(row.Runtime) != "cluster" {
		t.Errorf("runtime after session_started = %q, want cluster", derefOrEmpty(row.Runtime))
	}
}
