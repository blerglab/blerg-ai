package api

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// resolveArtifactLinks readies and checks the links of a write BEFORE the admission gate sees it (a malformed
// link must not be stored as a held review that can only fail when someone approves it). An agent publishing
// a file knows only the runner-relative path (/sessions/<id>?artifact=<file id>), so a relative artifact link
// is made absolute against the runner's UI address; an absolute one must be that same scheme, host and
// path prefix. An "artifact" link is the runner's viewer for one file, not a way to put any URL on a card.
func (a *API) resolveArtifactLinks(p *db.CardParams) error {
	for _, set := range []*[]db.Link{p.Links, p.AddLinks} {
		if set == nil {
			continue
		}
		for i := range *set {
			l := &(*set)[i]
			if l.Kind != "artifact" {
				continue
			}
			resolved, err := a.runnerArtifactURL(l.URL)
			if err != nil {
				return fmt.Errorf("%w: %s", db.ErrInvalidLink, err.Error())
			}
			l.URL = resolved
		}
	}
	return db.ValidateParamLinks(*p)
}

func (a *API) runnerArtifactURL(raw string) (string, error) {
	base := strings.TrimSpace(a.runner.UIBase)
	b, _ := url.Parse(base) // an unparsable address is treated as unset
	if base == "" || b == nil || b.Host == "" || (b.Scheme != "https" && b.Scheme != "http") {
		return "", fmt.Errorf("this board does not know the runner's address (RUNNER_UI_BASE), so it cannot link a runner file")
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") && !strings.Contains(raw, `\`) {
		raw = b.Scheme + "://" + b.Host + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("artifact link is not a URL")
	}
	if u.Scheme != b.Scheme || !strings.EqualFold(u.Host, b.Host) {
		return "", fmt.Errorf("artifact links must point at the runner (%s://%s)", b.Scheme, b.Host)
	}
	prefix := strings.TrimRight(b.Path, "/") + "/"
	rest, ok := strings.CutPrefix(u.Path, prefix)
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return "", fmt.Errorf("artifact links must be %s<session id>?artifact=<file id>", prefix)
	}
	return raw, nil
}
