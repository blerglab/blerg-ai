// BLG-453: readiness marks the release performance check times itself against.
// `mark` is a complete no-op unless the release-perf Playwright harness sets
// `window.__BLERG_RELEASE_PERF__` before navigation (via `page.addInitScript`).
// Every other context — dev, the existing component tests, a real install —
// never sets that flag, so this call compiles away to nothing: no new
// behavior, no new DOM calls, ships no cost to normal users.
declare global {
 interface Window {
  __BLERG_RELEASE_PERF__?: boolean
 }
}

export function mark(name: string): void {
 if (!window.__BLERG_RELEASE_PERF__) return
 performance.mark(name)
}

// markWhenPainted marks name once an element matching selector actually
// exists inside container AND a frame has committed since — "the newest
// window of events has painted," not just "the data arrived." A loading
// state resolving doesn't mean its rows are on screen yet: watch for the
// real DOM node, then wait two animation frames (the same proof-of-paint
// margin the test harness's own `painted()` helper uses) before marking.
// Returns a cleanup function: call it if the caller unmounts or moves on
// before the mark would have fired, so a stale mark never lands late
// against the wrong view. A no-op (returns a no-op cleanup) unless the
// release-perf flag is set, same as `mark` itself.
export function markWhenPainted(name: string, container: HTMLElement | null, selector: string): () => void {
 if (!window.__BLERG_RELEASE_PERF__ || !container) return () => {}
 let active = true
 const schedule = () => {
  requestAnimationFrame(() => requestAnimationFrame(() => { if (active) mark(name) }))
 }
 if (container.querySelector(selector)) {
  schedule()
  return () => { active = false }
 }
 const observer = new MutationObserver(() => {
  if (!active || !container.querySelector(selector)) return
  observer.disconnect()
  schedule()
 })
 observer.observe(container, { childList: true, subtree: true })
 return () => { active = false; observer.disconnect() }
}
