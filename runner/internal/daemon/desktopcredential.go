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
	EnrollmentPending bool   `json:"enrollment_pending,omitempty"`
	DesktopID         string `json:"desktop_id"`
	Token             string `json:"token"`
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

// StoreDesktopCredential uses the installer's file and atomic private replace.
func StoreDesktopCredential(stateDir string, c DesktopCredential) error {
	if c.DesktopID == "" || !strings.HasPrefix(c.Token, "bdt_") || len(c.Token) != 68 {
		return errors.New("invalid desktop credential")
	}
	if os.MkdirAll(stateDir, 0o700) != nil {
		return errors.New("private desktop state unavailable")
	}
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ownedByDaemon(info) {
		return errors.New("desktop state must be a private owned directory")
	}
	f, err := os.CreateTemp(stateDir, ".desktop-credential-")
	if err != nil {
		return errors.New("could not store private desktop credential")
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err = f.Chmod(0o600); err == nil {
		err = json.NewEncoder(f).Encode(c)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("could not store private desktop credential")
	}
	if os.Rename(name, filepath.Join(stateDir, "desktop-credential.json")) != nil {
		return errors.New("could not store private desktop credential")
	}
	dir, err := os.Open(stateDir) //nolint:gosec // private owned state directory verified above
	if err != nil {
		return errors.New("could not sync private desktop state")
	}
	defer func() { _ = dir.Close() }()
	if dir.Sync() != nil {
		return errors.New("could not sync private desktop state")
	}
	return nil
}
