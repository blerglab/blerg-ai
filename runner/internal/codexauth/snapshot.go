// Package codexauth prepares access-only snapshots for isolated Codex consumers.
package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrInvalid = errors.New("the Codex credential is missing or invalid")
var ErrDedicatedLogin = errors.New("cluster Codex needs a dedicated login to renew its access. For a person-owned launch, connect Codex under Settings → Managed connections and start a new cluster session. For an operator launch, configure an API key or use a person-owned launch")

// Snapshot never grants refresh authority. A desktop, a probe and concurrent
// pods cannot safely consume copies of the same rotating refresh token. Durable
// cluster access uses Core's dedicated managed login instead.
func Snapshot(raw []byte, now time.Time) ([]byte, error) {
	var auth struct {
		AuthMode    string    `json:"auth_mode"`
		LastRefresh time.Time `json:"last_refresh"`
		APIKey      string    `json:"OPENAI_API_KEY"`
		Tokens      *struct {
			AccessToken string `json:"access_token"`
			IDToken     string `json:"id_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil {
		return nil, ErrInvalid
	}
	if auth.APIKey != "" {
		return json.Marshal(map[string]string{"auth_mode": "apikey", "OPENAI_API_KEY": auth.APIKey})
	}
	if auth.Tokens == nil || auth.Tokens.AccessToken == "" {
		return nil, ErrInvalid
	}
	parts := strings.Split(auth.Tokens.AccessToken, ".")
	if len(parts) == 3 {
		claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, ErrInvalid
		}
		var claims struct {
			Exp int64 `json:"exp"`
		}
		if json.Unmarshal(claimsRaw, &claims) != nil {
			return nil, ErrInvalid
		}
		if claims.Exp > 0 && !time.Unix(claims.Exp, 0).After(now.Add(30*time.Second)) {
			return nil, ErrDedicatedLogin
		}
	}
	// LastRefresh is consumer-local metadata. Without it native Codex attempts a
	// proactive refresh even for a valid access token. JWT expiry was checked
	// above; this consumer must use the access token alone.
	auth.LastRefresh = now.UTC()
	if auth.AuthMode == "" {
		auth.AuthMode = "chatgpt"
	}
	return json.Marshal(auth) //nolint:gosec // G117: access-only credential intentionally marshaled only as a private access-only credential.
}

// NativeFile supplies the empty schema slot required by native Codex. It is
// written only to the consumer's private local file, never to a pod-bound Secret
// or environment. Snapshot first removes all original refresh authority.
func NativeFile(raw []byte, now time.Time) ([]byte, error) {
	snapshot, err := Snapshot(raw, now)
	if err != nil {
		return nil, err
	}
	var auth map[string]any
	if json.Unmarshal(snapshot, &auth) != nil {
		return nil, ErrInvalid
	}
	if tokens, ok := auth["tokens"].(map[string]any); ok {
		tokens["refresh_token"] = ""
	}
	return json.Marshal(auth)
}
