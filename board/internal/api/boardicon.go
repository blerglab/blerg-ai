package api

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// Board icons: a board with a deploy_url borrows the project's favicon, so
// the root page shows faces instead of grey tiles. Fetched server-side
// (browsers can't cross-origin favicons reliably), cached in memory.

type iconEntry struct {
	body      []byte
	ctype     string
	fetchedAt time.Time
	ok        bool
}

var (
	iconCache   = map[string]*iconEntry{}
	iconCacheMu sync.Mutex
)

const (
	iconTTL         = 6 * time.Hour
	iconNegativeTTL = 30 * time.Minute
	iconMaxBytes    = 256 << 10
)

// Inline data-URI favicons can contain raw '>' — a <link[^>]*> tag regex
// truncates mid-URI. Match rel≈icon and a quote-delimited href in one pass,
// in either attribute order; the quoted capture is immune to '>' inside.
var iconRelFirstRE = regexp.MustCompile(`(?is)<link[^>]*?rel=["'][^"']*icon[^"']*["'][^>]*?href=(?:"([^"]+)"|'([^']+)')`)
var iconHrefFirstRE = regexp.MustCompile(`(?is)<link[^>]*?href=(?:"([^"]+)"|'([^']+)')[^>]*?rel=["'][^"']*icon`)

func (a *API) handleBoardIcon(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	board, err := db.GetBoard(r.Context(), a.Pool, boardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	if board.DeployURL == nil || *board.DeployURL == "" {
		http.NotFound(w, r)
		return
	}
	base := strings.TrimRight(*board.DeployURL, "/")

	iconCacheMu.Lock()
	e := iconCache[boardID]
	iconCacheMu.Unlock()
	if r.URL.Query().Get("refresh") != "" {
		e = nil // explicit bypass: refetch now
	}
	if e != nil {
		age := time.Since(e.fetchedAt)
		if (e.ok && age < iconTTL) || (!e.ok && age < iconNegativeTTL) {
			serveIcon(w, e)
			return
		}
	}

	e = fetchIcon(r.Context(), base)
	iconCacheMu.Lock()
	iconCache[boardID] = e
	iconCacheMu.Unlock()
	serveIcon(w, e)
}

func serveIcon(w http.ResponseWriter, e *iconEntry) {
	if !e.ok {
		http.Error(w, "no icon", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", e.ctype)
	w.Header().Set("Cache-Control", "public, max-age=21600")
	_, _ = w.Write(e.body)
}

// fetchIcon: parse the page's <link rel=icon>, fall back to /favicon.ico.
func fetchIcon(ctx context.Context, base string) *iconEntry {
	client := &http.Client{Timeout: 8 * time.Second}
	get := func(u string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		return client.Do(req)
	}
	miss := &iconEntry{fetchedAt: time.Now()}

	candidates := []string{}
	if resp, err := get(base + "/"); err == nil {
		html, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
		_ = resp.Body.Close()
		doc := string(html)
		for _, re := range []*regexp.Regexp{iconRelFirstRE, iconHrefFirstRE} {
			for _, m := range re.FindAllStringSubmatch(doc, 4) {
				href := m[1]
				if href == "" {
					href = m[2]
				}
				candidates = append(candidates, href)
			}
		}
	}
	candidates = append(candidates, "/favicon.svg", "/favicon.ico", "/favicon.png")

	bu, err := url.Parse(base + "/")
	if err != nil {
		return miss
	}
	for _, c := range candidates {
		// data: URIs are the favicon inlined in the page — decode, done
		if strings.HasPrefix(c, "data:") {
			if e := decodeDataIcon(c); e != nil {
				return e
			}
			continue
		}
		cu, err := url.Parse(c)
		if err != nil {
			continue
		}
		resp, err := get(bu.ResolveReference(cu).String())
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, iconMaxBytes))
		_ = resp.Body.Close()
		ctype := resp.Header.Get("Content-Type")
		if resp.StatusCode != http.StatusOK || len(body) == 0 ||
			strings.HasPrefix(ctype, "text/html") {
			continue
		}
		if ctype == "" {
			ctype = "image/x-icon"
		}
		return &iconEntry{body: body, ctype: ctype, fetchedAt: time.Now(), ok: true}
	}
	return miss
}

// decodeDataIcon turns data:<mediatype>[;base64],<payload> into an icon.
func decodeDataIcon(u string) *iconEntry {
	rest := strings.TrimPrefix(u, "data:")
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return nil
	}
	meta, payload := rest[:comma], rest[comma+1:]
	var body []byte
	if strings.HasSuffix(meta, ";base64") {
		meta = strings.TrimSuffix(meta, ";base64")
		b, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil
		}
		body = b
	} else {
		dec, err := url.PathUnescape(payload)
		if err != nil {
			dec = payload
		}
		body = []byte(dec)
	}
	if len(body) == 0 || len(body) > iconMaxBytes {
		return nil
	}
	ctype := meta
	if ctype == "" {
		ctype = "image/svg+xml"
	}
	return &iconEntry{body: body, ctype: ctype, fetchedAt: time.Now(), ok: true}
}
