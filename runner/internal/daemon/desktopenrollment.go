package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func (c *WSClient) enrollDesktop(parent context.Context, raw []byte) error {
	var m protocol.DesktopEnroll
	if c.config.DaemonMode == "runner" || c.config.CredentialStateDir == "" || len(raw) > 8192 || json.Unmarshal(raw, &m) != nil || m.RequestID == "" || m.ConnectionGeneration == "" || m.Binding.OwnerID == "" || m.Binding.Revision < 1 || !m.Binding.ExpiresAt.After(time.Now()) {
		return errors.New("desktop enrollment refused; use Settings → Account → Your desktops")
	}
	current, err := LoadDesktopCredential(c.config.CredentialStateDir)
	if err != nil {
		return err
	}
	if current != nil && current.DesktopID != m.Binding.DesktopID {
		return errors.New("desktop enrollment identity changed")
	}
	if err := StoreDesktopCredential(c.config.CredentialStateDir, DesktopCredential{DesktopID: m.Binding.DesktopID, Token: m.Token, EnrollmentPending: true}); err != nil {
		return err
	}
	c.enrollmentLegacy = false
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	_ = c.SendBounded(ctx, protocol.DesktopEnrollAck{Type: "desktop_enroll_ack", RequestID: m.RequestID, ConnectionGeneration: m.ConnectionGeneration})
	// The server closes only after accepting the ack. A lost ack or obsolete
	// request can be rotated on this same socket before the next reconnect.
	return nil
}

// An in-flight retry can rotate before delivery and leave a stale private file.
// Only pending enrollment may recover the prior shared socket; the server still
// requires its upgrade snapshot and refuses all owned desktops using that token.
func (c *WSClient) recoverPendingEnrollment() {
	if c.config.CredentialStateDir == "" || c.config.DaemonMode == "runner" || c.config.EnrollmentLegacyToken == "" || strings.HasPrefix(c.config.EnrollmentLegacyToken, "bdt_") {
		return
	}
	credential, err := LoadDesktopCredential(c.config.CredentialStateDir)
	if err == nil && credential != nil && credential.EnrollmentPending {
		c.enrollmentLegacy = true
	}
}
func (c *WSClient) confirmDesktopEnrollment(token, id string) error {
	credential, err := LoadDesktopCredential(c.config.CredentialStateDir)
	if err != nil || credential == nil || credential.Token != token || credential.DesktopID != id {
		return errors.New("desktop credential changed")
	}
	if credential.EnrollmentPending {
		credential.EnrollmentPending = false
		if err := StoreDesktopCredential(c.config.CredentialStateDir, *credential); err != nil {
			return err
		}
	}
	c.enrollmentLegacy = false
	return nil
}
