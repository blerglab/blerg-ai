package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Turning push off in the bell removes this browser's subscription, so the server stops sending to
// it; removing one that is gone already is fine, and a body with no endpoint is refused.
func TestDeletePushSubscribeRemovesTheSubscription(t *testing.T) {
	f := newIngestFixture(t)
	api := NewAPI(f.hub, f.pool, "", nil, "")
	token := enableBrowserAuth(t, api)(coreAuthBrowserCap)
	ep := "https://push.example.com/sub/turn-off"
	if err := db.UpsertPushSubscription(f.ctx, f.pool, ep, "k", "a", "user-1", testSID); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPushSubscription(f.ctx, f.pool, ep+"-other", "k", "a", "other", testSID); err != nil {
		t.Fatal(err)
	}
	call := func(body string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodDelete, "/api/push/subscribe", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		api.HandleDeletePushSubscribe(w, r)
		return w.Code
	}
	if code := call(`{"endpoint":"` + ep + `"}`); code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", code)
	}
	if code := call(`{"endpoint":"` + ep + `"}`); code != http.StatusNoContent {
		t.Fatalf("delete again = %d, want 204", code)
	}
	if code := call(`{}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("no endpoint = %d, want 422", code)
	}
	subs, err := db.ListPushSubscriptions(f.ctx, f.pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Endpoint != ep+"-other" {
		t.Fatalf("subscriptions left = %+v, want only the other browser's", subs)
	}
}

func TestPushEndpointTransfersAndDeleteChecksAccount(t *testing.T) {
	f := newPrivFixture(t)
	ep := "https://fcm.googleapis.com/shared-browser"
	call := func(method, account string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/push/subscribe", strings.NewReader(`{"endpoint":"`+ep+`","keys":{"p256dh":"k","auth":"a"}}`))
		r.Header.Set("Authorization", "Bearer "+f.tok(t, "human", account, coreAuthBrowserCap))
		if method == http.MethodPost {
			f.api.HandlePostPushSubscribe(w, r)
		} else {
			f.api.HandleDeletePushSubscribe(w, r)
		}
		return w.Code
	}
	for _, account := range []string{privAcctA, privAcctB} {
		if code := call(http.MethodPost, account); code != http.StatusCreated {
			t.Fatalf("register %s: %d", account, code)
		}
	}
	if code := call(http.MethodDelete, privAcctA); code != http.StatusNoContent {
		t.Fatal(code)
	}
	subs, err := db.ListPushSubscriptions(t.Context(), f.pool)
	if err != nil || len(subs) != 1 || subs[0].AccountID != privAcctB {
		t.Fatalf("foreign delete removed transferred endpoint: %+v %v", subs, err)
	}
	if code := call(http.MethodDelete, privAcctB); code != http.StatusNoContent {
		t.Fatal(code)
	}
	subs, err = db.ListPushSubscriptions(t.Context(), f.pool)
	if err != nil || len(subs) != 0 {
		t.Fatalf("own delete: %+v %v", subs, err)
	}
}
