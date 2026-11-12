package mcpgw

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type SessionDispatchRequest struct {
	db.MCPSessionScope
	ConnectionID   string `json:"connection_id"`
	ActorSessionID string `json:"actor_session_id,omitempty"`
}
type SessionCoreClient interface {
	SessionToken(context.Context, SessionDispatchRequest) (Credential, error)
	SessionAdmit(context.Context, SessionDispatchRequest) error
}

var errSessionCore = errors.New("remote MCP admission is unavailable")

func (c *HTTPCoreClient) sessionRequest(ctx context.Context, in SessionDispatchRequest, mode string, generation *int64) (Credential, error) {
	token := mode == "token"
	if !in.Valid() || in.ConnectionID == "" || c.BaseURL == "" || c.InternalKey == "" {
		return Credential{}, sessionCoreFailure("configuration")
	}
	u, parseErr := url.Parse(c.BaseURL)
	if parseErr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme == "http" && !sessionCorePlaintextHost(u.Hostname(), c.ClusterDeployment)) {
		return Credential{}, sessionCoreFailure("transport_configuration")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var payload any = in
	if mode != "token" && mode != "admit" {
		payload = struct {
			SessionDispatchRequest
			Action string `json:"action"`
		}{in, mode}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Credential{}, sessionCoreFailure("encoding")
	}
	route := "/internal/mcp/session-connections/admit"
	if token {
		route = "/internal/mcp/session-connections/token"
	} else if mode != "admit" {
		route = "/internal/mcp/session-connections/lifecycle"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	if c.HTTP != nil {
		cp := *c.HTTP
		client = &cp
	}
	client.Transport = c.proxyFreeTransport()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for attempt := 0; attempt < 20; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+route, bytes.NewReader(raw))
		if err != nil {
			return Credential{}, sessionCoreFailure("request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Key", c.InternalKey)
		resp, err := client.Do(req)
		if err != nil {
			return Credential{}, sessionCoreFailure("transport")
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
		_ = resp.Body.Close()
		if readErr != nil || len(body) > 65536 {
			return Credential{}, sessionCoreFailure("response")
		}
		if resp.StatusCode == http.StatusServiceUnavailable {
			var busy struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(body, &busy) == nil && busy.Error == "refresh_in_progress" && attempt < 19 {
				if err := c.sessionWait(ctx, 100*time.Millisecond); err != nil {
					return Credential{}, sessionCoreFailure("refresh_timeout")
				}
				continue
			}
		}
		if resp.StatusCode == http.StatusNotFound && strings.TrimSpace(string(body)) == "not found" {
			_ = sessionCoreFailure("refused")
			return Credential{}, ErrConnectionGone
		}
		if resp.StatusCode != http.StatusOK {
			return Credential{}, sessionCoreFailure("status")
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		if token {
			var out coreTokenResponse
			if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF || out.URL == "" || out.HeaderName != "Authorization" || !strings.HasPrefix(out.Value, "Bearer ") || out.PinPolicy != PinPolicyStrict {
				return Credential{}, sessionCoreFailure("response")
			}
			cred := Credential{URL: out.URL, HeaderName: out.HeaderName, Value: out.Value, PinPolicy: PinPolicyStrict}
			if out.ExpiresAt != nil {
				cred.ExpiresAt = *out.ExpiresAt
			}
			return cred, nil
		}
		var out struct {
			Live       bool  `json:"live"`
			Applied    bool  `json:"applied"`
			Generation int64 `json:"credential_generation"`
		}
		if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF || (generation == nil && !out.Live) || (generation != nil && !out.Applied) {
			return Credential{}, sessionCoreFailure("response")
		}
		if generation != nil {
			valid := out.Generation == in.CredentialGeneration || (mode == "suspend" && out.Generation == in.CredentialGeneration+1)
			if !valid {
				return Credential{}, sessionCoreFailure("lifecycle_fence_response")
			}
			*generation = out.Generation
		}
		return Credential{}, nil
	}
	return Credential{}, sessionCoreFailure("refresh_timeout")
}
func (c *HTTPCoreClient) sessionWait(ctx context.Context, d time.Duration) error {
	if c.scopedWait != nil {
		return c.scopedWait(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func sessionCoreFailure(cause string) error {
	failurelog.Default.Event("mcp_session_gateway:"+cause, slog.LevelInfo, "component", "runner", "operation", "mcp_session_gateway", "cause", cause)
	return errSessionCore
}
func (c *HTTPCoreClient) SessionToken(ctx context.Context, in SessionDispatchRequest) (Credential, error) {
	return c.sessionRequest(ctx, in, "token", nil)
}
func (c *HTTPCoreClient) SessionAdmit(ctx context.Context, in SessionDispatchRequest) error {
	_, err := c.sessionRequest(ctx, in, "admit", nil)
	return err
}

func dispatchForGrant(gr *db.MCPGrant, actor string) (SessionDispatchRequest, error) {
	if gr.SessionScope == nil {
		return SessionDispatchRequest{}, sessionCoreFailure("scope_missing")
	}
	scope := *gr.SessionScope
	if !scope.Valid() || gr.Builtin != "" || gr.ProofKind != "session_id" || scope.AccountID != gr.AccountID || scope.RunnerSessionID != gr.SessionID || scope.HumanSessionID != gr.ProofValue || scope.GrantTokenHash != hex.EncodeToString(gr.TokenHash) {
		return SessionDispatchRequest{}, sessionCoreFailure("scope_invalid")
	}
	return SessionDispatchRequest{MCPSessionScope: scope, ConnectionID: gr.ConnectionID, ActorSessionID: actor}, nil
}
func (st *grantState) admitSession(ctx context.Context, in SessionDispatchRequest) error {
	core, ok := st.g.cfg.Core.(SessionCoreClient)
	if !ok {
		return sessionCoreFailure("unwired")
	}
	if err := core.SessionAdmit(ctx, in); err != nil {
		_ = sessionCoreFailure("core_refused")
		return err
	}
	// This read must follow the Core RPC: a restart or reissue during that RPC wins.
	live, err := db.MCPScopedGrantLive(ctx, st.g.cfg.DB, in.MCPSessionScope, in.ConnectionID)
	if err != nil {
		return sessionCoreFailure("runner_binding_read")
	}
	if !live {
		_ = sessionCoreFailure("runner_binding_refused")
		return ErrConnectionGone
	}
	return nil
}

func sessionCorePlaintextHost(host string, cluster bool) bool {
	host = strings.ToLower(host)
	if host == "localhost" || host == "blerg-core" {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.IsLoopback()
	}
	if !cluster {
		return false
	}
	for _, suffix := range []string{".svc.cluster.local", ".svc"} {
		if strings.HasSuffix(host, suffix) {
			labels := strings.Split(strings.TrimSuffix(host, suffix), ".")
			if len(labels) != 2 {
				return false
			}
			for _, label := range labels {
				if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
					return false
				}
				for _, c := range label {
					if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
						continue
					}
					return false
				}
			}
			return true
		}
	}
	return false
}

func (c *HTTPCoreClient) SessionLifecycle(ctx context.Context, in SessionDispatchRequest, action string) (int64, error) {
	switch action {
	case "renew", "suspend", "close":
	default:
		return 0, sessionCoreFailure("lifecycle_action")
	}
	var generation int64
	_, err := c.sessionRequest(ctx, in, action, &generation)
	return generation, err
}
