package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log"
	"os"

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

// SendPush sends a Web Push notification to all stored push subscriptions. url
// is the path the service worker opens when the notification is clicked (e.g.
// "/sessions/<id>"); pass "" for none. VAPID keys are read from
// BLERG_RUNNER_VAPID_PUBLIC_KEY and BLERG_RUNNER_VAPID_PRIVATE_KEY environment variables.
// Delivery is best-effort — errors are logged, not returned.
func SendPush(pool *pgxpool.Pool, title, body, url string) {
	if pool == nil {
		return
	}

	vapidPublic := os.Getenv("BLERG_RUNNER_VAPID_PUBLIC_KEY")
	vapidPrivate := os.Getenv("BLERG_RUNNER_VAPID_PRIVATE_KEY")
	if vapidPublic == "" || vapidPrivate == "" {
		log.Println("SendPush: VAPID keys not configured, skipping push")
		return
	}

	ctx := context.Background()
	subs, err := db.ListPushSubscriptions(ctx, pool)
	if err != nil {
		log.Printf("SendPush: list subscriptions: %v", err)
		return
	}

	payload, err := json.Marshal(map[string]string{"title": title, "body": body, "url": url})
	if err != nil {
		log.Printf("SendPush: marshal payload: %v", err)
		return
	}

	for _, sub := range subs {
		wpSub := &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys: webpush.Keys{
				P256dh: sub.P256dh,
				Auth:   sub.Auth,
			},
		}
		resp, err := webpush.SendNotification(payload, wpSub, &webpush.Options{
			VAPIDPublicKey:  vapidPublic,
			VAPIDPrivateKey: vapidPrivate,
			TTL:             30,
			Topic:           pushTopic(url),
		})
		if err != nil {
			log.Printf("SendPush: send to %s: %v", sub.Endpoint, err)
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 400 {
			log.Printf("SendPush: push server returned %d for %s", resp.StatusCode, sub.Endpoint)
		}
	}
}
