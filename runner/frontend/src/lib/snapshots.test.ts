// The bounded snapshot store (BLG-449 review M2): a popped-out message's body lives here, capped
// per snapshot and per account, evicting the oldest unprotected entry first, never throwing.
import { beforeEach, expect, it, vi } from 'vitest'
import { PER_ACCOUNT_CAP, PER_SNAPSHOT_CAP, clearAccountSnapshots, clearAllSnapshots, loadSnapshot, saveSnapshot } from './snapshots'

beforeEach(() => localStorage.clear())

it('saves and loads a snapshot by account, session and id', () => {
  expect(saveSnapshot('acct', 'sess', 'e1', 'hello')).toBe(true)
  expect(loadSnapshot('acct', 'sess', 'e1')).toBe('hello')
  expect(loadSnapshot('acct', 'sess', 'e2')).toBeUndefined() // a different id
  expect(loadSnapshot('acct', 'other-sess', 'e1')).toBeUndefined() // a different session
  expect(loadSnapshot('other-acct', 'sess', 'e1')).toBeUndefined() // a different account
})

it('refuses a single snapshot over the per-snapshot cap, without throwing', () => {
  const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
  const big = 'x'.repeat(Math.ceil(PER_SNAPSHOT_CAP / 2) + 10)
  expect(saveSnapshot('acct', 'sess', 'e1', big)).toBe(false)
  expect(loadSnapshot('acct', 'sess', 'e1')).toBeUndefined()
  expect(warn).toHaveBeenCalled()
  warn.mockRestore()
})

// Chunks sized well under the per-snapshot cap (so only the per-account cap is ever at play),
// large enough that a modest number of them crosses the per-account cap.
const CHUNK_CHARS = Math.floor(PER_SNAPSHOT_CAP / 2 / 2) // half the per-snapshot cap, in chars (UTF-16: *2 bytes)
const CHUNK = 'x'.repeat(CHUNK_CHARS)
const CHUNK_BYTES = CHUNK_CHARS * 2
const PER_ACCOUNT_COUNT = Math.ceil(PER_ACCOUNT_CAP / CHUNK_BYTES)

it('evicts the oldest entry first to make room under the per-account cap', () => {
  for (let i = 0; i < PER_ACCOUNT_COUNT; i++) expect(saveSnapshot('acct', 'sess', `id${i}`, CHUNK)).toBe(true)
  expect(loadSnapshot('acct', 'sess', 'id0')).toBe(CHUNK) // still fits exactly at the cap
  // One more pushes the account over: id0 (the oldest) is evicted to make room.
  expect(saveSnapshot('acct', 'sess', 'idN', CHUNK)).toBe(true)
  expect(loadSnapshot('acct', 'sess', 'id0')).toBeUndefined()
  expect(loadSnapshot('acct', 'sess', 'id1')).toBe(CHUNK) // the next-oldest survives
  expect(loadSnapshot('acct', 'sess', 'idN')).toBe(CHUNK)
})

it('never evicts a protected id, and fails the new save instead when that leaves no room', () => {
  const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
  const protect = new Set<string>()
  for (let i = 0; i < PER_ACCOUNT_COUNT; i++) {
    saveSnapshot('acct', 'sess', `id${i}`, CHUNK, protect)
    protect.add(`id${i}`) // every existing entry is "still in the layout": nothing evictable
  }
  // No unprotected entry to evict: the new one fails instead of displacing a protected one.
  expect(saveSnapshot('acct', 'sess', 'idN', CHUNK, protect)).toBe(false)
  for (let i = 0; i < PER_ACCOUNT_COUNT; i++) expect(loadSnapshot('acct', 'sess', `id${i}`)).toBe(CHUNK) // untouched
  expect(loadSnapshot('acct', 'sess', 'idN')).toBeUndefined()
  expect(warn).toHaveBeenCalled()
  warn.mockRestore()
})

it('overwriting the same id does not count its old size twice', () => {
  expect(saveSnapshot('acct', 'sess', 'e1', 'short')).toBe(true)
  expect(saveSnapshot('acct', 'sess', 'e1', 'a longer replacement body')).toBe(true)
  expect(loadSnapshot('acct', 'sess', 'e1')).toBe('a longer replacement body')
})

// BLG-451 review M2: a chart/diagram's id is a content hash (visualSnapshotId), so the same chart
// opened in two different sessions shares one id -- the index keys and protects by (session, id),
// not id alone, or one session's save would drop the other's index entry (orphaning its body:
// uncounted, never evicted, missed by clearAccountSnapshots) and one session's protect set would
// shield an entry belonging to a different session entirely.
it('keeps independent index entries for the same id saved under two different sessions', () => {
  expect(saveSnapshot('acct', 'sess-a', 'chart1', 'same content')).toBe(true)
  expect(saveSnapshot('acct', 'sess-b', 'chart1', 'same content')).toBe(true)
  // Neither save displaced the other's entry.
  expect(loadSnapshot('acct', 'sess-a', 'chart1')).toBe('same content')
  expect(loadSnapshot('acct', 'sess-b', 'chart1')).toBe('same content')
  clearAccountSnapshots('acct')
  // Both are in the index (not orphaned): clearing the account reaches both, not just the last-saved.
  expect(loadSnapshot('acct', 'sess-a', 'chart1')).toBeUndefined()
  expect(loadSnapshot('acct', 'sess-b', 'chart1')).toBeUndefined()
})

it('evicts a same-id entry that belongs to another session even when the id matches something protected here', () => {
  expect(saveSnapshot('acct', 'sess-a', 'chart1', CHUNK)).toBe(true)
  // sess-b's own protect set happens to name the same id (its own, unrelated, currently open tab
  // -- a coincidence content hashes make possible) -- that must never shield sess-a's entry.
  const protect = new Set(['chart1'])
  for (let i = 0; i < PER_ACCOUNT_COUNT; i++) expect(saveSnapshot('acct', 'sess-b', `id${i}`, CHUNK, protect)).toBe(true)
  expect(loadSnapshot('acct', 'sess-a', 'chart1')).toBeUndefined() // evicted: sess-a's entry, not sess-b's
  expect(loadSnapshot('acct', 'sess-b', 'id0')).toBe(CHUNK) // sess-b's own oldest survived instead
})

it('never evicts a protected id in the *same* session that matches another session\'s entry', () => {
  expect(saveSnapshot('acct', 'sess-a', 'chart1', CHUNK)).toBe(true)
  // Fill sess-b with entries up to the cap, chart1 among them and protected there too.
  const protect = new Set(['chart1'])
  expect(saveSnapshot('acct', 'sess-b', 'chart1', CHUNK, protect)).toBe(true)
  for (let i = 1; i < PER_ACCOUNT_COUNT; i++) { saveSnapshot('acct', 'sess-b', `id${i}`, CHUNK, protect); protect.add(`id${i}`) }
  expect(saveSnapshot('acct', 'sess-b', 'idN', CHUNK, protect)).toBe(false) // nothing left to evict in sess-b
  expect(loadSnapshot('acct', 'sess-b', 'chart1')).toBe(CHUNK) // sess-b's own protected copy survived
})

it('a setItem failure is reported and returns false, never thrown', () => {
  const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
  // Spy on the prototype, not the instance: on a native Web Storage, assigning `setItem` on the
  // instance can store a key named "setItem" instead of replacing the method (seen as a flake).
  const proto = Object.getPrototypeOf(localStorage) as Storage
  const real = proto.setItem
  const spy = vi.spyOn(proto, 'setItem').mockImplementation(function (this: Storage, k: string, v: string) {
    if (k.startsWith('blerg-runner.snapshot.')) throw new Error('quota exceeded')
    real.call(this, k, v)
  })
  expect(() => saveSnapshot('acct', 'sess', 'e1', 'hello')).not.toThrow()
  expect(saveSnapshot('acct', 'sess', 'e1', 'hello')).toBe(false)
  expect(loadSnapshot('acct', 'sess', 'e1')).toBeUndefined()
  expect(warn).toHaveBeenCalled()
  spy.mockRestore()
  warn.mockRestore()
})

it('clearAccountSnapshots forgets only that account', () => {
  saveSnapshot('acct-a', 'sess', 'e1', 'a')
  saveSnapshot('acct-b', 'sess', 'e1', 'b')
  clearAccountSnapshots('acct-a')
  expect(loadSnapshot('acct-a', 'sess', 'e1')).toBeUndefined()
  expect(loadSnapshot('acct-b', 'sess', 'e1')).toBe('b')
})

it('clearAllSnapshots forgets every account (sign-out)', () => {
  saveSnapshot('acct-a', 'sess', 'e1', 'a')
  saveSnapshot('acct-b', 'sess', 'e1', 'b')
  clearAllSnapshots()
  expect(loadSnapshot('acct-a', 'sess', 'e1')).toBeUndefined()
  expect(loadSnapshot('acct-b', 'sess', 'e1')).toBeUndefined()
})

it('the blerg-signed-out event clears every account', () => {
  saveSnapshot('acct-a', 'sess', 'e1', 'a')
  window.dispatchEvent(new Event('blerg-signed-out'))
  expect(loadSnapshot('acct-a', 'sess', 'e1')).toBeUndefined()
})
