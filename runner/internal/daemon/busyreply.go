package daemon

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// A request refused for lack of a free slot gets an immediate "busy" reply, so the
// server can retry after a short pause instead of waiting out its whole deadline.
// Replies run off the read loop; busyReplies bounds them. Past that bound (a flood)
// a refusal goes unanswered, as before, and is counted in the log.
var busyReplies = make(chan struct{}, 16)

func replyBusy(kind string, send func() error) {
	select {
	case busyReplies <- struct{}{}:
		busyLog.note(kind)
		go func() {
			defer func() { <-busyReplies }()
			_ = send()
		}()
	default:
		busyLog.note(kind + "_unanswered")
	}
}

// busyReport logs refusals at most once per interval with counts, never per request.
type busyReport struct {
	mu       sync.Mutex
	interval time.Duration
	last     time.Time
	counts   map[string]int
	logf     func(string, ...any)
	now      func() time.Time
}

var busyLog = &busyReport{interval: 10 * time.Second, logf: log.Printf, now: time.Now}

func (b *busyReport) note(kind string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.counts == nil {
		b.counts = map[string]int{}
	}
	b.counts[kind]++
	now := b.now()
	if now.Sub(b.last) < b.interval {
		return
	}
	kinds := make([]string, 0, len(b.counts))
	for k := range b.counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s=%d", k, b.counts[k]))
	}
	b.logf("workspace busy: refused %s", strings.Join(parts, " "))
	b.last = now
	b.counts = map[string]int{}
}
