package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// EngineCredentialAccess contains short-lived inference authority only. Sources
// never return a refresh token; callers must not persist or emit these values.
type EngineCredentialAccess struct {
	Mode        string    `json:"mode"`
	AccessToken string    `json:"access_token"`
	AccountID   string    `json:"chatgpt_account_id"`
	PlanType    string    `json:"plan_type,omitempty"`
	Generation  string    `json:"generation"`
	ExpiresAt   time.Time `json:"expires_at"`
}
type EngineCredentialSource func(context.Context, bool, string) (EngineCredentialAccess, error)

var errEngineCredential = errors.New("the managed engine connection is unavailable; reconnect it in Settings")

// ManagedEngineCredentialSource is used only by Cluster pods explicitly enrolled
// at launch. The token is the pod's per-launch runtime token: it authenticates
// this launch only, is never the operator's daemon master, and is stripped from
// every agent process's environment (the server refuses the messaging token).
func ManagedEngineCredentialSource(base, token string) EngineCredentialSource {
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return func(parent context.Context, force bool, previous string) (EngineCredentialAccess, error) {
		var access EngineCredentialAccess
		if base == "" || token == "" {
			return access, errEngineCredential
		}
		ctx, cancel := context.WithTimeout(parent, 8*time.Second)
		defer cancel()
		raw, err := json.Marshal(map[string]any{"force": force, "previous_generation": previous})
		if err != nil {
			return access, errEngineCredential
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/session/credentials/access", bytes.NewReader(raw))
		if err != nil {
			return access, errEngineCredential
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return access, errEngineCredential
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err = io.ReadAll(io.LimitReader(resp.Body, 32<<10+1))
		if err != nil || len(raw) > 32<<10 || resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &access) != nil || access.Mode != "chatgptAuthTokens" || access.AccessToken == "" || len(access.AccessToken) > 24<<10 || access.AccountID == "" || len(access.AccountID) > 256 || access.Generation == "" || !access.ExpiresAt.After(time.Now()) {
			return EngineCredentialAccess{}, errEngineCredential
		}
		return access, nil
	}
}
