package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
)

// ReadGit brokers a local workspace read as the current human, never as the
// board's automation account. Redirects are disabled by the runner client.
func (k *BlergRunner) ReadGit(ctx context.Context, session string, query url.Values, patch bool) ([]byte, int, error) {
	proof := desktopViewerProof(ctx)
	if proof == "" {
		return nil, 403, errors.New("sign in to read workspace Git")
	}
	path := "/api/sessions/" + url.PathEscape(session) + "/git"
	if patch {
		path += "/diff"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.url+path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, 502, errors.New("workspace Git unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+k.key)
	req.Header.Set("X-Blerg-Desktop-Viewer", proof)
	res, err := k.client.Do(req)
	if err != nil {
		return nil, 502, errors.New("workspace Git unavailable")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		message := "Workspace Git is unavailable. Keep the workspace open and connected."
		switch res.StatusCode {
		case 409:
			message = "Local Git is unavailable. Keep the workspace open and connected; update the daemon if it is older than the server."
		case 424:
			message = "Local Git objects needed for this diff are missing or unavailable. Blobless cluster clones may not contain them."
		case 403:
			message = "Only the workspace owner can read its local Git."
		case 404:
			message = "The workspace, branch, or base was not found."
		case 413:
			message = "This diff is too large to open here."
		case 422:
			message = "Choose a repository and valid local branch names."
		case 429:
			message = "Too many Git reads. Try again."
		}
		return nil, res.StatusCode, errors.New(message)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil || len(body) > 4<<20 {
		return nil, 502, errors.New("workspace Git response too large")
	}
	return body, 200, nil
}
