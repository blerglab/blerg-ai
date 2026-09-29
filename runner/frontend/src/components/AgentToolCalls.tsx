// Compact presentation of tool activity in an agent transcript: a run of
// consecutive calls becomes one group — inline one-liners for one or two
// calls, a collapsed summary row for three or more.
import { memo, useContext, useState } from 'react'
import {
  OUTPUT_LINE_CAP,
  describeInput,
  failureLine,
  formatDuration,
  groupStats,
  outputText,
  previewLine,
  safeJSON,
  summaryText,
  type ToolEntry,
} from '../lib/toolGroups'
import type { ToolResultPayload } from '../types'
import { TimeLabel } from './TimeLabel'
import { absoluteLabel, parseTs } from '../lib/timeLabel'
import { ClockOffsetContext } from '../lib/clockOffset'
import { useSecondClock } from '../hooks/useSecondClock'
import {
  LONG_RUNNING_MS,
  STILL_RUNNING_MS,
  longRunningTitle,
  runningLabel,
  runningMs,
} from '../lib/liveCalls'

const STILL_RUNNING_HINT = 'still running — Stop interrupts the turn'

// runningFor: ms a live call has run, ticking on the one shared second clock.
// Null (no clock at all) when the call is not live or its ts is unusable.
function useRunningFor(entry: ToolEntry, live: boolean): number | null {
  const offset = useContext(ClockOffsetContext)
  const now = useSecondClock(live)
  return live ? runningMs(parseTs(entry.ev.ts), now, offset) : null
}

function RunClock({ ms, text }: { ms: number; text?: string }) {
  const long = ms >= LONG_RUNNING_MS
  return (
    <span
      className={`tcg-clock${long ? ' tcg-clock-long' : ''}`}
      data-testid="tool-clock"
      title={long ? longRunningTitle(ms) : undefined}
    >
      {text ?? `running ${runningLabel(ms)}`}
    </span>
  )
}

const MAX_REVEALED_FAILURES = 5

function Status({ result, running }: { result?: ToolResultPayload; running: boolean }) {
  if (!result) {
    return running
      ? <span className="tcg-spin" role="img" aria-label="running" />
      : <span className="tcg-glyph" role="img" aria-label="no result">…</span>
  }
  return result.is_error
    ? <span className="tcg-glyph tcg-bad" role="img" aria-label="failed">✗</span>
    : <span className="tcg-glyph tcg-ok" role="img" aria-label="succeeded">✓</span>
}

function ToolDetail({ entry }: { entry: ToolEntry }) {
  const [all, setAll] = useState(false)
  const { result } = entry
  const output = result ? outputText(result) : ''
  const lines = output.split('\n')
  const capped = !all && lines.length > OUTPUT_LINE_CAP
  const shown = capped ? lines.slice(0, OUTPUT_LINE_CAP).join('\n') : output
  return (
    <div className="tcg-detail" data-testid="tool-detail">
      <pre>{safeJSON(entry.call.input)}</pre>
      {result && (
        <pre className={result.is_error ? 'tcg-out tcg-out-bad' : 'tcg-out'}>
          {`─── result${typeof result.duration_ms === 'number' ? ` (${result.duration_ms}ms)` : ''} ───\n${shown}`}
        </pre>
      )}
      {capped && (
        <button type="button" className="tcg-showall" onClick={() => setAll(true)}>
          Show all {lines.length} lines
        </button>
      )}
    </div>
  )
}

// The absolute time of a call, for a row's tooltip; none when unknown.
function rowTitle(entry: ToolEntry): string | undefined {
  const ms = parseTs(entry.ev.ts)
  return ms === null ? undefined : absoluteLabel(ms)
}

function ToolRow({ entry, open, onToggle, live }: {
  entry: ToolEntry
  open: boolean
  onToggle: () => void
  live: boolean
}) {
  const { call, result } = entry
  const ms = useRunningFor(entry, live && !entry.result)
  const { summary, shell } = describeInput(call.input)
  return (
    <div className={`tcg-row ${result?.is_error ? 'tcg-row-bad' : ''}`} data-testid="tool-row">
      <button type="button" className="tcg-row-head" aria-expanded={open} onClick={onToggle} title={rowTitle(entry)}>
        <Status result={result} running={live} />
        <span className="tcg-name">{call.tool}</span>
        <span className="tcg-sum">{shell ? '$ ' : ''}{summary}</span>
        {ms !== null && <RunClock ms={ms} />}
        {ms !== null && ms >= STILL_RUNNING_MS && <span className="tcg-hint">{STILL_RUNNING_HINT}</span>}
        {result && typeof result.duration_ms === 'number' && (
          <span className="tcg-dur">{formatDuration(result.duration_ms)}</span>
        )}
      </button>
      {open && <ToolDetail entry={entry} />}
    </div>
  )
}

interface ToolGroupProps {
  entries: ToolEntry[]
  /** Call ids in flight in the turn now running (see lib/liveCalls). */
  live: ReadonlySet<string>
}

function sameGroup(a: ToolGroupProps, b: ToolGroupProps): boolean {
  if (a.entries.length !== b.entries.length) return false
  return a.entries.every((e, i) =>
    e.ev === b.entries[i].ev && e.result === b.entries[i].result &&
    a.live.has(e.call.call_id) === b.live.has(e.call.call_id))
}

// ToolGroup keeps its own expanded state; the caller keys it by the call id of
// the group's first call, so the state survives calls being appended.
export const ToolGroup = memo(function ToolGroup({ entries, live }: ToolGroupProps) {
  const [open, setOpen] = useState(false)
  const [openRows, setOpenRows] = useState<ReadonlySet<string>>(new Set())
  const toggleRow = (id: string) => setOpenRows(prev => {
    const next = new Set(prev)
    if (!next.delete(id)) next.add(id)
    return next
  })
  // Hooks first: a group can grow from inline to collapsed between renders.
  const liveEntry = [...entries].reverse().find(e => !e.result && live.has(e.call.call_id))
  const inFlight = liveEntry !== undefined
  const latest = liveEntry ?? entries[entries.length - 1]
  const ms = useRunningFor(latest, inFlight)
  const rows = (
    <>
      {entries.map(e => (
        <ToolRow
          key={e.ev.client_event_id}
          entry={e}
          open={openRows.has(e.ev.client_event_id)}
          onToggle={() => toggleRow(e.ev.client_event_id)}
          live={live.has(e.call.call_id)}
        />
      ))}
    </>
  )
  if (entries.length <= 2) {
    return <div className="tcg-inline" data-testid="tool-inline">{rows}</div>
  }

  const stats = groupStats(entries)
  const failures = entries.filter(e => e.result?.is_error)
  return (
    <div className={`tcg-group ${stats.failed > 0 ? 'tcg-group-bad' : ''}`} data-testid="tool-group">
      <button
        type="button"
        className="tcg-summary"
        data-testid="tool-group-toggle"
        aria-expanded={open}
        onClick={() => setOpen(o => !o)}
      >
        <span className="tcg-line">
          {inFlight
            ? <span className="tcg-spin" role="img" aria-label="running" />
            : stats.failed > 0
              ? <span className="tcg-glyph tcg-bad" role="img" aria-label="failed">✗</span>
              : stats.pending > 0
                ? <span className="tcg-glyph" role="img" aria-label="no result">…</span>
                : <span className="tcg-glyph tcg-ok" role="img" aria-label="succeeded">✓</span>}
          <span className="tcg-title">{summaryText(stats)}</span>
          {stats.failed > 0 && <span className="tcg-badge" data-testid="tool-fail-badge">✗ {stats.failed} failed</span>}
          {ms !== null
            ? <RunClock ms={ms} text={`${runningLabel(ms)} running`} />
            : stats.hasDuration && <span className="tcg-dur">{formatDuration(stats.durationMs)}</span>}
          <TimeLabel ts={entries[entries.length - 1].ev.ts} />
          <span className="tcg-chev" aria-hidden="true">{open ? '▾' : '▸'}</span>
        </span>
        {!open && (
          <span className="tcg-preview" data-testid="tool-preview">
            {previewLine(latest.call)}
            {ms !== null && <>{'  ·  '}<RunClock ms={ms} />{ms >= STILL_RUNNING_MS && <span className="tcg-hint">{'  ·  '}{STILL_RUNNING_HINT}</span>}</>}
          </span>
        )}
      </button>
      {!open && failures.slice(0, MAX_REVEALED_FAILURES).map(e => (
        <div key={e.ev.client_event_id} className="tcg-fail" data-testid="tool-fail">
          <span className="tcg-name">{e.call.tool}</span> {failureLine(e.result)}
        </div>
      ))}
      {!open && failures.length > MAX_REVEALED_FAILURES && (
        <div className="tcg-fail">+{failures.length - MAX_REVEALED_FAILURES} more failed</div>
      )}
      {open && <div className="tcg-rows">{rows}</div>}
    </div>
  )
}, sameGroup)
