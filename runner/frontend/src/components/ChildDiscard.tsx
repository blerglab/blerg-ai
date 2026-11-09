import { SessionColorPicker } from './SessionColor'
import { useSessionStore } from '../hooks/useSessionStore'
import type { sessionNoun } from '../lib/sessionPresentation'
import { forgetSessionAttachments } from '@blerglab/chat'
import { ConfirmPopover } from '@blerglab/chat'
import { usePaletteCommands } from '../commands/context'
import {useEffect, useRef, useState, type RefObject} from 'react'
import {apiFetch} from '../apiFetch'
import {refreshChildren} from '../hooks/useChildrenSignal'
import './ChildDiscard.css'

class DiscardError extends Error {}

interface Props {
  noun?: ReturnType<typeof sessionNoun>
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
function DiscardAction({parentId, sessionId, title, palette = false, menu = false, focusTarget, onOpen, onDiscarded, running = false, hint, noun: providedNoun}: SessionProps & {parentId?: string}) {
  const child = !!parentId
  const noun = providedNoun ?? (child ? 'worker' : 'workspace')
  const actionName = `Discard ${noun}`
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [requested, setRequested] = useState(false)
  const [error, setError] = useState('')
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const trigger = useRef<HTMLButtonElement>(null)
  const dismissed = useRef(false)
  async function discard() {
    if (busy) return
    dismissed.current = false
    setBusy(true); setError('')
    try {
      const route = parentId ? `/api/sessions/${encodeURIComponent(parentId)}/children/${encodeURIComponent(sessionId)}/discard` : `/api/sessions/${encodeURIComponent(sessionId)}/discard`
      await discardWithDeadline(async () => {
        const response = await apiFetch(route, {method: 'POST'})
        if (!response.ok) {
          if (child && response.status === 409) throw new DiscardError(`This ${noun} is part of a pending discard of the workspace that started it. Retry that workspace’s discard to finish.`)
          const body = (typeof response.json === 'function' ? await response.json().catch(() => null) : null) as {error?: string} | null
          if (typeof body?.error === 'string') throw new DiscardError(body.error)
          throw new Error('refused')
        }
        if (!child) {
          const receipt = await response.json() as {status?: string; session_id?: string; archived_at?: string}
          if (receipt?.status !== 'discarded' || receipt.session_id !== sessionId || typeof receipt.archived_at !== 'string' || !Number.isFinite(Date.parse(receipt.archived_at))) throw new Error('discard not confirmed')
        }
      })
      refreshChildren(parentId || sessionId)
      // A discarded session takes no more files: forget any waiting on this device.
      void forgetSessionAttachments(sessionId)
      if (!mounted.current) return
      setRequested(true); setOpen(false)
      onDiscarded?.()
    } catch (problem) {if (mounted.current && !dismissed.current) setError(problem instanceof DiscardError ? problem.message : child ? `Could not discard this ${noun}. Try again.` : `Could not finish discarding this ${noun} and its workers and channels. Retry to complete any remaining steps.`)}
    finally {if (mounted.current) setBusy(false)}
  }
  usePaletteCommands(palette ? [{ id: `discard-session:${sessionId}`, title: `Discard ${noun}…`, destructive: true, run: () => { setError(''); setOpen(true) } }] : [])
  return <>
    {!palette && <button ref={trigger} type="button" role={menu ? 'menuitem' : undefined} className={hint ? 'icon-menu-item danger child-discard-trigger' : 'rail-link child-discard-trigger'} data-testid={hint ? 'discard-menuitem' : undefined} aria-label={menu ? actionName : `Discard ${title}`} title={menu ? actionName : `Discard ${title}`} disabled={busy || requested} onClick={() => {setError(''); setOpen(true); onOpen?.()}}>{menu ? (hint ? <><span className="icon-menu-label">Discard</span><span className="icon-menu-hint">{hint}</span></> : 'Discard') : <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7"/></svg>}</button>}
    {requested && !menu && <span className="child-discard-status" role="status">Discard requested. Updating workspace list…</span>}
    {(open || error) && <ConfirmPopover anchor={focusTarget ?? trigger}
      title={error ? "Discard didn't finish" : child ? `Discard ${noun}?` : `Discard ${noun} and all workers and channels?`}
      description={error || <>{title}. {running && <strong data-testid="discard-running">It is running now. Discarding stops the work it is doing. </strong>}{child ? `Stops this ${noun}; its conversation, history, files and insights stay available.` : 'Stops and archives this workspace and all workers and channels, including those marked Keep running and work in progress. Conversations, files, history and insights remain available; work cannot resume.'}</>}
      busy={busy} cancelWhileBusy closeOnConfirm={false} confirmLabel={busy ? 'Discarding…' : error ? 'Retry' : actionName} cancelLabel={error ? 'Dismiss' : 'Cancel'}
      onConfirm={() => { void discard() }} onCancel={() => { dismissed.current = true; setOpen(false); setError('') }} testId="discard-confirm" />}

  </>
}

/** Child cards keep their normal navigation; the menu is a separate keyboard/touch target. */
export function ChildSessionActions({childId, ...props}: Omit<Props, 'menu' | 'focusTarget' | 'onOpen'>) {
  return <SessionActions key={childId} {...props} sessionId={childId}/>
}

export function ParentSessionActions(props: Pick<SessionProps, 'sessionId' | 'title' | 'running' | 'noun'>) {
  return <SessionActions key={props.sessionId} {...props}/>
}

function SessionActions({parentId, sessionId, title, running, noun}: Pick<SessionProps, 'sessionId' | 'title' | 'running' | 'noun'> & {parentId?: string}) {
  const color = useSessionStore(s => s.sessions.find(session => session.id === sessionId)?.color)
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
    if ((event.target as HTMLElement).closest('[role="radiogroup"]')) return
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      if (!open) setOpen(true)
      else {
        const items = [...(root.current?.querySelectorAll<HTMLButtonElement | HTMLSelectElement>('[role="menuitem"], [role="radio"][tabindex="0"]') ?? [])].filter(item => !item.disabled)
        const current = items.indexOf(document.activeElement as HTMLButtonElement | HTMLSelectElement)
        const down = event.key === 'ArrowDown'
        const next = current < 0 ? (down ? 0 : items.length - 1) : (current + (down ? 1 : -1) + items.length) % items.length
        items[next]?.focus()
      }
    }
  }}>
    <button ref={trigger} type="button" className="child-card-menu-button" aria-label={`Actions for ${title}`} aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen(value => !value)}>⋯</button>
    <div className="child-card-menu" role="dialog" aria-label={`Actions for ${title}`} hidden={!open}>
      <SessionColorPicker sessionId={sessionId} color={color} />
      <div role="menu" aria-label="Workspace actions"><DiscardAction key={sessionId} parentId={parentId} sessionId={sessionId} noun={noun} title={title} running={running} menu focusTarget={trigger} onOpen={() => setOpen(false)}/></div>
    </div>
  </div>
}

/** Includes auth refresh and response decoding, even if either never settles. A timeout
 * leaves the server's durable cleanup intent intact; retry checks the same receipt. */
async function discardWithDeadline(request: () => Promise<void>) {
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    await Promise.race([request(), new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new DiscardError('Discard timed out. Workspace cleanup may still be pending. All work is retained; retry to check its result.')), 35000)
    })])
  } finally { clearTimeout(timer) }
}
