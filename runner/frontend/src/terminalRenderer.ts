// Pure rendering functions extracted from TerminalDOMView so they can be
// unit-tested without React or a live WebSocket.
import { arrowSeq } from './terminalKeys'

// ── Scrollback snapshot timing ─────────────────────────────────────────────────

// Trailing-edge repair window for tmux scrollback snapshot requests (ms).
// Mutable so browser tests can shrink it instead of waiting 30 real seconds —
// production code always starts at the default below. Lives here (rather than
// in TerminalDOMView.tsx) because that file may only export the component
// (react-refresh/only-export-components).
export const SCROLLBACK_WINDOW_MS = { value: 30_000 }

// Maximum number of tmux capture *lines* retained in the client scrollback. Also
// the `max_lines` asked of the daemon. Mutable (like SCROLLBACK_WINDOW_MS) so
// browser tests can shrink it to exercise the top-trim cap without emitting
// thousands of lines — production always starts at the default. Lives here (not
// in TerminalDOMView.tsx) because that file may only export the component
// (react-refresh/only-export-components).
export const MAX_SCROLLBACK_LINES = { value: 4000 }

// ── Color theme ───────────────────────────────────────────────────────────────
// Kept as literal hex (not CSS var()) — these values get assigned straight to
// element.style / read back and compared in tests and, via ANSI escape codes,
// they must resolve to real colors independent of any stylesheet cascade.
// Retheme by hand here to match brand/tokens.css instead of aliasing it.

export const BG = '#16181a'   // --basalt
export const FG = '#e8ecef'   // --chalk
export const CURSOR_BG = '#e8622c' // --blaze

/** 16-colour ANSI palette matching the Blerg brand theme. */
export const ANSI16: readonly string[] = [
  /* 0  black   */ '#1f2327', // --scree
  /* 1  red     */ '#cf5d5d', // --danger
  /* 2  green   */ '#8aa864', // --lichen
  /* 3  yellow  */ '#d9a441', // --amber
  /* 4  blue    */ '#3987e5', // --batch
  /* 5  magenta */ '#a8698a',
  /* 6  cyan    */ '#4a9aa8',
  /* 7  white   */ '#e8ecef', // --chalk
  /* 8  bright black   */ '#7c858d', // --fog-dim
  /* 9  bright red     */ '#e07a7a',
  /* 10 bright green   */ '#a8c98a',
  /* 11 bright yellow  */ '#e8bc6a',
  /* 12 bright blue    */ '#6aa8ec',
  /* 13 bright magenta */ '#c48ab0',
  /* 14 bright cyan    */ '#7ac0c8',
  /* 15 bright white   */ '#f5f7f8',
]

/** Build the full xterm 256-colour palette: 16 ANSI + 6³ colour cube + 24 grayscale. */
export function buildPalette(): string[] {
  const p: string[] = [...ANSI16]
  // 6×6×6 colour cube (indices 16–231)
  for (let r = 0; r < 6; r++)
    for (let g = 0; g < 6; g++)
      for (let b = 0; b < 6; b++)
        p.push(`rgb(${r ? r * 40 + 55 : 0},${g ? g * 40 + 55 : 0},${b ? b * 40 + 55 : 0})`)
  // Grayscale ramp (indices 232–255)
  for (let i = 0; i < 24; i++) {
    const v = i * 10 + 8
    p.push(`rgb(${v},${v},${v})`)
  }
  return p
}

/** Full 256-colour palette, built once at module load. */
export const PAL: readonly string[] = buildPalette()

// ── Cell interface (subset of xterm IBufferCell we actually use) ──────────────
// We keep this minimal so tests can supply plain JS objects as mock cells.

export interface BufferCell {
  getChars(): string
  getWidth(): number
  isFgDefault(): boolean
  isFgPalette(): boolean
  isFgRGB(): boolean
  getFgColor(): number
  isBgDefault(): boolean
  isBgPalette(): boolean
  isBgRGB(): boolean
  getBgColor(): number
  isBold(): number
  isItalic(): number
  isUnderline(): number
  isDim(): number
  isInverse(): number
  isStrikethrough(): number
  isInvisible(): number
}

export interface BufferLine {
  getCell(x: number, reuse?: BufferCell): BufferCell | undefined
}

// ── renderRow ─────────────────────────────────────────────────────────────────

/**
 * Convert one line of the xterm buffer into a styled DOM element.
 *
 * @param line    Buffer line (undefined → return empty row)
 * @param cols    Terminal column count
 * @param cell    Reusable cell object (mutated by getCell to avoid allocations)
 * @param cursorX Column index of the cursor on this row, or null if cursor is elsewhere
 */
export function renderRow(
  line: BufferLine | undefined,
  cols: number,
  cell: BufferCell,
  cursorX: number | null,
): HTMLElement {
  const row = document.createElement('div')
  row.style.cssText = 'min-height:1.2em;white-space:pre'
  if (!line) return row

  let lastCss = '\0' // sentinel: force a new span on the first cell
  let span: HTMLSpanElement | null = null

  // Find the last column worth rendering. Emitting a character for every column
  // pads short lines with trailing spaces that are invisible but SELECTABLE, so
  // a drag crossing a row boundary sweeps through them and highlights the row
  // edge-to-edge (and copies the padding). Anything visible keeps its cell: a
  // non-blank glyph, a background colour, inverse video, or a text decoration.
  // The cursor also anchors the scan so it still renders past end-of-line.
  let end = cursorX ?? -1
  for (let x = cols - 1; x > end; x--) {
    line.getCell(x, cell)
    const ch = cell.getChars()
    if (
      (ch !== '' && ch !== ' ') ||
      !cell.isBgDefault() ||
      cell.isInverse() ||
      cell.isUnderline() ||
      cell.isStrikethrough()
    ) {
      end = x
      break
    }
  }

  for (let x = 0; x <= end; x++) {
    line.getCell(x, cell)
    if (cell.getWidth() === 0) continue // skip wide-char continuation cells

    const ch = cell.getChars() || ' '
    const isCursor = x === cursorX

    // ── foreground ────────────────────────────────────────────────────────────
    let fg = FG
    if (!cell.isFgDefault()) {
      if (cell.isFgRGB()) {
        const c = cell.getFgColor()
        fg = `rgb(${(c >> 16) & 0xff},${(c >> 8) & 0xff},${c & 0xff})`
      } else if (cell.isFgPalette()) {
        const idx = cell.getFgColor()
        // Bold + palette colour 0-7 → use the bright variant (colour intensification)
        fg = (cell.isBold() && idx < 8) ? ANSI16[idx + 8] : (PAL[idx] ?? FG)
      }
    }

    // ── background ───────────────────────────────────────────────────────────
    let bg: string | null = null
    if (!cell.isBgDefault()) {
      if (cell.isBgRGB()) {
        const c = cell.getBgColor()
        bg = `rgb(${(c >> 16) & 0xff},${(c >> 8) & 0xff},${c & 0xff})`
      } else if (cell.isBgPalette()) {
        bg = PAL[cell.getBgColor()] ?? null
      }
    }

    if (cell.isInverse()) {
      const tmp = fg
      fg = bg ?? BG
      bg = tmp
    }

    // ── build CSS string ──────────────────────────────────────────────────────
    let css: string
    if (isCursor) {
      css = `color:${bg ?? BG};background:${CURSOR_BG}`
    } else {
      css = ''
      if (fg !== FG)        css += `color:${fg};`
      if (bg)               css += `background:${bg};`
      if (cell.isBold())    css += 'font-weight:bold;'
      if (cell.isItalic())  css += 'font-style:italic;'
      const ul = cell.isUnderline(), st = cell.isStrikethrough()
      if (ul && st)         css += 'text-decoration:underline line-through;'
      else if (ul)          css += 'text-decoration:underline;'
      else if (st)          css += 'text-decoration:line-through;'
      if (cell.isDim())     css += 'opacity:0.6;'
      if (cell.isInvisible()) css += 'opacity:0;'
    }

    // Start a new span whenever style changes, or right after the cursor cell
    // (the cursor cell always gets its own span so neighbours don't inherit it)
    if (css !== lastCss) {
      span = document.createElement('span')
      if (css) span.style.cssText = css
      row.appendChild(span)
      lastCss = css
    }
    span!.textContent += ch
    if (isCursor) lastCss = '\0' // force new span after cursor
  }

  return row
}

// ── keyToSeq ──────────────────────────────────────────────────────────────────

/**
 * Map a keyboard event to the escape sequence that should be sent to the PTY.
 * Returns null for ordinary printable keys (let the input event handle them).
 */
export function keyToSeq(e: KeyboardEvent, appCursor: boolean): string | null {
  // Ctrl+letter → control byte (e.g. Ctrl+C = \x03). Ctrl+V is carved out and
  // returns null so the browser's native paste event fires (see onPaste); the
  // ^V control byte (quoted-insert) is sacrificed for clipboard paste.
  if (e.ctrlKey && !e.altKey && !e.metaKey && e.key.length === 1) {
    if (e.key.toUpperCase() === 'V') return null
    const code = e.key.toUpperCase().charCodeAt(0)
    if (code >= 64 && code <= 95) return String.fromCharCode(code - 64)
  }

  switch (e.key) {
    case 'Enter':      return '\r'
    case 'Backspace':  return '\x7f'
    case 'Delete':     return '\x1b[3~'
    case 'Tab':        return e.shiftKey ? '\x1b[Z' : '\t'
    case 'Escape':     return '\x1b'
    case 'ArrowUp':    return arrowSeq('up',    appCursor)
    case 'ArrowDown':  return arrowSeq('down',  appCursor)
    case 'ArrowLeft':  return arrowSeq('left',  appCursor)
    case 'ArrowRight': return arrowSeq('right', appCursor)
    case 'PageUp':     return '\x1b[5~'
    case 'PageDown':   return '\x1b[6~'
    case 'Home':       return appCursor ? '\x1bOH' : '\x1b[H'
    case 'End':        return appCursor ? '\x1bOF' : '\x1b[F'
    case 'F1':         return '\x1bOP'
    case 'F2':         return '\x1bOQ'
    case 'F3':         return '\x1bOR'
    case 'F4':         return '\x1bOS'
    case 'F5':         return '\x1b[15~'
    case 'F6':         return '\x1b[17~'
    case 'F7':         return '\x1b[18~'
    case 'F8':         return '\x1b[19~'
    case 'F9':         return '\x1b[20~'
    case 'F10':        return '\x1b[21~'
    case 'F11':        return '\x1b[23~'
    case 'F12':        return '\x1b[24~'
    default:           return null
  }
}

// ── Scroll stick-mode helpers ─────────────────────────────────────────────────
// Pure functions so they can be unit-tested independently of the component.

/** True only when scroll container is at the very bottom (≤1px tolerance). */
export function isAtScrollBottom(dist: number): boolean {
  return dist <= 1
}

/** True when a WheelEvent deltaY means the user is scrolling up to view history. */
export function wheelScrollsUp(deltaY: number): boolean {
  return deltaY < 0
}

/** True when a touch finger-movement delta means scrolling up.
 *  On mobile, finger moves DOWN (positive dy) to reveal older content.
 *  Requires > 10px to suppress micro-movement false positives.
 *
 *  IMPORTANT: the caller MUST pass the CUMULATIVE delta from touchstart, not
 *  the per-event incremental delta.  Per-event deltas are typically 2-4 px
 *  and would never exceed the threshold even on a long swipe. */
export function touchScrollsUp(fingerDeltaY: number): boolean {
  return fingerDeltaY > 10
}

/** Computes what to send to the PTY when an IME composition text changes.
 *  If the new text is an extension of the old text (common case: each keystroke
 *  appends one char), only the new suffix is sent — no backspaces needed.
 *  If the text was replaced (autocomplete chose a different word), we backspace
 *  the entire previous value and send the new one from scratch. */
export function computeCompositionDiff(
  prevText: string,
  nextText: string,
): { backspaces: number; insert: string } {
  if (nextText.startsWith(prevText)) {
    return { backspaces: 0, insert: nextText.slice(prevText.length) }
  }
  return { backspaces: prevText.length, insert: nextText }
}

/** True when the primary buffer has scrollback lines that are worth rendering.
 *  When Claude enters alt screen immediately (no primary output), primaryVpy = 0
 *  and the primary buffer viewport is all blank rows.  Rendering those blank rows
 *  gives the scroll container height with no visible text — the "scrolls but nothing
 *  shows" bug.  Only render the primary viewport snapshot when real scrollback exists. */
export function shouldRenderPrimaryView(primaryVpy: number): boolean {
  return primaryVpy > 0
}

/** Returns the effective soft-keyboard height (px) to account for in layout.
 *  Returns 0 on desktop — keyboard adjustment is only meaningful on mobile.
 *  This keeps desktop resize/DevTools events from incorrectly triggering the
 *  mobile keyboard path. */
export function computeKeyboardAdjustment(isMobile: boolean, rawDeltaPx: number): number {
  if (!isMobile) return 0
  return Math.max(0, rawDeltaPx)
}

/** Box the mobile session container should occupy so the whole column (header,
 *  terminal, key bar) fits ABOVE the soft keyboard. Android keeps the layout
 *  viewport full-height when the keyboard opens (only the visual viewport
 *  shrinks), so a `position:fixed; inset:0` container leaves the key bar and
 *  the terminal's bottom rows behind the keyboard. Returns the visual-viewport
 *  box to pin the container to while the keyboard is open, or null when the
 *  container should keep its natural inset:0 (desktop, or keyboard closed). */
export function sessionViewportBox(
  isMobile: boolean, innerHeight: number, vvHeight: number, vvOffsetTop: number,
): { top: number; height: number } | null {
  const kb = computeKeyboardAdjustment(isMobile, innerHeight - vvHeight - vvOffsetTop)
  if (kb <= 0) return null
  return { top: vvOffsetTop, height: vvHeight }
}

/** Derive a terminal grid count (cols or rows) from the scroll container.
 *
 *  The scroll container uses `box-sizing: border-box` with padding, so its
 *  `clientWidth`/`clientHeight` INCLUDE that padding even though terminal cells
 *  only flow inside the content box. Dividing the raw client size by the cell
 *  size therefore over-counts by `floor(padding / cellPx)` ≈ 1-2 cells, and
 *  those phantom cells render past the content box and get clipped by overflow
 *  (the right-edge cut-off bug). Subtract the total padding for that axis first.
 *
 *  @param clientPx   container clientWidth (for cols) or clientHeight (for rows)
 *  @param cellPx     measured monospace cell width (for cols) or height (for rows)
 *  @param paddingPx  TOTAL padding on that axis (both sides summed)
 *  @param min        floor for the result so a tiny container still yields a usable grid
 */
export function gridCount(clientPx: number, cellPx: number, paddingPx: number, min: number): number {
  return Math.max(min, Math.floor((clientPx - paddingPx) / cellPx))
}
