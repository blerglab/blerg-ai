//go:build linux

package shellruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistentIdentityIgnoresDisplayHostnameFailure(t *testing.T) {
	called := false
	label := func() (string, error) { called = true; return "", errors.New("display label unavailable") }
	identity, err := resolveAccountIdentityMode(t.TempDir(), os.Geteuid(), true, label)
	if err != nil {
		t.Fatal("display label blocked persistent identity", err)
	}
	if !called || identity.Machine != "Desktop" || identity.MemberRoot.Inode == 0 {
		t.Fatal("display label became custody authority")
	}
	if _, err = resolveAccountIdentityMode(t.TempDir(), os.Geteuid(), false, label); err == nil || !called {
		t.Fatal("legacy label-check behavior changed")
	}
}

func TestPersistentIdentityDetectsReplacedMemberWithSamePath(t *testing.T) {
	root := t.TempDir()
	member := filepath.Join(root, "member")
	if err := os.Mkdir(member, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := ResolvePersistentIdentity(member)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(member, member+"-old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(member, 0700); err != nil {
		t.Fatal(err)
	}
	after, err := ResolvePersistentIdentity(member)
	if err != nil {
		t.Fatal(err)
	}
	if before.Cwd != after.Cwd || RuntimeIdentityDigest(before) == RuntimeIdentityDigest(after) {
		t.Fatal("member replacement retained old custody identity")
	}
}

func TestPersistentCwdDescriptorMustMatchTheConsentedMember(t *testing.T) {
	root := t.TempDir()
	member := filepath.Join(root, "member")
	if err := os.Mkdir(member, 0700); err != nil {
		t.Fatal(err)
	}
	identity, err := ResolvePersistentIdentity(member)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity, err := persistentMemberRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(member, member+"-old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(member, 0700); err != nil {
		t.Fatal(err)
	}
	process, err := startTraced(identity, rootIdentity, root)
	if process != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = process.stop(ctx)
	}
	var stage *startError
	if !errors.As(err, &stage) || stage.stage != "verify_cwd" {
		t.Fatal("member replacement reached process creation", err)
	}
}

func TestPersistentIdentityKeepsDisplayNameOutsideDigest(t *testing.T) {
	identity, err := resolveAccountIdentityMode(t.TempDir(), os.Geteuid(), true, func() (string, error) { return "Example desktop", nil })
	if err != nil {
		t.Fatal(err)
	}
	if identity.Machine != "Example desktop" {
		t.Fatal("consent machine label is blank")
	}
	before := RuntimeIdentityDigest(identity)
	identity.Machine = "Renamed desktop"
	if RuntimeIdentityDigest(identity) != before {
		t.Fatal("display name became authority")
	}
}
