// Card #143: the default session name and the guard that no made-up names remain.
import { describe, expect, it } from 'vitest'
import { defaultSessionName, repoShortName } from './sessionName'

describe('the default session name', () => {
  it('is the repository\'s name, whatever form the repository is named in', () => {
    expect(repoShortName('blerg')).toBe('blerg')
    expect(repoShortName('blerglab/blerg')).toBe('blerg')
    expect(repoShortName('https://github.com/blerglab/blerg.git')).toBe('blerg')
    expect(repoShortName('git@github.com:blerglab/blerg.git')).toBe('blerg')
    expect(defaultSessionName('blerglab/blerg', [])).toBe('blerg')
  })
  it('is "Session" without a repository', () => {
    expect(defaultSessionName('', [])).toBe('Session')
    expect(defaultSessionName(null, ['Session'])).toBe('Session 2')
  })
  it('is numbered past the names running sessions have', () => {
    expect(defaultSessionName('blerg', ['blerg'])).toBe('blerg 2')
    expect(defaultSessionName('blerg', ['blerg', 'blerg 2', 'Blerg 3'])).toBe('blerg 4')
    expect(defaultSessionName('blerg', ['blerg 2'])).toBe('blerg')
  })
})

// No made-up names anywhere in the app: the old word-list generator is gone, and nothing draws a
// name from Math.random.
const sources = import.meta.glob('../**/*.{ts,tsx}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
describe('no random session names remain', () => {
  it('no word list, generator or Math.random name in the app\'s code', () => {
    const offenders = Object.entries(sources)
      .filter(([path]) => !/\.test\.tsx?$/.test(path))
      .filter(([, src]) => /randomSessionName|SESSION_NAMES|sessionNames'|'Granite'|'Obsidian'/.test(src))
      .map(([path]) => path)
    expect(offenders).toEqual([])
    expect(Object.keys(sources).some(p => p.endsWith('/sessionNames.ts'))).toBe(false)
  })
})
