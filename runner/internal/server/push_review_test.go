package server

import (
	"bytes"
	webpush "github.com/SherClockHolmes/webpush-go"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestPushCoreFailuresKeepSubscriptions(t *testing.T) {
	for _, status := range []int{401, 403, 404, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newPrivFixture(t)
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			t.Cleanup(func() { log.SetOutput(previous) })
			core := livePushCore(t)
			core.Store(int32(status))
			t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
			t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
			if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/retry", "k", "a", privAcctA, testSID); err != nil {
				t.Fatal(err)
			}
			SendPushToAccount(f.pool, privAcctA, "t", "b", "/sessions/"+f.pubSession)
			if !strings.Contains(output.String(), "push withheld: Core") {
				t.Fatalf("withheld delivery was not logged: %s", output.String())
			}
			subs, err := db.ListPushSubscriptions(t.Context(), f.pool)
			if err != nil || len(subs) != 1 {
				t.Fatalf("Core %d erased subscription: %+v %v", status, subs, err)
			}
		})
	}
}

func TestFailureAlertRetriesAfterCoreOutage(t *testing.T) {
	f := newNoticeFixture(t)
	core := livePushCore(t)
	core.Store(503)
	HandleSessionEnded(t.Context(), f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.sid, ExitCode: 1, Message: "runtime stopped"})
	var notified bool
	if err := f.pool.QueryRow(t.Context(), `SELECT person_notified FROM session_failure_alerts WHERE session_id=$1`, f.sid).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if notified || f.pushes.Load() != 0 {
		t.Fatalf("outage acknowledged notification: %v %d", notified, f.pushes.Load())
	}
	apiDuringOutage := &API{hub: f.hub, dbPool: f.pool}
	if err := apiDuringOutage.deliverPendingFailureAlerts(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT person_notified FROM session_failure_alerts WHERE session_id=$1`, f.sid).Scan(&notified); err != nil || notified || f.pushes.Load() != 0 {
		t.Fatalf("outage retry acknowledged notification: %v %v", notified, err)
	}
	core.Store(200)
	api := &API{hub: f.hub, dbPool: f.pool}
	if err := api.deliverPendingFailureAlerts(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.pushes.Load() != 1 {
		t.Fatalf("recovery did not retry: %d", f.pushes.Load())
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT person_notified FROM session_failure_alerts WHERE session_id=$1`, f.sid).Scan(&notified); err != nil || !notified {
		t.Fatalf("recovery not acknowledged: %v %v", notified, err)
	}
}

func TestPushTransferRequiresMatchingKeys(t *testing.T) {
	f := newPrivFixture(t)
	ep := "https://fcm.googleapis.com/sub/transfer-proof"
	if err := db.UpsertPushSubscription(t.Context(), f.pool, ep, "real-key", "real-auth", privAcctA, testSID); err != nil {
		t.Fatal(err)
	}
	for _, keys := range []string{`"p256dh":"fake","auth":"real-auth"`, `"p256dh":"real-key","auth":"fake"`} {
		r := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(`{"endpoint":"`+ep+`","keys":{`+keys+`}}`))
		r.Header.Set("Authorization", "Bearer "+f.tok(t, "human", privAcctB, coreAuthBrowserCap))
		w := httptest.NewRecorder()
		f.api.HandlePostPushSubscribe(w, r)
		if w.Code != http.StatusConflict {
			t.Fatalf("made-up keys transferred endpoint: %d", w.Code)
		}
	}
	subs, err := db.ListPushSubscriptions(t.Context(), f.pool)
	if err != nil || len(subs) != 1 || subs[0].AccountID != privAcctA {
		t.Fatalf("owner changed: %+v %v", subs, err)
	}
}

func TestPushActivityOnlySuppressesItsAccount(t *testing.T) {
	f := newPrivFixture(t)
	livePushCore(t)
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/activity", "k", "a", privAcctA, testSID); err != nil {
		t.Fatal(err)
	}
	sent := 0
	previous := sendWebPush
	sendWebPush = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = previous })
	row, err := db.GetSession(t.Context(), f.pool, f.pubSession)
	if err != nil {
		t.Fatal(err)
	}
	f.hub.MarkActivity(privAcctB)
	f.api.pushForMessage(*row, db.MessageRow{Kind: "update", Body: "owner update"})
	if sent != 1 {
		t.Fatalf("foreign activity suppressed owner: %d", sent)
	}
	f.hub.MarkActivity(privAcctA)
	f.api.pushForMessage(*row, db.MessageRow{Kind: "update", Body: "owner update"})
	if sent != 1 {
		t.Fatalf("owner activity did not suppress update: %d", sent)
	}
}

func TestPushServiceGoneRemovesSubscription(t *testing.T) {
	for _, status := range []int{404, 410} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newPrivFixture(t)
			livePushCore(t)
			t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
			t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
			if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/gone", "k", "a", privAcctA, testSID); err != nil {
				t.Fatal(err)
			}
			previous := sendWebPush
			sendWebPush = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: http.NoBody}, nil
			}
			t.Cleanup(func() { sendWebPush = previous })
			if SendPushToAccount(f.pool, privAcctA, "t", "b", "") != pushComplete {
				t.Fatal("permanently gone endpoint retried")
			}
			subs, err := db.ListPushSubscriptions(t.Context(), f.pool)
			if err != nil || len(subs) != 0 {
				t.Fatalf("gone subscription retained: %+v %v", subs, err)
			}
		})
	}
}

func TestPushCoreSlowRequestIsBounded(t *testing.T) {
	f := newPrivFixture(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	t.Setenv("BLERG_CORE_URL", srv.URL)
	t.Setenv("BLERG_RUNNER_CORE_INTERNAL_KEY", "test-internal")
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/slow", "k", "a", privAcctA, testSID); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if SendPushToAccount(f.pool, privAcctA, "t", "b", "") != pushRetry {
		t.Fatal("slow Core was acknowledged")
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("push blocked the request past the short send deadline")
	}
}

func TestPushSubscribeRejectsUnsafeEndpoints(t *testing.T) {
	f := newPrivFixture(t)
	for _, ep := range []string{"http://fcm.googleapis.com/push", "https://127.0.0.1/push", "https://[::1]/push", "https://localhost/push", "https://evil.example/push", "https://fcm.googleapis.com.evil.example/push", "https://user@fcm.googleapis.com/push", "https://fcm.googleapis.com:8080/push"} {
		r := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(`{"endpoint":"`+ep+`","keys":{"p256dh":"k","auth":"a"}}`))
		r.Header.Set("Authorization", "Bearer "+f.tok(t, "human", privAcctA, coreAuthBrowserCap))
		w := httptest.NewRecorder()
		f.api.HandlePostPushSubscribe(w, r)
		if w.Code != 422 {
			t.Errorf("unsafe endpoint %s accepted: %d", ep, w.Code)
		}
	}
}

func TestPushVerifiesEachLoginOncePerSend(t *testing.T) {
	f := newPrivFixture(t)
	var proofs atomic.Int32
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proofs.Add(1); _, _ = w.Write([]byte(`{"engines":[]}`)) }))
	defer core.Close()
	t.Setenv("BLERG_CORE_URL", core.URL)
	t.Setenv("BLERG_RUNNER_CORE_INTERNAL_KEY", "test-internal")
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	for _, ep := range []string{"first", "second", "third"} {
		if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/"+ep, "k", "a", privAcctA, testSID); err != nil {
			t.Fatal(err)
		}
	}
	sent := 0
	previous := sendWebPush
	sendWebPush = func(_ []byte, _ *webpush.Subscription, options *webpush.Options) (*http.Response, error) {
		sent++
		client, ok := options.HTTPClient.(*http.Client)
		if !ok || client.Timeout <= 0 || client.Timeout > pushSendTimeout || client.CheckRedirect == nil {
			t.Fatal("push delivery lacks a deadline or redirect fence")
		}
		return &http.Response{StatusCode: 201, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = previous })
	if SendPushToAccount(f.pool, privAcctA, "t", "b", "") != pushComplete || sent != 3 || proofs.Load() != 1 {
		t.Fatalf("send=%d Core proofs=%d", sent, proofs.Load())
	}
}
