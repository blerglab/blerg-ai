import { useEffect, useState } from 'react'

/** useTicker returns Date.now(), refreshed every second while active. */
export function useTicker(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [active])
  return now
}

const REDUCED_MOTION = '(prefers-reduced-motion: reduce)'

function reducedMotionNow(): boolean {
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia(REDUCED_MOTION).matches
}

/** usePrefersReducedMotion tracks the OS "reduce motion" setting. */
export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(reducedMotionNow)
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const mq = window.matchMedia(REDUCED_MOTION)
    const on = () => setReduced(mq.matches)
    mq.addEventListener?.('change', on)
    return () => mq.removeEventListener?.('change', on)
  }, [])
  return reduced
}

/** useMinuteClock returns server time (Date.now() - offset) floored to the minute, refreshed at each
 *  minute boundary while active (like useTicker, it holds its last value while not). A view that
 *  needs the time only to judge what has gone stale (12 h) uses it instead of useTicker, so the
 *  second-by-second clock stays in the components that show elapsed time and does not re-render
 *  the whole view. */
export function useMinuteClock(active: boolean, offset: number): number {
  const [now, setNow] = useState(() => Date.now())
  const minute = Math.floor((now - offset) / 60_000) * 60_000
  useEffect(() => {
    if (!active) return
    const t = setTimeout(() => setNow(Date.now()), Math.max(1, minute + 60_000 + offset - Date.now()))
    return () => clearTimeout(t)
  }, [active, now, minute, offset]) // now: a timer that fires early is set again, not dropped
  return minute
}
