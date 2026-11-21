package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pushTopic derives the Web Push Topic (collapse key) from the notification's
// target URL. Pushes with the same Topic replace each other while queued at
// the push service, so a burst of updates for one session collapses to the
// latest instead of stacking. RFC 8030 caps Topic at 32 base64url characters;
// a hash keeps arbitrary URLs within that.
func pushTopic(url string) string {
	sum := sha256.Sum256([]byte(url))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:32]
}

// sendWebPush is webpush.SendNotification; a variable so tests can observe
// whether a delivery is attempted.
var sendWebPush = webpush.SendNotification

// pushDeliveryStatus distinguishes a completed best-effort delivery from a
// transient failure that a durable notification outbox must retry.
type pushDeliveryStatus bool

const (
	pushComplete pushDeliveryStatus = false
	pushRetry    pushDeliveryStatus = true
)
const pushSendTimeout = 2 * time.Second

// SendPushForSession calls the owner of an owned session, including public
// sessions. Ownerless public sessions call subscribed accounts that may see it.
// Optional activity suppression is evaluated for each recipient account.
func SendPushForSession(pool *pgxpool.Pool, sessionID, title, body, target string, quiet ...*Hub) pushDeliveryStatus {
	if pool == nil || sessionID == "" {
		log.Println("push withheld: session or database unavailable")
		return pushComplete
	}
	ctx, cancel := context.WithTimeout(context.Background(), pushSendTimeout)
	defer cancel()
	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil {
		log.Println("push withheld: session lookup unavailable")
		return pushRetry
	}
	if row == nil {
		log.Println("push withheld: session no longer exists")
		return pushComplete
	}
	active := func(account string) bool {
		return len(quiet) > 0 && quiet[0] != nil && quiet[0].ActiveWithin(account, activityTTL)
	}
	if owner := derefOrEmpty(row.SpawningAccountID); owner != "" {
		if active(owner) {
			log.Println("push withheld: recipient is active")
			return pushComplete
		}
		return SendPushToAccount(pool, owner, title, body, target, ctx)
	}
	if row.Private {
		log.Println("push withheld: private session has no owner")
		return pushComplete
	}
	subs, err := db.ListPushSubscriptions(ctx, pool)
	if err != nil {
		log.Println("push withheld: recipient lookup unavailable")
		return pushRetry
	}
	accounts := map[string]bool{}
	result := pushComplete
	for _, sub := range subs {
		if sub.AccountID != "" && !accounts[sub.AccountID] && canSeeAccount(sub.AccountID, row) {
			accounts[sub.AccountID] = true
			if active(sub.AccountID) {
				log.Println("push withheld: recipient is active")
				continue
			}
			if SendPushToAccount(pool, sub.AccountID, title, body, target, ctx) == pushRetry {
				result = pushRetry
			}
		}
	}
	return result
}

// SendPushToAccount never falls back to another account. A whole send has a
// short deadline, and repeated subscriptions of the same login share one proof.
func SendPushToAccount(pool *pgxpool.Pool, account, title, body, target string, parents ...context.Context) pushDeliveryStatus {
	if account == "" || pool == nil {
		log.Println("push withheld: account or database unavailable")
		return pushComplete
	}
	parent := context.Background()
	if len(parents) > 0 {
		parent = parents[0]
	}
	ctx, cancel := context.WithTimeout(parent, pushSendTimeout)
	defer cancel()
	return sendPush(ctx, pool, account, title, body, target)
}

// Only Core's canonical not-found response proves a login is gone. Auth errors,
// proxy/router 404s, malformed bodies and timeouts say nothing about its life.
func verifyPushLogin(ctx context.Context, account, login string) (bool, error) {
	coreURL, key := os.Getenv("BLERG_CORE_URL"), os.Getenv("BLERG_RUNNER_CORE_INTERNAL_KEY")
	if coreURL == "" || key == "" {
		return false, errCorePrincipalUnavailable
	}
	raw, err := json.Marshal(internalCredentialListRequest{AccountID: account, SessionID: login})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(coreURL, "/")+"/internal/credentials/list", bytes.NewReader(raw)) // #nosec G704 -- Operator-configured Core URL; redirects are disabled.
	if err != nil {
		return false, errCorePrincipalUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", key)
	resp, err := coreHTTPClient(nil).Do(req) // #nosec G704 -- Operator-configured Core URL; redirects are disabled.
	if err != nil {
		return false, errCorePrincipalUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound && isCoreNotFound(resp) {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, errCorePrincipalUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10+1))
	var result internalCredentialListResponse
	if err != nil || len(data) > 16<<10 || json.Unmarshal(data, &result) != nil || result.Engines == nil {
		return false, errCorePrincipalUnavailable
	}
	return true, nil
}

func sendPush(ctx context.Context, pool *pgxpool.Pool, account, title, body, target string) pushDeliveryStatus {
	return sendPushAt(ctx, pool, account, title, body, target, time.Now())
}

func sendPushAt(ctx context.Context, pool *pgxpool.Pool, account, title, body, target string, now time.Time) pushDeliveryStatus {
	public, private := os.Getenv("BLERG_RUNNER_VAPID_PUBLIC_KEY"), os.Getenv("BLERG_RUNNER_VAPID_PRIVATE_KEY")
	if public == "" || private == "" {
		log.Println("push withheld: VAPID keys not configured")
		return pushComplete
	}
	if id, ok := strings.CutPrefix(strings.SplitN(target, "?", 2)[0], "/sessions/"); ok {
		owners, err := db.PrivateSessionOwners(ctx, pool, []string{id})
		if err != nil {
			log.Println("push withheld: privacy lookup unavailable")
			return pushRetry
		}
		if owner, isPrivate := owners[id]; isPrivate && account != owner {
			log.Println("push withheld: private-session recipient mismatch")
			return pushComplete
		}
	}
	if holdQuietPush(ctx, pool, account, title, body, target, now) {
		return pushComplete
	}
	subs, err := db.ListPushSubscriptions(ctx, pool)
	if err != nil {
		logPersonTimeFailure("push_subscriptions", err)
		log.Println("push withheld: subscription lookup unavailable")
		return pushRetry
	}
	payload, err := json.Marshal(map[string]string{"title": title, "body": body, "url": target})
	if err != nil {
		log.Println("push withheld: invalid payload")
		return pushComplete
	}
	type proof struct {
		live bool
		err  error
	}
	proofs := map[string]proof{}
	result := pushComplete
	for _, sub := range subs {
		if sub.AccountID != account {
			continue
		}
		if sub.SessionID == "" {
			log.Println("push withheld: browser must re-register its login")
			continue
		}
		checked, ok := proofs[sub.SessionID]
		if !ok {
			checked.live, checked.err = verifyPushLogin(ctx, account, sub.SessionID)
			proofs[sub.SessionID] = checked
		}
		if checked.err != nil {
			log.Println("push withheld: Core could not verify browser login; delivery will retry")
			result = pushRetry
			continue
		}
		remove := func() {
			_, err := pool.Exec(ctx, `DELETE FROM push_subscriptions WHERE endpoint=$1 AND account_id=$2 AND session_id=$3 AND p256dh=$4 AND auth=$5`, sub.Endpoint, account, sub.SessionID, sub.P256dh, sub.Auth)
			if err != nil {
				log.Println("push cleanup unavailable; delivery will retry")
				result = pushRetry
			}
		}
		if !checked.live {
			log.Println("push withheld: browser login ended")
			remove()
			continue
		}
		deadline, _ := ctx.Deadline()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			log.Println("push withheld: delivery deadline reached; delivery will retry")
			result = pushRetry
			continue
		}
		resp, err := sendWebPush(payload, &webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth}}, &webpush.Options{
			VAPIDPublicKey: public, VAPIDPrivateKey: private, TTL: 30, Topic: pushTopic(target),
			HTTPClient: &http.Client{Timeout: remaining, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		})
		if err != nil {
			log.Println("push withheld: delivery failed; delivery will retry")
			result = pushRetry
			continue
		}
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
			remove()
		case resp.StatusCode >= 300:
			log.Printf("push withheld: push service returned %d; delivery will retry", resp.StatusCode)
			result = pushRetry
		}
	}
	return result
}

// Browsers use these public push services. Restricting registration to their
// HTTPS endpoints also prevents endpoints from becoming an internal HTTP proxy.
func validPushEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "fcm.googleapis.com" || host == "web.push.apple.com" || host == "updates.push.services.mozilla.com" || strings.HasSuffix(host, ".push.services.mozilla.com") || strings.HasSuffix(host, ".notify.windows.com")
}
