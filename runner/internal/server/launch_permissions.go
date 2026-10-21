package server

// Pre-authorization at launch. The launch sheet may send "permissions" with a start: the same
// session permissions the session panel sets (start sandboxed or unsandboxed children; file cards
// on a board; run an MCP write tool of a selected connection without asking). They are applied
// by the panel's own code paths, as the person who launched, right after the start is accepted
// and before the answer is sent, so they are in place when the agent begins. One that cannot be
// applied (the runtime cannot hold it, the board is not available) does not stop the session: it
// is named in permission_problems and the session asks as usual.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	maxLaunchBoards = 20
	maxLaunchTools  = 50
)

// launchPermissions is the launch sheet's "permissions" field.
type launchPermissions struct {
	Children []string `json:"children,omitempty"` // "docker" (sandboxed), "daemon" (unsandboxed), "cluster"
	Boards   []string `json:"boards,omitempty"`   // file without asking (0.4.3); board_levels says more
	// BoardLevels sets a board's access level (0.4.4): "read", "file" or "full".
	BoardLevels []launchBoardLevel `json:"board_levels,omitempty"`
	MCPTools    []struct {
		ConnectionID string `json:"connection_id"`
		Tool         string `json:"tool"`
	} `json:"mcp_tools,omitempty"`
}

type launchBoardLevel struct {
	BoardID string `json:"board_id"`
	Level   string `json:"level"`
}

func (p *launchPermissions) empty() bool {
	return p == nil || len(p.Children) == 0 && len(p.Boards) == 0 && len(p.BoardLevels) == 0 && len(p.MCPTools) == 0
}

// launchPermissionsProblem checks the field's shape; the policy is checked when it is applied.
func launchPermissionsProblem(p *launchPermissions, kind string) string {
	if p.empty() {
		return ""
	}
	if kind != "agent" {
		return "permissions apply only to agent sessions"
	}
	if len(p.Boards) > maxLaunchBoards || len(p.BoardLevels) > maxLaunchBoards || len(p.MCPTools) > maxLaunchTools || len(p.Children) > 3 {
		return "too many permissions"
	}
	for _, c := range p.Children {
		if c != runnerRuntimeDocker && c != runnerRuntimeDaemon && c != runnerRuntimeCluster {
			return `permissions.children takes "docker", "daemon" and "cluster"`
		}
	}
	for _, b := range p.Boards {
		if b == "" || !filingText(b, 200, false) {
			return "permissions.boards takes board ids"
		}
	}
	for _, b := range p.BoardLevels {
		if b.BoardID == "" || !filingText(b.BoardID, 200, false) || db.BoardLevelRank(b.Level) < 1 {
			return `permissions.board_levels takes board_id and a level of "read", "file" or "full"`
		}
	}
	for _, t := range p.MCPTools {
		if t.ConnectionID == "" || t.Tool == "" || len(t.Tool) > 256 {
			return "permissions.mcp_tools takes connection_id and tool"
		}
	}
	return ""
}

// launchCapture holds the start's answer until the permissions are applied.
type launchCapture struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (c *launchCapture) Header() http.Header { return c.header }
func (c *launchCapture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}
func (c *launchCapture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(b)
}

// HandleLaunchSession is POST /api/sessions, the launch sheet's start: HandlePostSessions (the
// spawn site), plus the permissions the person chose to grant the new session.
func (a *API) HandleLaunchSession(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "the request body is too large")
		return
	}
	var head struct {
		Kind        string             `json:"kind"`
		BoardID     string             `json:"board_id"`
		Permissions *launchPermissions `json:"permissions"`
	}
	_ = json.Unmarshal(raw, &head) // a malformed body is the start's to refuse
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if head.Permissions.empty() && head.BoardID == "" {
		a.HandlePostSessions(w, r)
		return
	}
	if head.Permissions.empty() {
		a.launchWithBoard(w, r, head.BoardID)
		return
	}
	if msg := launchPermissionsProblem(head.Permissions, head.Kind); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	who := requesterOf(principal)
	if principal.OnBehalfOf != "" {
		writeError(w, http.StatusForbidden, "only a signed-in person can grant session permissions")
		return
	}
	if apiErr := checkGrantRequester(who); apiErr != nil {
		writeError(w, apiErr.Status, "only a signed-in person can grant session permissions")
		return
	}
	capture := &launchCapture{header: make(http.Header)}
	a.HandlePostSessions(capture, r)
	var out map[string]any
	if capture.status == http.StatusAccepted && json.Unmarshal(capture.body.Bytes(), &out) == nil {
		if sid, _ := out["session_id"].(string); sid != "" {
			a.recordLaunchBoard(context.WithoutCancel(r.Context()), who, sid, head.BoardID)
			problems := a.applyLaunchPermissions(context.WithoutCancel(r.Context()), who, sid, head.Permissions)
			if len(problems) > 0 {
				out["permission_problems"] = problems
			}
			for k, v := range capture.header {
				w.Header()[k] = v
			}
			writeJSON(w, capture.status, out)
			return
		}
	}
	for k, v := range capture.header {
		w.Header()[k] = v
	}
	if capture.status == 0 {
		capture.status = http.StatusOK
	}
	w.WriteHeader(capture.status)
	_, _ = w.Write(capture.body.Bytes())
}

// applyLaunchPermissions grants what the launch sheet asked for, each through the panel's own path,
// and returns a sentence for each one that could not be granted.
func (a *API) applyLaunchPermissions(ctx context.Context, who grantRequester, sid string, p *launchPermissions) []string {
	if a.dbPool == nil {
		return []string{"Session permissions need the runner database; none were granted."}
	}
	var problems []string
	enabled := true
	for _, runtime := range p.Children {
		_, problem, err := a.applyChildApproval(ctx, who.Account, sid, true, childApprovalWrite{Runtime: runtime, Enabled: &enabled}, false, "launch_sheet")
		label := "sandboxed"
		switch runtime {
		case runnerRuntimeDaemon:
			label = "unsandboxed"
		case runnerRuntimeCluster:
			label = "cluster"
		}
		switch {
		case err != nil:
			log.Printf("launch permissions: child approval %s: %v", sid, err)
			problems = append(problems, "Starting "+label+" children was not allowed: try again from the session panel.")
		case problem != nil:
			problems = append(problems, "Starting "+label+" children was not allowed: "+problem.Message)
		}
	}
	row, err := a.readOwnedRow(ctx, who.Account, sid)
	if err != nil || row == nil {
		if len(p.Boards)+len(p.MCPTools) > 0 {
			problems = append(problems, "Proposal permissions were not granted: the session could not be read.")
		}
		return problems
	}
	for _, b := range p.BoardLevels {
		if _, apiErr := a.setBoardLevel(ctx, who, row, b.BoardID, b.Level, "launch", ""); apiErr != nil {
			problems = append(problems, "Board access ("+b.Level+") was not given: "+strings.TrimSuffix(apiErr.Message, "."))
		}
	}
	for _, board := range p.Boards {
		if _, apiErr := a.createProposalGrant(ctx, who, row, proposalGrantRequest{Kind: "board_file", BoardID: board}, "launch", ""); apiErr != nil {
			problems = append(problems, "Filing cards without asking was not allowed: "+strings.TrimSuffix(apiErr.Message, "."))
		}
	}
	for _, t := range p.MCPTools {
		if _, apiErr := a.createProposalGrant(ctx, who, row, proposalGrantRequest{Kind: "mcp_tool", ConnectionID: t.ConnectionID, Tool: t.Tool}, "launch", ""); apiErr != nil {
			problems = append(problems, "Running "+t.Tool+" without asking was not allowed: "+strings.TrimSuffix(apiErr.Message, "."))
		}
	}
	return problems
}

// readOwnedRow reads the account's session, or nil when it is not theirs.
func (a *API) readOwnedRow(ctx context.Context, account, sid string) (*db.SessionRow, error) {
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil || row == nil || !ownsSession(account, row) {
		return nil, err
	}
	return row, nil
}

// launchWithBoard is a start that chose a board and no permissions: the start as it is, then the
// board's level recorded as full (recordLaunchBoard), when a signed-in person started it.
func (a *API) launchWithBoard(w http.ResponseWriter, r *http.Request, boardID string) {
	capture := &launchCapture{header: make(http.Header)}
	a.HandlePostSessions(capture, r)
	var out map[string]any
	if capture.status == http.StatusAccepted && json.Unmarshal(capture.body.Bytes(), &out) == nil {
		if sid, _ := out["session_id"].(string); sid != "" {
			// The start authenticated the caller already; this only learns who it was, and writes nothing.
			if p, ok := a.authBrowser(&launchCapture{header: make(http.Header)}, r); ok && p.OnBehalfOf == "" {
				a.recordLaunchBoard(context.WithoutCancel(r.Context()), requesterOf(p), sid, boardID)
			}
		}
	}
	for k, v := range capture.header {
		w.Header()[k] = v
	}
	if capture.status == 0 {
		capture.status = http.StatusOK
	}
	w.WriteHeader(capture.status)
	_, _ = w.Write(capture.body.Bytes())
}

// recordLaunchBoard records the board a person chose at launch as level full in the session's
// Permissions, so it shows there, can be lowered or revoked, and files without asking like any full
// board. Best effort: without the row the launch board is full anyway (sessionBoardLevel), and a
// session that cannot hold permissions (restricted, for one) keeps exactly what its launch gave it.
func (a *API) recordLaunchBoard(ctx context.Context, who grantRequester, sid, boardID string) {
	if boardID == "" || a.dbPool == nil || checkGrantRequester(who) != nil || who.Sid == "" {
		return
	}
	row, err := a.readOwnedRow(ctx, who.Account, sid)
	if err != nil || row == nil || proposalGrantProblem(row) != "" {
		return
	}
	if lv, err := a.sessionBoardLevel(ctx, sid, boardID); err != nil || lv.Launch == nil {
		return // the start did not give it the board connection
	}
	if _, apiErr := a.setBoardLevel(ctx, who, row, boardID, db.BoardLevelFull, "launch", ""); apiErr != nil {
		log.Printf("board access: launch board of %s not recorded: %s", sid, apiErr.Message)
	}
}
