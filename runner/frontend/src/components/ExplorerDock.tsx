import { useEffect, useRef, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import type { ExplorerLayout as Layout } from '../hooks/useExplorerLayout'
import WorkspaceTree from './WorkspaceTree'
import './ExplorerDock.css'

export function ExplorerToggle({ layout: { triggerRef, open, id, toggle } }: { layout: Layout }) {
  return <button ref={triggerRef} type="button" className="icon-btn" aria-label="Toggle explorer" aria-expanded={open} aria-controls={id} title="Toggle explorer" onClick={toggle}>
    <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true"><path d="M3 4h7l2 3h9v13H3z" /><path d="M8 7v13" /></svg>
  </button>
}

export default function ExplorerDock({ sessionId, layout }: { sessionId: string; layout: Layout }) {
  return layout.open ? <ExplorerBody sessionId={sessionId} layout={layout} /> : null
}
function ExplorerBody({ sessionId, layout }: { sessionId: string; layout: Layout }) {
  const root = useRef<HTMLElement>(null)
  const drag = useRef<{ x: number; width: number; next: number } | null>(null)
  const { mobile, triggerRef } = layout
  useExplorerFocus(root, triggerRef, mobile)
  const body = <section ref={root} id={layout.id} className={`explorer-dock${mobile ? ' explorer-sheet' : ''}`} style={mobile ? undefined : { width: layout.width }} role={mobile ? 'dialog' : 'region'} aria-modal={mobile || undefined} aria-label="Explorer" onKeyDown={event => {
    // A shared file viewer has its own dialog and keyboard ownership.
    if (event.defaultPrevented || (event.target as Element).closest('[data-testid="workspace-viewer-overlay"]')) return
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); layout.close() }
    if (mobile && event.key === 'Tab') {
      const stops = [...root.current!.querySelectorAll<HTMLElement>('button:not(:disabled):not([tabindex="-1"]), input:not(:disabled), [tabindex="0"]')]
      const first = stops[0], last = stops.at(-1)
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
    }
  }}>
    <button type="button" className="explorer-collapse" aria-label="Collapse explorer" title="Collapse explorer; reopen from the session header or command palette" onClick={layout.close}><svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.3" aria-hidden="true"><path d="m10 4-4 4 4 4" /></svg></button>
    <div className="explorer-body"><WorkspaceTree sessionId={sessionId} active /></div>
    {!mobile && <div className="explorer-resize" role="separator" tabIndex={0} aria-label="Explorer width" aria-orientation="vertical" aria-valuemin={Math.round(layout.bounds.min)} aria-valuemax={Math.round(layout.bounds.max)} aria-valuenow={layout.width}
      onKeyDown={event => {
        let width: number | undefined
        if (event.key === 'ArrowLeft') width = layout.width - (event.shiftKey ? 80 : 20)
        if (event.key === 'ArrowRight') width = layout.width + (event.shiftKey ? 80 : 20)
        if (event.key === 'Home') width = layout.bounds.min
        if (event.key === 'End') width = layout.bounds.max
        if (width !== undefined) { event.preventDefault(); layout.resize(width) }
      }}
      onPointerDown={event => { if (event.button !== 0) return; event.preventDefault(); event.currentTarget.focus(); event.currentTarget.setPointerCapture(event.pointerId); drag.current = { x: event.clientX, width: layout.width, next: layout.width } }}
      onPointerMove={event => { if (drag.current) { drag.current.next = drag.current.width + event.clientX - drag.current.x; layout.resize(drag.current.next) } }}
      onPointerUp={event => { const collapse = drag.current && drag.current.next < layout.bounds.min * .45; drag.current = null; event.currentTarget.releasePointerCapture(event.pointerId); if (collapse) layout.close() }}
      onPointerCancel={() => { drag.current = null }} onLostPointerCapture={() => { drag.current = null }} />}
  </section>
  return mobile ? createPortal(body, document.body) : body
}

function useExplorerFocus(root: RefObject<HTMLElement | null>, triggerRef: RefObject<HTMLButtonElement | null>, mobile: boolean) {
  useEffect(() => {
    const button = triggerRef.current
    const previous = document.activeElement as HTMLElement | null
    const sessionRoot = button?.closest<HTMLElement>('[data-session-root]')
    const wasInert = sessionRoot?.inert ?? false
    const overflow = document.body.style.overflow
    if (mobile) {
      if (sessionRoot) sessionRoot.inert = true
      document.body.style.overflow = 'hidden'
      root.current?.querySelector<HTMLInputElement>('input')?.focus()
    } else root.current?.querySelector<HTMLInputElement>('input')?.focus()
    return () => {
      if (mobile) {
        if (sessionRoot) sessionRoot.inert = wasInert
        document.body.style.overflow = overflow
      }
      if (button?.isConnected) button.focus()
      else if (previous?.isConnected) previous.focus()
    }
  }, [root, triggerRef, mobile])
}
