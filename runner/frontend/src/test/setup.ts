// Global test setup, wired via vitest.config.ts `setupFiles`.
//
// jsdom omits several browser APIs the components touch. We provide minimal
// stubs here so components can mount without throwing. Tests that care about a
// specific API's behaviour override these per-test.

import '@testing-library/jest-dom/vitest'
import { afterEach, vi } from 'vitest'
import { cleanup, configure } from '@testing-library/react'

// Unmount any rendered components and clear the DOM between tests.
afterEach(() => {
  cleanup()
})

// waitFor's default second is not enough when the whole suite runs on a loaded CI runner; a
// passing test is not made slower by this, only a slow one is given the time it needs.
configure({ asyncUtilTimeout: 5000 })

// ── ResizeObserver ────────────────────────────────────────────────────────────
// Used by TerminalDOMView. jsdom has no implementation. A no-op observer is
// enough: tests drive resize behaviour by other means when needed.
class ResizeObserverStub {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
if (!('ResizeObserver' in globalThis)) {
  globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver
}

// ── visualViewport ────────────────────────────────────────────────────────────
// Soft-keyboard handling reads window.visualViewport. jsdom doesn't define it.
if (!window.visualViewport) {
  Object.defineProperty(window, 'visualViewport', {
    configurable: true,
    value: {
      width: window.innerWidth,
      height: window.innerHeight,
      offsetTop: 0,
      addEventListener: () => {},
      removeEventListener: () => {},
    },
  })
}

// ── localStorage / sessionStorage ───────────────────────────────────────────────
// On this jsdom setup (Node 26 / jsdom 29 combo in this repo) window.localStorage
// is undefined (or present but missing methods like .clear) rather than a working
// Storage — every test that touches persisted prefs (sidebarPrefs, useSessionStore,
// useMessageStore, refreshGuard's sessionStorage) fails on that, not on the logic
// under test. Install a minimal in-memory Storage polyfill, but only if the real
// thing isn't already usable — a working jsdom Storage should win.
class MemoryStorage implements Storage {
  private store = new Map<string, string>();
  get length(): number {
    return this.store.size;
  }
  clear(): void {
    this.store.clear();
  }
  getItem(key: string): string | null {
    return this.store.has(key) ? this.store.get(key)! : null;
  }
  key(index: number): string | null {
    return Array.from(this.store.keys())[index] ?? null;
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
}

function needsPolyfill(storage: unknown): boolean {
  return typeof storage === 'undefined' || typeof (storage as Storage).clear !== 'function'
}

if (needsPolyfill(globalThis.localStorage)) {
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: new MemoryStorage(),
  })
}
if (needsPolyfill(globalThis.sessionStorage)) {
  Object.defineProperty(globalThis, 'sessionStorage', {
    configurable: true,
    value: new MemoryStorage(),
  })
}

// ── matchMedia ────────────────────────────────────────────────────────────────
// useIsMobile calls window.matchMedia. Default to desktop (no match); tests that
// need mobile override window.matchMedia before rendering.
if (!window.matchMedia) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  }))
}
