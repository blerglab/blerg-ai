// Global test setup (vitest.config.ts). jsdom lacks a few browser APIs the components touch;
// minimal stubs let them mount, and tests that care override per test.
import '@testing-library/jest-dom/vitest'
import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'

afterEach(() => {
  cleanup()
})

class ResizeObserverStub {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver
}
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {}
}

// ── localStorage ──────────────────────────────────────────────────────────────
// Node's own experimental `localStorage` global shadows jsdom's and is undefined unless Node is
// started with --localstorage-file; the drafts and the compact-tools choice go through it. Install
// an in-memory Storage only when the real thing is not usable (the runner's setup does the same).
class MemoryStorage implements Storage {
  private store = new Map<string, string>()
  get length(): number {
    return this.store.size
  }
  clear(): void {
    this.store.clear()
  }
  getItem(key: string): string | null {
    return this.store.has(key) ? this.store.get(key)! : null
  }
  key(index: number): string | null {
    return Array.from(this.store.keys())[index] ?? null
  }
  removeItem(key: string): void {
    this.store.delete(key)
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value))
  }
}

function needsPolyfill(storage: unknown): boolean {
  return typeof storage === 'undefined' || typeof (storage as Storage).clear !== 'function'
}

if (needsPolyfill(globalThis.localStorage)) {
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: new MemoryStorage() })
}
