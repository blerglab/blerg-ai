package db

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var (
	artifactSessionRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	artifactIDRe      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
)

// maxLinkLabel bounds an artifact link's label (a file name and version).
const maxLinkLabel = 300

// webKinds are the link kinds a card UI turns into a clickable address, so their URL must be a web address.
// The other kinds (rcca, doc) are not addresses the UI follows and are left as they were.
var webKinds = map[string]bool{"pr": true, "url": true, "session": true, "artifact": true}

// ValidateParamLinks checks the links a write carries, before anything is decided about the write itself.
func ValidateParamLinks(p CardParams) error {
	if p.Links != nil && p.AddLinks != nil {
		return fmt.Errorf("%w: send links (replace) or add_links (append), not both", ErrInvalidLink)
	}
	if p.Links != nil {
		if err := validateLinks(*p.Links); err != nil {
			return err
		}
	}
	if p.AddLinks != nil {
		return validateLinks(*p.AddLinks)
	}
	return nil
}

func validateParamLinks(p CardParams) error { return ValidateParamLinks(p) }

// validateLinks checks each link's shape: a pr, url, session or artifact link must be an http(s) address (an
// anchor with another scheme could run script), and an artifact link must be the runner viewer's URL for one
// file of one session, exactly (https://host/.../<session uuid>?artifact=<id>), so the artifact kind cannot
// be used to put an arbitrary URL on a card.
func validateLinks(links []Link) error {
	for _, l := range links {
		if !webKinds[l.Kind] {
			continue
		}
		u, err := url.Parse(l.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			return fmt.Errorf("%w: a %s link must be an http(s) address", ErrInvalidLink, l.Kind)
		}
		if l.Kind == "artifact" {
			if err := validateArtifactLink(u, l); err != nil {
				return fmt.Errorf("%w: %s", ErrInvalidLink, err.Error())
			}
		}
	}
	return nil
}

func validateArtifactLink(u *url.URL, l Link) error {
	if u.Fragment != "" || u.EscapedPath() != u.Path || path.Clean(u.Path) != u.Path || strings.Contains(u.Path, "//") {
		return fmt.Errorf("artifact link must have a plain path")
	}
	seg := u.Path[strings.LastIndex(u.Path, "/")+1:]
	if !artifactSessionRe.MatchString(seg) {
		return fmt.Errorf("artifact link must end in the session id (…/sessions/<id>?artifact=<file id>)")
	}
	id, ok := strings.CutPrefix(u.RawQuery, "artifact=")
	if !ok || !artifactIDRe.MatchString(id) {
		return fmt.Errorf("artifact link must carry exactly one query parameter: artifact=<file id>")
	}
	if l.Label != nil && len(*l.Label) > maxLinkLabel {
		return fmt.Errorf("artifact link label is longer than %d characters", maxLinkLabel)
	}
	return nil
}

// newLinks are the entries of add that are not already among have (by kind and URL, the table's key).
func newLinks(have, add []Link) []Link {
	seen := make(map[[2]string]bool, len(have))
	for _, l := range have {
		seen[[2]string{l.Kind, l.URL}] = true
	}
	var out []Link
	for _, l := range add {
		k := [2]string{l.Kind, l.URL}
		if !seen[k] {
			seen[k] = true
			out = append(out, l)
		}
	}
	return out
}
