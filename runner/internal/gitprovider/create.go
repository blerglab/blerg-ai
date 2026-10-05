package gitprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrExists is CreateRepo's answer when owner/name is already taken. It is
// told apart from every other failure because a caller that falls back on
// "could not create" must never do so for a repository that exists but
// that the token cannot see — it would end up pushing into it.
var ErrExists = errors.New("repository already exists")

// maxErrorBody bounds how much of a provider's error body is read, and only to
// classify it (ErrExists); it is never logged or returned.
const maxErrorBody = 4096

// postJSON POSTs body as JSON to u with setAuth applied and decodes a 2xx
// answer into dst (nil = discard). A non-2xx is classify's answer when it
// returns one (it sees the status and at most maxErrorBody bytes of the
// body), else a *StatusError (status only).
func postJSON(ctx context.Context, client *http.Client, providerID, u string, setAuth func(*http.Request), body, dst any, classify func(status int, body []byte) error) error {
	if client == nil {
		client = defaultHTTPClient
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	setAuth(req)
	resp, err := client.Do(req)
	if err != nil {
		return err // the URL carries no token: it travels in a header
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		if classify != nil {
			if cerr := classify(resp.StatusCode, text); cerr != nil {
				return cerr
			}
		}
		return &StatusError{Provider: providerID, Status: resp.StatusCode}
	}
	if dst == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxPageBytes))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPageBytes)).Decode(dst); err != nil {
		return fmt.Errorf("%s api: decode: %w", providerID, err)
	}
	return nil
}

// ─── GitHub ──────────────────────────────────────────────────────────────────

func (g *GitHub) auth(token string) func(*http.Request) {
	return func(req *http.Request) {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// CreateRepo creates owner/name with a README (auto_init) so it has a default
// branch to clone. owner equal to the token's own login (GET /user) creates
// under the user (POST /user/repos), anything else under that organisation
// (POST /orgs/{owner}/repos). A 422 whose errors mention "already exists" is
// ErrExists.
func (g *GitHub) CreateRepo(ctx context.Context, token, owner, name string, private bool) error {
	if !g.ValidOwnerName(owner) || !g.ValidRepoName(name) {
		return fmt.Errorf("%s: invalid owner or repository name", g.id)
	}
	var me struct {
		Login string `json:"login"`
	}
	if err := getJSON(ctx, g.client, g.id, g.apiBase+"/user", g.auth(token), &me); err != nil {
		return err
	}
	u := g.apiBase + "/orgs/" + url.PathEscape(owner) + "/repos"
	if strings.EqualFold(me.Login, owner) {
		u = g.apiBase + "/user/repos"
	}
	body := map[string]any{"name": name, "private": private, "auto_init": true}
	return postJSON(ctx, g.client, g.id, u, g.auth(token), body, nil, func(status int, text []byte) error {
		if status == http.StatusUnprocessableEntity && bytes.Contains(bytes.ToLower(text), []byte("already exists")) {
			return ErrExists
		}
		return nil
	})
}

// ─── GitLab ──────────────────────────────────────────────────────────────────

func (g *GitLab) auth(token string) func(*http.Request) {
	return func(req *http.Request) {
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// CreateRepo creates the project owner/name with a README so it has a default
// branch to clone. owner equal to the token's own username (GET /user)
// creates in the user's namespace; anything else is looked up as a group
// (GET /namespaces/{owner}) and created there. A 400 saying "has already been
// taken" is ErrExists; a group that cannot be found is a 404 *StatusError.
func (g *GitLab) CreateRepo(ctx context.Context, token, owner, name string, private bool) error {
	if !g.ValidOwnerName(owner) || !g.ValidRepoName(name) {
		return fmt.Errorf("%s: invalid owner or project name", g.id)
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := getJSON(ctx, g.client, g.id, g.apiBase+"/user", g.auth(token), &me); err != nil {
		return err
	}
	visibility := "public"
	if private {
		visibility = "private"
	}
	body := map[string]any{"name": name, "path": name, "visibility": visibility, "initialize_with_readme": true}
	if !strings.EqualFold(me.Username, owner) {
		var ns struct {
			ID int64 `json:"id"`
		}
		if err := getJSON(ctx, g.client, g.id, g.apiBase+"/namespaces/"+url.PathEscape(owner), g.auth(token), &ns); err != nil {
			return err
		}
		if ns.ID == 0 {
			return fmt.Errorf("%s api: namespace %q has no id", g.id, owner)
		}
		body["namespace_id"] = ns.ID
	}
	return postJSON(ctx, g.client, g.id, g.apiBase+"/projects", g.auth(token), body, nil, func(status int, text []byte) error {
		if status == http.StatusBadRequest && bytes.Contains(bytes.ToLower(text), []byte("already been taken")) {
			return ErrExists
		}
		return nil
	})
}
