package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engineaccess"
)

// EngineCredentialAccess contains short-lived inference authority only. Sources
// never return a refresh token; callers must not persist or emit these values.
type EngineCredentialAccess = engineaccess.Access
type EngineCredentialSource func(context.Context, bool, string) (EngineCredentialAccess, error)

var errEngineCredential = errors.New("the managed engine connection is unavailable; reconnect it in Settings")

// ManagedEngineCredentialSource is used only by Cluster pods explicitly enrolled
// at launch. The token is the pod's per-launch runtime token: it authenticates
// this launch only, is never the operator's daemon master, and is stripped from
// every agent process's environment (the server refuses the messaging token).
// The selected engine binds each response to its registered access-only codec.
func ManagedEngineCredentialSource(base, token, engine string) EngineCredentialSource {
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return func(parent context.Context, force bool, previous string) (EngineCredentialAccess, error) {
		var access EngineCredentialAccess
		if base == "" || token == "" || !engineaccess.SupportsEngine(engine) {
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
		if err != nil || len(raw) > 32<<10 || resp.StatusCode != http.StatusOK {
			return EngineCredentialAccess{}, errEngineCredential
		}
		access, err = engineaccess.DecodeForEngine(engine, raw, time.Now())
		if err != nil {
			return EngineCredentialAccess{}, errEngineCredential
		}
		return access, nil
	}
}
