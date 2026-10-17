import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_CODE_COLOURS } from '@blerglab/chat'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

import { fetchCodeColours } from './codeColours'

const answer = (status: number, body: unknown) =>
  ({ ok: status >= 200 && status < 300, status, json: async () => body }) as Response

describe('fetchCodeColours', () => {
  beforeEach(() => apiFetch.mockReset())

  it('returns the server choice when there is one', async () => {
    apiFetch.mockResolvedValue(answer(200, { code_colours: { dark: 'dracula', light: 'github-light' } }))
    const c = await fetchCodeColours()
    expect(c?.dark).toBe('dracula')
  })

  it('returns the defaults when the server answers with none, so another account\'s cache does not stay', async () => {
    apiFetch.mockResolvedValue(answer(200, {}))
    expect(await fetchCodeColours()).toEqual(DEFAULT_CODE_COLOURS)
  })

  it('returns null when the server could not be read, so the cached choice stays', async () => {
    apiFetch.mockResolvedValue(answer(500, {}))
    expect(await fetchCodeColours()).toBeNull()
    apiFetch.mockImplementationOnce(() => Promise.reject(new Error('offline')))
    expect(await fetchCodeColours()).toBeNull()
  })
})
