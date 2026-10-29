// Package desktopauth contains the identity a trusted desktop credential proves.
package desktopauth

import "time"

// Binding contains no bearer or filesystem metadata. Revision is checked against
// runner's persisted desktop owner, never accepted from daemon hello fields.
type Binding struct {
	DesktopID string    `json:"desktop_id"`
	OwnerID   string    `json:"owner_id"`
	Revision  int64     `json:"ownership_revision"`
	TokenID   string    `json:"token_id"`
	ExpiresAt time.Time `json:"expires_at"`
}
