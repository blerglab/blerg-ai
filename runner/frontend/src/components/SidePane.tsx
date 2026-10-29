import { useEffect, useRef, type ReactNode, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import type { PaneLayout } from '../hooks/usePaneLayout'
import { ownsAppShortcut } from '../commands/shortcutOwnership'
import './SidePane.css'

type Props = { layout: PaneLayout; label: string; toggleLabel: string; icon: ReactNode; children: ReactNode; side?: 'left' | 'right' | 'bottom'; persistentRail?: boolean; hideRail?: boolean; railHost?: HTMLElement | null; keepMounted?: boolean; plain?: boolean; focusOnOpen?: boolean; hideCollapse?: boolean; as?: 'section' | 'aside'; testId?: string; hoverBoundary?: boolean; railLabel?: string; railActions?: ReactNode; className?: string; contentRef?: RefObject<HTMLElement | null> }
export function PaneCollapse({ label, close, paneId, side = 'left' }: { label: string; close(): void; paneId: string; side?: 'left' | 'right' | 'bottom' }) {
  return <button type="button" className={`pane-collapse pane-collapse-${side}`} aria-label={`Collapse ${label}`} aria-expanded={true} aria-controls={paneId} title={`Collapse ${label}`} onClick={close}><svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.3" aria-hidden="true"><path d={side === 'bottom' ? 'm4 6 4 4 4-4' : side === 'left' ? 'm10 4-4 4 4 4' : 'm6 4 4 4-4 4'} /></svg></button>
}
export function PaneToggle({ layout: { triggerRef, open, id, toggle }, label, icon }: { layout: PaneLayout; label: string; icon: ReactNode }) {
  return <button ref={triggerRef} type="button" className="icon-btn pane-toggle" aria-label={label} aria-expanded={open} aria-controls={id} title={label} onClick={toggle}>{icon}</button>
}
export default function SidePane({ layout, label, toggleLabel, icon, children, side = 'left', persistentRail = false, hideRail = false, railHost, keepMounted = false, plain = false, focusOnOpen = true, hideCollapse = false, as: Element = 'section', testId, hoverBoundary, railLabel = `${label} tools`, railActions, className = '', contentRef }: Props) {
  const ownContent = useRef<HTMLElement>(null)
  const paneRef = contentRef ?? ownContent
  const wasOpen = useRef(layout.open)
  useEffect(() => {
    if (wasOpen.current && !layout.open) layout.triggerRef.current?.focus()
    if (focusOnOpen && !wasOpen.current && layout.open) paneRef.current?.querySelector<HTMLElement>('input, button')?.focus()
    wasOpen.current = layout.open
  }, [layout.open, layout.triggerRef, paneRef, focusOnOpen])
  const drag = useRef<{ x: number; width: number; next: number } | null>(null)
  const rail = !plain && !hideRail && (!layout.open || persistentRail) && <div className={`pane-rail pane-rail-${side}`} role="toolbar" aria-label={railLabel} aria-orientation={side === 'bottom' ? 'horizontal' : 'vertical'}><PaneToggle layout={layout} label={toggleLabel} icon={icon} />{railActions}</div>
  const body = (plain || layout.open || keepMounted) && <Element data-testid={testId} data-hc-boundary={hoverBoundary ? '' : undefined} hidden={!plain && !layout.open} ref={paneRef} id={layout.id} className={plain ? 'pane-plain' : `side-pane side-pane-${side} ${className}`} style={plain ? { display: 'contents' } : side === 'bottom' ? { height: layout.width } : { width: layout.width }} role={plain ? undefined : 'region'} aria-label={plain ? undefined : label} onKeyDown={event => {
    if (!plain && event.key === 'Escape' && !event.defaultPrevented && !ownsAppShortcut(event.nativeEvent) && !(event.target as Element).closest('[data-testid="workspace-viewer-overlay"]')) { event.preventDefault(); event.stopPropagation(); layout.close() }
  }}>
    {!plain && !hideCollapse && <PaneCollapse label={label.toLowerCase()} close={layout.close} paneId={layout.id} side={side} />}
    {children}
    {!plain && <div className={`pane-resize pane-resize-${side}`} role="separator" tabIndex={0} aria-label={`${label} ${side === 'bottom' ? 'height' : 'width'}`} aria-orientation={side === 'bottom' ? 'horizontal' : 'vertical'} aria-valuemin={Math.round(layout.bounds.min)} aria-valuemax={Math.round(layout.bounds.max)} aria-valuenow={layout.width}
      onDoubleClick={layout.reset}
      onKeyDown={event => {
        let width: number | undefined
        if (event.key === (side === 'bottom' ? 'ArrowDown' : 'ArrowLeft')) width = layout.width + (side === 'right' ? 1 : -1) * (event.shiftKey ? 80 : 20)
        if (event.key === (side === 'bottom' ? 'ArrowUp' : 'ArrowRight')) width = layout.width + (side === 'right' ? -1 : 1) * (event.shiftKey ? 80 : 20)
        if (event.key === 'Home') width = layout.bounds.min
        if (event.key === 'End') width = layout.bounds.max
        if (width !== undefined) { event.preventDefault(); layout.resize(width) }
      }}
      onPointerDown={event => { if (event.button !== 0) return; event.preventDefault(); event.currentTarget.focus(); event.currentTarget.setPointerCapture(event.pointerId); drag.current = { x: side === 'bottom' ? event.clientY : event.clientX, width: layout.width, next: layout.width } }}
      onPointerMove={event => { if (drag.current) { drag.current.next = drag.current.width + ((side === 'bottom' ? event.clientY : event.clientX) - drag.current.x) * (side === 'left' ? 1 : -1); layout.resize(drag.current.next) } }}
      onPointerUp={event => { const collapse = drag.current && drag.current.next < layout.bounds.min * .45; drag.current = null; event.currentTarget.releasePointerCapture(event.pointerId); if (collapse) layout.close() }}
      onPointerCancel={() => { drag.current = null }} onLostPointerCapture={() => { drag.current = null }} />}
  </Element>
  const placedRail = railHost && rail ? createPortal(<>{rail.props.children}</>, railHost) : rail
  return side === 'left' ? <>{placedRail}{body}</> : <>{body}{placedRail}</>
}
