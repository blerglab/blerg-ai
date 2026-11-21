package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/livedata"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

// The only credential release path for a session-less poll. Core first asks
// the dedicated Runner to commit a single-use receipt for this exact attempt.
func liveDataProvider(deps api.Deps) func(context.Context, ld.AttemptReceipt) (ld.ProviderCredential, error) {
	return func(parent context.Context, receipt ld.AttemptReceipt) (ld.ProviderCredential, error) {
		if !receipt.Scope.Valid() || (receipt.Stage != "list" && receipt.Stage != "call") {
			return ld.ProviderCredential{}, ld.ErrDenied
		}
		if deps.MCPConnections == nil || deps.LiveDataRunnerClient == nil || deps.LiveDataRunnerURL == "" || deps.InternalKey == "" {
			return ld.ProviderCredential{}, livedata.ErrUnavailable
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		raw, err := json.Marshal(receipt)
		if err != nil {
			return ld.ProviderCredential{}, ld.ErrDenied
		}
		request, err := http.NewRequestWithContext(ctx, "POST", deps.LiveDataRunnerURL+"/internal/live-data/attempt", bytes.NewReader(raw)) // #nosec G704 -- fixed callback on configured dedicated Runner; never a request-selected URL.
		if err != nil {
			return ld.ProviderCredential{}, livedata.ErrUnavailable
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Internal-Key", deps.InternalKey)
		client := *deps.LiveDataRunnerClient
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := client.Do(request) // #nosec G704 -- dedicated Runner mTLS client with redirects disabled.
		if err != nil {
			return ld.ProviderCredential{}, livedata.ErrUnavailable
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode >= 500 || response.StatusCode == 429 {
			return ld.ProviderCredential{}, livedata.ErrUnavailable
		}
		if response.StatusCode != 200 {
			return ld.ProviderCredential{}, ld.ErrDenied
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 8193))
		if err != nil || len(body) > 8192 {
			return ld.ProviderCredential{}, ld.ErrDenied
		}
		var confirmed ld.AttemptReceipt
		if json.Unmarshal(body, &confirmed) != nil || confirmed != receipt {
			return ld.ProviderCredential{}, ld.ErrDenied
		}
		credential, err := deps.MCPConnections.FetchSecret(ctx, receipt.Scope.Owner, receipt.Scope.Connection, mcpconn.Proof{LiveAttemptID: receipt.Attempt})
		if err != nil {
			if errors.Is(err, mcpconn.ErrNotFound) {
				return ld.ProviderCredential{}, ld.ErrDenied
			}
			return ld.ProviderCredential{}, livedata.ErrUnavailable
		}
		out := ld.ProviderCredential{URL: credential.URL, HeaderName: credential.HeaderName, Value: credential.Value}
		if credential.ExpiresAt != nil {
			out.ExpiresAt = *credential.ExpiresAt
		}
		return out, nil
	}
}
