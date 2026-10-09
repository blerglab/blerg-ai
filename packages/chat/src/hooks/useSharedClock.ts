import { useSyncExternalStore } from 'react'
import { TICK_MS } from '../model/timeLabel'

// One shared clock for every relative time label on screen. The snapshot is a
// coarse bucket of Date.now() (stable between ticks, so React skips
// re-rendering until it moves); a single interval nudges subscribers while any
// exist, stops while the tab is hidden, and re-checks the moment the tab is
// visible again. There is never a timer per card.

const POLL_MS = 10_000
const listeners = new Set<() => void>()
let timer: ReturnType<typeof setInterval> | null = null

function notify() {
  listeners.forEach(l => l())
}

function hidden(): boolean {
  return typeof document !== 'undefined' && document.visibilityState === 'hidden'
}

function startTimer() {
  if (timer === null && !hidden()) timer = setInterval(notify, POLL_MS)
}

function stopTimer() {
  if (timer !== null) {
    clearInterval(timer)
    timer = null
  }
}

function onVisibility() {
  if (hidden()) {
    stopTimer()
  } else {
    notify()
    startTimer()
  }
}

function subscribe(cb: () => void): () => void {
  if (listeners.size === 0) {
    startTimer()
    if (typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisibility)
  }
  listeners.add(cb)
  return () => {
    listeners.delete(cb)
    if (listeners.size === 0) {
      stopTimer()
      if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisibility)
    }
  }
}

let cachedBucket = -1
let cachedNow = 0

// The snapshot is Date.now() as of the current 30 s bucket: the same value
// until the bucket moves, so React sees no change between ticks.
function snapshot(): number {
  const now = Date.now()
  const bucket = Math.floor(now / TICK_MS)
  if (bucket !== cachedBucket) {
    cachedBucket = bucket
    cachedNow = now
  }
  return cachedNow
}

/** useClockNow: the browser time (ms), refreshed about every 30 s while the tab is visible. */
export function useClockNow(): number {
  return useSyncExternalStore(subscribe, snapshot, snapshot)
}
