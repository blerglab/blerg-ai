package mcpconn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SessionOAuthRequest carries identifiers only. URLs, issuer, provenance and the
// current runtime generation must come from the trusted authority, never an agent.
type SessionOAuthRequest struct {
	AccountID       string `json:"account_id"`
	HumanSessionID  string `json:"human_session_id"`
	RunnerSessionID string `json:"runner_session_id"`
	DefinitionID    string `json:"definition_id"`
}

type SessionServerDefinition struct {
	ID       string `json:"id"`
	Version  int64  `json:"version"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Issuer   string `json:"issuer"`
	Source   string `json:"source"` // settings or session_suggestion_saved_by_human
	ClientID string `json:"client_id,omitempty"`
}

type SessionOAuthAuthorization struct {
	Binding          SessionBinding          `json:"binding"`
	Definition       SessionServerDefinition `json:"definition"`
	LaunchGeneration int64                   `json:"launch_generation"`
}

// SessionOAuthAuthority must check live ownership, human-approved definition and
// supported runtime. Nil is deliberately unavailable; production wiring follows
// in the Runner lifecycle increment. A stored login sid is not this authority.
type SessionOAuthAuthority interface {
	Resolve(context.Context, SessionOAuthRequest) (SessionOAuthAuthorization, error)
	Validate(context.Context, SessionOAuthAuthorization) error
}

// HTTPSessionOAuthAuthority speaks the future Runner control-plane contract.
// BaseURL and InternalKey are operator configuration. The installation label in
// the response is bookkeeping under that shared key, not another identity.
type HTTPSessionOAuthAuthority struct {
	directTransportOnce sync.Once
	directTransport     http.RoundTripper

	ClusterDeployment bool // operator configuration, never inferred from a hostname

	BaseURL     string
	InternalKey string       `json:"-"`
	HTTP        *http.Client `json:"-"`
}

type sessionAuthorityError struct {
	cause  string
	status int
}

func (e *sessionAuthorityError) Error() string {
	return fmt.Sprintf("session authority cause=%s status=%d", e.cause, e.status)
}
func (e *sessionAuthorityError) Unwrap() error {
	if e.status == http.StatusForbidden || e.status == http.StatusNotFound {
		return ErrNotFound
	}
	return ErrUnavailable
}

func (a *HTTPSessionOAuthAuthority) call(ctx context.Context, path string, input, output any) error {
	u, err := url.Parse(a.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || a.InternalKey == "" {
		return &sessionAuthorityError{cause: "configuration"}
	}
	if u.Scheme == "http" && !sessionAuthorityPlaintextHost(u.Hostname(), a.ClusterDeployment) {
		return &sessionAuthorityError{cause: "configuration"}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return &sessionAuthorityError{cause: "response"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return &sessionAuthorityError{cause: "response"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", a.InternalKey)
	client := &http.Client{Timeout: 10 * time.Second}
	if a.HTTP != nil {
		copyClient := *a.HTTP
		client = &copyClient
	}
	if client.Timeout <= 0 || client.Timeout > 10*time.Second {
		client.Timeout = 10 * time.Second
	}
	client.Transport = a.proxyFreeTransport()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return &sessionAuthorityError{cause: "transport"}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return &sessionAuthorityError{cause: "status", status: resp.StatusCode}
	}
	if resp.StatusCode != http.StatusOK {
		return &sessionAuthorityError{cause: "status", status: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10+1))
	if err != nil || len(body) > 64<<10 {
		return &sessionAuthorityError{cause: "response"}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err = dec.Decode(output); err != nil {
		return &sessionAuthorityError{cause: "response"}
	}
	if dec.Decode(new(any)) != io.EOF {
		return &sessionAuthorityError{cause: "response"}
	}
	return nil
}

func (a *HTTPSessionOAuthAuthority) Resolve(ctx context.Context, in SessionOAuthRequest) (SessionOAuthAuthorization, error) {
	var out SessionOAuthAuthorization
	err := a.call(ctx, "/internal/mcp/session-oauth/resolve", in, &out)
	return out, err
}
func (a *HTTPSessionOAuthAuthority) Validate(ctx context.Context, in SessionOAuthAuthorization) error {
	var out struct {
		Live bool `json:"live"`
	}
	if err := a.call(ctx, "/internal/mcp/session-oauth/validate", in, &out); err != nil {
		return err
	}
	if !out.Live {
		return ErrNotFound
	}
	return nil
}

var errSessionOAuthAuthority = fmt.Errorf("%w: session OAuth authority unavailable", ErrUnavailable)

func WithSessionOAuthAuthority(authority SessionOAuthAuthority) Option {
	return func(s *Service) { s.sessionAuthority = authority }
}

// Match the internal Kubernetes service URL form, never a public suffix lookalike.
// Production wiring must use a fully qualified Service name, not a bare hostname.
func sessionAuthorityPlaintextHost(host string, cluster bool) bool {
	host = strings.ToLower(host)
	if host == "localhost" || host == "blerg-runner" {
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

func (a *HTTPSessionOAuthAuthority) ValidateSessionDispatch(ctx context.Context, in SessionDispatchRequest, authorization SessionOAuthAuthorization) error {
	var out struct {
		Live bool `json:"live"`
	}
	if err := a.call(ctx, "/internal/mcp/session-oauth/admit", struct {
		Dispatch      SessionDispatchRequest    `json:"dispatch"`
		Authorization SessionOAuthAuthorization `json:"authorization"`
	}{in, authorization}, &out); err != nil {
		return err
	}
	if !out.Live {
		return ErrNotFound
	}
	return nil
}

// proxyFreeTransport keeps pooled direct connections and never mutates caller configuration.
// A custom nonstandard transport is a trusted injection seam; production uses *http.Transport.
func (a *HTTPSessionOAuthAuthority) proxyFreeTransport() http.RoundTripper {
	a.directTransportOnce.Do(func() {
		base := http.DefaultTransport
		if a.HTTP != nil && a.HTTP.Transport != nil {
			base = a.HTTP.Transport
		}
		if standard, ok := base.(*http.Transport); ok {
			clone := standard.Clone()
			clone.Proxy = nil
			a.directTransport = clone
		} else {
			a.directTransport = base
		}
	})
	return a.directTransport
}
