package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// handleCardGit assembles the card's git picture for the UI's Git panel:
// branch name, PR, and recent commit history — proxied from GitHub with the
// operator-synced token so private repos work.
func (a *API) handleCardGit(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	out := map[string]any{}
	var owner, repo, branch, commitsURL string
	for _, l := range card.Links {
		if l.Kind == "pr" {
			if m := prURLRE.FindStringSubmatch(l.URL); m != nil {
				owner, repo = m[1], m[2]
				out["pr_url"] = l.URL
				commitsURL = fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%s/commits?per_page=30", m[1], m[2], m[3])
			}
		}
	}
	for _, l := range card.Links {
		if m := branchURLRE.FindStringSubmatch(l.URL); m != nil {
			owner, repo, branch = m[1], m[2], m[3]
			out["branch"] = branch
			out["branch_url"] = l.URL
			if commitsURL == "" {
				commitsURL = fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?sha=%s&per_page=30", owner, repo, branch)
			}
		}
	}
	// no branch or PR — a card can still carry commit links (work done
	// outside a runner session, e.g. an operator session fixing it inline)
	if commitsURL == "" {
		commits := commitsFromLinks(r, card.Links)
		if len(commits) > 0 {
			out["commits"] = commits
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["repo"] = owner + "/" + repo

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, commitsURL, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "request build failed")
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		out["commits_error"] = fmt.Sprintf("github: HTTP %d: %.150s", resp.StatusCode, body)
		writeJSON(w, http.StatusOK, out)
		return
	}
	var raw []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
				Date string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadGateway, "github decode failed")
		return
	}
	commits := make([]map[string]string, 0, len(raw))
	for _, c := range raw {
		first := c.Commit.Message
		if i := len(first); i > 0 {
			for j, ch := range first {
				if ch == '\n' {
					first = first[:j]
					break
				}
			}
		}
		commits = append(commits, map[string]string{
			"sha": c.SHA[:7], "message": first,
			"author": c.Commit.Author.Name, "date": c.Commit.Author.Date,
			"url": c.HTMLURL,
		})
	}
	out["commits"] = commits
	writeJSON(w, http.StatusOK, out)
}

// commitsFromLinks resolves commit-URL links (kind-agnostic) against the
// GitHub commits API so inline-worked cards still get a commit history.
func commitsFromLinks(r *http.Request, links []db.Link) []map[string]string {
	client := &http.Client{Timeout: 15 * time.Second}
	var commits []map[string]string
	for _, l := range links {
		m := commitURLRE.FindStringSubmatch(l.URL)
		if m == nil {
			continue
		}
		api := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", m[1], m[2], m[3])
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, api, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var c struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
				Author  struct {
					Name string `json:"name"`
					Date string `json:"date"`
				} `json:"author"`
			} `json:"commit"`
			HTMLURL string `json:"html_url"`
		}
		err = json.NewDecoder(resp.Body).Decode(&c)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || c.SHA == "" {
			continue
		}
		msg := c.Commit.Message
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		commits = append(commits, map[string]string{
			"sha": c.SHA[:7], "message": msg,
			"author": c.Commit.Author.Name, "date": c.Commit.Author.Date,
			"url": c.HTMLURL,
		})
		if len(commits) >= 10 {
			break
		}
	}
	return commits
}
