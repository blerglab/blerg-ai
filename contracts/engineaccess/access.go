// Package engineaccess defines the access-only credential exchange. Each native
// consumer registers its wire mode; generic brokers never assume provider fields.
// Registration is trusted application wiring, not request data.
package engineaccess

import (
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// Access never contains refresh authority. AccountID is a provider identity,
// distinct from the Blerg account that authorized delivery.
type Access struct {
	Mode        string
	AccessToken string
	AccountID   string
	PlanType    string
	Generation  string
	ExpiresAt   time.Time
}

type Codec struct {
	Engine   string
	Mode     string
	Encode   func(Access) ([]byte, error)
	Decode   func([]byte) (Access, error)
	Validate func(Access) bool
}

var ErrInvalidAccess = errors.New("managed credential access is unavailable")

var registry = struct {
	sync.RWMutex
	modes map[string]Codec
}{modes: map[string]Codec{}}

// Register declares a supported native protocol and returns a restoration hook
// for isolated tests. Codecs may encode only access material from Access.
func Register(codec Codec) func() {
	if codec.Engine == "" || codec.Mode == "" || codec.Encode == nil || codec.Decode == nil {
		panic("invalid managed credential codec")
	}
	registry.Lock()
	old, had := registry.modes[codec.Mode]
	registry.modes[codec.Mode] = codec
	registry.Unlock()
	return func() {
		registry.Lock()
		defer registry.Unlock()
		if had {
			registry.modes[codec.Mode] = old
		} else {
			delete(registry.modes, codec.Mode)
		}
	}
}

func SupportsEngine(engine string) bool {
	registry.RLock()
	defer registry.RUnlock()
	for _, codec := range registry.modes {
		if codec.Engine == engine {
			return true
		}
	}
	return false
}

func SupportsMode(engine, mode string) bool {
	codec, ok := lookup(mode)
	return ok && engine != "" && codec.Engine == engine
}

func lookup(mode string) (Codec, bool) {
	registry.RLock()
	defer registry.RUnlock()
	codec, ok := registry.modes[mode]
	return codec, ok
}

func (a Access) MarshalJSON() ([]byte, error) {
	codec, ok := lookup(a.Mode)
	if !ok {
		return nil, ErrInvalidAccess
	}
	raw, err := codec.Encode(a)
	if err != nil {
		return nil, ErrInvalidAccess
	}
	return raw, nil
}

func (a *Access) UnmarshalJSON(raw []byte) error {
	decoded, err := decode(raw, "", time.Now())
	if err != nil {
		*a = Access{}
		return err
	}
	*a = decoded
	return nil
}

// DecodeForEngine binds the response mode to the engine selected by immutable
// launch configuration. A registered foreign mode is still not permission to
// change engine, provider identity or payer.
func DecodeForEngine(engine string, raw []byte, now time.Time) (Access, error) {
	if engine == "" {
		return Access{}, ErrInvalidAccess
	}
	return decode(raw, engine, now)
}

func decode(raw []byte, engine string, now time.Time) (Access, error) {
	if len(raw) > 32<<10 {
		return Access{}, ErrInvalidAccess
	}
	var header struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return Access{}, ErrInvalidAccess
	}
	codec, ok := lookup(header.Mode)
	if !ok || (engine != "" && codec.Engine != engine) {
		return Access{}, ErrInvalidAccess
	}
	a, err := codec.Decode(raw)
	if err != nil || a.Mode != codec.Mode || a.AccessToken == "" || len(a.AccessToken) > 24<<10 || len(a.AccountID) > 256 || len(a.PlanType) > 128 || a.Generation == "" || len(a.Generation) > 128 || !a.ExpiresAt.After(now) || (codec.Validate != nil && !codec.Validate(a)) {
		return Access{}, ErrInvalidAccess
	}
	return a, nil
}

type tokenWire struct {
	Mode        string    `json:"mode"`
	AccessToken string    `json:"access_token"`
	AccountID   string    `json:"account_id"`
	PlanType    string    `json:"plan_type,omitempty"`
	Generation  string    `json:"generation"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// TokenCodec is a common bearer-token envelope with a provider-neutral account
// field. An engine can supply its own codec and additional validator instead.
func TokenCodec(engine, mode string) Codec {
	return Codec{Engine: engine, Mode: mode, Encode: func(a Access) ([]byte, error) {
		return json.Marshal(tokenWire(a)) //nolint:gosec // G117: explicit access-only broker wire; refresh authority is absent.
	}, Decode: func(raw []byte) (Access, error) {
		var w tokenWire
		err := json.Unmarshal(raw, &w)
		return Access(w), err
	}}
}
