import { afterEach, describe, expect, it } from 'vitest'
import { isWrapShortcut, lineWrap, resetLineWrap, setLineWrap, wrapKind, wrapKindOf } from './lineWrap'

afterEach(() => { resetLineWrap(); localStorage.clear() })

describe('line wrap (#265)', () => {
  it.each([
    ['notes.md', 'prose'], ['docs/guide.markdown', 'prose'], ['todo.txt', 'prose'], ['index.rst', 'prose'],
    ['README', 'prose'], ['CHANGELOG.md', 'prose'], ['LICENSE', 'prose'], ['LICENSE-MIT', 'prose'], ['COPYING.LESSER', 'prose'], ['pkg/CONTRIBUTING', 'prose'],
    ['main.ts', 'code'], ['Makefile', 'code'], ['CHANGES.patch', 'code'], ['license.ts', 'code'], ['readme.go', 'code'], ['VERSION', 'code'],
  ])('%s reads as %s', (name, kind) => expect(wrapKind(name)).toBe(kind))

  it('a diff is prose only when all its files are', () => {
    expect(wrapKindOf(['README.md', 'docs/a.md'])).toBe('prose')
    expect(wrapKindOf(['README.md', 'src/a.ts'])).toBe('code')
    expect(wrapKindOf([])).toBe('code')
  })

  it('defaults on for prose and off for code, and keeps a choice per account and kind', () => {
    expect(lineWrap('prose', 'account-a')).toBe(true)
    expect(lineWrap('code', 'account-a')).toBe(false)
    setLineWrap('code', true, 'account-a')
    setLineWrap('prose', false, 'account-a')
    expect(lineWrap('code', 'account-a')).toBe(true)
    expect(lineWrap('prose', 'account-a')).toBe(false)
    // Another account keeps the defaults.
    expect(lineWrap('code', 'account-b')).toBe(false)
    expect(lineWrap('prose', 'account-b')).toBe(true)
  })

  it('is Alt+Z, never while typing', () => {
    const key = (init: Partial<{ altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean; code: string; key: string; target: EventTarget | null }>) =>
      isWrapShortcut({ altKey: false, ctrlKey: false, metaKey: false, shiftKey: false, code: 'KeyZ', key: 'z', target: document.body, ...init })
    expect(key({ altKey: true })).toBe(true)
    expect(key({ altKey: true, key: 'Ω' })).toBe(true) // Option+Z on a Mac
    expect(key({})).toBe(false)
    expect(key({ altKey: true, ctrlKey: true })).toBe(false)
    expect(key({ altKey: true, target: document.createElement('textarea') })).toBe(false)
  })
})
