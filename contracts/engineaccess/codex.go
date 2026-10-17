package engineaccess

import (
	"encoding/json"
	"regexp"
	"time"
)

type codexWire struct {
	Mode        string    `json:"mode"`
	AccessToken string    `json:"access_token"`
	AccountID   string    `json:"chatgpt_account_id"`
	PlanType    string    `json:"plan_type,omitempty"`
	Generation  string    `json:"generation"`
	ExpiresAt   time.Time `json:"expires_at"`
}

var codexAccount = regexp.MustCompile(`^[A-Za-z0-9._:@/-]{1,256}$`)
var codexPlan = regexp.MustCompile(`^[a-z0-9_-]{0,64}$`)

func init() {
	Register(Codec{Engine: "codex", Mode: "chatgptAuthTokens", Encode: func(a Access) ([]byte, error) {
		return json.Marshal(codexWire(a)) //nolint:gosec // G117: explicit access-only broker wire; refresh authority is absent.
	}, Decode: func(raw []byte) (Access, error) {
		var w codexWire
		err := json.Unmarshal(raw, &w)
		return Access(w), err
	}, Validate: func(a Access) bool { return codexAccount.MatchString(a.AccountID) && codexPlan.MatchString(a.PlanType) }})
}
