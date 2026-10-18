// Package updatenotice finds out whether a newer public Blerg release exists, so the apps can
// show a "new version available" link beside the version they run.
//
// Core runs a Checker against GitHub's latest-release API. Board and runner run a CoreFeed that
// reads what core found from core's public GET /api/site, so GitHub is contacted by one
// component only. Everything here fails quiet: no answer means no notice.
package updatenotice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultRepo is the public repository whose releases are checked.
const DefaultRepo = "blerglab/blerg-ai"

const (
	maxBody       = 1 << 20
	httpTimeout   = 10 * time.Second
	startDelay    = 30 * time.Second
	checkInterval = 6 * time.Hour
	feedTTL       = 10 * time.Minute
)

type semver struct{ major, minor, patch int }

// parse accepts "1.2.3" or "v1.2.3" and nothing else: no prerelease, no build metadata, no
// missing parts. A tag that is not exactly that is never reported.
func parse(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var n [3]int
	for i, p := range parts {
		if p == "" || len(p) > 9 {
			return semver{}, false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return semver{}, false
			}
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return semver{}, false
		}
		n[i] = v
	}
	return semver{n[0], n[1], n[2]}, true
}

// Newer reports whether tag is a strictly newer release than running. Either side that is not a
// plain X.Y.Z (optionally with a leading v) yields false.
func Newer(running, tag string) bool {
	r, ok := parse(running)
	if !ok {
		return false
	}
	t, ok := parse(tag)
	if !ok {
		return false
	}
	if t.major != r.major {
		return t.major > r.major
	}
	if t.minor != r.minor {
		return t.minor > r.minor
	}
	return t.patch > r.patch
}

// ValidReleaseURL reports whether u is an https://github.com/ URL, the only kind a notice links to.
func ValidReleaseURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.Host != "github.com" || p.User != nil {
		return false
	}
	return strings.HasPrefix(p.Path, "/")
}

// Enabled reports whether the update check is on. BLERG_UPDATE_CHECK=0 (also false/off/no) turns it off.
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BLERG_UPDATE_CHECK"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

var repoRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)

// ValidRepo reports whether r is an owner/name pair safe to put in an API path: no query or
// fragment characters, no extra slashes, no dot segments.
func ValidRepo(r string) bool {
	if !repoRE.MatchString(r) {
		return false
	}
	for _, seg := range strings.Split(r, "/") {
		if seg == "." || seg == ".." || strings.Contains(seg, "..") {
			return false
		}
	}
	return true
}

// Repo returns the repository to check: BLERG_UPDATE_REPO or DefaultRepo. The value is not
// validated here; NewChecker disables the check when it is invalid.
func Repo() string {
	if v := strings.TrimSpace(os.Getenv("BLERG_UPDATE_REPO")); v != "" {
		return v
	}
	return DefaultRepo
}

// Checker polls GitHub for the latest release of Repo.
type Checker struct {
	// APIBase is the GitHub API origin; tests point it at a local server.
	APIBase string
	Repo    string
	// Running is the release this process runs (the VERSION file); empty disables the check.
	Running   string
	UserAgent string
	Client    *http.Client
	// Disabled makes Start and Check do nothing at all (no network call).
	Disabled bool
	// Delay is the wait before the first check; zero means 30 seconds.
	Delay time.Duration

	mu      sync.RWMutex
	version string
	url     string
	warned  bool
}

// NewChecker builds a Checker for the running release from the environment.
func NewChecker(running, userAgentVersion string) *Checker {
	c := &Checker{
		APIBase:   "https://api.github.com",
		Repo:      Repo(),
		Running:   running,
		UserAgent: "blerg-core/" + userAgentVersion,
		Client:    newClient(httpTimeout),
		Disabled:  !Enabled(),
	}
	if !c.Disabled && !ValidRepo(c.Repo) {
		log.Print("update check: BLERG_UPDATE_REPO is not an owner/name pair; the check is disabled")
		c.Disabled = true
	}
	return c
}

// newClient never follows redirects: the only traffic is to the configured API host.
func newClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Latest returns the newer release known, or "", "" when there is none.
func (c *Checker) Latest() (version, releaseURL string) {
	if c == nil {
		return "", ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version, c.url
}

func (c *Checker) set(v, u string) {
	c.mu.Lock()
	c.version, c.url = v, u
	c.mu.Unlock()
}

func (c *Checker) warnOnce(msg string) {
	c.mu.Lock()
	first := !c.warned
	c.warned = true
	c.mu.Unlock()
	if first {
		log.Printf("update check: %s", msg)
	}
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// Check makes one request and updates the known release. Any failure leaves nothing reported.
func (c *Checker) Check(ctx context.Context) {
	if c == nil || c.Disabled {
		return
	}
	if _, ok := parse(c.Running); !ok {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.APIBase, "/")+"/repos/"+c.Repo+"/releases/latest", nil)
	if err != nil {
		c.warnOnce("bad request")
		return
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := c.Client
	if client == nil {
		client = newClient(httpTimeout)
	}
	resp, err := client.Do(req)
	if err != nil {
		c.warnOnce("request failed")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		c.warnOnce(fmt.Sprintf("GitHub answered %d", resp.StatusCode))
		return
	}
	var rel githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&rel); err != nil {
		c.warnOnce("unreadable answer")
		return
	}
	// A good answer replaces what was known: nothing newer clears it. A failure keeps it.
	if rel.Draft || rel.Prerelease || !Newer(c.Running, rel.TagName) || !ValidReleaseURL(rel.HTMLURL) {
		c.set("", "")
		return
	}
	c.set(strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v"), rel.HTMLURL)
}

// Start runs Check shortly after startup (30 seconds, so it never slows boot) and then every six hours, until ctx ends.
func (c *Checker) Start(ctx context.Context) {
	if c == nil || c.Disabled {
		return
	}
	go func() {
		delay := c.Delay
		if delay <= 0 {
			delay = startDelay
		}
		t := time.NewTimer(delay)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.Check(ctx)
				t.Reset(checkInterval)
			}
		}
	}()
}

// CoreFeed reads the newer release core knows of from core's public GET /api/site, cached for
// ten minutes. Latest never blocks: it returns the cached answer and refreshes in the background.
type CoreFeed struct {
	coreURL string
	client  *http.Client

	mu         sync.Mutex
	version    string
	url        string
	fetchedAt  time.Time
	refreshing bool
}

// NewCoreFeed returns a feed for the core at coreURL (BLERG_CORE_URL), or nil when coreURL is
// empty or the check is turned off. A nil feed is valid and reports nothing.
func NewCoreFeed(coreURL string) *CoreFeed {
	if strings.TrimSpace(coreURL) == "" || !Enabled() {
		return nil
	}
	return &CoreFeed{coreURL: strings.TrimRight(coreURL, "/"), client: newClient(5 * time.Second)}
}

// Latest returns the newer release known, or "", "".
func (f *CoreFeed) Latest() (version, releaseURL string) {
	if f == nil {
		return "", ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.refreshing && time.Since(f.fetchedAt) > feedTTL {
		f.refreshing = true
		go f.refresh()
	}
	return f.version, f.url
}

func (f *CoreFeed) refresh() {
	v, u, ok := f.fetch()
	f.mu.Lock()
	if ok { // a failed read keeps the last good answer
		f.version, f.url = v, u
	}
	f.fetchedAt, f.refreshing = time.Now(), false
	f.mu.Unlock()
}

func (f *CoreFeed) fetch() (version, releaseURL string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.coreURL+"/api/site", nil)
	if err != nil {
		return "", "", false
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return "", "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", false
	}
	var site struct {
		Version string `json:"latest_version"`
		URL     string `json:"latest_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&site); err != nil {
		return "", "", false
	}
	if _, valid := parse(site.Version); !valid || !ValidReleaseURL(site.URL) {
		return "", "", true // core answered: nothing newer to show
	}
	return site.Version, site.URL, true
}
