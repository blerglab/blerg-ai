// useKeptScroll: the scroll positions inside a view (every element under `root` that the reader
// scrolled, found again by its place in the tree) kept in memory per session and key (model/viewCache),
// so leaving and coming back to a session puts them back (#281): a file, diff or commit tab, the
// Files list, a panel tab. They are put back before the first paint when the content is there, or
// as soon as it lays out (a view that is hidden, or still loading), until the reader scrolls
// themselves; then they follow the reader again.
import { useEffect, useLayoutEffect, useRef, type RefObject } from 'react'
import { contentKeeper, peekContent } from '../model/viewCache'

interface Place { path: number[]; top: number; left: number }
/** How long a kept place waits for its content to lay out. */
const WAIT_MS = 4000

function pathOf(root: Element, el: Element): number[] | null {
  const path: number[] = []
  for (let node: Element | null = el; node && node !== root; node = node.parentElement) {
    const parent: Element | null = node.parentElement
    if (!parent) return null
    path.unshift(Array.prototype.indexOf.call(parent.children, node))
  }
  return path
}
function atPath(root: Element, path: number[]): Element | null {
  let node: Element | null = root
  for (const i of path) { node = node?.children[i] ?? null; if (!node) return null }
  return node
}

/**
 * keep: where the places are kept (the transport's files object or the transport, the session, a key
 * naming the view); null keeps nothing. active: the view is on screen (a hidden one is put back when
 * it shows).
 */
export function useKeptScroll(root: RefObject<HTMLElement | null>, keep: { scope: object; sessionId: string; key: string } | null, active = true): void {
  // Each key's places, apart: a key's leaving writes its own, never the next key's (#281).
  const placesByKey = useRef(new Map<string, Map<string, Place>>())
  const pending = useRef<Place[] | null>(null)
  const scope = keep?.scope, sessionId = keep?.sessionId, key = keep?.key
  // What was kept for this view: read once per key.
  useLayoutEffect(() => {
    if (!scope || !sessionId || !key) return
    const kept = peekContent<Place[]>(scope, sessionId, `scroll:${key}`)
    placesByKey.current.set(key, new Map((kept ?? []).map(p => [p.path.join('/'), p])))
    pending.current = kept && kept.length ? kept : null
  }, [scope, sessionId, key])
  // Puts the kept places back while the view is on screen; content that lays out later gets them then.
  useLayoutEffect(() => {
    const el = root.current
    if (!el || !active || !pending.current) return
    const apply = () => {
      const left = (pending.current ?? []).filter(p => {
        const target = atPath(el, p.path) as HTMLElement | null
        if (!target) return true
        target.scrollTop = p.top
        target.scrollLeft = p.left
        return Math.abs(target.scrollTop - p.top) > 1 || Math.abs(target.scrollLeft - p.left) > 1
      })
      pending.current = left.length ? left : null
      return !pending.current
    }
    if (apply()) return
    let done = false
    const stop = () => { done = true; observer.disconnect(); resize.disconnect(); clearTimeout(timer); el.removeEventListener('wheel', stop, true); el.removeEventListener('touchstart', stop, true); el.removeEventListener('keydown', stop, true) }
    const retry = () => { if (!done && apply()) stop() }
    const observer = new MutationObserver(retry)
    observer.observe(el, { childList: true, subtree: true })
    const resize = new ResizeObserver(retry)
    resize.observe(el)
    // The reader scrolled first: their place wins.
    el.addEventListener('wheel', stop, true); el.addEventListener('touchstart', stop, true); el.addEventListener('keydown', stop, true)
    const timer = setTimeout(() => { pending.current = null; stop() }, WAIT_MS)
    return stop
  }, [root, active, scope, sessionId, key])
  // Follows the reader's scrolling: each scrolled element's place, kept for the next visit.
  useEffect(() => {
    const el = root.current
    if (!el || !scope || !sessionId || !key) return
    let frame = 0
    const mine = placesByKey.current.get(key) ?? new Map<string, Place>()
    placesByKey.current.set(key, mine)
    const onScroll = (event: Event) => {
      const target = event.target as Element
      if (!(target instanceof Element) || !el.contains(target)) return
      const path = target === el ? [] : pathOf(el, target)
      if (!path) return
      mine.set(path.join('/'), { path, top: target.scrollTop, left: target.scrollLeft })
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => {
        const list = [...mine.values()]
        contentKeeper<Place[]>(scope, sessionId, `scroll:${key}`)(list, 64 + list.length * 32)
      })
    }
    el.addEventListener('scroll', onScroll, true)
    return () => {
      el.removeEventListener('scroll', onScroll, true)
      cancelAnimationFrame(frame)
      const list = [...mine.values()]
      if (list.length) contentKeeper<Place[]>(scope, sessionId, `scroll:${key}`)(list, 64 + list.length * 32)
    }
  }, [root, scope, sessionId, key])
}
