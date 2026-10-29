package protocol

// OwnerShellSignedGrant authenticates the unchanged P0 grant. The public key is pinned
// over the existing authenticated runtime connection; this envelope never reaches a browser.
type OwnerShellSignedGrant struct {
	Grant     ShellGrant `json:"grant"`
	Signature string     `json:"signature"`
}

// OwnerShellClientFrame is bound to a dedicated, authenticated controlling attachment.
// It deliberately has no command, environment, cwd, session input, or agent-output fields.
type OwnerShellClientFrame struct {
	Human bool   `json:"human,omitempty"`
	Type  string `json:"type"`
	Seq   uint64 `json:"seq"`
	Data  []byte `json:"data"`
	Cols  uint16 `json:"cols"`
	Rows  uint16 `json:"rows"`
}

// OwnerShellInfo is authorized metadata for the exact human owner/login only, never a hub
// broadcast. Attached means that attaching requires an explicit takeover of that controller.
type OwnerShellInfo struct {
	Attached bool   `json:"attached"`
	Handle   string `json:"handle"`
	Shell    string `json:"shell_id"`
	Runtime  string `json:"runtime"`
	Folder   string `json:"folder"`
}
