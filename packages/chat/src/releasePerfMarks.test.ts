import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { mark } from './releasePerfMarks'

type Flagged = typeof window & { __BLERG_RELEASE_PERF__?: boolean }

beforeEach(() => {
 delete (window as Flagged).__BLERG_RELEASE_PERF__
})
afterEach(() => {
 delete (window as Flagged).__BLERG_RELEASE_PERF__
 vi.restoreAllMocks()
})

it('never calls performance.mark when the release-perf flag is unset', () => {
 const spy = vi.spyOn(performance, 'mark')
 mark('key-committed')
 expect(spy).not.toHaveBeenCalled()
})

it('calls performance.mark with the exact name once the flag is set', () => {
 ;(window as Flagged).__BLERG_RELEASE_PERF__ = true
 const spy = vi.spyOn(performance, 'mark')
 mark('key-committed')
 expect(spy).toHaveBeenCalledWith('key-committed')
})
