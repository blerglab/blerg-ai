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
	"POST /api/sessions/{id}/discard":                  "signed-in human owner only; account then parent durable discard fence; same-owner children; strict stop delivery before archive",
	"POST /api/sessions/{id}/children/{child}/discard": "own-parent delegate credential or signed-in human owner; exact parent-child relation and same owner required before stop or archive",
	"GET /api/sessions/{id}/board-options":             "own-session messaging credential only; retained launcher proof (a started session: its parent's, while the parent may lend it) and current Core account permission, project-scoped target filtering; names only",
	"GET /api/sessions/{id}/board-search":              "own-session messaging credential only; current Core proof and selected-board scope; only public duplicate summaries, no body",
	"POST /api/sessions/{id}/file-ticket":              "own-session messaging credential only; server-derived owner/origin, current target access; private owner-only frozen proposal, no Board write or body fanout",
	"POST /api/sessions/{id}/board-create":             "own-session messaging credential only; server-derived owner; private owner-only frozen proposal, no Board write or draft fanout",
	"POST /api/sessions/{id}/filing-tools":             "same own-session credential and REST validation as CLI; one fixed built-in tool, no arbitrary upstream or account",
	"PATCH /api/proposals/{id}/draft":                  "human owner only with live sign-in proof and project target scope; pending unexpired built-in filing only, exact prior arguments CAS; origin immutable",
	"GET /api/session-boards":                          "signed-in human only; Core checks live account/session and board access, project-scoped credentials see only their board; no existing session data or credentials returned",
	"POST /api/sessions/{id}/title":                    "own-session messaging credential or daemon token only (agentSessionWrite); renames that running session only",
	"POST /api/sessions/{id}/progress":                 "own-session messaging credential or daemon token only; live daemon resolves optional origin; private progress reads use /live visibility",
	// Parent-session child operations: private surface, never authRunner.
	"GET /api/sessions/{id}/children":                                  "delegateParent verifies own-session token and parent policy; delegate principal canSee restricts to this parent child relation; files use sessionFilesFor before the common hardened artifact body",
	"GET /api/sessions/{id}/children/{child}":                          "delegateParent verifies own-session token and parent policy; delegate principal canSee restricts to this parent child relation; files use sessionFilesFor before the common hardened artifact body",
	"POST /api/sessions/{id}/children/{child}/message":                 "delegateParent verifies own-session token and parent policy; delegate principal canSee restricts to this parent child relation; files use sessionFilesFor before the common hardened artifact body",
	"GET /api/sessions/{id}/children/models":                           "delegateParent verifies the own-session token and parent policy; returns only the model catalog the parent's own owner may start children on, no other session's data",
	"POST /api/sessions/{id}/children/{child}/title":                   "delegateParent verifies own-session token and parent policy; delegate principal canSee restricts to this parent child relation; renames that child only",
	"POST /api/sessions/{id}/children/{child}/model":                   "delegateParent verifies own-session token and parent policy; delegate principal canSee restricts to this parent child relation; files use sessionFilesFor before the common hardened artifact body",
	"POST /api/sessions/{id}/children/{child}/interrupt":               "delegateParent verifies own-session token and parent policy; delegate principal canSee restricts to this parent child relation; files use sessionFilesFor before the common hardened artifact body",
	"POST /api/sessions/{id}/children/{child}/stop":                    "delegateParent verifies own-session token and parent policy; delegate principal canSee restricts to this parent child relation; files use sessionFilesFor before the common hardened artifact body",
	"GET /api/sessions/{id}/children/{child}/result":                   "delegateParent verifies own-session token and parent policy; delegate principal canSee restricts to this parent child relation; files use sessionFilesFor before the common hardened artifact body",
	"GET /api/sessions/{id}/children/{child}/events":                   "delegateParent verifies own-session token and parent policy; delegate principal canSee restricts to this parent child relation; files use sessionFilesFor before the common hardened artifact body",
	"POST /api/sessions/{id}/children/{child}/uploads":                 "delegateParent verifies own-session token and parent policy; delegate principal canSee restricts to this parent child relation; files use sessionFilesFor before the common hardened artifact body",
	"GET /api/sessions/{id}/children/{child}/artifacts":                "delegateParent verifies own-session token and parent policy; delegate principal canSee restricts to this parent child relation; files use sessionFilesFor before the common hardened artifact body",
	"GET /api/sessions/{id}/children/{child}/artifacts/{aid}/download": "delegateParent verifies own-session token and parent policy; delegate principal canSee restricts to this parent child relation; files use sessionFilesFor before the common hardened artifact body",

	"GET /api/sessions/{id}/workspace":             "human owner or account.manage administrator only; private sessions owner-only; current connection, immutable manifest and prepared runtime reference rechecked before dispatch and delivery; no transcript/broadcast",
	"GET /api/sessions/{id}/workspace-file":        "human owner or account.manage administrator only, even for public sessions; correlated current-daemon request with runtime authority rechecked before returning content",
	"GET /api/sessions/{id}/commits/{sha}":         "human owner or account.manage administrator only, even for public sessions (a private one: its owner alone, others the uniform 404); live session only; correlated current-daemon request with runtime authority rechecked before returning content",
	"GET /api/sessions/{id}/commits/{sha}/diff":    "human owner or account.manage administrator only, even for public sessions (a private one: its owner alone, others the uniform 404); live session only; correlated current-daemon request with runtime authority rechecked before returning content",
	"GET /api/sessions/{id}/plugins/catalog":       "human session owner only, signed in to the browser (sid, no on-behalf-of); others 403, a private session the uniform 404; live host session only; correlated current-daemon request; reply re-validated and bounded",
	"POST /api/sessions/{id}/plugins/marketplaces": "human session owner only (a private session: others the uniform 404); live host session only; allow-listed source; correlated current-daemon request",
	"POST /api/sessions/{id}/mcp/sign-in":          "human session owner only (a private session: others the uniform 404); live host session only; correlated current-daemon request; only an https URL from the engine is returned",
	// ── browser routes on /api/sessions ──
	"POST /api/sessions/{id}/shell-consent/{action}": "exact live human owner, origin, authenticated daemon identity and prepared runtime binding; affirmative one-use persisted challenge",
	"GET /api/sessions/{id}/shell-capability":        "no-store availability hint; exact live human owner/login and prepared runtime/policy required when enabled; false while gated",
	"GET /api/sessions/{id}/shells":                  "exact persisted human owner/login only; no admin exception or cross-login handle listing",
	"POST /api/sessions/{id}/shells":                 "exact persisted human owner/login only; current launch/runtime resolver; no admin exception",
	"GET /api/sessions":                              "list filtered by canSeeAccount in HandleGetSessions",
	"POST /api/sessions":                             "starts a NEW session (never private); names no existing one",
	"DELETE /api/sessions/{id}":                      "browserCanSee first: a private session answers 404 like an unknown one",
	"GET /api/sessions/{id}/limit-resume":            "owner-only authenticated browser; ownsSession before reading state",
	"PUT /api/sessions/{id}/limit-resume":            "owner-only authenticated browser; ownsSession before retaining explicit browser proof",
	"GET /api/sessions/{id}/plan-nudges":             "owner-only authenticated browser; ownsSession before reading the setting, others the uniform 404",
	"PUT /api/sessions/{id}/plan-nudges":             "owner-only authenticated browser; ownsSession before changing the setting, others the uniform 404",
	"POST /api/sessions/{id}/pause":                  "browserCanSee first: a private session answers 404 like an unknown one",
	"PATCH /api/sessions/{id}":                       "browserCanSee first: a private session answers 404 like an unknown one; then owner or account.manage administrator only (a shared viewer gets 403)",
	"GET /api/sessions/{id}/capabilities":            "browserCanSee: answers the same empty report as an unknown session",
	"GET /api/sessions/{id}/insights":                "person only (agent token 403); canSeeAccount, a private session answers the uniform 404",
	"GET /api/sessions/{id}/agent-events":            "daemon-token machine credential (runner pods rebuild context on resume); no human principal reaches it",
	"GET /api/sessions/{id}/pin-events":              "person only (agent token 403); account_id = caller on the query, so a foreign or unknown session lists nothing",
	// ── session artifacts (files an agent hands the user) ──
	"POST /api/sessions/{id}/artifacts":                     "the session's own credential: its board token (whose session must equal the path) or the daemon token; a person's token is refused",
	"GET /api/sessions/{id}/artifacts":                      "person only (agent token 403); browserCanSee-equivalent canSeeAccount, a private session answers the uniform 404",
	"GET /api/sessions/{id}/artifacts/{aid}/download":       "person only; canSeeAccount, a private session answers the uniform 404",
	"GET /api/sessions/{id}/artifacts/{aid}/raw":            "person only; canSeeAccount, a private session answers the uniform 404",
	"DELETE /api/sessions/{id}/artifacts/{aid}":             "person only; canSeeAccount, a private session answers the uniform 404",
	"GET /api/sessions/{id}/files":                          "the session's own credential (agentSession: its messaging token, whose session must equal the path); a person's token is refused",
	"DELETE /api/sessions/{id}/files/{aid}":                 "the session's own credential (agentSession), and only a file the session itself published",
	"POST /api/sessions/{id}/topics":                        "the session's own credential: its messaging token (whose session must equal the path) or the daemon token; a person's token is refused",
	"POST /api/sessions/{id}/marks":                         "the session's own credential (its messaging token, whose session must equal the path), the daemon token, or a signed-in person who owns the session (ownsSession; anyone else gets the uniform 404)",
	"GET /api/sessions/{id}/plugins":                        "human session owner only, live proof checked by Core; no public viewer or agent token reads",
	"POST /api/sessions/{id}/plugins":                       "human session owner only; exact approved allowlisted change, Core validates live human proof",
	"POST /api/sessions/{id}/plugin-requests":               "own session messaging token only; only queues owner-scoped approval, never installs",
	"GET /api/sessions/{id}/checklist/workers":              "the session's own messaging credential bound to the path, or daemon token; human tokens refused; native records scoped to session and direct child records require same owning account",
	"POST /api/sessions/{id}/checklist":                     "the session's own credential: its messaging token (whose session must equal the path) or the daemon token; a person's token is refused; read back through /live",
	"POST /api/sessions/{id}/tickets":                       "the session's own credential: its messaging token (whose session must equal the path) or the daemon token; a person's token is refused; read back through /live",
	"GET /api/sessions/{id}/topics":                         "person only (agent token 403); canSeeAccount, a private session answers the uniform 404",
	"POST /api/sessions/{id}/children":                      "the parent's own session token (board_id NULL) or runtime token for the path's session (delegateParent); daemon, assist, agent and browser credentials refused; owner-less, ended and child parents refused",
	"POST /api/sessions/{id}/review-requests":               "the session's own token only (its id must equal the path); names only the session's own published files",
	"GET /api/sessions/{id}/review-requests":                "the session's own token, or a person whose canSeeAccount allows the session (a private one: its owner); anything else the uniform 404",
	"GET /api/review-requests":                              "a person signed in as themselves (delegationCallerOf Account), only the pending requests of sessions they started (ListPendingReviewRequestsOwnedBy); any other credential 403",
	"GET /api/review-requests/{id}":                         "reviewReaderOf on the request's own session: its own token, or a person whose canSeeAccount allows it; anything else the uniform 404",
	"POST /api/review-requests/{id}/decide":                 "the session's owner signed in as themselves (human, no on_behalf_of) only; canSeeAccount first so a private session answers the uniform 404; CAS from pending",
	"POST /api/review-requests/{id}/withdraw":               "the session's own token, for its own request only (CAS from pending); anything else the uniform 404",
	"GET /api/delegation-requests/{id}":                     "the parent's own session token (its own requests only) or the owner's browser (who alone also gets the stored request); anything else the uniform 404",
	"POST /api/delegation-requests/{id}/expire":             "the parent's own session token, for its own request only (CAS from pending); anything else the uniform 404",
	"POST /api/delegation-requests/{id}/decide":             "human with sid, owner of the request only; anything else the uniform 404; CAS from pending; the child starts from the stored request",
	"GET /api/sessions/{id}/child-approval":                 "human owner with sid only; account lock and parent row fence; runtime/agent/delegated credentials refused",
	"GET /api/sessions/{id}/proposals/{pid}":                "the session's own token only (tokenSID == path id); the proposal must belong to that session and its owner, else a uniform 404; outcome only (card summary, note, error), never the frozen draft",
	"GET /api/sessions/{id}/permissions":                    "human owner with sid only (ownsSession, else uniform 404); grant views omit the login session the grant acts with",
	"PUT /api/sessions/{id}/board-access":                   "human owner with sid only (permissionOwner; ownsSession, else uniform 404); a level above propose needs the board in the owner's own board discovery; refused for restricted, cron and board-started sessions; the answer omits the login session a level acts with",
	"GET /api/sessions/{id}/board-access":                   "the session's own session/runtime token only (tokenSID == path id; never the daemon token); the session's own boards above propose: ids, names and levels only",
	"GET /api/sessions/{id}/board-card-url":                 "the session's own session/runtime token only (tokenSID == path id); answers the Board page address of the card the caller named, read from core's public site metadata; reads no card and no session data",
	"POST /api/sessions/{id}/board-tools":                   "the session's own session/runtime token only (tokenSID == path id; never the daemon token); the board's level read on every call; one fixed board tool through a runner-exchanged credential for that call and board, revoked after; never a credential to the caller",
	"POST /api/sessions/{id}/proposal-grants":               "human owner with sid only (ownsSession, else uniform 404); scope checked against the owner's own board discovery or this session's propose-mode tools; never broader than one board or one connection's tool",
	"PUT /api/sessions/{id}/child-approval":                 "human owner with sid only; exact current revision and parent fence; current daemon/runtime session grants only, never standing rules; session-visible audit carries opaque noncredential grant IDs (also present in child delegation_via), no owner account or credentials",
	"GET /api/delegation-rules":                             "personAccount: the caller's own rules only; names no session",
	"GET /api/desktop-ownership/{id}/enrollment-connection": "live human eligible for this exact desktop; current generation validated and private connection details written under a bounded desktop fence",
	"POST /api/desktop-ownership/{id}/enroll":               "live human eligible for this exact desktop; explicit confirmation of displayed current generation, origin guard, audit and private socket delivery; no browser bearer",
	"GET /api/desktop-ownership":                            "live human admin only; lists only unknown/own desktop labels and active account directory, never repository or filesystem metadata",
	"PUT /api/desktop-ownership/{id}":                       "live human admin only; enabled target owner and exact revision, ownership and connection fenced before revocation",
	"POST /internal/desktops/bootstrap":                     "component internal key only; installer owner or first admin from core, adopts only unknown desktop or preserves existing owner",
	"POST /api/delegation-rules":                            "person with sid; a rule of the caller's own account; names no session",
	"DELETE /api/delegation-rules/{id}":                     "person with sid; only the caller's own rule, else the uniform 404",
	"POST /api/delegation-grants/{id}/revoke":               "person with sid; only the caller's own grant, else the uniform 404",
	"GET /api/sessions/{id}/budget":                         "the session's own token (board_id NULL) or its owner's browser; anything else the uniform 404; reads only the session's own daemon",
	"GET /api/sessions/{id}/live":                           "person only (agent token 403); canSeeAccount, a private session answers the uniform 404; tokens and nonbilling child-usage snapshots per the Insights rule; child activity requires independent child visibility; tool changes and pending proposals are the viewer's own; MCP inventory, first-prompt hold and engine plans follow the same session privacy boundary",
	"POST /api/sessions/{id}/uploads":                       "person only (agent token 403); canSeeAccount, a private session answers the uniform 404",
	"POST /api/sessions/{id}/storage":                       "person only (agent token 403); canSeeAccount, a private session answers the uniform 404; then administrator only",
	"GET /api/sessions/{id}/attachments":                    "the session's own credential (its token's session must equal the path) or the daemon token; a person's token is refused",
	"GET /api/sessions/{id}/attachments/{aid}/file":         "the session's own credential (its token's session must equal the path) or the daemon token; only user-origin files, else the uniform 404",
	// ── runner contract (REST and, through the same functions, MCP) ──
	"GET /api/runner/sessions/{id}":               "requireSessionAccess -> canSee",
	"POST /api/runner/sessions/{id}/model":        "SetSessionModel: requireSessionAccess -> canSee; session engine from stored row, never caller-supplied",
	"POST /api/runner/sessions/{id}/message":      "requireSessionAccess -> canSee",
	"POST /api/runner/sessions/{id}/interrupt":    "requireSessionAccess -> canSee",
	"POST /api/runner/sessions/{id}/stop":         "requireSessionAccess -> canSee",
	"GET /api/runner/sessions/{id}/events":        "requireSessionAccess -> canSee",
	"GET /api/runner/sessions/{id}/events/stream": "requireSessionAccess -> canSee",
	"GET /api/runner/sessions/{id}/events/live":   "requireSessionAccess -> canSee before any frame; the hub watcher carries the principal's account, so broadcasts are scoped like a socket's and MarkPrivate evicts a foreign one",
	"GET /api/runner/sessions/{id}/result":        "requireSessionAccess -> canSee",
	// ── session files on the runner contract (runner_files.go) ──
	"GET /api/runner/sessions/{id}/artifacts":                "runnerSessionFiles: requireSessionAccess -> canSee, then the browser route's body",
	"GET /api/runner/sessions/{id}/artifacts/{aid}/raw":      "runnerSessionFiles: requireSessionAccess -> canSee, then the browser route's body",
	"GET /api/runner/sessions/{id}/artifacts/{aid}/download": "runnerSessionFiles: requireSessionAccess -> canSee, then the browser route's body",
	"DELETE /api/runner/sessions/{id}/artifacts/{aid}":       "runnerSessionFiles: requireSessionAccess -> canSee, then the browser route's body",
	"POST /api/runner/sessions/{id}/uploads":                 "runnerSessionFiles: requireSessionAccess -> canSee, then the browser route's body",
	"POST /api/runner/sessions/{id}/storage":                 "runnerSessionFiles: requireSessionAccess -> canSee, then administrator only",
	// ── sockets ──
	"/ws/browser": "outbound: BroadcastToBrowsers scopes by the payload's session; initial_state filters with canSeeAccount/ListMessagesFor; inbound: every frame naming a session passes accountCanSee; release_held_prompt additionally requires the actual session owner and a current daemon",
	"/ws/daemon":  "host machine credential or immutable current pod launch; pod traffic is scoped in both directions and revalidates revocation",
	"/mcp":        "MCP tools call the runner-contract functions (requireSessionAccess -> canSee)",
	// ── current session access-only engine credential ──
	"GET /api/session/plugin-marketplace-policy": "current pod runtime token bound to session_id; fresh Core policy only, no cached fallback; runtime rechecked before delivery",
	"POST /api/session/credentials/access":       "current pod runtime token (messaging token refused) + immutable private launch owner/engine/proof binding; Core principal checks and runtime recheck before access-only disclosure",
	// ── messages (session -> user) ──
	"GET /api/messages":               "db.ListMessagesFor(viewer): private sessions' messages are the owner's alone",
	"POST /api/messages":              "session's own credential (daemon token, or a board token bound to that session); broadcast is scoped, push skips private",
	"GET /api/messages/{id}/answer":   "session's own credential; a board token must own the message",
	"POST /api/messages/{id}/reply":   "coreActorCanSeeMessage for a person; daemon token and the session's board token are the session's side",
	"POST /api/messages/{id}/expire":  "session's own credential; a board token must own the message",
	"POST /api/messages/{id}/dismiss": "a person signed in as themselves (coreAnswerer) who passes coreActorCanSeeMessage; the owner for an owner-only question; the session's own credential, the daemon and board tokens are refused",
	// ── artifacts ──
	"POST /api/screenshots": "operator token or live owned-desktop session message token; explicit publication stored under an unguessable non-enumerable id",
	"GET /s/":               "screenshot served by unguessable id; carries no session id and is not enumerable",
	"POST /api/mockups":     "operator token or live owned-desktop session message token; explicit publication stored under an unguessable non-enumerable id",
	"GET /m/":               "mockup served by unguessable id; carries no session id and is not enumerable",
	// ── push ──
	"POST /api/push/subscribe":       "stores a browser subscription; names no session",
	"DELETE /api/push/subscribe":     "removes a browser subscription by its unguessable endpoint; names no session",
	"GET /api/push/vapid-public-key": "public key; names no session",
}

// sessionDataPrefixes are routes that carry session data without having
// "session" in their pattern. They are held to routeDecisions too.
var sessionDataPrefixes = []string{
	"/api/messages", "/api/screenshots", "/api/mockups", "/api/push", "/api/review-requests",
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
	"desktop_visibility.go:broadcastOwnedDesktop:BroadcastJSONPerAccount":       {1, "exact persisted desktop owner only, with private-session filtering at delivery"},
	"session_discard.go:handleSessionDiscard:BroadcastJSONPerAccount":           {1, "owner-only durable archive notification; immutable same-account parent/child ownership checked before mutation"},
	"delegation_discard.go:handleChildDiscard:BroadcastJSON":                    {1, whyScoped + "; parent and child independently scoped"},
	"delegation_children_live.go:broadcastChildrenChanged:BroadcastJSON":        {1, whyScoped + "; parent and triggering child are independently privacy-scoped"},
	"agentevents.go:HandleAgentEvent:BroadcastJSON":                             {2, whyScoped},
	"agentevents.go:HandleAgentEvent:FanOutAgentEvent":                          {1, whySubscribers},
	"background_reconcile.go:publishBackgroundTaskReconciliation:BroadcastJSON": {1, whyScoped},
	"sessionpaused.go:handleSessionPaused:BroadcastJSON":                        {2, whyScoped},
	"sessionpaused.go:clearPausedOnMessage:BroadcastJSON":                       {1, whyScoped},
	"agentevents.go:handleSubscribeAgentEvents:Subscribe":                       {1, whySubscribers},
	"session_rename.go:renameSession:BroadcastTitleChanged":                     {1, whyScoped + "; every route refuses non-owners first (browserCanSee and owner or admin, the parent's own child, the session's own token)"},
	"boards.go:HandlePostBoards:BroadcastBoard":                                 {1, whyBoard},
	"artifacts.go:recordArtifactEvent:FanOutAgentEvent":                         {1, whySubscribers},
	"browser_conn.go:browserReadPump:BroadcastJSON":                             {2, whyScoped},
	"browser_conn.go:handleSubscribeSession:SendToBrowser":                      {1, whyFocus},
	"browser_conn.go:handleSubscribeSession:Subscribe":                          {1, whySubscribers},
	"browser_conn.go:releaseFocus:SendToBrowser":                                {1, whyFocus},
	"columns.go:HandleDeleteColumn:BroadcastBoard":                              {1, whyBoard},
	"columns.go:HandlePatchColumn:BroadcastBoard":                               {1, whyBoard},
	"columns.go:HandlePostColumn:BroadcastBoard":                                {1, whyBoard},
	"daemon_conn.go:handleDaemonFrame:FanOutSessionOutput":                      {2, whySubscribers},
	"daemon_conn.go:setSessionStatusEnd:broadcastWithEnd":                       {1, whyScoped},
	"events_live.go:HandleRunnerEventsLive:Subscribe":                           {1, "requireSessionAccess (canSee) precedes the subscription; the same id is registered as a browser under the principal's account, so MarkPrivate evicts it like any foreign socket"},
	"jobreconcile.go:broadcastReconciledStatus:broadcastWithEnd":                {1, whyScoped},
	"messageevents.go:answerAskFrom:BroadcastJSON":                              {1, whyScoped},
	"messageevents.go:fanOutServerEvent:FanOutAgentEvent":                       {1, whySubscribers},
	"messages.go:HandlePostMessageExpire:BroadcastJSON":                         {1, whyScoped},
	"messages.go:replyMessageUnlocked:BroadcastJSON":                            {1, whyScoped},
	"questions.go:dismissMessageUnlocked:BroadcastJSON":                         {1, whyScoped},
	"messages.go:HandlePostMessages:BroadcastJSON":                              {1, whyScoped},
	"messages.go:HandlePostMessages:BroadcastJSONPerAccount":                    {1, whyScoped},
	"desktop.go:desktopOwnershipChanged:BroadcastJSONPerAccount":                {1, "old/new owner receive only desktop removal and revision; everyone else receives a metadata-free invalidation"},
	"desktop_internal.go:renameDesktopFor:BroadcastJSONPerAccount":              {1, "every account receives only a metadata-free desktop inventory invalidation"},
	"reviewrequests.go:pushForReview:SendPush":                                  {1, whyPush},
	"delegation_start.go:pushForDelegation:SendPush":                            {1, whyPush},
	"messages.go:pushForMessage:SendPush":                                       {1, whyPush},
	"notices.go:sendSessionNotice:BroadcastJSON":                                {1, whyScoped},
	"notices.go:sendSessionNotice:SendPush":                                     {1, whyPush},
	"sessionend.go:broadcastWithEnd:BroadcastJSON":                              {1, whyScoped},
	"sessionend.go:broadcastWithEnd:BroadcastJSONPerAccount":                    {1, whyScoped},
	"sessions.go:handleSessionEndedEffects:BroadcastJSON":                       {1, whyScoped},
	"sessions.go:handleSessionEndedEffects:broadcastWithEnd":                    {1, whyScoped},
	"sessions.go:HandleSessionMetaChanged:BroadcastJSON":                        {1, whyScoped},
	"sessions.go:HandleSessionOutput:FanOutSessionOutput":                       {1, whySubscribers},
	"sessions.go:HandleSessionStarted:BroadcastJSONPerAccount":                  {1, whyScoped},
	"sessions.go:HandleSessionStateChanged:broadcastWithEnd":                    {1, whyScoped},
	"sessions.go:handleSessionNotFound:BroadcastJSON":                           {1, whyScoped},
	"startstages.go:broadcastSessionCreated:BroadcastJSONPerAccount":            {1, whyScoped},
	"startstages.go:markClusterStartFailed:broadcastWithEnd":                    {1, whyScoped},
	"startstages.go:persistStartStages:FanOutAgentEvent":                        {1, whySubscribers},
	"tickets.go:HandleAddDependency:BroadcastBoard":                             {2, whyBoard},
	"tickets.go:HandleArchiveTicket:BroadcastBoard":                             {1, whyBoard},
	"tickets.go:HandlePatchTicket:BroadcastBoard":                               {2, whyBoard},
	"tickets.go:HandlePostTicket:BroadcastBoard":                                {1, whyBoard},
	"tickets.go:HandleRemoveDependency:BroadcastBoard":                          {2, whyBoard},
	"tickets.go:HandleSplitTicket:BroadcastBoard":                               {1, whyBoard},
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
	"FanOutAgentEvent":        true,
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
