import { send } from './ws'

// The client reports a throttled "activity" signal to the server on user
// interaction. The server uses it to decide whether to also send an OS push on a
// session transition (only when no one has interacted within its activityTTL).
// Toasts themselves are always shown client-side, independent of this — so the
// client no longer needs an engagement check, just the heartbeat.

// Cap how often we tell the server we're active. The server only needs to know
// we've interacted within its TTL, so one ping per throttle window is plenty.
const SEND_THROTTLE_MS = 30_000

// -Infinity so the very first interaction always sends, independent of the clock.
let lastSent = Number.NEGATIVE_INFINITY

// recordInteraction reports whether a throttled activity ping should be sent to
// the server at `now`, updating the throttle clock when it returns true.
export function recordInteraction(now: number): boolean {
  if (now - lastSent >= SEND_THROTTLE_MS) {
    lastSent = now
    return true
  }
  return false
}

// resetActivity clears the throttle clock. Test-only.
export function resetActivity(): void {
  lastSent = Number.NEGATIVE_INFINITY
}

function onInteraction(): void {
  if (recordInteraction(Date.now())) {
    send({ type: 'activity' })
  }
}

// startActivityTracking wires interaction listeners that feed the server a
// throttled activity signal. Call once at app startup.
export function startActivityTracking(): void {
  if (typeof window === 'undefined') return
  for (const ev of ['pointerdown', 'keydown', 'touchstart'] as const) {
    window.addEventListener(ev, onInteraction, { passive: true })
  }
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') onInteraction()
  })
}
