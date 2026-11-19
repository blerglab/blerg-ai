// BLG-468 §2: our own small parser for the declared flowchart subset. `parseFlowchart` is also
// the classifier (design doc §2/§5): it returns `null` — never throws — for anything outside the
// subset, the first line that isn't `flowchart`/`graph`, or a label it can't represent exactly
// (label.ts). The caller falls back to Mermaid on `null`. Deliberately strict rather than
// permissive: most constructs outside the declared grammar (classDef, style, linkStyle, class,
// direction, node-list fan-out, bidirectional arrows, Mermaid's own `@{…}` shape metadata) are
// never special-cased here — they simply don't tokenize as a known production, and the chain
// parser below returns null the same way an actual syntax error would. That is intentional: a
// line only ever succeeds by being one of the productions below, never by surviving a check that
// happens not to fire.
import type { ArrowStyle, FlowDirection, FlowEdge, FlowGraph, FlowNode, FlowShape, FlowSubgraph } from './ast'
import { decodeLabel } from './label'

function normalizeDirection(token: string): FlowDirection | null {
  const upper = token.toUpperCase()
  if (upper === 'TD') return 'TB'
  return upper === 'TB' || upper === 'LR' || upper === 'RL' || upper === 'BT' ? upper : null
}

const SHAPES: ReadonlyArray<{ open: string; close: string; shape: FlowShape }> = [
  { open: '([', close: '])', shape: 'stadium' },
  { open: '((', close: '))', shape: 'circle' },
  { open: '[[', close: ']]', shape: 'subroutine' },
  { open: '[', close: ']', shape: 'rect' },
  { open: '(', close: ')', shape: 'round' },
  { open: '{', close: '}', shape: 'diamond' },
]

// Excludes every character that is structural somewhere in the grammar (shape delimiters,
// quotes, arrow punctuation, `&` fan-out, `#`/`@` metadata, `:`/`;`/`,`). An id outside this set
// simply fails to tokenize — safe, not a gap: see the file header.
const ID_CHAR = /[^\s\-<>=.[\](){}|"'`&:;,#@]/u

interface Cursor { s: string; i: number }
function skipSpace(c: Cursor): void { while (c.i < c.s.length && /\s/.test(c.s[c.i])) c.i++ }

function readId(c: Cursor): string | null {
  const start = c.i
  while (c.i < c.s.length && ID_CHAR.test(c.s[c.i])) c.i++
  return c.i > start ? c.s.slice(start, c.i) : null
}

// Shape-like delimiters that are real Mermaid syntax but not one of the declared six (§1):
// recognized explicitly so the generic '[' handler below never silently swallows a cylinder as a
// plain rect — a shape we don't draw must fail the whole node ref, never be mistaken for one we do.
const UNSUPPORTED_SHAPE_OPEN = ['[(', '>']

/** `null`: no shape delimiter at this position at all (a bare reference). `false`: a recognized-
 *  but-unsupported shape delimiter — the caller must fail the whole node ref, not treat it as
 *  bare. Otherwise the matched shape and its decoded label. */
function readShapeLabel(c: Cursor): { shape: FlowShape; label: string } | null | false {
  if (UNSUPPORTED_SHAPE_OPEN.some(open => c.s.startsWith(open, c.i))) return false
  for (const { open, close, shape } of SHAPES) {
    if (!c.s.startsWith(open, c.i)) continue
    const contentStart = c.i + open.length
    let raw: string, end: number
    if (c.s[contentStart] === '"') {
      const closeQuote = c.s.indexOf('"', contentStart + 1)
      if (closeQuote < 0) return false
      raw = c.s.slice(contentStart + 1, closeQuote)
      end = closeQuote + 1
    } else {
      const closeAt = c.s.indexOf(close, contentStart)
      if (closeAt < 0) return false
      raw = c.s.slice(contentStart, closeAt)
      end = closeAt
    }
    if (!c.s.startsWith(close, end)) return false
    c.i = end + close.length
    return { shape, label: raw.trim() }
  }
  return null
}

interface NodeRef { id: string; shape?: FlowShape; label?: string }
function readNodeRef(c: Cursor): NodeRef | null {
  const id = readId(c)
  if (id === null || id === 'end') return null
  const shaped = readShapeLabel(c)
  if (shaped === false) return null
  return shaped ? { id, shape: shaped.shape, label: shaped.label } : { id }
}

const PLAIN_ARROW: ReadonlyArray<{ token: string; style: ArrowStyle }> = [
  { token: '-.->', style: 'dotted' },
  { token: '==>', style: 'thick' },
  { token: '-->', style: 'solid' },
  { token: '---', style: 'line' },
]

interface ArrowToken { style: ArrowStyle; label: string | null }
function readArrow(c: Cursor): ArrowToken | null {
  // The open inline-label form (§1/§2): "-- text -->" or "-- text ---". Tried first since it
  // shares the plain line token's "---" prefix.
  const open = /^-- +(\S.*?) +(-->|---)/.exec(c.s.slice(c.i))
  if (open) {
    c.i += open[0].length
    return { style: open[2] === '-->' ? 'solid' : 'line', label: open[1] }
  }
  for (const { token, style } of PLAIN_ARROW) {
    if (!c.s.startsWith(token, c.i)) continue
    let end = c.i + token.length
    let label: string | null = null
    if (c.s[end] === '|') {
      const closePipe = c.s.indexOf('|', end + 1)
      if (closePipe < 0) return null
      label = c.s.slice(end + 1, closePipe)
      end = closePipe + 1
    }
    c.i = end
    return { style, label }
  }
  return null
}

function parseChain(line: string): { refs: NodeRef[]; arrows: ArrowToken[] } | null {
  const c: Cursor = { s: line, i: 0 }
  skipSpace(c)
  const first = readNodeRef(c)
  if (!first) return null
  const refs = [first]
  const arrows: ArrowToken[] = []
  skipSpace(c)
  while (c.i < c.s.length) {
    const arrow = readArrow(c)
    if (!arrow) return null
    arrows.push(arrow)
    skipSpace(c)
    const next = readNodeRef(c)
    if (!next) return null
    refs.push(next)
    skipSpace(c)
  }
  return { refs, arrows }
}

function skipFrontmatter(lines: readonly string[]): number {
  if (!/^\s*---\s*$/.test(lines[0] ?? '')) return 0
  const close = lines.findIndex((line, index) => index > 0 && /^-{3}\s*$/.test(line))
  return close < 0 ? -1 : close + 1
}

/** `null`: not our declared subset (or not a flowchart at all) — the caller falls back to
 *  Mermaid. Never throws: an agent's own diagram text is untrusted input, not a program bug. */
export function parseFlowchart(source: string): FlowGraph | null {
  const lines = source.split('\n')
  let i = skipFrontmatter(lines)
  if (i < 0) return null
  const isBlankOrComment = (line: string) => line.trim() === '' || /^\s*%%/.test(line)
  while (i < lines.length && isBlankOrComment(lines[i])) i++
  const header = /^\s*(flowchart|graph)\s+(TD|TB|LR|RL|BT)\s*$/i.exec(lines[i] ?? '')
  if (!header) return null
  const direction = normalizeDirection(header[2])
  if (!direction) return null
  i++

  const nodes = new Map<string, FlowNode>()
  const edges: FlowEdge[] = []
  const subgraphs: FlowSubgraph[] = []
  let currentSubgraph: string | null = null
  let inSubgraph = false

  function ensureNode(ref: NodeRef): boolean {
    let node = nodes.get(ref.id)
    if (!node) {
      node = { id: ref.id, label: ref.id, shape: 'rect' }
      nodes.set(ref.id, node)
    }
    if (ref.shape && ref.label !== undefined) {
      const decoded = decodeLabel(ref.label)
      if (decoded === null) return false
      node.shape = ref.shape
      node.label = decoded
    }
    if (currentSubgraph && node.subgraph === undefined) node.subgraph = currentSubgraph
    return true
  }

  for (; i < lines.length; i++) {
    const line = lines[i].trim()
    if (isBlankOrComment(line)) continue

    const subgraphHeader = /^subgraph\s+(.*)$/i.exec(line)
    if (subgraphHeader) {
      if (inSubgraph) return null // one level only (design doc §1)
      const rest = subgraphHeader[1].trim()
      const idLabel = /^([^\s[\]]+)\s*\[(.*)\]$/.exec(rest)
      const id = idLabel ? idLabel[1] : rest
      if (id === 'end' || id === '') return null
      const label = decodeLabel(idLabel ? idLabel[2] : rest)
      if (label === null) return null
      subgraphs.push({ id, label })
      currentSubgraph = id
      inSubgraph = true
      continue
    }
    if (/^end$/i.test(line)) {
      if (!inSubgraph) return null
      inSubgraph = false
      currentSubgraph = null
      continue
    }

    const chain = parseChain(line)
    if (!chain) return null
    for (const ref of chain.refs) if (!ensureNode(ref)) return null
    for (let k = 0; k < chain.arrows.length; k++) {
      const arrow = chain.arrows[k]
      let label: string | null = null
      if (arrow.label !== null) {
        label = decodeLabel(arrow.label)
        if (label === null) return null
      }
      edges.push({ from: chain.refs[k].id, to: chain.refs[k + 1].id, label, style: arrow.style })
    }
  }
  if (inSubgraph || nodes.size === 0) return null
  return { direction, nodes: [...nodes.values()], edges, subgraphs }
}
