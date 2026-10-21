package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// GET /api/site-board gives the runner's browser app the Board's browser-facing URL. Core's public
// GET /api/site names it, but core sends no CORS headers, so the browser cannot read it from the
// runner's origin (a different host on k8s, a different port on desktop). The runner reads it
// server-side over BLERG_CORE_URL instead, and caches the answer: a found URL for siteBoardTTL,
// a failure for siteBoardRetry, so many card links on a page cost core one request at most.
const (
	siteBoardTTL   = 5 * time.Minute
	siteBoardRetry = 30 * time.Second
)

type siteBoardCache struct {
	mu      sync.Mutex
	url     string
	expires time.Time
	client  *http.Client // nil: a 5 s client
	now     func() time.Time
}

func (c *siteBoardCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// get returns the cached Board URL, asking core when the cached answer has expired. The lock is
// held across the fetch so concurrent callers share one request.
func (c *siteBoardCache) get(ctx context.Context, coreURL string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := c.clock(); now.Before(c.expires) {
		return c.url
	}
	found := fetchSiteBoardURL(ctx, c.client, coreURL)
	c.url = found
	if found != "" {
		c.expires = c.clock().Add(siteBoardTTL)
	} else {
		c.expires = c.clock().Add(siteBoardRetry)
	}
	return found
}

func fetchSiteBoardURL(ctx context.Context, client *http.Client, coreURL string) string {
	if coreURL == "" {
		return ""
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(coreURL, "/")+"/api/site", nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var site struct {
		BoardURL string `json:"board_url"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&site) != nil {
		return ""
	}
	return cleanBoardURL(site.BoardURL)
}

// cleanBoardURL accepts only a plain http(s) base address: no credentials, query or fragment.
func cleanBoardURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return ""
	}
	return strings.TrimRight(u.String(), "/")
}

// HandleGetSiteBoard answers {"board_url": "..."}; an empty value means "not known", and the app
// falls back to its own inference.
func (a *API) HandleGetSiteBoard(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authBrowser(w, r); !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	writeJSON(w, http.StatusOK, map[string]string{"board_url": a.siteBoard.get(r.Context(), a.coreURL)})
}
