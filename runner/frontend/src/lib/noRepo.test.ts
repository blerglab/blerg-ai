import { describe, it, expect, vi, afterEach } from 'vitest'
import { isNoRepo, repoLabel, repoDetail, validScratchSuffix, newScratchSuffix, SCRATCH_PREFIX } from './noRepo'

vi.mock('../sessionNames', () => ({ randomSessionName: () => 'Granite' }))

describe('noRepo', () => {
  afterEach(() => vi.restoreAllMocks())

  it('reads an empty repo (cluster) and a scratch folder (daemon) as no repository', () => {
    expect(isNoRepo('')).toBe(true)
    expect(isNoRepo(undefined)).toBe(true)
    expect(isNoRepo('.scratch-granite-8000')).toBe(true)
    expect(isNoRepo('widget')).toBe(false)
    expect(isNoRepo('org/widget')).toBe(false)
  })

  it('labels a no-repo session clearly, and keeps real repos as they are', () => {
    expect(repoLabel('')).toBe('No repository')
    expect(repoLabel('.scratch-granite-8000')).toBe('No repository')
    expect(repoLabel('org/widget')).toBe('org/widget')
    expect(repoDetail('')).toBe('No repository')
    expect(repoDetail('.scratch-granite-8000')).toBe('No repository · scratch folder .scratch-granite-8000')
    expect(repoDetail('widget')).toBe('widget')
  })

  it('accepts only what the server accepts after the prefix', () => {
    for (const ok of ['granite-8000', 'A1_b-2', 'x', 'a'.repeat(64)]) expect(validScratchSuffix(ok)).toBe(true)
    for (const bad of ['', '-x', '_x', 'a/b', '../x', 'a b', 'a.b', 'a'.repeat(65), 'é']) {
      expect(validScratchSuffix(bad)).toBe(false)
    }
  })

  it('generates a valid, readable suffix with 64 bits from crypto.getRandomValues, never Math.random', () => {
    const mathRandom = vi.spyOn(Math, 'random')
    const getRandom = vi.spyOn(crypto, 'getRandomValues')
    const s = newScratchSuffix()
    expect(s).toMatch(/^granite-[0-9a-f]{16}$/)
    expect(validScratchSuffix(s)).toBe(true)
    expect(isNoRepo(SCRATCH_PREFIX + s)).toBe(true)
    expect(getRandom).toHaveBeenCalledTimes(1)
    expect((getRandom.mock.calls[0][0] as Uint8Array).length).toBe(8)
    expect(mathRandom).not.toHaveBeenCalled()
    expect(newScratchSuffix()).not.toBe(s)
  })
})
