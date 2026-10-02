package server

// A guard for session privacy (privacy.go). It does not prove a surface is
// correct (privacy_test.go does); it fails when a NEW one appears with no
// recorded visibility decision, so adding a route or a broadcast is a
// conscious choice rather than a silent leak.
//
//   - every registered route that names a session (or is one of the routes
//     that carry session data by another name) must appear in routeDecisions;
//   - every call that pushes data to a browser, a push subscription or a
//     session's subscribers must appear in emitDecisions, with its count.
//
// A stale entry (a route or call that no longer exists) fails too, so the
// tables cannot rot into a list nobody reads.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// routeDecisions: route pattern (as registered, method included) -> the
// visibility decision that covers it.
var routeDecisions = map[string]string{
	// ── browser routes on /api/sessions ──
	"GET /api/sessions":                   "list filtered by canSeeAccount in HandleGetSessions",
	"POST /api/sessions":                  "starts a NEW session (never private); names no existing one",
	"DELETE /api/sessions/{id}":           "browserCanSee first: a private session answers 404 like an unknown one",
	"POST /api/sessions/{id}/pause":       "browserCanSee first: a private session answers 404 like an unknown one",
	"PATCH /api/sessions/{id}":            "browserCanSee first: a private session answers 404 like an unknown one",
	"GET /api/sessions/{id}/capabilities": "browserCanSee: answers the same empty report as an unknown session",
	"GET /api/sessions/{id}/agent-events": "daemon-token machine credential (runner pods rebuild context on resume); no human principal reaches it",
	// ── session artifacts (files an agent hands the user) ──
	"POST /api/sessions/{id}/artifacts":               "the session's own credential: its board token (whose session must equal the path) or the daemon token; a person's token is refused",
	"GET /api/sessions/{id}/artifacts":                "person only (agent token 403); browserCanSee-equivalent canSeeAccount, a private session answers the uniform 404",
	"GET /api/sessions/{id}/artifacts/{aid}/download": "person only; canSeeAccount, a private session answers the uniform 404",
	"GET /api/sessions/{id}/artifacts/{aid}/raw":      "person only; canSeeAccount, a private session answers the uniform 404",
	"DELETE /api/sessions/{id}/artifacts/{aid}":       "person only; canSeeAccount, a private session answers the uniform 404",
	"POST /api/sessions/{id}/uploads":                 "person only (agent token 403); canSeeAccount, a private session answers the uniform 404",
	"GET /api/sessions/{id}/attachments":              "the session's own credential (its token's session must equal the path) or the daemon token; a person's token is refused",
	"GET /api/sessions/{id}/attachments/{aid}/file":   "the session's own credential (its token's session must equal the path) or the daemon token; only user-origin files, else the uniform 404",
	// ── runner contract (REST and, through the same functions, MCP) ──
	"GET /api/runner/sessions/{id}":               "requireSessionAccess -> canSee",
	"POST /api/runner/sessions/{id}/message":      "requireSessionAccess -> canSee",
	"POST /api/runner/sessions/{id}/interrupt":    "requireSessionAccess -> canSee",
	"POST /api/runner/sessions/{id}/stop":         "requireSessionAccess -> canSee",
	"GET /api/runner/sessions/{id}/events":        "requireSessionAccess -> canSee",
	"GET /api/runner/sessions/{id}/events/stream": "requireSessionAccess -> canSee",
	"GET /api/runner/sessions/{id}/result":        "requireSessionAccess -> canSee",
	// ── sockets ──
	"/ws/browser": "outbound: BroadcastToBrowsers scopes by the payload's session; initial_state filters with canSeeAccount/ListMessagesFor; inbound: every frame naming a session passes accountCanSee",
	"/ws/daemon":  "daemon -> server only; the daemon is a machine credential and receives no other session's data",
	"/mcp":        "MCP tools call the runner-contract functions (requireSessionAccess -> canSee)",
	// ── messages (session -> user roll-up) ──
	"GET /api/messages":              "db.ListMessagesFor(viewer): private sessions' messages are the owner's alone",
	"POST /api/messages":             "session's own credential (daemon token, or a board token bound to that session); broadcast is scoped, push skips private",
	"GET /api/messages/{id}/answer":  "session's own credential; a board token must own the message",
	"POST /api/messages/{id}/reply":  "coreActorCanSeeMessage for a person; daemon token and the session's board token are the session's side",
	"POST /api/messages/{id}/expire": "session's own credential; a board token must own the message",
	// ── artifacts and previews ──
	"POST /api/screenshots": "daemon token; stored under an unguessable id with no session association",
	"GET /s/":               "screenshot served by unguessable id; carries no session id and is not enumerable",
	"POST /api/mockups":     "daemon token; stored under an unguessable id with no session association",
	"GET /m/":               "mockup served by unguessable id; carries no session id and is not enumerable",
	"POST /api/preview":     "daemon token; a push that names a session is scoped like any broadcast (unattributed pushes are the known gap)",
	// ── push ──
	"POST /api/push/subscribe":       "stores a browser subscription; names no session",
	"GET /api/push/vapid-public-key": "public key; names no session",
}

// sessionDataPrefixes are routes that carry session data without having
// "session" in their pattern. They are held to routeDecisions too.
var sessionDataPrefixes = []string{
	"/api/messages", "/api/screenshots", "/api/mockups", "/api/preview", "/api/push",
	"/s/", "/m/", "/mcp", "/ws/",
}

func routeNeedsDecision(pattern string) bool {
	path := pattern
	if _, rest, ok := strings.Cut(pattern, " "); ok {
		path = rest
	}
	if strings.Contains(strings.ToLower(path), "session") {
		return true
	}
	for _, p := range sessionDataPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// emitDecisions: "file:function:callee" -> {how many calls, why it is safe}.
// The callees are every way this package pushes data to a browser, a push
// subscription, or the subscribers of a session.
const (
	whyScoped      = "central: BroadcastToBrowsers reads the payload's session and delivers a private one only to its owner's sockets"
	whyBoard       = "board event: a ticket never carries a private session (SetTicketSession refuses one, SetSessionPrivate unbinds it)"
	whySubscribers = "the session's subscribers: a socket gets in only through the inbound gate (accountCanSee) in browserReadPump, and MarkPrivate evicts the rest"
	whyFocus       = "goes to a socket already subscribed to the session, so already past the inbound gate"
	whyPush        = "private sessions push nothing: skipped here and again inside SendPush (subscriptions belong to no account)"
)

var emitDecisions = map[string]emitDecision{
	"agentevents.go:HandleAgentEvent:BroadcastJSON":              {1, whyScoped},
	"agentevents.go:HandleAgentEvent:FanOutSessionOutput":        {1, whySubscribers},
	"agentevents.go:handleSubscribeAgentEvents:Subscribe":        {1, whySubscribers},
	"api.go:HandlePatchSession:BroadcastTitleChanged":            {1, whyScoped + "; the route also refuses non-owners first (browserCanSee)"},
	"boards.go:HandlePostBoards:BroadcastBoard":                  {1, whyBoard},
	"artifacts.go:recordArtifactEvent:FanOutSessionOutput":       {1, whySubscribers},
	"browser_conn.go:browserReadPump:BroadcastJSON":              {2, whyScoped},
	"browser_conn.go:handleSubscribeSession:SendToBrowser":       {1, whyFocus},
	"browser_conn.go:handleSubscribeSession:Subscribe":           {1, whySubscribers},
	"browser_conn.go:releaseFocus:SendToBrowser":                 {1, whyFocus},
	"columns.go:HandleDeleteColumn:BroadcastBoard":               {1, whyBoard},
	"columns.go:HandlePatchColumn:BroadcastBoard":                {1, whyBoard},
	"columns.go:HandlePostColumn:BroadcastBoard":                 {1, whyBoard},
	"daemon_conn.go:daemonReadPump:FanOutSessionOutput":          {2, whySubscribers},
	"daemon_conn.go:handleDaemonDisconnect:BroadcastJSON":        {1, whyScoped + "; a daemon_disconnected keeps only the sessions each socket may see"},
	"daemon_conn.go:setSessionStatusEnd:broadcastWithEnd":        {1, whyScoped},
	"jobreconcile.go:broadcastReconciledStatus:broadcastWithEnd": {1, whyScoped},
	"messages.go:HandlePostMessageExpire:BroadcastJSON":          {1, whyScoped},
	"messages.go:HandlePostMessageReply:BroadcastJSON":           {1, whyScoped},
	"messages.go:HandlePostMessages:BroadcastJSON":               {1, whyScoped},
	"messages.go:pushForMessage:SendPush":                        {1, whyPush},
	"preview.go:HandlePreview:BroadcastJSON":                     {1, whyScoped + "; the push carries the session when the pusher names it (unattributed pushes are the known gap)"},
	"sessionend.go:broadcastWithEnd:BroadcastJSON":               {1, whyScoped},
	"sessionend.go:broadcastWithEnd:BroadcastJSONPerAccount":     {1, whyScoped},
	"sessions.go:HandleSessionEnded:BroadcastJSON":               {1, whyScoped},
	"sessions.go:HandleSessionEnded:broadcastWithEnd":            {1, whyScoped},
	"sessions.go:HandleSessionMetaChanged:BroadcastJSON":         {1, whyScoped},
	"sessions.go:HandleSessionOutput:FanOutSessionOutput":        {1, whySubscribers},
	"sessions.go:HandleSessionStarted:BroadcastJSON":             {1, whyScoped},
	"sessions.go:HandleSessionStateChanged:SendPush":             {1, whyPush},
	"sessions.go:HandleSessionStateChanged:broadcastWithEnd":     {1, whyScoped},
	"sessions.go:handleSessionNotFound:BroadcastJSON":            {1, whyScoped},
	"startstages.go:broadcastSessionCreated:BroadcastJSON":       {1, whyScoped},
	"startstages.go:markClusterStartFailed:broadcastWithEnd":     {1, whyScoped},
	"startstages.go:persistStartStages:FanOutSessionOutput":      {1, whySubscribers},
	"tickets.go:HandleAddDependency:BroadcastBoard":              {2, whyBoard},
	"tickets.go:HandleArchiveTicket:BroadcastBoard":              {1, whyBoard},
	"tickets.go:HandlePatchTicket:BroadcastBoard":                {2, whyBoard},
	"tickets.go:HandlePostTicket:BroadcastBoard":                 {1, whyBoard},
	"tickets.go:HandleRemoveDependency:BroadcastBoard":           {2, whyBoard},
	"tickets.go:HandleSplitTicket:BroadcastBoard":                {1, whyBoard},
}

type emitDecision struct {
	count int
	why   string
}

// emitCallees are the calls held to emitDecisions.
var emitCallees = map[string]bool{
	"BroadcastToBrowsers":     true,
	"BroadcastJSON":           true,
	"BroadcastJSONPerAccount": true,
	"BroadcastTitleChanged":   true,
	"BroadcastBoard":          true,
	"SendToBrowser":           true,
	"FanOutSessionOutput":     true,
	"SendPush":                true,
	"Subscribe":               true,
	"broadcastWithEnd":        true,
}

func parseServerFiles(t *testing.T, pattern string) map[string]*ast.File {
	t.Helper()
	paths, err := filepath.Glob(pattern)
	if err != nil || len(paths) == 0 {
		t.Fatalf("no files for %s (%v)", pattern, err)
	}
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		out[filepath.Base(p)] = f
	}
	return out
}

func TestSessionPrivacy_EveryRouteHasADecision(t *testing.T) {
	registered := map[string]bool{}
	for _, glob := range []string{"*.go", "../../cmd/server/*.go"} {
		for _, f := range parseServerFiles(t, glob) {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if pat, err := strconv.Unquote(lit.Value); err == nil {
					registered[pat] = true
				}
				return true
			})
		}
	}
	if len(registered) < 20 {
		t.Fatalf("found only %d registered routes; the scan is broken", len(registered))
	}

	var missing, stale []string
	for pat := range registered {
		if routeNeedsDecision(pat) {
			if _, ok := routeDecisions[pat]; !ok {
				missing = append(missing, pat)
			}
		}
	}
	for pat, why := range routeDecisions {
		if !registered[pat] {
			stale = append(stale, pat)
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("route %q has an empty decision", pat)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	for _, p := range missing {
		t.Errorf("route %q names a session (or carries session data) and has no visibility decision: "+
			"filter it with canSee/canSeeAccount or record why it needs none in routeDecisions", p)
	}
	for _, p := range stale {
		t.Errorf("routeDecisions lists %q, which is no longer registered", p)
	}
}

func TestSessionPrivacy_EveryEmitSiteHasADecision(t *testing.T) {
	found := map[string]int{}
	for name, f := range parseServerFiles(t, "*.go") {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee := ""
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					callee = fun.Sel.Name
				case *ast.Ident:
					callee = fun.Name
				}
				if emitCallees[callee] && !isHubOrPushDefinition(name, fn.Name.Name) {
					found[name+":"+fn.Name.Name+":"+callee]++
				}
				return true
			})
		}
	}

	var problems []string
	for key, n := range found {
		d, ok := emitDecisions[key]
		switch {
		case !ok:
			problems = append(problems, "no visibility decision for "+key+" (add it to emitDecisions with the reason it cannot leak a private session)")
		case d.count != n:
			problems = append(problems, key+": "+strconv.Itoa(n)+" call(s), decision covers "+strconv.Itoa(d.count))
		}
	}
	for key, d := range emitDecisions {
		if _, ok := found[key]; !ok {
			problems = append(problems, "emitDecisions lists "+key+", which no longer exists")
		}
		if strings.TrimSpace(d.why) == "" {
			problems = append(problems, "empty reason for "+key)
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// The hub keeps its own record of private sessions and MarkPrivate is the only
// thing that updates it, so nothing else may write the flag.
func TestSessionPrivacy_OnlyMarkPrivateWritesTheFlag(t *testing.T) {
	for _, glob := range []string{"*.go", "../mcp/*.go", "../../cmd/*/*.go"} {
		for name, f := range parseServerFiles(t, glob) {
			if name == "privacy.go" {
				continue
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "SetSessionPrivate" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "db" {
						t.Errorf("%s calls db.SetSessionPrivate; use (*API).MarkPrivate so the hub's record stays right", name)
					}
				}
				return true
			})
		}
	}
}

// isHubOrPushDefinition skips the helpers' own bodies: a helper that calls the
// primitive it wraps (BroadcastJSON -> BroadcastToBrowsers) is not a new site.
func isHubOrPushDefinition(file, fn string) bool {
	return file == "hub.go" && (strings.HasPrefix(fn, "Broadcast") || fn == "SendToBrowser" || fn == "Subscribe")
}
