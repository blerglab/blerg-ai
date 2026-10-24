// Hovercards: a small preview of what a chip names, the way GitHub previews an issue or a commit, on
// every chip of the chat and the runner's session panel (a card, a file, a commit). One component,
// one place to tune it (HOVERCARD: the delays, the width and which fields show) and one switch to
// turn it off (setHovercardsEnabled, kept per browser; the runner's Settings has the checkbox).
//
// Behavior: it opens after HOVERCARD.openDelayMs of hover (or keyboard focus), stays open while the
// pointer moves into it, and closes on leave, on Esc, or when the chip is pressed. Only a device that
// can hover gets one ((hover: hover)); a tap on a touch screen keeps opening the thing as before.
// The card is drawn in a portal on the page's body, above the panel and any drawer, placed under
// (or over) its chip and kept on screen, so the panel's fixed width never clips it.
//
// Nothing is read for a chip that is never hovered: the data is read when the card first opens,
// and kept (the file previews' own bounded caches, the host's card reads, a small commit cache).
// The card holds no links and no controls: everything still opens from the chip itself, in Blerg.
import {
  cloneElement, createContext, isValidElement, useCallback, useContext, useEffect, useId, useLayoutEffect, useRef, useState,
  useSyncExternalStore, type CSSProperties, type FocusEvent, type PointerEvent as ReactPointerEvent, type ReactElement, type ReactNode,
} from 'react'
import { createPortal } from 'react-dom'
import { useChat } from './context'
import { formatSize, type ArtifactInfo } from '../model/artifacts'
import { INLINE_IMAGE_MAX, INLINE_TEXT_MAX, loadPreviewText } from '../model/artifactPreview'
import { mediaUrl, releaseMediaUrl } from '../model/artifactViews'
import { parsePatch } from '../model/patch'
import { visibleText } from '../model/visibleText'
import { visualFile } from '../model/visualKind'
import { localStorageChatStorage } from '../model/storage'
import { priorityGlyph, sourceLabel, statusTone } from '../model/worklist'
import type { CommitDetails, TransportFiles, WorkspaceGitRef } from '../transport/types'
import type { Ticket } from '../types'
import { VisualThumb } from './VisualBlock'
import './worklist.css'
import './hovercard.css'

export type CardField = 'status' | 'priority' | 'type' | 'tags' | 'assignee' | 'description'
export type FileField = 'preview' | 'version' | 'size' | 'publisher' | 'review'
export type CommitField = 'message' | 'author' | 'files'
export type RunField = 'kind' | 'engine' | 'model' | 'state' | 'step' | 'elapsed' | 'tokens' | 'card' | 'startedBy'

/** The one place to tune hovercards: how long a hover waits, how long the card lingers for the
 *  pointer to reach it, how wide it is, how much it shows, and which fields each kind draws (in
 *  this order; leave one out to drop it). */
export const HOVERCARD = {
  openDelayMs: 400,
  /** After the keyboard focus leaves a chip, how long its card stays. */
  closeDelayMs: 150,
  /** While a card is open, and this long after one closed, another chip's card opens at once (a
   *  warm group, as tooltips do): moving down a list switches cards with no delay. */
  warmMs: 300,
  /** How long a card waits for a pointer heading for it to move again (each move toward it starts
   *  the wait over); a move away closes it at once. headingMaxMs caps the whole trip. */
  graceMs: 500,
  headingMaxMs: 3000,
  /** A warm open still waits this long with the pointer on the chip, so a fast sweep across a
   *  list does not read and flash every card it crosses. */
  warmDwellMs: 70,
  /** The safe triangle's apex sits this far behind where the pointer left the chip (away from the
   *  card), so the first moves off the chip have room, whatever way the hand starts. */
  apexBehindPx: 8,
  /** Direction is judged over this window (the older of this many moves back and this many ms),
   *  never per sample: a 1 px staircase or a few vertical samples do not close the card. */
  windowMoves: 4,
  windowMs: 40,
  /** A move must point between the card's near corners, give or take angleDeg, and no steeper
   *  than steepDeg off straight at the card (straight down a list beside a tall card is 90°: not
   *  toward it). A move shorter than jitterPx over the window is not judged at all. */
  angleDeg: 15,
  steepDeg: 75,
  jitterPx: 4,
  /** The space between a card and what it opens beside. */
  gap: 8,
  width: 360,
  /** Lines of a markdown, text or code file. */
  excerptLines: 10,
  /** Lines of a card's description, and of a commit's message after its subject. */
  descriptionLines: 4,
  /** Files of a diff or a commit listed before "and N more". */
  files: 8,
  fields: {
    card: ['status', 'priority', 'type', 'tags', 'assignee', 'description'] as CardField[],
    file: ['preview', 'version', 'size', 'publisher', 'review'] as FileField[],
    commit: ['message', 'author', 'files'] as CommitField[],
    run: ['kind', 'engine', 'model', 'state', 'step', 'elapsed', 'tokens', 'card', 'startedBy'] as RunField[],
  },
}

// ── The switch ───────────────────────────────────────────────────────────────────────────────────
export const HOVERCARDS_KEY = 'blerg.chat.hovercards'
const switchListeners = new Set<() => void>()

/** Hovercards are on unless the person turned them off. */
export function hovercardsEnabled(): boolean {
  return localStorageChatStorage.get(HOVERCARDS_KEY) !== '0'
}

export function setHovercardsEnabled(on: boolean): void {
  localStorageChatStorage.set(HOVERCARDS_KEY, on ? null : '0')
  for (const l of switchListeners) l()
}

export function useHovercardsEnabled(): boolean {
  return useSyncExternalStore(cb => {
    switchListeners.add(cb)
    const onStorage = (e: StorageEvent) => { if (e.key === HOVERCARDS_KEY) cb() }
    window.addEventListener('storage', onStorage)
    return () => { switchListeners.delete(cb); window.removeEventListener('storage', onStorage) }
  }, hovercardsEnabled, () => true)
}

const HOVER_QUERY = '(hover: hover)'
function canHoverNow(): boolean {
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function' && !!window.matchMedia(HOVER_QUERY)?.matches
}

/** Whether this device can hover (a mouse or a trackpad); a touch screen cannot. */
export function useCanHover(): boolean {
  return useSyncExternalStore(cb => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return () => {}
    const mq = window.matchMedia(HOVER_QUERY)
    mq?.addEventListener?.('change', cb)
    return () => mq?.removeEventListener?.('change', cb)
  }, canHoverNow, () => false)
}

// ── What a chip names, and where its data comes from ────────────────────────────────────────────
export interface CardTarget {
  kind: 'card'
  source: string
  key: string
  boardId?: string
  /** What the chip already knows (the agent's copy of the pin): drawn at once, the host's read on top. */
  title?: string
  status?: string
  ticket?: Partial<Ticket>
  /** The worker on it (a subagent's or a started session's name), when the host knows. */
  worker?: string
}
export interface FileTarget {
  kind: 'file'
  sessionId: string
  id: string
  artifact?: ArtifactInfo
  /** A review of this file waits on the person. */
  reviewOpen?: boolean
}
export interface CommitTarget {
  kind: 'commit'
  member?: WorkspaceGitRef
  sessionId: string
  sha: string
  /** The session's published diff that holds the commit (its message, author and files are read from it).
   *  Without one, or when it does not hold the commit, the commit is read from the session's own
   *  workspace (the transport's commit read), when the transport has that read. */
  diff?: string
}
/** Something running (a Running now row): its title and the facts the host knows, each with its
 *  words and, where the words are a short form, the raw value (a model's id) for its tooltip. */
export interface RunTarget {
  kind: 'run'
  title: string
  facts: Partial<Record<RunField, { value: string; raw?: string }>>
}
export type HoverTarget = CardTarget | FileTarget | CommitTarget | RunTarget

/** A card as the hovercard draws it. done: it sits in a terminal column. */
export interface CardPreview {
  key?: string
  title?: string
  status?: string
  done?: boolean
  /** Read now from the board (not the agent's copy): the card says so. */
  live?: boolean
  priority?: string
  type?: string
  tags?: string[]
  assignee?: string
  description?: string
}

/** Where hovercards read from; a nested provider adds to (and overrides) the one above it. Inside a
 *  chat, the chat's own file operations are used when no provider gives them. */
export interface HovercardSources {
  files?: TransportFiles | null
  /** A session file by id (the session's list), for a chip that knows only the id. */
  artifact?: (sessionId: string, id: string) => ArtifactInfo | undefined
  /** Reads a card (once per hover; the host caches). null: nothing more than the chip knows. */
  card?: (t: CardTarget) => Promise<CardPreview | null>
  /** Whether a review of the file waits on the person. */
  reviewOpen?: (sessionId: string, artifactId: string) => boolean
  /** Who works on a card (a subagent or a started session), by name. */
  worker?: (t: CardTarget) => string | undefined
  /** The session's published diff that holds a commit, for a chip that does not name one. */
  diffFor?: (sessionId: string, sha: string) => string | undefined
  /** The name of the session's repository, shown with a commit's sha. */
  repo?: (sessionId: string) => string | undefined
  /** Who is signed in now: when it changes, everything hovercards read so far is forgotten (it was
   *  read with the other person's access). */
  account?: () => string
}

let heldAccount: string | undefined
/** Forgets what was read under another account than `who` (a silent renewal can sign in someone
 *  else without a reload). */
function holdAccount(who: string | undefined): void {
  if (who === undefined) return
  if (heldAccount !== undefined && heldAccount !== who) clearHovercardCache()
  heldAccount = who
}

const SourcesContext = createContext<HovercardSources>({})

export function HovercardProvider({ value, children }: { value: HovercardSources; children: ReactNode }) {
  const parent = useContext(SourcesContext)
  const merged: HovercardSources = { ...parent }
  for (const [k, v] of Object.entries(value)) if (v !== undefined) (merged as Record<string, unknown>)[k] = v
  return <SourcesContext.Provider value={merged}>{children}</SourcesContext.Provider>
}

function useSources(): HovercardSources {
  const given = useContext(SourcesContext)
  const chat = useChat()
  if (!chat) return given
  return {
    files: chat.files,
    artifact: (_s, id) => chat.attachmentById?.(id),
    ...Object.fromEntries(Object.entries(given).filter(([, v]) => v !== undefined && v !== null)),
  }
}

// ── The trigger ──────────────────────────────────────────────────────────────────────────────────
/** Wraps one chip: on a device that can hover (and with hovercards on), hovering it or focusing it
 *  from the keyboard opens its hovercard; otherwise the chip is returned as it is. */
export function Hover({ target, children }: { target: HoverTarget; children: ReactElement }) {
  const on = useHovercardsEnabled()
  const hover = useCanHover()
  if (!on || !hover) return children
  return <HoverAnchor target={target}>{children}</HoverAnchor>
}

// Focus opens a card only when the person moved it there with the keyboard (Tab, Shift+Tab or an
// arrow key; a click focuses the chip too, and so does a dialog closed with Esc or Enter handing the
// focus back), and the browser says the focus is visible.
const MOVE_KEYS = new Set(['Tab', 'ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'Home', 'End'])
let fromKeyboard = false
if (typeof document !== 'undefined') {
  document.addEventListener('keydown', e => { fromKeyboard = MOVE_KEYS.has(e.key) }, true)
  document.addEventListener('pointerdown', () => { fromKeyboard = false }, true)
}
function focusVisible(el: Element): boolean {
  if (!fromKeyboard) return false
  try { return el.matches(':focus-visible') } catch { return true }
}

// One card open at a time, page-wide: opening one closes the other. While one is open, and for
// HOVERCARD.warmMs after the last one closed, a hover opens the next card at once.
/** The time hovercards keep: delays, the warm window, the safe triangle's window. Tests put in a
 *  manual clock (setHovercardClock) so a timing test is decided by the clock they advance, never by
 *  how loaded the machine is. */
export interface HovercardClock {
  now(): number
  setTimeout(fn: () => void, ms: number): unknown
  clearTimeout(id: unknown): void
}
const realClock: HovercardClock = {
  now: () => performance.now(),
  setTimeout: (fn, ms) => globalThis.setTimeout(fn, ms),
  clearTimeout: id => globalThis.clearTimeout(id as ReturnType<typeof setTimeout>),
}
let clock: HovercardClock = realClock
/** The clock hovercards keep now (the panel times its own short waits by it, so tests drive both). */
export function getHovercardClock(): HovercardClock {
  return clock
}

/** Puts in a clock for hovercards' timing (null: the real one). For tests. */
export function setHovercardClock(c: HovercardClock | null): void {
  clock = c ?? realClock
  // Times read from the old clock mean nothing on the new one: no warm window, no open card held.
  closedAt = -Infinity
  current = null
}

let current: { owner: object; close: () => void; heading: () => boolean } | null = null
let closedAt = -Infinity
function warm(): boolean {
  return current !== null || clock.now() - closedAt < HOVERCARD.warmMs
}

type Point = { x: number; y: number }
/** Whether p is in the triangle a, b, c (with a few pixels of slack on every side). */
function inTriangle(p: Point, a: Point, b: Point, c: Point): boolean {
  const slack = 2
  const cx = (a.x + b.x + c.x) / 3, cy = (a.y + b.y + c.y) / 3
  const grow = (q: Point): Point => {
    const dx = q.x - cx, dy = q.y - cy
    const len = Math.hypot(dx, dy) || 1
    return { x: q.x + (dx / len) * slack, y: q.y + (dy / len) * slack }
  }
  const [A, B, C] = [grow(a), grow(b), grow(c)]
  const sign = (p1: Point, p2: Point, p3: Point) => (p1.x - p3.x) * (p2.y - p3.y) - (p2.x - p3.x) * (p1.y - p3.y)
  const d1 = sign(p, A, B), d2 = sign(p, B, C), d3 = sign(p, C, A)
  return !((d1 < 0 || d2 < 0 || d3 < 0) && (d1 > 0 || d2 > 0 || d3 > 0))
}
/** Whether the move from `was` to `p` heads for the card: it points between the corners of the
 *  card's edge that faces `was` (widened by HOVERCARD.angleDeg) and no steeper than steepDeg off
 *  straight at that edge. A move shorter than jitterPx is not judged (true). */
export function headsFor(p: Point, was: Point, card: DOMRect): boolean {
  const vx = p.x - was.x, vy = p.y - was.y
  if (Math.hypot(vx, vy) < HOVERCARD.jitterPx) return true
  // The facing edge's normal (toward the card) and its two corners.
  const [n, a, b] = was.x >= card.right ? [{ x: -1, y: 0 }, { x: card.right, y: card.top }, { x: card.right, y: card.bottom }]
    : was.x <= card.left ? [{ x: 1, y: 0 }, { x: card.left, y: card.top }, { x: card.left, y: card.bottom }]
    : was.y <= card.top ? [{ x: 0, y: 1 }, { x: card.left, y: card.top }, { x: card.right, y: card.top }]
    : [{ x: 0, y: -1 }, { x: card.left, y: card.bottom }, { x: card.right, y: card.bottom }]
  const angle = (x: number, y: number) => Math.atan2(n.x * y - n.y * x, n.x * x + n.y * y)
  const deg = Math.PI / 180
  const v = angle(vx, vy)
  const ca = angle(a.x - was.x, a.y - was.y), cb = angle(b.x - was.x, b.y - was.y)
  const tol = HOVERCARD.angleDeg * deg
  return Math.abs(v) <= HOVERCARD.steepDeg * deg && v >= Math.min(ca, cb) - tol && v <= Math.max(ca, cb) + tol
}

/** The triangle's apex: where the pointer left the chip, set back from the card. */
function apexBehind(from: Point, card: DOMRect): Point {
  const k = HOVERCARD.apexBehindPx
  if (from.x >= card.right) return { x: from.x + k, y: from.y }
  if (from.x <= card.left) return { x: from.x - k, y: from.y }
  return from.y <= card.top ? { x: from.x, y: from.y - k } : { x: from.x, y: from.y + k }
}

// Where the pointer was last: a chip's pointerleave comes before the move that left it, so this is
// the last point on the chip, the apex of its safe triangle.
let lastPoint: Point | null = null
if (typeof document !== 'undefined') {
  document.addEventListener('pointermove', e => { lastPoint = { x: e.clientX, y: e.clientY } }, true)
}
const inside = (p: Point, r: DOMRect) => p.x >= r.left && p.x <= r.right && p.y >= r.top && p.y <= r.bottom

/** The safe triangle: from where the pointer left the chip to the card's edge that faces it. */
function towardCard(p: Point, from: Point, card: DOMRect): boolean {
  if (inside(p, card)) return true
  const side = from.x <= card.left ? 'right' : from.x >= card.right ? 'left' : from.y <= card.top ? 'below' : 'above'
  const [a, b] = side === 'right' ? [{ x: card.left, y: card.top }, { x: card.left, y: card.bottom }]
    : side === 'left' ? [{ x: card.right, y: card.top }, { x: card.right, y: card.bottom }]
    : side === 'below' ? [{ x: card.left, y: card.top }, { x: card.right, y: card.top }]
    : [{ x: card.left, y: card.bottom }, { x: card.right, y: card.bottom }]
  return inTriangle(p, from, a, b)
}

/** What useHovercard hands a row: the props to spread on it (its ref and pointer and focus
 *  handlers; empty when hovercards are off or the device cannot hover), and the card to render. */
export interface HovercardBinding {
  enabled: boolean
  open: boolean
  props: {
    ref?: (el: HTMLElement | null) => void
    onPointerEnter?: (e: ReactPointerEvent) => void
    onPointerLeave?: (e: ReactPointerEvent) => void
    onPointerDown?: (e: ReactPointerEvent) => void
    onFocus?: (e: FocusEvent) => void
    onBlur?: (e: FocusEvent) => void
    'aria-describedby'?: string
    /** Marks a row anchor, so a chip nested in it is told apart (its events are the chip's). */
    'data-hc-anchor'?: string
  }
  card: ReactNode
}

/** The hovercard of a whole element (a Running now row, say) rather than one chip: spread
 *  `props` on the element and render `card` beside it. Same behavior, placement, warm group and
 *  switch as Hover. */
export function useHovercard(target: HoverTarget | null): HovercardBinding {
  const on = useHovercardsEnabled()
  const hover = useCanHover()
  const enabled = on && hover && !!target
  const b = useHoverAnchor(target, enabled)
  if (!enabled) return { enabled: false, open: false, props: {}, card: null }
  return b
}

function HoverAnchor({ target, children }: { target: HoverTarget; children: ReactElement }) {
  const b = useHoverAnchor(target, true)
  // The card replaces the chip's own tooltip (both at once would cover each other); while it is
  // open the chip is described by it.
  const chip = isValidElement(children)
    ? cloneElement(children as ReactElement<{ 'aria-describedby'?: string; title?: string }>, { title: undefined, ...(b.open ? { 'aria-describedby': b.props['aria-describedby'] } : {}) })
    : children
  const { 'aria-describedby': _described, 'data-hc-anchor': _mark, ...handlers } = b.props
  void _described
  void _mark
  return (
    <>
      <span className="hc-anchor" {...handlers}>{chip}</span>
      {b.card}
    </>
  )
}

function useHoverAnchor(target: HoverTarget | null, enabled: boolean): HovercardBinding {
  const sources = useSources()
  const [open, setOpen] = useState(false)
  // Open as of now (the state lags a render): the warm group's close time is written from it.
  const openNow = useRef(false)
  const anchor = useRef<HTMLElement | null>(null)
  const cardRef = useRef<HTMLDivElement | null>(null)
  const openTimer = useRef<unknown>(undefined)
  const closeTimer = useRef<unknown>(undefined)
  const untrack = useRef<(() => void) | null>(null)
  const me = useRef<object>({})
  const id = useId()
  const stopTracking = () => { untrack.current?.(); untrack.current = null }
  const clear = () => {
    clock.clearTimeout(openTimer.current)
    clock.clearTimeout(closeTimer.current)
    stopTracking()
  }
  const shut = useCallback(() => {
    clock.clearTimeout(openTimer.current)
    clock.clearTimeout(closeTimer.current)
    untrack.current?.()
    untrack.current = null
    if (openNow.current) closedAt = clock.now()
    openNow.current = false
    setOpen(false)
    if (current?.owner === me.current) current = null
  }, [])
  // Hovercards turned off (or the device stopped hovering) while this one is open: closed, and
  // nothing of it stays in the page-wide group.
  if (!enabled && open) setOpen(false)
  useEffect(() => {
    if (enabled) return
    clock.clearTimeout(openTimer.current)
    clock.clearTimeout(closeTimer.current)
    untrack.current?.()
    untrack.current = null
    openNow.current = false
    if (current?.owner === me.current) current = null
  }, [enabled])
  // Whether the pointer, out of the chip, is still heading for this card (in its safe triangle).
  const heading = useRef(false)
  const show = () => {
    clear()
    if (current && current.owner !== me.current) current.close()
    current = { owner: me.current, close: shut, heading: () => heading.current }
    openNow.current = true
    setOpen(true)
  }
  // Warm: at once. The pointer is on another chip: whatever the old card's triangle says, this
  // chip is what the person is on now (a hand drifting down a list beside a tall card is not
  // heading for it), so the old card closes and this one opens.
  const warmOpen = () => { show() }
  const warmStart = () => {
    clock.clearTimeout(openTimer.current)
    openTimer.current = clock.setTimeout(warmOpen, HOVERCARD.warmDwellMs)
  }
  useEffect(() => () => {
    clock.clearTimeout(openTimer.current)
    clock.clearTimeout(closeTimer.current)
    untrack.current?.()
    if (current?.owner === me.current) { current = null; closedAt = clock.now() }
    openNow.current = false
  }, [])
  const scheduleOpen = () => {
    clock.clearTimeout(closeTimer.current)
    stopTracking()
    if (open) return
    // Another card is open, or one just closed: this one at once.
    if (warm()) { warmStart(); return }
    clock.clearTimeout(openTimer.current)
    openTimer.current = clock.setTimeout(show, HOVERCARD.openDelayMs)
  }
  const scheduleClose = useCallback(() => {
    clock.clearTimeout(openTimer.current)
    clock.clearTimeout(closeTimer.current)
    closeTimer.current = clock.setTimeout(shut, HOVERCARD.closeDelayMs)
  }, [shut])
  // The pointer left the chip: the card stays only while the pointer heads for it and for at most
  // graceMs. "Heads for it": inside the safe triangle (apex set back behind where it left the chip)
  // and, judged over a short window of moves rather than per sample, pointing at the card within
  // a tolerance. Any other move (down the list, along the card) closes it at once.
  const leaveChip = (e: ReactPointerEvent) => {
    clock.clearTimeout(openTimer.current)
    if (!openNow.current) return
    const now = { x: e.clientX, y: e.clientY, t: clock.now() }
    const left = lastPoint && anchor.current && inside(lastPoint, anchorRect(anchor.current)) ? lastPoint : null
    stopTracking()
    clock.clearTimeout(closeTimer.current)
    const card0 = cardRef.current?.getBoundingClientRect()
    if (!card0) { shut(); return }
    const apex = apexBehind(left ?? now, card0)
    // A jump off the chip that already points away (onto the next chip below): closed at once.
    if (!towardCard(now, apex, card0) || (left && !headsFor(now, left, card0))) { shut(); return }
    closeTimer.current = clock.setTimeout(shut, HOVERCARD.graceMs)
    const cap = clock.setTimeout(shut, HOVERCARD.headingMaxMs)
    heading.current = true
    const moves: { x: number; y: number; t: number }[] = [...(left ? [{ ...left, t: now.t }] : []), now]
    const onMove = (m: PointerEvent) => {
      const card = cardRef.current?.getBoundingClientRect()
      const p = { x: m.clientX, y: m.clientY, t: clock.now() }
      if (anchor.current && inside(p, anchorRect(anchor.current))) return
      if (!card) { shut(); return }
      if (inside(p, card)) return
      moves.push(p)
      if (moves.length > 32) moves.shift()
      if (!towardCard(p, apex, card)) { shut(); return }
      // The window: the newest earlier move at least windowMoves back or windowMs old.
      let ref: Point | null = null
      for (let i = moves.length - 2, n = 1; i >= 0; i--, n++) {
        if (n >= HOVERCARD.windowMoves || p.t - moves[i].t >= HOVERCARD.windowMs) { ref = moves[i]; break }
      }
      if (ref && !headsFor(p, ref, card)) { shut(); return }
      // Still on its way: the wait starts over.
      clock.clearTimeout(closeTimer.current)
      closeTimer.current = clock.setTimeout(shut, HOVERCARD.graceMs)
    }
    document.addEventListener('pointermove', onMove, true)
    untrack.current = () => { heading.current = false; clock.clearTimeout(cap); document.removeEventListener('pointermove', onMove, true) }
  }
  const keep = useCallback(() => {
    clock.clearTimeout(closeTimer.current)
    untrack.current?.()
    untrack.current = null
  }, [])
  const onChip = (t: EventTarget) => !!anchor.current && t instanceof Node && anchor.current.contains(t)
  // The event is this anchor's own, not a chip with its own card nested in it (a ticket key in a
  // Running now row): that chip's events are its own.
  const own = (t: EventTarget) => {
    if (!onChip(t) || !(t instanceof Element)) return onChip(t)
    return t.closest('.hc-anchor, [data-hc-anchor]') === anchor.current
  }
  // Esc closes it wherever the focus is. It is taken only when the focus is on the chip (the person
  // reached it with the keyboard and the card is what Esc is for, so a drawer under it stays open);
  // anywhere else (the composer, a dialog) the Esc goes on to what has the focus as well.
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      if (anchor.current && document.activeElement && anchor.current.contains(document.activeElement)) e.stopPropagation()
      shut()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [open, shut])
  const setAnchor = useCallback((el: HTMLElement | null) => { anchor.current = el }, [])
  return {
    enabled: true,
    open,
    props: {
      ref: setAnchor,
      'data-hc-anchor': '',
      onPointerEnter: (e: ReactPointerEvent) => { if (e.pointerType !== 'touch' && own(e.target)) scheduleOpen() },
      onPointerLeave: (e: ReactPointerEvent) => { if (e.pointerType !== 'touch') leaveChip(e) },
      // Events from inside the card (a portal) can reach the anchor through React: only the
      // anchor's own press closes it.
      onPointerDown: (e: ReactPointerEvent) => { if (onChip(e.target)) shut() },
      onFocus: (e: FocusEvent) => { if (own(e.target) && focusVisible(e.target)) scheduleOpen() },
      onBlur: (e: FocusEvent) => { if (onChip(e.target)) scheduleClose() },
      ...(open ? { 'aria-describedby': id } : {}),
    },
    card: open && target ? createPortal(
      <Popup id={id} cardRef={cardRef} target={target} sources={sources} anchor={anchor} onEnter={keep} onLeave={shut} />,
      document.body,
    ) : null,
  }
}

const MARGIN = 8
const stop = (e: { stopPropagation: () => void }) => e.stopPropagation()

/** The anchor's box: a chip's (the span around it has none of its own), or a row's own. */
function anchorRect(anchor: HTMLElement): DOMRect {
  const el = anchor.classList.contains('hc-anchor') ? anchor.firstElementChild ?? anchor : anchor
  return el.getBoundingClientRect()
}

export type HovercardSide = 'left' | 'right' | 'below' | 'above'
export interface HovercardPlace { left: number; top: number; maxHeight: number; side: HovercardSide }

/** Where a card of w x h goes for a chip at `chip`: BESIDE what the chip sits in, never over the
 *  rows that follow it. In order: beside the boundary the host marked (`data-hc-boundary`: the
 *  runner's side panel, so the card opens to its left), else beside the chip's row (its list item),
 *  else beside the chip itself; on whichever side has room (the roomier one first), and beside a
 *  boundary or row only on a side the chip is within a card's width of. Only when no
 *  side has room (a narrow screen) it goes below or above. Always on screen. */
export function placeCard(chip: DOMRect, around: DOMRect[], w: number, h: number, vw: number, vh: number): HovercardPlace {
  const gap = HOVERCARD.gap
  const tall = Math.max(80, vh - 2 * MARGIN)
  const shown = Math.min(h, tall)
  const top = Math.max(MARGIN, Math.min(chip.top - 6, vh - MARGIN - shown))
  for (const r of around) {
    const right = vw - r.right - gap - MARGIN
    const left = r.left - gap - MARGIN
    const sides: ['left' | 'right', number][] = left >= right ? [['left', left], ['right', right]] : [['right', right], ['left', left]]
    for (const [side, room] of sides) {
      if (room < w) continue
      // Beside a wide row only when the chip is near that side of it: a card hundreds of pixels
      // from its chip (a chip at a row's far end) would be hard to reach; the chip's own side is.
      const reach = side === 'right' ? r.right - chip.right : chip.left - r.left
      if (r !== chip && reach > w) continue
      return { side, top, maxHeight: tall, left: side === 'right' ? r.right + gap : r.left - gap - w }
    }
  }
  const below = vh - chip.bottom - gap - MARGIN
  const over = chip.top - gap - MARGIN
  const above = h > below && over > below
  const room = Math.max(80, above ? over : below)
  const vshown = Math.min(h, room)
  return {
    side: above ? 'above' : 'below', maxHeight: room,
    top: above ? Math.max(MARGIN, chip.top - gap - vshown) : Math.min(chip.bottom + gap, vh - MARGIN - vshown),
    left: Math.max(MARGIN, Math.min(chip.left, vw - w - MARGIN)),
  }
}

function Popup({ id, cardRef, target, sources, anchor, onEnter, onLeave }: {
  id: string
  cardRef: { current: HTMLDivElement | null }
  target: HoverTarget
  sources: HovercardSources
  anchor: { current: HTMLElement | null }
  onEnter: () => void
  onLeave: () => void
}) {
  holdAccount(sources.account?.())
  const ref = cardRef
  const [place, setPlace] = useState<HovercardPlace | null>(null)
  useLayoutEffect(() => {
    const measure = () => {
      const card = ref.current
      const span = anchor.current
      if (!card || !span) return
      const chip = anchorRect(span)
      const around: DOMRect[] = []
      const boundary = span.closest('[data-hc-boundary]')
      if (boundary) around.push(boundary.getBoundingClientRect())
      const row = span.classList.contains('hc-anchor') ? span.parentElement?.closest('li, [data-hc-row]') : null
      if (row && (!boundary || boundary.contains(row) === false)) around.push(row.getBoundingClientRect())
      around.push(chip)
      const vw = document.documentElement.clientWidth || window.innerWidth
      const next = placeCard(chip, around, card.offsetWidth, card.scrollHeight, vw, window.innerHeight)
      setPlace(p => (p && p.left === next.left && p.top === next.top && p.maxHeight === next.maxHeight && p.side === next.side ? p : next))
    }
    measure()
    const card = ref.current
    const ro = typeof ResizeObserver === 'undefined' || !card ? null : new ResizeObserver(() => measure())
    if (card) ro?.observe(card)
    window.addEventListener('resize', measure)
    window.addEventListener('scroll', measure, true)
    return () => {
      ro?.disconnect()
      window.removeEventListener('resize', measure)
      window.removeEventListener('scroll', measure, true)
    }
  }, [anchor, ref])
  const style: CSSProperties = {
    position: 'fixed',
    left: place?.left ?? 0,
    top: place?.top ?? 0,
    maxHeight: place?.maxHeight,
    width: `min(${HOVERCARD.width}px, calc(100vw - ${2 * MARGIN}px))`,
    visibility: place ? 'visible' : 'hidden',
  }
  return (
    <div ref={ref} id={id} role="tooltip" className={`hc hc-${target.kind}`} data-testid="hovercard" data-kind={target.kind}
      data-side={place?.side ?? 'below'} style={style} onPointerEnter={onEnter} onPointerLeave={onLeave}
      // A portal: React would carry a press or a click in it up to the chip's ancestors (a row that
      // opens something, a drawer's backdrop). It stays in the card.
      onPointerDown={stop} onMouseDown={stop} onClick={stop}>
      {target.kind === 'card' && <CardBody target={target} sources={sources} />}
      {target.kind === 'file' && <FileBody target={target} sources={sources} />}
      {target.kind === 'commit' && <CommitBody target={target} sources={sources} />}
      {target.kind === 'run' && <RunBody target={target} />}
    </div>
  )
}

/** The data a hovercard reads, once it is open: null while it loads. */
function useRead<T>(read: (() => Promise<T>) | null, deps: readonly unknown[]): { value: T | null; failed: boolean; done: boolean } {
  const [state, setState] = useState<{ value: T | null; failed: boolean; done: boolean }>({ value: null, failed: false, done: false })
  useEffect(() => {
    if (!read) return
    let live = true
    read().then(v => { if (live) setState({ value: v, failed: false, done: true }) }, () => { if (live) setState({ value: null, failed: true, done: true }) })
    return () => { live = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)
  return state
}

// ── Cards ────────────────────────────────────────────────────────────────────────────────────────
function firstLines(text: string, n: number): string {
  return text.replace(/\r\n/g, '\n').split('\n').map(l => l.trimEnd()).filter((l, i, all) => l !== '' || (i > 0 && all[i - 1] !== '')).slice(0, n).join('\n').trim()
}

function CardBody({ target, sources }: { target: CardTarget; sources: HovercardSources }) {
  const read = useRead(sources.card ? () => sources.card!(target) : null, [target.source, target.boardId, target.key])
  const t = target.ticket ?? {}
  const c: CardPreview = {
    key: target.key,
    title: target.title ?? t.title,
    status: target.status ?? t.status,
    priority: t.priority,
    type: t.type,
    tags: t.labels,
    assignee: t.assignee,
    description: t.description,
    ...Object.fromEntries(Object.entries(read.value ?? {}).filter(([, v]) => v !== undefined && v !== null && v !== '')),
  }
  const worker = target.worker ?? sources.worker?.(target)
  const tone = c.done ? 'done' : statusTone(c.status)
  const fields = HOVERCARD.fields.card
  const glyph = priorityGlyph(c.priority)
  const parts: Record<CardField, ReactNode> = {
    status: c.status ? <span key="status" className={`ticket-chip ticket-status tone-${tone}`} data-testid="hovercard-status">{c.status}</span> : null,
    priority: c.priority ? <span key="priority" className="hc-chip" data-testid="hovercard-priority" title="Priority">{glyph && <span aria-hidden="true">{glyph} </span>}{c.priority}</span> : null,
    type: c.type ? <span key="type" className="hc-chip" data-testid="hovercard-type" title="Type">{c.type}</span> : null,
    tags: c.tags && c.tags.length > 0 ? <span key="tags" className="hc-tags" data-testid="hovercard-tags">{c.tags.map(tag => <span key={tag} className="hc-tag">{tag}</span>)}</span> : null,
    assignee: worker
      ? <span key="assignee" className="hc-who" data-testid="hovercard-worker" title="Working on it">↳ {worker}</span>
      : c.assignee ? <span key="assignee" className="hc-who" data-testid="hovercard-assignee" title="Assignee">{c.assignee}</span> : null,
    description: c.description ? <p key="description" className="hc-desc" data-testid="hovercard-description" style={{ WebkitLineClamp: HOVERCARD.descriptionLines }}>{firstLines(c.description, HOVERCARD.descriptionLines + 2)}</p> : null,
  }
  const chips = fields.filter(f => f !== 'description').map(f => parts[f]).filter(Boolean)
  return (
    <>
      <div className="hc-head">
        <span className="hc-source">{sourceLabel(target.source) || 'ticket'}</span>
        <span className="hc-key" data-testid="hovercard-key">{c.key}</span>
        {sources.card && !read.done && <span className="hc-loading" aria-hidden="true" />}
      </div>
      {c.title && <div className={`hc-title${tone === 'canceled' ? ' canceled' : ''}`} data-testid="hovercard-title">{c.title}</div>}
      {chips.length > 0 && <div className="hc-meta">{chips}</div>}
      {fields.includes('description') && parts.description}
      {c.live && <div className="hc-note hc-live" data-testid="hovercard-live">Live from the board</div>}
    </>
  )
}

// ── Running now rows ─────────────────────────────────────────────────────────────────────────────
const RUN_LABEL: Record<RunField, string> = {
  kind: 'Kind', engine: 'Engine', model: 'Model', state: 'State', step: 'Step', elapsed: 'Elapsed', tokens: 'Tokens', card: 'Card', startedBy: 'Started by',
}

function RunBody({ target }: { target: RunTarget }) {
  const fields = HOVERCARD.fields.run
  const step = fields.includes('step') ? target.facts.step : undefined
  const rows = fields.filter(f => f !== 'step' && target.facts[f]?.value)
  return (
    <>
      <div className="hc-title" data-testid="hovercard-run-title">{target.title}</div>
      {step?.value && <pre className="hc-step" data-testid="hovercard-step">{step.value}</pre>}
      {rows.length > 0 && (
        <dl className="hc-facts">
          {rows.map(f => (
            <div key={f} data-testid={`hovercard-run-${f}`}>
              <dt>{RUN_LABEL[f]}</dt>
              <dd title={target.facts[f]!.raw}>{target.facts[f]!.value}</dd>
            </div>
          ))}
        </dl>
      )}
    </>
  )
}

// ── Files ────────────────────────────────────────────────────────────────────────────────────────
type FilePreviewData =
  | { kind: 'image'; blob: Blob }
  | { kind: 'visual'; visual: 'mermaid' | 'vega-lite'; source: string }
  | { kind: 'diff'; files: { path: string; adds: number; dels: number }[]; adds: number; dels: number }
  | { kind: 'lines'; text: string; more: boolean }
  | { kind: 'none' }

const TEXT_VIEWS = new Set(['markdown', 'text', 'json', 'csv', 'diff'])

// The images hovercards drew, the last few only (oldest out first): a sweep down a long Files list
// keeps at most THUMBS of them. The card makes its own object URL and revokes it when it closes.
const THUMBS = 4
const thumbs = new Map<string, Promise<Blob>>()
function readThumb(sessionId: string, a: ArtifactInfo, raw: (id: string) => Promise<Blob>): Promise<Blob> {
  const key = `${sessionId}/${a.id}`
  let p = thumbs.get(key)
  if (p) thumbs.delete(key)
  else {
    p = raw(a.id)
    p.catch(() => thumbs.delete(key))
  }
  thumbs.set(key, p)
  while (thumbs.size > THUMBS) thumbs.delete(thumbs.keys().next().value!)
  return p
}

/** The image of a hovercard, from its bytes: a URL made for as long as the card is open (an SVG's a
 *  data: URL, never a same-origin blob: one; mediaUrl). */
function Thumb({ blob, view, contentType, alt }: { blob: Blob; view: string; contentType: string; alt: string }) {
  const ref = useRef<HTMLImageElement | null>(null)
  useEffect(() => {
    let u: string | null = null
    let gone = false
    void mediaUrl(blob, view, contentType).then(made => {
      if (gone) { releaseMediaUrl(made); return }
      u = made
      if (ref.current) ref.current.src = made
    })
    return () => { gone = true; releaseMediaUrl(u) }
  }, [blob, view, contentType])
  return <img ref={ref} alt={alt} className="hc-image" />
}

async function readFilePreview(sessionId: string, a: ArtifactInfo, files: TransportFiles): Promise<FilePreviewData> {
  const raw = (id: string) => files.raw(sessionId, id)
  if (a.view === 'image') {
    if (a.size > INLINE_IMAGE_MAX) return { kind: 'none' }
    return { kind: 'image', blob: await readThumb(sessionId, a, raw) }
  }
  if (!TEXT_VIEWS.has(a.view) || a.size > INLINE_TEXT_MAX) return { kind: 'none' }
  const text = await loadPreviewText(sessionId, a.id, raw)
  if (a.view === 'diff') {
    const p = parsePatch(text)
    return { kind: 'diff', files: p.files.map(f => ({ path: f.path, adds: f.adds, dels: f.dels })), adds: p.adds, dels: p.dels }
  }
  const visual = visualFile(a.name, a.view, text)
  if (visual) return { kind: 'visual', visual, source: text.replace(/\n$/, '') }
  const lines = text.replace(/^\uFEFF/, '').split('\n')
  const n = HOVERCARD.excerptLines
  return { kind: 'lines', text: lines.slice(0, n).map(l => (l.length > 200 ? `${l.slice(0, 200)}…` : l)).join('\n'), more: lines.length > n && !(lines.length === n + 1 && lines[n] === '') }
}

function whenLabel(iso: string | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })
}

function DiffFiles({ files, adds, dels, more }: { files: { path: string; adds: number; dels: number }[]; adds: number; dels: number; more?: boolean }) {
  const shown = files.slice(0, HOVERCARD.files)
  return (
    <div className="hc-diff" data-testid="hovercard-diff">
      <div className="hc-diff-total">{files.length}{more ? '+' : ''} {files.length === 1 && !more ? 'file' : 'files'} · <span className="diff-plus">+{adds}</span> <span className="diff-minus">−{dels}</span></div>
      <ul className="hc-files">
        {shown.map(f => (
          <li key={f.path}><bdi className="hc-path">{f.path}</bdi><span className="hc-counts"><span className="diff-plus">+{f.adds}</span> <span className="diff-minus">−{f.dels}</span></span></li>
        ))}
        {files.length > shown.length && <li className="hc-more">and {files.length - shown.length} more</li>}
      </ul>
    </div>
  )
}

function FileBody({ target, sources }: { target: FileTarget; sources: HovercardSources }) {
  const a = target.artifact ?? sources.artifact?.(target.sessionId, target.id)
  const files = sources.files ?? null
  const fields = HOVERCARD.fields.file
  const wantPreview = fields.includes('preview') && !!a && !!files && !a.missing
  const read = useRead(wantPreview ? () => readFilePreview(target.sessionId, a!, files!) : null, [target.sessionId, a?.id, wantPreview])
  if (!a) return <div className="hc-note" data-testid="hovercard-file-missing">This file is not in the session’s list.</div>
  const review = target.reviewOpen ?? sources.reviewOpen?.(target.sessionId, a.id) ?? false
  const by = a.origin === 'user' ? 'you' : 'the agent'
  const when = whenLabel(a.created_at)
  const p = read.value
  const meta: ReactNode[] = []
  for (const f of fields) {
    if (f === 'version' && a.version !== undefined) meta.push(<span key="v" data-testid="hovercard-version">v{a.version}{a.latest_version && a.latest_version > a.version ? ` of ${a.latest_version}` : ''}</span>)
    if (f === 'size') meta.push(<span key="s" data-testid="hovercard-size">{formatSize(a.size)}</span>)
    if (f === 'publisher') meta.push(<span key="p" data-testid="hovercard-publisher">{`by ${by}${when ? ` · ${when}` : ''}`}</span>)
    if (f === 'review' && review) meta.push(<span key="r" className="hc-review" data-testid="hovercard-review">✎ review open</span>)
  }
  return (
    <>
      {wantPreview && (
        <div className="hc-preview" data-testid="hovercard-preview" data-preview={p?.kind ?? (read.failed ? 'failed' : 'loading')}>
          {!read.done && <span className="hc-loading" aria-hidden="true" />}
          {p?.kind === 'image' && <Thumb blob={p.blob} view={a.view} contentType={a.content_type} alt={a.name} />}
          {p?.kind === 'visual' && <VisualThumb kind={p.visual} source={p.source} />}
          {p?.kind === 'diff' && <DiffFiles files={p.files} adds={p.adds} dels={p.dels} />}
          {p?.kind === 'lines' && <pre className={`hc-lines${p.more ? ' has-more' : ''}`} data-testid="hovercard-lines">{p.text}</pre>}
        </div>
      )}
      <div className="hc-head">
        <span className="hc-file-icon" aria-hidden="true">📎</span>
        <span className="hc-name" data-testid="hovercard-name">{a.name}</span>
      </div>
      {meta.length > 0 && <div className="hc-meta hc-dots">{meta}</div>}
      {a.missing && <div className="hc-note">Its bytes are no longer stored.</div>}
    </>
  )
}

// ── Commits ──────────────────────────────────────────────────────────────────────────────────────
// A commit that cannot be read anywhere (no published diff holds it, and the session's workspace
// cannot be read: the session ended, its daemon is offline, the sha is unknown, no access) shows
// its short sha and the repository's name, and nothing else: there is nothing for the person to do.

/** The last part of a repository path or name. */
export function repoName(repo: string | undefined): string {
  return (repo ?? '').replace(/[\\/]+$/, '').split(/[\\/]/).pop() ?? ''
}
interface CommitPreview {
  sha: string
  subject: string
  body: string
  author: string
  date: string
  files: { path: string; adds: number; dels: number }[]
  /** More files changed than are listed (a workspace read lists the first few hundred). */
  moreFiles?: boolean
  adds: number
  dels: number
}

/** A commit's card: what the diff says of it, null when the diff does not hold it, 'large' when the
 *  diff is over INLINE_TEXT_MAX (or of a size not known without reading it all) and is not read. */
type CommitRead = CommitPreview | null | 'large'

/** Commits read from session workspaces, by session and sha: a commit does not change, so a read
 *  is kept (the newest COMMIT_CACHE); a read that found nothing is kept COMMIT_MISS_MS, then tried
 *  again (the session's daemon may be back). */
const workspaceCommits = new Map<string, { read: Promise<CommitPreview | null>; until?: number }>()
const COMMIT_CACHE = 64
export const COMMIT_MISS_MS = 60_000
const COMMIT_SHA = /^[0-9a-f]{7,64}$/

function previewOf(c: CommitDetails): CommitPreview {
  return {
    sha: c.sha, subject: c.subject, body: c.body, author: c.author, date: c.author_date,
    files: c.files.map(f => ({ path: f.path, adds: f.adds, dels: f.dels })), moreFiles: !!c.files_truncated, adds: c.adds, dels: c.dels,
  }
}

/** A commit as drawn: its subject, message, author and file names with hidden characters shown. */
function shown(c: CommitPreview): CommitPreview {
  return {
    ...c, subject: visibleText(c.subject), body: visibleText(c.body), author: visibleText(c.author),
    files: c.files.map(f => ({ ...f, path: visibleText(f.path) })),
  }
}

/** The commit from the session's workspace, through the transport's commit read; null when it
 *  cannot be read (nothing is said of why). */
function lookupCommit(files: TransportFiles, sessionId: string, given: string, member?: WorkspaceGitRef): Promise<CommitPreview | null> {
  const sha = given.toLowerCase()
  if (!files.commit || !COMMIT_SHA.test(sha)) return Promise.resolve(null)
  const key = `${sessionId}/${sha}/${JSON.stringify(member)}`
  let entry = workspaceCommits.get(key)
  if (entry) workspaceCommits.delete(key)
  if (entry?.until !== undefined && Date.now() >= entry.until) entry = undefined
  if (!entry) {
    const read: Promise<CommitPreview | null> = (member ? files.commit(sessionId, sha, undefined, member) : files.commit(sessionId, sha)).then(c => (c ? previewOf(c) : null), () => null)
    const fresh: { read: Promise<CommitPreview | null>; until?: number } = { read }
    void read.then(v => { if (!v) fresh.until = Date.now() + COMMIT_MISS_MS })
    entry = fresh
  }
  workspaceCommits.set(key, entry)
  while (workspaceCommits.size > COMMIT_CACHE) workspaceCommits.delete(workspaceCommits.keys().next().value!)
  return entry.read
}

/** The commit from its published diff when one holds it, else from the session's workspace. A diff
 *  too large to read here stays 'large' when the workspace cannot say either. */
async function readAnyCommit(t: CommitTarget, files: TransportFiles, size: number | undefined): Promise<CommitRead> {
  let fromDiff: CommitRead = null
  if (t.diff && !t.member) fromDiff = await readCommit(t, files, size).catch(() => null)
  if (fromDiff && fromDiff !== 'large') return fromDiff
  return (await lookupCommit(files, t.sessionId, t.sha, t.member)) ?? fromDiff
}
/** The commits of a published diff, parsed once per session and diff: every commit chip of that
 *  diff reuses the one read. Only the parsed commits are kept, never the patch's text. */
const diffCommits = new Map<string, Promise<CommitPreview[] | 'large'>>()
const DIFF_CACHE = 16

/** Forgets what hovercards read so far (the commits and the images) and which one was open last
 *  (tests, an account switch). */
export function clearHovercardCache(): void {
  diffCommits.clear()
  workspaceCommits.clear()
  thumbs.clear()
  current = null
  closedAt = -Infinity
}

/** The diff's bytes, never more than INLINE_TEXT_MAX: not fetched when the list says it is larger;
 *  else read with the transport's capped read (it stops the download past the cap), or, without
 *  one, read whole only when the list gives a size within the cap. null: not read. */
async function cappedDiff(t: CommitTarget, files: TransportFiles, size: number | undefined): Promise<Blob | null> {
  if (size !== undefined && size > INLINE_TEXT_MAX) return null
  if (files.rawCapped) return files.rawCapped(t.sessionId, t.diff!, INLINE_TEXT_MAX)
  if (size === undefined) return null
  const blob = await files.raw(t.sessionId, t.diff!)
  return blob.size > INLINE_TEXT_MAX ? null : blob
}

function commitsOf(text: string): CommitPreview[] {
  const patch = parsePatch(text)
  return patch.commits.map((c, index) => {
    const author = c.lines.find(l => l.startsWith('Author:'))?.slice(7).trim() ?? ''
    const date = c.lines.find(l => l.startsWith('Date:'))?.slice(5).trim() ?? ''
    const message = c.lines.filter(l => /^ {4}/.test(l) || l === '').map(l => l.slice(4)).join('\n').trim()
    const body = message.split('\n').slice(1).join('\n').trim()
    const own = patch.files.filter(f => f.commit === index)
    return {
      sha: c.sha, subject: c.subject, body, author, date,
      files: own.map(f => ({ path: f.path, adds: f.adds, dels: f.dels })),
      adds: own.reduce((n, f) => n + f.adds, 0), dels: own.reduce((n, f) => n + f.dels, 0),
    }
  })
}

/** The commit from its published diff (the diff read and parsed once, for all its commits). */
async function readCommit(t: CommitTarget, files: TransportFiles, size: number | undefined): Promise<CommitRead> {
  const key = `${t.sessionId}/${t.diff}`
  let p = diffCommits.get(key)
  if (p) diffCommits.delete(key)
  else {
    p = cappedDiff(t, files, size).then(async blob => (blob ? commitsOf(await blob.text()) : 'large' as const))
    p.catch(() => diffCommits.delete(key))
  }
  diffCommits.set(key, p)
  while (diffCommits.size > DIFF_CACHE) diffCommits.delete(diffCommits.keys().next().value!)
  const all = await p
  if (all === 'large') return 'large'
  const sha = t.sha.toLowerCase()
  return all.find(c => c.sha.startsWith(sha) || sha.startsWith(c.sha)) ?? null
}

function CommitBody({ target: given, sources }: { target: CommitTarget; sources: HovercardSources }) {
  const diff = given.diff ?? sources.diffFor?.(given.sessionId, given.sha)
  const target: CommitTarget = diff ? { ...given, diff } : given
  const files = sources.files ?? null
  const can = !!files && (!!target.diff || !!files.commit)
  const size = target.diff ? sources.artifact?.(target.sessionId, target.diff)?.size : undefined
  const read = useRead(can ? () => readAnyCommit(target, files!, size) : null, [target.sessionId, target.diff, target.sha, target.member?.member_id, target.member?.launch_generation, target.member?.generation, can])
  const large = read.value === 'large'
  const c = read.value === 'large' || !read.value ? null : shown(read.value)
  const fields = HOVERCARD.fields.commit
  const loading = can && !read.done
  const repo = repoName(sources.repo?.(target.sessionId))
  return (
    <>
      <div className="hc-head">
        <span className="hc-key hc-sha" data-testid="hovercard-sha">{target.sha.slice(0, 7)}</span>
        {repo && <span className="hc-key hc-repo" data-testid="hovercard-repo">{repo}</span>}
        {loading && <span className="hc-loading" aria-hidden="true" />}
      </div>
      {c && fields.includes('message') && (
        <>
          <div className="hc-title" data-testid="hovercard-subject">{c.subject}</div>
          {c.body && <p className="hc-desc" style={{ WebkitLineClamp: HOVERCARD.descriptionLines }}>{firstLines(c.body, HOVERCARD.descriptionLines + 2)}</p>}
        </>
      )}
      {c && fields.includes('author') && (c.author || c.date) && (
        <div className="hc-meta hc-dots" data-testid="hovercard-author">
          {c.author && <span>{c.author}</span>}
          {c.date && <span>{whenLabel(c.date) || c.date}</span>}
        </div>
      )}
      {c && fields.includes('files') && c.files.length > 0 && <DiffFiles files={c.files} adds={c.adds} dels={c.dels} more={c.moreFiles} />}
      {!loading && large && <div className="hc-note" data-testid="hovercard-commit-none">{target.sha}{'\n'}Its diff is too large to preview here; open it to read the commit.</div>}
    </>
  )
}
