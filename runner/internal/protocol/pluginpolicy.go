package protocol

import "github.com/blerglab/blerg-ai/contracts/pluginspec"

// PluginMarketplacePolicy is sent only over the authenticated server→daemon socket.
// A nil policy signals unavailable Core authority; receivers retain their last
// accepted policy and revision. LeaseSeconds remains for v1 wire compatibility;
// its expiration cannot restore revoked permissions.
type PluginMarketplacePolicy struct {
	Type         string             `json:"type"`
	SessionID    string             `json:"session_id,omitempty"`
	Policy       *pluginspec.Policy `json:"policy"`
	LeaseSeconds int                `json:"lease_seconds"`
}
