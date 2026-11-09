// One ResizeObserver per pane, shared across every card registered against it (BLG-449 review:
// a long transcript must not accumulate one observer per message).
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { watchPaneCard } from './paneObserver'

class FakeResizeObserver {
  static instances: FakeResizeObserver[] = []
  elements: Element[] = []
  cb: () => void
  constructor(cb: () => void) { this.cb = cb; FakeResizeObserver.instances.push(this) }
  observe(el: Element) { this.elements.push(el) }
  unobserve(el: Element) { this.elements = this.elements.filter(e => e !== el) }
  disconnect() { this.elements = [] }
}

beforeEach(() => {
  FakeResizeObserver.instances = []
  vi.stubGlobal('ResizeObserver', FakeResizeObserver)
})
afterEach(() => vi.unstubAllGlobals())

it('creates one ResizeObserver per pane, shared by every card registered against it', () => {
  const pane = document.createElement('div')
  const cardA = document.createElement('div')
  const cardB = document.createElement('div')
  const seenA: number[] = []
  const seenB: number[] = []
  let n = 0
  watchPaneCard(pane, cardA, () => seenA.push(++n))
  watchPaneCard(pane, cardB, () => seenB.push(++n))
  expect(FakeResizeObserver.instances).toHaveLength(1)
  expect(FakeResizeObserver.instances[0].elements).toEqual([pane, cardA, cardB])
  // Each registration runs its check once immediately, independent of the others.
  expect(seenA).toHaveLength(1)
  expect(seenB).toHaveLength(1)
})

it('one callback (a pane resize, or a card growing while streaming) checks every registered card in that single pass', () => {
  const pane = document.createElement('div')
  const cardA = document.createElement('div')
  const cardB = document.createElement('div')
  let calls = 0
  watchPaneCard(pane, cardA, () => calls++)
  watchPaneCard(pane, cardB, () => calls++)
  calls = 0
  FakeResizeObserver.instances[0].cb()
  expect(calls).toBe(2) // one shared callback, both cards re-checked together
})

it('unsubscribing stops that card alone; the shared observer survives while another card is still registered', () => {
  const pane = document.createElement('div')
  const cardA = document.createElement('div')
  const cardB = document.createElement('div')
  let calls = 0
  const stopA = watchPaneCard(pane, cardA, () => calls++)
  watchPaneCard(pane, cardB, () => calls++)
  calls = 0
  stopA()
  expect(FakeResizeObserver.instances[0].elements).toEqual([pane, cardB])
  FakeResizeObserver.instances[0].cb()
  expect(calls).toBe(1) // only cardB's callback remains
})

it('disconnects the shared observer once its last card unregisters', () => {
  const pane = document.createElement('div')
  const card = document.createElement('div')
  const disconnect = vi.spyOn(FakeResizeObserver.prototype, 'disconnect')
  const stop = watchPaneCard(pane, card, () => {})
  stop()
  expect(disconnect).toHaveBeenCalledOnce()
  disconnect.mockRestore()
})

it('reuses the same observer for a second card registered later against the same pane', () => {
  const pane = document.createElement('div')
  const cardA = document.createElement('div')
  const cardB = document.createElement('div')
  watchPaneCard(pane, cardA, () => {})
  expect(FakeResizeObserver.instances).toHaveLength(1)
  watchPaneCard(pane, cardB, () => {})
  expect(FakeResizeObserver.instances).toHaveLength(1) // still one instance, not a second
})
