package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Who started a session (migration 037). A session the person launches in the app records
// nothing; a start through the v1 contract records the principal's kind and, for an agent
// token, the owner's label for it (asked of core once, best effort), so the app can keep
// tool-started sessions apart from the person's own. A child session (delegation.go) records kind
// "session" and its parent's title.

// startedByOf is the browser's view of a row: nil for the person's own session.
func startedByOf(row *db.SessionRow) *protocol.StartedBy {
	if row == nil {
		return nil
	}
	switch {
	case row.CronID != nil && *row.CronID != "":
		return &protocol.StartedBy{Kind: "cron"}
	case row.StartedByKind != "" && row.StartedByKind != "human":
		return &protocol.StartedBy{Kind: row.StartedByKind, Name: row.StartedByName}
	}
	return nil
}

// Who wrote a session's first prompt, as the source its user_message is recorded with (spawn
// frame initial_prompt_source; BLERG_RUNNER_INITIAL_PROMPT_SOURCE in a cluster pod). The person's
// own start keeps "" — the daemon's "chat", as before, and what a daemon that predates the field
// records anyway. Every other origin is named so the chat never draws its prompt as the person's.
const (
	promptSourceBoard   = "blerg-board" // a board's automation: a card run, a board session, a reviewer
	promptSourceSession = "session"     // a parent session (delegation): the source its later messages carry too
	promptSourceCron    = "cron"        // a cron run
)

// initialPromptSource is the source of a v1-contract start's prompt. It is derived only from what
// the caller cannot choose freely (the verified principal, the in-process cron id) or from what
// can only make the prompt less the person's (a board's env), never from a source in the body: no
// start can claim to be the person when its credential is not.
func initialPromptSource(principal runnerPrincipal, req runnerStartRequest) string {
	switch {
	case req.CronID != "":
		return promptSourceCron
	case principal.Kind == sessionDelegateKind || req.ParentSessionID != "":
		return promptSourceSession
	case req.Env["BLERG_BOARD_BOARD"] != "":
		return promptSourceBoard
	}
	return ""
}

// recordStartedBy writes the start's attribution for a session the v1 contract created. A start
// that is not the person's own must be marked (trustDirect reads the mark to keep agent, service
// and runner-key sessions away from session approval), so an error there fails the start; the
// person's own start writes nothing and never fails.
func (a *API) recordStartedBy(ctx context.Context, sessionID string, principal runnerPrincipal, cronID string) error {
	if a.dbPool == nil {
		return nil
	}
	kind, name := principal.Kind, ""
	if kind == "human" {
		kind = "" // the person's own start: nothing to record
	}
	switch {
	case cronID != "":
		kind = "cron"
	case principal.Kind == sessionDelegateKind:
		// A child: started by its parent session, named by the parent's title.
		kind, name = "session", a.parentLabel(ctx, principal.Sub)
	case principal.Kind == agentPrincipalKind:
		lookup := a.fetchTokenName
		if lookup == nil {
			lookup = func(ctx context.Context, accountID, tokenID string) (string, error) {
				return fetchTokenName(ctx, nil, a.coreURL, a.coreInternalKey, accountID, tokenID)
			}
		}
		nctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		n, err := lookup(nctx, principal.spawningAccountID(), principal.tokenID())
		cancel()
		if err != nil {
			log.Printf("session %s: agent token name unavailable: %v", sessionID, err)
		}
		name = n
	}
	if err := db.SetSessionStartedBy(ctx, a.dbPool, sessionID, kind, name); err != nil {
		log.Printf("SetSessionStartedBy %s: %v", sessionID, err)
		return err
	}
	return nil
}

// fetchTokenName asks core for the owner's label of an agent token: POST /internal/tokens/status
// answers it beside the liveness. "" when core has no name for it or is not configured.
func fetchTokenName(ctx context.Context, client *http.Client, coreURL, internalKey, accountID, tokenID string) (string, error) {
	if coreURL == "" || internalKey == "" || accountID == "" || tokenID == "" {
		return "", nil
	}
	client = coreHTTPClient(client)
	raw, err := json.Marshal(map[string]string{"account_id": accountID, "token_id": tokenID})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, coreURL+"/internal/tokens/status", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", internalKey)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request to core failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("core answered %d", resp.StatusCode)
	}
	var out struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return "", fmt.Errorf("decode core response: %w", err)
	}
	return out.Name, nil
}
