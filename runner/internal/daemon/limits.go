package daemon

import (
	"context"
	"errors"
	"log"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Usage limits: how much of each provider's limit windows the account this
// machine is logged in to has used. They belong to the provider account, not
// to a session, so the daemon reports them per provider (usage_limits), and
// the sidebar shows a meter per provider. Only percentages and reset times
// leave the machine.
//
//   - Codex: read from `codex app-server` (limits_codex.go) at start, every
//     10 minutes, and a few seconds after a Codex agent turn ends.
//   - Claude: read from the account usage endpoint the CLI's own usage view
//     calls, with the CLI's sign-in (limits_claude_usage.go), at start, every
//     10 minutes and a few seconds after a Claude Code turn ends. Claude also
//     says when a call is refused for its limit: a rate_limit API error that
//     names a reset (claudeRateLimitReset). The provider is then reported
//     "capped" until that time, on top of what the endpoint said.

const (
	usageLimitsInterval  = 10 * time.Minute
	usageLimitsTurnDelay = 5 * time.Second
	// usageLimitsMinGap: a connect-triggered read this soon after the last one is skipped, so a
	// flapping connection does not start a codex process per attempt.
	usageLimitsMinGap = time.Minute
	// usageLimitsWithdrawAfter failed reads in a row withdraw a provider's meters (an empty
	// report): the last percentages are not shown for ever once reads stop working.
	usageLimitsWithdrawAfter = 3
)

// errNoUsageLimits: the source has nothing to say, and that is not a failure
// worth a log line (an engine that is not installed, a setting that is off, or
// a source that already logged why itself). Wrapped errors count too.
var errNoUsageLimits = errors.New("no usage limits")

// errUsageUnchanged: the source chose not to read this time (it is backing off) and what the meter
// shows still holds. Not a failure: nothing is sent, logged or counted toward the withdraw.
var errUsageUnchanged = errors.New("usage limits unchanged")

// limitSource reads one provider's windows.
type limitSource struct {
	provider string
	read     func(ctx context.Context) ([]protocol.UsageWindow, error)
}

// UsageLimitsTracker polls the sources, keeps the latest windows per provider
// and sends them to the server. Safe for concurrent use.
type UsageLimitsTracker struct {
	send      func(msg any) error
	sources   []limitSource
	interval  time.Duration
	turnDelay time.Duration
	minGap    time.Duration
	logf      func(format string, args ...any)
	kick      chan struct{} // a turn ended (after turnDelay): read its providers now
	connected chan struct{} // the daemon (re)connected: read unless one just ran

	mu         sync.Mutex
	polled     map[string][]protocol.UsageWindow
	fetched    map[string]time.Time
	capped     map[string]time.Time // provider → when its cap lifts
	capTimers  map[string]*time.Timer
	failLogged map[string]bool
	failures   map[string]int // failed reads in a row, per provider
	lastPoll   time.Time
	turnTimer  *time.Timer
	turnsDone  map[string]bool // providers whose turn ended since their last read
}

// UsageLimitsOptions are the daemon's settings for its usage meters.
type UsageLimitsOptions struct {
	// ClaudeUsage reads Claude's limits from the account usage endpoint with the CLI's sign-in
	// (BLERG_RUNNER_CLAUDE_USAGE, on unless set to 0). Off, Claude shows only a reactive cap.
	ClaudeUsage bool
	// Version is the daemon's, for the User-Agent of the Claude read.
	Version string
}

// NewUsageLimitsTracker returns the daemon's tracker: Codex from its
// app-server, Claude from its usage endpoint when opts.ClaudeUsage (plus
// RateLimited either way). send is the connection's Send; a send while
// disconnected is dropped, and Resend on the next connect says it all again.
func NewUsageLimitsTracker(send func(msg any) error, opts UsageLimitsOptions) *UsageLimitsTracker {
	claude := func(context.Context) ([]protocol.UsageWindow, error) { return nil, errNoUsageLimits }
	if opts.ClaudeUsage {
		claude = newClaudeUsageSource(opts.Version).read
	}
	return newUsageLimitsTracker(send, []limitSource{
		{provider: "codex", read: codexLimits},
		{provider: "claude", read: claude},
	})
}

func newUsageLimitsTracker(send func(msg any) error, sources []limitSource) *UsageLimitsTracker {
	return &UsageLimitsTracker{
		send: send, sources: sources,
		interval: usageLimitsInterval, turnDelay: usageLimitsTurnDelay, minGap: usageLimitsMinGap,
		logf:       log.Printf,
		kick:       make(chan struct{}, 1),
		connected:  make(chan struct{}, 1),
		failures:   map[string]int{},
		polled:     map[string][]protocol.UsageWindow{},
		fetched:    map[string]time.Time{},
		capped:     map[string]time.Time{},
		capTimers:  map[string]*time.Timer{},
		failLogged: map[string]bool{},
		turnsDone:  map[string]bool{},
	}
}

// codexLimits is the Codex source: nothing (quietly) when codex is not
// installed.
func codexLimits(ctx context.Context) ([]protocol.UsageWindow, error) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		return nil, errNoUsageLimits
	}
	return readCodexRateLimits(ctx, bin, codexLimitsTimeout)
}

// Run reads every source on Kick (each connect) and every interval, and a provider's source after
// its turns end (CodexTurnEnded, ClaudeTurnEnded), until ctx ends. It does not read at start: a
// report sent before the connection is up is lost, and the connect's Kick reads at once.
func (t *UsageLimitsTracker) Run(ctx context.Context) {
	tick := time.NewTicker(t.interval)
	defer tick.Stop()
	defer t.stop()
	for {
		var only map[string]bool // nil: every source
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-t.kick:
			t.mu.Lock()
			only, t.turnsDone = t.turnsDone, map[string]bool{}
			t.mu.Unlock()
			if len(only) == 0 {
				continue
			}
		case <-t.connected:
			t.mu.Lock()
			recent := !t.lastPoll.IsZero() && time.Since(t.lastPoll) < t.minGap
			t.mu.Unlock()
			if recent {
				continue
			}
		}
		t.poll(ctx, only)
	}
}

// Kick asks for a read now, after a (re)connect; one within minGap of the last is skipped.
// Never blocks.
func (t *UsageLimitsTracker) Kick() {
	select {
	case t.connected <- struct{}{}:
	default:
	}
}

// stop ends the pending timers once Run is over.
func (t *UsageLimitsTracker) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.turnTimer != nil {
		t.turnTimer.Stop()
	}
	for _, tm := range t.capTimers {
		tm.Stop()
	}
}

// CodexTurnEnded asks for a read of Codex's limits shortly after a Codex turn
// ends (the turn just used some of the limit).
func (t *UsageLimitsTracker) CodexTurnEnded() { t.turnEnded("codex") }

// ClaudeTurnEnded asks for a read of Claude's limits shortly after a Claude
// Code turn ends.
func (t *UsageLimitsTracker) ClaudeTurnEnded() { t.turnEnded("claude") }

// turnEnded reads provider's source turnDelay after its turn ends. Turns
// ending in a burst, of either provider, make one read of each.
func (t *UsageLimitsTracker) turnEnded(provider string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turnsDone[provider] = true
	if t.turnTimer != nil {
		t.turnTimer.Stop()
	}
	t.turnTimer = time.AfterFunc(t.turnDelay, func() {
		select {
		case t.kick <- struct{}{}:
		default:
		}
	})
}

// ClaudeRateLimited reports Claude capped until resetsAt.
func (t *UsageLimitsTracker) ClaudeRateLimited(resetsAt time.Time) {
	t.RateLimited("claude", resetsAt)
}

// RateLimited reports provider capped until resetsAt: a "capped" window at
// 100 %, withdrawn at that time. A reset that has passed says nothing.
func (t *UsageLimitsTracker) RateLimited(provider string, resetsAt time.Time) {
	wait := time.Until(resetsAt)
	if wait <= 0 {
		return
	}
	resetsAt = resetsAt.UTC()
	t.mu.Lock()
	t.capped[provider] = resetsAt
	if old := t.capTimers[provider]; old != nil {
		old.Stop()
	}
	t.capTimers[provider] = time.AfterFunc(wait, func() {
		t.mu.Lock()
		if !t.capped[provider].Equal(resetsAt) {
			t.mu.Unlock()
			return // a later cap replaced this one
		}
		delete(t.capped, provider)
		delete(t.capTimers, provider)
		msg := t.reportLocked(provider)
		t.mu.Unlock()
		_ = t.send(msg)
	})
	msg := t.reportLocked(provider)
	t.mu.Unlock()
	_ = t.send(msg)
}

// Resend says every provider's current report again (after a reconnect).
func (t *UsageLimitsTracker) Resend() {
	t.mu.Lock()
	var msgs []protocol.UsageLimits
	for _, p := range t.providersLocked() {
		msgs = append(msgs, t.reportLocked(p))
	}
	t.mu.Unlock()
	for _, m := range msgs {
		_ = t.send(m)
	}
}

// poll reads the sources of the providers in only, or every source when only is nil.
func (t *UsageLimitsTracker) poll(ctx context.Context, only map[string]bool) {
	t.mu.Lock()
	if only == nil {
		t.lastPoll = time.Now()
		t.turnsDone = map[string]bool{} // this read covers them
	}
	t.mu.Unlock()
	for _, s := range t.sources {
		if only != nil && !only[s.provider] {
			continue
		}
		windows, err := s.read(ctx)
		if errors.Is(err, errUsageUnchanged) {
			continue
		}
		t.mu.Lock()
		if err != nil {
			if !errors.Is(err, errNoUsageLimits) && !t.failLogged[s.provider] {
				t.failLogged[s.provider] = true
				t.logf("usage limits: %s: %v (nothing reported; retried later)", s.provider, err)
			}
			t.failures[s.provider]++
			_, shown := t.polled[s.provider]
			if !shown || t.failures[s.provider] < usageLimitsWithdrawAfter {
				t.mu.Unlock()
				continue
			}
			// Reads keep failing: the last percentages are no longer the truth. Withdraw them.
			delete(t.polled, s.provider)
			delete(t.fetched, s.provider)
			msg := t.reportLocked(s.provider)
			t.mu.Unlock()
			_ = t.send(msg)
			continue
		}
		t.failLogged[s.provider] = false
		t.failures[s.provider] = 0
		t.polled[s.provider] = windows
		t.fetched[s.provider] = time.Now().UTC()
		msg := t.reportLocked(s.provider)
		t.mu.Unlock()
		_ = t.send(msg)
	}
}

// providersLocked lists the providers with something to say, sources first.
func (t *UsageLimitsTracker) providersLocked() []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, s := range t.sources {
		if _, ok := t.polled[s.provider]; ok {
			add(s.provider)
		} else if _, ok := t.capped[s.provider]; ok {
			add(s.provider)
		}
	}
	for p := range t.capped {
		add(p)
	}
	return out
}

// reportLocked is provider's message: its polled windows, plus "capped" while
// a cap holds. Empty windows withdraw what was shown.
func (t *UsageLimitsTracker) reportLocked(provider string) protocol.UsageLimits {
	windows := append([]protocol.UsageWindow{}, t.polled[provider]...)
	if reset, ok := t.capped[provider]; ok {
		// A reactive cap knows its reset; it replaces the polled "capped" (a reached spend limit has none).
		windows = slices.DeleteFunc(windows, func(w protocol.UsageWindow) bool { return w.ID == "capped" })
		windows = append(windows, protocol.UsageWindow{ID: "capped", UsedPercent: 100, ResetsAt: reset})
	}
	fetched := t.fetched[provider]
	if fetched.IsZero() {
		fetched = time.Now().UTC()
	}
	return protocol.UsageLimits{Type: "usage_limits", Provider: provider, Windows: windows, FetchedAt: fetched}
}
