// BLG-453: readiness marks the release performance check times itself
// against. `mark` is a complete no-op unless the release-perf Playwright
// harness sets `window.__BLERG_RELEASE_PERF__` before navigation (via
// `page.addInitScript`). Every other context — dev, the existing component
// tests, a real install — never sets that flag, so this call compiles away
// to nothing: no new behavior, no new DOM calls, ships no cost to normal
// users.
//
// This is a duplicate of runner/frontend/src/lib/releasePerfMarks.ts rather
// than a shared import: @blerglab/chat is its own npm package, used by both
// the runner and board frontends, and shouldn't gain an app-specific
// dependency for one tiny no-op function. Both copies must stay in sync if
// the gating logic ever changes.
declare global {
 interface Window {
  __BLERG_RELEASE_PERF__?: boolean
 }
}

export function mark(name: string): void {
 if (!window.__BLERG_RELEASE_PERF__) return
 performance.mark(name)
}
