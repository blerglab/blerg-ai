package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// In-app review diffs: blerg-board proxies the unified diff for a card's PR (or its
// wip branch) from GitHub, so the review box can show the change without
// leaving blerg-board. GITHUB_TOKEN (operator-synced) covers private repos.

var (
	prURLRE     = regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/pull/(\d+)`)
	branchURLRE = regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/tree/(.+)$`)
	commitURLRE = regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/commit/([0-9a-f]{7,40})`)
)

func (a *API) handleCardDiff(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	var apiURL string
	for _, l := range card.Links {
		if l.Kind == "pr" {
			if m := prURLRE.FindStringSubmatch(l.URL); m != nil {
				apiURL = fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%s", m[1], m[2], m[3])
				break
			}
		}
	}
	if apiURL == "" {
		// Fall back to the wip branch compared against the default branch.
		for _, l := range card.Links {
			if m := branchURLRE.FindStringSubmatch(l.URL); m != nil {
				apiURL = fmt.Sprintf("https://api.github.com/repos/%s/%s/compare/HEAD...%s", m[1], m[2], m[3])
				break
			}
		}
	}
	if apiURL == "" {
		writeError(w, http.StatusNotFound, "card has no PR or branch link to diff")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, apiURL, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "request build failed")
		return
	}
	req.Header.Set("Accept", "application/vnd.github.diff")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "github unreachable: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		writeError(w, http.StatusBadGateway,
			fmt.Sprintf("github: HTTP %d: %.200s", resp.StatusCode, body))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<20))
}
