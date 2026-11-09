package shellruntime

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"time"
)

type retainedConsent struct {
	value             protocol.ShellConsentV2
	accepted, revoked bool
	used              string
}

func (s *Service) consumeRetainedConsent(entry *registered, c protocol.ShellCustody) bool {
	if c.Validate() != nil {
		return false
	}
	entry.consentMu.Lock()
	defer entry.consentMu.Unlock()
	for _, consent := range entry.consents {
		if !consent.accepted || consent.revoked || !consent.value.Allows(c) {
			continue
		}
		if consent.used == c.Digest() {
			return true
		}
		if consent.used != "" || s.now().Unix() >= consent.value.ExpiresAt {
			return false
		}
		consent.used = c.Digest()
		return true
	}
	return false
}
func (s *Service) retainedConsentRPC(ctx context.Context, entry *registered, b ownershell.Binding, r RPC) RPC {
	result := RPC{Version: 2, Type: "owner_shell_result", SessionID: r.SessionID, RequestID: r.RequestID, Op: r.Op, ErrorCode: string(ownershell.ReasonRuntimeConsent)}
	if b.Runtime != "host" || b.ShellLink.Version != 2 || entry.persistentIdentity == nil || r.Actor.Kind != "human" || r.Actor.Owner != b.Owner || r.Actor.Login == "" {
		return result
	}
	closed := map[string]bool{}
	classes := append([]*retainedCoordinator(nil), entry.retainedHistory...)
	if entry.retained != nil {
		classes = append(classes, entry.retained)
	}
	for _, class := range classes {
		class.mu.Lock()
		for id, child := range class.children {
			if child.closed {
				closed[id] = true
			}
		}
		class.mu.Unlock()
	}
	entry.consentMu.Lock()
	if entry.consents == nil {
		entry.consents = map[string]*retainedConsent{}
	}
	for id, v := range entry.consents {
		// A close RPC may settle the child before its withdrawal RPC arrives.
		// Keep that exact acceptance long enough to authenticate and acknowledge
		// the terminal withdrawal; it can never authorize a new allocation.
		withdrawing := r.Op == "consent_withdraw" && r.PersistentConsent != nil && v.value == *r.PersistentConsent
		if closed[v.used] && !withdrawing || v.used == "" && s.now().Unix() >= v.value.ExpiresAt {
			delete(entry.consents, id)
		}
	}
	if r.Op == "consent_challenge" {
		if len(entry.consents) >= 128 {
			entry.consentMu.Unlock()
			return result
		}
		id, e1 := newHelperID()
		challenge, e2 := newHelperID()
		if e1 != nil || e2 != nil {
			entry.consentMu.Unlock()
			return result
		}
		i := *entry.persistentIdentity
		w := b.ShellLink
		host := protocol.HostShellConsent{ID: id, ChallengeID: challenge, OwnerID: b.Owner, LaunchGeneration: b.Launch, MachineID: i.Machine, Account: i.Account, UID: i.UID, Shell: i.Shell, Cwd: i.Cwd}
		pins := protocol.ShellCustody{Owner: b.Owner, Login: r.Actor.Login, Session: b.Session, Launch: b.Launch, Runtime: b.Runtime, RuntimeGeneration: b.RuntimeGeneration, Root: b.Root, Manifest: b.ManifestDigest, Desktop: w.Desktop.DesktopID, Credential: w.Desktop.TokenID, OwnershipRevision: w.Desktop.Revision, Runner: w.Runner, Daemon: w.Daemon, Identity: b.IdentityDigest}
		consent := protocol.NewShellConsentV2(host, pins, s.now().Add(2*time.Minute).Unix())
		entry.consents[challenge] = &retainedConsent{value: consent}
		result.PersistentConsent = &consent
		result.OK = true
		entry.consentMu.Unlock()
		return result
	}
	supplied := r.PersistentConsent
	if supplied == nil {
		entry.consentMu.Unlock()
		return result
	}
	known := entry.consents[supplied.Host.ChallengeID]
	if known == nil || known.value != *supplied || known.value.Pins.Owner != r.Actor.Owner || known.value.Pins.Login != r.Actor.Login {
		entry.consentMu.Unlock()
		return result
	}
	switch r.Op {
	case "consent_accept":
		if !known.accepted && !known.revoked && known.used == "" && s.now().Unix() < known.value.ExpiresAt {
			known.accepted = true
			result.OK = true
			result.PersistentConsent = &known.value
		}
	case "consent_withdraw":
		known.revoked = true
		result.OK = true
	}
	value := known.value
	entry.consentMu.Unlock()
	if r.Op == "consent_withdraw" && entry.retained != nil {
		c := entry.retained
		c.mu.Lock()
		var children []protocol.ShellCustody
		for id, child := range c.children {
			if child.custody.Consent == value.Host.ID && !child.closed {
				if child.closing == "" {
					child.closing = "consent-withdrawn"
					_ = c.registry.BeginClose(id, child.closing)
				}
				children = append(children, child.custody)
			}
		}
		c.suspendLocked()
		c.mu.Unlock()
		for _, child := range children {
			if _, err := c.Close(ctx, child, "consent-withdrawn"); err != nil {
				result.OK = false
			}
		}
	}
	return result
}

// Authenticated terminal intents fence locally before the per-launch command
// queue. A stalled bootstrap or rekey cannot run past a queued withdrawal.
func (s *Service) retainedRemoteBarrier(r RPC) {
	if r.Version != 2 || (r.Op != "close" && r.Op != "consent_withdraw") {
		return
	}
	s.mu.Lock()
	entry := s.launches[r.SessionID]
	s.mu.Unlock()
	if entry == nil {
		return
	}
	var consentID string
	if r.Op == "consent_withdraw" {
		supplied := r.PersistentConsent
		if supplied == nil {
			return
		}
		entry.consentMu.Lock()
		known := entry.consents[supplied.Host.ChallengeID]
		if known != nil && known.value == *supplied && known.value.Pins.Owner == r.Actor.Owner && known.value.Pins.Login == r.Actor.Login && r.Actor.Kind == "human" {
			known.revoked = true
			consentID = known.value.Host.ID
		}
		entry.consentMu.Unlock()
		if consentID == "" {
			return
		}
	}
	s.mu.Lock()
	c := entry.retained
	s.mu.Unlock()
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := false
	for id, child := range c.children {
		if child.closed {
			continue
		}
		transaction := "consent-withdrawn"
		if r.Op == "close" {
			if r.Custody == nil || *r.Custody != child.custody || len(r.Transaction) == 0 || len(r.Transaction) > 256 {
				continue
			}
			transaction = r.Transaction
		} else if child.custody.Consent != consentID {
			continue
		}
		if child.closing == "" {
			child.closing = transaction
			_ = c.registry.BeginClose(id, transaction)
		}
		changed = true
	}
	if changed {
		c.suspendLocked()
	}
}
