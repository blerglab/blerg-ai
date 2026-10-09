// Package chatproxy is the reference proxy for the @blerglab/chat package.
//
// This file is meant to be COPIED into an app, not imported: apps cannot
// depend on the private Blerg module, and the proxy is small enough to own.
// Copy chatproxy.go next to your HTTP server, implement RunnerClient over the
// authenticated runner client the app already has, write an Authorize
// function over the app's own sign-in, and call Mount.
//
// The proxy exposes the chat package's proxy contract under a prefix (for
// example "/blerg") and forwards each route to the runner's v1 session
// operations, one session per request, with the app's agent token. The token
// never appears in a response, in a log line or in a header sent back to the
// browser; the browser's own credentials (Authorization, Cookie) are never
// forwarded to the runner.
//
//	GET    {prefix}/sessions/{id}                      -> GET    /api/runner/sessions/{id}
//	GET    {prefix}/sessions/{id}/events/live?...      -> GET    /api/runner/sessions/{id}/events/live?...   (SSE, streamed)
//	GET    {prefix}/sessions/{id}/events?...           -> GET    /api/runner/sessions/{id}/events?...
//	POST   {prefix}/sessions/{id}/messages  {text}     -> POST   /api/runner/sessions/{id}/message  {text}
//	POST   {prefix}/sessions/{id}/stop                 -> POST   /api/runner/sessions/{id}/stop
//	POST   {prefix}/sessions/{id}/interrupt            -> POST   /api/runner/sessions/{id}/interrupt
//	POST   {prefix}/sessions/{id}/model                -> 501 Not Implemented (reserved)
//	GET    {prefix}/sessions/{id}/artifacts            -> GET    /api/runner/sessions/{id}/artifacts
//	GET    {prefix}/sessions/{id}/artifacts/{aid}/raw  -> GET    /api/runner/sessions/{id}/artifacts/{aid}/raw
//	GET    {prefix}/sessions/{id}/artifacts/{aid}/download -> GET /api/runner/sessions/{id}/artifacts/{aid}/download
//	DELETE {prefix}/sessions/{id}/artifacts/{aid}      -> DELETE /api/runner/sessions/{id}/artifacts/{aid}
//	POST   {prefix}/sessions/{id}/uploads              -> POST   /api/runner/sessions/{id}/uploads  (raw bytes, X-Artifact-Name)
//
// Every route runs Authorize first: false is a 403 and the runner is never
// contacted; an error is a 500. The runner's status code is returned as is,
// with its Content-Type, Content-Disposition and Cache-Control headers and
// nothing else.
package chatproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// RunnerClient is what the app already has: an authenticated HTTP client to
// the runner. path is the runner path including any query string (for
// example "/api/runner/sessions/abc/events/live?tail=1"); headers are
// request headers to add. The implementation adds the agent token and the
// runner's base URL, and must honour ctx so a browser that goes away cancels
// the runner request (http.NewRequestWithContext does this).
type RunnerClient interface {
	Do(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error)
}

// Authorize decides, for the app's own signed-in person, whether sessionID
// may be viewed. It runs before every route. Return (false, nil) to refuse
// (403); an error is a server fault (500). The app's rule is its own, for
// instance "the session belongs to a job of this person".
type Authorize func(r *http.Request, sessionID string) (ok bool, err error)

// ErrNotFound is what a Resolve returns for a session id the app does not
// know: the proxy answers 404 and the runner is never contacted.
var ErrNotFound = errors.New("chatproxy: session not found")

// Options is what MountWith takes beyond the client. Only Authorize is
// required.
type Options struct {
	// Authorize runs before every route (see the type).
	Authorize Authorize
	// Resolve maps the session id in the app's URLs to the runner's session
	// id, for an app that names sessions its own way (its own row ids). It
	// runs after Authorize. Nil: the two are the same id. Return ErrNotFound
	// for an id the app does not know (404); any other error is a 500.
	Resolve func(r *http.Request, sessionID string) (runnerSessionID string, err error)
	// MessageSource is recorded by the runner as the `source` of every message
	// sent through the proxy (for example "human", when the proxy's messages
	// are typed by a signed-in person). Empty: the runner's default.
	MessageSource string
}

// Mount registers the proxy contract under prefix (e.g. "/blerg") on mux.
// prefix must start with "/" and have no trailing slash; "" mounts at the root.
func Mount(mux *http.ServeMux, prefix string, client RunnerClient, authorize Authorize) {
	MountWith(mux, prefix, client, Options{Authorize: authorize})
}

// MountWith is Mount with the optional pieces: a session-id mapping and the
// source the runner records for proxied messages.
func MountWith(mux *http.ServeMux, prefix string, client RunnerClient, opts Options) {
	if client == nil || opts.Authorize == nil {
		panic("chatproxy: Mount needs a RunnerClient and an Authorize")
	}
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		panic("chatproxy: prefix must start with /")
	}
	p := &proxy{client: client, authorize: opts.Authorize, resolve: opts.Resolve, messageSource: opts.MessageSource}
	base := prefix + "/sessions/{id}"

	mux.HandleFunc("GET "+base, p.session)
	mux.HandleFunc("GET "+base+"/events/live", p.eventsLive)
	mux.HandleFunc("GET "+base+"/events", p.events)
	mux.HandleFunc("POST "+base+"/messages", p.messages)
	mux.HandleFunc("POST "+base+"/stop", p.stop)
	mux.HandleFunc("POST "+base+"/interrupt", p.interrupt)
	mux.HandleFunc("POST "+base+"/model", p.model)
	mux.HandleFunc("GET "+base+"/artifacts", p.artifacts)
	mux.HandleFunc("GET "+base+"/artifacts/{aid}/raw", p.artifactRaw)
	mux.HandleFunc("GET "+base+"/artifacts/{aid}/download", p.artifactDownload)
	mux.HandleFunc("DELETE "+base+"/artifacts/{aid}", p.artifactDelete)
	mux.HandleFunc("POST "+base+"/uploads", p.uploads)
}

// maxMessageBytes bounds a {text} body read into memory before it is
// re-encoded for the runner.
const maxMessageBytes = 1 << 20

// copiedHeaders are the only response headers that travel from the runner
// back to the browser. Set-Cookie, Authorization and everything else stop
// here.
var copiedHeaders = []string{"Content-Type", "Content-Disposition", "Cache-Control"}

type proxy struct {
	resolve       func(r *http.Request, sessionID string) (string, error)
	messageSource string
	client        RunnerClient
	authorize     Authorize
}

// gate runs Authorize for the request's session and reports whether the
// route may continue. It writes the refusal itself.
func (p *proxy) gate(w http.ResponseWriter, r *http.Request) (sessionID string, ok bool) {
	sessionID = r.PathValue("id")
	if !validSegment(sessionID) {
		http.Error(w, "bad session id", http.StatusBadRequest)
		return "", false
	}
	allowed, err := p.authorize(r, sessionID)
	if err != nil {
		http.Error(w, "authorization failed", http.StatusInternalServerError)
		return "", false
	}
	if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return "", false
	}
	if p.resolve == nil {
		return sessionID, true
	}
	// From here on the id is the runner's: every route builds its runner path
	// from what gate returns.
	runnerID, err := p.resolve(r, sessionID)
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "session not found", http.StatusNotFound)
		return "", false
	case err != nil || !validSegment(runnerID):
		http.Error(w, "session lookup failed", http.StatusInternalServerError)
		return "", false
	}
	return runnerID, true
}

// artifactID reads and validates the {aid} path value.
func artifactID(w http.ResponseWriter, r *http.Request) (string, bool) {
	aid := r.PathValue("aid")
	if !validSegment(aid) {
		http.Error(w, "bad artifact id", http.StatusBadRequest)
		return "", false
	}
	return aid, true
}

func validSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\?#")
}

// runnerPath builds the runner path for a session, escaping each id and
// carrying the browser's query string when asked to.
func runnerPath(sessionID, suffix string, query url.Values) string {
	path := "/api/runner/sessions/" + url.PathEscape(sessionID) + suffix
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return path
}

// forward sends one request to the runner and copies the response back:
// status, the allowed headers and the body.
func (p *proxy) forward(w http.ResponseWriter, r *http.Request, method, path string, body io.Reader, headers map[string]string) {
	resp, err := p.client.Do(r.Context(), method, path, body, headers)
	if err != nil {
		http.Error(w, "runner unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func copyHeaders(w http.ResponseWriter, resp *http.Response) {
	for _, name := range copiedHeaders {
		if v := resp.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
}

func (p *proxy) session(w http.ResponseWriter, r *http.Request) {
	id, ok := p.gate(w, r)
	if !ok {
		return
	}
	p.forward(w, r, http.MethodGet, runnerPath(id, "", nil), nil, nil)
}

func (p *proxy) events(w http.ResponseWriter, r *http.Request) {
	id, ok := p.gate(w, r)
	if !ok {
		return
	}
	p.forward(w, r, http.MethodGet, runnerPath(id, "/events", r.URL.Query()), nil, nil)
}

// eventsLive streams the runner's SSE through. The body is copied as it
// arrives and flushed after every frame (a blank line), so the browser sees
// each event and each keepalive comment the moment the runner sends it. The
// runner request carries the browser's context: when the browser goes away
// the runner request is cancelled and the handler returns.
func (p *proxy) eventsLive(w http.ResponseWriter, r *http.Request) {
	id, ok := p.gate(w, r)
	if !ok {
		return
	}
	resp, err := p.client.Do(r.Context(), http.MethodGet, runnerPath(id, "/events/live", r.URL.Query()), nil, map[string]string{
		"Accept": "text/event-stream",
	})
	if err != nil {
		http.Error(w, "runner unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyHeaders(w, resp)
	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	ctx := r.Context()
	rd := bufio.NewReader(resp.Body)
	for {
		line, err := rd.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				return
			}
			// A frame ends with an empty line: "...\n\n".
			if flusher != nil && len(bytes.TrimRight(line, "\r\n")) == 0 {
				flusher.Flush()
			}
		}
		if err != nil {
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func (p *proxy) messages(w http.ResponseWriter, r *http.Request) {
	id, ok := p.gate(w, r)
	if !ok {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxMessageBytes+1))
	if err != nil || len(raw) > maxMessageBytes {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.Text == "" {
		http.Error(w, `expected {"text": "..."}`, http.StatusBadRequest)
		return
	}
	msg := map[string]string{"text": in.Text}
	if p.messageSource != "" {
		msg["source"] = p.messageSource
	}
	out, err := json.Marshal(msg)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	p.forward(w, r, http.MethodPost, runnerPath(id, "/message", nil), bytes.NewReader(out), map[string]string{
		"Content-Type": "application/json",
	})
}

func (p *proxy) stop(w http.ResponseWriter, r *http.Request) {
	id, ok := p.gate(w, r)
	if !ok {
		return
	}
	p.forward(w, r, http.MethodPost, runnerPath(id, "/stop", nil), nil, nil)
}

// interrupt cuts the running turn short; the session goes on (the chat's Stop
// button and Esc).
func (p *proxy) interrupt(w http.ResponseWriter, r *http.Request) {
	id, ok := p.gate(w, r)
	if !ok {
		return
	}
	p.forward(w, r, http.MethodPost, runnerPath(id, "/interrupt", nil), nil, nil)
}

// model is reserved: the runner's v1 contract has no model switch for agent
// tokens yet. The package's transport treats 501 as "setModel unavailable".
func (p *proxy) model(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.gate(w, r); !ok {
		return
	}
	http.Error(w, "model switch not available through the proxy", http.StatusNotImplemented)
}

func (p *proxy) artifacts(w http.ResponseWriter, r *http.Request) {
	id, ok := p.gate(w, r)
	if !ok {
		return
	}
	p.forward(w, r, http.MethodGet, runnerPath(id, "/artifacts", nil), nil, nil)
}

func (p *proxy) artifactRaw(w http.ResponseWriter, r *http.Request) {
	id, ok := p.gate(w, r)
	if !ok {
		return
	}
	aid, ok := artifactID(w, r)
	if !ok {
		return
	}
	p.forward(w, r, http.MethodGet, runnerPath(id, "/artifacts/"+url.PathEscape(aid)+"/raw", nil), nil, nil)
}

func (p *proxy) artifactDownload(w http.ResponseWriter, r *http.Request) {
	id, ok := p.gate(w, r)
	if !ok {
		return
	}
	aid, ok := artifactID(w, r)
	if !ok {
		return
	}
	p.forward(w, r, http.MethodGet, runnerPath(id, "/artifacts/"+url.PathEscape(aid)+"/download", nil), nil, nil)
}

func (p *proxy) artifactDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := p.gate(w, r)
	if !ok {
		return
	}
	aid, ok := artifactID(w, r)
	if !ok {
		return
	}
	p.forward(w, r, http.MethodDelete, runnerPath(id, "/artifacts/"+url.PathEscape(aid), nil), nil, nil)
}

// uploads streams the browser's raw bytes to the runner with the file's
// name. The runner applies its own size caps; the proxy holds nothing.
func (p *proxy) uploads(w http.ResponseWriter, r *http.Request) {
	id, ok := p.gate(w, r)
	if !ok {
		return
	}
	headers := map[string]string{}
	if name := r.Header.Get("X-Artifact-Name"); name != "" {
		headers["X-Artifact-Name"] = name
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		headers["Content-Type"] = ct
	}
	p.forward(w, r, http.MethodPost, runnerPath(id, "/uploads", nil), r.Body, headers)
}

// ErrNoClient is returned by HTTPRunnerClient when it was built without a
// base URL.
var ErrNoClient = errors.New("chatproxy: runner client not configured")

// HTTPRunnerClient is a minimal RunnerClient over net/http for apps that
// have no runner client yet: BaseURL is the runner's origin, Token the
// app's agent token (sent as a Bearer), HTTP the client to use (nil for
// http.DefaultClient). Apps with an existing client should implement
// RunnerClient over it instead.
type HTTPRunnerClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// Do implements RunnerClient.
func (c *HTTPRunnerClient) Do(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	if c == nil || c.BaseURL == "" {
		return nil, ErrNoClient
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.BaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	return hc.Do(req)
}
