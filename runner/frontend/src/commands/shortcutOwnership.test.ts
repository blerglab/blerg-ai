import { afterEach, expect, it, vi } from 'vitest'
import { shellPaletteShortcut, tabFocusShortcut } from './shortcutOwnership'

afterEach(() => vi.restoreAllMocks())
it.each(['Linux x86_64', 'Win32', 'MacIntel'])('reserves only the platform tab focus binding in a shell on %s', platform => {
  vi.spyOn(navigator, 'platform', 'get').mockReturnValue(platform)
  const mac = platform === 'MacIntel'
  const key = { key: 'Enter', ctrlKey: !mac, metaKey: mac, altKey: false, shiftKey: true }
  expect(tabFocusShortcut(key)).toBe(true)
  expect(shellPaletteShortcut(key)).toBe(true)
  for (const other of [{ ...key, shiftKey: false }, { ...key, altKey: true }, { ...key, ctrlKey: mac, metaKey: !mac }, { ...key, ctrlKey: true, metaKey: true }]) {
    expect(tabFocusShortcut(other)).toBe(false)
    expect(shellPaletteShortcut(other)).toBe(false)
  }
})
