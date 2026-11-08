package protocol

import (
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
)

// ShellLinkHello is a fresh challenge from one shell custodian incarnation. On
// Linux host shells the custodian is the keeper, surviving daemon restarts. It
// provides no enrollment evidence; core authentication supplies that separately.
type ShellLinkHello struct {
	Version   int    `json:"version"`
	Daemon    string `json:"daemon"`
	Challenge string `json:"challenge"`
}

// ShellLinkWelcome travels only over the authenticated desktop link. Desktop
// contains verified credential metadata, never the credential itself. Legacy and
// compatibility desktops without a personal binding cannot receive v2 authority.
type ShellLinkWelcome struct {
	Version   int                 `json:"version"`
	Runner    string              `json:"runner"`
	Daemon    string              `json:"daemon"`
	Challenge string              `json:"challenge"`
	Desktop   desktopauth.Binding `json:"desktop"`
}

func (w ShellLinkWelcome) Matches(h ShellLinkHello, now time.Time) bool {
	if h.Version != ShellPersistenceVersion || w.Version != ShellPersistenceVersion || h.Daemon == "" || h.Challenge == "" || w.Daemon != h.Daemon || w.Challenge != h.Challenge || w.Desktop.Revision <= 0 || !w.Desktop.ExpiresAt.After(now) {
		return false
	}
	for _, value := range []string{w.Runner, w.Daemon, w.Challenge, w.Desktop.DesktopID, w.Desktop.OwnerID, w.Desktop.TokenID} {
		if value == "" || len(value) > 256 {
			return false
		}
	}
	return true
}

// SameCustodian compares immutable pins only. First authenticate the current
// link and call Matches with its fresh challenge and clock. Expiry checking is
// intentionally separate from identity equality.
func (w ShellLinkWelcome) SameCustodian(old ShellLinkWelcome) bool {
	return w.Version == ShellPersistenceVersion && old.Version == ShellPersistenceVersion && w.Runner != "" && w.Daemon != "" && w.Desktop.DesktopID != "" && w.Desktop.OwnerID != "" && w.Desktop.TokenID != "" && w.Desktop.Revision > 0 && w.Runner == old.Runner && w.Daemon == old.Daemon && w.Desktop.DesktopID == old.Desktop.DesktopID && w.Desktop.OwnerID == old.Desktop.OwnerID && w.Desktop.TokenID == old.Desktop.TokenID && w.Desktop.Revision == old.Desktop.Revision && w.Desktop.ExpiresAt.Equal(old.Desktop.ExpiresAt)
}
