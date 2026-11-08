package server

import (
	"encoding/json"
	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"net/http"
	"strings"
	"testing"
)

func TestInboxRecipientAndMainOnly(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterWorklistRoutes(fx.mux)
	body := map[string]any{"subject": "Morning summary", "body": "## Ready to read\n\nThe release is ready.", "kind": "summary"}
	path := "/api/sessions/" + fx.parent + "/inbox"
	code, item, _ := fx.do(http.MethodPost, path, fx.parentTok, body, nil)
	if code != 201 {
		t.Fatalf("post: %d %v", code, item)
	}
	code, _, _ = fx.do(http.MethodPost, path, delegDaemonToken, body, nil)
	if code != 403 {
		t.Fatalf("daemon post: %d", code)
	}
	id, _ := item["id"].(string)
	if id == "" {
		t.Fatal("missing item ID")
	}
	for _, account := range []string{"owner-2", "admin"} {
		code, _, _ = fx.do(http.MethodGet, "/api/inbox/"+id, func() string {
			if account == "admin" {
				return inboxAdminToken(fx)
			}
			return fx.human(account)
		}(), nil, nil)
		if code != 404 {
			t.Fatalf("foreign recipient %s: %d", account, code)
		}
	}
	code, got, _ := fx.do(http.MethodGet, "/api/inbox/"+id, fx.human("owner-1"), nil, nil)
	if code != 200 || got["subject"] != body["subject"] || got["read"] != false {
		t.Fatalf("read item: %d %v", code, got)
	}
	for _, read := range []bool{true, false} {
		code, got, _ = fx.do(http.MethodPut, "/api/inbox/"+id+"/read", fx.human("owner-1"), map[string]any{"read": read}, nil)
		if code != 200 || got["read"] != read {
			t.Fatalf("read state: %d %v", code, got)
		}
		code, got, _ = fx.do(http.MethodGet, "/api/inbox/"+id, fx.human("owner-1"), nil, nil)
		if code != 200 || got["read"] != read {
			t.Fatalf("durable read state: %d %v", code, got)
		}
	}
	child, childToken := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	code, _, _ = fx.do(http.MethodPost, "/api/sessions/"+child+"/inbox", childToken, body, nil)
	if code != 403 {
		t.Fatalf("worker post: %d", code)
	}
	code, _, _ = fx.do(http.MethodPost, path, childToken, body, nil)
	if code != 403 {
		t.Fatalf("worker impersonating Main: %d", code)
	}
	code, _, _ = fx.do(http.MethodGet, "/api/inbox/"+id, fx.parentTok, nil, nil)
	if code < 400 {
		t.Fatalf("session credential read: %d", code)
	}
	code, _, _ = fx.do(http.MethodPut, "/api/inbox/"+id+"/read", fx.human("owner-2"), map[string]any{"read": true}, nil)
	if code != 404 {
		t.Fatalf("foreign read mutation: %d", code)
	}
}

func TestInboxInvalidationRecipientOnly(t *testing.T) {
	hub := NewHub()
	api := NewAPI(hub, nil, "", nil, "")
	owner := &BrowserConn{ID: "owner", AccountID: "recipient", send: make(chan []byte, 4)}
	foreign := &BrowserConn{ID: "foreign", AccountID: "someone-else", send: make(chan []byte, 4)}
	hub.RegisterBrowser(owner)
	hub.RegisterBrowser(foreign)
	api.broadcastInboxChanged("recipient")
	if len(owner.send) != 1 {
		t.Fatal("recipient did not receive invalidation")
	}
	if len(foreign.send) != 0 {
		t.Fatal("invalidation leaked to another account")
	}
	if string(<-owner.send) != `{"type":"inbox_changed"}` {
		t.Fatal("invalidation contains content")
	}
}

func TestInboxInputValidation(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterWorklistRoutes(fx.mux)
	for _, body := range []any{map[string]any{"subject": "", "body": "body", "kind": "summary"}, map[string]any{"subject": "subject", "body": " ", "kind": "summary"}, map[string]any{"subject": "subject", "body": "body", "kind": "chat"}, map[string]any{"subject": "subject", "body": "body", "kind": "summary", "account_id": "other"}} {
		code, _, _ := fx.do(http.MethodPost, "/api/sessions/"+fx.parent+"/inbox", fx.parentTok, body, nil)
		if code != 400 {
			t.Fatalf("invalid input: %d", code)
		}
	}
	code, _, _ := fx.do(http.MethodPost, "/api/sessions/"+fx.parent+"/inbox", fx.parentTok, map[string]any{"subject": "Private subject", "body": "Private body", "kind": "summary"}, nil)
	if code != 201 {
		t.Fatalf("seed item: %d", code)
	}
	code, got, _ := fx.do(http.MethodGet, "/api/inbox", fx.human("owner-2"), nil, nil)
	if code != 200 || len(got["items"].([]any)) != 0 || got["unread"] != float64(0) {
		t.Fatalf("foreign list: %d %v", code, got)
	}
	agent, tok := fx.session(sessOpts{owner: "owner-1", startedByKind: "agent"})
	code, _, _ = fx.do(http.MethodPost, "/api/sessions/"+agent+"/inbox", tok, map[string]any{"subject": "subject", "body": "body", "kind": "summary"}, nil)
	if code != 403 {
		t.Fatalf("root automation post: %d", code)
	}
}

func TestInboxPushRecipientAndDeepLink(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterWorklistRoutes(fx.mux)
	livePushCore(t)
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	for _, account := range []string{"owner-1", "owner-2"} {
		if err := db.UpsertPushSubscription(t.Context(), fx.pool, "https://push.example.com/"+account, "k", "a", account, testSID); err != nil {
			t.Fatal(err)
		}
	}
	var got []map[string]string
	saved := sendWebPush
	sendWebPush = func(raw []byte, sub *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		if sub.Endpoint != "https://push.example.com/owner-1" {
			t.Fatal("Inbox pushed to a foreign recipient")
		}
		var data map[string]string
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		got = append(got, data)
		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = saved })
	code, item, _ := fx.do(http.MethodPost, "/api/sessions/"+fx.parent+"/inbox", fx.parentTok, map[string]any{"subject": "Morning summary", "body": "Private full body must stay in the app.", "kind": "summary"}, nil)
	if code != 201 || len(got) != 1 {
		t.Fatalf("post/push: %d %d", code, len(got))
	}
	if got[0]["url"] != "/sessions/"+fx.parent+"?inbox="+item["id"].(string) || strings.Contains(got[0]["body"], "Private full body") {
		t.Fatalf("push metadata: %v", got)
	}
}

func TestInboxReviewBodyBoundaryAndHumanMain(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterWorklistRoutes(fx.mux)
	main, token := fx.session(sessOpts{owner: "owner-1", startedByKind: "human"})
	path := "/api/sessions/" + main + "/inbox"
	for _, body := range []string{strings.Repeat("é", 131072), strings.Repeat("\"", 262144)} {
		code, _, _ := fx.do(http.MethodPost, path, token, map[string]any{"subject": "Boundary", "body": body, "kind": "summary"}, nil)
		if code != 201 {
			t.Fatalf("documented 256 KiB body: %d", code)
		}
	}
	code, _, _ := fx.do(http.MethodPost, path, token, map[string]any{"subject": "Too large", "body": strings.Repeat("x", 262145), "kind": "summary"}, nil)
	if code != 400 {
		t.Fatalf("decoded body over limit: %d", code)
	}
}

func TestInboxReviewIdempotency(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterWorklistRoutes(fx.mux)
	path := "/api/sessions/" + fx.parent + "/inbox"
	owner := &BrowserConn{ID: "inbox-owner", AccountID: "owner-1", send: make(chan []byte, 8)}
	fx.hub.RegisterBrowser(owner)
	body := map[string]any{"subject": "Once", "body": "Original body", "kind": "summary", "client_id": "invocation-1"}
	code, first, _ := fx.do(http.MethodPost, path, fx.parentTok, body, nil)
	if code != 201 {
		t.Fatalf("first post: %d", code)
	}
	body["body"] = "Retry must not replace original"
	code, again, _ := fx.do(http.MethodPost, path, fx.parentTok, body, nil)
	if len(owner.send) != 1 {
		t.Fatal("retry emitted another arrival")
	}
	if code != 200 || again["id"] != first["id"] || again["body"] != "Original body" {
		t.Fatalf("retry: %d %v", code, again)
	}
	code, list, _ := fx.do(http.MethodGet, "/api/inbox", fx.human("owner-1"), nil, nil)
	if code != 200 || len(list["items"].([]any)) != 1 {
		t.Fatalf("duplicate stored: %v", list)
	}
	other, tok := fx.session(sessOpts{owner: "owner-1"})
	code, second, _ := fx.do(http.MethodPost, "/api/sessions/"+other+"/inbox", tok, body, nil)
	if code != 201 || second["id"] == first["id"] {
		t.Fatal("client ID must be scoped to source Main")
	}
}

func TestInboxReviewPaginationAndAdmin(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterWorklistRoutes(fx.mux)
	_, err := fx.pool.Exec(t.Context(), `INSERT INTO inbox_items(id,account_id,session_id,subject,body,kind,created_at) SELECT 'page-'||n,'owner-1',$1,'Subject '||n,'body','summary','2026-11-05T12:00:00Z'::timestamptz+n*interval '1 second' FROM generate_series(1,105) n`, fx.parent)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fx.pool.Exec(t.Context(), `INSERT INTO inbox_items(id,account_id,session_id,subject,body,kind) VALUES ('foreign-cursor','owner-2',$1,'Foreign','body','summary')`, fx.parent)
	if err != nil {
		t.Fatal(err)
	}
	code, first, _ := fx.do(http.MethodGet, "/api/inbox", fx.human("owner-1"), nil, nil)
	if code != 200 || len(first["items"].([]any)) != 100 || first["unread"] != float64(105) {
		t.Fatalf("first page/count: %d %v", code, first)
	}
	code, last, _ := fx.do(http.MethodGet, "/api/inbox?before="+first["next"].(string), fx.human("owner-1"), nil, nil)
	if code != 200 || len(last["items"].([]any)) != 5 || last["next"] != "" || last["unread"] != float64(105) {
		t.Fatalf("last page: %d %v", code, last)
	}
	for _, cursor := range []string{"foreign-cursor", "unknown-cursor"} {
		code, got, _ := fx.do(http.MethodGet, "/api/inbox?before="+cursor, fx.human("owner-1"), nil, nil)
		if code != 200 || len(got["items"].([]any)) != 0 || got["unread"] != float64(105) {
			t.Fatalf("foreign/unknown cursor: %d %v", code, got)
		}
	}
	admin := inboxAdminToken(fx)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		path := "/api/inbox/page-105"
		var body any
		if method == http.MethodPut {
			path += "/read"
			body = map[string]any{"read": true}
		}
		code, _, _ = fx.do(method, path, admin, body, nil)
		if code != 404 {
			t.Fatalf("admin bypass: %s %d", method, code)
		}
	}
}

func TestInboxReviewRefusesChannelAndEndedMain(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterWorklistRoutes(fx.mux)
	for _, opts := range []sessOpts{{owner: "owner-1", parent: fx.parent, startedByKind: "human"}, {owner: "owner-1", status: "ended"}} {
		id, tok := fx.session(opts)
		code, _, _ := fx.do(http.MethodPost, "/api/sessions/"+id+"/inbox", tok, map[string]any{"subject": "No", "body": "body", "kind": "summary"}, nil)
		if opts.parent != "" && code != 403 {
			t.Fatalf("channel: %d", code)
		}
		if opts.status == "ended" && code != 409 {
			t.Fatalf("ended Main: %d", code)
		}
	}
}

func inboxAdminToken(fx *delegFx) string {
	fx.t.Helper()
	token := fx.coreToken(identity.Claims{Kind: "human", Sub: "admin", Sid: testSID, Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap, sessionAdminCap, "account.manage"}})
	principal, ok := fx.api.verifyBrowserToken(token)
	if !ok || !principal.Has(sessionAdminCap) || !principal.Has("account.manage") {
		fx.t.Fatal("fixture must authenticate with administrator capabilities")
	}
	return token
}

func TestInboxDeleteRecipientOnly(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterWorklistRoutes(fx.mux)
	code, item, _ := fx.do(http.MethodPost, "/api/sessions/"+fx.parent+"/inbox", fx.parentTok, map[string]any{"subject": "Delete me", "body": "Private body", "kind": "summary"}, nil)
	if code != 201 {
		t.Fatalf("seed: %d", code)
	}
	path := "/api/inbox/" + item["id"].(string)
	// owner-2 can see the public source workspace but is not its inbox recipient.
	principal, ok := fx.api.verifyBrowserToken(fx.human("owner-2"))
	if !ok || !fx.api.browserCanSee(t.Context(), principal.Sub, fx.parent) {
		t.Fatal("shared workspace fixture is not visible")
	}
	for _, token := range []string{fx.human("owner-2"), inboxAdminToken(fx), fx.parentTok} {
		code, _, _ = fx.do(http.MethodDelete, path, token, nil, nil)
		if token == fx.parentTok {
			if code < 400 {
				t.Fatalf("agent delete: %d", code)
			}
		} else if code != 404 {
			t.Fatalf("foreign delete: %d", code)
		}
	}
	owner := &BrowserConn{ID: "delete-owner", AccountID: "owner-1", send: make(chan []byte, 4)}
	fx.hub.RegisterBrowser(owner)
	code, _, _ = fx.do(http.MethodDelete, path, fx.human("owner-1"), nil, nil)
	if code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if len(owner.send) != 1 || string(<-owner.send) != `{"type":"inbox_changed"}` {
		t.Fatal("missing invalidation")
	}
	code, _, _ = fx.do(http.MethodGet, path, fx.human("owner-1"), nil, nil)
	if code != 404 {
		t.Fatalf("deleted get: %d", code)
	}
	_, list, _ := fx.do(http.MethodGet, "/api/inbox", fx.human("owner-1"), nil, nil)
	if len(list["items"].([]any)) != 0 || list["unread"] != float64(0) {
		t.Fatalf("deleted list: %v", list)
	}
	code, _, _ = fx.do(http.MethodDelete, path, fx.human("owner-1"), nil, nil)
	if code != 404 {
		t.Fatalf("missing delete: %d", code)
	}
}
