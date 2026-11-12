// Package shellpolicy defines the shared owner-shell control-plane wire contract.
package shellpolicy

// ShellPolicy is authoritative only over the authenticated core control plane.
// Unknown/unavailable policy must fail closed; restart cannot reuse cached enablement.
type ShellPolicy struct {
	Version  int    `json:"version"`
	Revision uint64 `json:"revision"`
	Enabled  bool   `json:"enabled"`
}

const ShellGrantSeconds = 30
const ShellRenewSeconds = 10

type ShellGrant struct {
	Privileged bool `json:"privileged,omitempty"`
	// Version 2 binds short terminal access to retained runtime custody.
	// Neither field authorizes process creation or extends process lifetime.
	CustodyID        string `json:"custody_id,omitempty"`
	AccessEpoch      uint64 `json:"access_epoch,omitempty"`
	Version          int    `json:"version"`
	Audience         string `json:"audience"`
	OwnerID          string `json:"owner_id"`
	LoginID          string `json:"login_id"`
	SessionID        string `json:"session_id"`
	LaunchGeneration string `json:"launch_generation"`
	ConnectionID     string `json:"connection_id"`
	ShellGeneration  string `json:"shell_generation"`
	Runtime          string `json:"runtime"`
	ConsentID        string `json:"consent_id,omitempty"`
	PolicyRevision   uint64 `json:"policy_revision"`
	IssuedAt         int64  `json:"issued_at"`
	ExpiresAt        int64  `json:"expires_at"`
}

type HostShellConsent struct {
	ID               string `json:"id"`
	OwnerID          string `json:"owner_id"`
	LaunchGeneration string `json:"launch_generation"`
	MachineID        string `json:"machine_id"`
	Account          string `json:"account"`
	UID              uint64 `json:"uid"`
	Shell            string `json:"shell"`
	Cwd              string `json:"cwd"`
	ChallengeID      string `json:"challenge_id"`
}

// AuthorizationRequest adds request correlation without changing the v1 policy
// response. A legacy caller may omit Nonce but cannot obtain a typed denial.
type AuthorizationRequest struct {
	Owner string `json:"owner_id"`
	Login string `json:"login_id"`
	Nonce string `json:"nonce,omitempty"`
}

// AuthorizationDenial is the only HTTP denial that proves this exact request's
// login is no longer live. Status codes and arbitrary error strings prove none
// of that. It is sent only over the authenticated internal core connection.
type AuthorizationDenial struct {
	Version int    `json:"version"`
	Outcome string `json:"outcome"`
	Owner   string `json:"owner_id"`
	Login   string `json:"login_id"`
	Nonce   string `json:"nonce"`
	Reason  string `json:"reason"`
}

func (d AuthorizationDenial) Matches(r AuthorizationRequest) bool {
	return d.Version == 1 && d.Outcome == "deny" && d.Reason == "login_unavailable" && r.Owner != "" && r.Login != "" && r.Nonce != "" && d.Owner == r.Owner && d.Login == r.Login && d.Nonce == r.Nonce
}

// AuthorizationAllowed proves a fresh exact-login check to persistence-capable
// runners and bounds custody by the original login's absolute expiration.
type AuthorizationAllowed struct {
	ShellPolicy
	AuthorizationVersion int    `json:"authorization_version,omitempty"`
	Outcome              string `json:"outcome,omitempty"`
	Owner                string `json:"owner_id,omitempty"`
	Login                string `json:"login_id,omitempty"`
	Nonce                string `json:"nonce,omitempty"`
	LoginExpiresAt       int64  `json:"login_expires_at,omitempty"`
}

func (a AuthorizationAllowed) Matches(r AuthorizationRequest, now int64) bool {
	return a.AuthorizationVersion == 2 && a.Outcome == "allow" && a.Version == 1 && a.Revision > 0 && r.Nonce != "" && r.Owner != "" && r.Login != "" && a.Nonce == r.Nonce && a.Owner == r.Owner && a.Login == r.Login && a.LoginExpiresAt > now
}

func (a AuthorizationAllowed) Legacy() bool {
	return a.AuthorizationVersion == 0 && a.Outcome == "" && a.Owner == "" && a.Login == "" && a.Nonce == "" && a.LoginExpiresAt == 0 && a.Version == 1 && a.Revision > 0
}
