// Package runner implements blerg-board's side of the runner contract: a driver
// interface with a blerg-runner HTTP implementation. blerg-board brokers work — it never
// executes. The contract centers the EVENT STREAM; there is no live-view URL
// and no iframe (see the design spec's embed correction).
package runner

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
	"time"
)

type StartRequest struct {
	// TargetAccount binds a per-run override to the automation owner that authorized it.
	// Internal to Board; never sent on the runner wire.
	TargetAccount string
	Repo          string
	Title         string
	Prompt        string
	Model         string
	Effort        string
	GitURL        string // full clone URL; overrides the driver's default git base
	Env           map[string]string
	// Engine is the coding engine the session runs ("" = the runner's
	// default, claude).
	Engine string
	// Interaction says whether a person is reading the session's chat live:
	// "interactive" (the agent discusses before acting, narrates as it works,
	// asks and waits for an answer) or "unattended" (nobody is watching: it
	// decides, notes its assumptions, finishes and summarises). It is fixed
	// for the session's life. "" sends nothing and leaves the runner's
	// default, which for a contract start is unattended.
	Interaction string
	// Runtime and DaemonID are where this ONE start runs, when its caller
	// chose (a person picked a target for a card's Run): a runtime of the
	// runner contract ("cluster", "docker" or "daemon") and, for the last two,
	// the connected daemon. Empty leaves the board's configured runtime and
	// the runner's own placement, as before.
	Runtime  string
	DaemonID string
	// Token is the credential this ONE start is made with: the board's
	// automation token, a blerg-core agent token its owner minted for
	// themselves. The runner resolves it to that person's account and runs the
	// session on their own engine credential. It is a bearer credential:
	// never log it, never put it in an error.
	Token string
}

// IdentityStarter is implemented by drivers that start sessions under a
// per-call identity (StartRequest.Token) rather than the driver's own shared
// key. blerg-board refuses to call Start on such a driver without one — see
// api.startIdentity — and the driver refuses too.
type IdentityStarter interface {
	RequiresStartIdentity() bool
}

// ErrNoStartIdentity: Start was called with no StartRequest.Token on a driver
// that requires one. Nothing is sent to the runner.
var ErrNoStartIdentity = errors.New("runner: no start identity (automation token) supplied")

// ErrStartUnauthorized: the runner refused the start's credential (HTTP 401
// or 403) — the automation token is expired, revoked, or lacks the
// session.start capability. It is a fact about the board's configuration, not
// about the card being started.
var ErrStartUnauthorized = errors.New("runner refused the start credential (the board's automation token is expired, revoked, or not a run-sessions token)")

type Status struct {
	SteeringSupported *bool  `json:"steering_supported,omitempty"`
	Lifecycle         string `json:"lifecycle"`
	Runtime           string `json:"runtime"`
	Resumable         bool   `json:"resumable"`
	ResumeMode        string `json:"resume_mode,omitempty"`
	ResumeReason      string `json:"resume_reason,omitempty"`
	Engine            string `json:"engine,omitempty"`
	Model             string `json:"model,omitempty"`
	Effort            string `json:"effort,omitempty"`
	// ErrorReason is why the session failed, as the runner recorded it (a
	// missing engine login, an uncloned repo, a sandbox image that isn't
	// there). The runner's one-shot error broadcast is long gone by the time
	// blerg-board polls, so without this a failed desktop session reaches the
	// user as a bare "error" and nothing else. Empty unless lifecycle is error.
	ErrorReason string `json:"error_reason,omitempty"`
}

type Event struct {
	Seq     int64           `json:"seq"`
	Ts      string          `json:"ts"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type Driver interface {
	Name() string
	Start(ctx context.Context, req StartRequest) (externalSessionID string, err error)
	Status(ctx context.Context, sessionID string) (Status, error)
	Message(ctx context.Context, sessionID, text, source string) error
	Interrupt(ctx context.Context, sessionID string) error
	// Stop ends the session entirely (frees runner capacity); Interrupt
	// only cancels the in-flight turn.
	Stop(ctx context.Context, sessionID string) error
	// Events returns settled events after seq; transient deltas never appear.
	Events(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]Event, bool, error)
	// SetModel re-models a LIVE session: same session, same context, same
	// event stream — only the model the next turn runs on changes. Nothing
	// is respawned, so an in-flight turn finishes on the old model.
	// Returns ErrUnsupported when the runner has no verb for it.
	SetModel(ctx context.Context, sessionID, model string) error
}

// TerminalLifecycles are states after which blerg-board stops polling and revokes
// the session's token. `disconnected` is NOT terminal — the session resumes
// on the next message.
func TerminalLifecycle(l string) bool { return l == "stopped" || l == "error" }

// ErrSessionGone means the runner has no record of the session (HTTP 404) —
// its Job finished and was reaped, or it never existed. Drivers wrap this
// into errors returned from Status so ingest can treat "gone" as terminal
// even when the runner never reports a settled lifecycle.
var ErrSessionGone = errors.New("runner: session not found")

// ErrUnsupported means the runner behind this driver has no verb for the
// operation — it is older than the contract, not broken. Callers surface it
// as "this runner can't do that" (501), never as a generic failure.
var ErrUnsupported = errors.New("runner: operation not supported")

// ── blerg-runner driver ───────────────────────────────────────────────────────────

type BlergRunner struct {
	url     string
	key     string
	gitBase string // e.g. https://github.com/blerglab — repo appended for clone URLs
	// runtime is sent as `runtime` on every start when set; empty leaves the
	// choice to the runner (cluster, else the daemon's sandbox, else its host).
	runtime string
	client  *http.Client
}

func NewBlergRunner(url, key, gitBase string) *BlergRunner {
	return &BlergRunner{url: strings.TrimRight(url, "/"), key: key,
		gitBase: strings.TrimRight(gitBase, "/"),
		client:  &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// SetRuntime pins the runtime every start asks for (a value ParseRuntime
// accepted); "" leaves it to the runner's default.
func (k *BlergRunner) SetRuntime(runtime string) { k.runtime = runtime }

// DefaultRuntime is the runtime every start asks for unless it names its own
// ("" = the runner decides).
func (k *BlergRunner) DefaultRuntime() string { return k.runtime }

// GitBase is the clone base a start without its own git_url is sent with ("" for none).
func (k *BlergRunner) GitBase() string { return k.gitBase }

// ParseRuntime validates BLERG_BOARD_RUNNER_RUNTIME: empty (the runner
// decides) or one of the runner contract's runtimes. Anything else is refused
// here, at boot, rather than turned into a 422 on every start.
func ParseRuntime(v string) (string, error) {
	switch v {
	case "", "daemon", "docker", "cluster":
		return v, nil
	}
	return "", fmt.Errorf("BLERG_BOARD_RUNNER_RUNTIME=%q: want empty, daemon, docker or cluster", v)
}

func (k *BlergRunner) Name() string { return "blerg-runner" }

// RequiresStartIdentity: every start this driver makes carries the caller's
// own identity. The shared key (k.key) only polls, messages and stops — it
// has no account behind it, so a session it started could only ever run on an
// operator credential, which self-service installs deliberately do not have.
func (k *BlergRunner) RequiresStartIdentity() bool { return true }

// joinRepoURL: a plain repo name appends to the base (org URL); an
// "org/name" repo resolves against the base's HOST — agents naturally write
// org-qualified repos, and gluing them onto an org URL 404s.
func JoinRepoURL(base, repo string) string {
	if strings.Contains(repo, "/") {
		if u, err := url.Parse(base); err == nil && u.Host != "" {
			return u.Scheme + "://" + u.Host + "/" + repo + ".git"
		}
	}
	return base + "/" + repo + ".git"
}

func (k *BlergRunner) req(ctx context.Context, method, path string, body, out any) error {
	_, err := k.do(ctx, method, path, body, out)
	return err
}

// respMeta is what a caller needs to judge a response beyond its error:
// "did this route exist?" is not answerable from the error alone (SetModel).
type respMeta struct {
	Status      int
	ContentType string
}

func (k *BlergRunner) do(ctx context.Context, method, path string, body, out any) (respMeta, error) {
	return k.doAs(ctx, k.key, method, path, body, out)
}

// doAs is do with an explicit bearer credential.
func (k *BlergRunner) doAs(ctx context.Context, bearer, method, path string, body, out any) (respMeta, error) {
	var meta respMeta
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return meta, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, k.url+path, &buf)
	if err != nil {
		return meta, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Blerg-Desktop-Viewer", "none")
	if viewer := desktopViewerProof(ctx); viewer != "" {
		req.Header.Set("X-Blerg-Desktop-Viewer", viewer)
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return meta, err
	}
	defer func() { _ = resp.Body.Close() }()
	meta.Status, meta.ContentType = resp.StatusCode, resp.Header.Get("Content-Type")
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		safeBody := string(raw)
		if viewer := desktopViewerProof(ctx); viewer != "" {
			safeBody = strings.ReplaceAll(safeBody, viewer, "[redacted]")
		}
		if bearer != "" {
			safeBody = strings.ReplaceAll(safeBody, bearer, "[redacted]")
		}
		httpErr := fmt.Errorf("blerg-runner %s %s: HTTP %d: %.200s", method, path, resp.StatusCode, safeBody)
		if resp.StatusCode == http.StatusNotFound {
			httpErr = fmt.Errorf("%w: %w", ErrSessionGone, httpErr)
		}
		return meta, httpErr
	}
	if out != nil {
		return meta, json.Unmarshal(raw, out)
	}
	return meta, nil
}

// Start makes the start under r.Token, never the shared key: see
// RequiresStartIdentity. A 401/403 is reported as ErrStartUnauthorized so the
// caller can tell "the board's token is bad" from "this card failed".
func (k *BlergRunner) Start(ctx context.Context, r StartRequest) (string, error) {
	var out struct {
		SessionID string `json:"session_id"`
	}
	if r.Token == "" {
		return "", ErrNoStartIdentity
	}
	body := map[string]any{
		"repo": r.Repo, "title": r.Title, "prompt": r.Prompt,
		"model": r.Model, "env": r.Env,
	}
	if r.Effort != "" {
		body["effort"] = r.Effort
	}
	if r.Engine != "" {
		body["engine"] = r.Engine
	}
	if r.Interaction != "" {
		body["interaction"] = r.Interaction
	}
	if r.Runtime != "" {
		body["runtime"] = r.Runtime
	} else if k.runtime != "" {
		body["runtime"] = k.runtime
	}
	if r.DaemonID != "" {
		body["daemon_id"] = r.DaemonID
	}
	if r.GitURL != "" {
		body["git_url"] = r.GitURL
	} else if k.gitBase != "" {
		body["git_url"] = JoinRepoURL(k.gitBase, r.Repo)
	}
	meta, err := k.doAs(ctx, r.Token, "POST", "/api/runner/start", body, &out)
	if err != nil && (meta.Status == http.StatusUnauthorized || meta.Status == http.StatusForbidden) {
		return "", fmt.Errorf("%w (HTTP %d)", ErrStartUnauthorized, meta.Status)
	}
	return out.SessionID, err
}

// Status returns ErrSessionGone (wrapped) when blerg-runner 404s the session —
// its Job finished and was reaped, or it never existed.
func (k *BlergRunner) Status(ctx context.Context, id string) (Status, error) {
	return k.StatusAs(ctx, k.key, id)
}

func (k *BlergRunner) StatusAs(ctx context.Context, token, id string) (Status, error) {
	var s Status
	meta, err := k.doAs(ctx, token, "GET", "/api/runner/sessions/"+id, nil, &s)
	if err != nil && (meta.Status == http.StatusUnauthorized || meta.Status == http.StatusForbidden) {
		return s, fmt.Errorf("%w: runner refused control authority", ErrControlUnauthorized)
	}
	return s, err
}

func (k *BlergRunner) Message(ctx context.Context, id, text, source string) error {
	return k.MessageAs(ctx, k.key, id, text, source, "")
}

func (k *BlergRunner) MessageAs(ctx context.Context, token, id, text, source, clientID string) error {
	body := map[string]string{"text": text, "source": source}
	if clientID != "" {
		body["client_id"] = clientID
	}
	_, err := k.doAs(ctx, token, "POST", "/api/runner/sessions/"+id+"/message", body, nil)
	return err
}

func (k *BlergRunner) Interrupt(ctx context.Context, id string) error {
	return k.req(ctx, "POST", "/api/runner/sessions/"+id+"/interrupt", nil, nil)
}

func (k *BlergRunner) Stop(ctx context.Context, id string) error {
	return k.req(ctx, "POST", "/api/runner/sessions/"+id+"/stop", nil, nil)
}

// SetModel is the newest verb in the runner contract:
//
//	POST /api/runner/sessions/{id}/model  {"model": "claude-sonnet-5"}
//
// The runner keeps the session and its context and re-models the NEXT turn
// (blerg-runner's daemon already works this way internally: each turn is a fresh
// `claude --resume <id> --model <m>`), so an in-flight turn finishes on the
// model it started with.
//
// A runner that predates the verb has no such route. blerg-runner serves its
// single-page app on unknown paths — HTTP 200 with an HTML body, NOT a 404 —
// so a missing route is detected by content type, and 404 keeps its usual
// meaning of "no such session". 405/501 are treated as unsupported too, for
// runners that route more strictly.
func (k *BlergRunner) SetModel(ctx context.Context, id, model string) error {
	return k.SetModelAs(ctx, k.key, id, model, "")
}

func (k *BlergRunner) SetModelAs(ctx context.Context, token, id, model, effort string) error {
	body := map[string]string{"model": model}
	if effort != "" {
		body["effort"] = effort
	}
	meta, err := k.doAs(ctx, token, "POST", "/api/runner/sessions/"+id+"/model", body, nil)
	if err != nil {
		if meta.Status == http.StatusMethodNotAllowed || meta.Status == http.StatusNotImplemented {
			return fmt.Errorf("%w: %w", ErrUnsupported, err)
		}
		return err
	}
	if meta.Status != http.StatusNoContent && !strings.Contains(meta.ContentType, "json") {
		return fmt.Errorf("%w: runner answered %d %q to POST /api/runner/sessions/{id}/model",
			ErrUnsupported, meta.Status, meta.ContentType)
	}
	return nil
}

// kindMap folds blerg-runner's event vocabulary onto blerg-board's closed set. Unmapped
// kinds become "status" (visible but unstyled) except the droppedKinds, which
// blerg-board does not draw and so does not store.
// droppedKinds: provider_blocks (raw model blocks — heavy and redundant with
// assistant_text), assistant_thinking (the model's working, not its answer),
// subagent_update (running totals that only feed the chat's subagent card) and
// background_task_started / _done (they only feed the chat's background strip).
var droppedKinds = map[string]bool{
	"provider_blocks": true, "assistant_thinking": true, "subagent_update": true,
	"background_task_started": true, "background_task_done": true,
}

var kindMap = map[string]string{
	"user_message":   "user_turn",
	"assistant_text": "assistant_turn",
	"ask":            "assistant_turn",
	"note":           "assistant_turn",
	"update":         "assistant_turn",
	"check_in":       "assistant_turn",
	"tool_call":      "tool_call",
	"tool_result":    "tool_result",
	"error":          "error",
	"turn_done":      "status", // payload keeps usage — blerg-board aggregates tokens from it
}

func (k *BlergRunner) Events(ctx context.Context, id string, afterSeq int64, limit int) ([]Event, bool, error) {
	var out struct {
		Events  []Event `json:"events"`
		HasMore bool    `json:"has_more"`
	}
	err := k.req(ctx, "GET",
		fmt.Sprintf("/api/runner/sessions/%s/events?after_seq=%d&limit=%d", id, afterSeq, limit),
		nil, &out)
	if err != nil {
		return nil, false, err
	}
	mapped := make([]Event, 0, len(out.Events))
	for _, ev := range out.Events {
		if droppedKinds[ev.Kind] {
			continue
		}
		if m, ok := kindMap[ev.Kind]; ok {
			ev.Kind = m
		} else {
			// Preserve the original kind inside the payload for debugging.
			wrapped, _ := json.Marshal(map[string]any{
				"source_kind": ev.Kind, "payload": ev.Payload,
			})
			ev.Payload = wrapped
			ev.Kind = "status"
		}
		mapped = append(mapped, ev)
	}
	return mapped, out.HasMore, nil
}

// IdentityController keeps a current owner proof on controls that can recover
// a disconnected native session. Implementations never borrow launch-time proof.
type IdentityController interface {
	StatusAs(context.Context, string, string) (Status, error)
	MessageAs(context.Context, string, string, string, string, string) error
	SetModelAs(context.Context, string, string, string, string) error
}

var ErrControlUnauthorized = errors.New("runner refused the control credential")
