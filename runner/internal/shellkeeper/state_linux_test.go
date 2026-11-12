//go:build linux

package shellkeeper

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func provision(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "keeper")
	if err := Initialize(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestInitializePreservesIdentityAndRefusesPartialInstall(t *testing.T) {
	dir := provision(t)
	i, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = Initialize(dir); err != nil {
		t.Fatal(err)
	}
	again, err := LoadIdentity(dir)
	if err != nil || !bytes.Equal(i.Daemon, again.Daemon) || !bytes.Equal(i.Keeper, again.Keeper) {
		t.Fatal("reinstall replaced identity")
	}
	if err = os.Remove(filepath.Join(dir, "daemon-key.json")); err != nil {
		t.Fatal(err)
	}
	if Initialize(dir) == nil {
		t.Fatal("missing identity was regenerated")
	}
	if _, err = os.Lstat(filepath.Join(dir, "daemon-key.json")); !os.IsNotExist(err) {
		t.Fatal("partial installation changed")
	}
}

func TestDaemonSingletonAndDurableMonotonicEpoch(t *testing.T) {
	dir := provision(t)
	first, err := AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	if second, err := AcquireDaemon(dir); err == nil {
		_ = second.Close()
		t.Fatal("second daemon acquired authority")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	if second.Epoch.Number != first.Epoch.Number+1 || second.Epoch.ID == first.Epoch.ID {
		t.Fatal("epoch did not advance durably")
	}
}

func TestPrivateStateRefusesUnsafeObjects(t *testing.T) {
	for _, name := range []string{"wide_directory", "wide_key", "symlink_key", "hardlink_key", "corrupt_key", "corrupt_epoch", "missing_epoch", "symlink_lock", "wide_lock"} {
		t.Run(name, func(t *testing.T) {
			dir := provision(t)
			key := filepath.Join(dir, "daemon-key.json")
			lock := filepath.Join(dir, "daemon.lock")
			var err error
			switch name {
			case "wide_directory":
				err = os.Chmod(dir, 0755)
			case "wide_key":
				err = os.Chmod(key, 0644)
			case "symlink_key":
				err = os.Rename(key, key+".original")
				if err == nil {
					err = os.Symlink(key+".original", key)
				}
			case "hardlink_key":
				err = os.Link(key, key+".alias")
			case "corrupt_key":
				err = os.WriteFile(key, []byte("fixture secret invalid key"), 0600)
			case "corrupt_epoch":
				err = os.WriteFile(filepath.Join(dir, "epoch.json"), []byte(`{"number":0}`), 0600)
			case "missing_epoch":
				err = os.Remove(filepath.Join(dir, "epoch.json"))
			case "symlink_lock":
				err = os.Symlink(key, lock)
			case "wide_lock":
				err = os.WriteFile(lock, nil, 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if a, err := AcquireDaemon(dir); err == nil {
				_ = a.Close()
				t.Fatal("unsafe state admitted")
			}
		})
	}
}
