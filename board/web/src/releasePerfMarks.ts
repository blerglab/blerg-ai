// BLG-453: readiness marks the release performance check times itself
// against. `mark` is a complete no-op unless the release-perf Playwright
// harness sets `window.__BLERG_RELEASE_PERF__` before navigation (via
// `page.addInitScript`). Every other context — dev, the existing component
// tests, a real install — never sets that flag, so this call compiles away
// to nothing: no new behavior, no new DOM calls, ships no cost to normal
// users. This is a duplicate of runner/frontend/src/lib/releasePerfMarks.ts
// and packages/chat/src/releasePerfMarks.ts — board/web is its own app and
// doesn't otherwise depend on either.
declare global {
 interface Window {
  __BLERG_RELEASE_PERF__?: boolean
 }
}

export function mark(name: string): void {
 if (!window.__BLERG_RELEASE_PERF__) return
 performance.mark(name)
}
