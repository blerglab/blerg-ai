// A manual clock for hovercards' timing in browser tests (setHovercardClock): nothing hovercards
// wait for happens until the test advances the clock, so a timing test is decided by the clock,
// not by how loaded the machine is. The pointer is still real (CDP or userEvent).
import { setHovercardClock, type HovercardClock } from '@blerglab/chat'

export interface ManualHoverClock {
  /** Runs every timer due within `ms`, in order, and moves the clock on. */
  advance(ms: number): void
  /** Timers waiting. */
  pending(): number
  uninstall(): void
}

export function installHoverClock(): ManualHoverClock {
  let t = 0
  let seq = 0
  const timers = new Map<number, { at: number; fn: () => void }>()
  const clock: HovercardClock = {
    now: () => t,
    setTimeout: (fn, ms) => { const id = ++seq; timers.set(id, { at: t + Math.max(0, ms), fn }); return id },
    clearTimeout: id => { timers.delete(id as number) },
  }
  setHovercardClock(clock)
  return {
    advance(ms) {
      const end = t + ms
      for (;;) {
        let next: [number, { at: number; fn: () => void }] | null = null
        for (const e of timers) if (e[1].at <= end && (!next || e[1].at < next[1].at || (e[1].at === next[1].at && e[0] < next[0]))) next = e
        if (!next) break
        timers.delete(next[0])
        t = next[1].at
        next[1].fn()
      }
      t = end
    },
    pending: () => timers.size,
    uninstall: () => setHovercardClock(null),
  }
}
