package gitprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxListedRepos caps how many repositories one ListMine call returns, and
// maxListPages how many API pages it reads to get there. A picker with more
// than a few hundred entries is not usable anyway, and every page is a
// request against the user's own rate limit.
const (
	MaxListedRepos = 500
	maxListPages   = 5
	perPage        = 100
	// maxPageBytes bounds one page's body: 100 repositories are well under
	// 1 MiB even in GitHub's full representation.
	maxPageBytes = 8 << 20
)

// defaultHTTPClient is used when a provider is built with a nil client.
var defaultHTTPClient = &http.Client{Timeout: 15 * time.Second}

// StatusError is a non-2xx answer from a provider's API. It carries the
// status only — never the body, which is the provider's text, not ours.
type StatusError struct {
	Provider string
	Status   int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s api: status %d", e.Provider, e.Status)
}

// listPages GETs first and follows rel="next" Link headers, decoding each
// page with decode (which returns how many items it kept) until there is no
// next page, maxListPages pages were read, or MaxListedRepos were kept.
//
// A next link is only followed when it points at the same scheme and host as
// first: the token rides along on every request, and a Link header is the
// server's input — an unexpected host there must never receive it.
func listPages(ctx context.Context, client *http.Client, providerID, first string, setAuth func(*http.Request), decode func(io.Reader) (int, error)) error {
	if client == nil {
		client = defaultHTTPClient
	}
	base, err := url.Parse(first)
	if err != nil {
		return err
	}
	next := first
	kept := 0
	for page := 0; page < maxListPages && next != "" && kept < MaxListedRepos; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return err
		}
		setAuth(req)
		resp, err := client.Do(req)
		if err != nil {
			// *url.Error embeds the request URL; ours never carries the token
			// (it travels in a header), so this is safe to return as-is.
			return err
		}
		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			return &StatusError{Provider: providerID, Status: resp.StatusCode}
		}
		n, err := decode(io.LimitReader(resp.Body, maxPageBytes))
		link := resp.Header.Get("Link")
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("%s api: decode: %w", providerID, err)
		}
		kept += n
		next = sameOriginNext(base, link)
	}
	return nil
}

// sameOriginNext returns the rel="next" URL from an RFC 8288 Link header when
// it is on base's scheme and host, else "".
func sameOriginNext(base *url.URL, header string) string {
	for _, part := range strings.Split(header, ",") {
		segs := strings.Split(part, ";")
		if len(segs) < 2 {
			continue
		}
		target := strings.TrimSpace(segs[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		isNext := false
		for _, p := range segs[1:] {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(k), "rel") {
				continue
			}
			for _, rel := range strings.Fields(strings.Trim(strings.TrimSpace(v), `"`)) {
				if strings.EqualFold(rel, "next") {
					isNext = true
				}
			}
		}
		if !isNext {
			continue
		}
		u, err := url.Parse(target[1 : len(target)-1])
		if err != nil || u.User != nil {
			return ""
		}
		if !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
			return ""
		}
		return u.String()
	}
	return ""
}

// getJSON GETs one API object into dst with setAuth applied. A non-200 is a
// *StatusError (status only, never the body).
func getJSON(ctx context.Context, client *http.Client, providerID, u string, setAuth func(*http.Request), dst any) error {
	if client == nil {
		client = defaultHTTPClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	setAuth(req)
	resp, err := client.Do(req)
	if err != nil {
		return err // the URL carries no token: it travels in a header
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return &StatusError{Provider: providerID, Status: resp.StatusCode}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPageBytes)).Decode(dst); err != nil {
		return fmt.Errorf("%s api: decode: %w", providerID, err)
	}
	return nil
}

// decodeJSONArray decodes a JSON array page into dst.
func decodeJSONArray[T any](r io.Reader, dst *[]T) error {
	return json.NewDecoder(r).Decode(dst)
}
