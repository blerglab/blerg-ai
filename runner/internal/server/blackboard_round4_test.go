package server

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type round4QueryTrace struct{ calls atomic.Int64 }

func (t *round4QueryTrace) TraceQueryStart(c context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	t.calls.Add(1)
	return c
}
func (*round4QueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestBlackboardRound4HTTPAdmissionAfterAuthenticationAndDecode(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	ctx := context.Background()
	owner := blackboard.Actor{ID: topicOwner, Kind: "human"}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.sess, Version: 1}
	if e := fx.api.blackboardStore().Create(ctx, record); e != nil {
		t.Fatal(e)
	}
	trace := &round4QueryTrace{}
	cfg := fx.pool.Config()
	cfg.MaxConns = 2
	cfg.ConnConfig.Tracer = trace
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	fx.api.dbPool = pool
	_, release, e := fx.api.blackboardStore().AdmitAgentWrite(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	request := func(path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	paths := []struct{ path, body string }{{"/api/sessions/" + fx.sess + "/plan?operation=propose", `{"changes":[{"operation":"add","item":{"title":"idea"}}]}`}, {"/api/blackboards/" + record.ID + "/events", `{"kind":"decision"}`}}
	trace.calls.Store(0)
	for _, p := range paths {
		w := request(p.path, fx.sessTok, p.body)
		if w.Code != 409 {
			t.Fatalf("saturated %s: %d %s", p.path, w.Code, w.Body.String())
		}
	}
	if trace.calls.Load() == 0 {
		t.Fatal("saturated agent skipped authentication")
	}
	if w := request(paths[0].path, "invalid-credential", paths[0].body); w.Code != 401 {
		t.Fatalf("admission preceded authentication: %d", w.Code)
	}
	if w := request(paths[0].path, fx.sessTok, "invalid-body"); w.Code != 400 {
		t.Fatalf("admission preceded decoding: %d", w.Code)
	}
	w := request(paths[1].path+"?session="+fx.sess, fx.token(topicOwner, "human"), paths[1].body)
	if w.Code != 201 {
		t.Fatalf("person shared admission: %d %s", w.Code, w.Body.String())
	}
	release()
	release = nil
	for _, p := range paths {
		w := request(p.path, fx.sessTok, p.body)
		if w.Code != 201 {
			t.Fatalf("admission not released/reentered: %d %s", w.Code, w.Body.String())
		}
	}
}
