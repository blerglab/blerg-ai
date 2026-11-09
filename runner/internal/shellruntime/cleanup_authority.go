package shellruntime

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

// cleanupRequest is local helper control, never browser or runner access
// authority. A receipt still needs independent physical cleanup evidence.
type cleanupRequest struct {
	Custody     string `json:"custody"`
	Helper      string `json:"helper"`
	Transaction string `json:"transaction"`
	MAC         string `json:"mac"`
}

type cleanupAuthority struct {
	key             [32]byte
	custody, helper string
}

func newCleanupAuthority(custody, helper string) (*cleanupAuthority, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, ErrClosed
	}
	return cleanupAuthorityFromBootstrap(custody, helper, key[:])
}

func cleanupAuthorityFromBootstrap(custody, helper string, key []byte) (*cleanupAuthority, error) {
	if custody == "" || helper == "" || len(key) != 32 {
		return nil, ErrClosed
	}
	a := &cleanupAuthority{custody: custody, helper: helper}
	copy(a.key[:], key)
	return a, nil
}

func (cleanupAuthority) String() string   { return "<shell cleanup authority>" }
func (cleanupAuthority) GoString() string { return "<shell cleanup authority>" }

// MarshalJSON cannot export the bootstrap key, even if fields are added later.
func (cleanupAuthority) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// bootstrapKey is used only in the helper's private first stdin frame. Do not
// include it in RPC, command arguments, environment, persisted state or errors.
func (a *cleanupAuthority) bootstrapKey() []byte { return append([]byte(nil), a.key[:]...) }

func (a *cleanupAuthority) tag(custody, helper, transaction string) []byte {
	raw, _ := json.Marshal([]string{"blerg-shell-cleanup-v2", "close", custody, helper, transaction})
	mac := hmac.New(sha256.New, a.key[:])
	_, _ = mac.Write(raw)
	return mac.Sum(nil)
}

func (a *cleanupAuthority) request(transaction string) cleanupRequest {
	return cleanupRequest{Custody: a.custody, Helper: a.helper, Transaction: transaction, MAC: base64.RawURLEncoding.EncodeToString(a.tag(a.custody, a.helper, transaction))}
}

func (a *cleanupAuthority) verify(r cleanupRequest) bool {
	if r.Custody != a.custody || r.Helper != a.helper || r.Transaction == "" || len(r.Transaction) > 128 || len(r.MAC) != 43 {
		return false
	}
	tag, err := base64.RawURLEncoding.DecodeString(r.MAC)
	return err == nil && hmac.Equal(tag, a.tag(r.Custody, r.Helper, r.Transaction))
}

// controlTag binds every request field, operation and sequence to this helper.
// Domain separation keeps close receipts and control authentication independent.
func (a *cleanupAuthority) controlTag(f HelperFrame) string {
	f.ControlMAC = ""
	raw, _ := json.Marshal(f)
	mac := hmac.New(sha256.New, a.key[:])
	_, _ = mac.Write([]byte("blerg-shell-control-v2\x00" + a.custody + "\x00" + a.helper + "\x00"))
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (a *cleanupAuthority) verifyControl(f HelperFrame) bool {
	return f.ControlSeq != 0 && len(f.ControlMAC) == 43 && hmac.Equal([]byte(f.ControlMAC), []byte(a.controlTag(f)))
}
