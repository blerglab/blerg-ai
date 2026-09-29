import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { Terminal } from '@xterm/xterm'
import {
  BG, CURSOR_BG, ANSI16, PAL,
  buildPalette, renderRow, keyToSeq,
  isAtScrollBottom, wheelScrollsUp, touchScrollsUp, computeKeyboardAdjustment, sessionViewportBox,
  computeCompositionDiff, shouldRenderPrimaryView, gridCount,
  type BufferCell, type BufferLine,
} from './terminalRenderer'

// ── Colour normalisation ──────────────────────────────────────────────────────
// jsdom normalises CSS colours to rgb(r, g, b) with spaces when read back from
// element.style.*. Our colour strings may be hex (#rrggbb) or compact rgb
// (rgb(r,g,b) without spaces). This helper converts both to the same canonical
// form so test assertions don't depend on serialisation details.

function toRgb(color: string): string {
  if (color.startsWith('#')) {
    const r = parseInt(color.slice(1, 3), 16)
    const g = parseInt(color.slice(3, 5), 16)
    const b = parseInt(color.slice(5, 7), 16)
    return `rgb(${r}, ${g}, ${b})`
  }
  // Already rgb(...) — normalise spacing
  return color.replace(/rgb\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*\)/, (_, r, g, b) =>
    `rgb(${r}, ${g}, ${b})`,
  )
}

// ── Mock cell / line factories ────────────────────────────────────────────────
// These replicate the xterm IBufferCell / IBufferLine interfaces so tests can
// exercise the renderer without a real xterm terminal instance.

interface MockCellDef {
  ch?: string
  width?: number
  fgPalette?: boolean; fgRGB?: boolean; fgColor?: number
  bgPalette?: boolean; bgRGB?: boolean; bgColor?: number
  bold?: boolean; italic?: boolean; underline?: boolean
  dim?: boolean; inverse?: boolean; strikethrough?: boolean; invisible?: boolean
}

function applyDef(c: BufferCell & Record<string, unknown>, d: MockCellDef) {
  c.getChars      = () => d.ch ?? ' '
  c.getWidth      = () => d.width ?? 1
  c.isFgDefault   = () => !(d.fgPalette || d.fgRGB)
  c.isFgPalette   = () => d.fgPalette ?? false
  c.isFgRGB       = () => d.fgRGB ?? false
  c.getFgColor    = () => d.fgColor ?? 0
  c.isBgDefault   = () => !(d.bgPalette || d.bgRGB)
  c.isBgPalette   = () => d.bgPalette ?? false
  c.isBgRGB       = () => d.bgRGB ?? false
  c.getBgColor    = () => d.bgColor ?? 0
  c.isBold        = () => d.bold        ? 1 : 0
  c.isItalic      = () => d.italic      ? 1 : 0
  c.isUnderline   = () => d.underline   ? 1 : 0
  c.isDim         = () => d.dim         ? 1 : 0
  c.isInverse     = () => d.inverse     ? 1 : 0
  c.isStrikethrough = () => d.strikethrough ? 1 : 0
  c.isInvisible   = () => d.invisible   ? 1 : 0
}

function makeNullCell(): BufferCell {
  const c = {} as BufferCell & Record<string, unknown>
  applyDef(c, {})
  return c
}

function makeLine(defs: (MockCellDef | null)[]): BufferLine {
  return {
    getCell(x, reuse) {
      const def = (x < defs.length ? defs[x] : null) ?? {}
      if (reuse) { applyDef(reuse as BufferCell & Record<string, unknown>, def); return reuse }
      const c = {} as BufferCell & Record<string, unknown>
      applyDef(c, def)
      return c
    },
  }
}

function makeKey(key: string, mods: { ctrl?: boolean; shift?: boolean; alt?: boolean; meta?: boolean } = {}): KeyboardEvent {
  return new KeyboardEvent('keydown', {
    key,
    ctrlKey:  mods.ctrl  ?? false,
    shiftKey: mods.shift ?? false,
    altKey:   mods.alt   ?? false,
    metaKey:  mods.meta  ?? false,
  })
}

// ── buildPalette ──────────────────────────────────────────────────────────────

describe('buildPalette', () => {
  it('produces exactly 256 entries', () => {
    expect(buildPalette()).toHaveLength(256)
  })

  it('starts with the 16 ANSI theme colours', () => {
    const p = buildPalette()
    for (let i = 0; i < 16; i++) expect(p[i]).toBe(ANSI16[i])
  })

  it('colour cube entry 16 is black (0,0,0)', () => {
    expect(PAL[16]).toBe('rgb(0,0,0)')
  })

  it('colour cube entry 17 is dark blue (0,0,95)', () => {
    expect(PAL[17]).toBe('rgb(0,0,95)')
  })

  it('colour cube entry 231 is white (255,255,255)', () => {
    expect(PAL[231]).toBe('rgb(255,255,255)')
  })

  it('grayscale entry 232 is nearly black (8,8,8)', () => {
    expect(PAL[232]).toBe('rgb(8,8,8)')
  })

  it('grayscale entry 255 is nearly white (238,238,238)', () => {
    expect(PAL[255]).toBe('rgb(238,238,238)')
  })
})

// ── keyToSeq ──────────────────────────────────────────────────────────────────

describe('keyToSeq — special keys', () => {
  it('Enter → \\r', () => expect(keyToSeq(makeKey('Enter'), false)).toBe('\r'))
  it('Backspace → DEL (\\x7f)', () => expect(keyToSeq(makeKey('Backspace'), false)).toBe('\x7f'))
  it('Delete → \\x1b[3~', () => expect(keyToSeq(makeKey('Delete'), false)).toBe('\x1b[3~'))
  it('Tab → \\t', () => expect(keyToSeq(makeKey('Tab'), false)).toBe('\t'))
  it('Shift+Tab → \\x1b[Z', () => expect(keyToSeq(makeKey('Tab', { shift: true }), false)).toBe('\x1b[Z'))
  it('Escape → \\x1b', () => expect(keyToSeq(makeKey('Escape'), false)).toBe('\x1b'))
  it('PageUp → \\x1b[5~', () => expect(keyToSeq(makeKey('PageUp'), false)).toBe('\x1b[5~'))
  it('PageDown → \\x1b[6~', () => expect(keyToSeq(makeKey('PageDown'), false)).toBe('\x1b[6~'))
})

describe('keyToSeq — arrow keys in normal cursor mode', () => {
  it('ArrowUp → CSI A', () => expect(keyToSeq(makeKey('ArrowUp'), false)).toBe('\x1b[A'))
  it('ArrowDown → CSI B', () => expect(keyToSeq(makeKey('ArrowDown'), false)).toBe('\x1b[B'))
  it('ArrowRight → CSI C', () => expect(keyToSeq(makeKey('ArrowRight'), false)).toBe('\x1b[C'))
  it('ArrowLeft → CSI D', () => expect(keyToSeq(makeKey('ArrowLeft'), false)).toBe('\x1b[D'))
})

describe('keyToSeq — arrow keys in application cursor mode', () => {
  it('ArrowUp → SS3 A', () => expect(keyToSeq(makeKey('ArrowUp'), true)).toBe('\x1bOA'))
  it('ArrowDown → SS3 B', () => expect(keyToSeq(makeKey('ArrowDown'), true)).toBe('\x1bOB'))
  it('ArrowRight → SS3 C', () => expect(keyToSeq(makeKey('ArrowRight'), true)).toBe('\x1bOC'))
  it('ArrowLeft → SS3 D', () => expect(keyToSeq(makeKey('ArrowLeft'), true)).toBe('\x1bOD'))
})

describe('keyToSeq — Home / End', () => {
  it('Home normal mode → CSI H', () => expect(keyToSeq(makeKey('Home'), false)).toBe('\x1b[H'))
  it('End normal mode → CSI F', () => expect(keyToSeq(makeKey('End'), false)).toBe('\x1b[F'))
  it('Home app mode → SS3 H', () => expect(keyToSeq(makeKey('Home'), true)).toBe('\x1bOH'))
  it('End app mode → SS3 F', () => expect(keyToSeq(makeKey('End'), true)).toBe('\x1bOF'))
})

describe('keyToSeq — F-keys', () => {
  const cases: [string, string][] = [
    ['F1',  '\x1bOP'],  ['F2',  '\x1bOQ'],  ['F3',  '\x1bOR'],  ['F4',  '\x1bOS'],
    ['F5',  '\x1b[15~'],['F6',  '\x1b[17~'],['F7',  '\x1b[18~'],['F8',  '\x1b[19~'],
    ['F9',  '\x1b[20~'],['F10', '\x1b[21~'],['F11', '\x1b[23~'],['F12', '\x1b[24~'],
  ]
  for (const [key, seq] of cases) {
    it(`${key} → ${JSON.stringify(seq)}`, () => expect(keyToSeq(makeKey(key), false)).toBe(seq))
  }
})

describe('keyToSeq — Ctrl+letter sequences', () => {
  it('Ctrl+C → ETX (\\x03)', () => expect(keyToSeq(makeKey('c', { ctrl: true }), false)).toBe('\x03'))
  it('Ctrl+D → EOT (\\x04)', () => expect(keyToSeq(makeKey('d', { ctrl: true }), false)).toBe('\x04'))
  it('Ctrl+Z → SUB (\\x1a)', () => expect(keyToSeq(makeKey('z', { ctrl: true }), false)).toBe('\x1a'))
  it('Ctrl+L → FF  (\\x0c)', () => expect(keyToSeq(makeKey('l', { ctrl: true }), false)).toBe('\x0c'))
  it('Ctrl+A → SOH (\\x01)', () => expect(keyToSeq(makeKey('a', { ctrl: true }), false)).toBe('\x01'))
  it('Ctrl+E → ENQ (\\x05)', () => expect(keyToSeq(makeKey('e', { ctrl: true }), false)).toBe('\x05'))
  it('Ctrl+U → NAK (\\x15)', () => expect(keyToSeq(makeKey('u', { ctrl: true }), false)).toBe('\x15'))
  it('Ctrl+W → ETB (\\x17)', () => expect(keyToSeq(makeKey('w', { ctrl: true }), false)).toBe('\x17'))
  it('uppercase key also maps (Ctrl+C uppercase)', () => expect(keyToSeq(makeKey('C', { ctrl: true }), false)).toBe('\x03'))
  // Ctrl+V is reserved for clipboard paste: returning null lets the browser's
  // native paste event fire instead of sending ^V (\x16) to the PTY.
  it('Ctrl+V → null (reserved for paste)', () => expect(keyToSeq(makeKey('v', { ctrl: true }), false)).toBeNull())
  it('Ctrl+V uppercase → null', () => expect(keyToSeq(makeKey('V', { ctrl: true }), false)).toBeNull())
  it('Ctrl+C still maps after Ctrl+V carve-out', () => expect(keyToSeq(makeKey('c', { ctrl: true }), false)).toBe('\x03'))
})

describe('keyToSeq — printable keys return null (handled by input event)', () => {
  it('plain letter a', () => expect(keyToSeq(makeKey('a'), false)).toBeNull())
  it('plain letter Z', () => expect(keyToSeq(makeKey('Z'), false)).toBeNull())
  it('digit 5',        () => expect(keyToSeq(makeKey('5'), false)).toBeNull())
  it('space',          () => expect(keyToSeq(makeKey(' '), false)).toBeNull())
  it('period',         () => expect(keyToSeq(makeKey('.'), false)).toBeNull())
})

// ── renderRow ────────────────────────────────────────────────────────────────

describe('renderRow — structure', () => {
  it('returns a <div> with white-space:pre', () => {
    const row = renderRow(undefined, 10, makeNullCell(), null)
    expect(row.tagName).toBe('DIV')
    expect(row.style.whiteSpace).toBe('pre')
  })

  it('returns empty row for undefined line', () => {
    const row = renderRow(undefined, 10, makeNullCell(), null)
    expect(row.children).toHaveLength(0)
  })

  it('renders plain text as a single span', () => {
    const line = makeLine([{ ch: 'H' }, { ch: 'i' }])
    const row = renderRow(line, 2, makeNullCell(), null)
    expect(row.textContent).toBe('Hi')
  })

  it('skips wide-character continuation cells (width=0)', () => {
    const line = makeLine([{ ch: '日', width: 2 }, { ch: '', width: 0 }])
    const row = renderRow(line, 2, makeNullCell(), null)
    expect(row.textContent).toBe('日')
  })

  it('uses a space for empty cells between text', () => {
    const line = makeLine([{ ch: 'a' }, { ch: '' }, { ch: 'b' }])
    const row = renderRow(line, 3, makeNullCell(), null)
    expect(row.textContent).toBe('a b')
  })
})

// ── Trailing blank padding ───────────────────────────────────────────────────
// Every row used to emit one character per COLUMN, padding short lines with
// spaces out to the full terminal width. Those trailing spaces are invisible but
// selectable, so a mouse drag crossing a row boundary ran through all of them:
// dragging from the middle of one line to the middle of the next highlighted
// both rows edge-to-edge ("selects the whole frame") and copied ~50 characters
// of padding instead of the handful the user dragged over.
describe('renderRow — trailing blank cells are not rendered', () => {
  it('does not pad a short line out to the column count', () => {
    const line = makeLine([{ ch: 'H' }, { ch: 'i' }])
    const row = renderRow(line, 50, makeNullCell(), null)
    expect(row.textContent).toBe('Hi')
  })

  it('renders a fully blank line as an empty row', () => {
    const row = renderRow(makeLine([]), 50, makeNullCell(), null)
    expect(row.textContent).toBe('')
  })

  it('keeps interior blanks, trims only the trailing run', () => {
    const line = makeLine([{ ch: 'a' }, { ch: '' }, { ch: 'b' }])
    const row = renderRow(line, 50, makeNullCell(), null)
    expect(row.textContent).toBe('a b')
  })

  it('keeps trailing blanks that carry a background colour (they are visible)', () => {
    // A blank cell with a background paints a coloured block — trimming it would
    // visibly truncate things like a selected menu row or a progress bar.
    const line = makeLine([{ ch: 'a' }, { ch: ' ', bgPalette: true, bgColor: 4 }])
    const row = renderRow(line, 50, makeNullCell(), null)
    expect(row.textContent).toBe('a ')
  })

  it('keeps trailing blanks up to the cursor so the cursor still renders', () => {
    const line = makeLine([{ ch: 'H' }, { ch: 'i' }])
    const row = renderRow(line, 50, makeNullCell(), 5)
    expect(row.textContent).toBe('Hi    ')
    // The cursor cell gets its own span with the cursor background.
    const last = row.children[row.children.length - 1] as HTMLSpanElement
    expect(toRgb(last.style.background)).toBe(toRgb(CURSOR_BG))
  })
})

describe('renderRow — colour rendering', () => {
  it('applies no colour style for default-fg cell', () => {
    const line = makeLine([{ ch: 'A' }])
    const row = renderRow(line, 1, makeNullCell(), null)
    const span = row.children[0] as HTMLSpanElement
    expect(span.style.color).toBe('')
  })

  it('applies ANSI palette colour 1 (red)', () => {
    const line = makeLine([{ ch: 'R', fgPalette: true, fgColor: 1 }])
    const row = renderRow(line, 1, makeNullCell(), null)
    const span = row.children[0] as HTMLSpanElement
    expect(toRgb(span.style.color)).toBe(toRgb(ANSI16[1]))
  })

  it('applies palette colour 2 (green)', () => {
    const line = makeLine([{ ch: 'G', fgPalette: true, fgColor: 2 }])
    const row = renderRow(line, 1, makeNullCell(), null)
    const span = row.children[0] as HTMLSpanElement
    expect(toRgb(span.style.color)).toBe(toRgb(ANSI16[2]))
  })

  it('bold + ANSI colour 1 uses bright-red (index 9)', () => {
    const line = makeLine([{ ch: 'B', fgPalette: true, fgColor: 1, bold: true }])
    const row = renderRow(line, 1, makeNullCell(), null)
    const span = row.children[0] as HTMLSpanElement
    expect(toRgb(span.style.color)).toBe(toRgb(ANSI16[9]))
  })

  it('bold + ANSI colour 8 (already bright) stays at index 8', () => {
    const line = makeLine([{ ch: 'X', fgPalette: true, fgColor: 8, bold: true }])
    const row = renderRow(line, 1, makeNullCell(), null)
    const span = row.children[0] as HTMLSpanElement
    expect(toRgb(span.style.color)).toBe(toRgb(ANSI16[8]))
  })

  it('applies 256-colour palette (index 196 = bright red in cube)', () => {
    const line = makeLine([{ ch: 'C', fgPalette: true, fgColor: 196 }])
    const row = renderRow(line, 1, makeNullCell(), null)
    const span = row.children[0] as HTMLSpanElement
    expect(toRgb(span.style.color)).toBe(toRgb(PAL[196]))
  })

  it('applies RGB foreground colour', () => {
    const packed = (0xff << 16) | (0x80 << 8) | 0x00 // #ff8000 orange
    const line = makeLine([{ ch: 'O', fgRGB: true, fgColor: packed }])
    const row = renderRow(line, 1, makeNullCell(), null)
    const span = row.children[0] as HTMLSpanElement
    expect(toRgb(span.style.color)).toBe('rgb(255, 128, 0)')
  })

  it('applies background palette colour', () => {
    const line = makeLine([{ ch: 'B', bgPalette: true, bgColor: 4 }])
    const row = renderRow(line, 1, makeNullCell(), null)
    const span = row.children[0] as HTMLSpanElement
    expect(toRgb(span.style.backgroundColor)).toBe(toRgb(ANSI16[4]))
  })

  it('applies RGB background colour', () => {
    const packed = (0x00 << 16) | (0x80 << 8) | 0xff
    const line = makeLine([{ ch: 'X', bgRGB: true, bgColor: packed }])
    const row = renderRow(line, 1, makeNullCell(), null)
    const span = row.children[0] as HTMLSpanElement
    expect(toRgb(span.style.backgroundColor)).toBe('rgb(0, 128, 255)')
  })

  it('inverse swaps fg/bg (default fg becomes bg)', () => {
    const line = makeLine([{ ch: 'I', fgPalette: true, fgColor: 2, inverse: true }])
    const row = renderRow(line, 1, makeNullCell(), null)
    const span = row.children[0] as HTMLSpanElement
    // fg was ANSI16[2]; after inverse it becomes the background
    expect(toRgb(span.style.backgroundColor)).toBe(toRgb(ANSI16[2]))
    // bg was default (BG); after inverse it becomes foreground
    expect(toRgb(span.style.color)).toBe(toRgb(BG))
  })
})

describe('renderRow — text attributes', () => {
  it('bold → font-weight:bold', () => {
    const line = makeLine([{ ch: 'B', bold: true }])
    const row = renderRow(line, 1, makeNullCell(), null)
    expect((row.children[0] as HTMLSpanElement).style.fontWeight).toBe('bold')
  })

  it('italic → font-style:italic', () => {
    const line = makeLine([{ ch: 'I', italic: true }])
    const row = renderRow(line, 1, makeNullCell(), null)
    expect((row.children[0] as HTMLSpanElement).style.fontStyle).toBe('italic')
  })

  it('underline → text-decoration:underline', () => {
    const line = makeLine([{ ch: 'U', underline: true }])
    const row = renderRow(line, 1, makeNullCell(), null)
    expect((row.children[0] as HTMLSpanElement).style.textDecoration).toContain('underline')
  })

  it('strikethrough → text-decoration:line-through', () => {
    const line = makeLine([{ ch: 'S', strikethrough: true }])
    const row = renderRow(line, 1, makeNullCell(), null)
    expect((row.children[0] as HTMLSpanElement).style.textDecoration).toContain('line-through')
  })

  it('underline + strikethrough → both in text-decoration', () => {
    const line = makeLine([{ ch: 'X', underline: true, strikethrough: true }])
    const row = renderRow(line, 1, makeNullCell(), null)
    const td = (row.children[0] as HTMLSpanElement).style.textDecoration
    expect(td).toContain('underline')
    expect(td).toContain('line-through')
  })

  it('dim → opacity:0.6', () => {
    const line = makeLine([{ ch: 'D', dim: true }])
    const row = renderRow(line, 1, makeNullCell(), null)
    expect((row.children[0] as HTMLSpanElement).style.opacity).toBe('0.6')
  })

  it('invisible → opacity:0', () => {
    const line = makeLine([{ ch: 'V', invisible: true }])
    const row = renderRow(line, 1, makeNullCell(), null)
    expect((row.children[0] as HTMLSpanElement).style.opacity).toBe('0')
  })
})

describe('renderRow — cursor', () => {
  it('cursor cell gets cursor background colour', () => {
    const line = makeLine([{ ch: 'A' }, { ch: 'B' }, { ch: 'C' }])
    const row = renderRow(line, 3, makeNullCell(), 1) // cursor at column 1
    // Find the span containing the cursor. It should have the cursor bg.
    const spans = Array.from(row.children) as HTMLSpanElement[]
    const cursorSpan = spans.find(s => toRgb(s.style.backgroundColor) === toRgb(CURSOR_BG))
    expect(cursorSpan).toBeDefined()
    expect(cursorSpan!.textContent).toBe('B')
  })

  it('cursor cell text colour uses cell bg (default → terminal bg)', () => {
    const line = makeLine([{ ch: 'X' }])
    const row = renderRow(line, 1, makeNullCell(), 0)
    const span = row.children[0] as HTMLSpanElement
    expect(toRgb(span.style.color)).toBe(toRgb(BG))
    expect(toRgb(span.style.backgroundColor)).toBe(toRgb(CURSOR_BG))
  })

  it('cursor at null → no cursor styling on any span', () => {
    const line = makeLine([{ ch: 'A' }, { ch: 'B' }])
    const row = renderRow(line, 2, makeNullCell(), null)
    const spans = Array.from(row.children) as HTMLSpanElement[]
    const hasCursor = spans.some(s => toRgb(s.style.backgroundColor) === toRgb(CURSOR_BG))
    expect(hasCursor).toBe(false)
  })

  it('chars before cursor share a span; cursor is isolated; chars after start a new span', () => {
    const line = makeLine([{ ch: 'A' }, { ch: '|' }, { ch: 'B' }])
    const row = renderRow(line, 3, makeNullCell(), 1)
    // There should be at least 3 spans: pre-cursor, cursor, post-cursor
    expect(row.children.length).toBeGreaterThanOrEqual(3)
    const spans = Array.from(row.children) as HTMLSpanElement[]
    const cursorIdx = spans.findIndex(s => toRgb(s.style.backgroundColor) === toRgb(CURSOR_BG))
    expect(cursorIdx).toBeGreaterThan(-1)
    expect(spans[cursorIdx].textContent).toBe('|')
  })
})

describe('renderRow — span batching', () => {
  it('runs of identical style share a single span', () => {
    const line = makeLine([
      { ch: 'H', fgPalette: true, fgColor: 1 },
      { ch: 'i', fgPalette: true, fgColor: 1 },
    ])
    const row = renderRow(line, 2, makeNullCell(), null)
    expect(row.children).toHaveLength(1)
    expect(row.textContent).toBe('Hi')
  })

  it('style change creates a new span', () => {
    const line = makeLine([
      { ch: 'R', fgPalette: true, fgColor: 1 },
      { ch: 'G', fgPalette: true, fgColor: 2 },
    ])
    const row = renderRow(line, 2, makeNullCell(), null)
    expect(row.children).toHaveLength(2)
    expect((row.children[0] as HTMLSpanElement).textContent).toBe('R')
    expect((row.children[1] as HTMLSpanElement).textContent).toBe('G')
  })
})

// ── Headless xterm integration ────────────────────────────────────────────────
// These tests verify that the headless Terminal (no open() call) correctly
// processes writes and fires onWriteParsed — the foundation of our renderer.

function writeAndFlush(term: Terminal, data: string): Promise<void> {
  return new Promise(resolve => {
    const d = term.onWriteParsed(() => { d.dispose(); resolve() })
    term.write(data)
    vi.runAllTimers()
  })
}

describe('headless Terminal — onWriteParsed', () => {
  let term: Terminal

  beforeEach(() => {
    vi.useFakeTimers()
    term = new Terminal({ cols: 80, rows: 24, allowProposedApi: true })
  })

  afterEach(() => {
    term.dispose()
    vi.useRealTimers()
  })

  it('fires onWriteParsed after write', async () => {
    let fired = false
    term.onWriteParsed(() => { fired = true })
    term.write('Hello')
    expect(fired).toBe(false) // not synchronous
    vi.runAllTimers()
    expect(fired).toBe(true)
  })

  it('does not fire before write', () => {
    let fired = false
    term.onWriteParsed(() => { fired = true })
    vi.runAllTimers() // timers without a write
    expect(fired).toBe(false)
  })

  it('fires once per write call', async () => {
    let count = 0
    term.onWriteParsed(() => count++)
    term.write('a')
    vi.runAllTimers()
    expect(count).toBe(1)
    term.write('b')
    vi.runAllTimers()
    expect(count).toBe(2)
  })
})

describe('headless Terminal — buffer content after write', () => {
  let term: Terminal

  beforeEach(() => {
    vi.useFakeTimers()
    term = new Terminal({ cols: 80, rows: 24, allowProposedApi: true })
  })

  afterEach(() => {
    term.dispose()
    vi.useRealTimers()
  })

  it('single character appears at cursor origin', async () => {
    await writeAndFlush(term, 'A')
    const line = term.buffer.active.getLine(0)!
    const cell = term.buffer.active.getNullCell()
    line.getCell(0, cell)
    expect(cell.getChars()).toBe('A')
  })

  it('string of characters populates columns', async () => {
    await writeAndFlush(term, 'Hello')
    const line = term.buffer.active.getLine(0)!
    const cell = term.buffer.active.getNullCell()
    const chars = ['H', 'e', 'l', 'l', 'o']
    for (let i = 0; i < chars.length; i++) {
      line.getCell(i, cell)
      expect(cell.getChars()).toBe(chars[i])
    }
  })

  it('cursor advances after each character', async () => {
    await writeAndFlush(term, 'Hi')
    expect(term.buffer.active.cursorX).toBe(2)
    expect(term.buffer.active.cursorY).toBe(0)
  })

  it('newline moves cursor to next row', async () => {
    await writeAndFlush(term, 'line1\r\nline2')
    expect(term.buffer.active.cursorY).toBe(1)
    const line1 = term.buffer.active.getLine(0)!
    const cell = term.buffer.active.getNullCell()
    line1.getCell(0, cell)
    expect(cell.getChars()).toBe('l')
  })

  it('ANSI colour sequence sets palette colour mode on cell', async () => {
    await writeAndFlush(term, '\x1b[31mR\x1b[m') // ESC[31m = red fg
    const line = term.buffer.active.getLine(0)!
    const cell = term.buffer.active.getNullCell()
    line.getCell(0, cell)
    expect(cell.getChars()).toBe('R')
    expect(cell.isFgPalette()).toBe(true)
    expect(cell.getFgColor()).toBe(1) // ANSI colour 1 = red
  })

  it('SGR bold sets bold attribute on cell', async () => {
    await writeAndFlush(term, '\x1b[1mB\x1b[m')
    const line = term.buffer.active.getLine(0)!
    const cell = term.buffer.active.getNullCell()
    line.getCell(0, cell)
    expect(cell.getChars()).toBe('B')
    expect(cell.isBold()).toBeTruthy()
  })

  it('SGR reset clears colour attributes', async () => {
    await writeAndFlush(term, '\x1b[31mR\x1b[mX')
    const line = term.buffer.active.getLine(0)!
    const cell = term.buffer.active.getNullCell()
    line.getCell(1, cell) // 'X' at column 1
    expect(cell.getChars()).toBe('X')
    expect(cell.isFgDefault()).toBe(true)
  })

  it('viewportY is 0 for a fresh terminal', () => {
    expect(term.buffer.active.viewportY).toBe(0)
  })

  it('viewportY advances when content scrolls off screen', async () => {
    // Write enough lines to push content off the top
    const lines = Array.from({ length: 30 }, (_, i) => `line${i}`).join('\r\n')
    await writeAndFlush(term, lines)
    expect(term.buffer.active.viewportY).toBeGreaterThan(0)
  })

  it('buffer length equals viewportY + rows when scrolled', async () => {
    const lines = Array.from({ length: 30 }, (_, i) => `l${i}`).join('\r\n')
    await writeAndFlush(term, lines)
    const buf = term.buffer.active
    expect(buf.length).toBe(buf.viewportY + term.rows)
  })

  it('getNullCell returns a mutable cell object', () => {
    const cell = term.buffer.active.getNullCell()
    expect(cell).toBeDefined()
    expect(typeof cell.getChars).toBe('function')
    expect(typeof cell.getWidth).toBe('function')
  })

  it('reset clears buffer content', async () => {
    await writeAndFlush(term, 'Hello')
    term.reset()
    // After reset the cursor should be at origin
    expect(term.buffer.active.cursorX).toBe(0)
    expect(term.buffer.active.cursorY).toBe(0)
    const cell = term.buffer.active.getNullCell()
    term.buffer.active.getLine(0)?.getCell(0, cell)
    expect(cell.getChars()).toBe('')
  })

  it('alt-screen snapshot: CSI J handler reads pre-clear buffer content', async () => {
    // This test proves the timing contract our history-snapshot feature relies on:
    // a registerCsiHandler callback fires BEFORE xterm executes the sequence, so
    // reading terminal.buffer.active inside the handler gives the pre-clear state.
    // xterm's registerCsiHandler callback signature: (params: (number | number[])[]) => boolean
    let capturedChar = ''
    ;(term as unknown as { parser: { registerCsiHandler(id: object, cb: (p: (number | number[])[]) => boolean): void } })
      .parser.registerCsiHandler({ final: 'J' }, (params) => {
        const p = (params[0] as number) ?? 0
        if (p === 2 && term.buffer.active.type === 'alternate') {
          const cell = term.buffer.active.getNullCell()
          term.buffer.active.getLine(0)?.getCell(0, cell)
          capturedChar = cell.getChars()
        }
        return false // fall through to xterm's default handler
      })

    await writeAndFlush(term, '\x1b[?1049h') // enter alt screen
    await writeAndFlush(term, 'Q')            // write 'Q' at origin
    await writeAndFlush(term, '\x1b[2J')      // erase display — handler fires first

    expect(capturedChar).toBe('Q')  // handler saw pre-clear content

    // Verify xterm DID clear after our handler returned false
    const cellAfter = term.buffer.active.getNullCell()
    term.buffer.active.getLine(0)?.getCell(0, cellAfter)
    expect(cellAfter.getChars()).toBe('')
  })
})

describe('headless Terminal — renderRow round-trip', () => {
  let term: Terminal

  beforeEach(() => {
    vi.useFakeTimers()
    term = new Terminal({ cols: 80, rows: 24, allowProposedApi: true })
  })

  afterEach(() => {
    term.dispose()
    vi.useRealTimers()
  })

  it('plain text renders to readable DOM', async () => {
    await writeAndFlush(term, 'Hello, world!')
    const buf = term.buffer.active
    const cell = buf.getNullCell()
    const row = renderRow(buf.getLine(buf.viewportY), term.cols, cell, null)
    expect(row.textContent?.startsWith('Hello, world!')).toBe(true)
  })

  it('ANSI red text renders with red colour span', async () => {
    await writeAndFlush(term, '\x1b[31mERROR\x1b[m')
    const buf = term.buffer.active
    const cell = buf.getNullCell()
    const row = renderRow(buf.getLine(buf.viewportY), term.cols, cell, null)
    const spans = Array.from(row.children) as HTMLSpanElement[]
    const redSpan = spans.find(s => s.textContent === 'ERROR')
    expect(redSpan).toBeDefined()
    expect(toRgb(redSpan!.style.color)).toBe(toRgb(ANSI16[1])) // red
  })

  it('cursor position renders cursor highlight on correct character', async () => {
    await writeAndFlush(term, 'ABC')
    const buf = term.buffer.active
    const cell = buf.getNullCell()
    const cursorAbsY = buf.viewportY + buf.cursorY
    const row = renderRow(buf.getLine(cursorAbsY), term.cols, cell, buf.cursorX)
    const spans = Array.from(row.children) as HTMLSpanElement[]
    const cursorSpan = spans.find(s => toRgb(s.style.backgroundColor) === toRgb(CURSOR_BG))
    expect(cursorSpan).toBeDefined()
  })
})

// ── IME composition diff ──────────────────────────────────────────────────────
// computeCompositionDiff drives what gets sent to the PTY on every insertCompositionText
// event.  Wrong logic here causes garbled text when autocomplete selects a word.

describe('computeCompositionDiff', () => {
  it('extension — sends only the new suffix, no backspaces', () => {
    expect(computeCompositionDiff('hel', 'hello')).toEqual({ backspaces: 0, insert: 'lo' })
  })
  it('fresh start — sends full word, no backspaces', () => {
    expect(computeCompositionDiff('', 'hello')).toEqual({ backspaces: 0, insert: 'hello' })
  })
  it('no change — no ops', () => {
    expect(computeCompositionDiff('hello', 'hello')).toEqual({ backspaces: 0, insert: '' })
  })
  it('autocomplete extends current word (wor → world)', () => {
    expect(computeCompositionDiff('wor', 'world')).toEqual({ backspaces: 0, insert: 'ld' })
  })
  it('regression: autocomplete replaces with different word — backspace old, insert new', () => {
    // Before fix: slice('wor'.length) = 'lo' was sent, leaving terminal with 'worlo'.
    // compositionEnd then compared 'hello' === 'hello' so no correction was sent.
    expect(computeCompositionDiff('wor', 'hello')).toEqual({ backspaces: 3, insert: 'hello' })
  })
  it('autocomplete corrects spelling (helo → hello)', () => {
    expect(computeCompositionDiff('helo', 'hello')).toEqual({ backspaces: 4, insert: 'hello' })
  })
  it('deletion — backspace all, reinsert shorter word', () => {
    expect(computeCompositionDiff('hello', 'hell')).toEqual({ backspaces: 5, insert: 'hell' })
  })
  it('full replacement — backspace all, insert new', () => {
    expect(computeCompositionDiff('abc', 'xyz')).toEqual({ backspaces: 3, insert: 'xyz' })
  })
  it('keyboard temporarily clears textarea (empty) then refills — handled in two steps', () => {
    // Step 1: keyboard clears to ''
    expect(computeCompositionDiff('hel', '')).toEqual({ backspaces: 3, insert: '' })
    // Step 2: keyboard fills with new word (prevText is now '' after step 1 updates it)
    expect(computeCompositionDiff('', 'hello')).toEqual({ backspaces: 0, insert: 'hello' })
  })
})

// ── Scroll stick-mode helpers ─────────────────────────────────────────────────
// These helpers drive whether renderNow() auto-scrolls to the bottom.
// Tests document the expected contract so regressions are caught before deploy.

describe('isAtScrollBottom', () => {
  it('is true at dist=0 (exactly at bottom)', () => {
    expect(isAtScrollBottom(0)).toBe(true)
  })
  it('is true at dist=1 (sub-pixel rounding tolerance)', () => {
    expect(isAtScrollBottom(1)).toBe(true)
  })
  it('is false at dist=2 — any meaningful scroll up exits stick', () => {
    expect(isAtScrollBottom(2)).toBe(false)
  })
  it('is false when scrolled up less than one row (regression: was true for dist<42)', () => {
    expect(isAtScrollBottom(10)).toBe(false)
    expect(isAtScrollBottom(14)).toBe(false)
  })
  it('is false when scrolled far up', () => {
    expect(isAtScrollBottom(500)).toBe(false)
  })
})

describe('wheelScrollsUp', () => {
  it('is true for negative deltaY (user scrolls up)', () => {
    expect(wheelScrollsUp(-100)).toBe(true)
    expect(wheelScrollsUp(-1)).toBe(true)
  })
  it('is false for positive deltaY (scrolling down)', () => {
    expect(wheelScrollsUp(100)).toBe(false)
  })
  it('is false for zero delta', () => {
    expect(wheelScrollsUp(0)).toBe(false)
  })
})

describe('touchScrollsUp', () => {
  it('is true when finger moves down > 10px (reveals older content)', () => {
    expect(touchScrollsUp(11)).toBe(true)
    expect(touchScrollsUp(50)).toBe(true)
  })
  it('is false at or below the 10px threshold (suppresses micro-movements)', () => {
    expect(touchScrollsUp(10)).toBe(false)
    expect(touchScrollsUp(5)).toBe(false)
    expect(touchScrollsUp(0)).toBe(false)
  })
  it('is false when finger moves up (scrolling down through new output)', () => {
    expect(touchScrollsUp(-50)).toBe(false)
  })
  it('regression: per-event incremental deltas (2-4px) must NOT trigger — caller must pass cumulative delta from touchstart', () => {
    // touchmove fires many times per swipe; each per-event delta is only 2-4 px.
    // If the handler updates its reference point each event (incremental), these
    // tiny deltas never exceed 10 px and stickRef never goes false — scroll
    // appears broken.  The handler must accumulate from touchstart instead.
    expect(touchScrollsUp(2)).toBe(false)
    expect(touchScrollsUp(3)).toBe(false)
    expect(touchScrollsUp(4)).toBe(false)
  })
})

describe('computeKeyboardAdjustment', () => {
  it('returns 0 on desktop regardless of rawDelta (keyboard path must not run on desktop)', () => {
    expect(computeKeyboardAdjustment(false, 0)).toBe(0)
    expect(computeKeyboardAdjustment(false, 300)).toBe(0)
    expect(computeKeyboardAdjustment(false, 50)).toBe(0)
  })
  it('returns the raw delta on mobile (keyboard height)', () => {
    expect(computeKeyboardAdjustment(true, 300)).toBe(300)
    expect(computeKeyboardAdjustment(true, 250)).toBe(250)
  })
  it('clamps negative rawDelta to 0 on mobile', () => {
    expect(computeKeyboardAdjustment(true, -10)).toBe(0)
  })
  it('returns 0 when keyboard is closed (rawDelta = 0)', () => {
    expect(computeKeyboardAdjustment(true, 0)).toBe(0)
  })
})

describe('sessionViewportBox', () => {
  it('returns null on desktop — the fixed container must keep inset:0', () => {
    expect(sessionViewportBox(false, 800, 500, 0)).toBeNull()
  })
  it('returns null on mobile with the keyboard closed (vv fills the layout viewport)', () => {
    expect(sessionViewportBox(true, 800, 800, 0)).toBeNull()
  })
  it('returns the visual-viewport box while the keyboard is open', () => {
    expect(sessionViewportBox(true, 800, 500, 0)).toEqual({ top: 0, height: 500 })
  })
  it('tracks vv.offsetTop when the browser scrolls the visual viewport', () => {
    expect(sessionViewportBox(true, 800, 500, 60)).toEqual({ top: 60, height: 500 })
  })
  it('ignores sub-threshold deltas (URL-bar show/hide is not a keyboard)', () => {
    // computeKeyboardAdjustment semantics: tiny/negative deltas are not a keyboard
    expect(sessionViewportBox(true, 800, 801, 0)).toBeNull()
  })
})

describe('shouldRenderPrimaryView', () => {
  it('returns false when primaryVpy is 0 — no scrollback means primary viewport is blank', () => {
    // When Claude enters alt screen immediately without writing to primary buffer,
    // primaryVpy stays at 0 and the primary viewport rows are all empty.
    // Rendering them creates scroll height with no visible text.
    expect(shouldRenderPrimaryView(0)).toBe(false)
  })
  it('returns true when there is scrollback (primaryVpy > 0)', () => {
    expect(shouldRenderPrimaryView(1)).toBe(true)
    expect(shouldRenderPrimaryView(50)).toBe(true)
    expect(shouldRenderPrimaryView(500)).toBe(true)
  })
})

// gridCount derives terminal cols/rows from the scroll container. The container
// uses box-sizing:border-box with horizontal/vertical padding, so clientWidth/
// clientHeight INCLUDE that padding while text only flows inside the content box.
// gridCount must subtract the padding before dividing, or the rightmost (and
// bottommost) 1-2 cells render past the content box and get clipped by overflow.
describe('gridCount', () => {
  it('subtracts the container padding before dividing', () => {
    // content box = 1000 - 12 = 988; 988 / 7 = 141.1 -> 141.
    // Without the padding subtraction this would be floor(1000/7) = 142 — the
    // off-by-one-to-two columns that get clipped on the right edge.
    expect(gridCount(1000, 7, 12, 20)).toBe(141)
  })

  it('handles a fractional character width', () => {
    // content box = 988 - 12 = 976; 976 / 7.5 = 130.1 -> 130.
    expect(gridCount(988, 7.5, 12, 20)).toBe(130)
  })

  it('clamps to the minimum for very small containers', () => {
    expect(gridCount(50, 7, 12, 20)).toBe(20)
  })

  it('equals a plain floor when there is no padding', () => {
    expect(gridCount(700, 7, 0, 5)).toBe(100)
  })
})
