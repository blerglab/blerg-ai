package server

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

type channelStart struct {
	Context            string   `json:"context,omitempty"`
	Title              string   `json:"title"`
	Engine             string   `json:"engine"`
	Repos              []string `json:"repos,omitempty"`
	InheritPermissions *bool    `json:"inherit_permissions,omitempty"`
	Private            bool     `json:"private,omitempty"`
}

// A person's channel uses the worker's fenced launch and worktree machinery. Its
// origin remains personal; no session credential can enter this route.
func (a *API) HandlePostChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	who, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, 503, noDatabaseMsg)
		return
	}
	parent, ok := a.ownedSession(ctx, w, who.Account, r.PathValue("id"))
	if !ok {
		return
	}
	if parent.ParentSessionID != nil || (parent.StartedByKind != "" && parent.StartedByKind != "human") || parent.CronID != nil || parent.Kind != "agent" || parent.ArchivedAt != nil || db.TerminallyEnded(parent) {
		writeError(w, 409, "start a channel from an active workspace")
		return
	}
	var input channelStart
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil || dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, 400, "invalid channel request")
		return
	}
	idemKey, problem := requestIdempotencyKey(r, false)
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	input.Title = cleanSessionTitle(input.Title)
	if input.Title == "" {
		writeError(w, 422, "name the channel")
		return
	}
	dc := a.hub.ownedHostDaemon(ctx, who.Account, parent.DaemonID)
	if !onClusterParent(parent) && dc == nil {
		writeError(w, 409, "workspace runtime unavailable")
		return
	}
	host := a.parentHost(parent, dc)
	ws, err := db.GetSessionWorkspace(ctx, a.dbPool, parent.ID)
	if err != nil {
		writeDatabaseFailure(w, "read workspace repositories", "channel workspace", err)
		return
	}
	if derefOrEmpty(parent.Runtime) == "daemon" && (ws == nil || ws.Manifest == nil) {
		writeError(w, 409, "start a new workspace on a Git repository before adding host channels")
		return
	}
	fields := map[string]any{"title": input.Title, "engine": input.Engine, "runtime": derefOrEmpty(parent.Runtime), "prompt": "Start a focused chat."}
	if ws != nil && ws.Manifest != nil {
		host.workspace = ws.Manifest
		if len(input.Repos) == 0 {
			fields["all_repos"] = true
		} else {
			fields["repos"] = input.Repos
		}
	} else {
		repo := parent.Repo
		if len(input.Repos) > 1 || len(input.Repos) == 1 && input.Repos[0] != repo {
			writeError(w, 422, "choose workspace repositories")
			return
		}
		if strings.HasPrefix(repo, ".scratch-") || repo == "" {
			fields["no_repo"] = true
		} else {
			fields["repo"] = repo
		}
	}
	raw, _ := json.Marshal(fields)
	cs, host, problem := parseChildStart(raw, host)
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	p := a.delegateFor(ctx, parent, cs.Runtime, who.Sid)
	if problem = a.validateChildStart(ctx, cs, host, p); problem != nil {
		writeAPIError(w, problem)
		return
	}
	cs.Prompt, cs.personChannel, cs.channelPrivate, cs.channelSid = input.Context, true, input.Private, who.Sid
	// A channel belongs to the person and survives Main stopping.
	cs.KeepRunning = true
	inherit := input.InheritPermissions == nil || *input.InheritPermissions
	cs.channelRestricted = parent.RestrictTools
	cs.channelInput = &input
	permissions := &launchPermissions{}
	if inherit {
		boards, err := a.boardLevelsFor(ctx, parent)
		if err != nil {
			writeDatabaseFailure(w, "read workspace permissions", "channel boards", err)
			return
		}
		for _, board := range boards {
			if db.BoardLevelRank(board.Level) > 0 {
				permissions.BoardLevels = append(permissions.BoardLevels, launchBoardLevel{BoardID: board.BoardID, Level: board.Level})
			}
		}
		grants, err := db.ListLiveProposalGrants(ctx, a.dbPool, parent.ID)
		if err != nil {
			writeDatabaseFailure(w, "read workspace permissions", "channel grants", err)
			return
		}
		for _, grant := range grants {
			if grant.Kind == db.ProposalGrantMCP {
				permissions.MCPTools = append(permissions.MCPTools, struct {
					ConnectionID string `json:"connection_id"`
					Tool         string `json:"tool"`
				}{grant.ConnectionID, grant.Tool})
			}
		}
	}
	if inherit {
		grants, err := db.ListMCPGrantsForSession(ctx, a.dbPool, parent.ID)
		if err != nil {
			writeDatabaseFailure(w, "read workspace connections", "channel MCP", err)
			return
		}
		selections := []MCPSelection{}
		for _, grant := range grants {
			if grant.Builtin != "" {
				continue
			}
			tools := map[string]MCPToolSelection{}
			for name, tool := range grant.Tools {
				tools[name] = MCPToolSelection{Mode: tool.Mode, Hash: tool.Hash}
			}
			if len(tools) > 0 {
				selections = append(selections, MCPSelection{Connection: grant.ConnectionID, Tools: tools})
			}
		}
		cs.channelGrant, problem = a.resolveGrant(ctx, who, selections, grantTarget{Runtime: cs.Runtime, Engine: cs.Engine, Kind: "agent", Daemon: dc})
		if problem != nil {
			writeAPIError(w, problem)
			return
		}
	}
	var resp StartResponse
	err = a.withDelegationLock(ctx, who.Account, func() error {
		replay := false
		if idemKey != "" {
			existing, err := db.GetIdempotencyKey(ctx, a.dbPool, "channel:"+parent.ID, idemKey)
			if err != nil {
				return err
			}
			replay = existing != nil && time.Since(existing.CreatedAt) < idempotencyTTL
		}
		if !replay {
			problem = a.checkChannelLimits(ctx, parent)
		}
		if problem != nil {
			return problem
		}
		resp, problem = a.startChildScoped(ctx, parent, cs, "person", idemKey, "channel:"+parent.ID, who.Sid)
		if problem != nil {
			return problem
		}
		return nil
	})
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	if err != nil {
		writeDatabaseFailure(w, "start channel", "channel admission", err)
		return
	}
	problems := []string{}
	ctx = context.WithoutCancel(ctx)
	if !resp.Replayed {
		problems = append(problems, a.applyLaunchPermissions(ctx, who, resp.SessionID, permissions)...)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"session_id": resp.SessionID, "permission_problems": problems})
}

// Channels share the account's launch budget, never Main's worker budget.
// The caller holds the account delegation lock across admission and launch.
func (a *API) checkChannelLimits(ctx context.Context, parent *db.SessionRow) *APIError {
	account := derefOrEmpty(parent.SpawningAccountID)
	n, err := db.CountAccountRunningChildren(ctx, a.dbPool, account)
	if err != nil {
		return apiErrorf(503, "could not check the channel limit")
	}
	limited := func(retry int, message string) *APIError {
		e := apiErrorf(429, "%s", message)
		e.RetryAfter = retry
		return e
	}
	if n >= accountRunningLimit(a.effectiveChildLimits(ctx)) {
		return limited(60, "Your account has reached its active worker and channel limit. Stop one first.")
	}
	n, oldest, err := db.AccountStartsSince(ctx, a.dbPool, account, time.Now().Add(-time.Hour))
	if err != nil {
		return apiErrorf(503, "could not check the channel start limit")
	}
	if n >= maxStartsPerAccountPerHour {
		retry := 60
		if oldest != nil {
			retry = max(1, int(math.Ceil(time.Until(oldest.Add(time.Hour)).Seconds())))
		}
		return limited(retry, "Your account has reached its worker and channel start limit for this hour. Try again later.")
	}
	return nil
}
