import { afterEach, expect, it, vi } from 'vitest'
import { applyRepositoryStatus, emptyRepositoryStatus, readRepositoryStatus, repositoryStatusLabel } from './repositoryStatus'

vi.mock('../apiFetch', () => ({ apiFetch: (...args: Parameters<typeof fetch>) => fetch(...args) }))
afterEach(() => vi.restoreAllMocks())

it.each([
  [409, { code: 'busy' }],
  [200, { result: { ok: false, error_code: 'busy' } }],
])('retains the last good state on busy %s', async (code, body) => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify(body), { status: code }))
  const previous = { status: { branch: 'main', changed: 2, untracked: 1, partial: false }, error: '' }
  const next = await readRepositoryStatus('session', '.', new AbortController().signal)
  expect(next.kind).toBe('busy')
  expect(applyRepositoryStatus(previous, next)).toBe(previous)
  expect(applyRepositoryStatus(emptyRepositoryStatus, next)).toBe(emptyRepositoryStatus)
})
it('uses partial counts and never calls an incomplete zero clean', async () => {
  const status = { branch: 'main', changed: 2, untracked: 1, partial: true }
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ result: { ok: true, status } })))
  const next = await readRepositoryStatus('session', '.', new AbortController().signal)
  const snapshot = applyRepositoryStatus(emptyRepositoryStatus, next)
  expect(snapshot.status).toEqual(status)
  expect(snapshot.error).toBe('')
  expect(repositoryStatusLabel(snapshot)).toBe('3+ uncommitted')
  expect(repositoryStatusLabel({ status: { ...status, changed: 0, untracked: 0 }, error: '' })).toBe('Status incomplete')
})
it('distinguishes real failure and preserves its safe reason', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ result: { ok: false, status_reason: 'metadata_changed' } })))
  const next = await readRepositoryStatus('session', '.', new AbortController().signal)
  expect(next.kind).toBe('failure')
  const snapshot = applyRepositoryStatus(emptyRepositoryStatus, next)
  expect(snapshot.error).toBeTruthy()
  expect(repositoryStatusLabel(snapshot)).toBe('Git status unavailable')
})
it.each([
  [200, null, 'The repository state could not be read.'],
  [200, { result: { ok: true } }, 'The repository state could not be read.'],
  [409, { code: 'daemon_timeout' }, 'The workspace runtime did not reply.'],
  [200, { result: { ok: false, error_code: 'cancelled' } }, 'The repository state read timed out.'],
])('explains an unusable response %s %j', async (code, body, reason) => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify(body), { status: code }))
  expect(await readRepositoryStatus('session', '.', new AbortController().signal)).toEqual({ kind: 'failure', reason })
})
it('clears an old failure on contention and replaces it when a good state arrives', () => {
  const previous = { status: { branch: 'main', changed: 2, untracked: 0, partial: false }, error: 'old failure' }
  expect(applyRepositoryStatus(previous, { kind: 'busy' })).toEqual({ ...previous, error: '' })
  expect(applyRepositoryStatus(previous, { kind: 'failure', reason: 'new failure' })).toEqual({ ...previous, error: 'new failure' })
})
