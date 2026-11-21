package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type blackboardHistoryTrace struct{ full, cuts atomic.Int64 }

func (t *blackboardHistoryTrace) TraceQueryStart(c context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "SELECT id,data->'frozen' FROM milestones") {
		t.cuts.Add(1)
	}
	if (strings.Contains(d.SQL, "FROM plan_events WHERE blackboard_id=$1 ORDER BY ordinal") || strings.Contains(d.SQL, "FROM plan_proposals WHERE blackboard_id=$1 ORDER BY ordinal")) && !strings.Contains(d.SQL, "LIMIT") {
		t.full.Add(1)
	}
	return c
}
func (*blackboardHistoryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestBlackboardRound3LifecycleRoutesDoNotReadAgentHistory(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	ctx := context.Background()
	mid := blackboard.NewID()
	id := blackboard.NewID()
	commit, reject := blackboard.ProposalID(blackboard.NewID()), blackboard.ProposalID(blackboard.NewID())
	owner := blackboard.Actor{ID: topicOwner, Kind: "human"}
	agent := blackboard.Actor{ID: "worker", Kind: "agent"}
	r := blackboard.Record{ID: id, Owner: owner, WorkspaceID: fx.sess, Version: 1, Milestones: []blackboard.Milestone{{ID: mid, Owner: owner, Stage: "Finalizing", Frozen: json.RawMessage(`{"cards":[]}`), Version: 1}}, Proposals: []blackboard.Proposal{{ID: commit, Actor: agent, MilestoneID: mid, Status: "pending", Changes: []blackboard.Change{{ID: "commit", Operation: "add", Item: blackboard.Item{Title: "idea", MilestoneID: mid}}}}, {ID: reject, Actor: agent, MilestoneID: mid, Status: "pending"}}, Events: []blackboard.Event{{ID: blackboard.NewID(), Source: "agent", MilestoneID: mid, Kind: "decision"}}}
	if err := fx.api.blackboardStore().Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	trace := &blackboardHistoryTrace{}
	cfg := fx.pool.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	fx.api.dbPool = pool
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	human := fx.token(topicOwner, "human")
	base := "/api/blackboards/" + id
	for _, req := range []struct{ method, path, body string }{
		{"POST", base + "/milestones/" + mid + "/final/abort", `{"revision":0}`},
		{"POST", base + "/commit", fmt.Sprintf(`{"milestone_id":%q,"proposal_id":%q,"revision":0,"selected":["commit"]}`, mid, commit)},
		{"POST", base + "/commit", fmt.Sprintf(`{"milestone_id":%q,"proposal_id":%q,"revision":0,"selected":[]}`, mid, reject)},
		{"POST", base + "/plan", fmt.Sprintf(`{"milestone_id":%q,"revision":1,"changes":[{"operation":"add","item":{"title":"person"}}]}`, mid)},
		{"GET", base + "/milestones/" + mid + "/slice", ""},
		{"POST", base + "/milestones/" + mid + "/final", `{"revision":0}`},
	} {
		request := httptest.NewRequest(req.method, req.path+"?session="+fx.sess, strings.NewReader(req.body))
		request.Header.Set("Authorization", "Bearer "+human)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, request)
		expected := 200
		if strings.HasSuffix(req.path, "/commit") || strings.HasSuffix(req.path, "/plan") || strings.HasSuffix(req.path, "/final") || strings.HasSuffix(req.path, "/final/abort") {
			expected = http.StatusGone
		}
		if w.Code != expected {
			t.Fatalf("%s %s: %d %s", req.method, req.path, w.Code, w.Body.String())
		}
		if trace.full.Load() != 0 {
			t.Fatalf("%s loaded unrelated agent history", req.path)
		}
		if trace.cuts.Load() != 0 {
			t.Fatalf("%s loaded cut evidence through a read or retired lifecycle route", req.path)
		}
	}
}
