import type { Reasoning } from '../model/reasoning'
// The timeline: start progress, the ready card, every event (folded into tool groups or as full
// cards), the streaming reply, sent-but-unrecorded messages, the working and waiting lines. It
// draws the newest WINDOW rows and reveals more as the reader scrolls up, follows new output until
// the reader scrolls up (and again once they are back at the very end), and holds their place while
// rows are put in above them.
import { Component, Fragment, memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { CardRegistry } from '../cards/registry'
import { parseTs, dayKey } from '../model/timeLabel'
import { HIDDEN_KINDS, isVisibleEvent, parentCallOf, sharedTimeline, timelineBuilder, type SubagentEntry } from '../model/toolGroups'
import { foldTicketRun } from '../model/worklist'
import type { StartAttempt } from '../model/startStages'
import type { WaitingOn } from '../model/liveCalls'
import { firstPersonMessage } from '../model/userMessages'
import { reviewRounds } from '../model/reviewRounds'
import { ReviewRoundsContext } from './ReviewRoundLine'
import type { AgentEvent, ToolResultPayload, UserMessagePayload } from '../types'
import { EventRow, ToolCard, renderEvent, type DescribeUserMessage, type MessageFooter } from './cards/EventCards'
import { MessageText } from './AttachmentChips'
import Markdown from './Markdown'
import StartProgress from './StartProgress'
import { SubagentCard } from './SubagentCard'
import { DayDivider } from './TimeLabel'
import { LongPressSheet, useLongPress } from './LongPress'
import { ToolGroup } from './ToolGroup'
import WorkingIndicator from './WorkingIndicator'
import type { QueuedMessage } from '../hooks/useSession'
import { useTicker } from '../hooks/useTicker'
import { inputClock } from '../model/clock'

/** How many rows (timeline items) the transcript draws at first, and how many more each time the
 *  reader reaches the top of what is drawn. The timeline, day breaks, turn footers and turn times
 *  are still worked out over every event held (cheap); only the drawing is windowed, because
 *  drawing thousands of rows at once is what makes a long session slow to open. */
export const WINDOW = 300

// How far above the view (in view heights) the top of the drawn rows starts revealing more, or
// fetching an older page: far enough that a page usually lands before the reader reaches it.
const AHEAD_VIEWS = 1.5
const AHEAD = `${AHEAD_VIEWS * 100}%`
// After a page lands, the next is asked for only once the reader scrolls up again (not while they
// sit at the top), and never sooner than this after the last request.
const ASK_GAP_MS = 400
// Scroll events in this long after a page lands may still be the glide it stopped: one is taken as
// the reader scrolling only once the window has passed.
const LANDED_SETTLE_MS = 150
// A wheel event this long after the one before starts a new gesture (a momentum tail never pauses).
const WHEEL_GESTURE_GAP_MS = 100
// The loading pill shows only for a page that takes longer than this.
const SLOW_LOAD_MS = 300
// How long after a page lands "Loaded earlier messages" is said, unless another page is loading.
const LOADED_SAY_MS = 1000

// How close to the end of the timeline counts as at the end, where following new output resumes.
const AT_END_PX = 8

// How long after a scroll of our own a scroll event is still taken as that scroll's (a few frames).
const OURS_MS = 50
const markOurs = (r: { current: number }) => { r.current = performance.now() }
const isOurs = (r: { current: number }) => r.current !== 0 && performance.now() - r.current < OURS_MS

// How long after the reader's finger lifts the view is still left where they put it (a flick goes on
// scrolling after the lift), and how long after they asked to go up a scroll landing near the end
// is still theirs (not a reason to resume following).
const SETTLE_MS = 300
// How far a finger moves down the screen (the content up), more than sideways, before it reads as
// scrolling up: a tap wobbles a few px, and a sideways swipe on a code block is not a scroll up.
const TOUCH_UP_PX = 10
// How long a finger may go without a move reaching the timeline before it no longer counts as down.
// The row under it can be taken out mid-touch (a streaming reply finished, the working line gone,
// the window trimmed); its touchmove and touchend then go to a detached node and never reach us.
const TOUCH_LOST_MS = 1500
// Keys that scroll up (Shift+Space too).
const UP_KEYS = new Set(['ArrowUp', 'PageUp', 'Home'])
// A key pressed in a text box moves its caret, not the timeline.
function typing(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false
  return t.isContentEditable || t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT'
}
// The input is for a box inside the timeline that can still scroll up itself (a long command's
// output, a code block with a max height): it scrolls that box, not the timeline.
function innerCanGoUp(t: EventTarget | null, root: Element): boolean {
  for (let el = t instanceof Element ? t : null; el && el !== root; el = el.parentElement) {
    if (el.scrollHeight > el.clientHeight && el.scrollTop > 0) return true
  }
  return false
}

// Where an element sits in the timeline's visible area (0: at its top edge).
function viewTop(root: Element, el: Element): number {
  return el.getBoundingClientRect().top - root.getBoundingClientRect().top
}

// How many rows in view the reader's place is kept by: the first one still drawn holds it (a row at
// the edge of an older page can be drawn again as a new element when the page merges into it).
const ANCHORS = 3
// After an upward wheel, keyboard or touch input, how long the view can still be gliding on its own
// (a flick's momentum, a smooth scroll), so an older page landing then stops it.
const GLIDE_MS = 1500
const TOUCH_GLIDE_MS = 3000
// Wheel events closer together than this are one stream (a trackpad's momentum comes as a stream),
// and the longest a stream is held back after a page lands (a mouse wheel spun on and on is the
// reader scrolling, not momentum).
const WHEEL_STREAM_MS = 120
const WHEEL_HOLD_MS = 500
// A trackpad scrolls in small pixel steps; a mouse wheel's notch is a larger one (or in lines).
const FINE_WHEEL_PX = 50
// What never holds the reader's place: the loading line (it comes and goes) and the sentinel (rows
// are put in below it).
const NO_ANCHOR = 'data-no-anchor'

/** The first few rows in the timeline's view, top first, and their content offsets (where each sits
 *  from the top of the scrolled content, which the reader's own scrolling does not change). Only the
 *  timeline's own children before `end` are rows; they are in order down the page, so the first in
 *  view is found by bisection. */
function anchorsIn(root: HTMLElement, end: Element | null): Array<{ el: Element; at: number }> {
  const kids = root.children
  // The end marker sits near the bottom (only the pill and a sheet come after it).
  let hi = kids.length
  for (let i = kids.length - 1; i >= 0; i--) if (kids[i] === end) { hi = i; break }
  const box = root.getBoundingClientRect()
  const viewEnd = box.top + root.clientHeight
  let lo = 0
  let top = hi
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    if (kids[mid].getBoundingClientRect().bottom > box.top) { top = mid; hi = mid } else lo = mid + 1
  }
  // Rows that start in view come first: one cut off by the top edge can still grow inside, above
  // the view (an image in it sizing late), which would push what the reader sees down. It holds the
  // place only when no row starts in view (a row taller than the view).
  const out: Array<{ el: Element; at: number }> = []
  let cut: { el: Element; at: number } | null = null
  for (let i = top; i < kids.length && kids[i] !== end && out.length < ANCHORS; i++) {
    const el = kids[i]
    if (el.hasAttribute(NO_ANCHOR)) continue
    const r = el.getBoundingClientRect()
    if (r.height <= 0) continue
    if (r.top >= viewEnd) break
    const a = { el, at: r.top - box.top + root.scrollTop }
    if (r.top < box.top - 0.5) cut ??= a
    else out.push(a)
  }
  if (cut) out.push(cut)
  return out
}

// Calls `on` in a commit that changes `when`, before React touches the DOM: what is measured then is
// the page as the reader last saw it (a class's getSnapshotBeforeUpdate is the one hook at that point).
class BeforeDomUpdate extends Component<{ when: string; on: () => void }> {
  getSnapshotBeforeUpdate(prev: Readonly<{ when: string }>) {
    if (prev.when !== this.props.when) this.props.on()
    return null
  }
  componentDidUpdate() {}
  render() { return null }
}

// The working line with its clock. It ticks on its own, so the second-by-second clock re-renders
// this line and not the timeline. since: server time the turn began (null: unknown, shows 0 s).
function TurnClock({ since, clockOffset, waitingOn, background, activity, reasoning }: {
  since: number | null
  clockOffset: number
  waitingOn: WaitingOn | null
  background?: BackgroundLine | null
  reasoning?: Reasoning | null
  activity?: string | null
}) {
  const now = useTicker(true) - clockOffset
  return <WorkingIndicator since={since ?? now} now={now} waitingOn={waitingOn} background={background} activity={activity} reasoning={reasoning} />
}

/** The background tasks running, for the working line: how many, the description of the only one,
 *  and when the oldest started (server time; null when unknown). */
export interface BackgroundLine {
  count: number
  description: string
  command?: string
  since: number | null
}

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
  /** The window goes back to the newest rows when this changes. */
  sessionId?: string
  events: AgentEvent[]
  streaming: string
  compact: boolean
  /** Whether the model's thinking is drawn (default) or left out. 'open' and 'collapsed' are the
   *  old names for shown and hidden. */
  thinking?: 'shown' | 'hidden' | 'open' | 'collapsed'
  cards?: CardRegistry
  messageFooter?: MessageFooter
  describeUserMessage?: DescribeUserMessage
  /** Call ids in flight in the turn now running. */
  liveIds: ReadonlySet<string>
  // ── loading ──
  showSkeleton: boolean
  /** An older page is being fetched right now. */
  loadingOlder: boolean
  /** The server holds older events than these. */
  hasOlder?: boolean
  /** Asks for the next older page (called when every held row is drawn and the reader reaches
   *  the top). */
  onLoadOlder?: () => void
  // ── start ──
  starting: boolean
  attempt: StartAttempt | null
  startSession?: { id: string; status: string; runtime?: string }
  onStopSession?: () => Promise<void>
  clockOffset: number
  /** The card that heads a live transcript (ChatView's default or the host's). */
  readyCard: ReactNode
  // ── the turn ──
  working: boolean
  waiting: boolean
  /** Server time the running turn began; null when unknown. */
  since: number | null
  waitingOn: WaitingOn | null
  /** What the agent is on, when known ("Thinking…", a model retry): the working line's words. */
  reasoning?: Reasoning | null
  activity?: string | null
  /** Background tasks running (null: none). While the turn has nothing else in flight, or the
   *  session is idle, the working line names them. */
  background?: BackgroundLine | null
  // ── sent and not yet recorded ──
  queued: QueuedMessage[]
  queueState: 'failed' | 'queued' | 'sending'
  /** Whether a message sent mid-turn reaches the agent at its next step (Claude Code) or waits. */
  steers: boolean
  /** Goes up by one each time the view should be pinned to the bottom again (ChatView, on send). */
  pin: number
}

// Memoised: ChatView re-renders on every keystroke in the composer and every tick of its clock, and
// none of that reaches the timeline unless one of these props changed (ChatView keeps them stable).
const Transcript = memo(function Transcript(p: TranscriptProps) {
  const { streaming, compact, cards, messageFooter, describeUserMessage, liveIds } = p
  // Hidden thinking is not a row at all: it is taken out of the events here, so the timeline, the
  // window and the "New output" pill never see it.
  const hideThinking = p.thinking === 'hidden' || p.thinking === 'collapsed'
  const events = useMemo(() => (hideThinking ? p.events.filter(ev => ev.kind !== 'assistant_thinking') : p.events), [p.events, hideThinking])
  // The session's first user message, once nothing older is left to fetch (a host may draw it as
  // the session's brief).
  const firstUserMessage = useMemo(
    () => (describeUserMessage && !p.hasOlder && !p.showSkeleton ? firstPersonMessage(events) : null),
    [describeUserMessage, p.hasOlder, p.showSkeleton, events],
  )
  // The subagent cards opened by hand, by call id: a card unmounted by the window and drawn again
  // reads it, so it does not close. Another session starts with none.
  const [foldSet, setFoldSet] = useState(() => ({ session: p.sessionId, folds: new Map<string, boolean>() }))
  if (foldSet.session !== p.sessionId) setFoldSet({ session: p.sessionId, folds: new Map() })
  const subagentFolds = foldSet.folds
  const options = useMemo(
    () => ({ cards, messageFooter, describeUserMessage, firstUserMessage, subagentFolds }),
    [cards, messageFooter, describeUserMessage, firstUserMessage, subagentFolds],
  )
  const bottomRef = useRef<HTMLDivElement | null>(null)
  const timelineRef = useRef<HTMLDivElement | null>(null)
  const lp = useLongPress(timelineRef)
  const sentinelRef = useRef<HTMLDivElement | null>(null)
  const loadingHistory = p.showSkeleton
  const sessionId = p.sessionId ?? ''

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
  // Built from the previous timeline, so a tool group that did not change keeps its entries.
  // A session's timeline is shared with the views beside the chat (sharedTimeline), so it is built
  // once per change; a transcript drawn without a session keeps its own.
  const [build] = useState(timelineBuilder)
  const timelineItems = useMemo(
    () => (compact ? (sessionId ? sharedTimeline(sessionId, events) : build(events)) : []),
    [build, events, compact, sessionId],
  )
  // The full-card view keeps one row per event, but a subagent is still one card holding its own
  // events: anchors maps the event each card sits at to the card, folded the events it holds.
  const [buildFull] = useState(() => timelineBuilder({ groupTools: false }))
  const fullSubs = useMemo(() => {
    const anchors = new Map<string, SubagentEntry>()
    const folded = new Set<string>()
    if (compact || !events.some(ev => ev.kind.startsWith('subagent_') || parentCallOf(ev))) return { anchors, folded }
    for (const item of (sessionId ? sharedTimeline(sessionId, events, { groupTools: false }) : buildFull(events))) {
      if (item.type !== 'subagent') continue
      anchors.set(item.sub.anchor.client_event_id, item.sub)
      for (const id of item.sub.ids.slice(1)) folded.add(id)
    }
    return { anchors, folded }
  }, [buildFull, events, compact, sessionId])
  // Review answers: every publish of a round but its newest draws nothing; that one is the round's line.
  const rounds = useMemo(() => reviewRounds(events), [events])
  const fullSeq = useMemo(
    () => (compact ? [] : events.filter(ev => !HIDDEN_KINDS.has(ev.kind) && !fullSubs.folded.has(ev.client_event_id) && !rounds.hidden.has(ev.client_event_id))),
    [events, compact, fullSubs, rounds],
  )
  // When each turn began (the user message before its turn_done), for "took 42 s".
  const turnStarts = useMemo(() => turnStartTimes(events), [events])
  // Turn footers folded into the assistant bubble that closes each turn.
  const footerPairs = useMemo(() => pairFooters(compact
    ? timelineItems.map(item => (item.type === 'event' ? item.ev : null))
    : fullSeq), [timelineItems, fullSeq, compact])
  // Where the local date changes between consecutive visible items: the
  // divider goes before the item, keyed like it, carrying that item's time.
  const dayBreaks = useMemo(() => {
    const stamps: Array<[string, number | null]> = compact
      ? timelineItems.map(item => item.type === 'tools'
          ? [`tools:${item.key}`, parseTs(item.entries[0].ev.ts)]
          : item.type === 'subagent'
            ? [`sub:${item.key}`, parseTs(item.sub.anchor.ts)]
            : item.type === 'card'
              ? [item.entry.ev.client_event_id, parseTs(item.entry.ev.ts)]
              : [item.ev.client_event_id, parseTs(item.ev.ts)])
      : fullSeq.filter(ev => isVisibleEvent(ev) || fullSubs.anchors.has(ev.client_event_id)).map(ev => [ev.client_event_id, parseTs(ev.ts)])
    return dayBreaksOf(stamps)
  }, [fullSeq, fullSubs, timelineItems, compact])
  // The full-card view's rows: every event drawn, less the turn ends folded into a reply and the
  // events a subagent card holds.
  const fullRows = useMemo(() => {
    if (compact) return []
    const rows: AgentEvent[] = []
    for (const ev of fullSeq) {
      if (footerPairs.absorbed.has(ev.client_event_id)) continue
      // Consecutive pins (or unpins) are one row, as in the compact view.
      const run = rows.length ? foldTicketRun(rows[rows.length - 1], ev) : null
      if (run) rows[rows.length - 1] = run
      else rows.push(ev)
    }
    return rows
  }, [fullSeq, compact, footerPairs])

  // ── the window ──
  // Each row's first event, and the row every event drawn belongs to. The window is pinned by the
  // event its first row starts with: older events put in front leave it where it is, live events
  // appended at the bottom are drawn, and the row a merged tool group ends up in is still found.
  const rowCount = compact ? timelineItems.length : fullRows.length
  const rowIndex = useMemo(() => {
    const firstIds: string[] = []
    const at = new Map<string, number>()
    const add = (ids: string[]) => {
      for (const id of ids) at.set(id, firstIds.length)
      firstIds.push(ids[0])
    }
    if (compact) {
      for (const item of timelineItems) {
        add(item.type === 'tools'
          ? item.entries.map(e => e.ev.client_event_id)
          : item.type === 'subagent'
            ? item.sub.ids
            : [item.type === 'card' ? item.entry.ev.client_event_id : item.ev.client_event_id])
      }
    } else {
      for (const ev of fullRows) add(fullSubs.anchors.get(ev.client_event_id)?.ids ?? [ev.client_event_id])
    }
    return { firstIds, at }
  }, [compact, timelineItems, fullRows, fullSubs])
  // The first drawn row's event; null: the newest WINDOW rows. Back to null for another session and
  // while the transcript loads (a forward catch-up is drawn as the newest rows, not all of it).
  // Showing or hiding thinking changes which rows there are, so it starts the window over too.
  const winKey = `${sessionId}\0${hideThinking}`
  // top: the reader reached the top and the drawn rows begin at the first row held. An older page put in front of them then has its
  // rows drawn at once (up to WINDOW more), in the same render that takes the loading line away: the
  // content above the reader only grows, so their place can always be kept (a line taken away with
  // nothing yet above it, at the very top, could not be made up for until the rows were revealed).
  const [win, setWin] = useState<{ session: string; anchor: string | null; top: boolean }>({ session: winKey, anchor: null, top: false })
  const anchor = win.session === winKey && !loadingHistory ? win.anchor : null
  const pinned = anchor !== null ? rowIndex.at.get(anchor) : undefined
  const start = pinned === undefined
    ? Math.max(0, rowCount - WINDOW)
    : win.top && pinned > 0 ? Math.max(0, pinned - WINDOW) : pinned
  const wanted = !loadingHistory && rowCount > 0 ? rowIndex.firstIds[start] : null
  // Set only by the sentinel once the reader reaches the top (never while they follow the end, so a
  // session opens drawing what it did); kept while the drawn rows still begin at the first one.
  const keepTop = win.top && wanted !== null && start === 0
  if (win.session !== winKey || win.anchor !== wanted || win.top !== keepTop) setWin({ session: winKey, anchor: wanted, top: keepTop })
  // The row WINDOW above the first drawn, revealed when the reader reaches the top.
  const revealTo = start > 0 ? rowIndex.firstIds[Math.max(0, start - WINDOW)] : null
  const hasOlder = !!p.hasOlder
  // Something is above the drawn rows: held rows not drawn yet, or older events on the server.
  const more = !loadingHistory && rowCount > 0 && (start > 0 || hasOlder)
  const canFetch = start === 0 && hasOlder && !p.loadingOlder
  // Every held row is drawn and the server has older ones.
  const atHeldTop = start === 0 && hasOlder
  // Every row is drawn and the server has nothing older: the very top of the session.
  const atBeginning = !loadingHistory && rowCount > 0 && start === 0 && !hasOlder && !p.loadingOlder

  // Follow new output until the reader scrolls up. Any upward scroll stops it, even a short one
  // inside the newest card (on a phone that card is often taller than the screen, so "near the
  // bottom" would still be following and every event would pull the view away from what they read);
  // it resumes only at the very end (within AT_END_PX) or when they send something (pin).
  const stick = useRef(true)
  // The reader's last scroll position, to tell an upward scroll from a downward one.
  const lastTop = useRef(0)
  // Set (to when) just before every scroll we make ourselves (following, the pill, keeping the
  // reader's place, trimming the window), so it does not read as the reader scrolling up. The next
  // scroll event clears it; a move that scrolled nothing fires none, so it also lapses after
  // OURS_MS (a few frames) without a timer of its own.
  const programmatic = useRef(0)
  const ours = () => markOurs(programmatic)
  // Busy output scrolls to the end on almost every event, so the reader's first upward scroll event
  // often lands inside that window and reads as ours. What they do with the wheel, a finger or the
  // keyboard says they are going up before any scroll event does: upAt is when they last did.
  const upAt = useRef(0)
  // A finger is on the timeline (until it lifts, or TOUCH_LOST_MS pass with no move from it), or
  // lifted less than SETTLE_MS ago: the view is theirs, and
  // following does not move it (missed: it would have, and does once the touch has settled).
  const touching = useRef(false)
  const missed = useRef(false)
  const pin = p.pin
  // New output arrived below the view while not following: the "New output" pill is up. It is
  // raised for one session and send (pin); another session or a send takes it down.
  const pillKey = `${sessionId}\n${pin}`
  const [unseenAt, setUnseenAt] = useState<string | null>(null)
  const unseen = unseenAt === pillKey
  useLayoutEffect(() => {
    if (pin > 0) stick.current = true
  }, [pin])
  // Another session opens at its end.
  useLayoutEffect(() => {
    stick.current = true
  }, [sessionId])

  const toEnd = useCallback(() => {
    markOurs(programmatic)
    bottomRef.current?.scrollIntoView?.({ block: 'end' })
    const root = timelineRef.current
    if (root) {
      // The marker ends before the timeline's bottom padding. Include that space too.
      root.scrollTop = Math.max(0, root.scrollHeight - root.clientHeight)
      lastTop.current = root.scrollTop
    }
  }, [])

  // The transcript arrives in pages. While it is still loading, do not paint or scroll it page by
  // page (that is a visible storm of jumps); show it once, already at the bottom.
  useLayoutEffect(() => {
    if (loadingHistory) return
    if (!stick.current) return
    if (touching.current) missed.current = true
    else toEnd()
    // queued.length: a message sent mid-turn shows as a queued bubble before any event arrives,
    // and the send just pinned the view to the bottom for it. start: rows revealed above a
    // transcript shorter than the view keep it at its end.
  }, [sessionId, events.length, p.queued.length, streaming, p.working, p.waiting, loadingHistory, pin, start, toEnd])

  // ── keeping the reader's place ──
  // The first rows in view when the reader last scrolled (or the view was last put right), by their
  // content offsets. Whatever changes above them (rows revealed, an older page put in front, the
  // loading line coming and going, an image, a diagram or a code block sizing late), the first of
  // them still drawn goes back to where it was on screen. Measured, not predicted, and done by us
  // alone (the browser's own scroll anchoring is off for the timeline, so the two never both move it).
  const anchors = useRef<Array<{ el: Element; at: number }>>([])
  // Where the view was when they were taken (a view moved since, whose scroll event is still to come,
  // may have other rows in it).
  const anchoredAt = useRef(0)
  const capture = useCallback(() => {
    const root = timelineRef.current
    anchors.current = root ? anchorsIn(root, bottomRef.current) : []
    anchoredAt.current = root?.scrollTop ?? 0
  }, [])
  // force: also while following (a finger is down, and following waits for it to lift).
  const restore = useCallback((force = false) => {
    const root = timelineRef.current
    if (!root || (stick.current && !force)) return
    // The view jumped more than a screen since the rows were taken (Home, a scrollbar drag) and its
    // scroll event is still to come: they were not in view, and say nothing of what the reader sees.
    if (Math.abs(root.scrollTop - anchoredAt.current) > root.clientHeight) return
    const a = anchors.current.find(x => x.el.isConnected && x.el.parentElement === root)
    if (!a) return
    // How far the row moved within the content: what changed above it, never the reader's own scroll.
    const moved = viewTop(root, a.el) + root.scrollTop - a.at
    if (Math.abs(moved) < 0.5) return
    markOurs(programmatic)
    root.scrollTop += moved
    lastTop.current = root.scrollTop
    anchoredAt.current = root.scrollTop
  }, [])
  // Puts the reader's place right, then takes it again from what is now in view.
  const keepPlace = useCallback(() => {
    restore()
    capture()
  }, [restore, capture])

  // Sizes change after a row is drawn (an image, a diagram or a highlighted code block lays out late;
  // opening the panel or laying out the composer resizes the viewport): following, the end stays in
  // view; scrolled up, the reader's place does. The observer watches the timeline and each of its
  // rows (kept in step after every render below), and runs after layout, before the frame is painted.
  // The rows are synced only when the timeline's children changed (a MutationObserver's records,
  // taken synchronously after each render), not on every streaming delta.
  type Observed = { ro: ResizeObserver; mo: MutationObserver | null; els: Set<Element>; dirty: boolean }
  const observed = useRef<Observed | null>(null)
  useLayoutEffect(() => {
    const root = timelineRef.current
    if (!root || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(() => {
      // Still loading: the timeline is hidden and has no layout to keep.
      if (root.classList.contains('loading')) return
      if (stick.current) {
        if (touching.current) {
          // Following waits for the finger to lift; meanwhile the text under it stays put.
          missed.current = true
          restore(true)
          capture()
        } else {
          toEnd()
        }
      } else {
        keepPlace()
      }
    })
    ro.observe(root)
    const state: Observed = { ro, mo: null, els: new Set(), dirty: true }
    if (typeof MutationObserver !== 'undefined') {
      state.mo = new MutationObserver(() => { state.dirty = true })
      state.mo.observe(root, { childList: true })
    }
    observed.current = state
    return () => {
      ro.disconnect()
      state.mo?.disconnect()
      if (observed.current === state) observed.current = null
    }
  }, [toEnd, keepPlace, capture, restore])

  // New output below while the reader is not following puts up the pill: a new last row drawn, or
  // the streaming reply growing. Events that draw no row of their own (hidden kinds, a subagent's
  // events folded into its card, a turn end folded into its reply) do not, nor does an older page
  // put in front (it changes the first row, not the last).
  const tailId = useMemo(() => {
    if (compact) {
      for (let i = timelineItems.length - 1; i >= 0; i--) {
        const id = rowIndex.firstIds[i]
        if (timelineItems[i].type === 'event' && footerPairs.absorbed.has(id)) continue
        return id
      }
      return ''
    }
    for (let i = fullRows.length - 1; i >= 0; i--) {
      const ev = fullRows[i]
      if (isVisibleEvent(ev) || fullSubs.anchors.has(ev.client_event_id)) return ev.client_event_id
    }
    return ''
  }, [compact, timelineItems, rowIndex, footerPairs, fullRows, fullSubs])
  const seenTail = useRef<string | null>(null)
  useLayoutEffect(() => {
    if (loadingHistory) return
    const first = seenTail.current === null
    const grew = !first && (tailId !== seenTail.current || streaming !== '')
    seenTail.current = tailId
    if (grew && !stick.current) setUnseenAt(pillKey)
  }, [tailId, streaming, loadingHistory, pillKey])

  // A long live visit: the window is pinned by its first row, so every live row is drawn and the
  // window grows. While the reader follows the bottom and more than twice WINDOW rows are drawn,
  // it goes back to the newest WINDOW (nothing they can see moves; the view stays at the end).
  const firstIds = rowIndex.firstIds
  useLayoutEffect(() => {
    if (loadingHistory || !stick.current || rowCount - start <= 2 * WINDOW) return
    ours()
    setWin({ session: winKey, anchor: firstIds[rowCount - WINDOW], top: false })
  }, [loadingHistory, rowCount, start, firstIds, winKey])

  // A page that lands while the view is still gliding up (a flick's momentum, a trackpad's momentum
  // stream) would carry the reader on through the rows that just came in, and into the next page:
  // the glide stops where the page landed, and they scroll again from there. A touch glide is ended
  // by taking the timeline's scrolling away for a frame or two (what stops it on every browser; the
  // gutter is kept so nothing reflows); a trackpad's stream of small wheel steps is held back until
  // it pauses, for at most WHEEL_HOLD_MS. Keys and a mouse wheel's notches have no momentum to stop
  // (and a smooth scroll is already ended by the scroll that keeps the reader's place).
  const lastFineWheel = useRef(-Infinity)
  const lastTouch = useRef(-Infinity)
  const wheelHold = useRef<(() => void) | null>(null)
  function stopGlide(root: HTMLElement) {
    const now = inputClock.now()
    if (root.scrollHeight <= root.clientHeight) return
    if (touching.current || now - lastTouch.current < TOUCH_GLIDE_MS) {
      const s = root.style
      s.scrollbarGutter = 'stable'
      s.overflowY = 'hidden'
      const win = root.ownerDocument.defaultView
      const back = () => {
        s.overflowY = ''
        s.scrollbarGutter = ''
      }
      if (win) win.requestAnimationFrame(() => win.requestAnimationFrame(back))
      else back()
    }
    // Wheel events held up behind drawing the page arrive just after it: a stream still going is
    // one whose next event comes within WHEEL_STREAM_MS of the page landing, and of each other.
    if (now - lastFineWheel.current < GLIDE_MS && !wheelHold.current) {
      let last = now
      const end = () => {
        clearTimeout(cap)
        root.removeEventListener('wheel', hold)
        if (wheelHold.current === end) wheelHold.current = null
      }
      const hold = (e: WheelEvent) => {
        const t = inputClock.now()
        if (t - now < WHEEL_HOLD_MS && t - last < WHEEL_STREAM_MS && e.deltaY < 0) {
          last = t
          e.preventDefault()
          return
        }
        end()
      }
      // The cap itself is the elapsed time checked above; this only takes the listener away later.
      const cap = setTimeout(end, 4 * WHEEL_HOLD_MS)
      wheelHold.current = end
      root.addEventListener('wheel', hold, { passive: false })
    }
  }

  // Whether the sentinel is near, when the last request went out, when the last page landed, when
  // the reader last scrolled up, and a request waiting out ASK_GAP_MS.
  const askNear = useRef(false)
  const askLast = useRef(-Infinity)
  const landAt = useRef(-Infinity)
  const intentAt = useRef(-Infinity)
  const askRetry = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const askAgain = useRef(() => {})
  // When the last page finished going in (input clock).
  const landEnd = useRef(-Infinity)
  // When the last wheel event came (input clock), so a scroll it caused is left to the wheel's rule.
  const lastWheel = useRef(-Infinity)
  // Just before a page or revealed rows go in: when the view moved since the reader's place was taken
  // (a scrollbar jump or Home whose scroll event is still to come), take it again from the view as it
  // is now, so the place kept is the one they jumped to.
  const beforeInsert = useCallback(() => {
    const root = timelineRef.current
    if (!root || stick.current || root.classList.contains('loading')) return
    if (Math.abs(root.scrollTop - anchoredAt.current) > 1) capture()
  }, [capture])

  // After every render: rows revealed above the reader, an older page put in front,
  // the ready card or the beginning line coming and going leave the reader's place where it was, and
  // the rows drawn now are the ones watched for late sizes.
  const oldest = events[0]?.client_event_id ?? ''
  const seenOldest = useRef({ key: winKey, id: oldest })
  useLayoutEffect(() => {
    const root = timelineRef.current
    if (!root || loadingHistory) {
      anchors.current = []
      seenOldest.current = { key: winKey, id: oldest }
      return
    }
    const obs = observed.current
    if (obs && (obs.dirty || !obs.mo || obs.mo.takeRecords().length > 0)) {
      obs.dirty = false
      const { ro, els } = obs
      for (const el of els) {
        if (el.parentElement !== root) {
          ro.unobserve(el)
          els.delete(el)
        }
      }
      for (const el of root.children) {
        if (!els.has(el)) {
          ro.observe(el)
          els.add(el)
        }
      }
    }
    // An older page just went in front while the reader is up in the transcript.
    const landed = seenOldest.current.key === winKey && seenOldest.current.id !== oldest && seenOldest.current.id !== ''
    seenOldest.current = { key: winKey, id: oldest }
    if (!stick.current || touching.current) keepPlace()
    if (landed) {
      // The next page waits for the reader to scroll up again only when this one moved the top out
      // of reach. A page that added nothing above the view (events that draw no row, or fold into
      // the top row) or too little leaves them still at the top: the next is asked for anyway.
      // (The next request itself comes from the sentinel's observer, made again for the new count.)
      const s = sentinelRef.current
      const stillNear = !!s && -viewTop(root, s) <= AHEAD_VIEWS * root.clientHeight
      if (!stillNear) landAt.current = inputClock.now()
      // A wheel gesture is new only after a pause from here too: drawing the page can hold the
      // wheel events of a momentum stream up for longer than the pause.
      landEnd.current = inputClock.now()
    }
    if (landed && !stick.current) stopGlide(root)
  })

  // The sentinel above the first drawn row: within AHEAD of the view, it reveals WINDOW more rows,
  // or, with every held row drawn, asks for the next older page. A new observer after each change
  // reports at once whether the sentinel is still near, so a short screen keeps filling up.
  // Asking is on the reader's intent: after a page lands, the next one waits for them to scroll up
  // again (sitting at the top asks nothing more), at most one request per ASK_GAP_MS. An older page
  // is asked for once per events held while the sentinel stays near: a fetch that brought nothing
  // (the link dropped) is not asked for again in a loop. The sentinel leaving re-arms it, so
  // scrolling down and back up tries once more (and a reconnect fetches it anyway).
  const askedAt = useRef(-1)
  const heldCount = events.length
  const askWith = useRef({ canFetch, heldCount, onLoadOlder: p.onLoadOlder })
  useLayoutEffect(() => {
    askWith.current = { canFetch, heldCount, onLoadOlder: p.onLoadOlder }
  })
  const maybeAsk = useCallback(() => {
    const w = askWith.current
    clearTimeout(askRetry.current)
    if (!askNear.current || !w.canFetch || askedAt.current === w.heldCount) return
    const root = timelineRef.current
    // A transcript shorter than its view cannot be scrolled: it fills up without being asked, and
    // without waiting between pages.
    const scrollable = !!root && root.scrollHeight > root.clientHeight
    if (scrollable && landAt.current > -Infinity && !(intentAt.current > landAt.current)) return
    const now = performance.now()
    if (scrollable && now - askLast.current < ASK_GAP_MS) {
      askRetry.current = setTimeout(() => askAgain.current(), askLast.current + ASK_GAP_MS - now)
      return
    }
    askedAt.current = w.heldCount
    askLast.current = now
    w.onLoadOlder?.()
  }, [])
  useEffect(() => {
    askAgain.current = maybeAsk
    return () => clearTimeout(askRetry.current)
  }, [maybeAsk])
  // The reader scrolled up: the next page may be asked for. A key or a finger counts at once; a
  // wheel only when its gesture began after the page landed (a momentum tail does not); a scroll the
  // input listeners did not see (the scrollbar) once the glide a landing stopped has had its window.
  const settleTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const intent = useCallback((kind: 'input' | 'scroll' | { wheelGestureAt: number }) => {
    const now = inputClock.now()
    if (typeof kind === 'object' && kind.wheelGestureAt <= landAt.current) return
    if (kind === 'scroll' && now - landAt.current < LANDED_SETTLE_MS) {
      clearTimeout(settleTimer.current)
      settleTimer.current = setTimeout(() => {
        intentAt.current = inputClock.now()
        maybeAsk()
      }, landAt.current + LANDED_SETTLE_MS - now)
      return
    }
    intentAt.current = now
    maybeAsk()
  }, [maybeAsk])
  // Another session (or thinking shown or hidden) starts its timing afresh.
  useLayoutEffect(() => {
    askedAt.current = -1
    askLast.current = -Infinity
    landAt.current = -Infinity
    intentAt.current = -Infinity
    clearTimeout(askRetry.current)
    clearTimeout(settleTimer.current)
  }, [winKey])
  useEffect(() => () => clearTimeout(settleTimer.current), [])
  // The loading pill: only near the top, and only for a page slower than SLOW_LOAD_MS.
  const [nearFor, setNearFor] = useState({ key: winKey, near: false })
  const near = nearFor.key === winKey && nearFor.near
  const [slow, setSlow] = useState(false)
  useEffect(() => {
    if (!p.loadingOlder) return
    const t = setTimeout(() => setSlow(true), SLOW_LOAD_MS)
    return () => {
      clearTimeout(t)
      setSlow(false)
    }
  }, [p.loadingOlder])
  const showPill = p.loadingOlder && slow && near && start === 0
  // Said to a screen reader (politely, from a region that is always there): the slow page coming,
  // and an earlier page put in while the reader is up at the top.
  // Another session starts with nothing said. "Loaded" waits a moment, so a run of pages (each slow,
  // each drawing little) is one "Loading…" and then one "Loaded", not the two in turn for each.
  const [said, setSaid] = useState({ key: winKey, oldest: '', text: '' })
  const oldestNow = events[0]?.client_event_id ?? ''
  if (said.key !== winKey) {
    setSaid({ key: winKey, oldest: oldestNow, text: '' })
  } else if (said.oldest !== oldestNow) {
    setSaid({ key: winKey, oldest: oldestNow, text: said.oldest !== '' && near && !loadingHistory ? 'Loaded earlier messages' : said.text })
  }
  // null: a page is on its way but not slow yet (whatever was going to be said waits for it).
  const sayNow = showPill ? 'Loading earlier messages…' : p.loadingOlder ? null : said.key === winKey ? said.text : ''
  const [announce, setAnnounce] = useState('')
  useEffect(() => {
    if (sayNow === null) return
    const t = setTimeout(() => setAnnounce(sayNow), sayNow === '' || showPill ? 0 : LOADED_SAY_MS)
    return () => clearTimeout(t)
  }, [sayNow, showPill])
  useEffect(() => {
    const target = sentinelRef.current
    const root = timelineRef.current
    if (!more || !target || !root || typeof IntersectionObserver === 'undefined') return
    if (revealTo === null && !atHeldTop) return
    let acted = false
    const io = new IntersectionObserver(entries => {
      const isNear = entries.some(e => e.isIntersecting)
      askNear.current = isNear
      setNearFor({ key: winKey, near: isNear })
      if (!isNear) {
        askedAt.current = -1
        return
      }
      if (acted) return
      if (revealTo !== null) {
        acted = true
        // The view may have jumped here (a scrollbar drag) with its scroll event still to come: take
        // the reader's place from what is in view now, before the rows go in above it.
        if (!stick.current) capture()
        setWin({ session: winKey, anchor: revealTo, top: false })
        return
      }
      // The reader is at the top of every held row: the page that lands next is drawn at once.
      setWin(w => (w.top || w.session !== winKey ? w : { ...w, top: true }))
      maybeAsk()
    }, { root, rootMargin: `${AHEAD} 0px 0px 0px` })
    io.observe(target)
    return () => {
      io.disconnect()
      askNear.current = false
    }
  }, [more, revealTo, atHeldTop, canFetch, maybeAsk, capture, winKey, heldCount])

  // The reader's input, listened to once on the timeline (passive: it never delays their scroll).
  // A touch ends on the window: the node it started on may have left the timeline by then.
  useEffect(() => {
    const root = timelineRef.current
    const win = root?.ownerDocument.defaultView
    if (!root || !win) return
    // When the wheel gesture now going on began (the first event after a pause).
    let gestureAt = -Infinity
    const up = (target: EventTarget | null, wheel?: WheelEvent) => {
      if (innerCanGoUp(target, root)) return
      stick.current = false
      upAt.current = performance.now()
      // A wheel event held back (a momentum stream after a page landed) scrolled nothing: it is
      // known only once every listener has seen it.
      if (wheel) {
        const g = gestureAt
        setTimeout(() => { if (!wheel.defaultPrevented) intent({ wheelGestureAt: g }) }, 0)
      } else {
        intent('input')
      }
    }
    // Per finger: where its current run down the screen began (reset when it goes back up the
    // screen, so down, up and down again still counts) and where it was last.
    const runs = new Map<number, { x: number; y: number; lastY: number }>()
    const fingers = (l: TouchList | undefined) => (l ? Array.from(l) : [])
    let settle: ReturnType<typeof setTimeout> | undefined
    let lost: ReturnType<typeof setTimeout> | undefined
    const release = () => {
      clearTimeout(settle)
      clearTimeout(lost)
      touching.current = false
      if (missed.current && stick.current) toEnd()
      missed.current = false
    }
    const stillDown = () => {
      clearTimeout(lost)
      lost = setTimeout(() => {
        runs.clear()
        release()
      }, TOUCH_LOST_MS)
    }
    const onWheel = (e: WheelEvent) => {
      const t = inputClock.now()
      if (t - Math.max(lastWheel.current, landEnd.current) > WHEEL_GESTURE_GAP_MS) gestureAt = t
      lastWheel.current = t
      if (e.deltaMode === 0 && Math.abs(e.deltaY) < FINE_WHEEL_PX) lastFineWheel.current = inputClock.now()
      if (e.deltaY < 0) up(e.target, e)
    }
    const onTouchStart = (e: TouchEvent) => {
      touching.current = true
      // While following, the rows under the finger hold their place until it lifts.
      if (stick.current) capture()
      clearTimeout(settle)
      stillDown()
      for (const t of fingers(e.changedTouches)) runs.set(t.identifier, { x: t.clientX, y: t.clientY, lastY: t.clientY })
    }
    const onTouchMove = (e: TouchEvent) => {
      stillDown()
      for (const t of fingers(e.changedTouches)) {
        const run = runs.get(t.identifier)
        if (!run) continue
        if (t.clientY < run.lastY) {
          run.x = t.clientX
          run.y = t.clientY
        }
        run.lastY = t.clientY
        const dy = t.clientY - run.y
        if (dy > TOUCH_UP_PX && dy > Math.abs(t.clientX - run.x)) up(e.target)
      }
    }
    const onTouchEnd = (e: TouchEvent) => {
      if (!touching.current) return
      for (const t of fingers(e.changedTouches)) runs.delete(t.identifier)
      if (e.touches && e.touches.length > 0) return
      lastTouch.current = inputClock.now()
      runs.clear()
      clearTimeout(lost)
      clearTimeout(settle)
      settle = setTimeout(release, SETTLE_MS)
    }
    const onKey = (e: KeyboardEvent) => {
      if (typing(e.target)) return
      if (UP_KEYS.has(e.key) || (e.key === ' ' && e.shiftKey)) up(e.target)
    }
    const passive = { passive: true }
    root.addEventListener('wheel', onWheel, passive)
    root.addEventListener('touchstart', onTouchStart, passive)
    root.addEventListener('touchmove', onTouchMove, passive)
    win.addEventListener('touchend', onTouchEnd, passive)
    win.addEventListener('touchcancel', onTouchEnd, passive)
    root.addEventListener('keydown', onKey)
    return () => {
      clearTimeout(settle)
      clearTimeout(lost)
      touching.current = false
      root.removeEventListener('wheel', onWheel)
      root.removeEventListener('touchstart', onTouchStart)
      root.removeEventListener('touchmove', onTouchMove)
      win.removeEventListener('touchend', onTouchEnd)
      win.removeEventListener('touchcancel', onTouchEnd)
      root.removeEventListener('keydown', onKey)
      wheelHold.current?.()
    }
  }, [toEnd, capture, intent])

  // Scroll events: the end resumes following; an upward scroll with no input seen (the scrollbar
  // dragged, say) stops it, unless it is ours.
  function onScroll(e: React.UIEvent<HTMLDivElement>) {
    const el = e.currentTarget
    const top = el.scrollTop
    // Going up just after the reader asked to (or with a finger down) is not a return to the end,
    // even when the first step of it still lands within AT_END_PX of it.
    const leaving = top < lastTop.current
      && (touching.current || (upAt.current !== 0 && performance.now() - upAt.current < SETTLE_MS))
    if (!leaving && el.scrollHeight - top - el.clientHeight <= AT_END_PX) {
      // At the very end (also where the browser puts the view when the content under it shrinks).
      stick.current = true
      setUnseenAt(null)
    } else if (top < lastTop.current && !isOurs(programmatic)) {
      stick.current = false
      // Up by the scrollbar (or any way the input listeners do not see).
      // (A scroll a wheel made is the wheel's to judge: its gesture rule leaves momentum out.)
      if (inputClock.now() - lastWheel.current > WHEEL_GESTURE_GAP_MS) intent('scroll')
    }
    programmatic.current = 0
    lastTop.current = top
    // The view moved: the rows now first in view hold the reader's place from here on. A size that
    // changed since the last frame (a late image above) is made up for first: the reader's scroll
    // does not move the rows within the content, so this only undoes what the layout did.
    keepPlace()
  }

  function showNew() {
    stick.current = true
    setUnseenAt(null)
    toEnd()
  }

  const q = p.queued
  // A first start has nothing above its progress panel. A RESUME (a new pod for a session that
  // already has a conversation) does: its panel goes after the conversation, where the person
  // just sent the message that started it, not above everything they would have to scroll past.
  const resuming = p.starting && !!p.attempt && events.some(isVisibleEvent)
  const startPanel = p.starting && p.attempt
    ? <StartProgress attempt={p.attempt} session={p.startSession} onStop={p.onStopSession} clockOffset={p.clockOffset} resuming={resuming} />
    : null
  return (
    <ReviewRoundsContext.Provider value={rounds}>
      {p.showSkeleton && <HistorySkeleton />}
      <BeforeDomUpdate when={`${oldest}\n${start}`} on={beforeInsert} />
      <span className="chat-sr-only" aria-live="polite" aria-atomic="true" data-testid="older-status">{announce}</span>
      <div ref={timelineRef} className={`agent-timeline${p.showSkeleton ? ' loading' : ''}${p.loadingOlder ? ' backfilling' : ''}`} onScroll={onScroll}>
        {/* Over the top of the view, taking no room: rows never move for it. */}
        {showPill && (
          <div className="older-loading-dock" data-no-anchor="">
            <div className="older-loading" data-testid="older-loading" aria-hidden="true">
              <span className="queue-spinner" aria-hidden="true" /> Loading earlier messages…
            </div>
          </div>
        )}
        {atBeginning && (
          <div className="history-start" data-testid="history-start">Beginning of the session</div>
        )}
        {!resuming && startPanel}
        {/* The ready card heads the transcript: only once its start is drawn. */}
        {!p.starting && !hasOlder && start === 0 && p.readyCard}
        {/* Rows revealed or put in front go in below the sentinel, so it never holds the reader's place. */}
        {more && <div ref={sentinelRef} className="transcript-more" aria-hidden="true" data-testid="transcript-more" data-no-anchor="" />}
        {compact
          ? timelineItems.slice(start).map(item => {
              if (item.type === 'tools') {
                const k = `tools:${item.key}`
                return (
                  <Fragment key={k}>
                    {dayBreaks.has(k) && <DayDivider ts={dayBreaks.get(k)!} />}
                    <ToolGroup entries={item.entries} live={liveIds} />
                  </Fragment>
                )
              }
              if (item.type === 'subagent') {
                const k = `sub:${item.key}`
                return (
                  <Fragment key={k}>
                    {dayBreaks.has(k) && <DayDivider ts={dayBreaks.get(k)!} />}
                    <SubagentCard sub={item.sub} live={liveIds} options={options} />
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
          : fullRows.slice(start).map(ev => {
              const footer = footerPairs.footers.get(ev.client_event_id)
              const sub = fullSubs.anchors.get(ev.client_event_id)
              return (
                <Fragment key={ev.client_event_id}>
                  {dayBreaks.has(ev.client_event_id) && <DayDivider ts={dayBreaks.get(ev.client_event_id)!} />}
                  {sub
                    ? <SubagentCard sub={sub} live={liveIds} options={options} />
                    : renderEvent(ev, resultsByCall, turnStarts.get(ev.client_event_id), footer,
                      footer ? turnStarts.get(footer.client_event_id) : undefined, options)}
                </Fragment>
              )
            })}
        {streaming && (
          <div className="agent-card assistant streaming" data-testid="streaming">
            <Markdown text={streaming} cards={cards} workspaceLinks />
          </div>
        )}
        {resuming && startPanel}
        {q.map((m, i) => (
          <div key={m.id} className={`agent-card user queued ${m.stale ? 'failed stale' : p.queueState}`} data-testid="queued-message">
            <span className="card-author">You</span>
            <MessageText text={m.text} cards={cards} />
            <div className="queue-status" role="status">
              {p.queueState === 'failed'
                ? <><span aria-hidden="true">⚠</span> Not delivered</>
                : m.stale
                ? <><span aria-hidden="true">⚠</span> May not have been delivered — send it again if the agent does not answer</>
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
        {p.working && !streaming && (
          <TurnClock since={p.since} clockOffset={p.clockOffset} waitingOn={p.waitingOn} background={p.background} activity={p.activity} reasoning={p.reasoning} />
        )}
        {!p.working && !p.waiting && p.background && (
          <TurnClock since={p.background.since} clockOffset={p.clockOffset} waitingOn={null} background={p.background} />
        )}
        {p.waiting && (
          <div className="agent-waiting" role="status" aria-live="polite" data-testid="waiting">
            <span className="agent-waiting-dot" aria-hidden="true" />
            Waiting for your answer
          </div>
        )}
        <div ref={bottomRef} />
        {lp.sheet && <LongPressSheet target={lp.sheet} onClose={lp.close} />}
        {unseen && (
          <div className="chat-new-output-dock">
            <button type="button" className="chat-new-output" aria-live="off" data-testid="new-output" onClick={showNew}>
              <span aria-hidden="true">↓</span> New output
            </button>
          </div>
        )}
      </div>
    </ReviewRoundsContext.Provider>
  )
})

export default Transcript
