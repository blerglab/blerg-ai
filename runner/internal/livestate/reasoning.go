package livestate

// Reasoning is a live observation. Tokens is absent when the engine has not
// reported a count; Since is server-time milliseconds for a ticking fallback.
type Reasoning struct {
	Since  int64 `json:"since"`
	Tokens *int  `json:"tokens,omitempty"`
}
