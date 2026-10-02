package mcpgw

import (
	"sync"
	"time"
)

// failLimiter counts failed authentications per key in a fixed window and blocks a key that
// reaches the limit until the window ends. Successful requests are not counted, so a busy
// legitimate session is never throttled.
type failLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time
	max    int
	byKey  map[string]*failWindow
}

type failWindow struct {
	start time.Time
	n     int
}

// maxTrackedAddrs bounds the map so an attacker spraying source addresses cannot grow it.
const maxTrackedAddrs = 10000

func newFailLimiter(limit int, window time.Duration, now func() time.Time) *failLimiter {
	return &failLimiter{limit: limit, window: window, now: now, max: maxTrackedAddrs, byKey: map[string]*failWindow{}}
}

func (l *failLimiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.byKey[key]
	if !ok {
		return false
	}
	if l.now().Sub(w.start) >= l.window {
		delete(l.byKey, key)
		return false
	}
	return w.n >= l.limit
}

func (l *failLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	w, ok := l.byKey[key]
	if !ok || now.Sub(w.start) >= l.window {
		if !ok && len(l.byKey) >= l.max {
			l.makeRoom(now)
		}
		w = &failWindow{start: now}
		l.byKey[key] = w
	}
	w.n++
}

// makeRoom frees one slot: expired windows first, and when every window is live the oldest one
// goes, so a flood of new offenders can never leave later ones untracked.
func (l *failLimiter) makeRoom(now time.Time) {
	for k, v := range l.byKey {
		if now.Sub(v.start) >= l.window {
			delete(l.byKey, k)
		}
	}
	if len(l.byKey) < l.max {
		return
	}
	var oldest string
	var oldestStart time.Time
	for k, v := range l.byKey {
		if oldest == "" || v.start.Before(oldestStart) {
			oldest, oldestStart = k, v.start
		}
	}
	delete(l.byKey, oldest)
}
