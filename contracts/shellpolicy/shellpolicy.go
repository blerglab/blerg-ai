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
