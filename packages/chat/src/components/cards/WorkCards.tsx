// The transcript's rows for what a session works through: a checklist change (one compact row,
// collapsed, that opens to the list; consecutive changes are folded into the latest by the
// timeline) and a pinned ticket (a card; an unpin is a marker line). Everything they say is in the
// event's payload: the server recorded the change, so nothing here compares events.
import { useState } from 'react'
import { checklistRowText, commitsText, readChecklist, readTicketEvent, sourceLabel, ticketChanges } from '../../model/worklist'
import type { AgentEvent, ChecklistItem } from '../../types'
import { CommitChips } from '../CommitChips'
import { useChat } from '../context'
import { TimeLabel } from '../TimeLabel'
import { TicketDetails, TicketHead } from '../TicketView'
import '../worklist.css'

/** An item's commits: one chip ("2 commits") that opens to the shas, and "View diff" when the
 *  item names the diff the CLI published of them and the host can open it. */
function ItemCommits({ commits, urls, onViewDiff }: {
  commits: readonly string[]; urls?: (sha: string) => string | null | undefined; onViewDiff?: (() => void) | null
}) {
  const [open, setOpen] = useState(false)
  return (
    <span className="checklist-commits">
      <button type="button" className="commit-chip checklist-commit-toggle" aria-expanded={open} onClick={() => setOpen(o => !o)}>
        {commitsText(commits.length)}
      </button>
      {onViewDiff && (
        <button type="button" className="commit-diff checklist-view-diff" title="View the diff of these commits" onClick={onViewDiff}>View diff</button>
      )}
      {open && <CommitChips commits={commits.map(sha => ({ sha, url: urls?.(sha) }))} />}
    </span>
  )
}

/** The list with its boxes (read-only: only the agent ticks). An item that holds commits has a commit
 *  chip. `commitUrls` (sha to page) and `onViewDiff` are the host's hooks: the links of the chips, and
 *  what "View diff" does with the id of the item's diff (a file of the session). They default to the
 *  chat's own: ChatView's `commitUrl`, and its viewer (the button shows when the file is listed).
 *  An item without a diff has no "View diff". */
export function ChecklistItems({ items, commitUrls, onViewDiff }: {
  items: readonly ChecklistItem[]; commitUrls?: Readonly<Record<string, string>>; onViewDiff?: (artifactId: string) => void
}) {
  const chat = useChat()
  const urls = commitUrls ? (sha: string) => commitUrls[sha] : chat?.commitUrl
  const diff = (id: string | undefined): (() => void) | null => {
    if (!id) return null
    if (onViewDiff) return () => onViewDiff(id)
    return chat?.viewDiff?.(id) ?? null
  }
  return (
    <ul className="checklist-items">
      {items.map((it, i) => (
        <li key={i} className={it.done ? 'done' : undefined}>
          <span className="checklist-box" aria-hidden="true">{it.done ? '☑' : '☐'}</span>
          <span className="checklist-text">{it.text}</span>
          <span className="worklist-sr">{it.done ? ' (done)' : ' (to do)'}</span>
          {it.commits && it.commits.length > 0 && <ItemCommits commits={it.commits} urls={urls} onViewDiff={diff(it.diff)} />}
        </li>
      ))}
    </ul>
  )
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

export function TicketCard({ ev }: { ev: AgentEvent }) {
  const p = readTicketEvent(ev.payload)
  if (!p) return null
  const key = ev.client_event_id
  const t = p.ticket
  if (p.action === 'unpin') {
    return (
      <div className="agent-card marker ticket-unpinned" data-testid="ticket-unpinned" data-event-id={key}>
        Unpinned {sourceLabel(t.source)} {t.key}{t.title ? ` · ${t.title}` : ''}
      </div>
    )
  }
  const changes = ticketChanges(p)
  return (
    <div className="agent-card ticket-card" data-testid="ticket-card" data-event-id={key} data-ticket-key={t.key}>
      <div className="ticket-kicker">
        <span>{p.action === 'update' ? 'Ticket updated' : 'Ticket pinned'}</span>
        {changes.length > 0 && <span className="ticket-changes" data-testid="ticket-changes"> · {changes.join(' · ')}</span>}
        <TimeLabel ts={ev.ts} className="card-time-end" />
      </div>
      <TicketHead ticket={t} />
      <TicketDetails ticket={t} />
    </div>
  )
}
