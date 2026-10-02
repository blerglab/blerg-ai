package mcpgw

// Proposals on the gateway side (spec 9): the sink that freezes a propose-mode call instead of
// forwarding it, and ExecuteApproved, the one place an approved proposal reaches the upstream.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultMaxPendingProposals is the cap on an account's pending proposals (spec 5.4, 9).
// DefaultMaxPendingPerSession and DefaultMaxPendingPerCron bound how much of it one session, or
// one cron across its sessions, can take, so a looping agent cannot fill the account's queue for
// the whole expiry window.
const (
	DefaultMaxPendingProposals  = 50
	DefaultMaxPendingPerSession = 10
	DefaultMaxPendingPerCron    = 20
)

const (
	summaryMaxBytes = 240
	summaryValueMax = 60 // runes of one argument value
)

// ProposalRefusal is what a ProposalSink returns when the call must not be queued for a reason
// the agent should hear. The gateway turns it into an error tool result carrying Msg; any other
// sink error becomes a generic "could not be queued" result.
type ProposalRefusal struct{ Msg string }

func (e *ProposalRefusal) Error() string { return e.Msg }

// DBProposalSink is the ProposalSink that stores proposals in mcp_proposals.
type DBProposalSink struct {
	pool       *pgxpool.Pool
	publicURL  string
	maxPending int
	perSession int
	perCron    int
}

// NewProposalSink returns the sink. publicURL is the runner's own public address
// (BLERG_RUNNER_SELF_URL): with it the agent is told the page of the proposal, without it only
// the id. maxPending <= 0 means DefaultMaxPendingProposals.
func NewProposalSink(pool *pgxpool.Pool, publicURL string, maxPending int) *DBProposalSink {
	if maxPending <= 0 {
		maxPending = DefaultMaxPendingProposals
	}
	return &DBProposalSink{pool: pool, publicURL: strings.TrimRight(strings.TrimSpace(publicURL), "/"), maxPending: maxPending,
		perSession: DefaultMaxPendingPerSession, perCron: DefaultMaxPendingPerCron}
}

// Propose implements ProposalSink: it freezes the call's arguments (size and JSON checked), with
// the connection URL and tool hash the grant pinned, and returns the text the agent receives.
// The upstream is never called.
func (s *DBProposalSink) Propose(ctx context.Context, req ProposalRequest) (string, error) {
	args := strings.TrimSpace(string(req.Arguments))
	switch {
	case args == "" || args == "null":
		args = "{}"
	case len(args) > db.MaxProposalArgumentsBytes:
		return "", &ProposalRefusal{Msg: fmt.Sprintf("this call was not queued: its arguments exceed the %d KiB limit for a proposal", db.MaxProposalArgumentsBytes>>10)}
	case !json.Valid([]byte(args)) || args[0] != '{':
		return "", &ProposalRefusal{Msg: "this call was not queued: its arguments are not a JSON object"}
	}
	p, err := db.InsertProposalLimited(ctx, s.pool, db.Proposal{
		AccountID: req.AccountID, SessionID: req.SessionID, ConnectionID: req.ConnectionID,
		ConnectionName: req.ConnectionName, URLSnapshot: req.URLSnapshot, Tool: req.Tool, ToolHash: req.ToolHash,
		Arguments: json.RawMessage(args), AgentSummary: summarizeProposal(req.Tool, json.RawMessage(args)),
	}, db.ProposalLimits{Account: s.maxPending, Session: s.perSession, Cron: s.perCron})
	switch {
	case errors.Is(err, db.ErrProposalSessionCap):
		return "", &ProposalRefusal{Msg: fmt.Sprintf("this call was not queued: this session already has %d proposals waiting for the person's approval. "+
			"Do not retry; put what you could not do in your card and stop", s.perSession)}
	case errors.Is(err, db.ErrProposalCronCap):
		return "", &ProposalRefusal{Msg: fmt.Sprintf("this call was not queued: this cron already has %d proposals waiting for the person's approval. "+
			"Do not retry; put what you could not do in your card and stop", s.perCron)}
	case errors.Is(err, db.ErrProposalCap):
		return "", &ProposalRefusal{Msg: fmt.Sprintf("this call was not queued: %d proposals are already pending the person's approval. "+
			"Do not retry; put what you could not do in your card and stop", s.maxPending)}
	case errors.Is(err, db.ErrProposalArguments):
		return "", &ProposalRefusal{Msg: "this call was not queued: its arguments cannot be stored (too large, or they contain a character that cannot be kept)"}
	case err != nil:
		return "", err
	}
	where := "Proposal id: " + p.ID + "."
	if s.publicURL != "" {
		where = "Proposal id: " + p.ID + ". Review link: " + s.publicURL + "/proposals?id=" + p.ID + "."
	}
	return "This action was NOT performed. It was queued for the person's approval and runs only if they approve it. " +
		where + " Do not repeat the call; mention the proposal (id and link) in your report.", nil
}

// summarizeProposal is a short one-line rendering of a call built only from data the gateway
// holds: the tool name and the top-level argument names with bounded values. It is a convenience
// for a list view; the frozen arguments are what a person decides on.
func summarizeProposal(tool string, args json.RawMessage) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args, &m); err != nil || len(m) == 0 {
		return capSummary(tool)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, oneLine(k, summaryValueMax)+"="+renderArg(m[k]))
	}
	return capSummary(tool + ": " + strings.Join(parts, ", "))
}

func renderArg(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return oneLine(s, summaryValueMax)
		}
	case '{':
		var o map[string]json.RawMessage
		if json.Unmarshal(raw, &o) == nil {
			return fmt.Sprintf("{%d fields}", len(o))
		}
	case '[':
		var a []json.RawMessage
		if json.Unmarshal(raw, &a) == nil {
			return fmt.Sprintf("[%d items]", len(a))
		}
	}
	return oneLine(string(raw), summaryValueMax)
}

// oneLine collapses whitespace and control characters to single spaces and cuts to n runes.
func oneLine(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > n {
		s = strings.ToValidUTF8(string([]rune(s)[:n]), "") + "..."
	}
	return s
}

func capSummary(s string) string {
	s = oneLine(s, summaryMaxBytes)
	if len(s) > summaryMaxBytes {
		s = cutUTF8(s, summaryMaxBytes-3) + "..."
	}
	return s
}

// ---- execution of an approved proposal ---------------------------------------------------

// ErrOutcomeUnknown marks an execution whose request may have been delivered but whose result
// was not received (a timeout, a dropped connection): the action may or may not have happened.
// The caller records `unknown` and never retries on its own.
var ErrOutcomeUnknown = errors.New("outcome unknown")

// ApprovedError is a failed execution of an approved proposal. Msg is safe to show the person
// and to store: it never contains the credential or the upstream's raw error text. Unknown
// reports the ambiguous case (errors.Is(err, ErrOutcomeUnknown)); otherwise nothing ran, or the
// upstream definitely refused. NotSent is set when the failure happened before any byte of the
// tools/call reached the network (credential, snapshot and tool-list checks, a refused connect):
// the approval can then be released back to pending instead of failed. Changed marks a NotSent
// refusal that is the person's to resolve (the connection or tool changed), not a transient one.
type ApprovedError struct {
	Msg     string
	Unknown bool
	NotSent bool
	Changed bool
	Err     error // the classified cause (for errors.Is), never shown
}

func (e *ApprovedError) Error() string { return e.Msg }
func (e *ApprovedError) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrOutcomeUnknown) true for the ambiguous case.
func (e *ApprovedError) Is(target error) bool { return target == ErrOutcomeUnknown && e.Unknown }

// ApprovedCall is one approved proposal to run.
type ApprovedCall struct {
	Proof        Proof // the approver's login session: the proof core needs for the credential
	ConnectionID string
	URLSnapshot  string // the connection URL recorded at proposal time; the credential must carry exactly this
	Tool         string
	ToolHash     string // the hash recorded at proposal time; the live definition must still match
	Arguments    json.RawMessage
}

// ApprovedResult is the upstream's answer: a text-only, size-capped tools/call result (the same
// sanitising as for an agent) and whether the tool reported an error.
type ApprovedResult struct {
	Result  json.RawMessage
	IsError bool
}

// ExecuteApproved calls the upstream tool with exactly the frozen arguments. No agent is
// involved and nothing is stored or cached. In order: a fresh credential from core on the
// approver's login proof; the credential's URL must equal the snapshot; a live tools/list must
// still show the tool with the recorded hash; only then the tools/call, once. A failure before
// the call is sent is a definite failure; after it, only an answer (or a definite refusal such as
// a connection that could not be opened) is definite, everything else is ErrOutcomeUnknown.
func (g *Gateway) ExecuteApproved(ctx context.Context, c ApprovedCall) (ApprovedResult, error) {
	fail := func(msg string, cause error) (ApprovedResult, error) {
		return ApprovedResult{}, &ApprovedError{Msg: msg, Err: cause}
	}
	// Nothing has been sent yet: the approval may go back to pending.
	unsent := func(msg string, cause error) (ApprovedResult, error) {
		return ApprovedResult{}, &ApprovedError{Msg: msg, NotSent: true, Err: cause}
	}
	changed := func(msg string) (ApprovedResult, error) {
		return ApprovedResult{}, &ApprovedError{Msg: msg, NotSent: true, Changed: true}
	}
	if !c.Proof.Valid() || c.Proof.SessionID == "" {
		return unsent("no live sign-in to fetch the connection's credential with", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, g.cfg.CallTimeout)
	defer cancel()
	gr := &db.MCPGrant{
		ConnectionID: c.ConnectionID, AccountID: c.Proof.AccountID, URLSnapshot: c.URLSnapshot,
		ProofKind: c.Proof.Kind(), ProofValue: c.Proof.Value(),
	}
	st := &grantState{g: g, sem: make(chan struct{}, 1)}

	cred, err := st.credential(ctx, gr)
	switch {
	case errors.Is(err, ErrConnectionGone):
		return fail("the connection no longer exists, or your sign-in has ended", ErrConnectionGone)
	case err != nil:
		return unsent(upstreamMessage(err), err)
	}
	if cred.URL != c.URLSnapshot {
		return changed("the connection's address changed since this action was proposed")
	}

	tools, rerr, err := st.listUpstream(ctx, gr, maxPages, false)
	switch {
	case err != nil:
		return unsent(upstreamMessage(err), err)
	case rerr != nil:
		return unsent("the MCP server refused to list its tools", nil)
	}
	found, hash := 0, ""
	for _, t := range tools {
		if t.Name == c.Tool {
			found++
			hash = t.Hash
		}
	}
	if found != 1 || hash == "" || hash != c.ToolHash {
		return changed("the tool's definition changed (or it is no longer offered) since this action was proposed")
	}

	params := map[string]any{"name": c.Tool}
	if a := strings.TrimSpace(string(c.Arguments)); a != "" && a != "null" {
		params["arguments"] = c.Arguments
	}
	res, rerr, err := st.request(ctx, gr, "tools/call", params)
	if err != nil {
		return ApprovedResult{}, classifyCallError(ctx, err)
	}
	if rerr != nil {
		return ApprovedResult{}, classifyRPCError(rerr)
	}
	clean := sanitizeResult(res, g.cfg.MaxResultBytes)
	var flag struct {
		IsError bool `json:"isError"`
	}
	_ = json.Unmarshal(clean, &flag)
	return ApprovedResult{Result: clean, IsError: flag.IsError}, nil
}

// classifyCallError decides whether a failure of the tools/call exchange is definite or
// ambiguous. The rule: a failure is DEFINITE only when the request was refused before it could
// act (a 4xx other than 408 and 429, an authentication or session refusal) or never left (the
// connection could not be opened, TLS failed, the network policy refused it). Everything else
// (5xx, 408, 429, a redirect, an unparseable reply, a timeout, a drop after sending) may have
// arrived after the side effect and is ErrOutcomeUnknown.
func classifyCallError(ctx context.Context, err error) *ApprovedError {
	var se *upstreamStatusError
	var oe *net.OpError
	var cv *tls.CertificateVerificationError
	switch {
	case errors.Is(err, ErrConnectionGone):
		return &ApprovedError{Msg: upstreamMessage(err), Err: err}
	case errors.Is(err, errConnectionURLRefused), errors.Is(err, netguard.ErrBlockedAddress), errors.Is(err, netguard.ErrURLNotPermitted),
		errors.As(err, &cv), errors.As(err, &oe) && oe.Op == "dial":
		// The connection could not even be opened: nothing was delivered.
		return &ApprovedError{Msg: upstreamMessage(err), NotSent: true, Err: err}
	case errors.Is(err, errUpstreamAuth), errors.Is(err, errUpstreamSession):
		return &ApprovedError{Msg: upstreamMessage(err), Err: err}
	case errors.As(err, &se) && definiteStatus(se.Status):
		return &ApprovedError{Msg: upstreamMessage(err), Err: err}
	case errors.As(err, &se):
		return &ApprovedError{Unknown: true, Err: err,
			Msg: "the MCP server answered with an error status after the request may have been delivered: check whether the action happened before deciding"}
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		return &ApprovedError{Unknown: true, Err: err,
			Msg: "the request to the MCP server timed out after it may have been delivered: check whether the action happened before deciding"}
	default:
		return &ApprovedError{Unknown: true, Err: err,
			Msg: "the connection to the MCP server was lost, or its reply could not be read, after the request may have been delivered: check whether the action happened before deciding"}
	}
}

// definiteStatus is an HTTP status that proves the server rejected the request before acting: a
// client error, except a timeout (408) or rate limit (429), which a proxy in front can produce
// after forwarding.
func definiteStatus(status int) bool {
	return status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests
}

// classifyRPCError classifies a JSON-RPC error answering tools/call. Only the protocol's
// request-level errors (parse, invalid request, method not found, invalid params) prove the tool
// did not run; an internal or server error (-32603, -32000..-32099) and every other code may
// come after the side effect.
func classifyRPCError(rerr *rpcError) *ApprovedError {
	switch rerr.Code {
	case codeParseError, codeInvalidRequest, codeMethodNotFound, codeInvalidParams:
		return &ApprovedError{Msg: "the MCP server returned an error: " + capRunes(stripNUL(rerr.Message), 300)}
	}
	return &ApprovedError{Unknown: true,
		Msg: "the MCP server reported an internal error after the request may have been delivered: check whether the action happened before deciding"}
}
