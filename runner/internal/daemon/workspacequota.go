package daemon

// Persistent quota storage uses a dedicated filesystem image per session. The
// filesystem itself, including all arbitrary agent writes, enforces bytes and
// inode counts. The image and journal stay outside the public tree. Linux needs
// mount authority; unsupported/unprivileged hosts explicitly refuse allocation.
import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type FilesystemWorkspaceStorage struct {
	Root string
	mu   sync.Mutex
}
type workspaceQuotaRecord struct {
	LoopDevice    string                   `json:"loop_device,omitempty"`
	Version       int                      `json:"version"`
	Session       string                   `json:"session"`
	Budget        protocol.WorkspaceBudget `json:"budget"`
	Phase         string                   `json:"phase"`
	ImageIdentity protocol.WorkspaceRoot   `json:"image_identity"`
}

func (s *FilesystemWorkspaceStorage) paths(session string) (directory, image, mount, tree string, err error) {
	if !sandboxCloneIDPattern.MatchString(session) || !filepath.IsAbs(s.Root) {
		return "", "", "", "", errors.New("invalid quota allocation binding")
	}
	directory = filepath.Join(s.Root, session)
	image = filepath.Join(directory, "checkout.img")
	mount = filepath.Join(directory, "mount")
	tree = filepath.Join(mount, "tree")
	return
}
func saveWorkspaceQuota(directory string, record workspaceQuotaRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	key, err := workspaceManifestKey()
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	envelope, err := json.Marshal(workspaceEnvelope{State: raw, MAC: hex.EncodeToString(mac.Sum(nil))})
	if err != nil {
		return err
	}
	return writeWorkspacePrivate(filepath.Join(directory, "allocation.json"), envelope)
}
func readWorkspaceQuota(directory string) (workspaceQuotaRecord, error) {
	var record workspaceQuotaRecord
	path := filepath.Join(directory, "allocation.json")
	info, err := os.Lstat(path)
	if err != nil || !safeSandboxGitInfo(info, false) || info.Mode().Perm() != 0o600 || info.Size() > 16384 {
		return record, errors.New("quota journal unavailable")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // validated private quota journal under a session-derived directory
	if err != nil {
		return record, err
	}
	var envelope workspaceEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return record, err
	}
	key, err := workspaceManifestKey()
	if err != nil {
		return record, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(envelope.State)
	signature, err := hex.DecodeString(envelope.MAC)
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return record, errors.New("quota journal authentication failed")
	}
	if err := json.Unmarshal(envelope.State, &record); err != nil || record.Version != 1 {
		return record, errors.New("invalid quota journal")
	}
	return record, nil
}
func writeWorkspacePrivate(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".transaction-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func (s *FilesystemWorkspaceStorage) Allocate(ctx context.Context, session string, budget protocol.WorkspaceBudget) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allocate(ctx, session, budget)
}
func (s *FilesystemWorkspaceStorage) Validate(ctx context.Context, session, root string, budget protocol.WorkspaceBudget) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, _, tree, err := s.paths(session)
	if err != nil {
		return err
	}
	if tree != root {
		return errors.New("quota root does not match session")
	}
	restored, err := s.allocate(ctx, session, budget)
	if err != nil {
		return err
	}
	if restored != root {
		return errors.New("quota root changed")
	}
	return nil
}
func (s *FilesystemWorkspaceStorage) Release(ctx context.Context, session, root string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.release(ctx, session, root)
}
