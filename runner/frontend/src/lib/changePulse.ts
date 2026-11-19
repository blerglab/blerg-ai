import { useLayoutEffect, useRef, type RefObject } from 'react'
import './changePulse.css'

export function pulse(element: HTMLElement) {
 element.classList.remove('change-pulse')
 // Restart on a second change while the first highlight is still visible.
 void element.offsetWidth
 element.classList.add('change-pulse')
 element.addEventListener('animationend', () => element.classList.remove('change-pulse'), { once: true })
}
/** Compare committed snapshots; initial data never flashes on page load. */
export function useChangePulses(root: RefObject<HTMLElement | null>, values: ReadonlyMap<string, string>, attribute: string, ready = true) {
 const previous = useRef<ReadonlyMap<string,string> | null>(null)
 useLayoutEffect(() => {
  if (!ready) { previous.current = null; return }
  const before = previous.current
  previous.current = values
  if (!before || !root.current) return
  for (const element of root.current.querySelectorAll<HTMLElement>(`[${attribute}]`)) {
   const key = element.getAttribute(attribute)!
   if (values.has(key) && before.get(key) !== values.get(key)) pulse(element)
  }
 }, [root, values, attribute, ready])
}
