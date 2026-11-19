import {useLayoutEffect, useRef} from 'react'

const format = (n: number) => new Intl.NumberFormat('en-US', {maximumFractionDigits: 2}).format(n)
/** Only text content moves. Card, value node and meter remain mounted. */
export function useScalarMotion(value: number | null) {
  const ref = useRef<HTMLSpanElement>(null)
  const previous = useRef<number | null>(null)
  useLayoutEffect(() => {
    const node = ref.current, from = previous.current
    if (!node) return
    if (value === null) { previous.current = null; node.textContent = 'Unknown'; return }
    const duration = parseFloat(getComputedStyle(node).getPropertyValue('--chat-data-transition-ms'))
    if (from === null || from === value || !Number.isFinite(duration) || duration <= 0 || matchMedia('(prefers-reduced-motion: reduce)').matches) {
      previous.current = value; node.textContent = format(value); return
    }
    let frame = 0, start: number | undefined
    node.textContent = format(from)
    const paint = (time: number) => {
      start ??= time
      const progress = Math.min(1, (time - start) / duration)
      previous.current = from + (value - from) * (1 - (1 - progress) ** 3)
      node.textContent = new Intl.NumberFormat('en-US', {maximumFractionDigits: Number.isInteger(from) && Number.isInteger(value) ? 0 : 2}).format(previous.current)
      if (progress < 1) frame = requestAnimationFrame(paint)
    }
    frame = requestAnimationFrame(paint)
    return () => cancelAnimationFrame(frame)
  }, [value])
  return ref
}
