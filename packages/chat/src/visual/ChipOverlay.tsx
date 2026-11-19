// BLG-485: a chart or diagram draws its own DOM imperatively (an SVG string, a plain HTML table
// cell) — it can mark a candidate's screen position (`data-blerg-chip-key`, the exact `RefKey`
// refChip.ts/refs.ts already produce) but it cannot render a React hovercard itself. This scans
// a container for that mark and, for each one the store actually resolves (a known card or
// commit — an unresolved key stays exactly as drawn, same as unconfirmed chat text), lays an
// invisible, focusable proxy over it: `Hover` (Hovercard.tsx) anchors its popup to *that* proxy's
// own box, so the existing hover/focus/click/singleton machinery chat text already has needs no
// parallel implementation here. The underlying element's own `tabindex` is turned off for the
// time a resolved proxy sits over it (restored once it isn't), so a resolved chip has exactly one
// tab stop, not two.
//
// Rendered as a plain sibling of the surface it scans (VisualBlock.tsx), never a child placed
// inside it: that surface is redrawn by replacing *all* of its children wholesale (Surface's own
// draw()), and a React-portaled child caught in that churn raced React's own commit against it
// (measured: a removeChild NotFoundError once two resolved chips shared a key before the id
// WeakMap below existed, and intermittently even after — React expects to own whatever it last
// appended, draw() doesn't know that). A sibling is never touched by that replacement at all.
import { useEffect, useLayoutEffect, useState, useSyncExternalStore, type CSSProperties, type RefObject } from 'react'
import { Hover } from '../components/Hovercard'
import { useCommitOpener } from '../components/CommitViewer'
import { useRefContext } from '../components/RefLinks'
import type { RefKey } from '../model/refs'

interface Spot { id: number; key: RefKey; el: Element; left: number; top: number; width: number; height: number }

function sameSpots(a: readonly Spot[], b: readonly Spot[]): boolean {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    const x = a[i], y = b[i]
    if (x.key !== y.key || x.el !== y.el || x.left !== y.left || x.top !== y.top || x.width !== y.width || x.height !== y.height) return false
  }
  return true
}

// The same reference can be drawn more than once (a bar chart's axis tick and its data-table
// row both naming #452) — a React list key needs the *element*'s own identity, not just the
// RefKey it resolves to. A WeakMap outlives a rescan without leaking once an element is gone.
let nextSpotId = 0
const spotIds = new WeakMap<Element, number>()
function idFor(el: Element): number {
  let id = spotIds.get(el)
  if (id === undefined) { id = ++nextSpotId; spotIds.set(el, id) }
  return id
}

/** `root`: the already-rendered container (a chart/diagram surface) to scan for
 *  `[data-blerg-chip-key]` elements — re-scanned on resize and on any DOM mutation inside it (a
 *  redraw, a tooltip appearing), so a chip that doesn't exist yet when this mounts is still found
 *  once it does. */
export function ChipOverlay({ root }: { root: RefObject<HTMLElement | null> }) {
  const [spots, setSpots] = useState<readonly Spot[]>([])
  useLayoutEffect(() => {
    const node = root.current
    if (!node) return
    let stopped = false
    const rescan = () => {
      if (stopped || !node.isConnected) return
      const base = node.getBoundingClientRect()
      const found: Spot[] = []
      for (const el of node.querySelectorAll('[data-blerg-chip-key]')) {
        const key = el.getAttribute('data-blerg-chip-key')
        if (!key) continue
        const r = el.getBoundingClientRect()
        if (!r.width || !r.height) continue
        found.push({ id: idFor(el), key: key as RefKey, el, left: r.left - base.left, top: r.top - base.top, width: r.width, height: r.height })
      }
      setSpots(previous => (sameSpots(previous, found) ? previous : found))
    }
    rescan()
    const ro = new ResizeObserver(rescan)
    ro.observe(node)
    const mo = new MutationObserver(rescan)
    mo.observe(node, { childList: true, subtree: true, attributes: true, attributeFilter: ['data-blerg-chip-key'] })
    return () => { stopped = true; ro.disconnect(); mo.disconnect() }
  }, [root])
  return <div className="visual-chip-overlay">{spots.map(spot => <ChipHotspot key={spot.id} spot={spot} />)}</div>
}

const none = () => () => {}

function ChipHotspot({ spot }: { spot: Spot }) {
  const ctx = useRefContext()
  const commitOpener = useCommitOpener()
  const store = ctx?.store
  const state = useSyncExternalStore(store ? store.subscribe : none, () => store?.get(spot.key) ?? null, () => null)
  useEffect(() => { store?.want(spot.key) }, [store, spot.key])
  const resolved = !!ctx && (state?.state === 'card' || state?.state === 'commit')
  // The real element stays the sole keyboard stop until (and unless) this proxy takes over.
  useEffect(() => {
    if (!resolved) return
    const original = spot.el.getAttribute('tabindex')
    spot.el.setAttribute('tabindex', '-1')
    spot.el.setAttribute('aria-hidden', 'true')
    return () => {
      if (original === null) spot.el.removeAttribute('tabindex')
      else spot.el.setAttribute('tabindex', original)
      spot.el.removeAttribute('aria-hidden')
    }
  }, [resolved, spot.el])
  if (!ctx || !state) return null
  const style: CSSProperties = { position: 'absolute', left: spot.left, top: spot.top, width: spot.width, height: spot.height }
  if (state.state === 'card' && ctx.refs?.openCard) {
    const open = ctx.refs.openCard
    const target = state.target
    return (
      <Hover target={target}>
        {/* button-guard-ignore: an invisible proxy over a chart/diagram chip (ChipOverlay) — opens the same card RefSpan does. */}
        <button type="button" className="visual-chip-hotspot" style={style} data-testid="visual-chip-hotspot" data-chip-key={spot.key}
          aria-label={`Open card ${target.key}${target.title ? `: ${target.title}` : ''}`}
          onClick={e => { e.stopPropagation(); open(target) }} />
      </Hover>
    )
  }
  if (state.state === 'commit' && commitOpener) {
    const sha = state.sha
    return (
      <Hover target={{ kind: 'commit', sessionId: ctx.sessionId, sha }}>
        {/* button-guard-ignore: an invisible proxy over a chart/diagram chip (ChipOverlay) — opens the same commit RefSpan does. */}
        <button type="button" className="visual-chip-hotspot" style={style} data-testid="visual-chip-hotspot" data-chip-key={spot.key}
          aria-label={`Open commit ${sha.slice(0, 7)}`}
          onClick={e => { e.stopPropagation(); commitOpener(ctx.sessionId, sha, undefined, ctx) }} />
      </Hover>
    )
  }
  return null
}
