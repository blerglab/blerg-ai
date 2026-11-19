package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestPushMessageOnlySessionOwner(t *testing.T) {
	f := newPrivFixture(t)
	livePushCore(t)
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	for _, account := range []string{privAcctA, privAcctB, ""} {
		if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/"+account, "k", "a", account, testSID); err != nil {
			t.Fatal(err)
		}
	}
	var endpoints []string
	saved := sendWebPush
	sendWebPush = func(_ []byte, s *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		endpoints = append(endpoints, s.Endpoint)
		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = saved })
	row, err := db.GetSession(t.Context(), f.pool, f.pubSession)
	if err != nil {
		t.Fatal(err)
	}
	f.api.pushForMessage(*row, db.MessageRow{Kind: "note", Body: "for owner"})
	if len(endpoints) != 1 || endpoints[0] != "https://push.example.com/"+privAcctA {
		t.Fatalf("delivered to %v; want only owner", endpoints)
	}
}

// Use the real Core proof HTTP contract, with no push delivery while signed out
// or while Core cannot establish liveness.
func livePushCore(t *testing.T, canonicalNotFound ...bool) *atomic.Int32 {
	t.Helper()
	status := &atomic.Int32{}
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/credentials/list" || r.Header.Get("X-Internal-Key") != "test-internal" {
			t.Error("missing Core proof authentication")
		}
		var proof struct {
			AccountID string `json:"account_id"`
			SessionID string `json:"session_id"`
		}
		if json.NewDecoder(r.Body).Decode(&proof) != nil || proof.AccountID == "" || proof.SessionID == "" {
			t.Error("missing exact login proof")
		}
		w.WriteHeader(int(status.Load()))
		if status.Load() == 404 && len(canonicalNotFound) > 0 && canonicalNotFound[0] {
			_, _ = w.Write([]byte("not found\n"))
			return
		}
		_, _ = w.Write([]byte(`{"engines":[]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("BLERG_CORE_URL", srv.URL)
	t.Setenv("BLERG_RUNNER_CORE_INTERNAL_KEY", "test-internal")
	return status
}

func TestPushRevokedLoginIsRemovedAndCoreFailureIsClosed(t *testing.T) {
	f := newPrivFixture(t)
	status := livePushCore(t, true)
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/login", "k", "a", privAcctA, testSID); err != nil {
		t.Fatal(err)
	}
	saved := sendWebPush
	deliveries := 0
	sendWebPush = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		deliveries++
		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = saved })
	status.Store(503)
	SendPushToAccount(f.pool, privAcctA, "t", "b", "/sessions/"+f.pubSession)
	subs, err := db.ListPushSubscriptions(t.Context(), f.pool)
	if err != nil || len(subs) != 1 || deliveries != 0 {
		t.Fatalf("Core unavailable: %v %v %d", subs, err, deliveries)
	}
	status.Store(404)
	SendPushToAccount(f.pool, privAcctA, "t", "b", "/sessions/"+f.pubSession)
	subs, err = db.ListPushSubscriptions(t.Context(), f.pool)
	if err != nil || len(subs) != 0 || deliveries != 0 {
		t.Fatalf("signed out: %v %v %d", subs, err, deliveries)
	}
}

func TestPushSessionRecipientsFailClosed(t *testing.T) {
	f := newPrivFixture(t)
	livePushCore(t)
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	for _, account := range []string{privAcctA, privAcctB, ""} {
		if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/"+account, "k", "a", account, testSID); err != nil {
			t.Fatal(err)
		}
	}
	var endpoints []string
	saved := sendWebPush
	sendWebPush = func(_ []byte, s *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		endpoints = append(endpoints, s.Endpoint)
		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = saved })
	owner := privAcctA
	for _, tc := range []struct {
		name, id string
		owner    *string
		private  bool
		count    int
	}{
		{"owned public", f.pubSession, &owner, false, 1},
		{"owned private", f.privSession, &owner, true, 1},
		{"ownerless private", f.privSession, nil, true, 0},
		{"ownerless public", f.pubSession, nil, false, 2},
		{"unknown", newUUID(), nil, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=$2,private=$3 WHERE id=$1`, tc.id, tc.owner, tc.private); err != nil {
				t.Fatal(err)
			}
			endpoints = nil
			SendPushForSession(f.pool, tc.id, "t", "b", "/sessions/"+tc.id+"?artifact=example")
			if len(endpoints) != tc.count {
				t.Fatalf("recipients %v; want %d", endpoints, tc.count)
			}
			if tc.count == 1 && endpoints[0] != "https://push.example.com/"+privAcctA {
				t.Fatalf("foreign delivery: %v", endpoints)
			}
		})
	}
	endpoints = nil
	SendPushToAccount(f.pool, "", "t", "b", "")
	if len(endpoints) != 0 {
		t.Fatalf("empty account delivered: %v", endpoints)
	}
}
