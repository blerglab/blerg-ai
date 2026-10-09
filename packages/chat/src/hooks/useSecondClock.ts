import { useSyncExternalStore } from 'react'

// One 1 s clock for every running tool call on screen. The interval exists
// only while at least one subscriber does (a hook is active only for a call
// in flight), stops while the tab is hidden and resyncs the moment it is
// visible again. Never a timer per row.

const listeners = new Set<() => void>()
let timer: ReturnType<typeof setInterval> | null = null

const notify = () => listeners.forEach(l => l())
const hidden = () => typeof document !== 'undefined' && document.visibilityState === 'hidden'

function start() {
  if (timer === null && !hidden()) timer = setInterval(notify, 1000)
}
function stop() {
  if (timer !== null) {
    clearInterval(timer)
    timer = null
  }
}
function onVisibility() {
  if (hidden()) stop()
  else {
    notify()
    start()
  }
}

function subscribe(cb: () => void): () => void {
  if (listeners.size === 0) {
    start()
    if (typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisibility)
  }
  listeners.add(cb)
  return () => {
    listeners.delete(cb)
    if (listeners.size === 0) {
      stop()
      if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisibility)
    }
  }
}

const idle = () => () => {}

let cachedSec = -1
let cachedNow = 0
// Date.now() as of the current second: stable between ticks.
function snapshot(): number {
  const now = Date.now()
  const sec = Math.floor(now / 1000)
  if (sec !== cachedSec) {
    cachedSec = sec
    cachedNow = now
  }
  return cachedNow
}

/** useSecondClock: browser Date.now(), refreshed each second while `active` and the tab is visible. */
export function useSecondClock(active: boolean): number {
  return useSyncExternalStore(active ? subscribe : idle, snapshot, snapshot)
}
