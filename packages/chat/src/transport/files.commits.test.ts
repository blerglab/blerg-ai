// The commit reads (card #131): a summary and an on-demand diff, from the runner's commit routes.
import { describe, expect, it, vi } from 'vitest'
import { COMMIT_DIFF_MAX, createFiles } from './files'

const SHA = '0123456789abcdef0123456789abcdef01234567'

function filesOver(reply: (url: string) => Response) {
  const fetch = vi.fn(async (url: string) => reply(url))
  const files = createFiles({ base: s => `/api/sessions/${s}`, fetch: fetch as unknown as typeof globalThis.fetch, commits: true })
  return { files, fetch }
}

describe('createFiles: commits', () => {
  it('only with commits: true', () => {
    expect(createFiles({ base: s => s }).commit).toBeUndefined()
    expect(createFiles({ base: s => s }).commitDiff).toBeUndefined()
  })

  it('a summary from GET <base>/commits/<sha>, lowercased; a refusal or a wrong commit is null', async () => {
    const body = { sha: SHA, short: SHA.slice(0, 7), subject: 'S', body: 'B', author: 'A', author_date: '2026-10-21T10:00:00Z', parents: [], files: [{ path: 'a.go', adds: 1, dels: 0 }], adds: 1, dels: 0 }
    const { files, fetch } = filesOver(() => new Response(JSON.stringify(body), { status: 200 }))
    const c = await files.commit!('s1', '0123456789ABCDEF')
    expect(fetch).toHaveBeenCalledWith('/api/sessions/s1/commits/0123456789abcdef', expect.anything())
    expect(c?.subject).toBe('S')
    expect(c?.files).toEqual([{ path: 'a.go', adds: 1, dels: 0 }])
    expect(await files.commit!('s1', 'fedcba9')).toBeNull() // the server answered for another commit
    for (const status of [404, 409, 429, 403]) {
      const { files } = filesOver(() => new Response('{"error":"x"}', { status }))
      expect(await files.commit!('s1', SHA)).toBeNull()
    }
  })

  it('anything but 7 to 64 hex digits is never asked', async () => {
    const { files, fetch } = filesOver(() => new Response('{}'))
    for (const sha of ['HEAD', '012345', '../x', '--output=x', '0123456/diff', 'g'.repeat(7), 'a'.repeat(65)]) {
      expect(await files.commit!('s1', sha)).toBeNull()
      expect(await files.commitDiff!('s1', sha)).toBeNull()
    }
    expect(fetch).not.toHaveBeenCalled()
  })

  it('a diff: its text; 413 or a stream past the cap is large; a refusal is null', async () => {
    const ok = filesOver(() => new Response('diff --git a/x b/x\n', { status: 200 }))
    expect(await ok.files.commitDiff!('s1', SHA)).toEqual({ text: 'diff --git a/x b/x\n' })
    expect(ok.fetch).toHaveBeenCalledWith(`/api/sessions/s1/commits/${SHA}/diff`, expect.anything())
    expect(await filesOver(() => new Response('{"error":"x"}', { status: 413 })).files.commitDiff!('s1', SHA)).toBe('large')
    expect(await filesOver(() => new Response(new Uint8Array(COMMIT_DIFF_MAX + 1), { status: 200 })).files.commitDiff!('s1', SHA)).toBe('large')
    expect(await filesOver(() => new Response('{"error":"x"}', { status: 409 })).files.commitDiff!('s1', SHA)).toBeNull()
  })
})

it('qualifies summaries and patches by member and launch and refuses mismatched provenance', async () => {
  const member = { member_id: 'two', launch_generation: 'launch', generation: 7 }
  const body = { sha: SHA, subject: 'Second member', ...member }
  const fetch = vi.fn(async (_url: string, _init?: RequestInit) => new Response(JSON.stringify(body), { status: 200 }))
  const files = createFiles({ base: s => `/api/sessions/${s}`, fetch: fetch as unknown as typeof globalThis.fetch, commits: true })
  expect(await files.commit!('s1', SHA, undefined, member)).toMatchObject(member)
  expect(fetch.mock.calls[0]?.[0]).toBe(`/api/sessions/s1/commits/${SHA}?member_id=two&launch_generation=launch&generation=7`)
  fetch.mockImplementation(async () => new Response(JSON.stringify({ ...body, member_id: 'one' }), { status: 200 }))
  expect(await files.commit!('s1', SHA, undefined, member)).toBeNull()
  fetch.mockImplementation(async () => new Response('second patch', { status: 200, headers: { 'X-Blerg-Member-ID': 'two', 'X-Blerg-Launch-Generation': 'launch', 'X-Blerg-Generation': '7' } }))
  expect(await files.commitDiff!('s1', SHA, undefined, member)).toEqual({ text: 'second patch', member })
  expect(fetch.mock.calls[2]?.[0]).toBe(`/api/sessions/s1/commits/${SHA}/diff?member_id=two&launch_generation=launch&generation=7`)
  fetch.mockImplementation(async () => new Response('old patch', { status: 200, headers: { 'X-Blerg-Member-ID': 'two', 'X-Blerg-Launch-Generation': 'launch', 'X-Blerg-Generation': '6' } }))
  expect(await files.commitDiff!('s1', SHA, undefined, member)).toBeNull()
})
