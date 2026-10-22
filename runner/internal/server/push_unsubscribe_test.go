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
	if err := db.UpsertPushSubscription(f.ctx, f.pool, ep, "k", "a"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPushSubscription(f.ctx, f.pool, ep+"-other", "k", "a"); err != nil {
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
