import { renderInFrame, type MermaidMeasured } from './mermaidFrame'
import { sanitizeSvg, validateMermaid } from './policy'
import { mermaidTheme, readVisualTheme, type VisualTheme } from './theme'
import type { RenderedVisual, VisualFit } from './render'

// BLG-467: in a tab or full screen (fit.fill), a diagram fills the space it's given rather than
// just its own drawn size — but only up to this multiple of that size, so filling a very large
// pane never blows its text up past a readable scale (a chat message's own 64rem cap, visual.css,
// already keeps this in bounds there without any of this; fit.fill is never set for that case).
const MAX_FILL_SCALE = 2

const CONFIG = {
  startOnLoad: false,
  securityLevel: 'strict' as const,
  suppressErrorRendering: true,
  maxTextSize: 16_384,
  maxEdges: 500,
  htmlLabels: false,
  flowchart: { htmlLabels: false },
  // Journey defaults to foreignObject labels even with global htmlLabels disabled.
  journey: { textPlacement: 'tspan' },
  fontFamily: 'sans-serif',
  secure: ['secure', 'securityLevel', 'startOnLoad', 'maxTextSize', 'maxEdges',
    'suppressErrorRendering', 'htmlLabels', 'flowchart', 'journey', 'themeCSS', 'fontFamily', 'theme', 'themeVariables'],
}

// One diagram at a time: the frame keeps Mermaid's global configuration (the theme) between
// initialize and render, and a hostile set of diagrams queues rather than piles up.
let pending: Promise<unknown> = Promise.resolve()
export function renderMermaid(source: string, container: HTMLElement, theme = readVisualTheme(container), fit?: VisualFit): Promise<RenderedVisual> {
  // Mermaid gets exactly the string the validator approved (line endings and spaces normalized).
  let drawn: string
  try { drawn = validateMermaid(source) } catch (error) { return Promise.reject(error) }
  const job = pending.then(() => drawMermaid(drawn, container, theme, fit))
  pending = job.catch(() => {})
  return job
}

/** The bounded side a diagram's longer edge is scaled to (mermaid.ts's long-standing 4096px cap,
 *  kept here so both the viewBox and the frame-measured fallback use the same rule). */
function boundedSize(width: number, height: number): {width: number; height: number} {
  const scale = Math.min(1, 4096 / Math.max(width, height))
  return {width: width * scale, height: height * scale}
}

async function drawMermaid(source: string, container: HTMLElement, theme: VisualTheme, fit?: VisualFit): Promise<RenderedVisual> {
  const drawn = validateMermaid(source)
  // Mermaid lays the diagram out in a sandboxed frame with no network (mermaidFrame.ts); only
  // its SVG text (and, when it drew no viewBox, its frame-measured box) comes back — the SVG is
  // sanitized and shown as an inert image.
  let svg: string, measured: MermaidMeasured | null
  try {
    ;({svg, measured} = await renderInFrame(drawn, {...CONFIG, ...mermaidTheme(theme), fontFamily: theme.fontFamily}))
  } catch (error) {
    throw new Error(mermaidReason(error), { cause: error })
  }
  const image = document.createElement('img')
  image.alt = 'Mermaid diagram'
  image.className = 'visual-diagram'
  let clean: string
  try {
    clean = sanitizeSvg(svg)
  } catch (error) {
    throw new Error(`the drawn diagram was refused (${error instanceof Error ? error.message : 'unsupported SVG'})`, { cause: error })
  }
  const safe = new DOMParser().parseFromString(clean, 'image/svg+xml').documentElement
  const failed = safe.querySelector('text.error-text')
  if (failed) throw new Error(mermaidReason(failed.textContent || 'Mermaid could not draw it'))
  const bounds = safe.getAttribute('viewBox')?.trim().split(/[\s,]+/).map(Number)
  // RenderedVisual.width/height: from the viewBox when there is one; otherwise from the frame's
  // own measured bounding box (there is nothing to measure on the main thread — a detached,
  // unlaid-out DOM answers getBBox()/getBoundingClientRect() with 0, mermaidFrame.ts).
  let size: {width: number; height: number} | null = null
  if (bounds?.length === 4 && bounds.every(Number.isFinite) && bounds[2] > 0 && bounds[3] > 0) size = boundedSize(bounds[2], bounds[3])
  else if (measured) size = boundedSize(measured.width, measured.height)
  if (size) {
    // A percentage SVG width gives an <img> the browser's 300px fallback,
    // shrinking labels even in a wide message. Keep its natural aspect and
    // bound decoded image dimensions; CSS still scales it down to fit.
    safe.setAttribute('width', String(size.width))
    safe.setAttribute('height', String(size.height))
  }
  const finalSvg = new XMLSerializer().serializeToString(safe)
  image.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(finalSvg)}`
  // Decode before displaying: CSP/image errors use the same source fallback as
  // parser/module failures, rather than leaving a broken image in the transcript.
  try {
    await image.decode()
  } catch {
    throw new Error('the browser could not display the drawn diagram')
  }
  container.replaceChildren(image)
  // Last resort when neither a viewBox nor a frame measurement was available: the browser's own
  // decoded size (its SVG-intrinsic default when the drawing gave it nothing to go on).
  const {width, height} = size ?? {width: image.naturalWidth, height: image.naturalHeight}
  // BLG-467: filling (a tab or full screen) sizes the displayed image explicitly — inline style,
  // so it wins over any stylesheet rule regardless of selector specificity — scaled up to fill
  // the available width, bounded by the available height and by MAX_FILL_SCALE so a small
  // diagram's text never grows past a readable size in a very large pane.
  if (fit?.fill && width > 0 && height > 0) {
    const widthScale = fit.available.width > 0 ? fit.available.width / width : Infinity
    const heightScale = fit.available.height > 0 ? fit.available.height / height : Infinity
    const scale = Math.min(widthScale, heightScale, MAX_FILL_SCALE)
    if (Number.isFinite(scale) && scale > 0) {
      image.style.width = `${width * scale}px`
      image.style.height = 'auto'
    }
  }
  return {svg: finalSvg, width, height, dispose: () => image.remove()}
}

/** Mermaid's own error, shortened to one line: "Mermaid: Parse error on line 3: Expecting …, got
 *  'LINK'". Its messages put the offending source and a caret on lines of their own. */
export function mermaidReason(error: unknown): string {
  const text = error instanceof Error ? error.message : typeof error === 'string' ? error : ''
  const lines = text.split('\n').map(line => line.trim()).filter(Boolean)
  if (!lines.length) return 'Mermaid could not draw it'
  const expecting = lines.find(line => /^Expecting\b/.test(line))
  const reason = [lines[0].replace(/[:.]$/, ''), expecting].filter(Boolean).join(': ')
  return `Mermaid: ${reason.length > 240 ? reason.slice(0, 239) + '…' : reason}`
}
