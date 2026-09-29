package server

import (
	"regexp"
	"testing"
)

// pushTopic derives the Web Push Topic (collapse key) from the notification's
// target URL. RFC 8030 limits Topic to at most 32 characters from the
// base64url alphabet. Pushes with the same Topic replace each other while
// queued at the push service, so a burst of updates for one session collapses
// to the latest instead of stacking.
func TestPushTopic(t *testing.T) {
	base64url := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

	t.Run("valid per RFC 8030", func(t *testing.T) {
		for _, url := range []string{"/", "/sessions/abc-123", ""} {
			topic := pushTopic(url)
			if len(topic) == 0 || len(topic) > 32 {
				t.Errorf("pushTopic(%q) length %d, want 1..32", url, len(topic))
			}
			if !base64url.MatchString(topic) {
				t.Errorf("pushTopic(%q) = %q, not base64url-safe", url, topic)
			}
		}
	})

	t.Run("deterministic per url, distinct across urls", func(t *testing.T) {
		a1 := pushTopic("/sessions/aaa")
		a2 := pushTopic("/sessions/aaa")
		b := pushTopic("/sessions/bbb")
		if a1 != a2 {
			t.Errorf("same url produced different topics: %q vs %q", a1, a2)
		}
		if a1 == b {
			t.Errorf("different urls produced same topic %q", a1)
		}
	})
}
