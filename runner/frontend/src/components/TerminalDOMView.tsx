import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { send, onMessage, onOpen } from '../ws'
import type { SessionOutput, HistoryDone, FocusStolen, FocusGranted, SessionScrollback } from '../types'
import { BG, FG, renderRow, keyToSeq, isAtScrollBottom, computeKeyboardAdjustment, computeCompositionDiff, gridCount, SCROLLBACK_WINDOW_MS, MAX_SCROLLBACK_LINES } from '../terminalRenderer'
import { useIsMobile } from '../hooks/useIsMobile'

// Scroll-container padding (px, per side). Shared between the inline style and
// the cols/rows computation so the terminal grid never over-counts into the
// padding and clips the right/bottom edge. See gridCount() in terminalRenderer.
const PAD_X = 6
const PAD_Y = 4

// Debounce after a resize_session send before requesting a fresh scrollback
// snapshot: the daemon's tmux refresh-client repaint debounce is ~120ms, so a
// capture taken immediately would land mid-reflow.
const RESIZE_SNAPSHOT_DEBOUNCE_MS = 150

// ── Public handle (exposed via ref) ──────────────────────────────────────────

export interface TerminalHandle {
  cols: number
  rows: number
  applicationCursorKeysMode: boolean
  focus(): void
}

// ── Component ─────────────────────────────────────────────────────────────────

interface Props {
  sessionId: string
  ref?: React.Ref<TerminalHandle | null>
}

export default function TerminalDOMView({ sessionId, ref }: Props) {
  const scrollRef      = useRef<HTMLDivElement>(null)   // scrollable outer
  const historyRef     = useRef<HTMLDivElement>(null)   // tmux-sourced scrollback (rendered from session_scrollback)
  const primaryViewRef = useRef<HTMLDivElement>(null)   // (reserved; kept empty)
  const viewportRef    = useRef<HTMLDivElement>(null)   // active-buffer current screen (re-rendered each frame)
  const taRef          = useRef<HTMLTextAreaElement>(null) // keyboard capture

  // Headless xterm for VT parsing
  const termRef    = useRef<Terminal | null>(null)
  const charWRef   = useRef(7)   // measured char width (px)
  const charHRef   = useRef(14)  // measured char height (px)
  const rafRef     = useRef<number | null>(null)
  const composing  = useRef(false)
  const stickRef    = useRef(true)  // whether to auto-scroll to bottom
  const kbRef       = useRef(0)    // current soft-keyboard height (px); 0 = closed
  const isMobileRef = useRef(false) // always-current mirror of the isMobile hook value
  const [loading, setLoading] = useState(true) // true until first output chunk arrives

  const [focusStolen, setFocusStolen] = useState(false)
  const focusStolenRef = useRef(false)

  const isMobile = useIsMobile()
  // Keep the refs in sync without re-running the main effect. A layout effect
  // runs on commit, before any event handler can read them.
  useLayoutEffect(() => {
    isMobileRef.current = isMobile
    focusStolenRef.current = focusStolen
  }, [isMobile, focusStolen])

  // Expose handle to parent via ref
  useEffect(() => {
    if (!ref) return
    const handle: TerminalHandle = {
      get cols() { return termRef.current?.cols ?? 80 },
      get rows() { return termRef.current?.rows ?? 24 },
      get applicationCursorKeysMode() { return termRef.current?.modes.applicationCursorKeysMode ?? false },
      focus() { taRef.current?.focus({ preventScroll: true }) },
    }
    if (typeof ref === 'function') ref(handle)
    else (ref as React.MutableRefObject<TerminalHandle | null>).current = handle
    return () => {
      if (typeof ref === 'function') ref(null)
      else (ref as React.MutableRefObject<TerminalHandle | null>).current = null
    }
  }, [ref])

  useEffect(() => {
    if (!scrollRef.current || !historyRef.current || !primaryViewRef.current || !viewportRef.current || !taRef.current || !sessionId) return
    // Reset per-session state so it doesn't leak across session switches.
    // stickRef MUST be reset here — if the user scrolled up in a previous session it
    // would stay false, leaving the new session stuck at the top with nothing to scroll
    // into (the recurring "can't scroll" bug on session switch).
    stickRef.current = true
    setFocusStolen(false)
    focusStolenRef.current = false  // reset ref synchronously; the render-time sync is deferred
    // Non-null aliases for use inside closures (TypeScript narrows at call site but not inside closures)
    const scrollEl      = scrollRef.current as HTMLDivElement
    const historyEl     = historyRef.current as HTMLDivElement
    const primaryViewEl = primaryViewRef.current as HTMLDivElement
    const viewportEl    = viewportRef.current as HTMLDivElement
    const taEl          = taRef.current as HTMLTextAreaElement

    // Measure a monospace character using a temp element
    const probe = document.createElement('span')
    probe.style.cssText = 'position:absolute;visibility:hidden;white-space:pre;font-family:monospace;font-size:13px'
    probe.textContent = 'W'
    scrollEl.appendChild(probe)
    const pr = probe.getBoundingClientRect()
    scrollEl.removeChild(probe)
    charWRef.current = pr.width  || 7
    charHRef.current = pr.height || 14

    const cols = gridCount(scrollEl.clientWidth,  charWRef.current, PAD_X * 2, 20)
    const rows = gridCount(scrollEl.clientHeight, charHRef.current, PAD_Y * 2, 5)

    const terminal = new Terminal({ cols, rows, allowProposedApi: true })
    termRef.current = terminal

    // Scrollback (historyEl) is sourced authoritatively from tmux via
    // session_scrollback messages — see applyScrollback below. We no longer
    // reconstruct it client-side from the replayed byte stream (which failed for
    // full-screen / alt-screen TUIs that redraw via absolute cursor positioning).
    // A dedicated headless terminal parses the captured ANSI for rendering.
    let sbTerm: Terminal | null = null
    let lastScrollback = ''   // dedup: skip rebuild when capture is unchanged
    let lastSbRequest = 0     // throttle scrollback refresh requests
    // Diff-append state: the capture lines currently represented in historyEl,
    // one DOM line-group child per entry (see applyScrollback). Lets consecutive
    // overlapping captures append only their new tail instead of rebuilding, and
    // lets the cap trim whole rendered line-groups from the top. Reset whenever
    // historyEl is wiped (firstChunk resubscribe / empty capture).
    let renderedLines: string[] = []

    // Whether bracketed-paste mode has ever been positively observed (true)
    // this connection. tmux 3.2a exposes no format variable for it (unlike
    // applicationCursorKeysMode), so the mode-sync preamble can't cover it —
    // the client only learns it from \x1b[?2004h bytes in the live stream,
    // which the 256KB replay tail can miss for long-running sessions. A
    // manual experiment (writing raw bytes to a tmux client's PTY, the same
    // layer blerg-runner uses) showed tmux does not mediate: it forwards
    // \x1b[200~/\x1b[201~ markers verbatim regardless of the pane's mode, so
    // an unwrapped paste to a mode-off app is merely the (rare) status quo,
    // while failing to wrap for Claude Code (the common case, mode on but
    // unobserved) would corrupt every multi-line paste. Default to wrapping
    // until proven otherwise this connection; see pasteText below.
    let bracketedPasteObserved = false

    // Scroll-intent tracking. onScroll distinguishes the user scrolling up (which
    // must release stick mode so output stops snapping the view down) from our own
    // programmatic scroll-to-bottom (which must NOT). Every programmatic scrollTop
    // write goes through setScrollTopProgrammatic so the resulting scroll event is
    // ignored for intent; any other upward movement is treated as the user reading.
    let lastScrollTop = scrollRef.current ? scrollRef.current.scrollTop : 0
    let programmaticScroll = false
    const setScrollTopProgrammatic = (v: number) => {
      const el = scrollRef.current
      if (!el) return
      const clamped = Math.max(0, v)
      if (Math.abs(el.scrollTop - clamped) > 0.5) {
        programmaticScroll = true
        el.scrollTop = clamped
      }
    }

    // ── Render ────────────────────────────────────────────────────────────────

    function renderNow() {
      rafRef.current = null

      // Freeze the live screen while the user has scrolled up (stick released).
      // The live screen always shows tmux's CURRENT screen, so re-rendering it as
      // output streams scrolls lines off the top and overwrites whatever the user
      // is reading (scrollback refresh is paused while scrolled up, so those lines
      // aren't archived to history either). The xterm buffer keeps advancing
      // underneath; we re-render and catch up when the user scrolls back to the
      // bottom (onScroll re-stick → scheduleRender).
      if (!stickRef.current) return

      const activeBuf  = terminal.buffer.active
      const vpy        = activeBuf.viewportY
      const cell       = activeBuf.getNullCell()
      const cursorAbsY = vpy + activeBuf.cursorY

      // ── Active buffer current screen → viewportEl ─────────────────────────
      // Only the live screen is rendered from the byte stream; scrollback lives
      // in historyEl, populated from tmux (applyScrollback).
      const frag = document.createDocumentFragment()
      for (let y = vpy; y < vpy + terminal.rows; y++) {
        const cursorX = y === cursorAbsY ? activeBuf.cursorX : null
        frag.appendChild(renderRow(activeBuf.getLine(y), terminal.cols, cell, cursorX))
      }
      viewportEl.replaceChildren(frag)

      scrollSticky()
    }

    // ── tmux scrollback → historyEl ─────────────────────────────────────────────
    // Parse the captured ANSI (one pane line per text line) in a dedicated
    // headless terminal and render each line into historyEl. Consecutive captures
    // overlap heavily (tmux re-emits the same scrollback plus a little more each
    // time), so instead of rebuilding the whole history DOM every capture we
    // diff: if the previous capture's lines line up with a region of the new one,
    // we append only the genuinely-new tail line-groups. A non-overlapping
    // capture (e.g. after a clear, or a width change that reflows everything)
    // falls back to a full rebuild — rebuild is always correct, append is the
    // optimisation. Either way the reader's position is preserved when scrolled up.
    //
    // Each capture line is rendered as its own line-group <div> (holding one or
    // more wrapped rows). historyEl's children are therefore 1:1 with
    // renderedLines, which lets us append tail groups and trim whole line-groups
    // from the top for the cap — capture *lines* ≠ DOM *rows* when a line wraps.

    // Find how a new capture extends the currently-rendered lines. Content only
    // shifts UP between captures (oldest lines drop off tmux's history when it
    // saturates; new lines arrive at the bottom), so the new capture equals
    // `rendered[shift..]` followed by brand-new tail lines. We look for the
    // smallest shift (⇒ longest overlap ⇒ strongest anchor) that fully aligns.
    // Returns the tail lines to append, or null when nothing overlaps well enough
    // (→ caller rebuilds). A whitespace-only overlap is too weak to trust.
    const MIN_ANCHOR_LINES = 2
    function computeAppend(rendered: string[], next: string[]): string[] | null {
      if (rendered.length === 0) return null
      for (let shift = 0; shift < rendered.length; shift++) {
        const overlapLen = rendered.length - shift
        if (overlapLen > next.length) continue // overlap can't fit in next; try a bigger shift
        let ok = true
        let hasContent = false
        for (let k = 0; k < overlapLen; k++) {
          if (rendered[shift + k] !== next[k]) { ok = false; break }
          if (next[k].trim() !== '') hasContent = true
        }
        if (!ok) continue
        if (overlapLen < MIN_ANCHOR_LINES || !hasContent) return null // anchor too weak → rebuild
        return next.slice(overlapLen)
      }
      return null
    }

    // Render capture lines into line-group <div>s (one per line). Grouping keys
    // off xterm's isWrapped: the first row of a logical line is unwrapped, its
    // continuation rows are wrapped. Callback receives the groups (or null if the
    // render was superseded by a newer capture).
    function renderLineGroups(lines: string[], done: (groups: HTMLElement[] | null) => void) {
      sbTerm?.dispose()
      const t = new Terminal({
        cols: terminal.cols,
        // rows=1 so all content flows into scrollback; avoids the bug where
        // line-wrapping produces more rendered rows than the viewport can hold,
        // which silently drops the oldest lines from the top of history.
        rows: 1,
        scrollback: lines.length * 6 + 8,
        allowProposedApi: true,
      })
      sbTerm = t
      t.write(lines.join('\r\n'), () => {
        if (t !== sbTerm) { done(null); return } // superseded by a newer capture
        const buf = t.buffer.active
        const rcell = buf.getNullCell()
        const groups: HTMLElement[] = []
        let group: HTMLElement | null = null
        for (let y = 0; y < buf.length; y++) {
          const line = buf.getLine(y)
          if (!group || !line || !line.isWrapped) {
            group = document.createElement('div')
            groups.push(group)
          }
          group.appendChild(renderRow(line, t.cols, rcell, null))
        }
        done(groups)
      })
    }

    // Enforce the line cap by trimming whole line-groups (not raw rows) from the
    // top. When the reader is scrolled up, drop scrollTop by the removed height so
    // the content under the viewport doesn't jump. Mutates renderedLines in step.
    function trimToCap() {
      const over = renderedLines.length - MAX_SCROLLBACK_LINES.value
      if (over <= 0) return
      // Capture the pre-removal scroll offset: the browser's scroll-anchoring may
      // auto-adjust scrollTop when content above the viewport is removed, so we
      // set an ABSOLUTE target (beforeTop − removedH) afterwards rather than a
      // relative delta (which would double-count the anchoring adjustment).
      const beforeTop = scrollEl.scrollTop
      // Use a single scrollHeight delta (not a sum of per-row offsetHeights):
      // each offsetHeight is integer-rounded, so summing them accumulates a
      // several-px error over many trimmed rows and the reader drifts.
      const beforeSH = scrollEl.scrollHeight
      for (let i = 0; i < over; i++) {
        const child = historyEl.firstElementChild as HTMLElement | null
        if (!child) break
        child.remove()
      }
      renderedLines.splice(0, over)
      const removedH = beforeSH - scrollEl.scrollHeight
      if (!stickRef.current && removedH > 0) {
        setScrollTopProgrammatic(beforeTop - removedH) // hold reading position
      }
    }

    function applyScrollback(text: string) {
      if (text === lastScrollback) return // unchanged — avoid needless work
      lastScrollback = text
      const trimmed = text.replace(/\n+$/, '')
      if (!trimmed) {
        historyEl.replaceChildren()
        renderedLines = []
        // Dispose to trip any in-flight render's supersede guard — otherwise a
        // pending non-empty render callback would revive the just-cleared history.
        sbTerm?.dispose(); sbTerm = null
        return
      }
      const all = trimmed.split('\n')

      const append = computeAppend(renderedLines, all)
      if (append !== null) {
        // ── Append path: render only the new tail line-groups ────────────────
        if (append.length === 0) return // no new lines (top may have shifted; kept history unchanged)
        renderLineGroups(append, (groups) => {
          if (!groups) return // superseded
          historyEl.append(...groups)
          renderedLines = renderedLines.concat(append)
          trimToCap()
          if (stickRef.current) scrollSticky()
          // Not stuck: appends land below the viewport and don't move the reader;
          // trimToCap already compensated for any top removal.
        })
        return
      }

      // ── Rebuild path: full replace (overlap failed / first capture) ─────────
      const lines = all.length > MAX_SCROLLBACK_LINES.value ? all.slice(all.length - MAX_SCROLLBACK_LINES.value) : all
      renderLineGroups(lines, (groups) => {
        if (!groups) return // superseded
        const beforeH = historyEl.offsetHeight
        historyEl.replaceChildren(...groups)
        renderedLines = lines
        if (stickRef.current) scrollSticky()
        else setScrollTopProgrammatic(scrollEl.scrollTop + historyEl.offsetHeight - beforeH) // hold reading position
      })
    }

    // One-shot trailing timer: armed whenever an unforced call is suppressed by
    // the throttle window, so history doesn't stay stale until the next output
    // event (possibly hours after a burst ends). Cancelled/rearmed on every
    // subsequent suppressed call and cleared whenever an actual send happens.
    let trailingTimer: ReturnType<typeof setTimeout> | null = null
    function clearTrailing() {
      if (trailingTimer !== null) { clearTimeout(trailingTimer); trailingTimer = null }
    }

    function sendScrollbackRequest(now: number) {
      clearTrailing()
      lastSbRequest = now
      send({ type: 'request_scrollback', session_id: sessionId, max_lines: MAX_SCROLLBACK_LINES.value })
    }

    // Ask the daemon (via the server) for fresh scrollback. Throttled to at most
    // once per SCROLLBACK_WINDOW_MS, and only while following the bottom —
    // refreshing while the user reads history would rebuild historyEl under
    // them. `force` bypasses both gates (initial load, and the explicit
    // snapshot triggers: post-replay, post-replay retry, focus_granted,
    // post-recalcSize resize).
    function requestScrollback(force = false) {
      const now = Date.now()
      if (force) { sendScrollbackRequest(now); return }
      if (!stickRef.current) return // scrolled up: no request, no trailing schedule either
      const elapsed = now - lastSbRequest
      if (elapsed >= SCROLLBACK_WINDOW_MS.value) { sendScrollbackRequest(now); return }
      // Suppressed by the window — arm a trailing one-shot for when it expires.
      // The trailing fire re-enters UNFORCED: by fire time the window has
      // elapsed, so it sends when still stuck to the bottom but no-ops if the
      // user has scrolled up meanwhile (a forced fire would rebuild historyEl
      // under a reader — exactly what the stick gate above exists to prevent).
      // The re-stick path in onScroll catches up when they return to the bottom.
      clearTrailing()
      const remaining = SCROLLBACK_WINDOW_MS.value - elapsed
      trailingTimer = setTimeout(() => {
        trailingTimer = null
        requestScrollback()
      }, remaining)
    }

    function scheduleRender() {
      if (rafRef.current === null) rafRef.current = requestAnimationFrame(renderNow)
    }

    terminal.onWriteParsed(scheduleRender)
    // Latch bracketedPasteObserved the moment we see the mode go true from any
    // written bytes (live output or the mode_prefix preamble). Sticky for the
    // rest of the connection — once true, subsequent honest true/false reads
    // of terminal.modes.bracketedPasteMode are trusted (see pasteText).
    terminal.onWriteParsed(() => {
      if (terminal.modes.bracketedPasteMode) bracketedPasteObserved = true
    })

    // ── Scroll helpers ────────────────────────────────────────────────────────

    // When the soft keyboard is open, scrolling to the DOM bottom (scrollHeight)
    // shows the empty trailing rows of the terminal below the cursor — wrong for
    // a new session where Claude's cursor is near the top. Instead we scroll to
    // make the cursor row the last visible line. For a full TUI session (cursor
    // at the bottom row) this gives the same result as scrolling to DOM bottom.
    function scrollSticky() {
      if (!stickRef.current) return
      const t = termRef.current
      if (kbRef.current > 0 && t) {
        const buf = t.buffer.active
        const histHeight = historyEl.offsetHeight + primaryViewEl.offsetHeight
        const cursorDomBottom = histHeight + (buf.cursorY + 1) * charHRef.current
        setScrollTopProgrammatic(cursorDomBottom - scrollEl.clientHeight)
      } else {
        setScrollTopProgrammatic(scrollEl.scrollHeight)
      }
    }

    // ── WebSocket messages ────────────────────────────────────────────────────

    // onOpen fires immediately if the socket is already open, so we rely solely
    // on it for subscription — no extra send() call at mount (that would replay
    // history twice and corrupt the cursor position).
    // Whether we've received the first output chunk since subscribing.
    // We delay clearing the DOM until the first real bytes arrive so the previous
    // screen stays visible during the round-trip rather than showing black.
    let firstChunk = true

    const unsubOpen = onOpen(() => {
      firstChunk = true
      // The bracketed-paste latch is connection-scoped (see pasteText below):
      // a reconnect's first chunk calls terminal.reset(), wiping
      // terminal.modes.bracketedPasteMode, so the latch must be wiped too or
      // a stale "observed" reading survives across the reset and lets an
      // unwrapped paste through before the mode is relearned.
      bracketedPasteObserved = false
      setLoading(true)
      send({ type: 'subscribe_session', session_id: sessionId })
    })

    const unsubOutput = onMessage<SessionOutput>('session_output', (msg) => {
      if (msg.session_id !== sessionId) return
      if (firstChunk) {
        // Clear stale DOM now that fresh bytes are arriving
        historyEl.replaceChildren()
        primaryViewEl.replaceChildren()
        viewportEl.replaceChildren()
        lastScrollback = ''
        renderedLines = []   // reset diff-append state: historyEl was just wiped
        sbTerm?.dispose(); sbTerm = null // trip any in-flight render's supersede guard
        terminal.reset()
        firstChunk = false
        setLoading(false)
      }
      const binary = atob(msg.data)
      const bytes = new Uint8Array(binary.length)
      for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
      terminal.write(bytes)
      // Keep scrollback fresh as new output scrolls off the live screen — but
      // only while following the bottom, and throttled (see requestScrollback).
      requestScrollback()
    })

    // After history replay: force a PTY redraw via a double-resize (SIGWINCH
    // nudge). tmux's refresh-client then repaints the CURRENT screen at our
    // width — see refreshTmuxClient / scheduleRefresh in the daemon.
    //
    // We deliberately do NOT wipe the replayed scrollback here, even when the
    // recorded PTY was wider than ours (the device-switch case: a desktop
    // session opened on a phone, or any width change between opens). Wiping was
    // the single biggest cause of "scroll doesn't work after switching
    // mobile/desktop": it left nothing above the current screen to scroll into,
    // and on mobile it fired on essentially every open (the server often doesn't
    // report PTY cols, so the old `!ptyCols && isMobile` heuristic always hit).
    //
    // The visible area (the current screen) self-heals via the repaint below —
    // tmux refresh-client redraws every cell of the pane at the new width.
    // Older scrollback recorded at a wider width may wrap slightly, but it stays
    // readable and, crucially, scrollable. Readable history beats a blank screen.
    let histDoneRetryTimer: ReturnType<typeof setTimeout> | null = null
    const unsubHistDone = onMessage<HistoryDone>('history_done', (msg) => {
      if (msg.session_id !== sessionId) return

      const cols = terminal.cols
      const rows = terminal.rows
      send({ type: 'resize_session', session_id: sessionId, cols, rows: Math.max(1, rows - 1) })
      setTimeout(() => {
        const t = termRef.current
        if (!t) return
        send({ type: 'resize_session', session_id: sessionId, cols: t.cols, rows: t.rows })
        // Fetch the real scrollback once the resize has settled.
        requestScrollback(true)
        // One retry ~500ms after the initial post-replay snapshot: the daemon's
        // tmux refresh-client repaint debounce is 120ms, so the first capture
        // can land mid-reflow and miss the settled screen.
        if (histDoneRetryTimer !== null) clearTimeout(histDoneRetryTimer)
        histDoneRetryTimer = setTimeout(() => {
          histDoneRetryTimer = null
          requestScrollback(true)
        }, 500)
      }, 80)
    })

    // tmux scrollback snapshot → render into historyEl (the scrollable history).
    const unsubScrollback = onMessage<SessionScrollback>('session_scrollback', (msg) => {
      if (msg.session_id !== sessionId) return
      // Mode-sync preamble: escape sequences reflecting the pane's modes
      // (application cursor keys, cursor visibility) at capture time. Write it
      // into the LIVE terminal (not historyEl) before applying the scrollback
      // text, so xterm's mode state matches the pane's even though replaying
      // history alone doesn't re-derive it.
      if (msg.mode_prefix) {
        const modeBinary = atob(msg.mode_prefix)
        const modeBytes = new Uint8Array(modeBinary.length)
        for (let i = 0; i < modeBinary.length; i++) modeBytes[i] = modeBinary.charCodeAt(i)
        termRef.current?.write(modeBytes)
      }
      if (!msg.data) { applyScrollback(''); return }
      const binary = atob(msg.data)
      const bytes = new Uint8Array(binary.length)
      for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
      applyScrollback(new TextDecoder().decode(bytes))
    })

    // ── Focus-stolen / focus-granted ──────────────────────────────────────────

    const unsubFocusStolen = onMessage<FocusStolen>('focus_stolen', (msg) => {
      if (msg.session_id !== sessionId) return
      focusStolenRef.current = true  // update immediately so synchronous guards see the new value
      setFocusStolen(true)
    })

    let focusGrantedSbTimer: ReturnType<typeof setTimeout> | null = null
    const unsubFocusGranted = onMessage<FocusGranted>('focus_granted', (msg) => {
      if (msg.session_id !== sessionId) return
      focusStolenRef.current = false  // update immediately
      setFocusStolen(false)
      // Proactively re-send our size so tmux snaps back when we regain focus.
      const t = termRef.current
      if (t) send({ type: 'resize_session', session_id: sessionId, cols: t.cols, rows: t.rows })
      // The resize triggers a daemon repaint (this device may have been away
      // while another device drove a different size) — snapshot once it settles.
      if (focusGrantedSbTimer !== null) clearTimeout(focusGrantedSbTimer)
      focusGrantedSbTimer = setTimeout(() => {
        focusGrantedSbTimer = null
        requestScrollback(true)
      }, RESIZE_SNAPSHOT_DEBOUNCE_MS)
    })

    // ── Resize ────────────────────────────────────────────────────────────────

    // Single source of truth for scroll intent, covering every input method
    // (wheel, touchscreen, scrollbar, momentum) because they all produce a native
    // scroll event. Our own scroll-to-bottom is flagged via setScrollTopProgrammatic
    // and ignored, so it can never fight the user. The earlier wheel/touch gesture
    // listeners (with a 10px touch threshold) lost the race against fast output on
    // mobile and missed momentum scrolling — this is what made the view snap back
    // down while the user tried to read streaming output.
    const onScroll = () => {
      const top = scrollEl.scrollTop
      const dist = scrollEl.scrollHeight - top - scrollEl.clientHeight
      if (programmaticScroll) {
        programmaticScroll = false           // our own scroll-to-bottom — not user intent
      } else if (isAtScrollBottom(dist)) {
        // User returned to the bottom → follow output again. Re-render once to catch
        // the live screen up (it was frozen while scrolled up — see renderNow).
        if (!stickRef.current) {
          stickRef.current = true
          scheduleRender()
          // History may have gone stale while scrolled up (refresh is paused
          // then) — catch up now that we're following again. Unforced: sends
          // immediately if the window has elapsed, otherwise arms the trailing
          // repair like any other suppressed call.
          requestScrollback()
        }
      } else if (top < lastScrollTop - 1) {
        stickRef.current = false             // user scrolled up → stop following so they can read
      }
      lastScrollTop = top
    }
    scrollEl.addEventListener('scroll', onScroll, { passive: true })

    let recalcSbTimer: ReturnType<typeof setTimeout> | null = null
    function recalcSize() {
      // Don't resize PTY while the soft keyboard is open — the scroll container
      // has been shrunk to match the visual viewport, but the PTY should stay
      // at the pre-keyboard dimensions so Claude's layout doesn't reflow.
      if (kbRef.current > 0) return
      // A blurred (focus-stolen) device must not fight the active device for PTY size.
      if (focusStolenRef.current) return
      // Skip when the element isn't laid out (clientWidth/Height collapse to 0 while
      // navigating away / hidden). Otherwise gridCount falls to its floor (20x5) and
      // we'd shrink the PTY until Claude's status lines vanish → session misreads as
      // idle. The daemon also rejects such sizes; this avoids even sending them.
      if (scrollEl.clientWidth < 1 || scrollEl.clientHeight < 1) return
      const cw = charWRef.current
      const ch = charHRef.current
      const newCols = gridCount(scrollEl.clientWidth,  cw, PAD_X * 2, 20)
      const newRows = gridCount(scrollEl.clientHeight, ch, PAD_Y * 2, 5)
      if (newCols === terminal.cols && newRows === terminal.rows) return
      terminal.resize(newCols, newRows)
      send({ type: 'resize_session', session_id: sessionId, cols: newCols, rows: newRows })
      scrollSticky()
      scheduleRender()
      // The resize triggers a daemon repaint — snapshot once it settles.
      // Debounced (not per-call) so a burst of ResizeObserver firings during
      // layout settling doesn't queue a pile of requests.
      if (recalcSbTimer !== null) clearTimeout(recalcSbTimer)
      recalcSbTimer = setTimeout(() => {
        recalcSbTimer = null
        requestScrollback(true)
      }, RESIZE_SNAPSHOT_DEBOUNCE_MS)
    }

    const ro = new ResizeObserver(recalcSize)
    ro.observe(scrollEl)

    // ── Soft keyboard handling (mobile) ───────────────────────────────────────
    // SessionDetail pins the whole session container to the visual viewport
    // while the keyboard is open (see its sessionViewportBox effect), so flex
    // sizes this scroll container correctly — no per-element height override.
    // Here we only track the keyboard state: the PTY is NOT resized while the
    // keyboard is open (kbRef guards recalcSize) so Claude's layout doesn't
    // reflow, and scrollSticky pins the cursor row above the keyboard.
    const handleVVResize = () => {
      const vv = window.visualViewport
      if (!vv) return
      const rawDelta = window.innerHeight - vv.height - vv.offsetTop
      // computeKeyboardAdjustment returns 0 on desktop so that browser-chrome /
      // DevTools / pinch-zoom events never set kbRef > 0 and block PTY resizes.
      kbRef.current = computeKeyboardAdjustment(isMobileRef.current, rawDelta)
      scrollSticky()
    }
    window.visualViewport?.addEventListener('resize', handleVVResize)
    window.visualViewport?.addEventListener('scroll', handleVVResize)

    // ── Keyboard input ────────────────────────────────────────────────────────

    function sendInputSeq(data: string) {
      const bytes = new Uint8Array(data.length)
      for (let i = 0; i < data.length; i++) bytes[i] = data.charCodeAt(i) & 0xff
      send({ type: 'send_input', session_id: sessionId, data: btoa(String.fromCharCode(...bytes)) })
    }

    const onKeyDown = (e: KeyboardEvent) => {
      if (focusStolenRef.current) return  // don't inject input while another device owns the session
      if (e.isComposing) return  // skip keydown during actual CJK IME composition
      const seq = keyToSeq(e, terminal.modes.applicationCursorKeysMode)
      if (seq !== null) {
        e.preventDefault()
        sendInputSeq(seq)
      }
    }

    // Android (Gboard et al.) delivers every keystroke as insertCompositionText,
    // so we can't wait for compositionend — that would require pressing space or
    // selecting autocomplete before anything reaches the PTY.
    // Instead we stream a diff: each input event we send the characters added
    // since the last event, so typing feels immediate.
    // iOS delivers composition inputs as insertText (not insertCompositionText)
    // so it falls through to the normal data path below and is unaffected.
    let compositionText = ''

    const onInput = (e: Event) => {
      if (focusStolenRef.current) return  // don't inject input while another device owns the session
      const ie = e as InputEvent
      if (ie.inputType === 'insertCompositionText') {
        const current = taEl.value
        // Use computeCompositionDiff so that autocomplete selections which
        // replace the current word (not just extend it) are handled correctly.
        // Without this, slice(compositionText.length) sent the wrong suffix and
        // compositionEnd saw text===compositionText so skipped correction.
        const { backspaces, insert } = computeCompositionDiff(compositionText, current)
        for (let i = 0; i < backspaces; i++) sendInputSeq('\x7f')
        if (insert) sendInputSeq(insert)
        compositionText = current
        return
      }
      const data = ie.data
      if (data) {
        sendInputSeq(data)
        taEl.value = ''
      }
    }

    const onCompositionStart = () => { composing.current = true; compositionText = '' }
    const onCompositionEnd   = () => {
      if (focusStolenRef.current) return  // don't inject input while another device owns the session
      composing.current = false
      const text = taEl.value
      // If the keyboard autocorrected to something different from what we streamed,
      // send backspaces then the replacement. With autoCorrect="off" this is rare.
      if (text !== compositionText) {
        for (let i = 0; i < compositionText.length; i++) sendInputSeq('\x7f')
        if (text) sendInputSeq(text)
      }
      taEl.value = ''
      compositionText = ''
    }

    // Single paste path: honours bracketed-paste mode so the inner program can
    // tell a paste from typed input. Used by both Ctrl+V (paste event) and the
    // right-click paste below.
    const pasteText = (text: string) => {
      if (!text) return
      // Wrap when the mode reads true, OR when we've never positively observed
      // it this connection at all (bracketedPasteObserved latches true only on
      // an actual true reading — see terminal.onWriteParsed above). Only an
      // explicit off AFTER having observed on this connection skips wrapping.
      if (terminal.modes.bracketedPasteMode || !bracketedPasteObserved) {
        sendInputSeq(`\x1b[200~${text}\x1b[201~`)
      } else {
        sendInputSeq(text)
      }
    }

    // After a paste the textarea can lose keyboard focus (right-click never
    // focuses it; some browsers blur it on paste), which silently kills all
    // subsequent typing until a page reload. Refocus so input keeps flowing.
    const refocusTa = () => taEl.focus({ preventScroll: true })

    const onPaste = (e: ClipboardEvent) => {
      if (focusStolenRef.current) return  // don't inject input while another device owns the session
      e.preventDefault()
      pasteText(e.clipboardData?.getData('text') ?? '')
      refocusTa()
    }

    const onContextMenu = (e: MouseEvent) => {
      e.preventDefault()
      navigator.clipboard?.readText?.().then(text => {
        pasteText(text)
        refocusTa()
      }).catch(() => {})
    }

    // Auto-copy on selection (PuTTY/X11 style): when a drag finishes with a
    // non-empty selection inside this terminal, copy it to the clipboard. This
    // is why Ctrl+C can stay SIGINT — copy never needs a keypress. Best-effort:
    // clipboard writes reject on permission / non-secure-context, so swallow.
    const onMouseUp = () => {
      const sel = window.getSelection()
      if (!sel || sel.isCollapsed) return
      const text = sel.toString()
      if (!text) return
      // Only act when the selection actually lives in this terminal's output.
      if (!scrollEl.contains(sel.anchorNode) && !scrollEl.contains(sel.focusNode)) return
      navigator.clipboard?.writeText?.(text).catch(() => {})
    }

    taEl.addEventListener('keydown', onKeyDown)
    taEl.addEventListener('input', onInput)
    taEl.addEventListener('compositionstart', onCompositionStart)
    taEl.addEventListener('compositionend', onCompositionEnd)
    taEl.addEventListener('paste', onPaste)
    scrollEl.addEventListener('contextmenu', onContextMenu)
    // On document, not scrollEl, so a drag released just outside the terminal
    // still copies; the containment check inside keeps it scoped.
    document.addEventListener('mouseup', onMouseUp)

    const focusTa = () => taEl.focus({ preventScroll: true })
    // On mobile, never focus the textarea implicitly. Auto-focusing pops the soft
    // keyboard, but the user is often reading first (on session open) or scrolling
    // through scrollback. Only a deliberate tap (the click listener below) opens
    // the keyboard. On desktop there is no soft keyboard, so eager focus is
    // harmless and keeps the terminal ready for input — autoFocusTa focuses there.
    const autoFocusTa = () => { if (!isMobileRef.current) focusTa() }
    // Only re-focus when the tab becomes visible again (not when it hides)
    const onVisibilityChange = () => {
      if (document.visibilityState === 'visible') autoFocusTa()
    }
    // A deliberate tap focuses (and opens the keyboard) on every device. We do
    // NOT focus on touchstart: that fires when starting a scroll, which would
    // pop the keyboard while the user is just reading.
    scrollEl.addEventListener('click', focusTa)
    // Desktop cannot rely on 'click' alone: while a session is RUNNING,
    // renderNow() replaces the viewport DOM every animation frame, so the node
    // under a mousedown is torn out before mouseup and Chromium never fires
    // 'click' — the user "clicks the terminal and can't type" until output
    // stops. pointerup always fires regardless of target churn, so focus there —
    // but only when the release did NOT end a drag selection: a focused text
    // control owns the selection in Chromium, so grabbing focus after a drag
    // would empty window.getSelection() and kill copy-on-select (the native
    // blur-on-mousedown is what lets the drag select at all). Mouse only:
    // touch pointerup also fires after scroll flicks, which must not pop the
    // soft keyboard — taps keep the 'click' path above.
    const onPointerUp = (e: PointerEvent) => {
      if (e.pointerType !== 'mouse') return
      const sel = window.getSelection()
      if (sel && !sel.isCollapsed) return
      focusTa()
    }
    scrollEl.addEventListener('pointerup', onPointerUp)
    document.addEventListener('visibilitychange', onVisibilityChange)
    window.addEventListener('focus', autoFocusTa)
    autoFocusTa()
    // The launch-sheet modal (and sidebar navigation) restore focus to their own
    // trigger element as they unmount. That restoration can run AFTER this
    // synchronous focus and clobber it, leaving the terminal unfocused so the
    // user can't type or interrupt until they click/paste (this is why a
    // right-click paste "frees" the input). Re-assert on the next tick — a
    // macrotask runs after that synchronous restoration — to win the race.
    const focusSoon = window.setTimeout(autoFocusTa, 0)

    return () => {
      clearTimeout(focusSoon)
      clearTrailing()
      if (histDoneRetryTimer !== null) clearTimeout(histDoneRetryTimer)
      if (focusGrantedSbTimer !== null) clearTimeout(focusGrantedSbTimer)
      if (recalcSbTimer !== null) clearTimeout(recalcSbTimer)
      if (rafRef.current !== null) {
        cancelAnimationFrame(rafRef.current)
        // Reset to null: rafRef is a component-level ref reused across session
        // switches. Leaving a stale (cancelled) frame id here would make the next
        // session's scheduleRender think a frame is already pending, so renderNow
        // would never run and the new terminal would stay blank until a refresh.
        rafRef.current = null
      }
      taEl.removeEventListener('keydown', onKeyDown)
      taEl.removeEventListener('input', onInput)
      taEl.removeEventListener('compositionstart', onCompositionStart)
      taEl.removeEventListener('compositionend', onCompositionEnd)
      taEl.removeEventListener('paste', onPaste)
      scrollEl.removeEventListener('contextmenu', onContextMenu)
      document.removeEventListener('mouseup', onMouseUp)
      scrollEl.removeEventListener('scroll', onScroll)
      scrollEl.removeEventListener('click', focusTa)
      scrollEl.removeEventListener('pointerup', onPointerUp)
      document.removeEventListener('visibilitychange', onVisibilityChange)
      window.removeEventListener('focus', autoFocusTa)
      window.visualViewport?.removeEventListener('resize', handleVVResize)
      window.visualViewport?.removeEventListener('scroll', handleVVResize)
      scrollEl.style.height = '100%'
      unsubOpen()
      unsubOutput()
      unsubHistDone()
      unsubScrollback()
      unsubFocusStolen()
      unsubFocusGranted()
      ro.disconnect()
      send({ type: 'unsubscribe_session', session_id: sessionId })
      sbTerm?.dispose()
      sbTerm = null
      terminal.dispose()
      termRef.current = null
    }
  }, [sessionId])

  return (
    <div style={{ position: 'relative', width: '100%', height: '100%' }}>
      {/* Hidden textarea — positioned off-screen so iOS doesn't scroll the page
          when it appears. fontSize 16 prevents iOS auto-zoom on focus. */}
      <textarea
        ref={taRef}
        aria-hidden="true"
        autoComplete="off"
        autoCorrect="off"
        autoCapitalize="none"
        spellCheck={false}
        style={{ position: 'fixed', top: -9999, left: -9999, width: 1, height: 1, opacity: 0, fontSize: 16 }}
      />
      {/* Scrollable terminal output area — native scroll works on mobile */}
      <div
        ref={scrollRef}
        style={{
          width: '100%',
          height: '100%',
          overflowY: 'auto',
          overflowX: 'hidden',
          overscrollBehavior: 'contain',
          touchAction: 'pan-y',
          background: BG,
          fontFamily: 'monospace',
          fontSize: 13,
          lineHeight: '1.2em',
          padding: `${PAD_Y}px ${PAD_X}px`,
          boxSizing: 'border-box',
          WebkitOverflowScrolling: 'touch',
          color: FG,
          ...(focusStolen ? { filter: 'blur(3px)', pointerEvents: 'none' } : {}),
        } as React.CSSProperties}
      >
        <div ref={historyRef} />
        <div ref={primaryViewRef} />
        <div ref={viewportRef} />
      </div>
      {loading && (
        <div style={{
          position: 'absolute', inset: 0,
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          background: 'rgba(5, 4, 3, 0.55)',
          pointerEvents: 'none',
        }}>
          <div className="terminal-loading-spinner" />
        </div>
      )}
      {/* Reload re-triggers subscribe_session, which re-asserts this device as active on the server. */}
      {focusStolen && (
        <div style={{
          position: 'absolute', inset: 0,
          display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center',
          background: 'rgba(5, 4, 3, 0.82)',
          zIndex: 10,
        }}>
          <div style={{ margin: '0 0 8px', fontSize: 16, fontFamily: 'monospace', color: FG, fontWeight: 600 }}>
            Focus taken by another device
          </div>
          <p style={{ margin: '0 0 20px', fontSize: 13, fontFamily: 'monospace', color: FG, opacity: 0.65 }}>
            Refresh to steal it back.
          </p>
          <button
            onClick={() => location.reload()}
            style={{
              fontFamily: 'monospace', fontSize: 13,
              padding: '6px 18px',
              background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))', color: FG,
              border: '1px solid color-mix(in srgb, var(--amber) 55%, var(--stone))', borderRadius: 3,
              cursor: 'pointer',
            }}
          >
            Reclaim
          </button>
        </div>
      )}
    </div>
  )
}
