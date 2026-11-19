package livedata

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalAuthorityState(t *testing.T) {
	root := t.TempDir()
	first, e := StartAuthority(root, true)
	if e != nil {
		t.Fatal(e)
	}
	if first.Epoch() != 1 {
		t.Fatal("initial epoch unavailable")
	}
	_, oldKey := first.Keys.auditIdentity()
	second, e := FenceAuthority(root)
	if e != nil {
		t.Fatal(e)
	}
	if second.Epoch() != 2 || first.Epoch() != 0 {
		t.Fatal("restart did not fence old process")
	}
	_, newKey := second.Keys.auditIdentity()
	if oldKey == newKey {
		t.Fatal("epoch not advanced")
	}
	a, _ := first.Keys.auditIdentity()
	b, _ := second.Keys.auditIdentity()
	if bytes.Equal(first.boot.Private, second.boot.Private) {
		t.Fatal("signing key reused across epochs")
	}
	if a == b {
		t.Fatal("key not rotated")
	}
	if e = os.WriteFile(filepath.Join(root, "authority.json"), []byte("broken"), 0600); e != nil {
		t.Fatal(e)
	}
	if second.Epoch() != 0 {
		t.Fatal("corrupt external state accepted")
	}
	if _, e = StartAuthority(root, true); e == nil {
		t.Fatal("corrupt authority replaced")
	}
}

func TestExternalAuthorityMissingStateRefusesReset(t *testing.T) {
	root := t.TempDir()
	state, e := StartAuthority(root, true)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(root, "authority.json")); e != nil {
		t.Fatal(e)
	}
	if state.Epoch() != 0 {
		t.Fatal("missing authority used cached positive epoch")
	}
	if _, e = StartAuthority(root, true); e == nil {
		t.Fatal("lost epoch silently reset")
	}
}

func TestExternalAuthorityRestartPreservesApprovalEpoch(t *testing.T) {
	root := t.TempDir()
	first, err := StartAuthority(root, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := StartAuthority(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Epoch() != 1 || second.Epoch() != 1 || !bytes.Equal(first.boot.Private, second.boot.Private) || first.boot.KeyID != second.boot.KeyID {
		t.Fatal("ordinary restart changed authority")
	}
}
