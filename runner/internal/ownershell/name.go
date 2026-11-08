package ownershell

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NormalizeName bounds presentation metadata; names never enter grants or audit records.
func NormalizeName(name string) (string, error) {
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 120 || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) }) {
		return "", ErrDenied
	}
	return strings.TrimSpace(name), nil
}

// Rename uses the same live owner/login authorization as terminal control. Persistence,
// when supplied, completes before the in-memory name changes.
func (m *Manager) Rename(ctx context.Context, a Actor, handle, name string, persist ...func(context.Context, Info) error) (Info, error) {
	name, err := NormalizeName(name)
	if err != nil {
		return Info{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, release, err := m.lockHandle(ctx, a, handle)
	if err != nil {
		return Info{}, err
	}
	defer release()
	s, err := m.live(ctx, a, handle)
	if err != nil {
		return Info{}, err
	}
	info := m.info(s)
	info.Name = name
	for _, save := range persist {
		if err := m.invoke(ctx, s, func(call context.Context) error { return save(call, info) }); err != nil {
			// Revocation can finish while a persistence call is in flight. Delete
			// again after that call returns so a late commit cannot retain the name.
			if m.shells[s.handle] != s && m.nameCleanup != nil {
				cleanup := m.nameCleanup
				_ = m.external(context.WithoutCancel(ctx), func(call context.Context) error {
					cleanup(call, s.actor, info.Session, info.Shell)
					return nil
				})
			}
			return Info{}, ErrUnknown
		}
	}
	s.name = name
	return info, nil
}

// SetNameCleanup installs the presentation-store cleanup used by every close path.
// The callback runs outside the manager lock and must honor its deadline.
func (m *Manager) SetNameCleanup(cleanup func(context.Context, Actor, string, string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nameCleanup = cleanup
}
