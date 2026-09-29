package keybackend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// k8sServiceAccountTokenPath is where Kubernetes projects a pod's own
// service-account JWT, used as the credential presented to Vault's
// Kubernetes auth method (POST auth/kubernetes/login).
const k8sServiceAccountTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token" //nolint:gosec // G101: the fixed path Kubernetes projects the token at, not a credential

// minRenewalInterval is a floor on the renewal-loop tick interval so a very
// short lease TTL (e.g. in a misconfigured Vault role, or a test) can't spin
// the loop into a tight HTTP-request storm.
const minRenewalInterval = 10 * time.Second

// vaultHTTPTimeout bounds every individual Vault HTTP call (login, renew,
// encrypt, decrypt) so a wedged Vault never hangs a caller indefinitely.
const vaultHTTPTimeout = 10 * time.Second

// VaultTransit is a Backend that encrypts and decrypts via Vault's Transit
// secrets engine (POST transit/encrypt/{key} and transit/decrypt/{key}).
// Vault owns and never exposes the raw key material -- this backend only
// ever sees base64 ciphertext on the wire. Every Vault HTTP error (network,
// auth, or transit-engine) is returned as a hard error from Encrypt/Decrypt;
// there is no code path that falls back to plaintext or to a different
// backend on failure (spec §6, Global Constraints: fail closed).
type VaultTransit struct {
	addr       string
	transitKey string
	httpClient *http.Client

	mu    sync.RWMutex
	token string

	// loginFn and renewFn are the renewal loop's seam: NewVaultTransitK8sAuth
	// wires them to real Vault HTTP calls (k8sLogin / renewSelf below);
	// tests in this package inject fakes to exercise the loop's timing and
	// retry behavior without a real Kubernetes cluster.
	loginFn func(ctx context.Context) (token string, leaseDuration time.Duration, err error)
	renewFn func(ctx context.Context, token string) (leaseDuration time.Duration, err error)

	stop chan struct{} // non-nil only when a renewal loop is running (k8s-auth path)
}

func (v *VaultTransit) ID() string { return "vault-transit" }

func (v *VaultTransit) currentToken() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.token
}

func (v *VaultTransit) setToken(tok string) {
	v.mu.Lock()
	v.token = tok
	v.mu.Unlock()
}

// NewVaultTransitWithStaticToken returns a VaultTransit authenticated with a
// fixed token (e.g. from $VAULT_TOKEN) and NO renewal loop.
//
// This constructor is for LOCAL/DEV USE ONLY, never production: a static
// token is a standing, non-rotating secret with no lifecycle of its own --
// unlike the Kubernetes-auth path (NewVaultTransitK8sAuth), nothing here
// renews it or reacts to it expiring. In production use
// NewVaultTransitK8sAuth instead.
func NewVaultTransitWithStaticToken(_ context.Context, addr, token, transitKeyName string) (*VaultTransit, error) {
	if addr == "" {
		return nil, errors.New("keybackend: vault addr is required")
	}
	if token == "" {
		return nil, errors.New("keybackend: vault token is required")
	}
	if transitKeyName == "" {
		return nil, errors.New("keybackend: vault transit key name is required")
	}
	return &VaultTransit{
		addr:       addr,
		transitKey: transitKeyName,
		httpClient: &http.Client{Timeout: vaultHTTPTimeout},
		token:      token,
	}, nil
}

// NewVaultTransitK8sAuth returns a VaultTransit authenticated via Vault's
// Kubernetes auth method: it logs in with this pod's own projected
// service-account JWT (read from k8sServiceAccountTokenPath) against the
// given Vault role, then starts a background renewal loop -- structurally
// the same ticker-driven "loop(interval, fn)" shape as
// runner/internal/coreauth.Client.loop -- that renews the login token well
// inside its lease TTL (half the TTL, floored at minRenewalInterval). If a
// renewal ever fails, the loop does NOT retry the renew call in place;
// instead it re-logs-in from scratch, since a failed renew can mean the
// lease is already gone and only a fresh login can recover it. This is the
// production path.
func NewVaultTransitK8sAuth(ctx context.Context, addr, role, transitKeyName string) (*VaultTransit, error) {
	if addr == "" {
		return nil, errors.New("keybackend: vault addr is required")
	}
	if role == "" {
		return nil, errors.New("keybackend: vault kubernetes-auth role is required")
	}
	if transitKeyName == "" {
		return nil, errors.New("keybackend: vault transit key name is required")
	}

	v := &VaultTransit{
		addr:       addr,
		transitKey: transitKeyName,
		httpClient: &http.Client{Timeout: vaultHTTPTimeout},
	}
	v.loginFn = func(ctx context.Context) (string, time.Duration, error) {
		return v.k8sLogin(ctx, role)
	}
	v.renewFn = v.renewSelf

	token, lease, err := v.loginFn(ctx)
	if err != nil {
		return nil, fmt.Errorf("keybackend: vault k8s-auth: initial login: %w", err)
	}
	v.setToken(token)
	v.startRenewalLoop(renewalInterval(lease)) //nolint:contextcheck // the renewal loop must outlive the constructor's ctx; Stop() ends it

	return v, nil
}

// renewalInterval picks a renewal-loop tick interval well inside a lease's
// TTL (half of it), floored at minRenewalInterval so a very short or
// zero-valued lease TTL can't cause a tight retry storm.
func renewalInterval(lease time.Duration) time.Duration {
	interval := lease / 2
	if interval < minRenewalInterval {
		interval = minRenewalInterval
	}
	return interval
}

// startRenewalLoop starts the background goroutine that ticks every
// interval and calls renewOrRelogin. Stop() must be called to release it.
func (v *VaultTransit) startRenewalLoop(interval time.Duration) {
	v.stop = make(chan struct{})
	go v.loop(interval, func() { v.renewOrRelogin(context.Background()) })
}

// loop is the same ticker-driven background-refresh shape as
// runner/internal/coreauth.Client.loop, extended with a stop channel so the
// goroutine can be released (coreauth's Client lives for the process
// lifetime and has no analogous need).
func (v *VaultTransit) loop(interval time.Duration, fn func()) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-v.stop:
			return
		case <-t.C:
			fn()
		}
	}
}

// renewOrRelogin is one renewal-loop tick: try to renew the current token
// in place, and if that fails for any reason, re-login from scratch rather
// than leaving a silently-expired (or expiring) token in place. A failed
// re-login is logged and left for the next tick to retry -- it does not
// panic or clear the current token, since the current token may still be
// valid for a little longer.
func (v *VaultTransit) renewOrRelogin(ctx context.Context) {
	_, renewErr := v.renewFn(ctx, v.currentToken())
	if renewErr == nil {
		return
	}
	log.Printf("keybackend: vault: token renew failed (%v); re-logging in from scratch", renewErr)

	token, _, err := v.loginFn(ctx)
	if err != nil {
		log.Printf("keybackend: vault: re-login failed, will retry next tick: %v", err)
		return
	}
	v.setToken(token)
}

// Stop releases the background renewal loop's goroutine, if one is
// running (only the NewVaultTransitK8sAuth path starts one).
func (v *VaultTransit) Stop() {
	if v.stop != nil {
		close(v.stop)
	}
}

// Encrypt seals plaintext via Vault's transit/encrypt/{key} endpoint. On
// any error -- network, auth, or transit-engine -- it returns a hard error;
// it never falls back to returning plaintext or using a different backend.
func (v *VaultTransit) Encrypt(ctx context.Context, plaintext []byte) ([]byte, string, error) {
	reqBody := struct {
		Plaintext string `json:"plaintext"`
	}{Plaintext: base64.StdEncoding.EncodeToString(plaintext)}

	raw, err := v.rawRequest(ctx, "transit/encrypt/"+v.transitKey, v.currentToken(), reqBody)
	if err != nil {
		return nil, "", fmt.Errorf("keybackend: vault encrypt: %w", err)
	}

	var parsed struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, "", fmt.Errorf("keybackend: vault encrypt: parsing response: %w", err)
	}
	if parsed.Data.Ciphertext == "" {
		return nil, "", errors.New("keybackend: vault encrypt: empty ciphertext in response")
	}
	return []byte(parsed.Data.Ciphertext), v.transitKey, nil
}

// Decrypt reverses Encrypt via Vault's transit/decrypt/{key} endpoint.
// keyID must be this backend's transit key name -- Vault's own ciphertext
// format ("vault:v1:...") already carries the key version, so keyID here
// only needs to route to the right transit key. On any error -- a keyID
// mismatch, or a network/auth/transit-engine failure from Vault -- it
// returns a hard error; it never falls back to returning unencrypted data.
func (v *VaultTransit) Decrypt(ctx context.Context, ciphertext []byte, keyID string) ([]byte, error) {
	if keyID != v.transitKey {
		return nil, fmt.Errorf("keybackend: vault decrypt: key_id %q does not match this backend's transit key %q", keyID, v.transitKey)
	}

	reqBody := struct {
		Ciphertext string `json:"ciphertext"`
	}{Ciphertext: string(ciphertext)}

	raw, err := v.rawRequest(ctx, "transit/decrypt/"+v.transitKey, v.currentToken(), reqBody)
	if err != nil {
		return nil, fmt.Errorf("keybackend: vault decrypt: %w", err)
	}

	var parsed struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("keybackend: vault decrypt: parsing response: %w", err)
	}
	plaintext, err := base64.StdEncoding.DecodeString(parsed.Data.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("keybackend: vault decrypt: decoding plaintext: %w", err)
	}
	return plaintext, nil
}

// k8sLogin authenticates to Vault's Kubernetes auth method
// (POST auth/kubernetes/login) using this pod's own projected
// service-account JWT.
func (v *VaultTransit) k8sLogin(ctx context.Context, role string) (string, time.Duration, error) {
	jwt, err := os.ReadFile(k8sServiceAccountTokenPath)
	if err != nil {
		return "", 0, fmt.Errorf("keybackend: vault k8s-auth: reading service account token: %w", err)
	}

	reqBody := struct {
		Role string `json:"role"`
		JWT  string `json:"jwt"`
	}{Role: role, JWT: strings.TrimSpace(string(jwt))}

	raw, err := v.rawRequest(ctx, "auth/kubernetes/login", "", reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("keybackend: vault k8s-auth: login: %w", err)
	}

	var parsed struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", 0, fmt.Errorf("keybackend: vault k8s-auth: parsing login response: %w", err)
	}
	if parsed.Auth.ClientToken == "" {
		return "", 0, errors.New("keybackend: vault k8s-auth: login response had no client_token")
	}
	return parsed.Auth.ClientToken, time.Duration(parsed.Auth.LeaseDuration) * time.Second, nil
}

// renewSelf renews the currently held token in place
// (POST auth/token/renew-self).
func (v *VaultTransit) renewSelf(ctx context.Context, token string) (time.Duration, error) {
	raw, err := v.rawRequest(ctx, "auth/token/renew-self", token, nil)
	if err != nil {
		return 0, fmt.Errorf("keybackend: vault: renew-self: %w", err)
	}
	var parsed struct {
		Auth struct {
			LeaseDuration int `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0, fmt.Errorf("keybackend: vault: renew-self: parsing response: %w", err)
	}
	return time.Duration(parsed.Auth.LeaseDuration) * time.Second, nil
}

// rawRequest POSTs payload (or no body, if nil) as JSON to
// {addr}/v1/{path}, with token as the X-Vault-Token header (omitted if
// empty, for the pre-auth kubernetes/login call), and returns the raw
// response body on any 2xx status. A non-2xx status or any transport
// failure is returned as an error carrying Vault's own error body, so
// callers (Encrypt/Decrypt/k8sLogin/renewSelf) always get a hard error
// rather than a response they'd have to guess the validity of.
func (v *VaultTransit) rawRequest(ctx context.Context, path, token string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshaling request: %w", err)
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.addr+"/v1/"+path, body)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: reading response: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, bytes.TrimSpace(raw))
	}
	return raw, nil
}
