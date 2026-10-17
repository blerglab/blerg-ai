// The timeline: start progress, the ready card, every event (folded into tool groups or as full
// cards), the streaming reply, sent-but-unrecorded messages, the working and waiting lines. It
// draws the newest WINDOW rows and reveals more as the reader scrolls up, follows new output until
// the reader scrolls up (and again once they are back at the very end), and holds their place while
// rows are put in above them.
import { Fragment, memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { CardRegistry } from '../cards/registry'
import { parseTs, dayKey } from '../model/timeLabel'
import { HIDDEN_KINDS, isVisibleEvent, parentCallOf, sharedTimeline, timelineBuilder, type SubagentEntry } from '../model/toolGroups'
import type { StartAttempt } from '../model/startStages'
import type { WaitingOn } from '../model/liveCalls'
import { firstPersonMessage } from '../model/userMessages'
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

/** How many rows (timeline items) the transcript draws at first, and how many more each time the
 *  reader reaches the top of what is drawn. The timeline, day breaks, turn footers and turn times
 *  are still worked out over every event held (cheap); only the drawing is windowed, because
 *  drawing thousands of rows at once is what makes a long session slow to open. */
export const WINDOW = 300

// How far above the view the top of the drawn rows starts revealing more (or fetching an older
// page), so a reader scrolling up rarely reaches the edge.
const REVEAL_MARGIN_PX = 800

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

// The working line with its clock. It ticks on its own, so the second-by-second clock re-renders
// this line and not the timeline. since: server time the turn began (null: unknown, shows 0 s).
function TurnClock({ since, clockOffset, waitingOn, background, activity }: {
  since: number | null
  clockOffset: number
  waitingOn: WaitingOn | null
  background?: BackgroundLine | null
  activity?: string | null
}) {
  const now = useTicker(true) - clockOffset
  return <WorkingIndicator since={since ?? now} now={now} waitingOn={waitingOn} background={background} activity={activity} />
}

/** The background tasks running, for the working line: how many, the description of the only one,
 *  and when the oldest started (server time; null when unknown). */
export interface BackgroundLine {
  count: number
  description: string
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
  startSession?: { id: string; status: string }
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
  const fullSeq = useMemo(
    () => (compact ? [] : events.filter(ev => !HIDDEN_KINDS.has(ev.kind) && !fullSubs.folded.has(ev.client_event_id))),
    [events, compact, fullSubs],
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
  const fullRows = useMemo(
    () => (compact ? [] : fullSeq.filter(ev => !footerPairs.absorbed.has(ev.client_event_id))),
    [fullSeq, compact, footerPairs],
  )

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
  const [win, setWin] = useState<{ session: string; anchor: string | null }>({ session: winKey, anchor: null })
  const anchor = win.session === winKey && !loadingHistory ? win.anchor : null
  const pinned = anchor !== null ? rowIndex.at.get(anchor) : undefined
  const start = pinned ?? Math.max(0, rowCount - WINDOW)
  const wanted = !loadingHistory && rowCount > 0 ? rowIndex.firstIds[start] : null
  if (win.session !== winKey || win.anchor !== wanted) setWin({ session: winKey, anchor: wanted })
  // The row WINDOW above the first drawn, revealed when the reader reaches the top.
  const revealTo = start > 0 ? rowIndex.firstIds[Math.max(0, start - WINDOW)] : null
  const hasOlder = !!p.hasOlder
  // Something is above the drawn rows: held rows not drawn yet, or older events on the server.
  const more = !loadingHistory && rowCount > 0 && (start > 0 || hasOlder)
  const canFetch = start === 0 && hasOlder && !p.loadingOlder

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

  // Opening the panel or laying out the composer can resize the viewport after its first
  // scroll. Keep the end in view through that layout, unless the reader has scrolled up.
  useLayoutEffect(() => {
    const root = timelineRef.current
    if (!root || loadingHistory || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => {
      if (!stick.current) return
      if (touching.current) missed.current = true
      else toEnd()
    })
    observer.observe(root)
    return () => observer.disconnect()
  }, [loadingHistory, toEnd])

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
    setWin({ session: winKey, anchor: firstIds[rowCount - WINDOW] })
  }, [loadingHistory, rowCount, start, firstIds, winKey])

  // Rows revealed above the reader, an older page put in front, the loading line or the ready card
  // coming and going: when they are not at the bottom, the first row drawn before stays where it was
  // on screen, so the text under their eyes does not jump. Where it was is worked out from its
  // place in the content and the reader's last scroll position (kept up to date by onScroll), and
  // compared with where it is now: a browser that already anchored the scroll itself leaves
  // nothing to move.
  const held = useRef<{ el: Element | null; top: number; scroll: number }>({ el: null, top: 0, scroll: 0 })
  useLayoutEffect(() => {
    const root = timelineRef.current
    if (!root || loadingHistory) {
      held.current = { el: null, top: 0, scroll: 0 }
      return
    }
    const prev = held.current
    if (!stick.current && prev.el?.isConnected) {
      const moved = viewTop(root, prev.el) - (prev.top - prev.scroll)
      if (moved !== 0) {
        ours()
        root.scrollTop += moved
      }
    }
    const first = sentinelRef.current?.nextElementSibling ?? null
    const scroll = root.scrollTop
    held.current = { el: first, top: first ? viewTop(root, first) + scroll : 0, scroll }
  }, [loadingHistory, events, start, more, p.loadingOlder, hasOlder, p.starting, p.readyCard])

  // The sentinel above the first drawn row: in view (or nearly), it reveals WINDOW more rows, or,
  // with every held row drawn, asks for the next older page. A new observer after each change
  // reports at once whether the sentinel is still in view, so a short screen keeps filling up.
  // An older page is asked for once per events held while the sentinel stays in view: a fetch that
  // brought nothing (the link dropped) is not asked for again in a loop. The sentinel leaving view
  // re-arms it, so scrolling down and back up tries once more (and a reconnect fetches it anyway).
  const askedAt = useRef(-1)
  const onLoadOlder = p.onLoadOlder
  const heldCount = events.length
  useEffect(() => {
    const target = sentinelRef.current
    const root = timelineRef.current
    if (!more || !target || !root || typeof IntersectionObserver === 'undefined') return
    if (revealTo === null && !canFetch) return
    let acted = false
    const io = new IntersectionObserver(entries => {
      if (!entries.some(e => e.isIntersecting)) {
        askedAt.current = -1
        return
      }
      if (acted) return
      if (revealTo !== null) {
        acted = true
        setWin({ session: winKey, anchor: revealTo })
      } else if (askedAt.current !== heldCount) {
        acted = true
        askedAt.current = heldCount
        onLoadOlder?.()
      }
    }, { root, rootMargin: `${REVEAL_MARGIN_PX}px 0px 0px 0px` })
    io.observe(target)
    return () => io.disconnect()
  }, [more, revealTo, canFetch, onLoadOlder, winKey, heldCount])

  // The reader's input, listened to once on the timeline (passive: it never delays their scroll).
  // A touch ends on the window: the node it started on may have left the timeline by then.
  useEffect(() => {
    const root = timelineRef.current
    const win = root?.ownerDocument.defaultView
    if (!root || !win) return
    const up = (target: EventTarget | null) => {
      if (innerCanGoUp(target, root)) return
      stick.current = false
      upAt.current = performance.now()
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
    const onWheel = (e: WheelEvent) => { if (e.deltaY < 0) up(e.target) }
    const onTouchStart = (e: TouchEvent) => {
      touching.current = true
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
    }
  }, [toEnd])

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
    }
    programmatic.current = 0
    lastTop.current = top
    held.current.scroll = top
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
    <>
      {p.showSkeleton && <HistorySkeleton />}
      <div ref={timelineRef} className={`agent-timeline${p.showSkeleton ? ' loading' : ''}${p.loadingOlder ? ' backfilling' : ''}`} onScroll={onScroll}>
        {p.loadingOlder && start === 0 && (
          <div className="older-loading" data-testid="older-loading" role="status">
            <span className="queue-spinner" aria-hidden="true" /> Loading earlier messages…
          </div>
        )}
        {!resuming && startPanel}
        {/* The ready card heads the transcript: only once its start is drawn. */}
        {!p.starting && !hasOlder && start === 0 && p.readyCard}
        {more && <div ref={sentinelRef} className="transcript-more" aria-hidden="true" data-testid="transcript-more" />}
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
          <TurnClock since={p.since} clockOffset={p.clockOffset} waitingOn={p.waitingOn} background={p.background} activity={p.activity} />
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
    </>
  )
})

export default Transcript
