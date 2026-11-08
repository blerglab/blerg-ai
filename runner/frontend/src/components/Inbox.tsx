import { longWhen } from '../lib/friendlyTime'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useLocation, useNavigate } from 'react-router-dom'
import { ownsAppShortcut, visibleSurfaceIsOpen } from '../commands/shortcutOwnership'
import { Hover, Markdown, ReferenceScope, type ChatRefs, type Ticket } from '@blerglab/chat'
import type { SessionTabs } from '../hooks/useSessionTabs'
import { OPEN_INBOX, openInbox, deleteInboxItem, loadMoreInbox, refreshInbox, setInboxRead, setInboxShowing, useInbox, useInboxAccount, useInboxShowing, type InboxItem } from '../hooks/useInbox'
import { getAccessToken } from '../authClient'
import { tokenSubject } from '../claims'
import { apiFetch } from '../apiFetch'
import { useSessionStore } from '../hooks/useSessionStore'
import { useLoadSessionBoard } from '../lib/sessionBoard'
import { boardCardsReader } from '../lib/cardRefs'
import { PinnedTicketModal } from './BoardCardView'
import './Inbox.css'

export function InboxIcon() { return <svg aria-hidden="true" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75"><path d="M4 4h16l2 11v5H2v-5zM2 15h6l2 3h4l2-3h6" /></svg> }
function DeleteAction({ id, row = false }: { id: string; row?: boolean }) {
 return <button type="button" className={`inbox-action${row ? ' inbox-row-delete' : ''}`} aria-label={row ? 'Delete item' : 'Delete'} title="Delete" onClick={event => { event.stopPropagation(); deleteInboxItem(id) }}><svg aria-hidden="true" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round"><path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7" /></svg></button>
}
function ReaderMarkdown({ item }: { item: InboxItem }) {
 const board = useLoadSessionBoard(item.session_id, true)
 const [ticket, setTicket] = useState<Ticket | null>(null)
 const refs = useMemo<ChatRefs>(() => ({ cards: board ? boardCardsReader(board, item.session_id) : undefined, boardCards: (id, numbers) => boardCardsReader({ id }, item.session_id)(numbers), openCard: target => setTicket({ source: 'blerg-board', board_id: target.boardId, key: target.key, title: target.title ?? '' }) }), [board, item.session_id])
 return <><ReferenceScope sessionId={`inbox:${item.id}`} refs={refs}><Markdown text={withoutLeadingTitle(item.body ?? '')} /></ReferenceScope>{ticket && <PinnedTicketModal ticket={ticket} sessionId={item.session_id} onClose={() => setTicket(null)} />}</>
}
const kindLabels: Record<string, string> = { summary: 'Summary', release: 'Release', failure: 'Failure', review: 'Review', important: 'Important' }
// Today shows the time, anything older the date; the full stamp is in the tooltip.
function shortWhen(iso: string) {
  const at = new Date(iso), now = new Date()
  return at.toDateString() === now.toDateString() ? at.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' }) : at.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}
// The subject is the title: a body that opens with its own top-level heading would say it twice.
function withoutLeadingTitle(body: string) {
  return body.replace(/^\s*# [^\n]*\n+/, '')
}
// Where an item came from reads like a mail sender: the workspace's own title, else "Main".
function useSenderNames(items: InboxItem[]) {
  const ids = items.map(item => item.session_id).join('\0')
  const titles = useSessionStore(state => JSON.stringify(ids.split('\0').map(id => state.sessions.find(session => session.id === id)?.title ?? '')))
  return useMemo(() => { const names = JSON.parse(titles) as string[]; return new Map(ids.split('\0').map((id, index) => [id, names[index] || 'Main'])) }, [ids, titles])
}
function InboxCards({ items, onSelect }: { items: InboxItem[]; onSelect(id: string): void }) {
  const senders = useSenderNames(items)
  return <ul className="inbox-cards" aria-label="Inbox items">{items.map(item => <li key={item.id}>
    <InboxCard item={item} sender={senders.get(item.session_id) ?? 'Main'} onSelect={onSelect} /><DeleteAction id={item.id} row />
  </li>)}</ul>
}
function InboxCard({ item, sender, onSelect }: { item: InboxItem; sender: string; onSelect(id: string): void }) {
  const showing = useInboxShowing(item.id)
  const details = <div className="inbox-hover">
    <div className="inbox-hover-meta"><span className="inbox-card-kind">{kindLabels[item.kind] ?? 'Note'}</span><span>{sender}</span><time dateTime={item.created_at}>{longWhen(item.created_at)}</time></div>
    <strong className="inbox-hover-subject">{item.subject}</strong>
    {item.preview && <p className="inbox-hover-preview">{item.preview}</p>}
  </div>
  return <Hover target={{ kind: 'run', title: item.subject, facts: {} }} content={details}>
    <button type="button" className={`inbox-card${item.read ? '' : ' unread'}${showing ? ' selected' : ''}`} aria-current={showing ? 'true' : undefined} aria-label={`${item.subject}, ${item.read ? 'read' : 'unread'}`} onClick={() => onSelect(item.id)}>
      <span className="inbox-card-subject">{item.subject}</span>
      <time dateTime={item.created_at} title={new Date(item.created_at).toLocaleString()}>{shortWhen(item.created_at)}</time>
    </button>
  </Hover>
}
export function CompactInbox() {
  const { items, loading, error, next } = useInbox()
  return <section className="inbox-compact" aria-label="Inbox">
    
    {error ? <p role="alert">{error}<button className="icon-btn" title="Retry" aria-label="Retry inbox" onClick={() => void refreshInbox()}>↻</button></p> : loading && !items.length ? <p role="status">Loading…</p> : !items.length ? <p className="inbox-empty-compact">Nothing to read yet. Summaries, releases and failures land here.</p> : <><InboxCards items={items} onSelect={openInbox} />{next && <button className="icon-btn" title="Load older items" aria-label="Load older inbox items" onClick={() => void loadMoreInbox()}>⌄</button>}</>}
  </section>
}
function InboxReader({ itemId, active, openRevision, readFailure }: { itemId?: string; active: boolean; openRevision: number; readFailure: string }) {
  const { items, removedIds } = useInbox()
  const [loadedItem, setItem] = useState<InboxItem | null>(null)
  const item = loadedItem && !removedIds.includes(loadedItem.id) ? loadedItem : null
  // Read state comes from the inbox store, so Mark read / Mark unread flips at once.
  const isRead = item ? items.find(entry => entry.id === item.id)?.read ?? item.read : false
  const [failure, setFailure] = useState('')
  const [mutationFailure, setMutationFailure] = useState('')
  const [reading, setReading] = useState(!!itemId)
  const [seen, setSeen] = useState({ itemId, openRevision })
  if (seen.itemId !== itemId || seen.openRevision !== openRevision) {
    setSeen({ itemId, openRevision }); setReading(!!itemId); setMutationFailure('')
  }
  useEffect(() => {
    if (!itemId || !active) return
    const controller = new AbortController()
    const read = async () => {
      setFailure(''); setItem(null)
      try {
        const response = await apiFetch(`/api/inbox/${encodeURIComponent(itemId)}`, { signal: controller.signal })
        if (!response.ok) throw new Error(`HTTP ${response.status}`)
        const next = await response.json() as InboxItem
        if (!controller.signal.aborted) setItem(next)
      } catch (cause) {
        if (!controller.signal.aborted) {
          console.warn('Inbox item failed', { cause: cause instanceof Error ? cause.name : 'unknown' })
          setFailure('Couldn’t open this item. Select it to try again.')
        }
      }
    }
    void read()
    return () => controller.abort()
  }, [itemId, active, openRevision])
  return <div className="inbox-host"><section className={`inbox-screen${reading ? ' inbox-reading' : ''}`} aria-label="Inbox">
    <div className="inbox-reading-pane"><div className="inbox-toolbar">{item && <button type="button" className="inbox-action icon-only" aria-label={isRead ? 'Mark unread' : 'Mark read'} title={isRead ? 'Mark unread' : 'Mark read'} aria-pressed={!isRead} onClick={() => { setMutationFailure(''); void setInboxRead(item.id, !isRead).catch(() => setMutationFailure(isRead ? 'Couldn’t mark this item unread. Try again.' : 'Couldn’t mark this item read. Try again.')) }}>{isRead
        ? <svg aria-hidden="true" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="5" width="18" height="14" rx="2" /><path d="m3 7 9 6 9-6" /><circle cx="19" cy="6" r="3" fill="currentColor" stroke="none" /></svg>
        : <svg aria-hidden="true" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round"><path d="M3 10 12 4l9 6v9a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1z" /><path d="m3 10 9 6 9-6" /></svg>}</button>}{item && <DeleteAction id={item.id} />}</div>
      {failure && <p role="alert">{failure}</p>}{(readFailure || mutationFailure) && <p role="alert">{readFailure || mutationFailure}</p>}{item ? <article className="inbox-document"><header className="inbox-doc-head"><span className="inbox-card-kind">{kindLabels[item.kind] ?? 'Note'}</span><time dateTime={item.created_at} title={new Date(item.created_at).toLocaleString()}>{longWhen(item.created_at)}</time></header><h1>{item.subject}</h1><ReaderMarkdown key={item.id} item={item} /></article> : !failure && <p className="inbox-empty">{itemId && !removedIds.includes(itemId) ? 'Loading…' : items.length ? 'Choose an item in the Inbox list.' : 'Nothing to read yet.'}</p>}
    </div>
  </section></div>
}
export default function InboxTabs({ tabs, onOpen }: { tabs: SessionTabs; onOpen?(): void }) {
  const { open } = tabs
  const account = useInboxAccount()
  const { removedIds } = useInbox()
  const location = useLocation()
  const navigate = useNavigate()
  const [openRevision, setOpenRevision] = useState(0)
  const [readFailure, setReadFailure] = useState<{ account: string; id: string; message: string } | null>(null)
  const readRequest = useRef(0)
  const consumedDeepLink = useRef('')
  const openItem = useCallback((id?: string) => {
    const current = tabs.tabs.find(tab => tab.view.kind === 'inbox')?.view
    const itemId = id ?? (current?.kind === 'inbox' ? current.itemId : undefined)
    open({ kind: 'inbox', itemId }, true); onOpen?.()
    if (!id) return
    setOpenRevision(value => value + 1); setReadFailure(null)
    const request = ++readRequest.current
    // Only explicit opens write read state. Fetching, restoring and activating are reads.
    void setInboxRead(id, true).catch(() => {
      if (request === readRequest.current && account === tokenSubject(getAccessToken())) setReadFailure({ account, id, message: 'Couldn’t mark this item read. Try opening it again.' })
    })
  }, [tabs.tabs, open, onOpen, account])
  useEffect(() => {
    const receive = (event: Event) => { const id = (event as CustomEvent<{ id?: string }>).detail?.id; openItem(id) }
    window.addEventListener(OPEN_INBOX, receive)
    return () => window.removeEventListener(OPEN_INBOX, receive)
  }, [openItem])
  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      const mac = /Mac|iPhone|iPad|iPod/.test(navigator.platform)
      if (event.key.toLowerCase() !== 'i' || !event.altKey || event.shiftKey || !(mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) || event.defaultPrevented || event.isComposing || ownsAppShortcut(event)) return
      if (visibleSurfaceIsOpen('[aria-modal="true"], [role="dialog"], [role="menu"], .confirm-popover')) return
      event.preventDefault()
      if (!event.repeat) { openItem() }
    }
    window.addEventListener('keydown', keydown)
    return () => window.removeEventListener('keydown', keydown)
  }, [openItem])
  useEffect(() => {
    const query = new URLSearchParams(location.search), item = query.get('inbox')
    if (!item) { consumedDeepLink.current = ''; return }
    const intent = `${location.pathname}:${location.search}`
    if (consumedDeepLink.current === intent) return
    let canceled = false
    queueMicrotask(() => {
      if (canceled) return
      consumedDeepLink.current = intent
      openItem(item)
      query.delete('inbox')
      navigate({ pathname: location.pathname, search: query.toString() }, { replace: true })
    })
    return () => { canceled = true }
  }, [location.search, location.pathname, navigate, openItem])
  const showingIds = tabs.tabs.flatMap(tab => tab.view.kind === 'inbox' && tab.view.itemId && tabs.visible.includes(tab.id) && !removedIds.includes(tab.view.itemId) ? [tab.view.itemId] : []).join('\0')
  useEffect(() => { setInboxShowing(showingIds ? showingIds.split('\0') : []) }, [showingIds])
  useEffect(() => () => setInboxShowing([]), [])
  return tabs.tabs.map(tab => tab.view.kind === 'inbox' && createPortal(<InboxReader key={account} itemId={tab.view.itemId} active={tabs.visible.includes(tab.id)} openRevision={openRevision} readFailure={readFailure?.account === account && readFailure.id === tab.view.itemId ? readFailure.message : ''} />, tabs.targetFor(tab.id), tab.id))
}
