//go:build linux

package shellkeeper

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const stateLimit = 8192

// Identity is provisioned once by the existing installer. The same-UID threat
// boundary is explicit: these files are not a sandbox against the account owner.
type Identity struct {
	Daemon       ed25519.PrivateKey `json:"daemon"`
	Keeper       ed25519.PrivateKey `json:"keeper"`
	DaemonPublic ed25519.PublicKey  `json:"-"`
	KeeperPublic ed25519.PublicKey  `json:"-"`
}

type roleKey struct {
	Key  ed25519.PrivateKey `json:"key"`
	Peer ed25519.PublicKey  `json:"peer"`
}

const RepairInstructions = "Keeper state was refused. Do not delete live custody or regenerate keys. Cancel the failed update, preserve the directory and restore a verified private backup. Only after proving no keeper or shells exist may an operator quarantine incomplete state and rerun shell-keeper init."

func privateDir(dir string) error {
	var st unix.Stat_t
	if err := unix.Lstat(dir, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0777 != 0700 || st.Uid != uint32(os.Getuid()) { //nolint:gosec // getuid is a kernel uid
		return ErrRefused
	}
	return nil
}

func privateFile(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "shell-keeper-state")
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 { //nolint:gosec // kernel uid
		_ = f.Close()
		return nil, ErrRefused
	}
	return f, nil
}

func readPrivate(path string, value any) error {
	f, err := privateFile(path, unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, stateLimit+1))
	if err != nil || len(data) > stateLimit {
		return ErrRefused
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(value) != nil || dec.Decode(new(any)) != io.EOF {
		return ErrRefused
	}
	return nil
}

// Initialize never repairs, rotates or regenerates an existing installation.
// An incomplete first install is refused, including an empty preexisting dir.
func Initialize(dir string) error {
	if err := initialize(dir); err != nil {
		return fmt.Errorf("%w; %s", err, RepairInstructions)
	}
	return nil
}

func initialize(dir string) error {
	return initializeAt(dir, nil)
}
func initializeAt(dir string, checkpoint func(string)) error {
	step := func(name string) {
		if checkpoint != nil {
			checkpoint(name)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil { //nolint:gosec // trusted installer state path, never a peer field
		return refusal("initialize_parent", err)
	}
	if _, err := os.Lstat(dir); err == nil { //nolint:gosec // trusted installer state path, validated before use
		_, err = LoadIdentity(dir)
		if err != nil {
			return err
		}
		_, err = loadEpoch(dir)
		return err
	} else if !os.IsNotExist(err) {
		return refusal("initialize_directory", err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(dir), ".keeper-init-")
	if err != nil {
		return refusal("initialize_stage", err)
	}
	defer func() { _ = os.RemoveAll(stage) }() //nolint:gosec // only the fresh private staging directory created above
	step("stage")
	_, daemon, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return refusal("initialize_daemon_key", err)
	}
	_, keeper, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return refusal("initialize_keeper_key", err)
	}
	if err = writePrivate(stage, "daemon-key.json", roleKey{Key: daemon, Peer: publicKey(keeper)}, false); err != nil {
		return refusal("initialize_identity", err)
	}
	step("daemon_key")
	if err = writePrivate(stage, "keeper-key.json", roleKey{Key: keeper, Peer: publicKey(daemon)}, false); err != nil {
		return refusal("initialize_identity", err)
	}
	step("keeper_key")
	id, err := nonce()
	if err != nil {
		return refusal("initialize_nonce", err)
	}
	e := Epoch{Number: 1, ID: id}
	e.Signature = sign(daemon, "epoch", epochBody(e))
	if err = writePrivate(stage, "epoch.json", e, false); err != nil {
		return refusal("initialize_epoch", err)
	}
	step("epoch")
	if err = unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, dir, unix.RENAME_NOREPLACE); err != nil {
		return refusal("initialize_publish", err)
	}
	step("published")
	if err = syncDirectory(filepath.Dir(dir)); err != nil {
		return refusal("initialize_sync", err)
	}
	return nil
}

func LoadIdentity(dir string) (Identity, error) {
	d, err := loadRole(dir, "daemon-key.json")
	if err != nil {
		return Identity{}, err
	}
	k, err := loadRole(dir, "keeper-key.json")
	if err != nil {
		return Identity{}, err
	}
	if !bytes.Equal(publicKey(d.Key), k.Peer) || !bytes.Equal(publicKey(k.Key), d.Peer) {
		return Identity{}, refusal("identity_pins", ErrRefused)
	}
	return Identity{Daemon: d.Key, Keeper: k.Key, DaemonPublic: k.Peer, KeeperPublic: d.Peer}, nil
}

func loadRole(dir, name string) (roleKey, error) {
	var identity roleKey
	if err := privateDir(dir); err != nil {
		return identity, refusal("identity_directory", err)
	}
	if err := readPrivate(filepath.Join(dir, name), &identity); err != nil {
		return roleKey{}, refusal("identity_read", err)
	}
	for _, key := range []ed25519.PrivateKey{identity.Key} {
		if len(key) != ed25519.PrivateKeySize || !bytes.Equal(key, ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])) {
			return roleKey{}, refusal("identity_key", ErrRefused)
		}
	}
	if len(identity.Peer) != ed25519.PublicKeySize {
		return roleKey{}, refusal("identity_peer", ErrRefused)
	}
	return identity, nil
}

func loadDaemon(dir string) (Identity, error) {
	r, err := loadRole(dir, "daemon-key.json")
	return Identity{Daemon: r.Key, DaemonPublic: publicKey(r.Key), KeeperPublic: r.Peer}, err
}
func loadKeeper(dir string) (Identity, error) {
	r, err := loadRole(dir, "keeper-key.json")
	return Identity{Keeper: r.Key, KeeperPublic: publicKey(r.Key), DaemonPublic: r.Peer}, err
}

func loadEpoch(dir string) (Epoch, error) {
	i, err := loadDaemon(dir)
	if err != nil {
		return Epoch{}, err
	}
	return loadEpochWithKey(dir, i.DaemonPublic)
}

func loadEpochWithKey(dir string, public ed25519.PublicKey) (Epoch, error) {
	var e Epoch
	if err := readPrivate(filepath.Join(dir, "epoch.json"), &e); err != nil {
		return Epoch{}, refusal("epoch_read", err)
	}
	if !validEpoch(public, e) {
		return Epoch{}, refusal("epoch_signature", ErrRefused)
	}
	return e, nil
}

// DaemonAuthority keeps singleton ownership until Close. Never release it while
// this daemon may admit writers or collect workspaces.
type DaemonAuthority struct {
	Epoch Epoch
	lock  *os.File
}

func AcquireDaemon(dir string) (*DaemonAuthority, error) {
	i, err := loadDaemon(dir)
	if err != nil {
		return nil, err
	}
	lock, err := acquireLock(dir, "daemon.lock")
	if err != nil {
		return nil, err
	}
	e, err := loadEpoch(dir)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	if e.Number == math.MaxUint64 {
		_ = lock.Close()
		return nil, refusal("epoch_exhausted", ErrRefused)
	}
	id, err := nonce()
	if err != nil {
		_ = lock.Close()
		return nil, refusal("epoch_nonce", err)
	}
	e.Number++
	e.ID = id
	e.Signature = sign(i.Daemon, "epoch", epochBody(e))
	if err = writePrivate(dir, "epoch.json", e, true); err != nil {
		_ = lock.Close()
		return nil, refusal("epoch_persist", err)
	}
	return &DaemonAuthority{Epoch: e, lock: lock}, nil
}

func (a *DaemonAuthority) Close() error { return a.lock.Close() }

func acquireLock(dir, name string) (*os.File, error) {
	return acquireLockAfter(dir, name, nil)
}

func acquireLockAfter(dir, name string, after func(*os.File)) (*os.File, error) {
	if err := privateDir(dir); err != nil {
		return nil, refusal("lock_directory", err)
	}
	f, err := privateFile(filepath.Join(dir, name), unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, refusal("lock_open", err)
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, refusal("lock_held", err)
	}
	if after != nil {
		after(f)
	}
	if err = sameLock(f, filepath.Join(dir, name)); err != nil {
		_ = f.Close()
		return nil, refusal("lock_replaced", err)
	}
	return f, nil
}

func sameLock(f *os.File, path string) error {
	var held, named unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &held); err != nil {
		return err
	}
	if err := unix.Lstat(path, &named); err != nil {
		return err
	}
	if held.Dev != named.Dev || held.Ino != named.Ino || held.Nlink != 1 || named.Mode&unix.S_IFMT != unix.S_IFREG {
		return ErrRefused
	}
	return nil
}

func writePrivate(dir, name string, value any, replace bool) error {
	return writePrivateAt(dir, name, value, replace, nil)
}
func writePrivateAt(dir, name string, value any, replace bool, afterPublish func()) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".keeper-state-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }() //nolint:gosec // only the fresh state temporary created above
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if replace {
		err = os.Rename(f.Name(), filepath.Join(dir, name)) //nolint:gosec // private state directory and fixed developer-owned state basename
	} else {
		err = unix.Renameat2(unix.AT_FDCWD, f.Name(), unix.AT_FDCWD, filepath.Join(dir, name), unix.RENAME_NOREPLACE)
	}
	if err != nil {
		return err
	}
	if afterPublish != nil {
		afterPublish()
	}
	return syncDirectory(dir)
}

func syncDirectory(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // trusted private install-state directory, fsynced for durable epochs
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
