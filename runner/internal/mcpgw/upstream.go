package mcpgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// maxTools bounds a tools/list across all pages (spec 5.4). Tools past the cap are
// invisible and uncallable: the failure is closed.
const maxTools = 500

// maxPages bounds the pages followed, so a server cycling cursors cannot loop forever.
const maxPages = 100

// hashRefreshMaxPages bounds the pages a tools/call's hash refresh may fetch. Every page past the
// first is charged to the grant's call budget, so one call costs at most this many budget units.
// A refresh that stops at this cap MERGES into the cached hashes instead of replacing them, so a tool
// beyond the window keeps the hash the last full tools/list recorded; a tool never seen in a full
// listing is refused (fail closed) until one has run. The human-facing listings (the start-time
// check and the tool picker) are bounded by the same cap.
const hashRefreshMaxPages = 10

// hashRefreshGap is the least age of the cached listing at which a pin that does not match it is
// re-checked against the upstream. Younger than this, the mismatch is trusted, so a burst of calls
// for a changed tool costs one refetch, not one each.
const hashRefreshGap = 2 * time.Second

var (
	errBudgetExhausted = errors.New("the call budget for this session and connection is used up")
	errUpstreamAuth    = errors.New("the upstream server rejected the connection's credential")
	errUpstreamSession = errors.New("the upstream server lost its session")
)

// upstreamStatusError is an upstream reply with a non-2xx status to a request. The text is the
// gateway's own wording; ExecuteApproved reads Status to tell a definite refusal from a proxy
// that may have forwarded the call.
type upstreamStatusError struct{ Status int }

func (e *upstreamStatusError) Error() string {
	return fmt.Sprintf("the upstream server returned status %d", e.Status)
}

// grantState is the in-memory state for one grant (one gateway token): the concurrency
// limiter, the cached credential, the upstream MCP session and the last verified tool hashes.
type grantState struct {
	g   *Gateway
	sem chan struct{}

	credMu    sync.Mutex
	cred      *Credential
	credUntil time.Time

	initMu sync.Mutex
	mu     sync.Mutex // guards ready, sid, proto
	ready  bool
	sid    string
	proto  string
	nextID atomic.Int64

	hashMu   sync.Mutex
	hashes   map[string]string
	hashesAt time.Time

	inflight atomic.Int32
	lastUsed atomic.Int64 // unix nanoseconds

	// A built-in grant (board.go). builtin selects the trusted upstream client and the exchange
	// credential; the rest is what revoking needs once the grant is gone. closed is guarded by credMu.
	builtin   bool
	sessionID string
	account   string
	tokenHash []byte
	closed    bool
}

func (st *grantState) touch() { st.lastUsed.Store(st.g.now().UnixNano()) }

// tryAcquire takes one of the grant's concurrency slots without waiting.
func (st *grantState) tryAcquire() bool {
	select {
	case st.sem <- struct{}{}:
		st.inflight.Add(1)
		return true
	default:
		return false
	}
}

func (st *grantState) release() {
	st.inflight.Add(-1)
	<-st.sem
}

// hostKey is scheme, lower-cased host and effective port: what a credential's URL must
// share with the grant's snapshot.
func hostKey(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port, nil
}

var forbiddenCredentialHeaders = map[string]bool{
	"host": true, "content-length": true, "transfer-encoding": true, "connection": true, "upgrade": true,
	"te": true, "trailer": true, "mcp-session-id": true, "content-type": true, "accept": true, "cookie": true,
	"mcp-protocol-version": true,
}

// checkCredential refuses a credential the gateway must not use: a URL the network policy
// rejects, a host that differs from the grant's snapshot (so a connection edited to point
// somewhere else cannot receive the secret), or a header core should never have sent.
func (g *Gateway) checkCredential(c Credential, snapshot string) error {
	if err := g.cfg.Policy.CheckURL(c.URL); err != nil {
		return errConnectionURLRefused
	}
	have, err := hostKey(c.URL)
	if err != nil {
		return errors.New("connection url is invalid")
	}
	want, err := hostKey(snapshot)
	if err != nil || have != want {
		return errors.New("the connection's address changed since this session started")
	}
	if c.HeaderName != "" {
		if forbiddenCredentialHeaders[strings.ToLower(c.HeaderName)] || !validHeaderName(c.HeaderName) {
			return errors.New("connection header is not permitted")
		}
	}
	if strings.ContainsAny(c.Value, "\r\n\x00") {
		return errors.New("connection credential is malformed")
	}
	return nil
}

func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// credential returns the connection's credential, from memory while it is fresh (at most
// CredMaxTTL, and never past its own expiry) and from core otherwise.
func (st *grantState) credential(ctx context.Context, gr *db.MCPGrant) (Credential, error) {
	if gr.Builtin != "" {
		return st.boardCredential(ctx, gr)
	}
	st.credMu.Lock()
	defer st.credMu.Unlock()
	g := st.g
	now := g.now()
	if st.cred != nil && now.Before(st.credUntil) {
		return *st.cred, nil
	}
	st.cred = nil
	c, err := g.cfg.Core.Token(ctx, ProofFromGrant(gr.AccountID, gr.ProofKind, gr.ProofValue), gr.ConnectionID)
	if err != nil {
		return Credential{}, err
	}
	if err := g.checkCredential(c, gr.URLSnapshot); err != nil {
		return Credential{}, err
	}
	ttl := g.cfg.CredMaxTTL
	if !c.ExpiresAt.IsZero() {
		ttl = min(ttl, c.ExpiresAt.Sub(now))
	}
	if ttl > 0 {
		st.cred = &c
		st.credUntil = now.Add(ttl)
	}
	return c, nil
}

func (st *grantState) invalidateCredential() {
	st.credMu.Lock()
	st.cred = nil
	st.credMu.Unlock()
}

func (st *grantState) session() (sid, proto string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.sid, st.proto
}

func (st *grantState) resetSession(sid string) {
	st.mu.Lock()
	if st.sid == sid {
		st.ready, st.sid, st.proto = false, "", ""
	}
	st.mu.Unlock()
}

// post sends one JSON-RPC message upstream. With wantID set it returns the response
// carrying that id, whether the server answered in JSON or as an event stream; with
// wantID empty (a notification) it only checks the status. A 404 is returned as a status,
// not an error, because it may mean the upstream dropped the session.
func (st *grantState) post(ctx context.Context, cred Credential, payload []byte, wantID, sid, proto string) (rpcMessage, http.Header, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cred.URL, bytes.NewReader(payload))
	if err != nil {
		return rpcMessage{}, nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if cred.HeaderName != "" && cred.Value != "" {
		req.Header.Set(cred.HeaderName, cred.Value)
	}
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	if proto != "" {
		req.Header.Set("Mcp-Protocol-Version", proto)
	}
	client := st.g.client
	if st.builtin {
		client = st.g.boardClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return rpcMessage{}, nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return rpcMessage{}, resp.Header, resp.StatusCode, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return rpcMessage{}, resp.Header, resp.StatusCode, errUpstreamAuth
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return rpcMessage{}, resp.Header, resp.StatusCode, &upstreamStatusError{Status: resp.StatusCode}
	}
	if wantID == "" {
		return rpcMessage{}, resp.Header, resp.StatusCode, nil
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mt {
	case "application/json":
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return rpcMessage{}, resp.Header, resp.StatusCode, err
		}
		msg, err := matchJSONResponse(body, wantID)
		return msg, resp.Header, resp.StatusCode, err
	case "text/event-stream":
		msg, err := readSSEResponse(resp.Body, wantID)
		return msg, resp.Header, resp.StatusCode, err
	default:
		return rpcMessage{}, resp.Header, resp.StatusCode, fmt.Errorf("the upstream server replied with unsupported content type %q", mt)
	}
}

// matchJSONResponse extracts the response with the wanted id from a JSON reply, which a
// server may send as one object or (unusually) a single-element batch.
func matchJSONResponse(body []byte, wantID string) (rpcMessage, error) {
	body = bytes.TrimSpace(body)
	var msgs []rpcMessage
	if len(body) > 0 && body[0] == '[' {
		if err := json.Unmarshal(body, &msgs); err != nil {
			return rpcMessage{}, errors.New("the upstream server sent invalid JSON")
		}
	} else {
		var m rpcMessage
		if err := json.Unmarshal(body, &m); err != nil {
			return rpcMessage{}, errors.New("the upstream server sent invalid JSON")
		}
		msgs = []rpcMessage{m}
	}
	for _, m := range msgs {
		if m.Method == "" && m.hasID() && m.idString() == wantID {
			return m, nil
		}
	}
	return rpcMessage{}, errNoResponse
}

// ensureInit performs the upstream initialize handshake once per upstream session.
func (st *grantState) ensureInit(ctx context.Context, cred Credential) error {
	st.initMu.Lock()
	defer st.initMu.Unlock()
	st.mu.Lock()
	ready := st.ready
	st.mu.Unlock()
	if ready {
		return nil
	}
	id := strconv.FormatInt(st.nextID.Add(1), 10)
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": json.Number(id), "method": "initialize",
		"params": map[string]any{
			"protocolVersion": latestProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "blerg-gateway", "version": "1"},
		},
	})
	if err != nil {
		return err
	}
	msg, hdr, status, err := st.post(ctx, cred, payload, id, "", "")
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("the upstream server returned status %d to initialize", status)
	}
	if msg.Error != nil {
		return fmt.Errorf("the upstream server refused to initialize: %s", capRunes(msg.Error.Message, 200))
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(msg.Result, &res); err != nil {
		return errors.New("the upstream server sent an unreadable initialize result")
	}
	proto, ok := negotiateVersion(res.ProtocolVersion)
	if !ok {
		return fmt.Errorf("the upstream server speaks unsupported protocol version %q", capRunes(res.ProtocolVersion, 40))
	}
	sid := hdr.Get("Mcp-Session-Id")
	note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if _, _, status, err := st.post(ctx, cred, note, "", sid, proto); err != nil {
		return err
	} else if status < 200 || status > 299 {
		return fmt.Errorf("the upstream server returned status %d to initialized", status)
	}
	st.mu.Lock()
	st.ready, st.sid, st.proto = true, sid, proto
	st.mu.Unlock()
	return nil
}

// request sends one JSON-RPC request upstream on the grant's upstream session, initialising
// it lazily and once more after a 404 that means the session was lost. It returns the
// upstream result or the upstream's JSON-RPC error.
func (st *grantState) request(ctx context.Context, gr *db.MCPGrant, method string, params any) (json.RawMessage, *rpcError, error) {
	for attempt := 0; ; attempt++ {
		cred, err := st.credential(ctx, gr)
		if err != nil {
			return nil, nil, err
		}
		if err := st.ensureInit(ctx, cred); err != nil {
			if errors.Is(err, errUpstreamAuth) {
				st.invalidateCredential()
			}
			return nil, nil, err
		}
		sid, proto := st.session()
		id := strconv.FormatInt(st.nextID.Add(1), 10)
		payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.Number(id), "method": method, "params": params})
		if err != nil {
			return nil, nil, err
		}
		msg, _, status, err := st.post(ctx, cred, payload, id, sid, proto)
		if status == http.StatusNotFound {
			if sid != "" && attempt == 0 {
				st.resetSession(sid)
				continue
			}
			return nil, nil, errUpstreamSession
		}
		if err != nil {
			if errors.Is(err, errUpstreamAuth) {
				st.invalidateCredential()
			}
			return nil, nil, err
		}
		return msg.Result, msg.Error, nil
	}
}

type upstreamTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
	Hash        string          `json:"-"`
}

// listUpstream fetches every page of the upstream's tools/list (at most maxTools tools) and
// records each tool's hash, which tools/call re-checks for a short while.
//
// pages bounds the pages followed. With charge set, every page after the first spends one call
// of the grant's budget (errBudgetExhausted when none is left), so paging is never free.
func (st *grantState) listUpstream(ctx context.Context, gr *db.MCPGrant, pages int, charge bool) ([]upstreamTool, *rpcError, error) {
	var tools []upstreamTool
	seen := map[string]bool{}
	cursor := ""
	complete := false // the walk reached the end of the upstream's listing
	for i := range pages {
		if charge && i > 0 {
			ok, err := db.ConsumeMCPCall(ctx, st.g.cfg.DB, gr.SessionID, gr.ConnectionID)
			if err != nil {
				return nil, nil, err
			}
			if !ok {
				return nil, nil, errBudgetExhausted
			}
		}
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, rerr, err := st.request(ctx, gr, "tools/list", params)
		if err != nil || rerr != nil {
			return nil, rerr, err
		}
		var page struct {
			Tools      []upstreamTool `json:"tools"`
			NextCursor string         `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &page); err != nil {
			return nil, nil, errors.New("the upstream server sent an unreadable tool list")
		}
		for _, t := range page.Tools {
			if len(tools) >= maxTools {
				break
			}
			t.Hash = ToolHash(t.Name, t.Description, t.InputSchema)
			tools = append(tools, t)
		}
		if page.NextCursor == "" || seen[page.NextCursor] {
			complete = true
			break
		}
		if len(tools) >= maxTools {
			break
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	hashes := make(map[string]string, len(tools))
	for _, t := range tools {
		if _, dup := hashes[t.Name]; dup {
			hashes[t.Name] = "" // a name listed twice matches nothing
			continue
		}
		hashes[t.Name] = t.Hash
	}
	st.hashMu.Lock()
	if !complete && st.hashes != nil {
		// A partial walk knows only its own window: overlay it on what a fuller listing recorded, so
		// the tools past the window do not lose their hash.
		for name, h := range st.hashes {
			if _, in := hashes[name]; !in {
				hashes[name] = h
			}
		}
	}
	st.hashes, st.hashesAt = hashes, st.g.now()
	st.hashMu.Unlock()
	return tools, nil, nil
}

// currentHash returns the upstream's present hash for a tool, from the last listing when it
// is younger than HashCacheTTL and from a fresh listing otherwise. ok is false when the
// upstream does not list the tool.
//
// want is the hash the grant pinned. A cached listing that agrees with it is enough; one that
// disagrees is re-fetched (at most once per hashRefreshGap) before the tool is refused, so a tool
// restored inside the cache window is not refused on stale data. The refetch is capped at
// hashRefreshMaxPages and its extra pages are charged to the budget.
func (st *grantState) currentHash(ctx context.Context, gr *db.MCPGrant, name, want string) (hash string, ok bool, rerr *rpcError, err error) {
	now := st.g.now()
	st.hashMu.Lock()
	age := now.Sub(st.hashesAt)
	fresh := st.hashes != nil && age < st.g.cfg.HashCacheTTL
	h, has := st.hashes[name]
	st.hashMu.Unlock()
	if fresh && (h == want || age < hashRefreshGap) {
		return h, has && h != "", nil, nil
	}
	if _, rerr, err = st.listUpstream(ctx, gr, hashRefreshMaxPages, true); err != nil || rerr != nil {
		return "", false, rerr, err
	}
	st.hashMu.Lock()
	h, has = st.hashes[name]
	st.hashMu.Unlock()
	return h, has && h != "", nil, nil
}
