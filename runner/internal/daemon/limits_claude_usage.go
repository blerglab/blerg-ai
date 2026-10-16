package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Claude's usage limits, read the way the CLI's own usage view reads them:
// GET https://api.anthropic.com/api/oauth/usage with the CLI's OAuth sign-in
// (Authorization: Bearer <accessToken>, anthropic-beta: oauth-2025-04-20). The
// answer holds, among much else, five_hour and seven_day, each
// {"utilization": 0–100, "resets_at": RFC 3339}; only those two are decoded.
//
// The sign-in is the CLI's, in ~/.claude/.credentials.json (claudeAiOauth).
// This source only ever reads that file: it takes the access token and its
// expiry, never the refresh token; it never refreshes the token and never
// writes the file. The token goes to Anthropic in that one header and nowhere
// else: it is not kept between reads, logged or sent to the Blerg server.
//
// Neither the endpoint nor the file is a documented API: both are the CLI's
// internals and may change. When they do, reads fail, the tracker logs it
// once and the meter is withdrawn.
//
//   - Not installed (no `claude` on PATH), or no claudeAiOauth block (an API
//     key, another provider): nothing, quietly.
//   - No credential file (a macOS Keychain login, a token in the daemon's
//     environment): nothing, logged once.
//   - The file readable by anyone but its owner: not read (logged once).
//   - The token past its expiry: no call until the file changes (the CLI
//     refreshes it as it runs). Logged once.
//   - 401/403: no call until the file changes. Logged once.
//   - 429 or 5xx: no call for a while, doubling from 5 minutes to an hour.
//
// While it holds back (the last three), the meter keeps its last answer for
// up to an hour (errUsageUnchanged), then is withdrawn.

const (
	claudeUsageURL       = "https://api.anthropic.com/api/oauth/usage"
	claudeUsageBeta      = "oauth-2025-04-20"
	claudeUsageUA        = "blerg-runner-usage-meter" // + "/<daemon version>"
	claudeUsageTimeout   = 10 * time.Second
	claudeUsageMinGap    = 30 * time.Second // reads closer than this reuse the last answer
	claudeUsageBackoff   = 5 * time.Minute
	claudeUsageMaxWait   = time.Hour
	claudeUsageMaxBody   = 1 << 20
	claudeCredentialsMax = 1 << 20
)

// claudeUsageSource reads Claude's windows. Safe for concurrent use.
type claudeUsageSource struct {
	credPath  func() string // "" when the home directory is unknown
	url       string
	userAgent string
	// allowPlainHTTP lets tests use an http:// fake endpoint; the daemon only ever calls https.
	allowPlainHTTP bool
	client         *http.Client
	installed      func() bool
	now            func() time.Time
	logf           func(format string, args ...any)

	mu        sync.Mutex
	blockedOn string    // the credential file's fingerprint when a read was refused for it; "" = not blocked
	waitUntil time.Time // 429/5xx: no call before this
	backoff   time.Duration
	last      []protocol.UsageWindow
	lastAt    time.Time
	logged    string // the last state logged, so each is logged once
}

func newClaudeUsageSource(version string) *claudeUsageSource {
	if version == "" {
		version = "dev"
	}
	return &claudeUsageSource{
		credPath: func() string {
			if h := homeDir(); h != "" {
				return filepath.Join(h, ".claude", ".credentials.json")
			}
			return ""
		},
		url:       claudeUsageURL,
		userAgent: claudeUsageUA + "/" + version,
		client: &http.Client{
			Timeout: claudeUsageTimeout,
			// A redirect is not followed: the token is for this one endpoint.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		installed: claudeOnPath,
		now:       time.Now,
		logf:      log.Printf,
	}
}

// quiet is an error the tracker does not log: the source logged why itself, or there is nothing
// to say.
func quiet(why string) error { return fmt.Errorf("%w: %s", errNoUsageLimits, why) }

// logOnce logs msg unless it is what was logged last. Callers hold s.mu.
func (s *claudeUsageSource) logOnce(msg string) {
	if s.logged == msg {
		return
	}
	s.logged = msg
	s.logf("claude usage meter: %s", msg)
}

// claudeOAuthFile is the part of the credential file this source decodes: the access token and
// its expiry. The refresh token is never decoded.
type claudeOAuthFile struct {
	ClaudeAiOauth *struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"` // ms since the epoch
	} `json:"claudeAiOauth"`
}

// fingerprint names a version of the credential file: a refused sign-in waits for a new one.
func fingerprint(fi os.FileInfo) string {
	return fi.ModTime().UTC().Format(time.RFC3339Nano) + "/" + strconv.FormatInt(fi.Size(), 10)
}

// skip is a read this source chose not to make (backing off, or waiting for a new sign-in): the
// meter keeps what it shows (errUsageUnchanged) while the last answer is under an hour old, and is
// withdrawn after that like any failed read. Callers hold s.mu.
func (s *claudeUsageSource) skip(now time.Time, why string) ([]protocol.UsageWindow, error) {
	if s.last != nil && now.Sub(s.lastAt) < claudeUsageMaxWait {
		return nil, errUsageUnchanged
	}
	return nil, quiet(why)
}

func (s *claudeUsageSource) read(ctx context.Context) ([]protocol.UsageWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.last != nil && now.Sub(s.lastAt) < claudeUsageMinGap {
		return s.last, nil
	}
	if !s.installed() {
		return nil, quiet("claude is not installed")
	}
	path := s.credPath()
	if path == "" {
		return nil, quiet("no home directory")
	}
	cred, err := readClaudeCredential(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.logOnce("no ~/.claude/.credentials.json (an API key, a macOS Keychain login or a token in the daemon's environment has no file to read); meter off")
		return nil, quiet("no credential file")
	case errors.Is(err, errCredentialMode):
		s.logOnce(fmt.Sprintf("~/.claude/.credentials.json is readable by other users (%v); not reading it", err))
		return nil, quiet("credential file mode")
	case err != nil:
		return nil, quiet("the credential file cannot be read")
	}
	if s.blockedOn != "" && s.blockedOn == cred.fingerprint {
		return s.skip(now, "waiting for a new sign-in")
	}
	s.blockedOn = ""
	if now.Before(s.waitUntil) {
		return s.skip(now, "backing off")
	}
	if cred.token == "" {
		return nil, quiet("no Claude sign-in in the credential file")
	}
	if cred.expiresAt > 0 && now.After(time.UnixMilli(cred.expiresAt)) {
		s.blockedOn = cred.fingerprint
		s.logOnce("the CLI's sign-in has expired; no read until the CLI refreshes it")
		return s.skip(now, "sign-in expired")
	}

	status, windows, retryAfter, err := s.fetch(ctx, cred.token)
	switch {
	case err != nil:
		return nil, err // logged once by the tracker; the token is never in it
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		s.blockedOn = cred.fingerprint
		s.logOnce(fmt.Sprintf("the usage endpoint refused the CLI's sign-in (%d); no read until it changes", status))
		return s.skip(now, "refused")
	case status == http.StatusTooManyRequests || status >= 500:
		s.backoff = min(max(2*s.backoff, claudeUsageBackoff), claudeUsageMaxWait)
		wait := max(s.backoff, min(retryAfter, claudeUsageMaxWait))
		s.waitUntil = now.Add(wait)
		s.logOnce(fmt.Sprintf("the usage endpoint answered %d; next read in %s at the earliest", status, wait.Round(time.Second)))
		return s.skip(now, "backing off")
	case status != http.StatusOK:
		return nil, fmt.Errorf("claude usage endpoint: status %d", status)
	}
	s.backoff, s.waitUntil, s.logged = 0, time.Time{}, ""
	s.last, s.lastAt = windows, now
	return windows, nil
}

// errCredentialMode: the credential file is readable by someone other than its owner.
var errCredentialMode = errors.New("credential file mode")

// claudeCredential is what one read of the CLI's credential file yields: its access token
// ("" when it holds no Claude sign-in), the token's expiry and the file's fingerprint.
type claudeCredential struct {
	token       string
	expiresAt   int64
	fingerprint string
}

// readClaudeCredential opens the CLI's credential file once and checks, on that open file, that it
// is a regular file of sane size that only its owner can read, before reading it. The file buffer
// is cleared once decoded; the decoded token is a local that lives until the request returns and
// the garbage collector reclaims it.
func readClaudeCredential(path string) (claudeCredential, error) {
	f, err := os.Open(path) //nolint:gosec // the CLI's own credential file under the user's home
	if err != nil {
		return claudeCredential{}, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return claudeCredential{}, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > claudeCredentialsMax {
		return claudeCredential{}, errors.New("not a regular file")
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return claudeCredential{}, fmt.Errorf("%w %04o", errCredentialMode, perm)
	}
	b, err := io.ReadAll(io.LimitReader(f, claudeCredentialsMax))
	if err != nil {
		return claudeCredential{}, err
	}
	defer clear(b)
	c := claudeCredential{fingerprint: fingerprint(fi)}
	var file claudeOAuthFile
	if json.Unmarshal(b, &file) == nil && file.ClaudeAiOauth != nil {
		c.token, c.expiresAt = file.ClaudeAiOauth.AccessToken, file.ClaudeAiOauth.ExpiresAt
	}
	return c, nil
}

type claudeUsageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type claudeUsageAnswer struct {
	FiveHour *claudeUsageWindow `json:"five_hour"`
	SevenDay *claudeUsageWindow `json:"seven_day"`
}

// fetch calls the endpoint once. A non-200 status comes back with no error; err is a failure to
// ask or an answer that cannot be read.
func (s *claudeUsageSource) fetch(ctx context.Context, token string) (status int, windows []protocol.UsageWindow, retryAfter time.Duration, err error) {
	ctx, cancel := context.WithTimeout(ctx, claudeUsageTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return 0, nil, 0, errors.New("claude usage endpoint: bad request")
	}
	if req.URL.Scheme != "https" && !s.allowPlainHTTP {
		return 0, nil, 0, errors.New("claude usage endpoint: not https; the token is not sent")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", claudeUsageBeta)
	req.Header.Set("User-Agent", s.userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		// A *url.Error names the URL (never the header); unwrap it to keep the line short.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, nil, 0, fmt.Errorf("claude usage endpoint: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, claudeUsageMaxBody))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			retryAfter = time.Duration(secs) * time.Second
		}
		return resp.StatusCode, nil, retryAfter, nil
	}
	var a claudeUsageAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, claudeUsageMaxBody)).Decode(&a); err != nil {
		return 0, nil, 0, errors.New("claude usage endpoint: unreadable answer")
	}
	windows = claudeUsageWindows(a)
	if len(windows) == 0 {
		return 0, nil, 0, errors.New("claude usage endpoint: no limit window in the answer")
	}
	return http.StatusOK, windows, 0, nil
}

// claudeUsageWindows maps the answer to the meter's windows: five_hour → "5h", seven_day → "7d",
// the share used floored to a whole percent as the CLI shows it, the reset in UTC. A window with no utilization is
// left out; an unreadable reset is left zero.
func claudeUsageWindows(a claudeUsageAnswer) []protocol.UsageWindow {
	var out []protocol.UsageWindow
	for _, w := range []struct {
		id  string
		win *claudeUsageWindow
	}{{"5h", a.FiveHour}, {"7d", a.SevenDay}} {
		if w.win == nil || w.win.Utilization == nil {
			continue
		}
		uw := protocol.UsageWindow{ID: w.id, UsedPercent: math.Floor(usedPercent(*w.win.Utilization))}
		if w.win.ResetsAt != nil {
			if t, err := time.Parse(time.RFC3339Nano, *w.win.ResetsAt); err == nil {
				uw.ResetsAt = t.UTC()
			}
		}
		out = append(out, uw)
	}
	return out
}
