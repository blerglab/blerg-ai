package server

// The runner's half of the agent contract (spec §1 discovery, §2 introspection):
// a public GET /agents manifest, a public GET /openapi.json description of the
// session contract, and GET /api/runner/me so a caller can see exactly what its
// credential is without guessing from a 401.

import (
	_ "embed"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
)

// runnerOpenAPIDoc is the runner's hand-maintained OpenAPI 3.1 document, served
// verbatim at GET /openapi.json. It is embedded rather than generated: the
// public session contract is small and stable, and openapi_test.go asserts it
// agrees with both the manifest and the mux, so a hand-written document cannot
// silently drift from the server.
//
//go:embed openapi.json
var runnerOpenAPIDoc []byte

// componentVersion is the build version the runner reports in its manifest.
// main.go sets it from the same ldflags-stamped string it gives the hub; it is
// an atomic package value because /agents may be served concurrently with (and
// before) the registration goroutine that also publishes it.
var componentVersion atomic.Value

// SetComponentVersion records the runner's build version for its manifest entry.
func SetComponentVersion(v string) {
	if v != "" {
		componentVersion.Store(v)
	}
}

func componentVersionString() string {
	if v, ok := componentVersion.Load().(string); ok {
		return v
	}
	return "dev"
}

// runnerOperations is the documented operation table for the runner's manifest
// entry. It is the single source of truth openapi_test.go checks openapi.json
// against, so adding a public runner-contract route means adding it here and in
// openapi.json together, or the test fails.
//
// The full v1 surface is listed here, and every entry is a route that is
// actually registered — openapi_test.go checks the manifest, openapi.json and
// the mux against each other, with no exemptions.
//
// Every operation is behind authRunner, which requires coreAuthRunnerCap on a
// core-issued token, so they all carry the same Cap.
var runnerOperations = []agentsmanifest.Operation{
	{Name: "start", Method: "POST", Path: "/api/runner/start", Cap: coreAuthRunnerCap, Idempotent: true,
		Summary: "Start a coding-agent session on a repository; repeat safely with the Idempotency-Key header."},
	{Name: "status", Method: "GET", Path: "/api/runner/sessions/{id}", Cap: coreAuthRunnerCap, Idempotent: true,
		Summary: "Session status: lifecycle, runtime, whether it can be resumed."},
	{Name: "send_message", Method: "POST", Path: "/api/runner/sessions/{id}/message", Cap: coreAuthRunnerCap,
		Summary: "Send a conversational turn to the session; resumes a disconnected one."},
	{Name: "interrupt", Method: "POST", Path: "/api/runner/sessions/{id}/interrupt", Cap: coreAuthRunnerCap,
		Summary: "Cancel the session's in-flight turn without ending it."},
	{Name: "stop", Method: "POST", Path: "/api/runner/sessions/{id}/stop", Cap: coreAuthRunnerCap, Idempotent: true,
		Summary: "End the session for good and free its runtime slot."},
	{Name: "events", Method: "GET", Path: "/api/runner/sessions/{id}/events", Cap: coreAuthRunnerCap, Idempotent: true,
		Summary: "Read a window of the session transcript from a cursor (after_seq)."},
	{Name: "events_stream", Method: "GET", Path: "/api/runner/sessions/{id}/events/stream", Cap: coreAuthRunnerCap, Idempotent: true,
		Summary: "The same events as a Server-Sent Events stream, resumable with Last-Event-ID."},
	{Name: "events_live", Method: "GET", Path: "/api/runner/sessions/{id}/events/live", Cap: coreAuthRunnerCap, Idempotent: true,
		Summary: "The live transcript as Server-Sent Events: a replay page, then every event, typing delta and status change as it happens."},
	{Name: "result", Method: "GET", Path: "/api/runner/sessions/{id}/result", Cap: coreAuthRunnerCap, Idempotent: true,
		Summary: "The structured outcome: terminal state, branch, last assistant message."},
	{Name: "files_list", Method: "GET", Path: "/api/runner/sessions/{id}/artifacts", Cap: coreAuthRunnerCap, Idempotent: true,
		Summary: "The session's files (published by the agent or attached by a person), newest first, with versions."},
	{Name: "file_raw", Method: "GET", Path: "/api/runner/sessions/{id}/artifacts/{aid}/raw", Cap: coreAuthRunnerCap, Idempotent: true,
		Summary: "One file's bytes for an in-app viewer, with the server-chosen content type."},
	{Name: "file_download", Method: "GET", Path: "/api/runner/sessions/{id}/artifacts/{aid}/download", Cap: coreAuthRunnerCap, Idempotent: true,
		Summary: "One file's bytes as an attachment."},
	{Name: "file_delete", Method: "DELETE", Path: "/api/runner/sessions/{id}/artifacts/{aid}", Cap: coreAuthRunnerCap, Idempotent: true,
		Summary: "Delete one of the session's files, whichever origin it came from."},
	{Name: "upload", Method: "POST", Path: "/api/runner/sessions/{id}/uploads", Cap: coreAuthRunnerCap,
		Summary: "Attach a person's file to the session (raw body, X-Artifact-Name header), for the agent to fetch."},
	{Name: "me", Method: "GET", Path: "/api/runner/me", Cap: coreAuthRunnerCap, Idempotent: true,
		Summary: "What the calling credential is: kind, subject, audience, capabilities, expiry."},
}

// RunnerManifest builds the runner's ComponentEntry for the given public base
// URL: the document served at GET /agents and the body sent to core's
// POST /components.
//
// LastSeen, Stale and Auth.TokenEndpoint are deliberately left unset — they are
// core's to fill in. Nothing here is a secret: public URLs, capability names and
// the documentation of endpoints that each enforce their own auth.
func RunnerManifest(baseURL string) agentsmanifest.ComponentEntry {
	base := strings.TrimRight(baseURL, "/")
	return agentsmanifest.ComponentEntry{
		Name:            "blerg-runner",
		BaseURL:         base,
		Version:         componentVersionString(),
		ContractVersion: agentsmanifest.ContractVersion,
		Capabilities:    []string{"runner", "sessions", "mcp"},
		Description:     "Starts and drives coding-agent sessions (cluster pods or workstation daemons)",
		DocsURL:         base + "/agents",
		OpenAPIURL:      base + "/openapi.json",
		MCPURL:          base + "/mcp",
		Auth: &agentsmanifest.AuthInfo{
			Audience: coreAuthAudience,
			Presets:  []string{"run-sessions"},
			// No human_session: the runner contract has no browser sign-in, only
			// a core-minted agent token or the operator's static runner key.
			Accepts: []string{"agent_token", "runner_key"},
		},
		Operations: runnerOperations,
		// The chat package, when this build packed one (packages.go).
		UI: runnerUIInfo(base),
	}
}

// runnerOrigin is the public origin of this request — how a caller reached the
// runner, so the URLs in the manifest are the URLs that work for them. Behind
// the ingress the scheme arrives in X-Forwarded-Proto, as elsewhere in this
// package (see checkBrowserWSOrigin).
func runnerOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// HandleAgents is GET /agents: the runner's manifest entry, as JSON or (with
// Accept: text/markdown, or ?format=md) as the Markdown agent guide.
//
// Deliberately unauthenticated (spec §1): an agent must be able to read how to
// get a credential BEFORE it has one, so gating discovery behind a credential
// would make the contract unbootstrappable. The document holds no secrets.
func (a *API) HandleAgents(w http.ResponseWriter, r *http.Request) {
	// The body depends on Accept (JSON or Markdown): a shared cache must not
	// serve one to a client that asked for the other.
	w.Header().Set("Vary", "Accept")
	entry := RunnerManifest(runnerOrigin(r))
	if agentsmanifest.WantsMarkdown(r) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(agentsmanifest.RenderComponentMarkdown(entry))) //nolint:gosec // Content-Type is text/markdown (never rendered as HTML) and the body is the static manifest
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// handleOpenAPI is GET /openapi.json — the embedded document, unauthenticated
// for the same reason /agents is: it is documentation of a contract, not access
// to it.
//
// Unexported, like core's and board's: every component's contract routes are
// wired by its own register function (below), so nothing outside the package
// has any business naming the handler.
func handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(runnerOpenAPIDoc)
}

// HandleRunnerMe is GET /api/runner/me (spec §2): introspection of the calling
// credential. It returns only what the caller already proved it holds — never
// the token itself — so an agent can tell an expired token from a wrong
// audience from a missing capability without a support round trip.
func (a *API) HandleRunnerMe(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authRunner(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// RegisterRunnerContractRoutes wires every route the runner's openapi.json
// describes. main.go calls it so there is exactly one route table for the agent
// contract, and openapi_test.go drives the same function — a documented route
// that is never registered fails the build's tests rather than an agent's call.
func RegisterRunnerContractRoutes(mux *http.ServeMux, a *API) {
	// Discovery: public, no auth (see HandleAgents).
	mux.HandleFunc("GET /agents", a.HandleAgents)
	mux.HandleFunc("GET /openapi.json", handleOpenAPI)
	// The per-engine model picker: readable by a browser session or a runner
	// credential (authBrowserOrRunner), so it lives on the contract too.
	mux.HandleFunc("GET /api/models/{engine}", a.HandleGetModels)

	// The session contract: every handler calls authRunner itself.
	mux.HandleFunc("GET /api/runner/me", a.HandleRunnerMe)
	mux.HandleFunc("POST /api/runner/start", a.HandleRunnerStart)
	mux.HandleFunc("GET /api/runner/sessions/{id}", a.HandleRunnerStatus)
	mux.HandleFunc("POST /api/runner/sessions/{id}/message", a.HandleRunnerMessage)
	mux.HandleFunc("POST /api/runner/sessions/{id}/interrupt", a.HandleRunnerInterrupt)
	mux.HandleFunc("POST /api/runner/sessions/{id}/stop", a.HandleRunnerStop)
	mux.HandleFunc("GET /api/runner/sessions/{id}/events", a.HandleRunnerEvents)
	mux.HandleFunc("GET /api/runner/sessions/{id}/events/stream", a.HandleRunnerEventStream)
	mux.HandleFunc("GET /api/runner/sessions/{id}/events/live", a.HandleRunnerEventsLive)
	mux.HandleFunc("GET /api/runner/sessions/{id}/result", a.HandleRunnerResult)
	// Session files for the same credential (runner_files.go).
	mux.HandleFunc("GET /api/runner/sessions/{id}/artifacts", a.HandleRunnerFilesList)
	mux.HandleFunc("GET /api/runner/sessions/{id}/artifacts/{aid}/raw", a.HandleRunnerFileRaw)
	mux.HandleFunc("GET /api/runner/sessions/{id}/artifacts/{aid}/download", a.HandleRunnerFileDownload)
	mux.HandleFunc("DELETE /api/runner/sessions/{id}/artifacts/{aid}", a.HandleRunnerFileDelete)
	mux.HandleFunc("POST /api/runner/sessions/{id}/uploads", a.HandleRunnerUpload)
	// The UI package the manifest advertises (packages.go): public, like /agents.
	mux.HandleFunc("GET /packages/{$}", handlePackagesIndex)
	mux.HandleFunc("GET /packages/{file}", handlePackageFile)
	mux.HandleFunc("GET /packages/{scope}/{name}/README.md", handlePackageReadme)
}
