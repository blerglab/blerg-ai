// BLG-470 live files, round 2 review:
// 1. A patch applies only when strictly newer than the list's own row (updated_at, sha256 tie-break).
// 2. The merge is memoized: an unrelated re-render (same list, same patches) keeps the same items
//    reference, so ChatView's big useMemo values (keyed on artifacts.items) do not churn.
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useArtifacts } from './useArtifacts'
import { createFakeFiles, type FakeFiles } from '../test/fakeTransport'
import type { ArtifactInfo } from '../transport/types'
import type { ArtifactPayload } from '../types'

let files: FakeFiles

const base: ArtifactInfo = { id: 'live1', name: 'chart.png', size: 10, content_type: 'image/png', view: 'image', live: true, updated_at: '2026-11-04T00:00:00Z', sha256: 'aaaa' }

beforeEach(() => {
  files = createFakeFiles([base])
})

afterEach(() => vi.restoreAllMocks())

async function mount(liveArtifacts: Record<string, ArtifactPayload>, prune?: (sessionId: string, ids: string[]) => void) {
  const view = renderHook(
    ({ live }: { live: Record<string, ArtifactPayload> }) => useArtifacts('s1', files, [], live, prune),
    { initialProps: { live: liveArtifacts } },
  )
  await act(async () => {}) // let the initial files.list() resolve
  return view
}

describe('useArtifacts: a patch applies only when strictly newer', () => {
  it('ignores an older patch (an earlier republish arriving after a fresher list read)', async () => {
    const stale: ArtifactPayload = { ...base, size: 1, updated_at: '2026-11-03T23:59:00Z', sha256: 'zzzz' }
    const view = await mount({ live1: stale })
    expect(view.result.current.items[0].size).toBe(10) // the list's own (newer) value, not the stale patch
  })

  it('applies a strictly newer patch', async () => {
    const fresher: ArtifactPayload = { ...base, size: 999, updated_at: '2026-11-04T00:05:00Z', sha256: 'bbbb' }
    const view = await mount({ live1: fresher })
    expect(view.result.current.items[0].size).toBe(999)
  })

  it('breaks an exact updated_at tie with sha256, consistently either way round', async () => {
    const higherSha: ArtifactPayload = { ...base, size: 20, sha256: 'bbbb' } // same updated_at, higher sha
    const lowerSha: ArtifactPayload = { ...base, size: 30, sha256: '0000' } // same updated_at, lower sha
    expect((await mount({ live1: higherSha })).result.current.items[0].size).toBe(20)
    expect((await mount({ live1: lowerSha })).result.current.items[0].size).toBe(10) // base's 'aaaa' > '0000'
  })

  it('a patch for an id the list does not have changes nothing', async () => {
    const other: ArtifactPayload = { id: 'gone', name: 'x', size: 1, content_type: 'text/plain', view: 'text', live: true, updated_at: '2099-01-01T00:00:00Z' }
    const view = await mount({ gone: other })
    expect(view.result.current.items).toEqual([base])
  })
})

describe('useArtifacts: the merge is memoized', () => {
  it('keeps the same items reference across a re-render with the same list and patches', async () => {
    const fresher: ArtifactPayload = { ...base, size: 999, updated_at: '2026-11-04T00:05:00Z', sha256: 'bbbb' }
    const live = { live1: fresher }
    const view = await mount(live)
    const first = view.result.current.items
    // An unrelated re-render (e.g. a streaming delta elsewhere): same props, same object identities.
    view.rerender({ live })
    expect(view.result.current.items).toBe(first)
  })

  it('a caught-up patch is pruned, and pruning it changes nothing else about the returned items', async () => {
    const prune = vi.fn()
    // A patch the list has already caught up with (its own row is at least as new).
    const caughtUp: ArtifactPayload = { ...base, size: 1, updated_at: base.updated_at, sha256: '0000' }
    const view = await mount({ live1: caughtUp }, prune)
    await act(async () => {})
    expect(prune).toHaveBeenCalledWith('s1', ['live1'])
    expect(view.result.current.items[0].size).toBe(10) // the list's own value, unapplied
  })

  it('a caught-up patch leaves the items array itself unchanged (same reference as the plain list)', async () => {
    const caughtUp: ArtifactPayload = { ...base, size: 1, updated_at: base.updated_at, sha256: '0000' }
    const view = await mount({ live1: caughtUp })
    const plain = await mount({})
    expect(view.result.current.items).toEqual(plain.result.current.items)
  })
})
