import {useEffect, useId, useRef, useState, type RefObject} from 'react'
import {createPortal} from 'react-dom'
import {apiFetch} from '../apiFetch'
import {refreshChildren} from '../hooks/useChildrenSignal'
import './ChildDiscard.css'

interface Props {
  parentId: string
  childId: string
  title: string
  menu?: boolean
  focusTarget?: RefObject<HTMLButtonElement | null>
  onOpen?(): void
}
interface SessionProps extends Omit<Props, 'parentId' | 'childId'> {
  sessionId: string
  onDiscarded?(): void
}

export function ChildDiscardAction({childId, ...props}: Props) {
  return <DiscardAction key={childId} {...props} sessionId={childId}/>
}

export function ParentDiscardAction(props: SessionProps) {
  return <DiscardAction key={props.sessionId} {...props}/>
}

/** A human-only action: never changes local session records ahead of the server's update. */
function DiscardAction({parentId, sessionId, title, menu = false, focusTarget, onOpen, onDiscarded}: SessionProps & {parentId?: string}) {
  const child = !!parentId
  const actionName = child ? 'Discard child session' : 'Discard session and children'
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [requested, setRequested] = useState(false)
  const [error, setError] = useState('')
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const trigger = useRef<HTMLButtonElement>(null)
  const dialog = useRef<HTMLDivElement>(null)
  const heading = useId()
  const description = useId()
  useEffect(() => {
    if (!open) return
    const restore = focusTarget?.current ?? trigger.current
    dialog.current?.querySelector<HTMLButtonElement>('button')?.focus()
    return () => {restore?.focus()}
  }, [open, focusTarget])
  useEffect(() => {if (open && busy) dialog.current?.focus()}, [open, busy])
  async function discard() {
    if (busy) return
    setBusy(true); setError('')
    try {
      const route = parentId ? `/api/sessions/${encodeURIComponent(parentId)}/children/${encodeURIComponent(sessionId)}/discard` : `/api/sessions/${encodeURIComponent(sessionId)}/discard`
      const response = await apiFetch(route, {method: 'POST'})
      if (!response.ok) {
        if (child && response.status === 409) {
          if (mounted.current) setError('This child is part of a pending parent discard. Retry discarding its parent to finish.')
          return
        }
        throw new Error('refused')
      }
      if (!child) {
        const receipt = await response.json() as {status?: string; session_id?: string; archived_at?: string}
        if (receipt?.status !== 'discarded' || receipt.session_id !== sessionId || typeof receipt.archived_at !== 'string' || !Number.isFinite(Date.parse(receipt.archived_at))) throw new Error('discard not confirmed')
      }
      refreshChildren(parentId || sessionId)
      if (!mounted.current) return
      setRequested(true); setOpen(false)
      onDiscarded?.()
    } catch {if (mounted.current) setError(child ? 'Could not discard this child session. Try again.' : 'Could not finish discarding this session and its children. Retry to complete any remaining steps.')}
    finally {if (mounted.current) setBusy(false)}
  }
  return <>
    <button ref={trigger} type="button" role={menu ? 'menuitem' : undefined} className="rail-link child-discard-trigger" aria-label={menu ? actionName : `Discard ${title}`} title={menu ? actionName : `Discard ${title}`} disabled={busy || requested} onClick={() => {setError(''); setOpen(true); onOpen?.()}}>{menu ? 'Discard' : <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7"/></svg>}</button>
    {requested && !menu && <span className="child-discard-status" role="status">Discard requested. Updating session list…</span>}
    {open && createPortal(<div className="child-discard-backdrop" onClick={event => {event.stopPropagation(); if (event.target === event.currentTarget && !busy) setOpen(false)}}>
      <div ref={dialog} className="child-discard-dialog" role="dialog" tabIndex={-1} aria-modal="true" aria-labelledby={heading} aria-describedby={description} onKeyDown={event => {
        if (event.key === 'Escape') {event.stopPropagation(); event.preventDefault(); if (!busy) setOpen(false)}
        if (event.key === 'Tab') {
          const buttons = [...(dialog.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? [])]
          if (!buttons.length) {event.preventDefault(); return}
          const first = buttons[0], last = buttons[buttons.length - 1]
          if (event.shiftKey && document.activeElement === first) {event.preventDefault(); last?.focus()}
          else if (!event.shiftKey && document.activeElement === last) {event.preventDefault(); first?.focus()}
        }
      }}>
        <h3 id={heading}>{child ? 'Discard child session?' : 'Discard session and all children?'}</h3>
        <p className="child-discard-title">{title}</p>
        <p id={description}>{child ? 'This stops any running work and removes this child from the active session lists. Its conversation, history and insights remain available.' : 'This stops and archives this session and every child, including children marked Keep running and work in progress. Their conversations, files, history and insights remain available. This cannot be resumed.'}</p>
        {error && <p role="alert">{error}</p>}
        <div className="child-discard-buttons">
          <button type="button" className="rail-link" disabled={busy} onClick={() => setOpen(false)}>Cancel</button>
          <button type="button" className="rail-link child-discard-confirm" disabled={busy} onClick={() => {void discard()}}>{busy ? 'Discarding…' : actionName}</button>
        </div>
      </div>
    </div>, document.body)}
  </>
}

/** Child cards keep their normal navigation; the menu is a separate keyboard/touch target. */
export function ChildSessionActions({childId, ...props}: Omit<Props, 'menu' | 'focusTarget' | 'onOpen'>) {
  return <SessionActions key={childId} {...props} sessionId={childId}/>
}

export function ParentSessionActions(props: Pick<SessionProps, 'sessionId' | 'title'>) {
  return <SessionActions key={props.sessionId} {...props}/>
}

function SessionActions({parentId, sessionId, title}: Pick<SessionProps, 'sessionId' | 'title'> & {parentId?: string}) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (!open) return
    root.current?.querySelector<HTMLButtonElement>('[role="menuitem"]')?.focus()
    const outside = (event: PointerEvent) => {if (!root.current?.contains(event.target as Node)) setOpen(false)}
    document.addEventListener('pointerdown', outside)
    return () => document.removeEventListener('pointerdown', outside)
  }, [open])
  return <div ref={root} className="child-card-actions" onClick={event => event.stopPropagation()} onKeyDown={event => {
    if (event.key === 'Escape') {event.stopPropagation(); setOpen(false); trigger.current?.focus()}
    if (event.key === 'Tab') setOpen(false)
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {event.preventDefault(); if (!open) setOpen(true); else root.current?.querySelector<HTMLButtonElement>('[role="menuitem"]')?.focus()}
  }}>
    <button ref={trigger} type="button" className="child-card-menu-button" aria-label={`Actions for ${title}`} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen(value => !value)}>⋯</button>
    <div className="child-card-menu" role="menu" aria-label={`Actions for ${title}`} hidden={!open}>
      <DiscardAction key={sessionId} parentId={parentId} sessionId={sessionId} title={title} menu focusTarget={trigger} onOpen={() => setOpen(false)}/>
    </div>
  </div>
}
