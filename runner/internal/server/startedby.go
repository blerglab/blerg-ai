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
// tool-started sessions apart from the person's own.

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

// recordStartedBy writes the start's attribution for a session the v1 contract created.
func (a *API) recordStartedBy(ctx context.Context, sessionID string, principal runnerPrincipal, cronID string) {
	if a.dbPool == nil {
		return
	}
	kind, name := principal.Kind, ""
	if kind == "human" {
		kind = "" // the person's own start: nothing to record
	}
	if cronID != "" {
		kind = "cron"
	} else if principal.Kind == agentPrincipalKind {
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
	}
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
