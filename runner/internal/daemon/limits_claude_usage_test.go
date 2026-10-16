package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// The fake sign-in. Neither may ever show up in a log line or an error.
const (
	fakeClaudeAccessToken  = "sk-ant-oat01-FAKE-ACCESS-do-not-log"
	fakeClaudeRefreshToken = "sk-ant-ort01-FAKE-REFRESH-never-read"
)

// usageFixture is a Claude usage source over a fake credential file and a fake endpoint whose
// answers the test controls.
type usageFixture struct {
	t      *testing.T
	src    *claudeUsageSource
	cred   string
	calls  atomic.Int32
	status atomic.Int32
	body   atomic.Value // string
	now    time.Time
	logs   []string
	logMu  sync.Mutex
	gotHdr http.Header
	hdrMu  sync.Mutex
}

// fakeUsageBody has the shape of a captured answer (utilization is 0–100; most other keys left out).
const fakeUsageBody = `{"five_hour":{"utilization":41.4,"resets_at":"2026-10-15T18:59:59.655108+00:00"},` +
	`"seven_day":{"utilization":67.6,"resets_at":"2026-10-19T15:59:59.655127+00:00"},` +
	`"seven_day_opus":null,"extra_usage":{"is_enabled":false},"spend":{"x":1}}`

func newUsageFixture(t *testing.T) *usageFixture {
	t.Helper()
	f := &usageFixture{t: t, now: time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)}
	f.status.Store(http.StatusOK)
	f.body.Store(fakeUsageBody)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		f.hdrMu.Lock()
		f.gotHdr = r.Header.Clone()
		f.hdrMu.Unlock()
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/usage" {
			http.Error(w, "wrong endpoint", http.StatusNotFound)
			return
		}
		st := int(f.status.Load())
		if st == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "60")
		}
		w.WriteHeader(st)
		if st == http.StatusOK {
			_, _ = w.Write([]byte(f.body.Load().(string)))
		} else {
			// An error body that echoes the token: it must not reach a log line either.
			_, _ = fmt.Fprintf(w, `{"error":"bad token %s"}`, r.Header.Get("Authorization"))
		}
	}))
	t.Cleanup(srv.Close)
	f.cred = filepath.Join(t.TempDir(), ".credentials.json")
	f.writeCred(f.now.Add(time.Hour), 0o600)
	src := newClaudeUsageSource("test")
	src.allowPlainHTTP = true
	src.credPath = func() string { return f.cred }
	src.url = srv.URL + "/api/oauth/usage"
	src.installed = func() bool { return true }
	src.now = func() time.Time { return f.now }
	src.logf = func(format string, args ...any) {
		f.logMu.Lock()
		defer f.logMu.Unlock()
		f.logs = append(f.logs, fmt.Sprintf(format, args...))
	}
	f.src = src
	return f
}

// writeCred writes the credential file the CLI would, with the token expiring at exp, and moves
// its mtime on so a rewrite is always a new version of the file.
func (f *usageFixture) writeCred(exp time.Time, mode os.FileMode) {
	f.t.Helper()
	body := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,`+
		`"refreshTokenExpiresAt":%d,"scopes":["user:inference","user:profile"],"subscriptionType":"max",`+
		`"rateLimitTier":"default_claude_max_20x"},"mcpOAuth":{}}`,
		fakeClaudeAccessToken, fakeClaudeRefreshToken, exp.UnixMilli(), exp.Add(30*24*time.Hour).UnixMilli())
	if err := os.WriteFile(f.cred, []byte(body), mode); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(f.cred, mode); err != nil {
		f.t.Fatal(err)
	}
	mt := time.Now().Add(time.Duration(f.calls.Load()+1) * time.Minute)
	if fi, err := os.Stat(f.cred); err == nil && !fi.ModTime().Before(mt) {
		mt = fi.ModTime().Add(time.Second)
	}
	if err := os.Chtimes(f.cred, mt, mt); err != nil {
		f.t.Fatal(err)
	}
}

// later moves the fake clock past the source's minimum gap, so the next read calls again.
func (f *usageFixture) later(d time.Duration) { f.now = f.now.Add(d) }

func (f *usageFixture) read() ([]protocol.UsageWindow, error) {
	return f.src.read(context.Background())
}

func (f *usageFixture) logText() string {
	f.logMu.Lock()
	defer f.logMu.Unlock()
	return strings.Join(f.logs, "\n")
}

func TestClaudeUsageReadsAndMapsTheWindows(t *testing.T) {
	f := newUsageFixture(t)
	got, err := f.read()
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.UsageWindow{
		{ID: "5h", UsedPercent: 41, ResetsAt: time.Date(2026, 10, 15, 18, 59, 59, 655108000, time.UTC)},
		{ID: "7d", UsedPercent: 67, ResetsAt: time.Date(2026, 10, 19, 15, 59, 59, 655127000, time.UTC)},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("windows = %+v, want %+v", got, want)
	}
	f.hdrMu.Lock()
	h := f.gotHdr
	f.hdrMu.Unlock()
	if h.Get("Authorization") != "Bearer "+fakeClaudeAccessToken || h.Get("anthropic-beta") != "oauth-2025-04-20" ||
		h.Get("User-Agent") != "blerg-runner-usage-meter/test" || h.Get("Content-Type") != "" {
		t.Fatalf("headers: beta=%q ua=%q bearer ok=%v", h.Get("anthropic-beta"), h.Get("User-Agent"), h.Get("Authorization") == "Bearer "+fakeClaudeAccessToken)
	}
	// A read within the minimum gap reuses the answer; a later one asks again.
	if _, err := f.read(); err != nil || f.calls.Load() != 1 {
		t.Fatalf("a read right after: err=%v calls=%d", err, f.calls.Load())
	}
	f.later(time.Minute)
	if _, err := f.read(); err != nil || f.calls.Load() != 2 {
		t.Fatalf("a read a minute later: err=%v calls=%d", err, f.calls.Load())
	}
}

func TestClaudeUsageWindowMapping(t *testing.T) {
	pct := func(v float64) *float64 { return &v }
	str := func(v string) *string { return &v }
	got := claudeUsageWindows(claudeUsageAnswer{
		FiveHour: &claudeUsageWindow{Utilization: pct(130), ResetsAt: str("not a time")},
		SevenDay: &claudeUsageWindow{Utilization: nil, ResetsAt: str("2026-10-19T16:00:00Z")},
	})
	if len(got) != 1 || got[0] != (protocol.UsageWindow{ID: "5h", UsedPercent: 100}) {
		t.Fatalf("clamped / dropped = %+v", got)
	}
	got = claudeUsageWindows(claudeUsageAnswer{SevenDay: &claudeUsageWindow{Utilization: pct(-3), ResetsAt: str("2026-10-19T18:00:00+02:00")}})
	if len(got) != 1 || got[0] != (protocol.UsageWindow{ID: "7d", UsedPercent: 0, ResetsAt: time.Date(2026, 10, 19, 16, 0, 0, 0, time.UTC)}) {
		t.Fatalf("7d only = %+v", got)
	}
	// An answer with neither window is a failure, not an empty meter.
	f := newUsageFixture(t)
	f.body.Store(`{"five_hour":null,"seven_day":{"utilization":null}}`)
	if _, err := f.read(); err == nil || errors.Is(err, errNoUsageLimits) {
		t.Fatalf("no windows: err = %v, want a logged failure", err)
	}
}

func TestClaudeUsageNothingWithoutClaudeOrSignIn(t *testing.T) {
	f := newUsageFixture(t)
	f.src.installed = func() bool { return false }
	if _, err := f.read(); !errors.Is(err, errNoUsageLimits) {
		t.Fatalf("not installed: %v", err)
	}
	f.src.installed = func() bool { return true }
	if err := os.WriteFile(f.cred, []byte(`{"mcpOAuth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.read(); !errors.Is(err, errNoUsageLimits) {
		t.Fatalf("no claudeAiOauth: %v", err)
	}
	if f.calls.Load() != 0 || f.logText() != "" {
		t.Fatalf("calls = %d, logs = %q", f.calls.Load(), f.logText())
	}
	// No file at all (a Keychain login, a token in the daemon's environment): said once.
	if err := os.Remove(f.cred); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := f.read(); !errors.Is(err, errNoUsageLimits) {
			t.Fatalf("no file: %v", err)
		}
	}
	if f.calls.Load() != 0 || strings.Count(f.logText(), "no ~/.claude/.credentials.json") != 1 {
		t.Fatalf("calls = %d, logs = %q", f.calls.Load(), f.logText())
	}
}

func TestClaudeUsageRefusesACredentialFileOthersCanRead(t *testing.T) {
	f := newUsageFixture(t)
	f.writeCred(f.now.Add(time.Hour), 0o644)
	for range 2 {
		if _, err := f.read(); !errors.Is(err, errNoUsageLimits) {
			t.Fatalf("mode 0644: %v", err)
		}
	}
	if f.calls.Load() != 0 || strings.Count(f.logText(), "mode 0644") != 1 {
		t.Fatalf("calls = %d, logs = %q", f.calls.Load(), f.logText())
	}
	f.writeCred(f.now.Add(time.Hour), 0o600)
	if _, err := f.read(); err != nil || f.calls.Load() != 1 {
		t.Fatalf("back to 0600: err=%v calls=%d", err, f.calls.Load())
	}
}

func TestClaudeUsageSkipsAnExpiredTokenUntilTheFileChanges(t *testing.T) {
	f := newUsageFixture(t)
	f.writeCred(f.now.Add(-time.Minute), 0o600)
	for range 3 {
		if _, err := f.read(); !errors.Is(err, errNoUsageLimits) {
			t.Fatalf("expired: %v", err)
		}
		f.later(time.Hour)
	}
	if f.calls.Load() != 0 || strings.Count(f.logText(), "expired") != 1 {
		t.Fatalf("calls = %d, logs = %q", f.calls.Load(), f.logText())
	}
	// The CLI refreshed its sign-in: the file changed, and reads resume.
	f.writeCred(f.now.Add(time.Hour), 0o600)
	if _, err := f.read(); err != nil || f.calls.Load() != 1 {
		t.Fatalf("after the refresh: err=%v calls=%d", err, f.calls.Load())
	}
}

func TestClaudeUsageStopsOnRefusalUntilTheFileChanges(t *testing.T) {
	for _, st := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		f := newUsageFixture(t)
		f.status.Store(int32(st))
		for range 3 {
			if _, err := f.read(); !errors.Is(err, errNoUsageLimits) {
				t.Fatalf("%d: %v", st, err)
			}
			f.later(time.Hour)
		}
		if f.calls.Load() != 1 || strings.Count(f.logText(), "refused") != 1 {
			t.Fatalf("%d: calls = %d, logs = %q", st, f.calls.Load(), f.logText())
		}
		f.status.Store(http.StatusOK)
		f.writeCred(f.now.Add(time.Hour), 0o600)
		if _, err := f.read(); err != nil || f.calls.Load() != 2 {
			t.Fatalf("%d, new sign-in: err=%v calls=%d", st, err, f.calls.Load())
		}
	}
}

func TestClaudeUsageBacksOffOnRateLimitAndServerErrors(t *testing.T) {
	f := newUsageFixture(t)
	f.writeCred(f.now.Add(30*24*time.Hour), 0o600) // the sign-in outlives the test's hours
	f.status.Store(http.StatusServiceUnavailable)
	if _, err := f.read(); !errors.Is(err, errNoUsageLimits) {
		t.Fatalf("503: %v", err)
	}
	// 5 min, then doubling: 10, 20, 40, 60, 60.
	for i, wait := range []time.Duration{5, 10, 20, 40, 60, 60} {
		wait *= time.Minute
		before := f.calls.Load()
		f.later(wait - time.Second)
		_, _ = f.read()
		if f.calls.Load() != before {
			t.Fatalf("step %d: called before the %s wait was over", i, wait)
		}
		f.later(time.Second)
		_, _ = f.read()
		if f.calls.Load() != before+1 {
			t.Fatalf("step %d: no call once the %s wait was over", i, wait)
		}
	}
	// A 429 is backed off the same way; a success resets it.
	f.status.Store(http.StatusTooManyRequests)
	f.later(time.Hour)
	_, _ = f.read()
	f.later(time.Hour)
	f.status.Store(http.StatusOK)
	if _, err := f.read(); err != nil {
		t.Fatalf("recovered: %v", err)
	}
	f.status.Store(http.StatusTooManyRequests)
	f.later(time.Minute)
	_, _ = f.read()
	before := f.calls.Load()
	f.later(5 * time.Minute)
	_, _ = f.read()
	if f.calls.Load() != before+1 {
		t.Fatalf("after a success the back-off starts again at 5 minutes")
	}
}

// The token (and the refresh token, which is never even decoded) appears in no log line and no
// error, whatever the endpoint says, including an error body that echoes it.
func TestClaudeUsageNeverLogsTheToken(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	f := newUsageFixture(t)
	f.src.logf = log.Printf
	var errs []string
	readAll := func() {
		_, err := f.read()
		if err != nil {
			errs = append(errs, err.Error())
		}
		f.later(2 * time.Hour)
	}
	for _, st := range []int{http.StatusOK, http.StatusTeapot, http.StatusInternalServerError, http.StatusTooManyRequests, http.StatusUnauthorized} {
		f.status.Store(int32(st))
		readAll()
		f.writeCred(f.now.Add(time.Hour), 0o600)
	}
	f.status.Store(http.StatusOK)
	f.body.Store("not json")
	readAll()
	f.writeCred(f.now.Add(-time.Hour), 0o600)
	readAll()
	f.writeCred(f.now.Add(time.Hour), 0o644)
	readAll()
	f.writeCred(f.now.Add(time.Hour), 0o600)
	f.src.url = "http://127.0.0.1:1/api/oauth/usage" // nothing listens
	readAll()

	// The tracker logs what the source returns: run its errors through it too.
	tr := newUsageLimitsTracker(func(any) error { return nil }, []limitSource{{provider: "claude", read: func(context.Context) ([]protocol.UsageWindow, error) {
		return nil, errors.New(strings.Join(errs, "; "))
	}}})
	tr.poll(context.Background(), nil)

	all := buf.String() + "\n" + strings.Join(errs, "\n")
	for _, secret := range []string{fakeClaudeAccessToken, fakeClaudeRefreshToken, "FAKE-ACCESS", "FAKE-REFRESH", "Bearer"} {
		if strings.Contains(all, secret) {
			t.Fatalf("%q leaked into:\n%s", secret, all)
		}
	}
	if !strings.Contains(buf.String(), "claude usage meter:") {
		t.Fatalf("expected the source's own log lines, got:\n%s", buf.String())
	}
}

// Through the tracker: the endpoint's windows are reported for claude, and a cap from a refused
// call rides on top of them while it holds.
func TestUsageLimitsTrackerClaudeEndpointPlusCap(t *testing.T) {
	f := newUsageFixture(t)
	sent := make(chan protocol.UsageLimits, 8)
	tr := newUsageLimitsTracker(func(msg any) error { sent <- msg.(protocol.UsageLimits); return nil },
		[]limitSource{{provider: "claude", read: f.src.read}})
	tr.poll(context.Background(), nil)
	m := <-sent
	if m.Provider != "claude" || len(m.Windows) != 2 || m.Windows[0].ID != "5h" || m.Windows[1].ID != "7d" {
		t.Fatalf("report = %+v", m)
	}
	reset := time.Now().Add(time.Hour)
	tr.ClaudeRateLimited(reset)
	m = <-sent
	if len(m.Windows) != 3 || m.Windows[2] != (protocol.UsageWindow{ID: "capped", UsedPercent: 100, ResetsAt: reset.UTC()}) {
		t.Fatalf("capped report = %+v", m)
	}
	tr.stop()
}

// A reached spend limit reports "capped" with no reset; a later reactive cap (which knows its
// reset) replaces it rather than doubling it.
func TestUsageLimitsTrackerReactiveCapReplacesTheSpendCap(t *testing.T) {
	f := newUsageFixture(t)
	f.body.Store(usageFixtureBody(t, "limit_reached.json"))
	sent := make(chan protocol.UsageLimits, 8)
	tr := newUsageLimitsTracker(func(msg any) error { sent <- msg.(protocol.UsageLimits); return nil },
		[]limitSource{{provider: "claude", read: f.src.read}})
	defer tr.stop()
	tr.poll(context.Background(), nil)
	m := <-sent
	if len(m.Windows) != 2 || m.Windows[1] != (protocol.UsageWindow{ID: "capped", UsedPercent: 100}) {
		t.Fatalf("report = %+v", m)
	}
	reset := time.Now().Add(time.Hour)
	tr.ClaudeRateLimited(reset)
	m = <-sent
	if len(m.Windows) != 2 || m.Windows[1] != (protocol.UsageWindow{ID: "capped", UsedPercent: 100, ResetsAt: reset.UTC()}) {
		t.Fatalf("capped report = %+v", m)
	}
}

// A redirect is not followed (the token is for this one endpoint), and is a failure the tracker
// logs, not a quiet skip.
func TestClaudeUsageDoesNotFollowARedirect(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere.Add(1)
		_, _ = w.Write([]byte(fakeUsageBody))
	}))
	t.Cleanup(other.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/oauth/usage", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	f := newUsageFixture(t)
	f.src.url = redirect.URL + "/api/oauth/usage"
	_, err := f.read()
	if err == nil || errors.Is(err, errNoUsageLimits) || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect: err = %v, want a logged status 302 failure", err)
	}
	if elsewhere.Load() != 0 {
		t.Fatalf("the redirect was followed")
	}
}

// The token is only ever sent over https.
func TestClaudeUsageRefusesPlainHTTP(t *testing.T) {
	f := newUsageFixture(t)
	f.src.allowPlainHTTP = false
	if _, err := f.read(); err == nil || errors.Is(err, errNoUsageLimits) {
		t.Fatalf("http:// endpoint: err = %v, want a refusal", err)
	}
	if f.calls.Load() != 0 {
		t.Fatalf("the token went over plain http")
	}
}

// Turns ending inside a back-off keep the meter as it was (nothing counted toward the withdraw);
// once the last answer is over an hour old, skips count as failures and withdraw it.
func TestUsageLimitsTrackerClaudeBackoffKeepsTheMeter(t *testing.T) {
	f := newUsageFixture(t)
	f.writeCred(f.now.Add(30*24*time.Hour), 0o600)
	sent := make(chan protocol.UsageLimits, 16)
	tr := newUsageLimitsTracker(func(msg any) error { sent <- msg.(protocol.UsageLimits); return nil },
		[]limitSource{{provider: "claude", read: f.src.read}})
	tr.logf = func(string, ...any) {}
	claudeTurn := map[string]bool{"claude": true}
	tr.poll(context.Background(), claudeTurn)
	if m := <-sent; len(m.Windows) != 2 {
		t.Fatalf("first report = %+v", m)
	}
	f.status.Store(http.StatusTooManyRequests)
	for range 4 { // the first meets the 429, the next three fall inside its back-off
		f.later(time.Minute)
		tr.poll(context.Background(), claudeTurn)
	}
	select {
	case m := <-sent:
		t.Fatalf("a back-off sent %+v", m)
	default:
	}
	tr.mu.Lock()
	kept, failures := len(tr.polled["claude"]), tr.failures["claude"]
	tr.mu.Unlock()
	if f.calls.Load() != 2 || kept != 2 || failures != 0 {
		t.Fatalf("calls = %d, windows kept = %d, failures = %d", f.calls.Load(), kept, failures)
	}
	// Still refused an hour on: the last answer is stale, and three such reads withdraw it.
	for range 3 {
		f.later(2 * time.Hour)
		tr.poll(context.Background(), claudeTurn)
	}
	if m := <-sent; m.Provider != "claude" || len(m.Windows) != 0 {
		t.Fatalf("after an hour of 429s = %+v, want claude withdrawn", m)
	}
}

// usageFixtureBody reads a captured-shape answer from testdata/claude_usage. Currency is masked.
func usageFixtureBody(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "claude_usage", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestClaudeUsageSpendLimitWindows(t *testing.T) {
	r5 := time.Date(2026, 10, 15, 18, 59, 59, 655108000, time.UTC)
	r7 := time.Date(2026, 10, 19, 15, 59, 59, 655127000, time.UTC)
	cases := []struct {
		name string
		body string
		want []protocol.UsageWindow
	}{
		// A work account: no 5h / 7d, only the monthly spend limit. $70.72 of $10,000.00 is under
		// 1 %, so the fraction is kept (the meter shows "<1%", not 0 %). No reset is given.
		{"work account", "work_account.json", []protocol.UsageWindow{{ID: "month", UsedPercent: 0.7072}}},
		// A subscription with extra usage on shows all three; the share is floored like the others.
		{"subscription with extra usage", "subscription_with_extra.json", []protocol.UsageWindow{
			{ID: "5h", UsedPercent: 41, ResetsAt: r5}, {ID: "7d", UsedPercent: 67, ResetsAt: r7}, {ID: "month", UsedPercent: 50},
		}},
		{"limit reached", "limit_reached.json", []protocol.UsageWindow{{ID: "month", UsedPercent: 100}, {ID: "capped", UsedPercent: 100}}},
		// No utilization: used_credits / monthly_limit.
		{"utilization absent", "utilization_absent.json", []protocol.UsageWindow{{ID: "month", UsedPercent: 25}}},
		{"daily and weekly", "daily_weekly.json", []protocol.UsageWindow{
			{ID: "month", UsedPercent: 25}, {ID: "day", UsedPercent: 12}, {ID: "spend-week", UsedPercent: 40},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newUsageFixture(t)
			f.body.Store(usageFixtureBody(t, c.body))
			got, err := f.read()
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("windows = %+v, want %+v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("window %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestClaudeUsageSpendLimitEdgeCases(t *testing.T) {
	win := func(body string) ([]protocol.UsageWindow, error) {
		f := newUsageFixture(t)
		f.body.Store(body)
		return f.read()
	}
	ids := func(ws []protocol.UsageWindow) string {
		var s []string
		for _, w := range ws {
			s = append(s, fmt.Sprintf("%s=%v", w.ID, w.UsedPercent))
		}
		return strings.Join(s, ",")
	}
	t.Run("a disabled or limitless spend setting adds nothing", func(t *testing.T) {
		for _, body := range []string{
			usageFixtureBody(t, "extra_disabled.json"),
			`{"extra_usage":{"is_enabled":true,"monthly_limit":0,"used_credits":5,"utilization":50}}`,
			`{"extra_usage":{"is_enabled":true,"monthly_limit":-10,"utilization":50}}`,
			`{"extra_usage":{"is_enabled":true,"utilization":50}}`,
			`{"extra_usage":{"is_enabled":false,"monthly_limit":1000,"utilization":50}}`,
		} {
			if ws, err := win(body); err == nil || errors.Is(err, errNoUsageLimits) {
				t.Fatalf("%s: windows %+v, err %v; want a logged failure (no window)", body, ws, err)
			}
		}
	})
	t.Run("a disabled spend setting leaves the subscription windows alone", func(t *testing.T) {
		ws, err := win(fakeUsageBody)
		if err != nil || ids(ws) != "5h=41,7d=67" {
			t.Fatalf("got %s, %v", ids(ws), err)
		}
	})
	t.Run("a share outside 0-100 is clamped", func(t *testing.T) {
		ws, err := win(`{"extra_usage":{"is_enabled":true,"monthly_limit":100,"utilization":180}}`)
		if err != nil || ids(ws) != "month=100" {
			t.Fatalf("got %s, %v", ids(ws), err)
		}
		ws, err = win(`{"extra_usage":{"is_enabled":true,"monthly_limit":100,"used_credits":250}}`)
		if err != nil || ids(ws) != "month=100" {
			t.Fatalf("fallback: got %s, %v", ids(ws), err)
		}
	})
	t.Run("an unreadable share is not shown as 0 %", func(t *testing.T) {
		if ws, err := win(`{"extra_usage":{"is_enabled":true,"monthly_limit":100}}`); err == nil || errors.Is(err, errNoUsageLimits) {
			t.Fatalf("windows %+v, err %v; want a logged failure (no window)", ws, err)
		}
	})
	t.Run("a reached limit with no readable share is 100 % and reports the cap", func(t *testing.T) {
		ws, err := win(`{"extra_usage":{"is_enabled":true,"monthly_limit":100,"spend_limit_reached":true}}`)
		if err != nil || ids(ws) != "month=100,capped=100" {
			t.Fatalf("got %s, %v", ids(ws), err)
		}
	})
	t.Run("garbage shapes are ignored, not fatal", func(t *testing.T) {
		for body, want := range map[string]string{
			`{"extra_usage":"on","five_hour":{"utilization":10}}`:                                                              "5h=10",
			`{"extra_usage":[1,2],"five_hour":{"utilization":10}}`:                                                             "5h=10",
			`{"extra_usage":null,"five_hour":{"utilization":10}}`:                                                              "5h=10",
			`{"five_hour":"x","seven_day":{"utilization":20},"extra_usage":{"is_enabled":"yes","monthly_limit":9}}`:            "7d=20",
			`{"extra_usage":{"is_enabled":true,"monthly_limit":"1000","utilization":"5"},"five_hour":{"utilization":10}}`:      "5h=10",
			`{"extra_usage":{"is_enabled":true,"monthly_limit":100,"utilization":"5","used_credits":30}}`:                      "month=30",
			`{"extra_usage":{"is_enabled":true,"monthly_limit":100,"utilization":5,"daily":"x","weekly":{"utilization":"y"}}}`: "month=5",
			`{"extra_usage":{"is_enabled":true,"monthly_limit":100,"utilization":5,"daily":{"x":1},"weekly":[3]}}`:             "month=5",
			`{"extra_usage":{"is_enabled":true,"monthly_limit":100,"utilization":5,"spend_limit_reached":"true"}}`:             "month=5",
		} {
			ws, err := win(body)
			if err != nil || ids(ws) != want {
				t.Fatalf("%s: got %s, %v; want %s", body, ids(ws), err, want)
			}
		}
	})
}

// Only percentages and reset times leave the machine: a report built from a work-account answer
// carries no amount, currency, limit or balance, in any field it marshals.
func TestClaudeUsageSpendReportCarriesNoAmounts(t *testing.T) {
	f := newUsageFixture(t)
	f.body.Store(usageFixtureBody(t, "subscription_with_extra.json"))
	ws, err := f.read()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(protocol.UsageLimits{Type: "usage_limits", Provider: "claude", Windows: ws})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Windows []map[string]any `json:"windows"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for _, w := range back.Windows {
		for k := range w {
			if k != "id" && k != "used_percent" && k != "resets_at" {
				t.Fatalf("unexpected field %q in a window: %s", k, raw)
			}
		}
	}
	for _, leak := range []string{"XXX", "2500", "5000", "currency", "limit\"", "credit", "balance"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("report mentions %q: %s", leak, raw)
		}
	}
}
