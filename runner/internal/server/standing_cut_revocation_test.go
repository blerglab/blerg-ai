package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func TestStandingCutReconciliationRetainsLocalRevocationFence(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelRead)
	state, err := db.LatestBoardGrant(t.Context(), fx.pool, fx.sess, "board-a")
	if err != nil || state == nil {
		t.Fatalf("grant=%v err=%v", state, err)
	}
	proof := mcpgw.Proof{AccountID: state.Grant.AccountID, SessionID: state.Grant.ProofSessionID}
	record := blackboard.Record{WorkspaceID: fx.sess, Owner: blackboard.Actor{ID: topicOwner, Kind: "human"}}
	check := func() error {
		tx, err := fx.pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(t.Context()) }()
		return fx.api.lockCutReadProof(t.Context(), tx, record, "board-a", proof)
	}
	if err = check(); err != nil {
		t.Fatalf("live proof refused: %v", err)
	}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/revocations" {
			_ = json.NewEncoder(w).Encode([]map[string]string{{"kind": "sid", "value": proof.SessionID}})
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer core.Close()
	fx.api.coreAuth = ownedCoreAuthClient(t, core.URL)
	if err = check(); err == nil {
		t.Fatal("locally revoked original login passed the reconciliation fence")
	}
}
