import { forgetSessionAttachments } from '@blerglab/chat'
import { ConfirmPopover } from '@blerglab/chat'
import { usePaletteCommands } from '../commands/context'
import {useEffect, useRef, useState, type RefObject} from 'react'
import {apiFetch} from '../apiFetch'
import {refreshChildren} from '../hooks/useChildrenSignal'
import './ChildDiscard.css'

interface Props {
  parentId: string
  childId: string
  title: string
  palette?: boolean
  menu?: boolean
  focusTarget?: RefObject<HTMLButtonElement | null>
  onOpen?(): void
  /** The session is at work now: the confirmation says discarding stops it. */
  running?: boolean
  /** In a ⋯ menu (IconMenu): the item's one-line explanation under its name. */
  hint?: string
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
function DiscardAction({parentId, sessionId, title, palette = false, menu = false, focusTarget, onOpen, onDiscarded, running = false, hint}: SessionProps & {parentId?: string}) {
  const child = !!parentId
  const actionName = child ? 'Discard child session' : 'Discard session and children'
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [requested, setRequested] = useState(false)
  const [error, setError] = useState('')
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const trigger = useRef<HTMLButtonElement>(null)
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
      // A discarded session takes no more files: forget any waiting on this device.
      void forgetSessionAttachments(sessionId)
      if (!mounted.current) return
      setRequested(true); setOpen(false)
      onDiscarded?.()
    } catch {if (mounted.current) setError(child ? 'Could not discard this child session. Try again.' : 'Could not finish discarding this session and its children. Retry to complete any remaining steps.')}
    finally {if (mounted.current) setBusy(false)}
  }
  usePaletteCommands(palette ? [{ id: `discard-session:${sessionId}`, title: 'Discard session…', destructive: true, run: () => { setError(''); setOpen(true) } }] : [])
  return <>
    {!palette && <button ref={trigger} type="button" role={menu ? 'menuitem' : undefined} className={hint ? 'icon-menu-item danger child-discard-trigger' : 'rail-link child-discard-trigger'} data-testid={hint ? 'discard-menuitem' : undefined} aria-label={menu ? actionName : `Discard ${title}`} title={menu ? actionName : `Discard ${title}`} disabled={busy || requested} onClick={() => {setError(''); setOpen(true); onOpen?.()}}>{menu ? (hint ? <><span className="icon-menu-label">Discard</span><span className="icon-menu-hint">{hint}</span></> : 'Discard') : <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7"/></svg>}</button>}
    {requested && !menu && <span className="child-discard-status" role="status">Discard requested. Updating session list…</span>}
    {(open || error) && <ConfirmPopover anchor={focusTarget ?? trigger}
      title={error ? "Discard didn't finish" : child ? 'Discard child session?' : 'Discard session and all children?'}
      description={error || <>{title}. {running && <strong data-testid="discard-running">It is running now. Discarding stops the work it is doing. </strong>}{child ? 'Stops this child; its conversation, history, files and insights stay available.' : 'Stops and archives this session and all children, including children marked Keep running and work in progress. Conversations, files, history and insights remain available; work cannot resume.'}</>}
      busy={busy} closeOnConfirm={false} confirmLabel={busy ? 'Discarding…' : error ? 'Retry' : actionName} cancelLabel={error ? 'Dismiss' : 'Cancel'}
      onConfirm={() => { void discard() }} onCancel={() => { setOpen(false); setError('') }} testId="discard-confirm" />}

  </>
}

/** Child cards keep their normal navigation; the menu is a separate keyboard/touch target. */
export function ChildSessionActions({childId, ...props}: Omit<Props, 'menu' | 'focusTarget' | 'onOpen'>) {
  return <SessionActions key={childId} {...props} sessionId={childId}/>
}

export function ParentSessionActions(props: Pick<SessionProps, 'sessionId' | 'title' | 'running'>) {
  return <SessionActions key={props.sessionId} {...props}/>
}

function SessionActions({parentId, sessionId, title, running}: Pick<SessionProps, 'sessionId' | 'title' | 'running'> & {parentId?: string}) {
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
      <DiscardAction key={sessionId} parentId={parentId} sessionId={sessionId} title={title} running={running} menu focusTarget={trigger} onOpen={() => setOpen(false)}/>
    </div>
  </div>
}
