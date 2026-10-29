import { describe, expect, it, vi } from 'vitest'
import { CommandRegistry, assertCommandSafety, fuzzyScore, paletteShortcut } from './registry'
const item = (id: string, title = id) => ({ id, title, run: vi.fn() })
describe('command registry', () => {
  it('ranks fuzzy matches, keeps recent matches first, and removes feature registrations', () => {
    const registry = new CommandRegistry()
    const off = registry.register([item('settings', 'Open Settings'), item('sessions', 'Open Sessions')])
    expect(registry.rank(registry.getSnapshot().items, 'opst').map(i => i.id)).toEqual(['settings'])
    registry.remember('settings')
    expect(registry.rank(registry.getSnapshot().items, '').map(i => i.id)[0]).toBe('settings')
    expect(registry.rank(registry.getSnapshot().items, 'Settings').map(i => i.id)).toEqual(['settings'])
    off()
    expect(registry.getSnapshot().items).toEqual([])
    expect(fuzzyScore('src/HelloWorld.tsx', 'hlw')).not.toBeNull()
    expect(fuzzyScore('a.ts', 'zzz')).toBeNull()
  })
  it('updates atomically, retains unchanged results and providers, and discards removed actions', () => {
    const registry = new CommandRegistry(), changed = vi.fn()
    const provider = { id: 'files', mode: 'files' as const, search: async () => ({ items: [] }) }
    const registration = registry.register([item('a'), item('b')], [provider])
    registry.subscribe(changed)
    const snapshot = registry.getSnapshot(), latest = vi.fn()
    registration.update([{ ...item('a'), run: latest }, item('b')], [provider])
    expect(registry.getSnapshot()).toBe(snapshot)
    expect(changed).not.toHaveBeenCalled()
    snapshot.items[0].run()
    expect(latest).toHaveBeenCalledOnce()
    registration.update([item('a', 'Changed')], [provider])
    expect(changed).toHaveBeenCalledOnce()
    expect(registry.getSnapshot().items.map(i => i.title)).toEqual(['Changed'])
    expect(registry.getSnapshot().providers).toBe(snapshot.providers)
    registration()
    snapshot.items[0].run()
    expect(latest).toHaveBeenCalledOnce()
    expect(registry.getSnapshot().items).toEqual([])
  })
  it('notifies subscribers and disposes providers with their owning feature', () => {
    const registry = new CommandRegistry(), changed = vi.fn()
    registry.subscribe(changed)
    const off = registry.register([], [{ id: 'shell', mode: 'commands', search: async () => ({ items: [] }) }])
    expect(registry.getSnapshot().providers).toHaveLength(1)
    off()
    expect(changed).toHaveBeenCalledTimes(2)
    expect(registry.getSnapshot().providers).toEqual([])
  })
})
describe('shortcut ownership', () => {
  const key = (value: string, meta = false) => ({ key: value, ctrlKey: !meta, metaKey: meta, altKey: false, shiftKey: false, isComposing: false, repeat: false })
  it('takes Ctrl and Command K and session Print, preserving Board and non-session Print', () => {
    for (const meta of [false, true]) {
      expect(paletteShortcut(key('K', meta), '/sessions/a')).toBe('commands')
      expect(paletteShortcut(key('p', meta), '/sessions/a')).toBe('files')
      expect(paletteShortcut({ ...key('k', meta), repeat: true }, '/sessions/a')).toBe('commands')
      expect(paletteShortcut({ ...key('p', meta), repeat: true }, '/sessions/a')).toBe('files')
      expect(paletteShortcut(key('p', meta), '/insights')).toBeNull()
      for (const path of ['/boards', '/boards/b', '/boards/b/ticket/c', '/boards/b/archive']) {
        expect(paletteShortcut(key('k', meta), path)).toBeNull()
        expect(paletteShortcut(key('p', meta), path)).toBeNull()
      }
    }
    expect(paletteShortcut({ ...key('/'), ctrlKey: false }, '/sessions/a')).toBeNull()
    expect(paletteShortcut({ ...key('k'), isComposing: true }, '/sessions/a')).toBeNull()
    expect(paletteShortcut({ ...key('k'), shiftKey: true }, '/sessions/a')).toBeNull()
    expect(paletteShortcut({ ...key('k'), altKey: true }, '/sessions/a')).toBeNull()
  })
})

describe('palette relevance and safety', () => {
  it('puts exact and prefix names ahead of recent fuzzy and detail matches', () => {
    const r = new CommandRegistry()
    const rows = [item('fuzzy', 'B long e long r long g'), item('prefix', 'Blerg work'), item('exact', 'blerg')]
    r.remember('fuzzy')
    expect(r.rank(rows, 'blerg').map(i => i.id)).toEqual(['exact', 'prefix', 'fuzzy'])
  })
  it('ranks session name before repo before detail', () => {
    const r = new CommandRegistry()
    const rows = [
      { ...item('detail', 'Other'), group: 'sessions' as const, detail: 'blerg' },
      { ...item('repo', 'Unrelated'), group: 'sessions' as const, repo: 'example-org/blerg' },
      { ...item('name', 'B long l long e long r long g'), group: 'sessions' as const },
    ]
    r.remember('detail')
    expect(r.rank(rows, 'blerg').map(i => i.id)).toEqual(['name', 'repo', 'detail'])
  })
  it.each(['', 'End'])('ignores remembered and timestamp recency between protected commands for %s', query => {
    const r = new CommandRegistry()
    const rows = [
      { ...item('alpha', 'End alpha'), destructive: true, recentAt: '2026-10-01' },
      { ...item('beta', 'End beta'), destructive: true, recentAt: '2026-10-26' },
    ]
    r.remember('beta')
    expect(r.rank(rows, query).map(row => row.id)).toEqual(['alpha', 'beta'])
  })
  it('keeps destructive actions last and ignores their recency', () => {
    const r = new CommandRegistry()
    const rows = [{ ...item('danger', 'End'), destructive: true }, item('safe', 'End overview')]
    r.remember('danger')
    expect(r.rank(rows, 'End').map(i => i.id)).toEqual(['safe', 'danger'])
  })
})

// This checks new registrations as well as the audited real-command integration tests.
describe('explicit action safety', () => {
  it('refuses an unclassified action instead of silently allowing default selection', () => {
    // @ts-expect-error New feature commands must declare their safety at compile time too.
    const unclassified: import('./registry').PaletteCommand = item('grant-access', 'Allow access')
    expect(() => assertCommandSafety([unclassified])).toThrow('must explicitly set destructive')
    expect(() => assertCommandSafety([{ ...unclassified, destructive: true }])).not.toThrow()
    expect(() => assertCommandSafety([{ ...item('help'), destructive: false }])).not.toThrow()
  })
})
