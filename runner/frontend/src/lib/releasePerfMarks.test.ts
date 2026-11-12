import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { mark, markWhenPainted } from './releasePerfMarks'

const paint = () => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))

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
 mark('board-rendered')
 expect(spy).not.toHaveBeenCalled()
})

it('never calls performance.mark when the flag is explicitly false', () => {
 ;(window as Flagged).__BLERG_RELEASE_PERF__ = false
 const spy = vi.spyOn(performance, 'mark')
 mark('board-rendered')
 expect(spy).not.toHaveBeenCalled()
})

it('calls performance.mark with the exact name once the flag is set', () => {
 ;(window as Flagged).__BLERG_RELEASE_PERF__ = true
 const spy = vi.spyOn(performance, 'mark')
 mark('board-rendered')
 expect(spy).toHaveBeenCalledWith('board-rendered')
})

it('markWhenPainted never marks when the release-perf flag is unset, even once the selector exists', async () => {
 const container = document.createElement('div')
 container.innerHTML = '<div data-event-id="e1"></div>'
 const spy = vi.spyOn(performance, 'mark')
 markWhenPainted('transcript-rendered', container, '[data-event-id]')
 await paint()
 expect(spy).not.toHaveBeenCalled()
})

it('markWhenPainted marks only after the selector appears and a frame has committed, not when the loading state alone resolves', async () => {
 ;(window as Flagged).__BLERG_RELEASE_PERF__ = true
 const container = document.createElement('div')
 const spy = vi.spyOn(performance, 'mark')
 markWhenPainted('transcript-rendered', container, '[data-event-id]')
 await paint()
 expect(spy).not.toHaveBeenCalled() // container resolved (mounted) but carries no events yet

 const row = document.createElement('div')
 row.setAttribute('data-event-id', 'e1')
 container.appendChild(row)
 await paint()
 expect(spy).toHaveBeenCalledWith('transcript-rendered')
})

it('markWhenPainted marks immediately (after a paint) when the selector is already present', async () => {
 ;(window as Flagged).__BLERG_RELEASE_PERF__ = true
 const container = document.createElement('div')
 container.innerHTML = '<div data-event-id="e1"></div>'
 const spy = vi.spyOn(performance, 'mark')
 markWhenPainted('transcript-rendered', container, '[data-event-id]')
 await paint()
 expect(spy).toHaveBeenCalledWith('transcript-rendered')
})

it('markWhenPainted never marks after its cleanup runs, even if the selector then appears', async () => {
 ;(window as Flagged).__BLERG_RELEASE_PERF__ = true
 const container = document.createElement('div')
 const spy = vi.spyOn(performance, 'mark')
 const cleanup = markWhenPainted('transcript-rendered', container, '[data-event-id]')
 cleanup()
 const row = document.createElement('div')
 row.setAttribute('data-event-id', 'e1')
 container.appendChild(row)
 await paint()
 expect(spy).not.toHaveBeenCalled()
})
