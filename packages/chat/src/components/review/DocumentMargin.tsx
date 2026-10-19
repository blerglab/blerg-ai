// One scrolling page and margin: comment cards follow their text, with collision spacing.
import { useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from 'react'
import { findQuote } from '../../model/anchor'
import type { Anchor, ReviewRequest } from '../../model/review'
import RequestList from './RequestList'

interface Props {
  draftId?: string
  onReopen?(id: string, explanation: string): void
  busy?: boolean
  pending?: ReadonlySet<string>
  rich?: boolean
  canLocate?(id: string): boolean
  resolveRef?: RefObject<((id: string, anchor: Anchor) => Range | null) | null>
  root: RefObject<HTMLDivElement | null>
  requests: ReviewRequest[]
  draft: ReactNode
  draftAnchor?: Anchor
  focusedId: string | null
  onFocus(id: string | null): void
  deletable: ReadonlySet<string>
  onDelete(id: string): void
  updateKey: string
}
function locate(root: HTMLElement, anchor: Anchor): Range | null {
  const page = anchor.page ? root.querySelector<HTMLElement>(`.pdf-page[data-page="${anchor.page}"] .textLayer`) : root.querySelector<HTMLElement>('[data-testid="review-page"]')
  return page ? findQuote(page, anchor) : null
}
export default function DocumentMargin({onReopen, busy, pending, draftId, rich, canLocate, resolveRef, root, requests, draft, draftAnchor, focusedId, onFocus, deletable, onDelete, updateKey}: Props) {
  const margin = useRef<HTMLElement | null>(null)
  const [layout, setLayout] = useState<{tops: number[]; found: boolean[]; height: number}>({tops: [], found: [], height: 0})
  const items = [...requests].sort((a,b) => a.createdAt < b.createdAt ? 1 : -1)
  useLayoutEffect(() => {
    const doc = root.current, side = margin.current
    if (!doc || !side) return
    const measure = () => {
      const origin = side.getBoundingClientRect().top
      const anchors = [...(draftAnchor ? [draftAnchor] : []), ...items.map(r => r.anchor)]
      const ranges: Range[] = [], active: Range[] = []
      const targets = anchors.map((anchor, index) => {
        const request = draftAnchor && index === 0 ? null : items[index - (draftAnchor ? 1 : 0)]
        const range = request ? (rich ? resolveRef?.current?.(request.id, anchor) ?? null : canLocate?.(request.id) === false ? null : locate(doc, anchor)) : rich ? (draftId ? resolveRef?.current?.(draftId, anchor) ?? null : null) : draftId && canLocate?.(draftId) === false ? null : locate(doc, anchor)
        if (range) { ranges.push(range); if (!request || request.id === focusedId) active.push(range) }
        const rect = range?.getBoundingClientRect?.()
        return {index, y: rect ? Math.max(0, rect.top - origin) : 0, found: !!range}
      }).sort((a,b) => a.y - b.y || a.index - b.index)
      const api = globalThis as unknown as {CSS?: {highlights?: Map<string, unknown>}; Highlight?: new (...ranges: Range[]) => unknown}
      if (api.CSS?.highlights && api.Highlight) {
        api.CSS.highlights.set('chat-document-comments', new api.Highlight(...ranges))
        api.CSS.highlights.set('chat-document-active', new api.Highlight(...active))
      }
      const tops: number[] = [], found: boolean[] = []
      let end = 0
      for (const target of targets) {
        tops[target.index] = Math.max(target.y, end)
        found[target.index] = target.found
        const el = side.querySelector<HTMLElement>(`[data-margin-index="${target.index}"]`)
        end = tops[target.index] + (el?.getBoundingClientRect().height ?? 120) + 12
      }
      setLayout(previous => {
        const next = {tops, found, height: end}
        return JSON.stringify(previous) === JSON.stringify(next) ? previous : next
      })
    }
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(doc);ro.observe(side)
    for (const card of side.children) ro.observe(card)
    const mo = new MutationObserver(measure)
    mo.observe(doc, {childList: true, subtree: true, characterData: true})
    return () => {ro.disconnect();mo.disconnect(); const api = globalThis as unknown as {CSS?: {highlights?: Map<string, unknown>}}; api.CSS?.highlights?.delete('chat-document-comments');api.CSS?.highlights?.delete('chat-document-active')}
  // The ordered IDs and source bytes determine which anchors need measuring.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draftId, focusedId, root, requests, draftAnchor, updateKey, draft !== null, rich, canLocate, resolveRef])
  function reveal(r: ReviewRequest) {
    const range = rich ? resolveRef?.current?.(r.id, r.anchor) : canLocate?.(r.id) === false ? null : root.current && locate(root.current, r.anchor)
    const el = range?.startContainer.parentElement
    el?.scrollIntoView?.({block: 'center', behavior: 'smooth'})
    onFocus(r.id)
  }
  const shift = draftAnchor ? 1 : 0
  return <aside ref={margin} className="document-margin" aria-label="Comments" style={{minHeight: layout.height}}>
    {draft && <div className="document-margin-card is-draft" data-margin-index="0" style={{top: layout.tops[0] ?? 0}}>{draft}</div>}
    {items.map((r,index) => <div key={r.id} className="document-margin-card" data-margin-index={index+shift} style={{top: layout.tops[index+shift] ?? 0}}>
      <button type="button" className="document-comment-link" onClick={() => reveal(r)} disabled={!layout.found[index+shift]}>
        {layout.found[index+shift] ? `Show passage${r.anchor.page ? ` · page ${r.anchor.page}` : ''}` : 'Original passage unavailable'}
      </button>
      <RequestList onReopen={onReopen} busy={busy} pending={pending} requests={[r]} focusedId={focusedId} onHover={onFocus} deletable={deletable} onDelete={onDelete}/>
    </div>)}
    {!items.length && !draft && <p className="review-empty">Select text and choose Add comment, or press Ctrl+Alt+M.</p>}
  </aside>
}
