package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// Doc links: a card link with kind "doc" points at a requirements/design doc
// worth reading without leaving blerg-board. Fetched server-side (raw-markdown
// hosts don't send CORS headers for browser fetches) and handed to the UI to
// render with MdText. The target must already be one of the card's own
// "doc" links — this is not a general proxy — and link URLs come from
// whoever can write the card (gated agents included), so the dialer resolves
// and checks every address it connects to, refusing private/loopback/
// link-local targets the same way an open HTTP proxy would need to.

var githubBlobRE = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/blob/(.+)$`)

const docMaxBytes = 1 << 20 // 1MB

var docHTTPClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: &http.Transport{DialContext: safeDialContext},
}

func (a *API) handleCardDoc(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	target := r.URL.Query().Get("url")
	found := false
	for _, l := range card.Links {
		if l.Kind == "doc" && l.URL == target {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "url is not a doc link on this card")
		return
	}

	fetchURL := target
	if m := githubBlobRE.FindStringSubmatch(target); m != nil {
		fetchURL = fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", m[1], m[2], m[3])
	}
	u, err := url.Parse(fetchURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		writeError(w, http.StatusBadRequest, "doc url must be http(s)")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, fetchURL, nil) //nolint:gosec // G704: fetchURL must equal a doc link stored on the card, is http(s) only, and docHTTPClient dials through safeDialContext which refuses private/loopback/link-local addresses
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid url")
		return
	}
	resp, err := docHTTPClient.Do(req) //nolint:gosec // G704: same request; safeDialContext checks the address actually dialled
	if err != nil {
		writeError(w, http.StatusBadGateway, "fetch failed: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("doc host: HTTP %d", resp.StatusCode))
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, docMaxBytes+1))
	if err != nil {
		writeError(w, http.StatusBadGateway, "read failed: "+err.Error())
		return
	}
	truncated := len(body) > docMaxBytes
	if truncated {
		body = body[:docMaxBytes]
		w.Header().Set("X-Doc-Truncated", "1")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(body)
}

// safeDialContext resolves the host itself and dials the resolved IP
// directly (skipping private/loopback/link-local/multicast addresses),
// so the safety check and the connection target are always the same
// address — no DNS-rebinding gap between a check and a later dial.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{}
	var lastErr error
	for _, ip := range ips {
		if isPrivateOrLocalIP(ip.IP) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no public address for %s", host)
	}
	return nil, lastErr
}

func isPrivateOrLocalIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}
