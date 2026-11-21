package livedata

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"sync/atomic"
	"syscall"
)

// AuthorityState is independent of Postgres snapshots. Ordinary starts reuse
// the persisted key and epoch. Explicit restore/compromise fencing rotates both.
// Never restore this directory with a database snapshot.
type AuthorityState struct {
	fenced atomic.Bool
	dir    string
	boot   authorityFile
	Keys   *Keyring
}
type authorityFile struct {
	Epoch       int64  `json:"epoch"`
	Revocations int64  `json:"revocations"`
	KeyID       string `json:"key_id"`
	Private     []byte `json:"private"`
}

func StartAuthority(dir string, allowInitial bool) (*AuthorityState, error) {
	return startAuthority(dir, allowInitial, false)
}

// FenceAuthority is an operator-only restore/compromise action. It cannot
// initialize lost state, and has no public or agent-accessible HTTP route.
func FenceAuthority(dir string) (*AuthorityState, error) {
	return startAuthority(dir, false, true)
}

func startAuthority(dir string, allowInitial, fence bool) (*AuthorityState, error) {
	root, e := os.OpenRoot(dir)
	if e != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = root.Close() }()
	lock, e := root.OpenFile("authority.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	fresh := e == nil
	if os.IsExist(e) {
		lock, e = root.OpenFile("authority.lock", os.O_RDWR, 0600)
	}
	if e != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = lock.Close() }()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	previous, e := readAuthority(dir)
	if os.IsNotExist(e) && fresh && allowInitial {
		previous = authorityFile{}
	} else if e != nil {
		return nil, ErrUnavailable
	}
	if previous.Epoch > 0 && !fence {
		return &AuthorityState{dir: dir, boot: previous, Keys: authorityKeys(previous)}, nil
	}
	if previous.Epoch == math.MaxInt64 {
		return nil, ErrUnavailable
	}
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return nil, ErrUnavailable
	}
	var id [16]byte
	if _, e = rand.Read(id[:]); e != nil {
		return nil, ErrUnavailable
	}
	next := authorityFile{Epoch: previous.Epoch + 1, KeyID: hex.EncodeToString(id[:]), Private: key, Revocations: previous.Revocations}
	if e := writeAuthority(root, next); e != nil {
		return nil, e
	}
	keys := &Keyring{epoch: next.Epoch, active: next.KeyID, private: key, previous: map[string]retiredKey{}}
	return &AuthorityState{dir: dir, boot: next, Keys: keys}, nil
}
func readAuthority(dir string) (authorityFile, error) {
	root, e := os.OpenRoot(dir)
	if e != nil {
		return authorityFile{}, e
	}
	defer func() { _ = root.Close() }()
	f, e := root.Open("authority.json")
	if e != nil {
		return authorityFile{}, e
	}
	defer func() { _ = f.Close() }()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return authorityFile{}, ErrUnavailable
	}
	raw, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil || len(raw) > 4096 {
		return authorityFile{}, ErrUnavailable
	}
	var out authorityFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || out.Epoch < 1 || out.Revocations < 0 || !keyID.MatchString(out.KeyID) || !validAuthority(out) {
		return authorityFile{}, ErrUnavailable
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return authorityFile{}, ErrUnavailable
	}
	return out, nil
}
func (s *AuthorityState) Epoch() int64 {
	if s.fenced.Load() {
		return 0
	}
	current, e := readAuthority(s.dir)
	if e != nil {
		return 0
	}
	if current.Epoch != s.boot.Epoch || current.KeyID != s.boot.KeyID || !bytes.Equal(current.Private, s.boot.Private) {
		s.fenced.Store(true)
		return 0
	}
	return current.Epoch
}

func validAuthority(state authorityFile) bool {
	return len(state.Private) == ed25519.PrivateKeySize && bytes.Equal(ed25519.NewKeyFromSeed(state.Private[:ed25519.SeedSize]), state.Private)
}

func authorityKeys(state authorityFile) *Keyring {
	return &Keyring{epoch: state.Epoch, active: state.KeyID, private: append(ed25519.PrivateKey(nil), state.Private...), previous: map[string]retiredKey{}}
}

func writeAuthority(root *os.Root, next authorityFile) error {
	raw, e := json.Marshal(next)
	if e != nil {
		return ErrUnavailable
	}
	temporary := ".authority-" + next.KeyID
	file, e := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return ErrUnavailable
	}
	defer func() { _ = file.Close(); _ = root.Remove(temporary) }()
	if file.Chmod(0600) != nil {
		return ErrUnavailable
	}
	if _, e = file.Write(raw); e != nil {
		return ErrUnavailable
	}
	if file.Sync() != nil {
		return ErrUnavailable
	}
	if file.Close() != nil {
		return ErrUnavailable
	}
	if root.Rename(temporary, "authority.json") != nil {
		return ErrUnavailable
	}
	parent, e := root.Open(".")
	if e != nil {
		return ErrUnavailable
	}
	defer func() { _ = parent.Close() }()
	if parent.Sync() != nil {
		return ErrUnavailable
	}
	return nil
}
