package server

// Completion webhook (agent contract v1, spec §3): when a session with a
// callback_url reaches a terminal lifecycle the runner POSTs the result JSON,
// signed when a secret was given, retried on network errors and 5xx, and
// delivered at most once per session.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// webhookSession creates a terminal session row with the given callback, and
// returns the API (with retry delays collapsed so tests do not sleep through
// real backoff) and the session id.
func webhookSession(t *testing.T, callbackURL, secret string) (*API, *pgxpool.Pool, string) {
	t.Helper()
	api, _, pool := clusterRunnerAPI(t, &fakeK8s{})
	api.webhookBackoff = []time.Duration{0, 0, 0}
	ctx := context.Background()

	const daemonID = "00000000-0000-4000-8000-0000000000d9"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	sessionID := newUUID()
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "ended", "/workspace/org/proj", "org/proj", "T", ""); err != nil {
		t.Fatal(err)
	}
	if callbackURL != "" {
		allowLoopbackDelivery(t, callbackURL)
		if err := db.SetSessionCallback(ctx, pool, sessionID, callbackURL, secret); err != nil {
			t.Fatal(err)
		}
	}
	return api, pool, sessionID
}

// allowLoopbackDelivery opts the test process into loopback callback targets
// when (and only when) the target IS one. Since I-4 a plain-http loopback
// callback is refused unless the install has set the operator switch, and a
// local httptest receiver is precisely the case that switch exists for — the
// desktop compose stack sets it for the same reason.
func allowLoopbackDelivery(t *testing.T, callbackURL string) {
	t.Helper()
	u, err := url.Parse(callbackURL)
	if err == nil && webhookLocalhostException(u) {
		t.Setenv(webhookAllowPrivateEnv, "true")
	}
}

// webhookEvents returns the session's `webhook` agent events, oldest first.
func webhookEvents(t *testing.T, pool *pgxpool.Pool, sessionID string) []map[string]any {
	t.Helper()
	rows, err := db.ListAgentEventsTail(context.Background(), pool, sessionID, "webhook", 100)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]any, 0, len(rows))
	// ListAgentEventsTail returns newest first; report in attempt order.
	for i := len(rows) - 1; i >= 0; i-- {
		var payload map[string]any
		if err := json.Unmarshal([]byte(rows[i].Payload), &payload); err != nil {
			t.Fatalf("webhook event payload %q: %v", rows[i].Payload, err)
		}
		out = append(out, payload)
	}
	return out
}

// A delivery carries the result body, the documented headers and a signature
// that verifies against the stored secret — and happens exactly once, however
// many terminal transitions fire.
func TestWebhookDeliversSignedResultOnce(t *testing.T) {
	const secret = "s3cr3t-signing-key"
	var (
		mu    sync.Mutex
		hits  int
		gotBd []byte
		gotHd http.Header
	)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits++
		gotBd, gotHd = body, r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()

	api, pool, sessionID := webhookSession(t, recv.URL+"/hook", secret)
	api.deliverCompletion(context.Background(), sessionID)
	// A second terminal transition for the same session must not re-deliver.
	api.deliverCompletion(context.Background(), sessionID)

	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("receiver got %d requests, want exactly 1", hits)
	}
	if ct := gotHd.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if ev := gotHd.Get("X-Blerg-Event"); ev != "session.completed" {
		t.Errorf("X-Blerg-Event = %q, want session.completed", ev)
	}
	if got := gotHd.Get("X-Blerg-Session"); got != sessionID {
		t.Errorf("X-Blerg-Session = %q, want %q", got, sessionID)
	}
	delivery := gotHd.Get("X-Blerg-Delivery")
	if len(delivery) < 36 {
		t.Errorf("X-Blerg-Delivery = %q, want a uuid", delivery)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(gotBd)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := gotHd.Get("X-Blerg-Signature"); got != want {
		t.Errorf("X-Blerg-Signature = %q, want %q", got, want)
	}

	// The body is the documented result payload for this session.
	var res sessionResult
	if err := json.Unmarshal(gotBd, &res); err != nil {
		t.Fatalf("body is not a result object: %v", err)
	}
	if res.SessionID != sessionID || res.Lifecycle != "ended" || !res.Terminal {
		t.Errorf("result = %+v, want session %s / ended / terminal", res, sessionID)
	}

	events := webhookEvents(t, pool, sessionID)
	if len(events) != 1 {
		t.Fatalf("webhook events = %d, want 1: %v", len(events), events)
	}
	ev := events[0]
	if ev["delivery_id"] != delivery {
		t.Errorf("event delivery_id = %v, want %q", ev["delivery_id"], delivery)
	}
	if ev["attempt"] != float64(1) || ev["status"] != float64(200) || ev["ok"] != true {
		t.Errorf("event = %v, want attempt 1 / status 200 / ok true", ev)
	}
	// The event must never carry the secret or the delivered body.
	raw, _ := json.Marshal(ev)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "last_assistant_message") {
		t.Errorf("webhook event leaks secret or body: %s", raw)
	}
}

// No secret configured → no signature header at all (rather than a signature
// over an empty key, which a receiver could mistake for a verified delivery).
func TestWebhookWithoutSecretSendsNoSignature(t *testing.T) {
	var (
		mu  sync.Mutex
		hdr http.Header
	)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hdr = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer recv.Close()

	api, _, sessionID := webhookSession(t, recv.URL+"/hook", "")
	api.deliverCompletion(context.Background(), sessionID)

	mu.Lock()
	defer mu.Unlock()
	if hdr == nil {
		t.Fatal("receiver got no request")
	}
	if got := hdr.Get("X-Blerg-Signature"); got != "" {
		t.Errorf("X-Blerg-Signature = %q, want absent", got)
	}
}

// A 5xx is transient: retry, and stop as soon as the receiver accepts.
func TestWebhookRetriesOn5xxThenSucceeds(t *testing.T) {
	var (
		mu   sync.Mutex
		hits int
	)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()

	api, pool, sessionID := webhookSession(t, recv.URL+"/hook", "")
	api.deliverCompletion(context.Background(), sessionID)

	mu.Lock()
	got := hits
	mu.Unlock()
	if got != 2 {
		t.Fatalf("receiver got %d requests, want 2 (500 then success)", got)
	}
	events := webhookEvents(t, pool, sessionID)
	if len(events) != 2 {
		t.Fatalf("webhook events = %d, want one per attempt: %v", len(events), events)
	}
	if events[0]["status"] != float64(500) || events[0]["ok"] != false {
		t.Errorf("attempt 1 event = %v, want status 500 / ok false", events[0])
	}
	if events[1]["attempt"] != float64(2) || events[1]["ok"] != true {
		t.Errorf("attempt 2 event = %v, want attempt 2 / ok true", events[1])
	}
}

// A receiver that never recovers gets the documented number of attempts and no
// more (initial + three retries).
func TestWebhookGivesUpAfterRetries(t *testing.T) {
	var (
		mu   sync.Mutex
		hits int
	)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer recv.Close()

	api, pool, sessionID := webhookSession(t, recv.URL+"/hook", "")
	api.deliverCompletion(context.Background(), sessionID)

	mu.Lock()
	got := hits
	mu.Unlock()
	if got != 4 {
		t.Fatalf("receiver got %d requests, want 4 (initial + 3 retries)", got)
	}
	if events := webhookEvents(t, pool, sessionID); len(events) != 4 {
		t.Fatalf("webhook events = %d, want 4: %v", len(events), events)
	}
}

// A 4xx is the receiver's verdict, not a hiccup: delivery ends there.
func TestWebhookStopsOn4xx(t *testing.T) {
	var (
		mu   sync.Mutex
		hits int
	)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer recv.Close()

	api, pool, sessionID := webhookSession(t, recv.URL+"/hook", "")
	api.deliverCompletion(context.Background(), sessionID)

	mu.Lock()
	got := hits
	mu.Unlock()
	if got != 1 {
		t.Fatalf("receiver got %d requests, want 1 (4xx ends delivery)", got)
	}
	events := webhookEvents(t, pool, sessionID)
	if len(events) != 1 || events[0]["status"] != float64(400) || events[0]["ok"] != false {
		t.Fatalf("webhook events = %v, want a single 400 / ok false", events)
	}
}

// Redirects are refused: the signed body must never be replayed to a host the
// caller did not name.
func TestWebhookDoesNotFollowRedirects(t *testing.T) {
	var (
		mu               sync.Mutex
		target, redirect int
	)
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		target++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer dest.Close()
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		redirect++
		mu.Unlock()
		http.Redirect(w, r, dest.URL+"/hook", http.StatusTemporaryRedirect)
	}))
	defer recv.Close()

	api, pool, sessionID := webhookSession(t, recv.URL+"/hook", "")
	api.deliverCompletion(context.Background(), sessionID)

	mu.Lock()
	defer mu.Unlock()
	if target != 0 {
		t.Errorf("redirect target got %d requests, want 0", target)
	}
	if redirect != 1 {
		t.Errorf("callback URL got %d requests, want 1 (a refused redirect is not retried)", redirect)
	}
	events := webhookEvents(t, pool, sessionID)
	if len(events) != 1 || events[0]["ok"] != false {
		t.Fatalf("webhook events = %v, want a single failed attempt", events)
	}
}

// End-to-end through the real hooks: a session_ended from the daemon delivers
// the callback, while a daemon WS drop — which marks desktop sessions "error"
// and is routinely undone by reconcile when the daemon reattaches — must NOT.
// A false completion there would also burn the session's one delivery, so the
// test follows the disconnect with a genuine end and requires that to deliver.
func TestCompletionHooksFireOnEndNotOnDisconnect(t *testing.T) {
	var (
		mu   sync.Mutex
		hits int
	)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()
	hitCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return hits
	}

	api, hub, pool := clusterRunnerAPI(t, &fakeK8s{})
	api.webhookBackoff = []time.Duration{}
	ctx := context.Background()

	dc := &DaemonConn{ID: newUUID(), Name: "desktop", send: make(chan []byte, 8)}
	if err := db.UpsertDaemon(ctx, pool, dc.ID, dc.Name, "desktop", "/repos"); err != nil {
		t.Fatal(err)
	}
	hub.Register(dc)
	sessionID := newUUID()
	if err := db.InsertSession(ctx, pool, sessionID, dc.ID, "running", "/repos/org/proj", "org/proj", "T", ""); err != nil {
		t.Fatal(err)
	}
	allowLoopbackDelivery(t, recv.URL+"/hook")
	if err := db.SetSessionCallback(ctx, pool, sessionID, recv.URL+"/hook", ""); err != nil {
		t.Fatal(err)
	}

	// The daemon's WS drops. The session is marked "error" but is revivable.
	handleDaemonDisconnect(hub, dc, pool)
	if row, _ := db.GetSession(ctx, pool, sessionID); row == nil || row.Status != "error" {
		t.Fatalf("session after disconnect = %+v, want error", row)
	}
	time.Sleep(200 * time.Millisecond)
	if n := hitCount(); n != 0 {
		t.Fatalf("a daemon disconnect delivered %d webhooks, want 0", n)
	}

	// The session really ends: that is the delivery the caller was promised.
	HandleSessionEnded(ctx, hub, pool, protocol.SessionEnded{
		Type: "session_ended", SessionID: sessionID, ExitCode: 0,
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && hitCount() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := hitCount(); n != 1 {
		t.Fatalf("session_ended delivered %d webhooks, want 1", n)
	}
}

// The SSRF guard: a callback that resolves into the runner's own network is
// refused at dial time, with a fixed reason and without the receiver ever
// being contacted. (https to a loopback address is the shape that passes
// validateCallbackURL but must still not be dialled — the spec's only
// exception is plain http to localhost, which every other test here uses.)
func TestWebhookRefusesPrivateAddress(t *testing.T) {
	var (
		mu   sync.Mutex
		hits int
	)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()

	api, pool, sessionID := webhookSession(t, "https://"+strings.TrimPrefix(recv.URL, "http://")+"/hook", "")
	api.deliverCompletion(context.Background(), sessionID)

	mu.Lock()
	got := hits
	mu.Unlock()
	if got != 0 {
		t.Errorf("receiver got %d requests, want 0 (blocked before connect)", got)
	}
	events := webhookEvents(t, pool, sessionID)
	if len(events) != 1 {
		t.Fatalf("webhook events = %v, want a single refused attempt", events)
	}
	if events[0]["ok"] != false || events[0]["reason"] != "blocked_private_address" {
		t.Errorf("event = %v, want ok false / reason blocked_private_address", events[0])
	}
}

// An operator whose receivers live on the same private network opts back in
// with BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE=true: the address guard no longer
// refuses the target. (The attempt still fails here — the test receiver speaks
// plain HTTP, not the TLS the https URL asks for — but it fails as an ordinary
// network error, which is precisely the evidence that the dial was allowed.)
func TestWebhookAllowsPrivateAddressWithEnvOptIn(t *testing.T) {
	t.Setenv("BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE", "true")
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()

	api, pool, sessionID := webhookSession(t, "https://"+strings.TrimPrefix(recv.URL, "http://")+"/hook", "")
	api.deliverCompletion(context.Background(), sessionID)

	events := webhookEvents(t, pool, sessionID)
	if len(events) == 0 {
		t.Fatal("no webhook attempt recorded")
	}
	for _, ev := range events {
		if ev["reason"] == "blocked_private_address" {
			t.Fatalf("attempt still blocked with the opt-in set: %v", ev)
		}
	}
}

// A stored callback URL that no longer passes validation is refused before any
// request is made, and says so.
func TestWebhookRejectsInvalidStoredURL(t *testing.T) {
	api, pool, sessionID := webhookSession(t, "http://example.test/hook", "")
	api.deliverCompletion(context.Background(), sessionID)

	events := webhookEvents(t, pool, sessionID)
	if len(events) != 1 || events[0]["reason"] != "invalid_callback_url" {
		t.Fatalf("webhook events = %v, want a single invalid_callback_url attempt", events)
	}
}

// The address guard covers more than RFC1918: anything that belongs to
// infrastructure rather than the public internet, however it is spelled.
func TestBlockedWebhookIPRanges(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "0.0.0.0", // scrub:allow
		"169.254.169.254", // cloud metadata
		"100.64.0.1",      // CGNAT / Tailscale; scrub:allow
		"0.1.2.3",         // "this network"
		"198.18.0.5",      // benchmarking
		"240.0.0.1",       // reserved
		"::1", "fc00::1", "fe80::1",
		"::ffff:10.0.0.1",  // IPv4-mapped private; scrub:allow
		"::ffff:127.0.0.1", // IPv4-mapped loopback
		"64:ff9b::a00:1",   // NAT64-wrapped 10.0.0.1; scrub:allow
		"64:ff9b::7f00:1",  // NAT64-wrapped 127.0.0.1
	}
	for _, s := range blocked {
		if !blockedWebhookIP(net.ParseIP(s)) {
			t.Errorf("blockedWebhookIP(%s) = false, want true", s)
		}
	}
	public := []string{"93.184.216.34", "8.8.8.8", "1.1.1.1", "2606:4700::1111", "64:ff9b::808:808"}
	for _, s := range public {
		if blockedWebhookIP(net.ParseIP(s)) {
			t.Errorf("blockedWebhookIP(%s) = true, want false", s)
		}
	}
}

// A hostname with several A records must not become a way in: the public one
// is dialled and the private ones are skipped, whatever their order.
func TestPickWebhookIPPrefersThePublicRecord(t *testing.T) {
	mixed := []net.IP{net.ParseIP("10.0.0.7"), net.ParseIP("93.184.216.34")} // scrub:allow
	got, err := pickWebhookIP(mixed, false, false)
	if err != nil || !got.Equal(net.ParseIP("93.184.216.34")) {
		t.Fatalf("pickWebhookIP(mixed) = %v, %v; want the public address", got, err)
	}
	allPrivate := []net.IP{net.ParseIP("10.0.0.7"), net.ParseIP("192.168.0.9")} // scrub:allow
	if got, err := pickWebhookIP(allPrivate, false, false); err == nil {
		t.Fatalf("pickWebhookIP(all private) = %v, want refusal", got)
	}
	// The localhost exception and the operator opt-in each unlock loopback.
	if _, err := pickWebhookIP([]net.IP{net.ParseIP("127.0.0.1")}, false, true); err != nil {
		t.Errorf("localhost exception refused: %v", err)
	}
	if _, err := pickWebhookIP([]net.IP{net.ParseIP("10.0.0.7")}, true, false); err != nil { // scrub:allow
		t.Errorf("env opt-in refused a private address: %v", err)
	}
}

// No callback_url: nothing is sent, nothing is recorded, and the delivery
// marker is left unset so a later-configured callback is still possible.
func TestWebhookNoCallbackURLIsANoOp(t *testing.T) {
	api, pool, sessionID := webhookSession(t, "", "")
	api.deliverCompletion(context.Background(), sessionID)

	if events := webhookEvents(t, pool, sessionID); len(events) != 0 {
		t.Fatalf("webhook events = %v, want none", events)
	}
	var delivered *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT webhook_delivered_at FROM sessions WHERE id = $1`, sessionID).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered != nil {
		t.Errorf("webhook_delivered_at = %v, want NULL", delivered)
	}
}
