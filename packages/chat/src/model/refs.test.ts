import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { splitRefs, createRefStore, DEBOUNCE_MS, refStoreFor, resetRefStoresForTest, type RefKey, type RefStore } from './refs'
import type { CardTarget } from '../components/Hovercard'

const SHA = '08025f2c9d1e4b7a0c3f5e6d7a8b9c0d1e2f3a4b'
const target: CardTarget = { kind: 'card', source: 'blerg-board', boardId: 'private-board', key: '#7', title: 'PRIVATE TITLE' }
type Kind = 'bare card' | 'qualified card' | 'commit'
type Resolvers = Parameters<RefStore['setResolvers']>[0]
const cases: { kind: Kind; key: RefKey }[] = [
  { kind: 'bare card', key: 'card:7' },
  { kind: 'qualified card', key: 'card:private-board:7' },
  { kind: 'commit', key: `commit:${SHA.slice(0, 8)}` },
]
function resolvers(kind: Kind, read: () => Promise<boolean>): Resolvers {
  const cards = async () => await read() ? new Map([[7, target]]) : new Map<number, CardTarget>()
  if (kind === 'commit') return { commit: async () => await read() ? SHA : null }
  return kind === 'bare card' ? { cards, commit: null } : { boardCards: cards, commit: null }
}
function deferred() {
  let resolve!: (value: boolean) => void
  let reject!: (error: Error) => void
  const promise = new Promise<boolean>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const flush = () => vi.advanceTimersByTimeAsync(DEBOUNCE_MS)
beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { resetRefStoresForTest(); vi.useRealTimers() })

describe.each(cases)('$kind authority', ({ kind, key }) => {
  it('rechecks a cached result when its resolver changes', async () => {
    const store = createRefStore()
    store.setResolvers(resolvers(kind, async () => true))
    store.want(key)
    await flush()
    expect(store.get(key).state).toBe(kind === 'commit' ? 'commit' : 'card')
    const denied = vi.fn(async () => false)
    store.setResolvers(resolvers(kind, denied))
    expect(store.get(key).state).toBe('pending')
    await flush()
    expect(denied).toHaveBeenCalledOnce()
    expect(store.get(key)).toEqual({ state: 'none' })
  })

  it.each(['success', 'failure'] as const)('drops an old in-flight %s after resolver replacement', async outcome => {
    const store = createRefStore()
    const old = deferred()
    store.setResolvers(resolvers(kind, () => old.promise))
    store.want(key)
    await flush()
    // A stale success must not overwrite denial; a stale failure must not erase a new success.
    store.setResolvers(resolvers(kind, async () => outcome === 'failure'))
    await flush()
    const current = store.get(key)
    if (outcome === 'success') old.resolve(true)
    else old.reject(new Error('old reader failed'))
    await flush()
    expect(store.get(key)).toEqual(current)
    expect(current.state).toBe(outcome === 'success' ? 'none' : kind === 'commit' ? 'commit' : 'card')
  })

  it('clears cached answers even when a remount reuses the same resolver', async () => {
    const store = refStoreFor('shared-session')
    const readers = resolvers(kind, async () => true)
    store.setResolvers(readers)
    store.want(key)
    await flush()
    resetRefStoresForTest()
    expect(store.get(key)).toEqual({ state: 'pending' })
    const current = refStoreFor('shared-session')
    current.setResolvers(readers)
    expect(current.get(key)).toEqual({ state: 'pending' })
  })

  it('clears retained stores and drops responses after an identity reset', async () => {
    const store = refStoreFor('shared-session')
    const old = deferred()
    store.setResolvers(resolvers(kind, () => old.promise))
    store.want(key)
    await flush()
    resetRefStoresForTest()
    expect(refStoreFor('shared-session')).not.toBe(store)
    old.resolve(true)
    await flush()
    expect(store.get(key)).toEqual({ state: 'pending' })
  })
})

it('stops queued commit reads when their resolver is retired', async () => {
  const store = createRefStore()
  const old = deferred()
  const read = vi.fn(async () => { await old.promise; return SHA })
  store.setResolvers({ commit: read })
  const keys = Array.from({ length: 8 }, (_, i) => `commit:${i}8025f2c` as const)
  for (const key of keys) store.want(key)
  await flush()
  expect(read).toHaveBeenCalledTimes(4)
  store.setResolvers({ commit: null })
  await flush()
  old.resolve(true)
  await flush()
  expect(read).toHaveBeenCalledTimes(4)
  for (const key of keys) expect(store.get(key)).toEqual({ state: 'none' })
})

it('links every card in a slash-separated list and preserves separators', () => {
  expect(splitRefs('#210/#211/#212')).toEqual([
    { text: '#210', key: 'card:210' }, { text: '/' },
    { text: '#211', key: 'card:211' }, { text: '/' },
    { text: '#212', key: 'card:212' },
  ])
})
it.each(['path/#3', 'a/#1', 'https://example.com/#3', 'https://example.com/#210/#211', 'a/#210/#211', '#210/path/#211', '#210//#211'])('leaves path and URL references unlinked: %s', text => {
  const pieces = splitRefs(text)
  expect(pieces.filter(p => 'key' in p).map(p => p.text)).toEqual(text.startsWith('#210') ? ['#210'] : [])
  expect(pieces.map(p => p.text).join('')).toBe(text)
})


it.each(['BLG-205', '(BLG-205)', 'BLG-205/AD-6'])('parses board keys: %s', text => {
  const refs = splitRefs(text).filter(p => 'key' in p)
  expect(refs).toContainEqual({ text: 'BLG-205', key: 'card:BLG:205' })
  if (text.includes('AD-6')) expect(refs).toContainEqual({ text: 'AD-6', key: 'card:AD:6' })
  expect(splitRefs(text).map(p => p.text).join('')).toBe(text)
})
it.each(['555-1234', '2026-10', '155-157', '1AB-2', 'A-2', 'ABCDEF-2', 'BLG-0', 'BLG-02', 'prefixBLG-2', 'https://example.com/BLG-2', 'BLG-2suffix'])('does not parse invalid key references: %s', text => {
  expect(splitRefs(text).filter(p => 'key' in p && p.key.startsWith('card:'))).toEqual([])
})
