import { describe, expect, it, vi } from 'vitest'
import { CommandRegistry, fuzzyScore, paletteShortcut } from './registry'
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
