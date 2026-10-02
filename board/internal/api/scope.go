package api

import (
	"context"
	"net/http"

	"github.com/blerglab/blerg-ai/board/internal/auth"
)

// Project scoping.
//
// A core-issued AGENT token carrying a Project claim is scoped to exactly one
// board (auth.Principal.ProjectScoped). The capability checks inside every
// handler already narrow such a token (auth maps the claim onto Token.BoardID),
// but they answer 403 for an object that exists on another board and 404 for
// one that does not, which would let the token probe for ids. So, in front of
// every authenticated route, a scoped principal goes through scopeDecision:
// an id that does not belong to the token's board is answered exactly like an
// id that does not exist (404 "not found"), and routes with no board to scope
// to are refused outright. Tokens without a Project claim never enter here.
//
// routeScopes is the explicit decision for EVERY registered route: a new route
// without an entry is refused for scoped tokens, and TestEveryRouteHasAScopeDecision
// fails until one is added.

// scopeRule says how a route is scoped for a project-scoped principal.
type scopeRule int

const (
	// scopePublic: registered without authentication; no principal involved.
	scopePublic scopeRule = iota + 1
	// scopeSelf: the handler itself narrows or refuses a board-scoped token
	// (list/search filter to the token's board; /ws checks the named board).
	scopeSelf
	// scopeBoard: {id} is a board id.
	scopeBoard
	// scopeCard, scopeColumn, scopeReview, scopeStanding, scopeSession: {id}
	// names an object that belongs to some board.
	scopeCard
	scopeColumn
	scopeReview
	scopeStanding
	scopeSession
	// scopeBoardStart / scopeCardStart: {id} is a board / card id, and the
	// route starts a runner session or run, which mints a native 12-hour board
	// token. A project-scoped principal is refused even on its own board (403,
	// after the ownership check so a foreign id still answers 404): an exchange
	// token exists for card work only.
	scopeBoardStart
	scopeCardStart
	// scopeQueryBoard: optional ?board= must be the token's board when given.
	scopeQueryBoard
	// scopeDeny: instance-wide; no single board to scope it to.
	scopeDeny
)

type routeScope struct {
	rule scopeRule
	why  string
}

// routeScopes is keyed by the exact mux pattern.
var routeScopes = map[string]routeScope{
	"GET /onboard":                 {scopePublic, "static onboarding text"},
	"GET /agents":                  {scopePublic, "static agent contract"},
	"GET /openapi.json":            {scopePublic, "static spec"},
	"GET /ws":                      {scopeSelf, "handleWS requires card.read on the named board (token scoped elsewhere: 403) before upgrading"},
	"GET /api/me":                  {scopeSelf, "returns the caller's own identity, incl. its board_id"},
	"GET /api/boards":              {scopeSelf, "handler filters the list to the token's board"},
	"POST /api/boards":             {scopeDeny, "creating a board is instance-wide"},
	"GET /api/search":              {scopeSelf, "handler forces the token's board into the query"},
	"GET /api/overview":            {scopeDeny, "cross-board overview"},
	"GET /api/sessions":            {scopeDeny, "cross-board session list"},
	"GET /api/tokens":              {scopeDeny, "instance-wide token admin"},
	"POST /api/tokens":             {scopeDeny, "instance-wide token admin"},
	"POST /api/tokens/refresh":     {scopeDeny, "only native board tokens refresh"},
	"POST /api/tokens/{id}/revoke": {scopeDeny, "instance-wide token admin"},
	"POST /api/deployments":        {scopeDeny, "board named in the body, admin tier"},
	"GET /api/reviews":             {scopeQueryBoard, "review list filtered by ?board=; unfiltered is cross-board (handler 403)"},

	"GET /api/boards/{id}":                        {scopeBoard, "board read"},
	"PATCH /api/boards/{id}":                      {scopeBoard, "board update"},
	"DELETE /api/boards/{id}":                     {scopeBoard, "board delete"},
	"GET /api/boards/{id}/columns":                {scopeBoard, "columns of the board"},
	"POST /api/boards/{id}/columns":               {scopeBoard, "create column"},
	"GET /api/boards/{id}/cards":                  {scopeBoard, "cards of the board"},
	"POST /api/boards/{id}/cards":                 {scopeBoard, "create card"},
	"GET /api/boards/{id}/cards/search":           {scopeBoard, "search within the board"},
	"GET /api/boards/{id}/schema":                 {scopeBoard, "field schema"},
	"GET /api/boards/{id}/metrics":                {scopeBoard, "board metrics"},
	"GET /api/boards/{id}/icon":                   {scopeBoard, "board icon"},
	"POST /api/boards/{id}/sessions":              {scopeBoardStart, "spawn board session"},
	"GET /api/boards/{id}/sessions":               {scopeBoard, "board sessions"},
	"GET /api/boards/{id}/active-sessions":        {scopeBoard, "board active sessions"},
	"GET /api/boards/{id}/deployments":            {scopeBoard, "board deployments"},
	"POST /api/boards/{id}/run":                   {scopeBoardStart, "start board run"},
	"POST /api/boards/{id}/run/stop":              {scopeBoard, "stop board run"},
	"GET /api/boards/{id}/run":                    {scopeBoard, "board run state"},
	"GET /api/boards/{id}/standing-agents":        {scopeBoard, "board standing agents"},
	"POST /api/boards/{id}/standing-agents":       {scopeBoard, "create standing agent"},
	"PATCH /api/columns/{id}":                     {scopeColumn, "rename column"},
	"DELETE /api/columns/{id}":                    {scopeColumn, "delete column"},
	"POST /api/columns/{id}/move":                 {scopeColumn, "move column"},
	"GET /api/cards/{id}":                         {scopeCard, "card read"},
	"PATCH /api/cards/{id}":                       {scopeCard, "card update"},
	"DELETE /api/cards/{id}":                      {scopeCard, "card delete"},
	"POST /api/cards/{id}/move":                   {scopeCard, "card move"},
	"POST /api/cards/{id}/archive":                {scopeCard, "card archive"},
	"POST /api/cards/{id}/dependencies":           {scopeCard, "add dependency (handler also scopes depends_on)"},
	"DELETE /api/cards/{id}/dependencies/{depID}": {scopeCard, "remove dependency"},
	"GET /api/cards/{id}/events":                  {scopeCard, "card events"},
	"POST /api/cards/{id}/comments":               {scopeCard, "card comment"},
	"GET /api/cards/{id}/diff":                    {scopeCard, "card diff"},
	"GET /api/cards/{id}/git":                     {scopeCard, "card git info"},
	"GET /api/cards/{id}/stats":                   {scopeCard, "card stats"},
	"GET /api/cards/{id}/doc":                     {scopeCard, "card doc"},
	"POST /api/cards/{id}/spawn":                  {scopeCardStart, "spawn session from card"},
	"GET /api/cards/{id}/runner-sessions":         {scopeCard, "card runner sessions"},
	"POST /api/cards/{id}/accept":                 {scopeCard, "accept card (admin)"},
	"GET /api/reviews/{id}":                       {scopeReview, "review read"},
	"POST /api/reviews/{id}/resolve":              {scopeReview, "resolve held review (human admin)"},
	"PATCH /api/standing-agents/{id}":             {scopeStanding, "update standing agent"},
	"DELETE /api/standing-agents/{id}":            {scopeStanding, "delete standing agent"},
	"POST /api/runner-sessions/{id}/close":        {scopeSession, "close session"},
	"GET /api/runner-sessions/{id}/events":        {scopeSession, "session events"},
	"POST /api/runner-sessions/{id}/message":      {scopeSession, "message session"},
	"POST /api/runner-sessions/{id}/interrupt":    {scopeSession, "interrupt session"},
	"POST /api/runner-sessions/{id}/model":        {scopeSession, "re-model session; the literal \"self\" is refused (a core token owns no session)"},
}

// RouteScopeDecision reports the reason recorded for a mux pattern's
// project-scope decision, and whether it has one. The server-wide guard test
// (cmd/blerg-board-server) uses it to check every registered route.
func RouteScopeDecision(pattern string) (why string, ok bool) {
	rs, ok := routeScopes[pattern]
	return rs.why, ok
}

// notFoundForScope is the one answer for every out-of-scope or missing object.
func notFoundForScope(w http.ResponseWriter) { writeError(w, http.StatusNotFound, "not found") }

// scopeDecision decides whether a project-scoped principal may enter the
// route. It returns false after writing the refusal.
func (a *API) scopeDecision(w http.ResponseWriter, r *http.Request, board string) bool {
	rs, ok := routeScopes[r.Pattern]
	if !ok {
		writeError(w, http.StatusForbidden, "not available to a board-scoped token")
		return false
	}
	id := r.PathValue("id")
	ctx := r.Context()
	var owned bool
	switch rs.rule {
	case scopePublic, scopeSelf:
		return true
	case scopeDeny:
		writeError(w, http.StatusForbidden, "not available to a board-scoped token")
		return false
	case scopeBoard, scopeBoardStart:
		owned = id == board
	case scopeQueryBoard:
		q := r.URL.Query().Get("board")
		owned = q == "" || q == board // empty: the handler refuses the cross-board list itself
	case scopeCard, scopeCardStart:
		owned = a.ownedBy(ctx, OwnerCard, id, board)
	case scopeColumn:
		owned = a.ownedBy(ctx, OwnerColumn, id, board)
	case scopeReview:
		owned = a.ownedBy(ctx, OwnerReview, id, board)
	case scopeStanding:
		owned = a.ownedBy(ctx, OwnerStanding, id, board)
	case scopeSession:
		owned = a.ownedBy(ctx, OwnerSession, id, board)
	}
	if !owned {
		notFoundForScope(w)
		return false
	}
	if rs.rule == scopeBoardStart || rs.rule == scopeCardStart {
		writeError(w, http.StatusForbidden, "not available to a board-scoped token")
		return false
	}
	return true
}

// OwnerKind names what an id addresses, for OwnerBoard.
type OwnerKind string

const (
	OwnerCard     OwnerKind = "card"
	OwnerColumn   OwnerKind = "column"
	OwnerReview   OwnerKind = "review"
	OwnerStanding OwnerKind = "standing agent"
	OwnerSession  OwnerKind = "runner session"
)

// ownerQueries maps each kind to the query that resolves its board. The ids
// are validated as uuids first, so a malformed id is simply "not found".
var ownerQueries = map[OwnerKind]string{
	OwnerCard:     `SELECT board_id FROM cards WHERE id = $1`,
	OwnerColumn:   `SELECT board_id FROM board_columns WHERE id = $1`,
	OwnerReview:   `SELECT board_id FROM admission_reviews WHERE id = $1`,
	OwnerStanding: `SELECT board_id FROM standing_agents WHERE id = $1`,
	OwnerSession:  `SELECT board_id FROM runner_sessions WHERE id = $1`,
}

// OwnerBoard resolves the board an object belongs to. ok is false for an
// unknown, malformed or unresolvable id (all the same to the caller).
func (a *API) OwnerBoard(ctx context.Context, kind OwnerKind, id string) (board string, ok bool) {
	q, known := ownerQueries[kind]
	if !known || !uuidRe.MatchString(id) {
		return "", false
	}
	if err := a.Pool.QueryRow(ctx, q, id).Scan(&board); err != nil {
		return "", false
	}
	return board, true
}

func (a *API) ownedBy(ctx context.Context, kind OwnerKind, id, board string) bool {
	owner, ok := a.OwnerBoard(ctx, kind, id)
	return ok && owner == board
}

// InScope reports whether p may address the object: always true for a
// principal that is not project-scoped, otherwise only when the object
// belongs to the token's board. Used by the MCP server and by handlers that
// follow an id out of a request body.
func (a *API) InScope(ctx context.Context, p auth.Principal, kind OwnerKind, id string) bool {
	board, scoped := p.ProjectScoped()
	if !scoped {
		return true
	}
	return a.ownedBy(ctx, kind, id, board)
}
