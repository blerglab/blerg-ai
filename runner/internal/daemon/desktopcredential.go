package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DesktopCredential is private daemon state, not a public protocol payload.
type DesktopCredential struct {
	DesktopID string `json:"desktop_id"`
	Token     string `json:"token"`
}

// LoadDesktopCredential refuses links, shared files and oversized data. The
// installer writes atomically in the user's private daemon state directory.
func LoadDesktopCredential(stateDir string) (*DesktopCredential, error) {
	name := filepath.Join(stateDir, "desktop-credential.json")
	before, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 || !ownedByDaemon(before) {
		return nil, errors.New("desktop credential must be a private regular file")
	}
	f, err := os.Open(name) //nolint:gosec // private daemon state path, verified before and after opening
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("desktop credential changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return nil, errors.New("invalid desktop credential")
	}
	var c DesktopCredential
	if json.Unmarshal(raw, &c) != nil || c.DesktopID == "" || !strings.HasPrefix(c.Token, "bdt_") || len(c.Token) != 68 {
		return nil, errors.New("invalid desktop credential")
	}
	return &c, nil
}
