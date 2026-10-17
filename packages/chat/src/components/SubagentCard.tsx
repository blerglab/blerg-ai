// A subagent drawn as one line, like a tool group's row: the task it was given and one glyph for its
// state (its type, model and tokens are in the row's tooltip and accessible name; the session panel
// draws the full card). Opened, it holds the brief it was given, everything it did (drawn with the
// same rows as the top level) and the report it returned. The Agent call that started it is folded
// in here rather than shown as a tool row.
import { memo, useContext, useState } from 'react'
import { useMinuteClock } from '../hooks/useMinuteClock'
import { ClockOffsetContext } from '../model/clockOffset'
import { useChat } from './context'
import { NO_END_RECORDED } from '../model/stale'
import type { SubagentEntry, TimelineItem } from '../model/toolGroups'
import {
  doingLine, endLine, handBackReport, inputField, payloadOf, resumeAck, resumedSubagent, staleSubagent, subagentEffort, subagentModel, subagentStatus, subagentTitle,
  subagentTokens, tokenLabel, type SubagentStatus,
} from '../model/subagents'
import { EventRow, ToolCard, type RenderOptions } from './cards/EventCards'
import Markdown from './Markdown'
import { ToolGroup } from './ToolGroup'

const STATUS_LABEL: Record<SubagentStatus, string> = {
  running: 'running',
  background: 'running in the background',
  done: 'done',
  error: 'error',
  stopped: 'no result',
}

// The one state glyph at the right of the row, drawn like a tool group's: a spinner while it runs,
// a muted spinner and ⏱ while it runs in the background, ✓ done, ✗ error, … no result.
/** The first line of s that is not blank, trimmed. */
function firstLine(s: string): string {
  for (const line of s.split('\n')) {
    const t = line.trim()
    if (t) return t
  }
  return ''
}

function StatusGlyph({ status }: { status: SubagentStatus }) {
  const common = { 'data-testid': 'subagent-status', role: 'img', 'aria-label': STATUS_LABEL[status] } as const
  switch (status) {
    case 'running':
      return <span className="subagent-status running" {...common}><span className="tcg-spin" aria-hidden="true" /></span>
    case 'background':
      return <span className="subagent-status background" {...common}><span className="tcg-spin" aria-hidden="true" /><span className="subagent-bg-glyph" aria-hidden="true">⏱</span></span>
    case 'done':
      return <span className="subagent-status done" {...common}><span className="tcg-glyph tcg-ok" aria-hidden="true">✓</span></span>
    case 'error':
      return <span className="subagent-status error" {...common}><span className="tcg-glyph tcg-bad" aria-hidden="true">✗</span></span>
    default:
      return <span className="subagent-status stopped" {...common}><span className="tcg-glyph" aria-hidden="true">…</span></span>
  }
}

/** The rows of a timeline (a subagent's own), drawn with the top level's components. */
function Rows({ items, live, options }: { items: TimelineItem[]; live: ReadonlySet<string>; options: RenderOptions }) {
  return (
    <>
      {items.map(item => {
        switch (item.type) {
          case 'tools':
            return <ToolGroup key={`tools:${item.key}`} entries={item.entries} live={live} />
          case 'card':
            return <ToolCard key={item.entry.ev.client_event_id} call={item.entry.call} result={item.entry.result} ts={item.entry.ev.ts} />
          case 'subagent':
            return <SubagentCard key={`sub:${item.key}`} sub={item.sub} live={live} options={options} />
          default:
            return <EventRow key={item.ev.client_event_id} ev={item.ev} options={options} />
        }
      })}
    </>
  )
}

export const SubagentCard = memo(function SubagentCard({ sub, live, options }: {
  sub: SubagentEntry
  live: ReadonlySet<string>
  options: RenderOptions
}) {
  // Collapsed unless the person opened it; the transcript keeps that in options.subagentFolds, so a
  // card drawn again (scrolled out of the window and back) keeps it. Kept by sub.key: the call id
  // of the Agent call, which a "started earlier" placeholder already has (its rows' parent_call_id),
  // so it keeps its state as it fills in.
  const folds = options.subagentFolds
  const [open, setOpen] = useState(() => folds?.get(sub.key) ?? false)
  const toggle = () => {
    folds?.set(sub.key, !open)
    setOpen(!open)
  }
  const offset = useContext(ClockOffsetContext)
  // The chat's clock in server time, moving by the minute: a subagent cut off long ago is stopped,
  // not running, and an open card flips at the 12 hour mark.
  const now = useMinuteClock() - offset
  const sessionOver = useChat()?.sessionOver === true
  const status = subagentStatus(sub, live, now, sessionOver)
  const stale = staleSubagent(sub, now)
  const over = status === 'done' || status === 'error'
  const title = subagentTitle(sub)
  const model = subagentModel(sub)
  const tokens = subagentTokens(sub)
  const type = sub.info.agent_type || inputField(sub.call?.call.input, 'subagent_type')
  const summary = over ? payloadOf(sub.done).summary ?? '' : ''
  const line = stale ? NO_END_RECORDED : over ? endLine(sub, status) : doingLine(sub)
  const resumed = resumedSubagent(sub)
  // A resumed run's call is the SendMessage: the message it was sent stands for the prompt.
  const prompt = inputField(sub.call?.call.input, resumed ? 'message' : 'prompt')
  // A background subagent's call returned its launch, not its report; a resumed run's call may have
  // returned only that it resumed.
  const result = sub.launched || (resumed && resumeAck(sub.call?.result?.output)) ? undefined : sub.call?.result
  const report = result && typeof result.output === 'string' ? (resumed ? handBackReport(result.output) : result.output) : ''
  // The report's first line (else the end's summary): at the top of the opened card, and for an
  // error in the row's tooltip.
  const gist = over ? firstLine(report) || firstLine(summary) : ''
  // Why a row reads ✗ or …, for its tooltip: the error's first line, or that no end was recorded.
  const why = status === 'error'
    ? (result?.is_error ? firstLine(report) : '') || firstLine(summary).replace(/^error:\s*/, '')
    : status === 'stopped' && stale ? NO_END_RECORDED : ''
  // Everything the row leaves out, for its tooltip and its accessible name.
  const label = [
    title,
    resumed ? 'resumed' : '',
    type,
    model,
    `configured effort: ${subagentEffort(sub) || 'unknown'}`,
    tokens !== null && tokens > 0 ? tokenLabel(tokens) : '',
    STATUS_LABEL[status],
    why,
  ].filter(Boolean).join(' · ')
  return (
    // status-background draws the card dashed: the turn went on without it.
    <div className={`agent-card subagent subagent-card status-${status}`} data-testid="subagent-card" data-status={status} data-call-id={sub.key}>
      <button type="button" className="subagent-row" data-testid="subagent-row" aria-expanded={open} aria-label={label} title={label} onClick={toggle}>
        <span className="subagent-title" data-testid="subagent-title">↳ {title}</span>
        {resumed && <span className="subagent-resumed" data-testid="subagent-resumed">resumed</span>}
        <StatusGlyph status={status} />
        <span className="tcg-chev" aria-hidden="true">{open ? '▾' : '▸'}</span>
      </button>
      {open && (
        <div className="subagent-body" data-testid="subagent-body">
          {line && <div className={`subagent-line${!over && !stale ? ' doing' : ''}`} data-testid="subagent-line">{line}</div>}
          {gist && <div className="subagent-line subagent-gist" data-testid="subagent-gist">↲ {gist}</div>}
          <div className="subagent-line" data-testid="subagent-settings">Model: {model || 'unknown'} · Configured effort: {subagentEffort(sub) || 'unknown'}</div>
          {prompt && (
            <details className="subagent-detail">
              <summary>{resumed ? 'Message' : 'Prompt'}</summary>
              <div className="subagent-prompt">{prompt}</div>
            </details>
          )}
          <Rows items={sub.children} live={live} options={options} />
          {result
            ? (
                <div className={`subagent-report${result.is_error ? ' error' : ''}`} data-testid="subagent-report">
                  <div className="subagent-report-label">{result.is_error ? 'Error' : 'Report'}</div>
                  <Markdown text={report} cards={options.cards} />
                </div>
              )
            // With no report, the end's summary: in full here when it is more than its first line.
            : summary.trim() !== firstLine(summary) && <div className="subagent-report" data-testid="subagent-report">↲ {summary}</div>}
        </div>
      )}
    </div>
  )
})
