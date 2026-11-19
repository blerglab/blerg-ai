package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livemcp"
)

// Ask the configured trusted Runner to perform a NEW upstream listing, using
// Core's own connection URL. No hash/credential cache or request metadata is used.
func (d Deps) liveFreshTool(ctx context.Context, config ld.Revision, proof internalMCPProof) error {
	if d.LiveDataRunnerURL == "" || d.MCPConnections == nil {
		return ld.ErrDenied
	}
	conn, err := d.MCPConnections.Get(ctx, config.Scope.Owner, config.Scope.Connection)
	if err != nil {
		return ld.ErrDenied
	}
	body, _ := json.Marshal(map[string]any{"account_id": proof.AccountID, "session_id": proof.SessionID, "token_id": proof.TokenID, "connection_id": config.Scope.Connection, "url": conn.URL, "tool": config.Scope.Tool})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", d.LiveDataRunnerURL+"/internal/live-data/definition", bytes.NewReader(body)) // #nosec G704 -- fixed endpoint on configured trusted Runner.
	if err != nil {
		return ld.ErrDenied
	}
	req.Header.Set("X-Internal-Key", d.InternalKey)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if d.LiveDataRunnerClient != nil {
		client.Transport = d.LiveDataRunnerClient.Transport
	}
	res, err := client.Do(req) // #nosec G704 -- configured trusted Runner, redirects disabled.
	if err != nil {
		return ld.ErrDenied
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(raw) > 65536 || res.StatusCode != 200 {
		return ld.ErrDenied
	}
	var listing struct {
		Definition json.RawMessage `json:"definition"`
		ReadOnly   bool            `json:"read_only"`
	}
	if json.Unmarshal(raw, &listing) != nil || !listing.ReadOnly {
		return ld.ErrDenied
	}
	canonical, err := livemcp.CanonicalObject(listing.Definition)
	if err != nil || !bytes.Equal(canonical, config.ToolDefinition) {
		return ld.ErrDenied
	}
	return nil
}
