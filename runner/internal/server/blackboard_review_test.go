package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func TestBlackboardReviewReadGuards(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	ctx := context.Background()
	owner := blackboard.Actor{ID: topicOwner, Kind: "human"}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.sess, Version: 1, Name: "Private plain text"}
	if err := fx.api.blackboardStore().Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	req := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	base := "/api/blackboards/" + record.ID
	for _, path := range []string{base, base + "/slice", base + "/timeline", "/api/blackboards"} {
		if w := req(path+"?session="+fx.sess, fx.token(topicOwner, "agent")); w.Code != 403 {
			t.Fatalf("agent read %s %d %s", path, w.Code, w.Body.String())
		}
		for _, actor := range []struct {
			token string
			code  int
		}{{fx.token(topicOwner, "agent"), 403}, {fx.token(topicOwner, "human"), 404}} {
			w := req(path+"?session="+fx.privSess, actor.token)
			if w.Code != actor.code || strings.Contains(w.Body.String(), record.Name) {
				t.Fatalf("read guard %s %d %s", path, w.Code, w.Body.String())
			}
		}
	}
	if w := req(base, fx.token(topicOwner, "human")); w.Code != 200 {
		t.Fatalf("boardless owner %d %s", w.Code, w.Body.String())
	}
	if w := req(base+"?session="+fx.privSess, fx.token(topicOwner, "human")); w.Code != 404 {
		t.Fatalf("workspace mismatch %d", w.Code)
	}
	private := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: topicOther, Kind: "human"}, WorkspaceID: fx.privSess, Version: 1, Name: "Private owner text"}
	if err := fx.api.blackboardStore().Create(ctx, private); err != nil {
		t.Fatal(err)
	}
	if w := req("/api/blackboards/"+private.ID+"?session="+fx.privSess, fx.token(topicOwner, "human")); w.Code != 404 {
		t.Fatalf("plain visibility %d %s", w.Code, w.Body.String())
	}
	// A non-owner can see this public workspace but cannot add person-sourced history.
	r := httptest.NewRequest("POST", base+"/events?session="+fx.sess, strings.NewReader(`{"kind":"decision"}`))
	r.Header.Set("Authorization", "Bearer "+fx.token(topicOther, "human"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("viewer event %d %s", w.Code, w.Body.String())
	}
}
func TestBlackboardReviewAgentAncestryAndEnded(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	ctx := context.Background()
	child := newChildOf(t, fx)
	record := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: topicOwner, Kind: "human"}, WorkspaceID: fx.sess, Version: 1, Name: "Secret"}
	if err := fx.api.blackboardStore().Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	req := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/sessions/"+child.id+"/plan?blackboard="+record.ID, nil)
		r.Header.Set("Authorization", "Bearer "+child.tok)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := req(); w.Code != 200 {
		t.Fatalf("ancestry %d %s", w.Code, w.Body.String())
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, fx.sess, topicOther); err != nil {
		t.Fatal(err)
	}
	if w := req(); w.Code != 404 {
		t.Fatalf("foreign ancestor %d %s", w.Code, w.Body.String())
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, fx.sess, topicOwner); err != nil {
		t.Fatal(err)
	}
	if err := fx.api.blackboardStore().Update(ctx, record.ID, func(r *blackboard.Record) error { r.WorkspaceID = fx.privSess; return nil }); err != nil {
		t.Fatal(err)
	}
	if w := req(); w.Code != 404 {
		t.Fatalf("unlinked %d %s", w.Code, w.Body.String())
	}
	if err := fx.api.blackboardStore().Update(ctx, record.ID, func(r *blackboard.Record) error { r.WorkspaceID = fx.sess; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"status='stopped'", "status='running',archived_at=now()", "archived_at=NULL,discard_requested_at=now()"} {
		if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET `+column+` WHERE id=$1`, child.id); err != nil {
			t.Fatal(err)
		}
		if w := req(); w.Code != 409 {
			t.Fatalf("ended %s %d %s", column, w.Code, w.Body.String())
		}
	}
}

type blackboardFailGateway struct {
	blackboardGateway
	fail bool
}

func (b *blackboardFailGateway) CallBoard(ctx context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	if b.fail && c.Tool != "blerg_board_get" {
		return nil, errors.New("upstream unavailable")
	}
	return b.blackboardGateway.CallBoard(ctx, c)
}
func TestBlackboardReviewBoardProjectToolAndFailure(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	human := fx.token(topicOwner, "human")
	if code, body := setLevel(fx, human, "board-a", "read"); code != 200 {
		t.Fatalf("grant %d %s", code, body)
	}
	b := &blackboardFailGateway{}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: b})
	record := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: topicOwner, Kind: "human"}, WorkspaceID: fx.sess, Version: 1, Links: []blackboard.Link{{WorkspaceID: fx.sess, BoardID: "board-a"}}, Milestones: []blackboard.Milestone{{ID: blackboard.NewID(), BoardID: "board-a", ReleaseID: "release", Version: 1}}}
	if err := fx.api.blackboardStore().Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	req := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path+"?session="+fx.sess, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	base := "/api/blackboards/" + record.ID
	projectToken := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: topicOwner, Aud: coreAuthAudience, Kind: "human", Sid: testSID, Project: "other-board", Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if w := req(base, projectToken); w.Code != 404 {
		t.Fatalf("project %d %s", w.Code, w.Body.String())
	}
	if code, body := setLevel(fx, human, "board-a", "propose"); code != 200 {
		t.Fatalf("grant %d %s", code, body)
	}
	if w := req(base, human); w.Code != 403 {
		t.Fatalf("boardtool %d %s", w.Code, w.Body.String())
	}
	if code, body := setLevel(fx, human, "board-a", "read"); code != 200 {
		t.Fatalf("grant %d %s", code, body)
	}
	b.fail = true
	if w := req(base+"/milestones/"+record.Milestones[0].ID+"/slice", human); w.Code != 502 || strings.Contains(w.Body.String(), "database") {
		t.Fatalf("board failure %d %s", w.Code, w.Body.String())
	}
}
func TestBlackboardReviewByteCapWrapperAndHumanPages(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	record := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: topicOwner, Kind: "human"}, WorkspaceID: fx.sess, Version: 1, Name: "Untrusted"}
	for n := 0; n < 20; n++ {
		record.Items = append(record.Items, blackboard.Item{ID: blackboard.ItemID(blackboard.NewID()), Title: strings.Repeat("hostile ", 4000), State: "planned"})
	}
	if err := fx.api.blackboardStore().Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	req := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := req("/api/sessions/"+fx.sess+"/plan?limit=20", fx.sessTok)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(w.Body.Bytes()) > 64<<10 || body["untrusted_data"] == nil || body["rows"] != nil || string(body["truncated"]) != "true" {
		t.Fatalf("cap/wrapper %d bytes=%d", w.Code, w.Body.Len())
	}
	w = req("/api/blackboards/"+record.ID+"?session="+fx.sess+"&limit=2", fx.token(topicOwner, "human"))
	var human struct {
		Record blackboard.Record `json:"record"`
		Next   string            `json:"next_cursor"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &human)
	if w.Code != 200 || len(human.Record.Items) != 2 || human.Next != "2" {
		t.Fatalf("human pagination %d %s", w.Code, w.Body.String())
	}
}

func TestBlackboardReviewAbortWithoutBoardAndTimelinePage(t *testing.T) {
	fx, b, _ := newBoardLevelFx(t)
	ctx := context.Background()
	mid := blackboard.NewID()
	record := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: topicOwner, Kind: "human"}, WorkspaceID: fx.sess, Version: 1, Milestones: []blackboard.Milestone{{ID: mid, Version: 1, Stage: "Finalizing"}}}
	for n := 0; n < 3; n++ {
		record.Events = append(record.Events, blackboard.Event{ID: blackboard.NewID(), Kind: "decision", Source: "person", RecordedAt: time.Now()})
	}
	if err := fx.api.blackboardStore().Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	path := "/api/blackboards/" + record.ID + "/milestones/" + mid + "/final/abort"
	for _, token := range []string{fx.token(topicOwner, "agent"), fx.token(topicOther, "human")} {
		w := request("POST", path, token, `{"revision":0}`)
		if w.Code != 403 {
			t.Fatalf("abort authority %d %s", w.Code, w.Body.String())
		}
	}
	if w := request("POST", path, fx.token(topicOwner, "human"), `{"revision":0}`); w.Code != 410 {
		t.Fatalf("abort %d %s", w.Code, w.Body.String())
	}
	if b.count() != 0 {
		t.Fatal("abort called board")
	}
	w := request("GET", "/api/blackboards/"+record.ID+"/timeline?session="+fx.sess+"&limit=2", fx.token(topicOwner, "human"), "")
	var result struct {
		Events []blackboard.Event `json:"events"`
		Next   string             `json:"next_cursor"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if w.Code != 200 || len(result.Events) != 2 || result.Next != "2" {
		t.Fatalf("timeline page %d %s", w.Code, w.Body.String())
	}
	if err := fx.api.blackboardStore().Update(ctx, record.ID, func(r *blackboard.Record) error {
		r.Links = []blackboard.Link{{WorkspaceID: fx.sess, BoardID: "board-a"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	projectToken := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: topicOwner, Aud: coreAuthAudience, Kind: "human", Sid: testSID, Project: "other-board", Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if w := request("POST", path, projectToken, `{"revision":0}`); w.Code != 404 {
		t.Fatalf("abort project %d %s", w.Code, w.Body.String())
	}
}

func TestBlackboardReviewRequestByteCap(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"value":"`+strings.Repeat("雪", 50000)+`"}`))
	var body struct {
		Value string `json:"value"`
	}
	if decodeBlackboard(w, r, &body) || w.Code != 400 {
		t.Fatalf("request bytes not bounded: %d", w.Code)
	}
}

func TestBlackboardReviewOversizedMetadataKeepsBoundedRows(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	for _, count := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			record := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: topicOwner, Kind: "human"}, WorkspaceID: fx.sess, Version: 1, Name: "Plan", Vision: strings.Repeat("雪", 30000)}
			for n := 0; n < count; n++ {
				record.Items = append(record.Items, blackboard.Item{ID: blackboard.ItemID(blackboard.NewID()), Title: "Keep this item", State: "planned"})
			}
			if err := fx.api.blackboardStore().Create(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/api/sessions/"+fx.sess+"/plan?blackboard="+record.ID, nil)
			r.Header.Set("Authorization", "Bearer "+fx.sessTok)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			var response struct {
				Data struct {
					Rows []any `json:"rows"`
				} `json:"untrusted_data"`
				Truncated bool `json:"truncated"`
			}
			err := json.Unmarshal(w.Body.Bytes(), &response)
			if err != nil || w.Code != 200 || w.Body.Len() > 64<<10 || !response.Truncated || len(response.Data.Rows) != count {
				t.Fatalf("metadata cap: code=%d bytes=%d rows=%d want=%d truncated=%v err=%v", w.Code, w.Body.Len(), len(response.Data.Rows), count, response.Truncated, err)
			}
			if count > 0 && !strings.Contains(w.Body.String(), "Keep this item") {
				t.Fatal("bounded row lost with oversized metadata")
			}
		})
	}
}
