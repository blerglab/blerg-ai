package server

// The completion webhook (agent contract v1, spec §3).
//
// A broker that starts a session should not have to poll to learn it ended, so
// a session started with a `callback_url` gets one POST of the same result
// body the result endpoint and the SSE `end` event serve, signed with the
// caller's secret when they gave one.
//
// Two properties matter more than anything else here and shape the code:
//
//   - Exactly once. Several independent paths can mark one session terminal,
//     so delivery is claimed in the database (webhook_delivered_at, migration
//     014) before a byte is sent.
//   - Nothing secret ever escapes. The signing key is used to sign and is
//     never logged, never put in an event, and never returned by an endpoint;
//     the delivered body is not logged either (it carries the agent's own
//     prose). Failure logs name the session and a status code, nothing else.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	// webhookEventKind is the agent-event kind each delivery attempt is
	// recorded under, so an operator can answer "did the callback fire?" from
	// the same event stream as the rest of the session.
	webhookEventKind = "webhook"
	// webhookTimeout bounds one attempt (spec §3).
	webhookTimeout = 10 * time.Second
)

// webhookBackoffDefault is the retry schedule (spec §3): the initial attempt,
// then up to three retries at these delays. Overridden in tests via
// API.webhookBackoff so a retry path is exercised in milliseconds.
var webhookBackoffDefault = []time.Duration{5 * time.Second, 30 * time.Second, 120 * time.Second}

// errWebhookRedirect is returned by the client's CheckRedirect. A callback URL
// is a destination the caller named and the runner validated at start; a 3xx
// asks to send a signed body somewhere else entirely, which is exactly the
// request a compromised or misconfigured receiver would make. Refusing is also
// final rather than transient, so — unlike a network error — it is not retried.
var errWebhookRedirect = errors.New("webhook: redirects are not followed")

// errWebhookBlockedAddress is returned by the dialer when a callback host
// resolves to an address inside the runner's own network.
var errWebhookBlockedAddress = errors.New("webhook: callback address is not reachable from the public internet")

// Fixed reasons recorded on a failed attempt. They are short enum-like tokens
// so an operator can tell "we refused this" from "the receiver was down"
// without anything about the target leaking into the event.
const (
	webhookReasonBlocked  = "blocked_private_address"
	webhookReasonRedirect = "redirect_refused"
	webhookReasonInvalid  = "invalid_callback_url"
	webhookReasonNetwork  = "network_error"
	webhookReasonStatus   = "http_status"
)

// webhookAllowPrivateEnv lets an operator whose brokers live on the same
// private network as the runner (the desktop compose stack, an in-cluster
// receiver) opt back into private targets. Off by default: the runner would
// otherwise be a willing SSRF proxy for anyone who can start a session.
const webhookAllowPrivateEnv = "BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE"

// webhookSlots bounds concurrent deliveries. A retry schedule can hold a
// goroutine (and a connection) for minutes, so a burst of sessions ending at
// once — a cluster drain, say — must not turn into unbounded fan-out. Excess
// deliveries wait for a slot rather than being dropped.
var webhookSlots = make(chan struct{}, 32)

// webhookAllowPrivate reports whether private/loopback callback targets are
// permitted. Read per delivery rather than cached so an operator's change
// takes effect on restart-free config reloads.
func webhookAllowPrivate() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(webhookAllowPrivateEnv)), "true")
}

// webhookLocalhostException is the one case spec §3 allows in the clear:
// plain http to the operator's own machine, for local testing. It is only ever
// consulted together with webhookAllowPrivate (validateCallbackURL refuses such
// a URL at start otherwise), so it describes the shape of the exception, not
// the permission to use it.
func webhookLocalhostException(u *url.URL) bool {
	if u.Scheme != "http" {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// Ranges that are not "private" by net.IP.IsPrivate's definition but are just
// as much part of somebody's infrastructure — and just as attractive to an
// SSRF probe. 100.64/10 is carrier-grade NAT (and Tailscale); 0.0.0.0/8 is
// "this network", which several stacks route to the local host; 198.18/15 is
// benchmarking; 240/4 is reserved and behaves unpredictably per-stack.
var blockedExtraV4 = []*net.IPNet{
	mustCIDR("100.64.0.0/10"), // scrub:allow
	mustCIDR("0.0.0.0/8"),
	mustCIDR("198.18.0.0/15"),
	mustCIDR("240.0.0.0/4"),
}

// nat64Prefix is the well-known NAT64 prefix: 64:ff9b::/96 addresses embed an
// IPv4 address in their low 32 bits, so a blocked v4 target can be expressed
// as a v6 one. The embedded address is checked as the v4 address it is.
var nat64Prefix = mustCIDR("64:ff9b::/96")

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("webhook: bad CIDR " + s)
	}
	return n
}

// blockedWebhookIP reports whether an address is one the runner refuses to
// deliver to: its own host, its own network, or a range that belongs to
// infrastructure rather than the public internet. This is the SSRF guard — a
// caller who can name a callback URL must not be able to make the runner reach
// things only the runner can.
//
// IPv4-mapped IPv6 (::ffff:a.b.c.d) and NAT64 (64:ff9b::/96) are both just
// spellings of a v4 address, and are unwrapped before the v4 rules apply;
// otherwise either would be a way to write a blocked address that looks v6.
func blockedWebhookIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if nat64Prefix.Contains(ip) && len(ip) == net.IPv6len {
		return blockedWebhookIP(net.IPv4(ip[12], ip[13], ip[14], ip[15]))
	}
	if v4 := ip.To4(); v4 != nil {
		for _, n := range blockedExtraV4 {
			if n.Contains(v4) {
				return true
			}
		}
		ip = v4
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast()
}

// pickWebhookIP chooses which resolved address to dial. A hostname can carry
// several A/AAAA records, and an attacker only needs one of them to point
// inside: the public ones are dialable and the rest are skipped, so a mixed
// record set connects to a public address or not at all.
func pickWebhookIP(ips []net.IP, allowAll, allowLoopback bool) (net.IP, error) {
	for _, ip := range ips {
		switch {
		case allowAll, !blockedWebhookIP(ip), allowLoopback && ip.IsLoopback():
			return ip, nil
		}
	}
	return nil, errWebhookBlockedAddress
}

// webhookHTTPClient returns the client used for deliveries to one target: a
// dedicated one (not http.DefaultClient) so the timeout, redirect policy and
// address guard apply here and nowhere else, and so tests can substitute their
// own. The guard runs in DialContext, on the address actually connected to —
// not on the hostname — so a DNS name that resolves to a private address, or
// rebinds to one between attempts, is caught every time.
func (a *API) webhookHTTPClient(target *url.URL) *http.Client {
	if a.webhookClient != nil {
		return a.webhookClient
	}
	allowAll := webhookAllowPrivate()
	allowLoopback := webhookLocalhostException(target)
	dialer := &net.Dialer{Timeout: webhookTimeout}
	return &http.Client{
		Timeout: webhookTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errWebhookRedirect
		},
		Transport: &http.Transport{
			// No connection reuse: every attempt re-resolves and re-checks the
			// address it is about to talk to.
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, errWebhookBlockedAddress
				}
				addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, err
				}
				ips := make([]net.IP, 0, len(addrs))
				for _, resolved := range addrs {
					ips = append(ips, resolved.IP)
				}
				ip, err := pickWebhookIP(ips, allowAll, allowLoopback)
				if err != nil {
					return nil, err
				}
				// Dial the address that was checked, not the name: nothing can
				// re-resolve to something else in between.
				return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			},
		},
	}
}

// terminalSessionStatus reports whether a raw sessions.status value means the
// session is over, using the same projection the result endpoint publishes —
// so "what a broker is told is terminal" and "what fires the webhook" cannot
// drift apart. jobFinished is not consulted: the callers are transitions that
// have just written a status, not questions about cluster state.
func terminalSessionStatus(status string) bool {
	return runnerLifecycleTerminal(runnerLifecycle(status, false))
}

// notifyCompletion fires the completion webhook for a session that has just
// reached a terminal lifecycle. It returns immediately: delivery can take
// minutes across retries, and every caller is a request handler, a WebSocket
// reader loop, or a reconcile pass that must not be held up by a slow receiver.
//
// Safe to call on every terminal transition, for every session: a session with
// no callback_url is a no-op, and a session whose webhook already went out is
// stopped by the database claim.
func (a *API) notifyCompletion(sessionID string) {
	if a == nil || a.dbPool == nil || sessionID == "" {
		return
	}
	// Most sessions have no callback, and the overwhelmingly common case
	// should not cost a goroutine (or a delivery slot) to discover that: one
	// indexed lookup here, on the caller's time, settles it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	row, err := db.GetSession(ctx, a.dbPool, sessionID)
	cancel()
	if err != nil {
		log.Printf("webhook %s: session lookup: %v", sessionID, err)
		return
	}
	if row == nil || row.CallbackURL == nil || *row.CallbackURL == "" {
		return
	}
	go func() {
		webhookSlots <- struct{}{}
		defer func() { <-webhookSlots }()
		// Deliberately not the request's context: the handler that observed
		// the transition returns long before the retry schedule is done, and
		// cancelling delivery with it would drop the callback whenever a
		// receiver was briefly down.
		a.deliverCompletion(context.Background(), sessionID)
	}()
}

// deliverCompletion is the synchronous body of notifyCompletion (called
// directly by tests). It loads the session, claims delivery, and runs the
// attempt schedule.
func (a *API) deliverCompletion(ctx context.Context, sessionID string) {
	if a.dbPool == nil {
		return
	}
	row, err := db.GetSession(ctx, a.dbPool, sessionID)
	if err != nil {
		log.Printf("webhook %s: session lookup: %v", sessionID, err)
		return
	}
	// No row, or no callback: nothing was ever promised to anyone. The claim
	// is deliberately NOT taken here, so a session that gains a callback_url
	// later can still be delivered.
	if row == nil || row.CallbackURL == nil || *row.CallbackURL == "" {
		return
	}
	res, err := a.buildSessionResult(ctx, sessionID)
	if err != nil {
		log.Printf("webhook %s: build result: %v", sessionID, err)
		return
	}
	body, err := json.Marshal(res)
	if err != nil {
		log.Printf("webhook %s: marshal result: %v", sessionID, err)
		return
	}
	claimed, err := db.ClaimSessionWebhook(ctx, a.dbPool, sessionID)
	if err != nil {
		log.Printf("webhook %s: claim: %v", sessionID, err)
		return
	}
	if !claimed {
		// Another terminal transition (or another replica) already owns this
		// session's delivery.
		return
	}
	secret := ""
	if row.CallbackSecret != nil {
		secret = *row.CallbackSecret
	}
	a.runWebhookAttempts(ctx, sessionID, *row.CallbackURL, secret, body)
}

// parseWebhookTarget re-checks the stored callback URL at delivery time. The
// start handler already validated it, but a row can outlive the code that
// wrote it (and the rules that applied then), and this is the last point
// before the runner makes a request on a caller's behalf — so the check is
// repeated rather than assumed.
func parseWebhookTarget(raw string) (*url.URL, error) {
	if err := validateCallbackURL(raw); err != nil {
		return nil, err
	}
	return url.Parse(raw)
}

// runWebhookAttempts POSTs the body, retrying on network errors and 5xx per the
// backoff schedule. A 2xx (delivered) or a 4xx (the receiver's verdict, which
// retrying cannot change) ends delivery, and so does a refused redirect.
func (a *API) runWebhookAttempts(ctx context.Context, sessionID, rawURL, secret string, body []byte) {
	backoff := a.webhookBackoff
	if backoff == nil {
		backoff = webhookBackoffDefault
	}
	deliveryID := newUUID()
	target, err := parseWebhookTarget(rawURL)
	if err != nil {
		// Recorded as a failed attempt rather than dropped silently: "the
		// callback never fired" must always have an entry explaining why.
		log.Printf("webhook %s: stored callback URL is not deliverable", sessionID)
		a.recordWebhookAttempt(ctx, sessionID, deliveryID, 1, 0, false, webhookReasonInvalid)
		return
	}
	client := a.webhookHTTPClient(target)

	for attempt := 1; attempt <= len(backoff)+1; attempt++ {
		status, err := a.postWebhook(ctx, client, target.String(), sessionID, deliveryID, secret, body)
		ok := status >= 200 && status < 300
		a.recordWebhookAttempt(ctx, sessionID, deliveryID, attempt, status, ok, webhookAttemptReason(status, ok, err))

		switch {
		case ok:
			return
		case errors.Is(err, errWebhookBlockedAddress):
			log.Printf("webhook %s: callback address refused (private or loopback)", sessionID)
			return
		case errors.Is(err, errWebhookRedirect):
			log.Printf("webhook %s: callback URL redirected; delivery refused", sessionID)
			return
		case status >= 300 && status < 500:
			// A 4xx is the receiver's rejection of this delivery and a 3xx is
			// a redirect the runner will not follow; neither changes if the
			// same body is sent again.
			log.Printf("webhook %s: receiver returned %d, giving up", sessionID, status)
			return
		}
		if attempt > len(backoff) {
			log.Printf("webhook %s: undelivered after %d attempts", sessionID, attempt)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff[attempt-1]):
		}
	}
}

// postWebhook performs one attempt and reports the HTTP status (0 when the
// request never produced a response). The response body is discarded unread
// beyond what closing requires: nothing the receiver says is contractual.
func (a *API) postWebhook(ctx context.Context, client *http.Client, target, sessionID, deliveryID, secret string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body)) //nolint:gosec // target is the session's callback URL; webhookHTTPClient refuses private/loopback dial addresses and redirects (the SSRF guard)
	if err != nil {
		// The error text would quote the URL, which can itself carry a token,
		// so only the fixed fact is logged.
		log.Printf("webhook %s: callback URL could not be turned into a request", sessionID)
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Blerg-Event", "session.completed")
	req.Header.Set("X-Blerg-Session", sessionID)
	req.Header.Set("X-Blerg-Delivery", deliveryID)
	// Only signed when a secret was given: an HMAC over an empty key is not a
	// weaker signature, it is a forgeable one, and a receiver that checks the
	// header's presence must not be handed something that looks verified.
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Blerg-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := client.Do(req) //nolint:gosec // same request, sent by the SSRF-guarded webhook client
	if err != nil {
		// Logged as a fixed string: the error text would quote the URL, and a
		// callback URL can itself carry a token.
		if !errors.Is(err, errWebhookRedirect) && !errors.Is(err, errWebhookBlockedAddress) {
			log.Printf("webhook %s: delivery attempt failed (network error)", sessionID)
		}
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

// webhookAttemptReason names why an attempt did not succeed, as one of the
// fixed tokens above — never the transport error's text, which quotes the URL.
func webhookAttemptReason(status int, ok bool, err error) string {
	switch {
	case ok:
		return ""
	case errors.Is(err, errWebhookBlockedAddress):
		return webhookReasonBlocked
	case errors.Is(err, errWebhookRedirect):
		return webhookReasonRedirect
	case err != nil:
		return webhookReasonNetwork
	case status != 0:
		return webhookReasonStatus
	default:
		return webhookReasonNetwork
	}
}

// recordWebhookAttempt appends the per-attempt `webhook` event. The payload is
// deliberately a handful of scalar fields — never the callback URL (which can
// embed a token), never the secret, never the delivered body.
func (a *API) recordWebhookAttempt(ctx context.Context, sessionID, deliveryID string, attempt, status int, ok bool, reason string) {
	payload, err := json.Marshal(map[string]any{
		"delivery_id": deliveryID,
		"attempt":     attempt,
		"status":      status,
		"ok":          ok,
		"reason":      reason,
	})
	if err != nil {
		return
	}
	// agent_events.client_event_id is a uuid column, and each attempt is its
	// own event rather than a retry of one, so it gets its own id; the
	// delivery_id in the payload is what ties a delivery's attempts together.
	if _, _, err := db.AppendAgentEvent(ctx, a.dbPool, sessionID, newUUID(), webhookEventKind, string(payload)); err != nil {
		log.Printf("webhook %s: record attempt %d: %v", sessionID, attempt, err)
	}
}
