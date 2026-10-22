// The Plan tab's checklist: one line per item. The box (a spinner while doing), the title (cut with an
// ellipsis; a click or the focus shows it whole), who it is assigned to as small glyphs (their names in
// the tooltip and the accessible name, never an id), and its commits as one chip on the right that
// opens their diff inside Blerg. The open and doing items lead; the done ones fold into one
// "✓ N done · show" row. A commit set shared by several items shows its chip once (planRows).
import { CardRefStates, CardRefText } from './ChecklistRefs'
import { useHasCardRef } from '../lib/checklistRefs'
import { useEffect, useId, useState } from 'react'
import { Hover, useCommitOpener, type ChecklistItem, type HoverTarget, type PatchSummary } from '@blerglab/chat'
import { ENGINE_LABELS } from '../lib/runtimes'
import { planRows, type PlanChip, type PlanRow } from '../lib/railPlan'

export interface PlanAssignee { kind: string; name: string; engine?: string }

export interface PlanChecklistProps {
  items: readonly ChecklistItem[]
  /** Who each item is assigned to, by name (SessionRail resolves them). */
  assignees: (item: ChecklistItem) => PlanAssignee[]
  onViewDiff: (artifactId: string, commit?: string) => void
  diffSummary?: (artifactId: string) => Promise<PatchSummary>
  /** The session still runs: a doing item's spinner turns. */
  live: boolean
  highlightedId?: string
  onItemElement?: (id: string, element: HTMLLIElement | null) => void
  /** The done items are shown (the owner of the state opens them for a jump to a done item). */
  doneOpen: boolean
  onDoneOpen: (open: boolean) => void
  /** The session: a commit chip's hovercard reads its commit (or its diff) from it. */
  sessionId?: string
}

function assigneeLabel(a: PlanAssignee): string {
  if (a.kind === 'subagent') return `Subagent: ${a.name}`
  const engine = a.engine ? ENGINE_LABELS[a.engine] ?? a.engine : ''
  return `Started session${engine ? ` (${engine})` : ''}: ${a.name}`
}

/** A subagent is the panel's robot; a started session the arrow Started sessions draws. */
const glyphOf = (a: PlanAssignee) => (a.kind === 'subagent' ? '🤖' : '↳')

function Assignees({ list }: { list: PlanAssignee[] }) {
  if (list.length === 0) return null
  const shown = list.slice(0, 3)
  const rest = list.slice(3)
  return (
    <span className="rail-plan-assignees" data-testid="rail-plan-assignees">
      {shown.map((a, i) => {
        const label = assigneeLabel(a)
        return <span key={i} className={`rail-plan-assignee ${a.kind}`} role="img" aria-label={label} title={label}>{glyphOf(a)}</span>
      })}
      {rest.length > 0 && (
        <span className="rail-plan-assignee more" role="img" aria-label={`and ${rest.map(assigneeLabel).join('; ')}`} title={rest.map(assigneeLabel).join('\n')}>+{rest.length}</span>
      )}
    </span>
  )
}

/** What a commit chip's hovercard previews: its one commit (from the diff), else the diff's files. */
function chipTarget(chip: PlanChip, sessionId: string): HoverTarget | null {
  if (chip.commits.length === 1) return { kind: 'commit', sessionId, sha: chip.commits[0], ...(chip.diff ? { diff: chip.diff } : {}) }
  if (chip.diff) return { kind: 'file', sessionId, id: chip.diff }
  return chip.commits.length > 0 ? { kind: 'commit', sessionId, sha: chip.commits[0] } : null
}

function CommitChip({ chip, onViewDiff, diffSummary, sessionId }: { chip: PlanChip; onViewDiff: PlanChecklistProps['onViewDiff']; diffSummary?: PlanChecklistProps['diffSummary']; sessionId?: string }) {
  const [counts, setCounts] = useState<PatchSummary | null>(null)
  const { diff } = chip
  useEffect(() => {
    if (!diff || !diffSummary) return
    let on = true
    diffSummary(diff).then(s => { if (on) setCounts(s) }, () => {})
    return () => { on = false }
  }, [diff, diffSummary])
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(false), 2000)
    return () => clearTimeout(t)
  }, [copied])
  const first = chip.commits[0]?.slice(0, 7) ?? ''
  const more = chip.commits.length - 1
  const shas = chip.commits.map(c => c.slice(0, 7)).join(', ')
  // One commit and no published diff: its diff generated on demand, when the page can read it.
  const opener = useCommitOpener()
  const openCommit = !diff && chip.commits.length === 1 && opener && sessionId ? () => opener(sessionId, chip.commits[0]) : null
  const what = chip.commits.length === 0 ? 'the diff' : chip.commits.length === 1 ? `commit ${first}` : `commits ${shas}`
  const lines = counts ? `, ${counts.adds} added and ${counts.dels} removed lines` : ''
  const covers = chip.covers > 1 ? `; covers ${chip.covers} items` : ''
  const label = diff || openCommit ? `Open the diff of ${what}${lines}${covers}` : `Copy ${what}${covers}`
  const tip = [chip.commits.join('\n'), counts ? `+${counts.adds} −${counts.dels}` : '', chip.covers > 1 ? `Covers ${chip.covers} items` : '', diff || openCommit ? 'Open the diff' : 'Click to copy']
    .filter(Boolean).join('\n')
  const target = sessionId ? chipTarget(chip, sessionId) : null
  const button = (
    <button type="button" className={`commit-chip rail-plan-commit${copied ? ' copied' : ''}`} data-testid="rail-plan-commit" aria-label={label} title={tip}
      onClick={() => {
        if (diff) onViewDiff(diff)
        else if (openCommit) openCommit()
        else navigator.clipboard?.writeText(chip.commits.join('\n')).then(() => setCopied(true), () => {})
      }}>
      {diff && <span className="rail-plan-commit-glyph" aria-hidden="true">±</span>}
      <span aria-hidden="true">{first || 'diff'}</span>
      {more > 0 && <span className="rail-plan-commit-more" aria-hidden="true">+{more}</span>}
      {copied && <span className="rail-sr" role="status">Copied</span>}
    </button>
  )
  return target ? <Hover target={target}>{button}</Hover> : button
}

function Row({ row, props, hidden }: { row: PlanRow; props: PlanChecklistProps; hidden?: boolean }) {
  const { item: it } = row
  const [whole, setWhole] = useState(false)
  const state = it.done ? 'done' : it.in_progress ? 'doing' : 'todo'
  const hasRef = useHasCardRef(it.text)
  const titleId = useId()
  return (
    <li tabIndex={it.id ? -1 : undefined} hidden={hidden}
      ref={it.id && props.onItemElement ? element => props.onItemElement!(it.id!, element) : undefined}
      data-checklist-id={it.id} data-testid="rail-plan-row" data-state={state}
      className={['rail-plan-row', state, it.id && it.id === props.highlightedId ? 'checklist-highlight' : ''].filter(Boolean).join(' ')}>
      <span className="checklist-box" aria-hidden="true">{it.done ? '☑' : it.in_progress ? <span className={`checklist-progress${props.live ? ' animated' : ''}`} /> : '☐'}</span>
      {/* The words: a click shows the whole item (a card link may sit inside them, #157, so they are
          not a button). The chevron beside them is the item's control, the same on every item:
          Tab reaches it, Enter or Space shows the whole item. */}
      <span className="checklist-text rail-plan-title" id={titleId} data-testid="rail-plan-title" data-expanded={whole ? 'true' : undefined} title={it.text} onClick={() => setWhole(w => !w)}>
        {hasRef ? <CardRefText text={it.text} /> : it.text}<span className="rail-sr">{it.done ? ', done' : it.in_progress ? ', doing' : ', to do'}</span>
      </span>
      <button type="button" className="rail-plan-expand" data-testid="rail-plan-expand" aria-expanded={whole} aria-controls={titleId}
        aria-label={whole ? 'Show the item on one line' : 'Show the whole item'} title={whole ? 'Show on one line' : 'Show the whole item'}
        onClick={() => setWhole(w => !w)}><span aria-hidden="true">{whole ? '▾' : '▸'}</span></button>
      <CardRefStates text={it.text} state={state} />
      <Assignees list={props.assignees(it)} />
      {row.chip ? <CommitChip chip={row.chip} onViewDiff={props.onViewDiff} diffSummary={props.diffSummary} sessionId={props.sessionId} /> : null}
    </li>
  )
}

export default function PlanChecklist(props: PlanChecklistProps) {
  const { open, done } = planRows(props.items)
  const { doneOpen, onDoneOpen } = props
  return (
    <ul className="checklist-items rail-plan-list" data-testid="rail-plan-list">
      {open.map(r => <Row key={r.item.id ?? `i${r.index}`} row={r} props={props} />)}
      {done.length > 0 && (
        <li className="rail-plan-done-toggle">
          <button type="button" data-testid="rail-plan-done-toggle" aria-expanded={doneOpen} onClick={() => onDoneOpen(!doneOpen)}>
            <span aria-hidden="true">✓</span> {done.length} done · {doneOpen ? 'hide' : 'show'}
          </button>
        </li>
      )}
      {done.map(r => <Row key={r.item.id ?? `i${r.index}`} row={r} props={props} hidden={!doneOpen} />)}
    </ul>
  )
}
