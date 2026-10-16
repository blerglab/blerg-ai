// A hand-driven IntersectionObserver (jsdom has none). Not installed globally: without one the
// artifact previews and PDF pages render at once, which other suites count on. A suite that wants
// it calls installIntersectionObserver() in beforeEach (vi.unstubAllGlobals() in afterEach takes it
// away again) and makes what is observed enter or leave view with intersect(), inside act().
import { vi } from 'vitest'

const live = new Set<FakeIntersectionObserver>()

export class FakeIntersectionObserver implements IntersectionObserver {
  readonly root: Element | Document | null
  readonly rootMargin: string
  readonly scrollMargin = ''
  readonly thresholds: ReadonlyArray<number> = [0]
  private readonly targets = new Set<Element>()
  private readonly callback: IntersectionObserverCallback

  constructor(callback: IntersectionObserverCallback, options: IntersectionObserverInit = {}) {
    this.callback = callback
    this.root = options.root ?? null
    this.rootMargin = options.rootMargin ?? '0px'
  }
  observe(target: Element): void {
    this.targets.add(target)
    live.add(this)
  }
  unobserve(target: Element): void {
    this.targets.delete(target)
    if (this.targets.size === 0) live.delete(this)
  }
  disconnect(): void {
    this.targets.clear()
    live.delete(this)
  }
  takeRecords(): IntersectionObserverEntry[] {
    return []
  }
  fire(isIntersecting: boolean): void {
    const entries = [...this.targets].map(target => ({
      target,
      isIntersecting,
      intersectionRatio: isIntersecting ? 1 : 0,
      time: 0,
      boundingClientRect: target.getBoundingClientRect(),
      intersectionRect: target.getBoundingClientRect(),
      rootBounds: null,
    }) as IntersectionObserverEntry)
    if (entries.length > 0) this.callback(entries, this)
  }
}

export function installIntersectionObserver(): void {
  live.clear()
  vi.stubGlobal('IntersectionObserver', FakeIntersectionObserver)
}

/** Every observed element enters view (or leaves it): each observer still connected is told. */
export function intersect(isIntersecting = true): void {
  for (const o of [...live]) o.fire(isIntersecting)
}
