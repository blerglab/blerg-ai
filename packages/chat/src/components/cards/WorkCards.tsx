// The transcript's rows for what a session works through: a checklist change (one compact row,
// collapsed, that opens to the list; consecutive changes are folded into the latest by the
// timeline) and a pinned ticket (one compact row; consecutive pins, or unpins, folded into one). Everything they say is in the
// event's payload: the server recorded the change, so nothing here compares events.
import { useEffect, useState, type ReactNode } from 'react'
import { checklistRowText, PIN_STATE_GLYPH, pinStateOfStatus, readChecklist, readTicketEvent, sourceLabel, ticketChanges, ticketHref, ticketRunOf } from '../../model/worklist'
import type { PatchSummary } from '../../model/patch'
import type { AgentEvent, ChecklistItem, Ticket } from '../../types'
import { CommitChips } from '../CommitChips'
import { useChat } from '../context'
import { Hover } from '../Hovercard'
import { TimeLabel } from '../TimeLabel'
import { TicketModal } from '../TicketView'
import { useRefContext } from '../RefLinks'
import '../worklist.css'

/** "± View diff", with the diff's +/− counts once they are read. */
function ViewDiffButton({ onClick, summary }: { onClick: () => void; summary?: () => Promise<PatchSummary> }) {
  const [counts, setCounts] = useState<PatchSummary | null>(null)
  useEffect(() => {
    let live = true
    summary?.().then(s => { if (live) setCounts(s) }, () => {})
    return () => { live = false }
  }, [summary])
  const label = counts ? `View diff, ${counts.adds} added and ${counts.dels} removed lines` : 'View diff'
  return (
    <button type="button" className="commit-view-diff" aria-label={label} title="View the diff of these commits" onClick={onClick}>
      <span className="commit-view-diff-glyph" aria-hidden="true">±</span>
      <span>View diff</span>
      {counts && (
        <span className="commit-view-diff-counts" aria-hidden="true">
          <span className="commit-plus">+{counts.adds}</span>
          <span className="commit-minus">−{counts.dels}</span>
        </span>
      )}
    </button>
  )
}

/** An item's commits: up to three short shas, then "± View diff" when the item names the diff the CLI
 *  published of them and the host can open it. With that diff a sha opens it at the commit; without,
 *  it opens the commit's diff read from the session (under a CommitViewerHost that can), else it
 *  copies itself. Nothing links out of Blerg. */
function ItemCommits({ commits, open, summary, hover }: {
  commits: readonly string[]
  hover?: { sessionId: string; diff?: string }
  open: ((commit?: string) => void) | null
  summary?: () => Promise<PatchSummary>
}) {
  return (
    <span className="checklist-commits">
      {commits.length > 0 && <CommitChips commits={commits.map(sha => ({ sha }))} onOpen={open ? sha => open(sha) : undefined} hover={hover} />}
      {open && <ViewDiffButton onClick={() => open()} summary={summary} />}
    </span>
  )
}

/** The list with its boxes (read-only: only the agent ticks). An item that holds commits has its
 *  sha chips and, with a published diff, "± View diff". `onViewDiff` (the diff's id and, from a sha
 *  chip, the commit) and `diffSummary` are the host's hooks; they default to the chat's own viewer
 *  (the button shows when the file is listed) and its cached reading of the diff. `commitUrls` is
 *  accepted for compatibility and not used: commits never link out of Blerg. */
export function ChecklistItems({ items, onViewDiff, diffSummary, highlightedId, onItemElement, renderAssignees, live = false }: {
  items: readonly ChecklistItem[]
  renderAssignees?: (item: ChecklistItem) => ReactNode
  highlightedId?: string
  onItemElement?: (id: string, element: HTMLLIElement | null) => void
  /** Historical snapshots retain Doing state without suggesting the work is still running. */
  live?: boolean
  /** @deprecated Not used since 0.19.0. */
  commitUrls?: Readonly<Record<string, string>>
  onViewDiff?: (artifactId: string, commit?: string) => void
  diffSummary?: (artifactId: string) => Promise<PatchSummary>
}) {
  const chat = useChat()
  const opener = (id: string | undefined): ((commit?: string) => void) | null => {
    if (!id) return null
    if (onViewDiff) return (commit?: string) => onViewDiff(id, commit)
    return chat?.viewDiff?.(id) ?? null
  }
  const reader = diffSummary ?? chat?.diffSummary
  return (
    <ul className="checklist-items">
      {items.map((it, i) => {
        const open = opener(it.diff)
        const assignees = renderAssignees?.(it)
        return (
          <li key={it.id ?? i} tabIndex={it.id ? -1 : undefined}
            ref={it.id && onItemElement ? element => onItemElement(it.id!, element) : undefined}
            data-checklist-id={it.id}
            className={[it.done ? 'done' : it.in_progress ? 'doing' : '', it.id && it.id === highlightedId ? 'checklist-highlight' : ''].filter(Boolean).join(' ') || undefined}>
            <span className="checklist-box" aria-hidden="true">{it.done ? '☑' : it.in_progress ? <span className={`checklist-progress${live ? ' animated' : ''}`} /> : '☐'}</span>
            <span className="checklist-text">{it.text}</span>
            <span className="worklist-sr">{it.done ? ' (done)' : it.in_progress ? ' (doing)' : ' (to do)'}</span>
            {assignees && <span className="checklist-assignees">{assignees}</span>}
            {((it.commits && it.commits.length > 0) || open) && (
              <ItemCommits commits={it.commits ?? []} open={open} summary={open && it.diff && reader ? summaryOf(reader, it.diff) : undefined}
                hover={chat ? { sessionId: chat.sessionId, ...(it.diff ? { diff: it.diff } : {}) } : undefined} />
            )}
          </li>
        )
      })}
    </ul>
  )
}

// One stable function per reader and diff, so ViewDiffButton's effect runs once.
const summaries = new WeakMap<object, Map<string, () => Promise<PatchSummary>>>()
function summaryOf(reader: (id: string) => Promise<PatchSummary>, id: string): () => Promise<PatchSummary> {
  let m = summaries.get(reader)
  if (!m) { m = new Map(); summaries.set(reader, m) }
  let f = m.get(id)
  if (!f) { f = () => reader(id); m.set(id, f) }
  return f
}

export function ChecklistRow({ ev }: { ev: AgentEvent }) {
  const [open, setOpen] = useState(false)
  const p = readChecklist(ev.payload)
  if (!p) return null
  const key = ev.client_event_id
  return (
    <div className="agent-card marker checklist-row" data-testid="checklist-row" data-event-id={key}>
      {p.items.length === 0
        ? <span>☐ {checklistRowText(p)}</span>
        : (
            <button type="button" className="checklist-toggle" aria-expanded={open} onClick={() => setOpen(o => !o)}>
              <span aria-hidden="true">{open ? '▾' : '▸'}</span> {checklistRowText(p)}
            </button>
          )}
      {open && <ChecklistItems items={p.items} />}
    </div>
  )
}

// A ticket's card number (or another tracker's key) as the chat's card chip: its hovercard on hover,
// a click opening the ticket in the host's dialog (the runner's live one), else the chat's own.
function TicketChip({ ticket: t, onOpen }: { ticket: Ticket; onOpen: (t: Ticket) => void }) {
  return (
    <Hover target={{ kind: 'card', source: t.source, key: t.key, ...(t.board_id ? { boardId: t.board_id } : {}), title: t.title, ...(t.status ? { status: t.status } : {}), ticket: t }}>
      <button type="button" className="md-ref md-ref-card ticket-chip" data-testid="ticket-chip"
        aria-label={`Open ticket ${t.key}${t.title ? `: ${t.title}` : ''}`}
        onClick={e => { e.stopPropagation(); onOpen(t) }}>{t.key}</button>
    </Hover>
  )
}

function StateGlyph({ status }: { status?: string }) {
  const s = PIN_STATE_GLYPH[pinStateOfStatus(status)]
  return <span className="ticket-row-glyph" title={status || s.words} aria-label={status || s.words}>{s.glyph}</span>
}

const isBoard = (t: Ticket) => t.source === 'blerg-board'

// A pin, an unpin or a refresh as one system row, as wide as a bubble at most (#159):
//   📌 Pinned #157 ◐ Plan: card numbers in checklist items … · 2 min ago
//   📌 Pinned #157, #158 · 2 min ago            (consecutive pins, folded by the timeline)
//   📌 Unpinned #127 (done) · just now
//   📌 Updated #157 ✓ status In progress → Done · just now
//   📌 Pinned ENG-12 ○ Fix the login · JIRA · just now   (another tracker: its key and source)
export function TicketCard({ ev }: { ev: AgentEvent }) {
  const [open, setOpen] = useState<Ticket | null>(null)
  const refs = useRefContext()?.refs
  const p = readTicketEvent(ev.payload)
  if (!p) return null
  const key = ev.client_event_id
  const run = ticketRunOf(ev) ?? { action: p.action === 'unpin' ? 'unpin' as const : 'pin' as const, tickets: [p.ticket] }
  const tickets = run.tickets
  const one = tickets.length === 1 ? tickets[0] : null
  const openTicket = (t: Ticket) => (refs?.openTicket ? refs.openTicket(t) : setOpen(t))
  const verb = p.action === 'update' ? 'Updated' : p.action === 'unpin' ? 'Unpinned' : 'Pinned'
  const changes = ticketChanges(p)
  const done = one && pinStateOfStatus(one.status) === 'done'
  return (
    <div className={`agent-card marker ticket-row${p.action === 'unpin' ? ' is-unpin' : ''}`} data-testid={p.action === 'unpin' ? 'ticket-unpinned' : 'ticket-card'} data-event-id={key} data-ticket-key={tickets.map(t => t.key).join(' ')} data-ticket-url={(one && ticketHref(one.url)) || undefined}>
      <span className="ticket-row-pin" aria-hidden="true">📌</span>
      <span className="ticket-row-verb">{verb}</span>
      <span className="ticket-row-chips">
        {tickets.map((t, i) => (
          <span key={`${t.source}:${t.board_id ?? ''}:${t.key}`}>{i > 0 && ', '}<TicketChip ticket={t} onOpen={openTicket} /></span>
        ))}
      </span>
      {one && p.action !== 'unpin' && <StateGlyph status={one.status} />}
      {one && p.action === 'unpin' && done && <span className="ticket-row-note">(done)</span>}
      {one && p.action !== 'unpin' && one.title && <span className="ticket-row-title" title={one.title}>{one.title}</span>}
      {changes.length > 0 && <span className="ticket-row-note" data-testid="ticket-changes">· {changes.join(' · ')}</span>}
      {one && !isBoard(one) && <span className="ticket-row-note">· {sourceLabel(one.source)}</span>}
      <TimeLabel ts={ev.ts} className="ticket-row-time" />
      {open && <TicketModal ticket={open} onClose={() => setOpen(null)} />}
    </div>
  )
}
