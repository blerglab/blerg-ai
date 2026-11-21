package livedata

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"syscall"
)

// RevocationAuthority is independent of database backups. Advance is durable
// before it returns and cannot lower the mark or cross an authority fence.
type RevocationAuthority interface {
	RevocationMark(context.Context) (int64, error)
	AdvanceRevocations(context.Context, int64) error
}

func sameAuthority(a, b authorityFile) bool {
	return a.Epoch == b.Epoch && a.KeyID == b.KeyID && bytes.Equal(a.Private, b.Private)
}
func (s *AuthorityState) RevocationMark(context.Context) (int64, error) {
	if s.fenced.Load() {
		return 0, ErrUnavailable
	}
	state, e := readAuthority(s.dir)
	if e != nil {
		return 0, ErrUnavailable
	}
	if !sameAuthority(state, s.boot) || state.Revocations < s.boot.Revocations {
		s.fenced.Store(true)
		return 0, ErrUnavailable
	}
	return state.Revocations, nil
}
func (s *AuthorityState) AdvanceRevocations(ctx context.Context, mark int64) error {
	if ctx.Err() != nil || mark < 0 || s.fenced.Load() {
		return ErrUnavailable
	}
	root, e := os.OpenRoot(s.dir)
	if e != nil {
		return ErrUnavailable
	}
	defer func() { _ = root.Close() }()
	lock, e := root.OpenFile("authority.lock", os.O_RDWR, 0600)
	if e != nil {
		return ErrUnavailable
	}
	defer func() { _ = lock.Close() }()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		return ErrUnavailable
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	state, e := readAuthority(s.dir)
	if e != nil {
		return ErrUnavailable
	}
	if !sameAuthority(state, s.boot) || state.Revocations < s.boot.Revocations {
		s.fenced.Store(true)
		return ErrUnavailable
	}
	if mark <= state.Revocations {
		return nil
	}
	state.Revocations = mark
	return writeAuthority(root, state)
}
func (s *SecretAuthorityState) RevocationMark(ctx context.Context) (int64, error) {
	if s.fenced.Load() {
		return 0, ErrUnavailable
	}
	record, e := s.backend.request(ctx, "GET", nil)
	if e != nil {
		return 0, e
	}
	var state authorityFile
	if json.Unmarshal(record.Data["authority.json"], &state) != nil {
		return 0, ErrUnavailable
	}
	if !sameAuthority(state, s.boot) || state.Revocations < s.boot.Revocations {
		s.fenced.Store(true)
		return 0, ErrUnavailable
	}
	return state.Revocations, nil
}
func (s *SecretAuthorityState) AdvanceRevocations(ctx context.Context, mark int64) error {
	if mark < 0 || s.fenced.Load() {
		return ErrUnavailable
	}
	record, e := s.backend.request(ctx, "GET", nil)
	if e != nil {
		return e
	}
	var state authorityFile
	if json.Unmarshal(record.Data["authority.json"], &state) != nil {
		return ErrUnavailable
	}
	if !sameAuthority(state, s.boot) || state.Revocations < s.boot.Revocations {
		s.fenced.Store(true)
		return ErrUnavailable
	}
	if mark <= state.Revocations {
		return nil
	}
	state.Revocations = mark
	raw, e := json.Marshal(state)
	if e != nil {
		return ErrUnavailable
	}
	record.Data["authority.json"] = raw
	_, e = s.backend.request(ctx, "PUT", record)
	return e
}
