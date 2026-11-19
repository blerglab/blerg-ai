package protocol

// ShellConsentV2 is a separate, one-use host-shell affirmation. Its pins omit
// access connection/epoch and display hostname; accepting it never supplies
// restart custody. Both processes must retain their own affirmative memory.
type ShellConsentV2 struct {
	Version   int              `json:"version"`
	Host      HostShellConsent `json:"host"`
	Pins      ShellCustody     `json:"pins"`
	ExpiresAt int64            `json:"expires_at"`
}

func consentPins(c ShellCustody) ShellCustody {
	c.Version = 2
	c.Shell = ""
	c.Nonce = ""
	c.Created = 0
	c.LifetimeUntil = 0
	return c
}
func NewShellConsentV2(host HostShellConsent, c ShellCustody, expires int64) ShellConsentV2 {
	c.Consent = host.ID
	return ShellConsentV2{Version: 2, Host: host, Pins: consentPins(c), ExpiresAt: expires}
}
func (c ShellConsentV2) Allows(child ShellCustody) bool {
	return c.Version == 2 && c.Host.ID != "" && c.Host.ChallengeID != "" && c.Pins.Consent == c.Host.ID && c.Pins == consentPins(child)
}

// PinsDigest identifies pre-allocation consent context, never allocation proof.
func (c ShellConsentV2) PinsDigest() string { return custodyDigest(consentPins(c.Pins)) }
