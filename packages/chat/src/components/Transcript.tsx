// The timeline: start progress, the ready card, every event (folded into tool groups or as full
// cards), the streaming reply, sent-but-unrecorded messages, the working and waiting lines. It
// follows new output while the reader is at the bottom and holds their place while older events
// are put in above them.
import { Fragment, useLayoutEffect, useMemo, useRef, type ReactNode } from 'react'
import type { CardRegistry } from '../cards/registry'
import { parseTs, dayKey } from '../model/timeLabel'
import { HIDDEN_KINDS, buildTimeline, isVisibleEvent } from '../model/toolGroups'
import type { StartAttempt } from '../model/startStages'
import type { AgentEvent, ToolResultPayload, UserMessagePayload } from '../types'
import { EventRow, ToolCard, renderEvent, type DescribeUserMessage, type MessageFooter } from './cards/EventCards'
import { MessageText } from './AttachmentChips'
import Markdown from './Markdown'
import StartProgress from './StartProgress'
import { DayDivider } from './TimeLabel'
import { ToolGroup } from './ToolGroup'
import WorkingIndicator from './WorkingIndicator'
import type { QueuedMessage } from '../hooks/useSession'

// Placeholder bubbles shown while a session's transcript loads: the rough shape of a conversation
// (your message, a longer reply, a tool row, another exchange) so the page does not start blank.
export function HistorySkeleton() {
  const bars = (widths: number[]) => widths.map((w, i) => <span key={i} className="sk-bar" style={{ width: `${w}%` }} />)
  return (
    <div className="agent-skeleton" role="status" aria-label="Loading conversation" data-testid="history-loading">
      <div className="sk-bubble user">{bars([55, 30])}</div>
      <div className="sk-bubble assistant">{bars([92, 88, 70])}</div>
      <div className="sk-bubble tool">{bars([48])}</div>
      <div className="sk-bubble assistant">{bars([85, 95, 60])}</div>
      <div className="sk-bubble user">{bars([40])}</div>
      <div className="sk-bubble assistant">{bars([90, 75])}</div>
      <div className="sk-bubble tool">{bars([60])}</div>
      <div className="sk-bubble assistant">{bars([95, 82, 90, 55])}</div>
      <div className="sk-bubble user">{bars([65, 25])}</div>
      <div className="sk-bubble assistant">{bars([88, 92, 70])}</div>
      <div className="sk-bubble tool">{bars([42])}</div>
      <div className="sk-bubble assistant">{bars([90, 80])}</div>
    </div>
  )
}

// A turn's footer (stop reason, model, tokens, time taken) is shown inside the
// assistant bubble it closes, not as a floating line between bubbles. seq is the
// rendered sequence with null wherever a non-event (a tool card or group) sits:
// only an assistant reply IMMEDIATELY followed by its turn_done is paired.
export function pairFooters(seq: (AgentEvent | null)[]): { footers: Map<string, AgentEvent>; absorbed: Set<string> } {
  const footers = new Map<string, AgentEvent>()
  const absorbed = new Set<string>()
  for (let i = 0; i + 1 < seq.length; i++) {
    const a = seq[i]
    const b = seq[i + 1]
    if (a && b && a.kind === 'assistant_text' && b.kind === 'turn_done') {
      footers.set(a.client_event_id, b)
      absorbed.add(b.client_event_id)
    }
  }
  return { footers, absorbed }
}

// turnStartTimes maps each turn_done event to when its turn began: the time of
// the first real user message since the previous turn ended (messages sent
// while a turn runs are folded into it). Absent when unknown.
export function turnStartTimes(events: AgentEvent[]): Map<string, number> {
  const out = new Map<string, number>()
  let start: number | null = null
  for (const ev of events) {
    if (ev.kind === 'user_message') {
      if ((ev.payload as UserMessagePayload | undefined)?.source === 'system') continue
      // A message sent mid-turn is part of the turn already under way: the clock runs from the first.
      if (start === null) start = parseTs(ev.ts)
    } else if (ev.kind === 'turn_done') {
      if (start !== null) out.set(ev.client_event_id, start)
      start = null
    } else if (ev.kind === 'status_changed' && (ev.payload as { status?: unknown } | undefined)?.status === 'idle') {
      // Idle without a turn_done (a message that never started a turn): the next turn starts afresh.
      start = null
    }
  }
  return out
}

// dayBreaksOf: for consecutive [key, time] stamps, the keys whose local date
// differs from the previous dated item, mapped to their time. Undated items
// neither start a break nor reset the date.
export function dayBreaksOf(stamps: Array<[string, number | null]>): Map<string, number> {
  const out = new Map<string, number>()
  let prev: number | null = null
  for (const [key, ms] of stamps) {
    if (ms === null) continue
    const day = dayKey(ms)
    if (prev !== null && day !== prev) out.set(key, ms)
    prev = day
  }
  return out
}

export interface TranscriptProps {
  events: AgentEvent[]
  streaming: string
  compact: boolean
  cards?: CardRegistry
  messageFooter?: MessageFooter
  describeUserMessage?: DescribeUserMessage
  /** Call ids in flight in the turn now running. */
  liveIds: ReadonlySet<string>
  // ── loading ──
  showSkeleton: boolean
  loadingOlder: boolean
  // ── start ──
  starting: boolean
  attempt: StartAttempt | null
  startSession?: { id: string; status: string }
  onStopSession?: () => Promise<void>
  clockOffset: number
  /** The card that heads a live transcript (ChatView's default or the host's). */
  readyCard: ReactNode
  // ── the turn ──
  working: boolean
  waiting: boolean
  since: number
  now: number
  waitingOn: { tool: string; startedAt: number } | null
  // ── sent and not yet recorded ──
  queued: QueuedMessage[]
  queueState: 'failed' | 'queued' | 'sending'
  /** Whether a message sent mid-turn reaches the agent at its next step (Claude Code) or waits. */
  steers: boolean
  /** Goes up by one each time the view should be pinned to the bottom again (ChatView, on send). */
  pin: number
}

export default function Transcript(p: TranscriptProps) {
  const { events, streaming, compact, cards, messageFooter, describeUserMessage, liveIds } = p
  const options = useMemo(() => ({ cards, messageFooter, describeUserMessage }), [cards, messageFooter, describeUserMessage])
  const bottomRef = useRef<HTMLDivElement | null>(null)
  const timelineRef = useRef<HTMLDivElement | null>(null)
  const loadingHistory = p.showSkeleton

  // Follow new output only while the reader is at the bottom (or has just sent something).
  const stick = useRef(true)
  const pin = p.pin
  useLayoutEffect(() => {
    if (pin > 0) stick.current = true
  }, [pin])

  // The transcript arrives in pages. While it is still loading, do not paint or scroll it page by
  // page (that is a visible storm of jumps); show it once, already at the bottom.
  useLayoutEffect(() => {
    if (loadingHistory) return
    if (stick.current) bottomRef.current?.scrollIntoView?.({ block: 'end' })
    // queued.length: a message sent mid-turn shows as a queued bubble before any event arrives,
    // and the send just pinned the view to the bottom for it.
  }, [events.length, p.queued.length, streaming, p.working, p.waiting, loadingHistory, pin])

  // Older events arrive above what the reader is looking at. When they are not at the bottom, move
  // the view down by exactly what was added so the text under their eyes does not jump.
  const heldFirstSeq = useRef(0)
  const heldHeight = useRef(0)
  const firstSeq = events.find(e => e.seq != null)?.seq ?? 0
  useLayoutEffect(() => {
    const el = timelineRef.current
    if (!el) return
    const grewUpward = heldFirstSeq.current > 0 && firstSeq > 0 && firstSeq < heldFirstSeq.current
    if (grewUpward && !stick.current) el.scrollTop += el.scrollHeight - heldHeight.current
    heldFirstSeq.current = firstSeq
    heldHeight.current = el.scrollHeight
  })

  function onScroll(e: React.UIEvent<HTMLDivElement>) {
    const el = e.currentTarget
    stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
  }

  // Pair tool_results with their calls so each renders as one card (legacy
  // view), or fold runs of calls into groups (compact view).
  const resultsByCall = useMemo(() => {
    const m = new Map<string, ToolResultPayload>()
    if (compact) return m
    for (const ev of events) {
      if (ev.kind === 'tool_result') {
        const r = ev.payload as ToolResultPayload
        m.set(r.call_id, r)
      }
    }
    return m
  }, [events, compact])
  const timelineItems = useMemo(() => (compact ? buildTimeline(events) : []), [events, compact])
  // When each turn began (the user message before its turn_done), for "took 42 s".
  const turnStarts = useMemo(() => turnStartTimes(events), [events])
  // Turn footers folded into the assistant bubble that closes each turn.
  const footerPairs = useMemo(() => pairFooters(compact
    ? timelineItems.map(item => (item.type === 'event' ? item.ev : null))
    : events.filter(ev => !HIDDEN_KINDS.has(ev.kind))), [events, timelineItems, compact])
  // Where the local date changes between consecutive visible items: the
  // divider goes before the item, keyed like it, carrying that item's time.
  const dayBreaks = useMemo(() => {
    const stamps: Array<[string, number | null]> = compact
      ? timelineItems.map(item => item.type === 'tools'
          ? [`tools:${item.key}`, parseTs(item.entries[0].ev.ts)]
          : item.type === 'card'
            ? [item.entry.ev.client_event_id, parseTs(item.entry.ev.ts)]
            : [item.ev.client_event_id, parseTs(item.ev.ts)])
      : events.filter(isVisibleEvent).map(ev => [ev.client_event_id, parseTs(ev.ts)])
    return dayBreaksOf(stamps)
  }, [events, timelineItems, compact])

  const q = p.queued
  // A first start has nothing above its progress panel. A RESUME (a new pod for a session that
  // already has a conversation) does: its panel goes after the conversation, where the person
  // just sent the message that started it, not above everything they would have to scroll past.
  const resuming = p.starting && !!p.attempt && events.some(isVisibleEvent)
  const startPanel = p.starting && p.attempt
    ? <StartProgress attempt={p.attempt} session={p.startSession} onStop={p.onStopSession} clockOffset={p.clockOffset} resuming={resuming} />
    : null
  return (
    <>
      {p.showSkeleton && <HistorySkeleton />}
      <div ref={timelineRef} className={`agent-timeline${p.showSkeleton ? ' loading' : ''}${p.loadingOlder ? ' backfilling' : ''}`} onScroll={onScroll}>
        {p.loadingOlder && (
          <div className="older-loading" data-testid="older-loading" role="status">
            <span className="queue-spinner" aria-hidden="true" /> Loading earlier messages…
          </div>
        )}
        {!resuming && startPanel}
        {!p.starting && !p.loadingOlder && p.readyCard}
        {compact
          ? timelineItems.map(item => {
              if (item.type === 'tools') {
                const k = `tools:${item.key}`
                return (
                  <Fragment key={k}>
                    {dayBreaks.has(k) && <DayDivider ts={dayBreaks.get(k)!} />}
                    <ToolGroup entries={item.entries} live={liveIds} />
                  </Fragment>
                )
              }
              if (item.type === 'card') {
                const k = item.entry.ev.client_event_id
                return (
                  <Fragment key={k}>
                    {dayBreaks.has(k) && <DayDivider ts={dayBreaks.get(k)!} />}
                    <ToolCard call={item.entry.call} result={item.entry.result} ts={item.entry.ev.ts} />
                  </Fragment>
                )
              }
              const k = item.ev.client_event_id
              if (footerPairs.absorbed.has(k)) return null
              const footer = footerPairs.footers.get(k)
              return (
                <Fragment key={k}>
                  {dayBreaks.has(k) && <DayDivider ts={dayBreaks.get(k)!} />}
                  <EventRow
                    ev={item.ev}
                    turnStart={turnStarts.get(k)}
                    footer={footer}
                    footerStart={footer ? turnStarts.get(footer.client_event_id) : undefined}
                    options={options}
                  />
                </Fragment>
              )
            })
          : events.filter(ev => !HIDDEN_KINDS.has(ev.kind) && !footerPairs.absorbed.has(ev.client_event_id)).map(ev => {
              const footer = footerPairs.footers.get(ev.client_event_id)
              return (
                <Fragment key={ev.client_event_id}>
                  {dayBreaks.has(ev.client_event_id) && <DayDivider ts={dayBreaks.get(ev.client_event_id)!} />}
                  {renderEvent(ev, resultsByCall, turnStarts.get(ev.client_event_id), footer,
                    footer ? turnStarts.get(footer.client_event_id) : undefined, options)}
                </Fragment>
              )
            })}
        {streaming && (
          <div className="agent-card assistant streaming" data-testid="streaming">
            <Markdown text={streaming} cards={cards} />
          </div>
        )}
        {resuming && startPanel}
        {q.map((m, i) => (
          <div key={m.id} className={`agent-card user queued ${p.queueState}`} data-testid="queued-message">
            <span className="card-author">You</span>
            <MessageText text={m.text} cards={cards} />
            <div className="queue-status" role="status">
              {p.queueState === 'failed'
                ? <><span aria-hidden="true">⚠</span> Not delivered</>
                : <>
                    <span className="queue-spinner" aria-hidden="true" />
                    {p.queueState === 'sending'
                      ? 'Sending…'
                      : q.length === 1
                        ? (p.steers ? 'Queued — the agent picks it up at its next step' : 'Queued')
                        : `Queued · ${i + 1} of ${q.length}`}
                  </>}
            </div>
          </div>
        ))}
        {p.working && !streaming && <WorkingIndicator since={p.since} now={p.now} waitingOn={p.waitingOn} />}
        {p.waiting && (
          <div className="agent-waiting" role="status" aria-live="polite" data-testid="waiting">
            <span className="agent-waiting-dot" aria-hidden="true" />
            Waiting for your answer
          </div>
        )}
        <div ref={bottomRef} />
      </div>
    </>
  )
}
