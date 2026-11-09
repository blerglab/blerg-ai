package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"runtime/pprof"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInsightsActivityOwnerScope(t *testing.T) {
	fx := newTopicFx(t)
	api, board, _ := newLiveCardAPI(t)
	api.dbPool, api.coreAuth = fx.pool, fx.api.coreAuth
	_, err := fx.pool.Exec(context.Background(), `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload) VALUES($1,1,$2,'mark','{"kind":"review","text":"private summary","by":"codex","board_id":"board-a","card":7,"verdict":"approved","target":"abcdef1","round":2,"findings":["high","high"]}')`, fx.sess, newUUID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fx.pool.Exec(context.Background(), `WITH m AS (INSERT INTO messages(session_id,kind,body,status) VALUES($1,'ask','private failure summary','answered') RETURNING id) INSERT INTO session_failure_alerts(session_id,occurrence,turn_seq,message_id) SELECT $1,'turn:42',42,id FROM m`, fx.sess); err != nil {
		t.Fatal(err)
	}
	get := func(account, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/insights/activity?range=7d"+query, nil)
		req.Header.Set("Authorization", "Bearer "+fx.token(account, "human"))
		w := httptest.NewRecorder()
		started := time.Now()
		api.HandleInsightsActivity(w, req)
		t.Logf("7d activity API: %s", time.Since(started))
		return w
	}
	own := get(topicOwner, "")
	if own.Code != 200 || !strings.Contains(own.Body.String(), `"round":2`) || strings.Contains(own.Body.String(), "private summary") || strings.Contains(own.Body.String(), "private failure summary") {
		t.Fatalf("owner projection: %d %s", own.Code, own.Body.String())
	}
	if !strings.Contains(own.Body.String(), `"occurrence":"turn:42"`) {
		t.Fatalf("automatic failure omitted: %s", own.Body.String())
	}
	if _, err = fx.pool.Exec(context.Background(), `INSERT INTO messages(session_id,kind,body,status) VALUES($1,'ask','secret ordinary question','answered')`, fx.sess); err != nil {
		t.Fatal(err)
	}
	questions := get(topicOwner, "")
	var questionReport insightActivityReport
	if err := json.Unmarshal(questions.Body.Bytes(), &questionReport); err != nil || len(questionReport.Questions) != 1 || questionReport.Questions[0].Asked != 1 || questionReport.Questions[0].ReachedPerson != 1 || strings.Contains(questions.Body.String(), "secret ordinary question") {
		t.Fatalf("question projection: %s %v", questions.Body.String(), err)
	}
	var report struct {
		Scope    string `json:"scope"`
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(own.Body.Bytes(), &report); err != nil || report.Scope != "mine" || len(report.Sessions) != 2 {
		t.Fatalf("report: %s %v", own.Body.String(), err)
	}
	if report.Sessions[0].ID == report.Sessions[1].ID || (report.Sessions[0].ID != fx.sess && report.Sessions[1].ID != fx.sess) {
		t.Fatalf("owned linked/unlinked sessions: %s", own.Body.String())
	}
	adminReq := httptest.NewRequest("GET", "/api/insights/activity?scope=all", nil)
	adminReq.Header.Set("Authorization", "Bearer "+fx.token(topicOther, "human", "account.manage"))
	adminOut := httptest.NewRecorder()
	api.HandleInsightsActivity(adminOut, adminReq)
	if adminOut.Code != 200 || strings.Contains(adminOut.Body.String(), `"round":2`) {
		t.Fatalf("admin widened owner scope: %d %s", adminOut.Code, adminOut.Body.String())
	}
	other := get(topicOther, "")
	if other.Code != 200 || strings.Contains(other.Body.String(), `"round":2`) {
		t.Fatalf("other owner: %d %s", other.Code, other.Body.String())
	}
	denied := get(topicOther, "&workspace="+fx.sess)
	if denied.Code != 404 {
		t.Fatalf("foreign workspace: %d", denied.Code)
	}
	agentReq := httptest.NewRequest("GET", "/api/insights/activity", nil)
	agentReq.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "agent"))
	agentOut := httptest.NewRecorder()
	api.HandleInsightsActivity(agentOut, agentReq)
	if agentOut.Code != 403 {
		t.Fatalf("agent credential accepted: %d", agentOut.Code)
	}
	board.mu.Lock()
	board.refuse = true
	board.mu.Unlock()
	refused := get(topicOwner, "")
	if refused.Code != 200 || strings.Contains(refused.Body.String(), `"round":2`) || !strings.Contains(refused.Body.String(), `"omitted_cards":1`) {
		t.Fatalf("revoked board: %d %s", refused.Code, refused.Body.String())
	}
	var refusedReport insightActivityReport
	if err := json.Unmarshal(refused.Body.Bytes(), &refusedReport); err != nil {
		t.Fatal(err)
	}
	foundUnknown := false
	for _, s := range refusedReport.Sessions {
		if s.ID == fx.sess {
			foundUnknown = s.AttributionUnknown && len(s.Cards) == 0
		}
	}
	if !foundUnknown {
		t.Fatal("omitted card attribution was silently labeled unlinked")
	}
	if len(refusedReport.Questions) != 1 || !refusedReport.Questions[0].AttributionUnknown || len(refusedReport.Questions[0].Cards) != 0 {
		t.Fatalf("question card permission: %+v", refusedReport.Questions)
	}

}

func TestInsightsActivityWorkspaceLifetime(t *testing.T) {
	fx := newTopicFx(t)
	api, _, _ := newLiveCardAPI(t)
	api.dbPool, api.coreAuth = fx.pool, fx.api.coreAuth
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET started_at=now()-interval '120 days' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/insights/activity?range=all&workspace="+fx.sess, nil)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	w := httptest.NewRecorder()
	api.HandleInsightsActivity(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), fx.sess) {
		t.Fatalf("lifetime workspace scope: %d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("GET", "/api/insights/activity?range=all", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	w = httptest.NewRecorder()
	api.HandleInsightsActivity(w, req)
	if w.Code != 400 {
		t.Fatalf("unbounded global report accepted: %d", w.Code)
	}
}

func TestInsightsActivityOldSessionRecentEvidence(t *testing.T) {
	fx := newTopicFx(t)
	api, _, _ := newLiveCardAPI(t)
	api.dbPool, api.coreAuth = fx.pool, fx.api.coreAuth
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET started_at=now()-interval '120 days' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(context.Background(), `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload) VALUES($1,1,$2,'mark','{"kind":"failure","board_id":"board-a","card":7,"type":"test","severity":"high"}')`, fx.sess, newUUID()); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/insights/activity?range=24h", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	w := httptest.NewRecorder()
	api.HandleInsightsActivity(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"type":"test"`) {
		t.Fatalf("recent failure from old Main missing: %d %s", w.Code, w.Body.String())
	}
}

type insightsReviewBoard struct {
	*liveCardBoard
	stopAfterFirst bool
	reads          atomic.Int64
	events         bool
}

func (b *insightsReviewBoard) CallBoard(ctx context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	if c.Tool == "blerg_board_get" {
		b.reads.Add(1)
		var args struct {
			From    time.Time `json:"events_from"`
			To      time.Time `json:"events_to"`
			Numbers []int     `json:"card_numbers"`
		}
		if err := json.Unmarshal(c.Arguments, &args); err != nil {
			return nil, err
		}
		if !c.ReadOnly || c.BoardID != "board-a" || !args.To.After(args.From) {
			return nil, fmt.Errorf("invalid event read")
		}
		cards := []map[string]any{}
		for _, n := range args.Numbers {
			if b.stopAfterFirst && len(cards) > 0 {
				break
			}
			card := map[string]any{"board_id": c.BoardID, "number": n}
			if n == 1 {
				card["updated_at"] = args.To.Add(-time.Second)
			}
			cards = append(cards, card)
		}
		events := []map[string]any{}
		if b.events {
			events = append(events, map[string]any{"at": args.To.Add(-time.Hour), "label": "Deployed production"})
		}
		text, _ := json.Marshal(map[string]any{"events": events, "insight_cards": cards})
		return json.Marshal(map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}})
	}
	if c.Tool != "blerg_card_get" {
		return b.liveCardBoard.CallBoard(ctx, c)
	}
	reads := b.reads.Add(1)
	if b.stopAfterFirst && reads > 1 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var args struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(c.Arguments, &args); err != nil {
		return nil, err
	}
	at := time.Now().UTC().Add(-60*24*time.Hour + time.Duration(args.Number)*24*time.Hour)
	if args.Number == 1 {
		at = time.Now().UTC().Add(-time.Minute)
	}
	card := map[string]any{"board_id": "board-a", "number": args.Number, "id": fmt.Sprint(args.Number), "title": "Fixture", "flow": []map[string]any{{"at": at.Add(-time.Hour), "in_progress": true, "done": false}, {"at": at, "in_progress": false, "done": true}}}
	if args.Number == 1 {
		card["updated_at"] = time.Now().UTC().Add(-time.Second)
	}
	encoded, err := json.Marshal(card)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"content": []map[string]any{{"type": "text", "text": string(encoded)}}})
}

func TestInsightsActivityDeploymentEvents(t *testing.T) {
	fx := newTopicFx(t)
	api, base, _ := newLiveCardAPI(t)
	api.dbPool, api.coreAuth = fx.pool, fx.api.coreAuth
	installInsightsReviewBoard(api, &insightsReviewBoard{liveCardBoard: base, events: true})
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO session_card_links VALUES($1,'board-a',7,'pin',now())`, fx.sess); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/insights/activity?range=7d", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	out := httptest.NewRecorder()
	api.HandleInsightsActivity(out, req)
	var report insightActivityReport
	if err := json.Unmarshal(out.Body.Bytes(), &report); err != nil || out.Code != 200 || len(report.Events) != 1 || report.Events[0].Label != "Deployed production" || report.EventsUnavailable {
		t.Fatalf("events response: %d %s %v", out.Code, out.Body.String(), err)
	}
}

type insightsQueryTrace struct{ t *testing.T }
type insightsQueryStart struct{}

func (q *insightsQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	name := "other"
	for _, s := range []string{"fingerprints AS", "pending AS", "recent AS", "records AS", "questions AS", "SELECT e.session_id", "INSERT INTO session_insight_timings"} {
		if strings.Contains(d.SQL, s) {
			name = s
			break
		}
	}
	return context.WithValue(ctx, insightsQueryStart{}, struct {
		name string
		at   time.Time
	}{name, time.Now()})
}
func (q *insightsQueryTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	s := ctx.Value(insightsQueryStart{}).(struct {
		name string
		at   time.Time
	})
	q.t.Logf("query %s %s", s.name, time.Since(s.at))
}
func TestInsights7dSeededLatency(t *testing.T) {
	fx := newTopicFx(t)
	cfg := fx.pool.Config()
	cfg.ConnConfig.Tracer = &insightsQueryTrace{t}
	measured, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(measured.Close)
	api, base, _ := newLiveCardAPI(t)
	api.dbPool, api.coreAuth = measured, fx.api.coreAuth
	board := &insightsReviewBoard{liveCardBoard: base, events: true}
	installInsightsReviewBoard(api, board)
	// 400 completed workers spread across six days, 20 running workers,
	// 168,000 tool events and 1,000 card references. Completed workers end
	// just after their recorded work, without days of artificial unknown time.
	// All database work uses the shipped handler.
	ctx := t.Context()
	if _, err = fx.pool.Exec(ctx, `UPDATE sessions SET started_at=now()-interval '7 days' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	_, err = fx.pool.Exec(ctx, `INSERT INTO sessions(daemon_id,project_path,repo,title,spawning_account_id,parent_session_id,started_at,ended_at,engine)
 SELECT s.daemon_id,s.project_path,s.repo,'Seeded worker '||n,s.spawning_account_id,s.id,now()-interval '6 days'+n*interval '20 minutes',CASE WHEN n<=400 THEN now()-interval '6 days'+n*interval '20 minutes'+interval '12100 seconds' END,CASE WHEN n%2=0 THEN 'codex' ELSE 'claude' END FROM sessions s CROSS JOIN generate_series(1,420) n WHERE s.id=$1`, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fx.pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload,ts)
 SELECT s.id,n,gen_random_uuid(),CASE WHEN n%2=1 THEN 'tool_call' ELSE 'tool_result' END,CASE WHEN n%2=1 THEN jsonb_build_object('tool','Bash','call_id',((n+1)/2)::text,'input',jsonb_build_object('command','go test ./...')) ELSE jsonb_build_object('call_id',(n/2)::text,'is_error',false) END,s.started_at+n*interval '30 seconds' FROM sessions s CROSS JOIN generate_series(1,400) n WHERE s.parent_session_id=$1`, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fx.pool.Exec(ctx, `INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at) SELECT s.id,'board-a',n,'review',now()-n*interval '1 minute' FROM generate_series(1,1000) n JOIN (SELECT id,row_number() OVER(ORDER BY id) AS rn FROM sessions WHERE parent_session_id=$1) s ON s.rn=(n-1)%420+1`, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	// Link backfill has its own bounded progress. Finish it before measuring the
	// steady-state request, while retaining the cold measurement separately.
	var cold, warm, fresh time.Duration
	for i := range 7 {
		if i == 1 {
			for {
				n, e := db.BackfillInsightTimings(ctx, fx.pool, topicOwner)
				if e != nil {
					t.Fatal(e)
				}
				if n == 0 {
					break
				}
			}
		}
		if i == 6 {
			_, err = fx.pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload) SELECT id,401,gen_random_uuid(),'mark','{"kind":"milestone","text":"Seeded fix"}' FROM sessions WHERE parent_session_id=$1 AND ended_at IS NULL ORDER BY id LIMIT 1`, fx.sess)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest("GET", "/api/insights/activity?range=7d", nil)
		req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
		w := httptest.NewRecorder()
		started := time.Now()
		profilePath := "warm"
		if i == 0 {
			profilePath = "cold"
		}
		if i == 6 {
			profilePath = "fresh"
		}
		pprof.Do(req.Context(), pprof.Labels("insights_path", profilePath), func(ctx context.Context) { api.HandleInsightsActivity(w, req.WithContext(ctx)) })
		elapsed := time.Since(started)
		if w.Code != 200 {
			t.Fatalf("status %d", w.Code)
		}
		var report insightActivityReport
		if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		var gateSeconds float64
		var gateRuns int
		for _, s := range report.Sessions {
			if s.Timing != nil {
				gateSeconds += s.Timing.Phases["gates"]
				gateRuns += len(s.Timing.GateRuns)
			}
		}
		if gateSeconds == 0 || gateRuns == 0 {
			t.Fatal("fixture did not exercise gate timing through the HTTP response")
		}
		if i == 0 {
			cold = elapsed
			if report.PendingSessions != 400 {
				t.Fatalf("cold pending=%d want 400", report.PendingSessions)
			}
		}
		if i == 5 {
			warm = elapsed
		}
		if i == 6 {
			fresh = elapsed
		}
	}
	if got := board.reads.Load(); got != 7 {
		t.Fatalf("expected one board exchange per request, got %d", got)
	}
	t.Logf("seeded 7d API: cold=%s warm=%s fresh-after-event=%s target=500ms; 420 conversations, 168000 events, 1000 card refs, one authorized board exchange", cold, warm, fresh)
	req := httptest.NewRequest("GET", "/api/insights?range=7d&scope=mine", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	w := httptest.NewRecorder()
	started := time.Now()
	api.HandleGetInsights(w, req)
	if w.Code != 200 {
		t.Fatalf("summary status %d", w.Code)
	}
	t.Logf("seeded 7d summary API: %s", time.Since(started))
}
func installInsightsReviewBoard(api *API, b *insightsReviewBoard) {
	api.hub.mcpStart.set(&mcpStartConfig{Core: &liveKeyBoardList{}, Board: b})
}

func TestInsightsActivityRecentCompletionAndLatestCardCap(t *testing.T) {
	fx := newTopicFx(t)
	api, base, _ := newLiveCardAPI(t)
	api.dbPool, api.coreAuth = fx.pool, fx.api.coreAuth
	b := &insightsReviewBoard{liveCardBoard: base}
	installInsightsReviewBoard(api, b)
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET started_at=now()-interval '120 days' WHERE id=$1;`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(context.Background(), `INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at) SELECT $1,'board-a',n,'pin',now()-interval '120 days'+n*interval '1 day' FROM generate_series(1,51) n`, fx.sess); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	payload := fmt.Sprintf(`{"kind":"review","board_id":"board-a","card":2,"at":%q,"verdict":"approved"}`, at)
	if _, err := fx.pool.Exec(context.Background(), `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload,ts) VALUES($1,1,$2,'mark',$3,now()-interval '120 days')`, fx.sess, newUUID(), payload); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/insights/activity?range=24h", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	w := httptest.NewRecorder()
	api.HandleInsightsActivity(w, req)
	var report insightActivityReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || w.Code != 200 {
		t.Fatalf("report %d %s %v", w.Code, w.Body.String(), err)
	}
	if len(report.Cards) != 50 || !report.Truncated || report.Cards[0].Key != "board-a#1" {
		t.Fatalf("recent completion/card cap: %+v", report)
	}
	for _, c := range report.Cards {
		if c.Key == "board-a#3" {
			t.Fatal("oldest card retained instead of latest activity")
		}
	}
	if len(report.Reviews) != 1 || report.Reviews[0].Card != 2 {
		t.Fatal("dated review activity did not affect the shared card selection")
	}
	for _, m := range report.Reviews {
		found := false
		for _, c := range report.Cards {
			if c.Key == fmt.Sprintf("%s#%d", m.BoardID, m.Card) {
				found = true
			}
		}
		if !found {
			t.Fatal("reviews used a different card cap than flow")
		}
	}
}
func TestInsightsActivityDeadlineRetainsPartialCards(t *testing.T) {
	fx := newTopicFx(t)
	api, base, _ := newLiveCardAPI(t)
	api.dbPool, api.coreAuth = fx.pool, fx.api.coreAuth
	b := &insightsReviewBoard{liveCardBoard: base, stopAfterFirst: true}
	installInsightsReviewBoard(api, b)
	if _, err := fx.pool.Exec(context.Background(), `INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at) SELECT $1,'board-a',n,'pin',now() FROM generate_series(1,2) n`, fx.sess); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest("GET", "/api/insights/activity", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	w := httptest.NewRecorder()
	api.HandleInsightsActivity(w, req)
	var report insightActivityReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || w.Code != 200 || report.OmittedCards != 1 || len(report.Cards) != 1 {
		t.Fatalf("deadline discarded partial data: %d %s %v", w.Code, w.Body.String(), err)
	}
}

func TestInsightsBothReportsRequireAccountWideHumanSignIn(t *testing.T) {
	fx := newTopicFx(t)
	for _, tc := range []struct {
		kind, sid, project string
		want               int
	}{{"agent", testSID, "", 403}, {"human", testSID, "board-a", 403}, {"human", "", "", 401}} {
		token := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: topicOwner, Aud: coreAuthAudience, Kind: tc.kind, Sid: tc.sid, Project: tc.project, Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
		for _, route := range []string{"/api/insights", "/api/insights/activity"} {
			req := httptest.NewRequest("GET", route+"?workspace="+fx.sess, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			if route == "/api/insights" {
				fx.api.HandleGetInsights(w, req)
			} else {
				fx.api.HandleInsightsActivity(w, req)
			}
			if w.Code != tc.want {
				t.Errorf("%s kind=%s project=%s sid=%t: got %d want %d", route, tc.kind, tc.project, tc.sid != "", w.Code, tc.want)
			}
		}
	}
}
