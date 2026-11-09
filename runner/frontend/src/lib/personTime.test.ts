import { afterEach, expect, it, vi } from 'vitest'
import { apiFetch } from '../apiFetch'
import { initializePersonTime } from './personTime'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
afterEach(() => vi.resetAllMocks())
it('captures the browser zone only when the account has none', async () => {
  vi.mocked(apiFetch).mockResolvedValue({ ok: true, json: async () => ({}) } as Response)
  await initializePersonTime()
  expect(apiFetch).toHaveBeenLastCalledWith('/api/me/preferences', expect.objectContaining({
    headers: { 'Content-Type': 'application/json', 'If-None-Match': '*' },
    body: JSON.stringify({ person_time: { timezone: Intl.DateTimeFormat().resolvedOptions().timeZone } }),
  }))
})
it('preserves the stored zone and leaves unavailable preferences alone', async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce({ ok: true, json: async () => ({ person_time: { timezone: 'Europe/Paris' } }) } as Response)
  await initializePersonTime()
  expect(apiFetch).toHaveBeenCalledTimes(1)
  vi.mocked(apiFetch).mockClear().mockResolvedValueOnce({ ok: false } as Response)
  await initializePersonTime()
  expect(apiFetch).toHaveBeenCalledTimes(1)
})
