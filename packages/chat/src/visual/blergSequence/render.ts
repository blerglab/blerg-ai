// BLG-468 §5 (sequence): ties parse → layout → draw → attach together, mirroring
// blergFlow/render.ts's own shape exactly: in chat, a diagram never draws larger than its own
// natural size (text at most 1x) — it shrinks to the message's own width when that's narrower
// than natural, but never grows past it. A two-participant sequence diagram in a very wide chat
// message is exactly the case that surfaced this: scaled to fill the message unconditionally, its
// text blew up several times over and ran off the bottom. Growing past natural size (capped at
// MAX_FILL_SCALE, bounded by the available height) is `fit.fill`'s own affordance — a tab or full
// screen — never chat's. render.ts's shared dispatcher lazily imports this module only after a
// flowchart parse attempt has already returned null.
import { parseSequence } from './parse'
import { layoutSequence } from './layout'
import { drawSequence } from './draw'
import { wireSequenceInteractivity } from './accessibility'
import { sanitizeSvg } from '../policy'
import type { VisualTheme } from '../theme'
import type { RenderedVisual } from '../render'
import type { VisualFit } from '../chartSize'
import type { SeqGraph } from './ast'

const MAX_FILL_SCALE = 2

function chatSize(natural: { width: number; height: number }, availableWidth: number): { width: number; height: number } {
  const width = Math.max(1, Math.min(natural.width, availableWidth || natural.width))
  return { width, height: Math.max(1, Math.round(natural.height * width / natural.width)) }
}

/** `null`: not our declared subset — the caller falls back to Mermaid, same contract as
 *  `tryRenderFlowchart`. `onChipOpen` resolves a participant whose label is a card reference. */
export function tryRenderSequence(source: string, container: HTMLElement, theme: VisualTheme, fit: VisualFit, onChipOpen?: (key: string) => void): RenderedVisual | null {
  const graph: SeqGraph | null = parseSequence(source)
  if (!graph) return null
  const natural = layoutSequence(graph, theme.fontSize)

  let layoutToDraw = natural
  let themeToDraw = theme
  if (fit.fill) {
    const availableWidth = Math.max(1, fit.available.width || natural.width)
    const availableHeight = fit.available.height > 0 ? fit.available.height : Infinity
    let scale = Math.min(availableWidth / natural.width, availableHeight / natural.height, MAX_FILL_SCALE)
    for (let i = 0; i < 4 && Number.isFinite(scale) && scale > 0 && Math.abs(scale - 1) > 0.02; i++) {
      themeToDraw = { ...theme, fontSize: theme.fontSize * scale }
      layoutToDraw = layoutSequence(graph, themeToDraw.fontSize)
      const next = Math.min(availableWidth / layoutToDraw.width, availableHeight / layoutToDraw.height)
      if (Math.abs(next - 1) < 0.02) break
      scale = Math.min(scale * next, MAX_FILL_SCALE)
    }
  }

  const drawn = drawSequence(graph, layoutToDraw, themeToDraw)
  const svg = sanitizeSvg(drawn.svg)
  const root = new DOMParser().parseFromString(svg, 'image/svg+xml').documentElement as unknown as SVGSVGElement
  root.setAttribute('data-diagram-renderer', 'blerg-sequence')
  root.setAttribute('role', 'group')
  root.setAttribute('aria-label', 'Sequence diagram')
  if (fit.fill) {
    const availableWidth = Math.max(1, fit.available.width || drawn.width)
    const availableHeight = fit.available.height > 0 ? fit.available.height : Infinity
    const clamp = Math.min(1, availableWidth / drawn.width, availableHeight / drawn.height)
    root.setAttribute('width', String(Math.max(1, Math.round(drawn.width * clamp))))
    root.setAttribute('height', String(Math.max(1, Math.round(drawn.height * clamp))))
    root.style.position = 'absolute'
    root.style.top = '50%'
    root.style.left = '50%'
    root.style.transform = 'translate(-50%, -50%)'
  } else {
    const size = chatSize({ width: drawn.width, height: drawn.height }, fit.available.width)
    root.setAttribute('width', String(size.width))
    root.setAttribute('height', String(size.height))
  }
  const live = wireSequenceInteractivity(root, onChipOpen)
  container.replaceChildren(root, live)
  return { svg, width: drawn.width, height: drawn.height, dispose: () => container.replaceChildren() }
}
