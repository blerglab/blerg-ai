package livedata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/contracts/livemcp"
)

const UnsafeHostWarning = "Agents on this machine can reach the polling credentials."

// Revision is the exact, immutable approval selection. Digest is computed with
// scope.digest empty; Scope's other fields are included, including all epochs.
// CardSpec is the setup-time native renderer declaration, not executable code.
// Legacy approved grants may omit it; all new proposal entry points require it.
type CardSpec struct {
	Kind      string          `json:"kind"`
	Sparkline bool            `json:"sparkline,omitempty"`
	Spec      json.RawMessage `json:"spec,omitempty"`
}

const NativeCardSetupRefusal = "MCP polling supports native stat and gauge cards without sparklines. Use a stat or gauge card; other chart specs cannot be wired."

func (c *CardSpec) Native() bool {
	return c != nil && (c.Kind == "stat" || c.Kind == "gauge") && !c.Sparkline && len(c.Spec) == 0
}

type Revision struct {
	Card            *CardSpec       `json:"card,omitempty"`
	Scope           Scope           `json:"scope"`
	ToolDefinition  json.RawMessage `json:"tool_definition"`
	Arguments       json.RawMessage `json:"arguments"`
	Mapping         livemcp.Mapping `json:"mapping"`
	IntervalSeconds int             `json:"interval_seconds"`
	ExpiresAt       time.Time       `json:"expires_at"`
	OwnerOnly       bool            `json:"owner_only"`
}

func (r Revision) Canonical(now time.Time) (Revision, []byte, error) {
	r.Scope.Digest = strings.Repeat("0", 64)
	if (r.Card != nil && !r.Card.Native()) || !r.Scope.Valid() || !r.OwnerOnly || r.IntervalSeconds < 15 || r.IntervalSeconds > 86400 || !r.ExpiresAt.After(now) || r.ExpiresAt.After(now.Add(30*24*time.Hour)) {
		return Revision{}, nil, ErrDenied
	}
	args, err := livemcp.CanonicalObject(r.Arguments)
	if err != nil {
		return Revision{}, nil, ErrDenied
	}
	definition, defErr := livemcp.CanonicalObject(r.ToolDefinition)
	if defErr != nil || !ValidateToolArguments(definition, args, r.Scope.Tool, r.Scope.Hash) || SecretArguments(args) || livemcp.Validate(r.Mapping) != nil {
		return Revision{}, nil, ErrDenied
	}
	for _, f := range r.Mapping.Fields {
		if len(f.Constant) > 0 && SecretArguments(f.Constant) {
			return Revision{}, nil, ErrDenied
		}
	}
	r.ToolDefinition = definition
	r.Arguments = args
	r.ExpiresAt = r.ExpiresAt.UTC().Truncate(time.Microsecond)
	r.Scope.Digest = ""
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > 64*1024 {
		return Revision{}, nil, ErrDenied
	}
	sum := sha256.Sum256(append([]byte("blerg-live-binding-v1\x00"), raw...))
	r.Scope.Digest = hex.EncodeToString(sum[:])
	return r, raw, nil
}

// SecretArguments is deliberately conservative, not a claim that arbitrary
// strings can be proven nonsecret. Authentication belongs in the connection.
func SecretArguments(raw []byte) bool {
	if len(raw) > 64*1024 {
		return true
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if d.Decode(&value) != nil {
		return true
	}
	return secretValue(value, 0)
}
func secretValue(v any, depth int) bool {
	if depth > 64 {
		return true
	}
	switch x := v.(type) {
	case map[string]any:
		for name, value := range x {
			if secretName(name) || secretValue(value, depth+1) {
				return true
			}
		}
	case []any:
		for _, value := range x {
			if secretValue(value, depth+1) {
				return true
			}
		}
	case string:
		text := strings.ToLower(strings.TrimSpace(x))
		fields := strings.Fields(text)
		if len(fields) > 1 && (fields[0] == "bearer" || fields[0] == "basic" || fields[0] == "token") {
			return true
		}
		if strings.Contains(text, "bearer ") || strings.Contains(text, "basic ") || strings.Contains(text, "private key-----") || strings.HasPrefix(text, "sk-") || strings.HasPrefix(text, "eyj") || strings.HasPrefix(text, "ghp_") || strings.HasPrefix(text, "gho_") || strings.HasPrefix(text, "github_pat_") || strings.HasPrefix(text, "xoxb-") || strings.HasPrefix(text, "akia") || strings.HasPrefix(text, "glpat-") {
			return true
		}
		if u, err := url.Parse(x); err == nil {
			if u.User != nil {
				return true
			}
			for key := range u.Query() {
				if secretName(key) {
					return true
				}
			}
		}
	}
	return false
}
func secretName(name string) bool {
	name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, name)
	for _, part := range []string{"authorization", "password", "secret", "credential", "apikey", "privatekey", "cookie", "accesstoken", "refreshtoken", "authtoken", "clienttoken"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	switch name {
	case "auth", "headers", "token", "key", "pwd", "passwd", "passphrase", "sig", "session", "xamzsignature":
		return true
	}
	return false
}

type Proposal struct {
	AskID         string    `json:"ask_id"`
	CreatedAt     time.Time `json:"created_at"`
	Configuration Revision  `json:"configuration"`
	Digest        string    `json:"digest"`
	Status        string    `json:"status"`
}
type Decision struct {
	AccountID   string `json:"account_id"`
	AccessToken string `json:"access_token,omitempty"`
	BindingID   string `json:"binding_id"`
	Revision    int64  `json:"revision"`
	Digest      string `json:"digest"`
	AskID       string `json:"ask_id"`
	Approve     bool   `json:"approve"`
}
type PolicyWrite struct {
	AccountID   string `json:"account_id"`
	AccessToken string `json:"access_token,omitempty"`
	BindingID   string `json:"binding_id"`
	Revision    int64  `json:"revision"`
	Digest      string `json:"digest"`
	Mode        string `json:"mode"`
}
type DecisionReceipt struct {
	BindingID string `json:"binding_id"`
	Revision  int64  `json:"revision"`
	Digest    string `json:"digest"`
	AskID     string `json:"ask_id"`
	Status    string `json:"status"`
}

// ApprovedRevision revalidates the complete immutable configuration at each
// authority boundary. A scope digest alone cannot authorize a local mapper.
func (g Grant) ApprovedRevision() (Revision, error) {
	var r Revision
	if json.Unmarshal(g.Configuration, &r) != nil {
		return Revision{}, ErrDenied
	}
	config, raw, err := r.Canonical(g.ExpiresAt.Add(-time.Hour))
	args, argsErr := livemcp.CanonicalObject(g.Arguments)
	if err != nil || argsErr != nil || config.Scope != g.Scope || !bytes.Equal(raw, g.Configuration) || !bytes.Equal(config.Arguments, args) || !config.ExpiresAt.Equal(g.ExpiresAt) {
		return Revision{}, ErrDenied
	}
	return config, nil
}
