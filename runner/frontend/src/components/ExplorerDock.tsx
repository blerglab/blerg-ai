import { useEffect, useRef, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import type { ExplorerLayout as Layout } from '../hooks/useExplorerLayout'
import WorkspaceTree from './WorkspaceTree'
import './ExplorerDock.css'
import SessionDockWindows from './SessionDockWindows'
import type { SessionInfo } from '../types'
import SidePane, { PaneCollapse } from './SidePane'

const shown = (element: HTMLElement | null): element is HTMLElement => !!element && (typeof element.checkVisibility === 'function' ? element.checkVisibility() : !element.closest('[hidden]'))
const explorerIcon = <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true"><path d="M3 4h7l2 3h9v13H3z" /><path d="M8 7v13" /></svg>
export default function ExplorerDock({ sessionId, layout, onOpen, selectedSessionId }: { sessionId: string; selectedSessionId?: string; layout: Layout; onOpen?(session: SessionInfo, keep?: boolean): void }) {
  return <ExplorerBody sessionId={sessionId} selectedSessionId={selectedSessionId} layout={layout} onOpen={onOpen} />
}
function ExplorerBody({ sessionId, layout, onOpen, selectedSessionId }: { sessionId: string; selectedSessionId?: string; layout: Layout; onOpen?(session: SessionInfo, keep?: boolean): void }) {
  const root = useRef<HTMLElement>(null)
  const { mobile, triggerRef } = layout
  useExplorerFocus(root, triggerRef, mobile, layout.open, layout.close)
  const files = <><div className="explorer-body"><WorkspaceTree sessionId={sessionId} active onFileOpen={mobile ? layout.close : undefined} /></div><p className="explorer-hint">Click a file to preview.<br />Double-click to keep it open.{mobile && <><br />On touch, double-tap the file tab to keep it open.</>}</p></>
  const children = onOpen ? <SessionDockWindows sessionId={sessionId} selectedSessionId={selectedSessionId} mobile={mobile} files={files} onOpen={onOpen} onHistory={mobile ? layout.closeForNavigation : undefined} /> : files
  if (!mobile) return <SidePane layout={layout} label="Explorer" toggleLabel="Toggle explorer" icon={explorerIcon} hideRail hideCollapse className="explorer-dock" contentRef={root} focusOnOpen={false}>{children}</SidePane>
  if (!layout.open) return null
  const body = <section ref={root} id={layout.id} className={`explorer-dock${mobile ? ' explorer-sheet' : ''}`} style={mobile ? undefined : { width: layout.width }} role={mobile ? 'dialog' : 'region'} aria-modal={mobile || undefined} aria-label="Explorer" onKeyDown={event => {
    // A shared file viewer has its own dialog and keyboard ownership.
    if (event.defaultPrevented || (event.target as Element).closest('[data-testid="workspace-viewer-overlay"]')) return
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); layout.close() }
    if (mobile && event.key === 'Tab') {
      const stops = [...root.current!.querySelectorAll<HTMLElement>('button:not(:disabled):not([tabindex="-1"]), input:not(:disabled), a[href], [tabindex="0"]')].filter(shown)
      const first = stops[0], last = stops.at(-1)
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
    }
  }}>
    <header className="explorer-sheet-header"><PaneCollapse label="explorer" close={layout.close} paneId={layout.id} /><strong>Explorer</strong></header>
    {children}
  </section>
  return mobile ? createPortal(body, document.body) : body
}

function useExplorerFocus(root: RefObject<HTMLElement | null>, triggerRef: RefObject<HTMLButtonElement | null>, mobile: boolean, open: boolean, close: () => void) {
  const closeRef = useRef(close)
  useEffect(() => { closeRef.current = close }, [close])
  useEffect(() => {
    if (!open) return
    const button = triggerRef.current
    const previous = document.activeElement as HTMLElement | null
    const sessionRoot = button?.closest<HTMLElement>('[data-session-root]')
    const wasInert = sessionRoot?.inert ?? false
    const overflow = document.body.style.overflow
    if (mobile) {
      history.pushState({ ...history.state, blergSheet: 'explorer' }, '')
      if (sessionRoot) sessionRoot.inert = true
      document.body.style.overflow = 'hidden'
    }
    // Opening by a click may focus the search; a restored open pane never does.
    const content = root.current
    const input = content?.querySelector<HTMLInputElement>('input') ?? null
    if (mobile || previous === button) (shown(input) ? input : content?.querySelector<HTMLElement>('[role=tab][aria-selected=true], button'))?.focus()
    const back = () => { if (mobile && history.state?.blergSheet !== 'explorer') closeRef.current() }
    window.addEventListener('popstate', back)
    return () => {
      window.removeEventListener('popstate', back)
      if (mobile && history.state?.blergSheet === 'explorer') history.replaceState({ ...history.state, blergSheet: undefined }, '')
      if (mobile) {
        if (sessionRoot) sessionRoot.inert = wasInert
        document.body.style.overflow = overflow
      }
      if (mobile && button?.isConnected && (!document.activeElement || document.activeElement === document.body || content?.contains(document.activeElement))) button.focus()
    }
  }, [root, triggerRef, mobile, open])
}
