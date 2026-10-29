import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { EXPLORER_PAGE_CAP, VIEW_CACHE_SESSIONS, capPages, pageTotal, viewEpoch, viewWriter, cachedViewSessions, clearViews, dropView, readView, resetViewCacheForTest, syncViewCacheSessions, writeView } from './sessionViewCache'

const ids = (n: number) => Array.from({ length: n }, (_, i) => `s${i + 1}`)
const live = (list: string[]) => syncViewCacheSessions(list.map(id => ({ id, status: 'idle' })))

describe('sessionViewCache', () => {
  beforeEach(() => live(ids(20)))
  afterEach(() => resetViewCacheForTest())

  it('keeps the last ten sessions viewed and evicts the least recently viewed at the eleventh', () => {
    expect(VIEW_CACHE_SESSIONS).toBe(10)
    for (const id of ids(10)) writeView('a', id, 'explorer', id)
    // Viewing s1 again makes s2 the least recently viewed.
    expect(readView('a', 's1', 'explorer')).toBe('s1')
    writeView('a', 's11', 'explorer', 's11')
    expect(cachedViewSessions()).toHaveLength(10)
    expect(readView('a', 's2', 'explorer')).toBeUndefined()
    expect(readView('a', 's1', 'explorer')).toBe('s1')
    expect(readView('a', 's11', 'explorer')).toBe('s11')
  })

  it('never shows one account what another read', () => {
    writeView('a', 's1', 'permissions', { secret: 1 })
    expect(readView('b', 's1', 'permissions')).toBeUndefined()
    // Reading under b forgot everything a had: going back to a finds nothing either.
    expect(readView('a', 's1', 'permissions')).toBeUndefined()
    // A reply a asked for, landing after b has read, is not kept.
    readView('b', 's2', 'permissions')
    writeView('a', 's2', 'permissions', { late: true })
    expect(readView('b', 's2', 'permissions')).toBeUndefined()
    expect(cachedViewSessions()).toEqual([])
  })

  it('forgets everything on an identity or desktop scope change', () => {
    writeView('a', 's1', 'explorer', 1)
    window.dispatchEvent(new Event('blerg-identity-changed'))
    expect(cachedViewSessions()).toEqual([])
    writeView('a', 's1', 'explorer', 1)
    window.dispatchEvent(new Event('blerg-desktop-scope-changed'))
    expect(cachedViewSessions()).toEqual([])
  })

  it('drops a session that ended, was archived or is gone, and keeps nothing for one that is not live', () => {
    for (const id of ['s1', 's2', 's3', 's4']) writeView('a', id, 'explorer', id)
    syncViewCacheSessions([
      { id: 's1', status: 'ended' }, { id: 's2', status: 'idle', archived_at: '2026-10-27T00:00:00Z' },
      { id: 's4', status: 'running' }, { id: 's5', status: 'stopped' }, { id: 's6', status: 'error' },
    ])
    expect(cachedViewSessions()).toEqual(['s4'])
    writeView('a', 's5', 'explorer', 5)
    writeView('a', 's3', 'explorer', 3)
    expect(cachedViewSessions()).toEqual(['s4'])
  })

  it('keeps nothing until the session store has said which sessions are live', () => {
    resetViewCacheForTest()
    writeView('a', 's1', 'explorer', 1)
    expect(readView('a', 's1', 'explorer')).toBeUndefined()
  })

  it('keeps nothing without a signed-in account', () => {
    writeView('', 's1', 'explorer', 1)
    expect(cachedViewSessions()).toEqual([])
  })

  it('drops one slot or a whole session', () => {
    writeView('a', 's1', 'explorer', 1)
    writeView('a', 's1', 'topics', 2)
    dropView('s1', 'explorer')
    expect(readView('a', 's1', 'explorer')).toBeUndefined()
    expect(readView('a', 's1', 'topics')).toBe(2)
    dropView('s1')
    expect(cachedViewSessions()).toEqual([])
    writeView('a', 's1', 'explorer', 1)
    clearViews()
    expect(cachedViewSessions()).toEqual([])
  })

  it('caps the explorer pages kept per session, the open folders first', () => {
    const pages = Object.fromEntries(Array.from({ length: EXPLORER_PAGE_CAP + 50 }, (_, i) => [`dir${i}`, { loaded: 1 }]))
    const kept = capPages({ ...pages, '': { loaded: 1 }, deep: {} }, ['', 'deep', 'missing'])
    expect(pageTotal(kept)).toBe(EXPLORER_PAGE_CAP)
    expect(Object.keys(kept).slice(0, 2)).toEqual(['', 'deep'])
    // The others most recently read first: the last ones read stay.
    expect(kept[`dir${EXPLORER_PAGE_CAP + 49}`]).toBeTruthy()
    expect(kept.dir0).toBeUndefined()
    expect(capPages({ a: { loaded: 3 } }, [])).toEqual({ a: { loaded: 3 } })
  })

  it('counts response pages, not directories: Load more in one directory counts every page', () => {
    const big = { loaded: EXPLORER_PAGE_CAP + 1 }
    expect(pageTotal({ big })).toBe(EXPLORER_PAGE_CAP + 1)
    expect(capPages({ '': { loaded: 1 }, big }, [''])).toEqual({ '': { loaded: 1 } })
    const kept = capPages({ '': { loaded: 1 }, a: { loaded: 150 }, b: { loaded: 60 }, c: { loaded: 40 } }, ['', 'a'])
    expect(Object.keys(kept)).toEqual(['', 'a', 'c'])
    expect(pageTotal(kept)).toBeLessThanOrEqual(EXPLORER_PAGE_CAP)
  })

  it('drops a reply asked for before a clear, another account, or a newer read of the same slot', () => {
    // A desktop scope change while a read is out: its reply is not kept.
    const scoped = viewWriter('a', 's1', 'permissions')
    window.dispatchEvent(new Event('blerg-desktop-scope-changed'))
    expect(scoped({ source: 'old desktop scope' })).toBe(false)
    expect(readView('a', 's1', 'permissions')).toBeUndefined()
    // Another account: a reply asked for under a, landing after b read, is not kept.
    const before = viewWriter('a', 's1', 'permissions')
    readView('b', 's1', 'permissions')
    expect(before({ who: 'a' })).toBe(false)
    expect(readView('b', 's1', 'permissions')).toBeUndefined()
    // The later read wins, whatever order the replies land in.
    const older = viewWriter('b', 's1', 'permissions')
    const newer = viewWriter('b', 's1', 'permissions')
    expect(newer('new')).toBe(true)
    expect(older('old')).toBe(false)
    expect(readView('b', 's1', 'permissions')).toBe('new')
    // writeView at an epoch that has passed keeps nothing.
    const at = viewEpoch()
    clearViews()
    expect(writeView('b', 's1', 'explorer', 1, at)).toBe(false)
  })
})
