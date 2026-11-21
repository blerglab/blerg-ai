package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/jackc/pgx/v5/pgxpool"
)

type round5DeadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *round5DeadlineWriter) SetReadDeadline(at time.Time) error {
	w.deadlines = append(w.deadlines, at)
	return nil
}

type round5TimedBody struct {
	writer *round5DeadlineWriter
	t      *testing.T
}

func (b round5TimedBody) Read([]byte) (int, error) {
	if len(b.writer.deadlines) != 1 || b.writer.deadlines[0].IsZero() {
		b.t.Error("body read before deadline")
	}
	return 0, os.ErrDeadlineExceeded
}
func (round5TimedBody) Close() error { return nil }
func TestBlackboardRound5BodyReadDeadline(t *testing.T) {
	w := &round5DeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	r := httptest.NewRequest("POST", "/", nil)
	r.Body = round5TimedBody{w, t}
	start := time.Now()
	var body map[string]any
	if decodeBlackboard(w, r, &body) || w.Code != 408 {
		t.Fatalf("deadline outcome: %d", w.Code)
	}
	if len(w.deadlines) != 1 || w.deadlines[0].Before(start) || w.deadlines[0].After(start.Add(6*time.Second)) || !r.Close || w.Header().Get("Connection") != "close" {
		t.Fatalf("timed-out reader lost its deadline/close: %v", w.deadlines)
	}
	w = &round5DeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"ok":true}`))
	if !decodeBlackboard(w, r, &body) || len(w.deadlines) != 2 || !w.deadlines[1].IsZero() {
		t.Fatal("complete body did not reset deadline")
	}
}

type round5StalledBody struct {
	entered chan struct{}
	release <-chan struct{}
	reader  io.Reader
}

func (b *round5StalledBody) Read(p []byte) (int, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return b.reader.Read(p)
}
func (*round5StalledBody) Close() error { return nil }
func TestBlackboardRound5StalledBodyHoldsNoPermit(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	ctx := context.Background()
	cfg := fx.pool.Config()
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	fx.api.dbPool = pool
	owner := blackboard.Actor{ID: topicOwner, Kind: "human"}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.sess, Version: 1}
	if err = fx.api.blackboardStore().Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	release := make(chan struct{})
	body := &round5StalledBody{make(chan struct{}, 1), release, strings.NewReader(`{"changes":[{"operation":"add","item":{"title":"idea"}}]}`)}
	r := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/plan?blackboard="+record.ID, nil)
	r.Body = body
	r.Header.Set("Authorization", "Bearer "+fx.sessTok)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); mux.ServeHTTP(w, r); done <- w }()
	defer func() {
		close(release)
		w := <-done
		if w.Code != 201 {
			t.Errorf("stalled request after release: %d %s", w.Code, w.Body.String())
		}
	}()
	guard, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case <-body.entered:
	case <-guard.Done():
		t.Fatal("request never entered body decode")
	}
	other := httptest.NewRequest("POST", "/api/blackboards/"+record.ID+"/events", strings.NewReader(`{"kind":"decision"}`))
	child := newChildOf(t, fx)
	other.Header.Set("Authorization", "Bearer "+child.tok)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, other)
	if w.Code != 201 {
		t.Fatalf("stalled body consumed another agent's permit: %d %s", w.Code, w.Body.String())
	}
}
func TestBlackboardRound5CommitAssertionRecordedAndLogged(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	ctx := context.Background()
	owner := blackboard.Actor{ID: topicOwner, Kind: "human"}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.sess, Version: 1}
	if err := fx.api.blackboardStore().Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(buf, nil)))
	defer func() { failurelog.Default = old }()
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	canary := "private-assertion-canary"
	r := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/plan?operation=commit&blackboard="+record.ID+"&reason="+canary, strings.NewReader(canary))
	r.Header.Set("Authorization", "Bearer "+fx.sessTok)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("asserted commit status %d", w.Code)
	}
	got, err := fx.api.blackboardStore().Load(ctx, record.ID)
	if err != nil || len(got.Events) != 1 || got.Events[0].Kind != "assertion" || got.Events[0].Source != "agent" || got.Events[0].AuthenticatedActor.ID != fx.sess || got.PlanRevision != 0 || len(got.Items) != 0 || len(got.Proposals) != 0 {
		t.Fatalf("refused assertion not persisted independently: %v", err)
	}
	var payload map[string]any
	if err = json.Unmarshal(got.Events[0].Payload, &payload); err != nil || payload["accepted"] != false {
		t.Fatal("assertion claimed authority")
	}
	if !strings.Contains(buf.String(), `"stage":"commit_assertion"`) || strings.Contains(buf.String(), canary) {
		t.Fatalf("missing/private assertion diagnostic: %s", buf.String())
	}
}

type round5StalledGateway struct {
	blackboardGateway
	session string
	entered chan struct{}
	release <-chan struct{}
}

func (b *round5StalledGateway) CallBoard(ctx context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	if c.SessionRef == b.session && c.Tool == "blerg_board_get" {
		select {
		case b.entered <- struct{}{}:
		default:
		}
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.blackboardGateway.CallBoard(ctx, c)
}
func TestBlackboardRound5GatewayHoldsNoWritePermit(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	ctx := context.Background()
	human := fx.token(topicOwner, "human")
	if code, body := setLevel(fx, human, "board-a", "read"); code != 200 {
		t.Fatalf("grant: %d %s", code, body)
	}
	cfg := fx.pool.Config()
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	fx.api.dbPool = pool
	owner := blackboard.Actor{ID: topicOwner, Kind: "human"}
	blocked := blackboard.Record{ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.sess, Version: 1, Links: []blackboard.Link{{WorkspaceID: fx.sess, BoardID: "board-a"}}}
	other := blackboard.Record{ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.sess, Version: 1}
	for _, r := range []blackboard.Record{blocked, other} {
		if err = fx.api.blackboardStore().Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	release := make(chan struct{})
	gateway := &round5StalledGateway{session: fx.sess, entered: make(chan struct{}, 1), release: release}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: gateway})
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	request := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+fx.sessTok)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- request("/api/sessions/"+fx.sess+"/plan?blackboard="+blocked.ID, `{"changes":[{"operation":"add","item":{"title":"idea"}}]}`)
	}()
	defer func() {
		close(release)
		w := <-done
		if w.Code != 201 {
			t.Errorf("gateway request after release: %d %s", w.Code, w.Body.String())
		}
	}()
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case <-gateway.entered:
	case <-c.Done():
		t.Fatal("gateway not reached")
	}
	w := request("/api/blackboards/"+other.ID+"/events", `{"kind":"decision"}`)
	if w.Code != 201 {
		t.Fatalf("gateway consumed another agent's write permit: %d %s", w.Code, w.Body.String())
	}
}
func TestBlackboardRound5PublicReadsArePagedInSQL(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	ctx := context.Background()
	owner := blackboard.Actor{ID: topicOwner, Kind: "human"}
	mid := blackboard.NewID()
	record := blackboard.Record{ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.sess, Version: 1, Milestones: []blackboard.Milestone{{ID: mid, Owner: owner, Version: 1, Frozen: json.RawMessage(`{"large":"` + strings.Repeat("x", 2<<20) + `"}`)}}}
	for i := 0; i < 120; i++ {
		record.Events = append(record.Events, blackboard.Event{ID: fmt.Sprintf("event-%03d", i), MilestoneID: mid, Kind: "decision", Source: "agent", AuthenticatedActor: blackboard.Actor{ID: fx.sess, Kind: "agent"}, Payload: json.RawMessage(`{"text":"legacy"}`)})
	}
	if err := fx.api.blackboardStore().Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	trace := &blackboardHistoryTrace{}
	cfg := fx.pool.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	fx.api.dbPool = pool
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	for _, path := range []string{"/api/blackboards/" + record.ID + "?session=" + fx.sess + "&limit=1&cursor=100", "/api/sessions/" + fx.sess + "/plan?blackboard=" + record.ID + "&operation=history&limit=1&cursor=100", "/api/blackboards/" + record.ID + "/milestones/" + mid + "/timeline?session=" + fx.sess + "&limit=1&cursor=100"} {
		token := fx.token(topicOwner, "human")
		if strings.Contains(path, "/api/sessions/") {
			token = fx.sessTok
		}
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "event-100") || strings.Contains(w.Body.String(), "event-099") {
			t.Fatalf("public SQL page: %d %s", w.Code, w.Body.String())
		}
		if trace.full.Load() != 0 || trace.cuts.Load() != 0 {
			t.Fatal("public read loaded unbounded history or cut")
		}
	}
}

func TestBlackboardRound5DecodeConsumesWholeBody(t *testing.T) {
	for _, body := range []string{`{"ok":true}{"extra":true}`, `{"ok":true}` + strings.Repeat(" ", 128<<10)} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		var value map[string]any
		if decodeBlackboard(w, r, &value) || w.Code != 400 || !r.Close {
			t.Fatal("trailing body bypassed size/EOF check")
		}
	}
}
