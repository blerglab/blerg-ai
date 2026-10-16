package credentials

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Public native-client identity and routes verified against pinned Codex 0.162.1
// with an isolated loopback issuer; this is not a client secret.
const codexPublicClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
const codexIssuer = "https://auth.openai.com"

var deviceUserCode = regexp.MustCompile(`^[A-Z0-9-]{4,32}$`)
var codexAccountID = regexp.MustCompile(`^[A-Za-z0-9._:@/-]{1,256}$`)
var codexPlanType = regexp.MustCompile(`^[a-z0-9_-]{0,64}$`)

type codexAdapter struct {
	issuer string
	client *http.Client
}

func newCodexAdapter() *codexAdapter {
	return &codexAdapter{issuer: codexIssuer, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("OAuth redirects refused") }}}
}
func (*codexAdapter) AccessMode() string         { return "chatgptAuthTokens" }
func (*codexAdapter) Engine() string             { return "codex" }
func (*codexAdapter) VerificationOrigin() string { return codexIssuer }
func (*codexAdapter) Kind() string               { return "codex_oauth" }

// request never returns provider text, URLs, headers or token contents in errors.
func (a *codexAdapter) request(ctx context.Context, path, contentType string, body []byte, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.issuer+path, bytes.NewReader(body))
	if err != nil {
		return 0, ErrReconnectRequired
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	response, err := a.client.Do(req)
	if err != nil {
		return 0, ErrReconnectRequired
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, ErrReconnectRequired
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return response.StatusCode, ErrReconnectRequired
	}
	if json.Unmarshal(raw, out) != nil {
		return response.StatusCode, ErrReconnectRequired
	}
	return response.StatusCode, nil
}
func (a *codexAdapter) StartDevice(ctx context.Context) (DeviceGrant, error) {
	var out struct {
		DeviceID string          `json:"device_auth_id"`
		UserCode string          `json:"user_code"`
		Interval json.RawMessage `json:"interval"`
	}
	raw, _ := json.Marshal(map[string]string{"client_id": codexPublicClientID})
	if _, err := a.request(ctx, "/api/accounts/deviceauth/usercode", "application/json", raw, &out); err != nil {
		return DeviceGrant{}, err
	}
	intervalText := strings.Trim(string(out.Interval), `"`)
	interval, err := strconv.Atoi(intervalText)
	if err != nil || interval < 1 || interval > 60 || out.DeviceID == "" || len(out.DeviceID) > 4096 || !deviceUserCode.MatchString(out.UserCode) {
		return DeviceGrant{}, ErrReconnectRequired
	}
	return DeviceGrant{DeviceID: out.DeviceID, UserCode: out.UserCode, VerificationURL: codexIssuer + "/codex/device", IntervalSeconds: interval, ExpiresAt: time.Now().Add(15 * time.Minute)}, nil
}
func (a *codexAdapter) PollDevice(ctx context.Context, grant DeviceGrant) (ManagedToken, error) {
	var out struct {
		Code     string `json:"authorization_code"`
		Verifier string `json:"code_verifier"`
	}
	raw, _ := json.Marshal(map[string]string{"device_auth_id": grant.DeviceID, "user_code": grant.UserCode})
	status, err := a.request(ctx, "/api/accounts/deviceauth/token", "application/json", raw, &out)
	// Native device flow uses 403/404 to mean authorization has not happened.
	if status == http.StatusForbidden || status == http.StatusNotFound {
		return ManagedToken{}, ErrLoginPending
	}
	if err != nil {
		return ManagedToken{}, err
	}
	if out.Code == "" || out.Verifier == "" || len(out.Code) > 8192 || len(out.Verifier) > 8192 {
		return ManagedToken{}, ErrReconnectRequired
	}
	return a.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {codexPublicClientID}, "code": {out.Code}, "redirect_uri": {a.issuer + "/deviceauth/callback"}, "code_verifier": {out.Verifier}}, ManagedToken{})
}
func (a *codexAdapter) Refresh(ctx context.Context, old ManagedToken) (ManagedToken, error) {
	token, err := a.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {codexPublicClientID}, "refresh_token": {old.RefreshToken}}, old)
	if err != nil || token.AccountID != old.AccountID {
		return ManagedToken{}, ErrReconnectRequired
	}
	return token, nil
}
func (a *codexAdapter) exchange(ctx context.Context, form url.Values, old ManagedToken) (ManagedToken, error) {
	var out struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		ID      string `json:"id_token"`
		Expires int64  `json:"expires_in"`
	}
	contentType, body := "application/x-www-form-urlencoded", []byte(form.Encode())
	// The pinned native client uses JSON for refresh and form encoding for the
	// initial authorization-code/PKCE exchange. Preserve that provider contract.
	if form.Get("grant_type") == "refresh_token" {
		contentType = "application/json"
		body, _ = json.Marshal(map[string]string{"client_id": form.Get("client_id"), "grant_type": "refresh_token", "refresh_token": form.Get("refresh_token")})
	}
	if _, err := a.request(ctx, "/oauth/token", contentType, body, &out); err != nil {
		return ManagedToken{}, err
	}
	if out.Refresh == "" {
		out.Refresh = old.RefreshToken
	}
	if out.ID == "" {
		out.ID = out.Access
	}
	claims, ok := codexClaims(out.ID)
	if !ok {
		return ManagedToken{}, ErrReconnectRequired
	}
	if claims.Auth.AccountID == "" {
		claims.Auth.AccountID = old.AccountID
		claims.Auth.PlanType = old.PlanType
	}
	if out.Expires < 0 || out.Expires > 31*24*3600 {
		return ManagedToken{}, ErrReconnectRequired
	}
	expiry := time.Now().Add(time.Duration(out.Expires) * time.Second)
	accessClaims, valid := codexClaims(out.Access)
	if !valid || accessClaims.Exp <= 0 {
		return ManagedToken{}, ErrReconnectRequired
	}
	if accessClaims.Exp > 0 {
		jwtExpiry := time.Unix(accessClaims.Exp, 0)
		if out.Expires <= 0 || jwtExpiry.Before(expiry) {
			expiry = jwtExpiry
		}
	}
	if out.Access == "" || out.Refresh == "" || len(out.Access) > 32<<10 || len(out.Refresh) > 16<<10 || !codexAccountID.MatchString(claims.Auth.AccountID) || !codexPlanType.MatchString(claims.Auth.PlanType) || !expiry.After(time.Now().Add(30*time.Second)) || expiry.After(time.Now().Add(31*24*time.Hour)) {
		return ManagedToken{}, ErrReconnectRequired
	}
	return ManagedToken{AccessToken: out.Access, RefreshToken: out.Refresh, AccountID: claims.Auth.AccountID, PlanType: claims.Auth.PlanType, ExpiresAt: expiry}, nil
}

type codexTokenClaims struct {
	Exp  int64 `json:"exp"`
	Auth struct {
		AccountID string `json:"chatgpt_account_id"`
		PlanType  string `json:"chatgpt_plan_type"`
	} `json:"https://api.openai.com/auth"`
}

func codexClaims(token string) (codexTokenClaims, bool) {
	var claims codexTokenClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(parts[1]) > 48<<10 {
		return claims, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(raw, &claims) != nil {
		return claims, false
	}
	return claims, true
}
