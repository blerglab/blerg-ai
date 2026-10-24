package daemon

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Docker's default profile (Apache-2.0, moby/profiles@4b8214fc4971e06172938a1f1554ccff6647ee80/seccomp/default.json)
// plus EXDEV for link/linkat. Keep all existing default restrictions. EXDEV
// allows Git's atomic object-write rename fallback without permitting aliases.
//
//go:embed workspace-seccomp.json
var workspaceEntryProfile []byte

func workspaceEntrySecurityOption() (string, error) {
	path := filepath.Join(sandboxCloneRoot(), "checkout-seccomp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if old, err := os.ReadFile(path); err == nil { //nolint:gosec // fixed daemon-private policy path, checked against embedded bytes
		if !bytes.Equal(old, workspaceEntryProfile) {
			return "", errors.New("entry policy changed")
		}
	} else if os.IsNotExist(err) {
		if err := writeWorkspacePrivate(path, workspaceEntryProfile); err != nil {
			return "", err
		}
	} else {
		return "", err
	}
	return "seccomp=" + path, nil
}

func workspaceEntryPolicyMatches(options []string) bool {
	var expected any
	if json.Unmarshal(workspaceEntryProfile, &expected) != nil {
		return false
	}
	canonical, _ := json.Marshal(expected)
	for _, option := range options {
		if !strings.HasPrefix(option, "seccomp=") {
			continue
		}
		var actual any
		if json.Unmarshal([]byte(strings.TrimPrefix(option, "seccomp=")), &actual) != nil {
			continue
		}
		encoded, _ := json.Marshal(actual)
		if bytes.Equal(encoded, canonical) {
			return true
		}
	}
	return false
}

func (d *DockerWorkspaceContainer) validateEntryAuthority(ctx context.Context) error {
	raw, err := d.command(ctx, "inspect", "--format", "{{json .HostConfig.SecurityOpt}}", d.ID)
	var options []string
	if err != nil || json.Unmarshal(raw, &options) != nil || !workspaceEntryPolicyMatches(options) {
		return errors.New("runtime lacks per-write checkout entry authority")
	}
	return nil
}

func workspaceWorkerEntryBound() error {
	f, err := os.CreateTemp("", "entry-authority-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(name); _ = os.Remove(name + "-alias") }()
	if err := os.Link(name, name+"-alias"); !errors.Is(err, syscall.EXDEV) {
		return errors.New("per-write hardlink authority unavailable")
	}
	return nil
}
