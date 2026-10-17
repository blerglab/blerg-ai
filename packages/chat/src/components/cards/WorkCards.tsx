// The transcript's rows for what a session works through: a checklist change (one compact row,
// collapsed, that opens to the list; consecutive changes are folded into the latest by the
// timeline) and a pinned ticket (a card; an unpin is a marker line). Everything they say is in the
// event's payload: the server recorded the change, so nothing here compares events.
import { useEffect, useState } from 'react'
import { checklistRowText, readChecklist, readTicketEvent, sourceLabel, ticketChanges } from '../../model/worklist'
import type { PatchSummary } from '../../model/patch'
import type { AgentEvent, ChecklistItem } from '../../types'
import { CommitChips } from '../CommitChips'
import { useChat } from '../context'
import { TimeLabel } from '../TimeLabel'
import { TicketHead, TicketModal, TicketSummary } from '../TicketView'
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
 *  a sha copies itself. Nothing links out of Blerg. */
function ItemCommits({ commits, open, summary }: {
  commits: readonly string[]
  open: ((commit?: string) => void) | null
  summary?: () => Promise<PatchSummary>
}) {
  return (
    <span className="checklist-commits">
      {commits.length > 0 && <CommitChips commits={commits.map(sha => ({ sha }))} onOpen={open ? sha => open(sha) : undefined} />}
      {open && <ViewDiffButton onClick={() => open()} summary={summary} />}
    </span>
  )
}

/** The list with its boxes (read-only: only the agent ticks). An item that holds commits has its
 *  sha chips and, with a published diff, "± View diff". `onViewDiff` (the diff's id and, from a sha
 *  chip, the commit) and `diffSummary` are the host's hooks; they default to the chat's own viewer
 *  (the button shows when the file is listed) and its cached reading of the diff. `commitUrls` is
 *  accepted for compatibility and not used: commits never link out of Blerg. */
export function ChecklistItems({ items, onViewDiff, diffSummary }: {
  items: readonly ChecklistItem[]
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
        return (
          <li key={i} className={it.done ? 'done' : undefined}>
            <span className="checklist-box" aria-hidden="true">{it.done ? '☑' : '☐'}</span>
            <span className="checklist-text">{it.text}</span>
            <span className="worklist-sr">{it.done ? ' (done)' : ' (to do)'}</span>
            {((it.commits && it.commits.length > 0) || open) && (
              <ItemCommits commits={it.commits ?? []} open={open} summary={open && it.diff && reader ? summaryOf(reader, it.diff) : undefined} />
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

export function TicketCard({ ev }: { ev: AgentEvent }) {
  const [open, setOpen] = useState(false)
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
        <span>{p.action === 'update' ? 'Updated' : 'Pinned ticket'}</span>
        {changes.length > 0 && <span className="ticket-changes" data-testid="ticket-changes"> · {changes.join(' · ')}</span>}
        <TimeLabel ts={ev.ts} className="card-time-end" />
      </div>
      <TicketHead ticket={t} />
      <TicketSummary ticket={t} />
      <div className="ticket-actions">
        <button type="button" className="ticket-open" onClick={() => setOpen(true)}>Open ticket</button>
      </div>
      {open && <TicketModal ticket={t} onClose={() => setOpen(false)} />}
    </div>
  )
}
