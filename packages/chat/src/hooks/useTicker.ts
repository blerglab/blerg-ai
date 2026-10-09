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
